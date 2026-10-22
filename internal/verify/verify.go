// Package verify is `shelf verify` (ruling E-58): read every page of every book
// the index knows, in full, and hold the bytes against the container's own
// integrity data — the recorded length and CRC-32 of each entry.
//
// The scan never does this. It reads central directories only (FR-IDX-002),
// so a book indexed `ok` can still have damaged pages, and page serving does
// not hash either. verify is the offline answer to "is any of my collection
// broken", and it is written to be safe to run beside a live server:
//
//   - the book list comes from index.db opened without writing (index.go),
//     read into memory, and closed before any container is opened;
//   - pages come from the containers through the same source.Factory the
//     server uses, not from the index's page rows;
//   - nothing in this package writes to the filesystem. `make lint`'s
//     check-readonly guard enforces that, test files included.
package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"shelf/internal/archive"
	"shelf/internal/config"
	"shelf/internal/openpool"
	"shelf/internal/source"
)

// Options configures Run.
type Options struct {
	// Config is the loaded configuration: roots, the data directory holding
	// index.db, and scan.workers.
	Config *config.Config
	// Roots limits the run to these root names. Empty means every enabled
	// root. A name that is not configured, or is disabled, is an error.
	Roots []string
	// Workers is how many books are read at once. Zero means
	// Config.Scan.Workers, which config.Load has already resolved from its own
	// 0 => min(8, max(2, NumCPU/2)) rule; a hand-built Config that leaves it 0
	// gets one worker.
	Workers int
	// Out receives the BROKEN lines and the SUMMARY line.
	Out io.Writer
	// Progress receives a few human-oriented lines; nil discards them.
	Progress io.Writer
	// Logger is handed to the source layer; nil means slog.Default().
	Logger *slog.Logger

	hooks hooks
}

// hooks are test seams in the worker path; nil fields do nothing.
type hooks struct {
	// book runs as a worker takes a book; the func it returns runs when the
	// worker is done with it.
	book func(source.Book) func()
	// page runs before each page of a book is read.
	page func(source.Book, int)
}

// Summary is the closing count of a run.
type Summary struct {
	Books          int // books whose container was opened and listed, or attempted
	Pages          int // pages whose bytes were read and checked
	DefectiveBooks int // books with at least one defect, book-level or page-level
	DefectivePages int // pages that failed their check
	Unverifiable   int // books with no integrity data to check against (dir, pdf, unsupported, encrypted), or with pages this build cannot open
	// UnverifiablePages are pages listed with integrity data that this build
	// could not open to check (an unsupported method, an encrypted entry).
	UnverifiablePages int
	// RootsNotChecked are selected roots that could not be opened; each is
	// named on an UNCHECKED line.
	RootsNotChecked int
}

// Run verifies the selected roots. It writes one tab-separated line per defect
// to opts.Out, then a SUMMARY line.
//
// The error is non-nil only when the run could not start — configuration,
// root selection, index.db — or was cancelled (it then wraps ctx.Err()).
// Defects are not errors: they are the output, and Summary counts them. A
// selected root that cannot be opened, or where not one of its indexed books
// exists (an unmounted volume, often an empty mount point), is not an error
// either and does not stop the run: every other root is verified to the end,
// and that root is named on an UNCHECKED line and counted in
// Summary.RootsNotChecked — never reported as damage.
func Run(ctx context.Context, opts Options) (Summary, error) {
	cfg := opts.Config
	if cfg == nil {
		return Summary{}, errors.New("verify: no configuration")
	}
	log := opts.Logger
	if log == nil {
		log = slog.Default()
	}
	progress := opts.Progress
	if progress == nil {
		progress = io.Discard
	}

	selected, err := selectRoots(cfg.Roots, opts.Roots)
	if err != nil {
		return Summary{}, err
	}

	indexPath := IndexPath(cfg.Storage.DataDir)
	loaded, err := LoadBooks(ctx, indexPath)
	if err != nil {
		return Summary{}, err
	}
	all, mode := loaded.Books, loaded.Mode
	if loaded.Warning != "" {
		fmt.Fprintf(progress, "verify: warning: %s\n", loaded.Warning)
	}

	// Wired as internal/app wires the server's sources (app.go step 6), less
	// the PDF renderer: a PDF has no integrity data to check, and without a
	// renderer every PDF book is ErrUnsupported — the same verdict a nopdf
	// build gives.
	roots, err := source.OpenRoots(ctx, selected, log)
	if err != nil {
		return Summary{}, fmt.Errorf("opening roots: %w", err)
	}
	defer roots.Close()
	v := &verifier{out: opts.Out, hooks: opts.hooks}
	inScope := make(map[string]bool, len(selected))
	for _, r := range selected {
		if _, ok := roots.Root(r.Name); ok {
			inScope[r.Name] = true
			continue
		}
		// OpenRoots keeps the reason to itself (it logs it); ask again.
		reason := rootProblem(r.Path)
		fmt.Fprintf(progress, "verify: root %q is not checked: %s\n", r.Name, reason)
		v.unchecked = append(v.unchecked, strings.Join([]string{"UNCHECKED", field(r.Name), field(reason)}, "\t")+"\n")
		v.sum.RootsNotChecked++
	}
	byRoot := map[string][]source.Book{}
	for _, b := range all {
		if inScope[b.RootName] {
			byRoot[b.RootName] = append(byRoot[b.RootName], b)
		}
	}
	// A volume that is not mounted often leaves its mount point behind as an
	// empty directory, which opens like any other root. Not one of the books
	// the index holds for it exists there, and reporting each as BROKEN would
	// call a missing mount a library's worth of damage. So such a root is not
	// checked, exactly like one that cannot be opened. Asking whether any
	// indexed book exists, rather than whether the directory is empty, also
	// covers a different volume mounted in its place.
	//
	// The trade-off: deleting every indexed book of a root looks exactly the
	// same, and is reported the same way — UNCHECKED and exit 2, never a clean
	// 0. Telling the two apart is a rescan's job, not this one's. A root that
	// still holds even one of its books is checked, and its missing books are
	// BROKEN as before.
	for _, r := range selected {
		rb := byRoot[r.Name]
		if len(rb) == 0 {
			continue
		}
		if ctx.Err() != nil {
			break
		}
		if fsys, ok := roots.Root(r.Name); ok && !anyBookExists(ctx, fsys, rb) {
			reason := fmt.Sprintf("none of the %d books indexed under %s exists there; is the volume mounted?", len(rb), r.Path)
			fmt.Fprintf(progress, "verify: root %q is not checked: %s\n", r.Name, reason)
			v.unchecked = append(v.unchecked, strings.Join([]string{"UNCHECKED", field(r.Name), field(reason)}, "\t")+"\n")
			v.sum.RootsNotChecked++
			delete(inScope, r.Name)
		}
	}
	books := make([]source.Book, 0, len(all))
	for _, b := range all {
		if inScope[b.RootName] {
			books = append(books, b)
		}
	}
	fmt.Fprintf(progress, "verify: %d of %d indexed books in %d root(s); index read %s\n",
		len(books), len(all), len(inScope), mode)

	pool := openpool.New(openpool.Options{Open: roots.PoolOpener(), Logger: log})
	defer pool.Close()
	v.factory = source.NewFactory(source.Options{Roots: roots, Pool: pool, Logger: log})

	workers := opts.Workers
	if workers <= 0 {
		workers = cfg.Scan.Workers
	}
	workers = max(1, workers)

	feed := make(chan source.Book)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for b := range feed {
				if v.hooks.book != nil {
					done := v.hooks.book(b)
					v.book(ctx, b)
					done()
					continue
				}
				v.book(ctx, b)
			}
		}()
	}
feeding:
	for _, b := range books {
		select {
		case feed <- b:
		case <-ctx.Done():
			break feeding
		}
	}
	close(feed)
	wg.Wait()

	sum := v.summary()
	if err := ctx.Err(); err != nil {
		fmt.Fprintf(progress, "verify: interrupted; the counts below cover only the books read so far\n")
		v.printSummary(sum)
		return sum, fmt.Errorf("verify interrupted: %w", err)
	}
	v.printSummary(sum)
	return sum, nil
}

// anyBookExists reports whether at least one book's container — the file, or
// the directory of a dir book — is present under the root. Only "does not
// exist" counts as absent: a book that is there but unreadable is a defect for
// the check to report, not evidence of a missing volume.
//
// Every volume of a nested container shares one RelPath, which is stat'ed
// once. A cancelled context answers true, so an interrupted run never names a
// root it did not finish looking at.
func anyBookExists(ctx context.Context, fsys *os.Root, books []source.Book) bool {
	seen := make(map[string]bool, len(books))
	for _, b := range books {
		if seen[b.RelPath] {
			continue
		}
		seen[b.RelPath] = true
		if ctx.Err() != nil {
			return true
		}
		if _, err := fsys.Stat(filepath.FromSlash(b.RelPath)); !errors.Is(err, fs.ErrNotExist) {
			return true
		}
	}
	return false
}

// rootProblem says why a root could not be opened, in the words of the same
// call the source layer made.
func rootProblem(path string) string {
	r, err := os.OpenRoot(path)
	if err != nil {
		return err.Error()
	}
	_ = r.Close()
	return path + " could not be opened when verify started"
}

// selectRoots resolves --root names against the configuration.
func selectRoots(configured []config.Root, names []string) ([]config.Root, error) {
	if len(names) == 0 {
		var out []config.Root
		for _, r := range configured {
			if r.Enabled {
				out = append(out, r)
			}
		}
		return out, nil
	}
	byName := make(map[string]config.Root, len(configured))
	for _, r := range configured {
		byName[r.Name] = r
	}
	var out []config.Root
	seen := map[string]bool{}
	for _, n := range names {
		r, ok := byName[n]
		switch {
		case !ok:
			return nil, fmt.Errorf("--root %q: no root of that name is configured", n)
		case !r.Enabled:
			return nil, fmt.Errorf("--root %q: that root is disabled in the configuration", n)
		case seen[n]:
			continue
		}
		seen[n] = true
		out = append(out, r)
	}
	return out, nil
}

// verifier holds the shared state of one run. Every field below mu is guarded
// by it, and every line is written under it in a single Write, so lines from
// different workers never interleave.
type verifier struct {
	factory *source.Factory
	hooks   hooks

	mu        sync.Mutex
	out       io.Writer
	sum       Summary
	unchecked []string // UNCHECKED lines, written just before SUMMARY
}

func (v *verifier) summary() Summary {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.sum
}

func (v *verifier) printSummary(s Summary) {
	v.mu.Lock()
	defer v.mu.Unlock()
	for _, l := range v.unchecked {
		_, _ = io.WriteString(v.out, l)
	}
	fmt.Fprintf(v.out, "SUMMARY\tbooks=%d\tpages=%d\tdefective_books=%d\tdefective_pages=%d\tunverifiable_books=%d"+
		"\tunverifiable_pages=%d\troots_not_checked=%d\n",
		s.Books, s.Pages, s.DefectiveBooks, s.DefectivePages, s.Unverifiable, s.UnverifiablePages, s.RootsNotChecked)
}

// bookResult is one book's tally, merged into the summary once at the end so
// a book is counted defective at most once.
type bookResult struct {
	b                 source.Book
	pages             int
	defects           int
	pageDefects       int
	unverifiablePages int
	unverifiable      bool
	lines             []string
}

func (r *bookResult) defect(p *source.Page, cause error) {
	no, name := "-", "-"
	if p != nil {
		no, name = strconv.Itoa(p.No), p.Name
		r.pageDefects++
	}
	r.defects++
	r.lines = append(r.lines, strings.Join([]string{
		"BROKEN", field(r.b.ID), field(bookPath(r.b)), no, field(name), field(cause.Error()),
	}, "\t")+"\n")
}

func (v *verifier) commit(r *bookResult) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.sum.Books++
	v.sum.Pages += r.pages
	v.sum.DefectivePages += r.pageDefects
	v.sum.UnverifiablePages += r.unverifiablePages
	if r.defects > 0 {
		v.sum.DefectiveBooks++
	}
	if r.unverifiable {
		v.sum.Unverifiable++
	}
	for _, l := range r.lines {
		_, _ = io.WriteString(v.out, l)
	}
}

// book verifies one book. A cancelled context abandons the book without
// counting it: cancellation is not damage.
func (v *verifier) book(ctx context.Context, b source.Book) {
	if ctx.Err() != nil {
		return
	}
	r := &bookResult{b: b}
	v.check(ctx, r)
	if ctx.Err() != nil {
		return
	}
	v.commit(r)
}

func (v *verifier) check(ctx context.Context, r *bookResult) {
	src, err := v.factory.Open(ctx, r.b)
	if err != nil {
		if unverifiable(err) {
			r.unverifiable = true
			return
		}
		r.defect(nil, err)
		return
	}
	defer src.Close()

	listing, err := src.List(ctx)
	switch {
	case err == nil:
	case errors.Is(err, source.ErrNoPages):
		// Opened cleanly, nothing to read: books.status 'empty', not damage.
		return
	case unverifiable(err):
		// Something in the book cannot be read by design. A partial listing
		// still comes back with it — a RAR chain that stops at an encrypted
		// or solid entry returns the entries before it — and those pages are
		// checked like any other.
		r.unverifiable = true
		if listing == nil {
			return
		}
	default:
		// A book-level failure. zipidx's salvage (and the other partial
		// listings) still return what they recovered, and those pages are
		// checked like any other.
		r.defect(nil, err)
		if listing == nil || !hasIntegrityData(r.b.Kind) {
			return
		}
	}
	if !hasIntegrityData(r.b.Kind) {
		r.unverifiable = true
		return
	}
	for i := range listing.Pages {
		if ctx.Err() != nil {
			return
		}
		p := &listing.Pages[i]
		if v.hooks.page != nil {
			v.hooks.page(r.b, p.No)
		}
		unv, err := checkPage(ctx, src, *p)
		switch {
		case unv:
			// The page-level form of the book-level verdict above: this
			// build cannot open the entry, which says nothing about damage.
			r.unverifiablePages++
			r.unverifiable = true
			continue
		case err != nil:
			r.defect(p, err)
		}
		r.pages++
	}
}

// checkPage reads one page and checks it. unv reports a page this build
// cannot open by design (see unverifiable); err is then the reason.
func checkPage(ctx context.Context, src source.BookSource, p source.Page) (unv bool, err error) {
	st, err := src.Open(ctx, p, source.OpenOptions{})
	if err != nil {
		return unverifiable(err), err
	}
	defer st.Close()
	return false, CheckStream(st, p.Size, p.CRC32)
}

// hasIntegrityData reports whether a kind's pages carry a recorded length and
// CRC-32: every archive kind does, a directory and a PDF do not. nesteddir is
// a directory *inside* a ZIP or RAR, so its pages are archive entries.
func hasIntegrityData(k source.Kind) bool {
	switch k {
	case source.KindDir, source.KindPDF:
		return false
	default:
		return true
	}
}

// unverifiable is a book this build cannot read at all, by design rather than
// by damage: a format or method it does not support, or a password.
func unverifiable(err error) bool {
	return errors.Is(err, source.ErrUnsupported) ||
		errors.Is(err, archive.ErrEncrypted) ||
		errors.Is(err, archive.ErrUnsupportedMethod)
}

// bookPath is root/rel_path, with !inner_path for a book inside a container.
func bookPath(b source.Book) string {
	p := b.RootName + "/" + b.RelPath
	if b.InnerPath != "" {
		p += "!" + b.InnerPath
	}
	return p
}

// field keeps one value on one line and inside one column.
func field(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' || r == '\n' || r == '\r' {
			return ' '
		}
		return r
	}, s)
}
