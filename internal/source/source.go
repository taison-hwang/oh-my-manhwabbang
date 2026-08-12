// Package source makes every shape a book can take — an archive, a folder of
// images, a PDF, a volume inside a container — look identical to everything
// above it.
//
// prd §2.2 says a 권 is "a ZIP file, an image sub-folder, or a PDF", and AC-003
// and AC-004 both demand that the UI flow not care which. That is this
// package's entire job: the scanner asks a [BookSource] for its pages, the
// HTTP layer asks it for one page's bytes, and neither contains the word "zip".
// D-71 is the proof it works — RAR arrived as one [archive.Reader] and two
// openers, and nothing above this package changed at all.
//
// It also owns two rules that are the same for every kind:
//
//   - FR-IDX-006 exclusions and FR-IDX-011 extensions (exclude.go), so a
//     `Thumbs.db` is dropped identically whether it came out of an archive or
//     off a disk.
//   - FR-IDX-007 natural page order, via internal/natsort, so `1.jpg`,
//     `2.jpg`, `10.jpg` come back in that order from every kind.
//
// Nothing in this package writes to the media volume (FR-CFG-005 /
// NFR-DAT-002), which `make lint`'s check-readonly grep enforces.
package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"shelf/internal/archive"
	"shelf/internal/archive/rar4"
	"shelf/internal/archive/zipidx"
	"shelf/internal/config"
	"shelf/internal/natsort"
	"shelf/internal/openpool"
	"shelf/internal/pdfium"
)

// Kind is the books.kind column of arch §3.5.
type Kind string

const (
	KindZIP Kind = "zip"
	KindDir Kind = "dir"
	KindPDF Kind = "pdf"
	// KindRAR is a RAR 4.x container, `.rar` or `.cbr`. prd §7.2 and D-07 put
	// it out of scope for v1; D-71 brought it back in after measuring that not
	// one of the collection's 14 archives is solid, which is what a page served
	// from a recorded offset requires (see internal/archive/rar4).
	KindRAR Kind = "rar"
	// KindNestedZIP is one volume inside a container of volumes — an entry that
	// is itself a ZIP. The container is the book's RelPath and the volume is its
	// InnerPath.
	KindNestedZIP Kind = "nestedzip"
	// KindNestedRAR is the same shape with a RAR volume. `사모님은 학생회장.zip`
	// is the case it exists for: 7 ZIPs and 8 RARs in one container, and under
	// D-07 only the 7 were books.
	KindNestedRAR Kind = "nestedrar"
)

// Errors callers match with errors.Is.
var (
	// ErrUnsupported — this build cannot serve the book: an unknown kind, or a
	// PDF with PDF support compiled out or disabled. It maps to
	// books.status='unsupported' and to HTTP 501.
	ErrUnsupported = errors.New("book format is not supported by this build")
	// ErrNoPages — the container opened cleanly but nothing survived the
	// FR-IDX-006 exclusions. books.status='empty'.
	//
	// It used to be what a container of nested volumes produced — the 1.44 GB
	// `엔젤하트` archive of 33 sub-ZIPs and zero images was the standing example.
	// That is no longer true: those containers are now series of nested books
	// (see nestedsource.go). Nor is it what a book holding one unreadable
	// format produces — that is ErrUnsupported naming the format (D-72). So
	// this error is back to meaning what it says.
	ErrNoPages = errors.New("no supported image entries")
	// ErrUnknownRoot — the book names a root that is not configured or not
	// currently reachable.
	ErrUnknownRoot = errors.New("unknown root")
	// ErrUnsafePath — a stored relative path is not local (path-traversal
	// layer 2). This can only happen if the index was tampered with.
	ErrUnsafePath = errors.New("unsafe relative path")
	// ErrContainerChanged — the container could not be read at the (size,
	// mtime) the caller asked for, even after re-opening it. Only [BookSource.List]
	// raises it, and only for the scanner: a listing is written down beside the
	// metadata that was passed in, so listing one file under another's identity
	// produces a *wrong verdict that looks measured*. books.status='error'
	// (archive.StatusOf's default), which the next scan re-reads because the
	// recorded (size, mtime) no longer match the disk. Serving is unaffected —
	// it reads offsets that belong to the file as the index recorded it, and
	// arch §5.2 tolerates that deliberately.
	ErrContainerChanged = errors.New("archive changed on disk while it was being read")
)

// StatusOf maps a failure to the books.status value of arch §4.11.
func StatusOf(err error) archive.Status {
	switch {
	case err == nil:
		return archive.StatusOK
	case errors.Is(err, ErrNoPages):
		return archive.StatusEmpty
	case errors.Is(err, ErrUnsupported):
		return archive.StatusUnsupported
	default:
		return archive.StatusOf(err)
	}
}

// Book is the persisted description of one 권, as the index holds it. It is a
// plain value so this package never imports internal/index: consumers map
// their rows onto it.
type Book struct {
	ID       string
	Kind     Kind
	RootName string
	// RelPath is a slash-separated path relative to the root: the container
	// file for zip/pdf, the directory for dir.
	RelPath string
	// InnerPath is the entry path of this book inside RelPath, for
	// [KindNestedZIP] and [KindNestedRAR]. Empty for every other kind.
	InnerPath string
	// FileSize and FileMtime are what the index recorded for the container.
	// They are passed to the handle pool so a changed file can be reported as
	// stale (arch §5.2) instead of silently served from wrong offsets. Zero
	// means "do not check".
	FileSize  int64
	FileMtime int64
}

// Page is one page as the index holds it — the pages row of arch §3.5,
// minus the columns only the API cares about.
type Page struct {
	No        int    // 1-based. There is no page 0 anywhere in this product.
	Name      string // decoded display name
	EntryPath string // zip: full decoded entry path · dir: name within the book dir · pdf: ""
	Ext       string // lowercase, with dot

	Size     int64 // uncompressed bytes; 0 for a pdf page, which has none until rendered
	CompSize int64 // archives only: bytes on disk
	// Method is the container's own compression method, so its numbering is
	// per format: ZIP uses 0 stored / 8 deflate, RAR uses 0x30 stored /
	// 0x31–0x35 packed. The two ranges do not overlap, but nothing relies on
	// that — the reader that wrote the row is the reader that reads it.
	Method uint16

	// LocalHdrOff is where the entry's own header starts: the ZIP local file
	// header, or the RAR block header. FR-SRV-002 lives or dies on this column.
	LocalHdrOff int64
	CRC32       uint32 // archives only, free from the directory read
	Mtime       int64  // dir only: the file's own mtime
}

// Ref reduces a page to what streaming it needs.
func (p Page) Ref() archive.EntryRef {
	return archive.EntryRef{
		LocalHdrOff: p.LocalHdrOff,
		CompSize:    p.CompSize,
		Size:        p.Size,
		Method:      p.Method,
		CRC32:       p.CRC32,
	}
}

// Listing is what enumerating a book produces.
type Listing struct {
	Kind  Kind
	Pages []Page
	// NameEncoding is the encoding kenc chose for this book's entry names
	// (arch §4.4), recorded so the UI can surface a book decoded as CP949.
	NameEncoding string
	// Excluded counts the entries dropped by FR-IDX-006, for the scan log.
	Excluded int
	// Foreign names the container format this book turned out to hold, when it
	// holds one this build cannot open and no pages at all — "HV3" for
	// `펌프킨 시저스 04.zip`. Empty for every ordinary book, including one that
	// merely contains a stray `.7z` alongside its pages.
	Foreign string
	// TotalBytes is the sum of the pages' uncompressed sizes — books.total_bytes.
	TotalBytes int64
	// ZIP64 reports that the container used 64-bit records (FR-IDX-009).
	ZIP64 bool
}

// OpenOptions are the per-request knobs of page serving.
type OpenOptions struct {
	// Width is the requested render width in pixels for a PDF page
	// (FR-SRV-006). Ignored by the other kinds, where FR-SRV-008 forbids any
	// re-encoding. Zero means the configured default.
	Width int
}

// Stream is one page's bytes, ready to be written to a response.
//
// For stored archive entries (ZIP and RAR alike), dir pages and rendered PDF
// pages the body also implements io.ReadSeeker; use [Stream.ReadSeeker] to hand
// it to http.ServeContent and get Range support (arch §5.3). Compressed entries
// are forward-only by construction.
type Stream struct {
	io.ReadCloser
	ContentType string
	// Size is the body length in bytes, or -1 when it is not known ahead of
	// time (it always is, in this build).
	Size    int64
	ModTime time.Time
}

// ReadSeeker returns the body as an io.ReadSeeker when it is one.
func (s *Stream) ReadSeeker() (io.ReadSeeker, bool) {
	rs, ok := s.ReadCloser.(io.ReadSeeker)
	return rs, ok
}

// BookSource serves one book. Implementations are safe for concurrent use.
type BookSource interface {
	// Kind reports which of the three shapes this book is.
	Kind() Kind

	// List enumerates the book's pages in natural order with the FR-IDX-006
	// exclusions applied. For ZIP containers it reads the central directory
	// and nothing else (FR-IDX-002).
	//
	// A book-level failure comes back as an error whose StatusOf gives the
	// books.status to record; the Listing may still be non-nil and partially
	// populated. Callers must isolate it and carry on (FR-IDX-010).
	List(ctx context.Context) (*Listing, error)

	// Open streams one page's bytes. The page must have come from List or
	// from the index rows List produced.
	Open(ctx context.Context, p Page, opt OpenOptions) (*Stream, error)

	// Close releases anything the source is holding. It never closes a stream
	// already handed out by Open.
	Close() error
}

// StaleChecker is implemented by sources whose container can change on disk
// while the index still describes the old one — today, [KindZIP].
//
// The HTTP layer type-asserts for it: a page request carrying `?v=` whose
// source reports stale is answered `409 stale_version` so the client refetches
// its metadata, rather than served bytes read at offsets that no longer mean
// anything (arch §5.2, §7.6).
type StaleChecker interface {
	Stale(ctx context.Context) (bool, error)
}

// Opener builds a BookSource for one book kind.
//
// It is the extension point prd §7.2 asks for: RAR/CBR and 7z are out of scope
// for this build, but adding one is a matter of implementing BookSource and
// calling [Factory.Register] — no edit to the dispatch below, and nothing above
// this package changes at all.
type Opener func(ctx context.Context, f *Factory, b Book) (BookSource, error)

// Options configures a Factory.
type Options struct {
	// Roots resolves a root name to its os.Root handle. Required.
	Roots *RootSet
	// Pool supplies pooled container handles (FR-SRV-004). Required for
	// KindZIP.
	Pool *openpool.Pool
	// Archive is the ZIP container reader. Zero means zipidx.New().
	Archive archive.Reader
	// RAR is the RAR container reader. Zero means rar4.New().
	RAR archive.Reader
	// PDF is the rasteriser. Zero, or a build with -tags nopdf, makes every
	// PDF book ErrUnsupported.
	PDF *pdfium.Renderer
	// PDFWidth is the default render width when a request does not ask for
	// one; PDFMaxWidth clamps what it may ask for (arch §3.2 pdf block).
	PDFWidth    int
	PDFMaxWidth int
	// PDFQuality is the JPEG quality of a rendered page.
	PDFQuality int
	// Logger; zero means slog.Default().
	Logger *slog.Logger
}

// Factory opens book sources. One value serves the whole process.
type Factory struct {
	roots *RootSet
	pool  *openpool.Pool
	// readers is one archive.Reader per container format, keyed by the plain
	// kind that names it. A nested book looks up two of them: one for the
	// container it lives in and one for its own format.
	readers map[Kind]archive.Reader
	pdf     *pdfium.Renderer
	pdfW    int
	pdfMaxW int
	pdfQ    int
	log     *slog.Logger

	mu      sync.RWMutex
	openers map[Kind]Opener
}

// NewFactory returns a factory with the three built-in kinds registered.
func NewFactory(opts Options) *Factory {
	f := &Factory{
		roots:   opts.Roots,
		pool:    opts.Pool,
		readers: make(map[Kind]archive.Reader, 2),
		pdf:     opts.PDF,
		pdfW:    opts.PDFWidth,
		pdfMaxW: opts.PDFMaxWidth,
		pdfQ:    opts.PDFQuality,
		log:     opts.Logger,
		openers: make(map[Kind]Opener, 6),
	}
	f.readers[KindZIP] = opts.Archive
	if f.readers[KindZIP] == nil {
		f.readers[KindZIP] = zipidx.New()
	}
	f.readers[KindRAR] = opts.RAR
	if f.readers[KindRAR] == nil {
		f.readers[KindRAR] = rar4.New()
	}
	if f.log == nil {
		f.log = slog.Default()
	}
	f.openers[KindZIP] = openZIP
	f.openers[KindRAR] = openRAR
	f.openers[KindDir] = openDir
	f.openers[KindPDF] = openPDF
	f.openers[KindNestedZIP] = openNestedZIP
	f.openers[KindNestedRAR] = openNestedRAR
	return f
}

// readerFor returns the reader for a container kind, or nil for a kind that
// names no container format.
func (f *Factory) readerFor(k Kind) archive.Reader { return f.readers[k] }

// Register installs an opener for a container kind, replacing any existing one.
func (f *Factory) Register(kind Kind, open Opener) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.openers[kind] = open
}

// Kinds lists the registered kinds, sorted, for diagnostics.
func (f *Factory) Kinds() []Kind {
	f.mu.RLock()
	defer f.mu.RUnlock()
	out := make([]Kind, 0, len(f.openers))
	for k := range f.openers {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// Open returns a source for b.
func (f *Factory) Open(ctx context.Context, b Book) (BookSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.RLock()
	open, ok := f.openers[b.Kind]
	f.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("opening book %s: %w (kind %q)", b.ID, ErrUnsupported, b.Kind)
	}
	return open(ctx, f, b)
}

// resolve applies path-traversal layers 2 and 4 (arch §8.1) and returns the
// root handle plus the absolute container path.
//
// Layer 1 — the caller only ever holds an opaque id that the index turned into
// (root_name, rel_path) — has already done the real work; these are the cheap
// insurance policies. Layer 3 is the *os.Root itself, which every open below
// goes through.
func (f *Factory) resolve(b Book) (*os.Root, string, error) {
	if f.roots == nil {
		return nil, "", fmt.Errorf("resolving book %s: %w %q", b.ID, ErrUnknownRoot, b.RootName)
	}
	root, rootPath, err := f.roots.lookup(b.RootName)
	if err != nil {
		return nil, "", fmt.Errorf("resolving book %s: %w", b.ID, err)
	}
	rel, err := safeRel(b.RelPath)
	if err != nil {
		return nil, "", fmt.Errorf("resolving book %s: %w", b.ID, err)
	}
	abs := filepath.Join(rootPath, filepath.FromSlash(rel))
	// Layer 4: a final prefix assertion. It never fires; if it ever does, that
	// is a bug worth screaming about rather than a request worth serving.
	if !withinRoot(abs, rootPath) {
		return nil, "", fmt.Errorf("resolving book %s: %w %q", b.ID, ErrUnsafePath, b.RelPath)
	}
	return root, abs, nil
}

// safeRel implements path-traversal layer 2: filepath.IsLocal plus an explicit
// element check on the slash-split path.
//
// The element check is not redundant. filepath.IsLocal(`..\win`) is *true* on
// Linux, because backslash is an ordinary filename byte there — so an index
// built on Linux and carried to Windows would otherwise hand `..\win` straight
// to the filesystem. VERIFIED in arch §8.1.
func safeRel(rel string) (string, error) {
	clean := path.Clean(strings.ReplaceAll(rel, `\`, "/"))
	if clean == "" || clean == "." {
		return "", fmt.Errorf("%w %q", ErrUnsafePath, rel)
	}
	if path.IsAbs(clean) || !filepath.IsLocal(filepath.FromSlash(clean)) {
		return "", fmt.Errorf("%w %q", ErrUnsafePath, rel)
	}
	for _, el := range strings.Split(clean, "/") {
		if el == "" || el == "." || el == ".." {
			return "", fmt.Errorf("%w %q", ErrUnsafePath, rel)
		}
	}
	return clean, nil
}

func withinRoot(abs, rootPath string) bool {
	cleanRoot := filepath.Clean(rootPath)
	cleanAbs := filepath.Clean(abs)
	return cleanAbs == cleanRoot || strings.HasPrefix(cleanAbs, cleanRoot+string(os.PathSeparator))
}

// finish sorts a page list into natural order (FR-IDX-007) and numbers it from
// 1. Every kind funnels through here, which is what makes page order identical
// across the three.
func finish(l *Listing, pages []Page) *Listing {
	slices.SortStableFunc(pages, func(a, b Page) int {
		if c := natsort.Compare(a.EntryPath, b.EntryPath); c != 0 {
			return c
		}
		return strings.Compare(a.EntryPath, b.EntryPath)
	})
	var total int64
	for i := range pages {
		pages[i].No = i + 1
		total += pages[i].Size
	}
	l.Pages = pages
	l.TotalBytes = total
	return l
}

// RootSet holds one *os.Root per configured root — path-traversal layer 3.
//
// os.Root refuses any path that escapes the directory it was opened on,
// including through a symlink, at the openat(2) level. That is also why the
// E2E suite cannot use a symlink farm (decision D-48): the refusal is not
// configurable.
type RootSet struct {
	mu      sync.RWMutex
	entries map[string]*rootEntry
	log     *slog.Logger
}

type rootEntry struct {
	name string
	path string
	root *os.Root
	err  error
}

// OpenRoots opens every enabled root. A root that cannot be opened — an
// unmounted drive, a typo in the YAML — is recorded rather than fatal: arch
// §4.9 is explicit that an unreachable root must not take the rest of the
// library down with it.
func OpenRoots(ctx context.Context, roots []config.Root, log *slog.Logger) (*RootSet, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	s := &RootSet{entries: make(map[string]*rootEntry, len(roots)), log: log}
	for _, r := range roots {
		if !r.Enabled {
			continue
		}
		abs, err := filepath.Abs(r.Path)
		if err != nil {
			abs = r.Path
		}
		e := &rootEntry{name: r.Name, path: filepath.Clean(abs)}
		root, err := os.OpenRoot(e.path)
		if err != nil {
			e.err = fmt.Errorf("opening root %q: %w", r.Name, err)
			log.Warn("root is unreachable", "root", r.Name, "err", err)
		} else {
			e.root = root
		}
		s.entries[r.Name] = e
	}
	return s, nil
}

// NewRootSet builds a RootSet from name→path pairs. It is the constructor
// tests and one-off tools use when there is no config.Config in hand.
func NewRootSet(ctx context.Context, paths map[string]string, log *slog.Logger) (*RootSet, error) {
	roots := make([]config.Root, 0, len(paths))
	for name, p := range paths {
		roots = append(roots, config.Root{Name: name, Path: p, Enabled: true})
	}
	slices.SortFunc(roots, func(a, b config.Root) int { return strings.Compare(a.Name, b.Name) })
	return OpenRoots(ctx, roots, log)
}

func (s *RootSet) lookup(name string) (*os.Root, string, error) {
	s.mu.RLock()
	e, ok := s.entries[name]
	s.mu.RUnlock()
	if !ok {
		return nil, "", fmt.Errorf("%w %q", ErrUnknownRoot, name)
	}
	if e.err != nil {
		return nil, "", e.err
	}
	return e.root, e.path, nil
}

// Root returns the os.Root for a configured root name.
func (s *RootSet) Root(name string) (*os.Root, bool) {
	root, _, err := s.lookup(name)
	return root, err == nil
}

// Path returns the absolute path of a configured root.
func (s *RootSet) Path(name string) (string, bool) {
	s.mu.RLock()
	e, ok := s.entries[name]
	s.mu.RUnlock()
	if !ok {
		return "", false
	}
	return e.path, true
}

// PoolOpener returns an openpool.OpenFunc that resolves an absolute container
// path back to the root that contains it and opens it through that root's
// os.Root.
//
// Wiring it into the pool extends path-traversal layer 3 to archive containers,
// which would otherwise be opened by absolute path. It also means a path
// belonging to no configured root cannot be opened at all, whatever put it in
// the index.
func (s *RootSet) PoolOpener() openpool.OpenFunc {
	return func(abs string) (openpool.File, error) {
		clean := filepath.Clean(abs)
		s.mu.RLock()
		defer s.mu.RUnlock()

		// Longest match wins. Two configured roots may nest (`/media` and
		// `/media/manga`), and iterating a map would otherwise pick a different
		// one from run to run — which is not unsafe, since os.Root refuses an
		// escape either way, but it would make failures irreproducible.
		var best *rootEntry
		for _, e := range s.entries {
			if e.root == nil || clean == e.path || !withinRoot(clean, e.path) {
				continue
			}
			if best == nil || len(e.path) > len(best.path) {
				best = e
			}
		}
		if best == nil {
			return nil, fmt.Errorf("opening %q: %w", abs, ErrUnknownRoot)
		}
		rel := filepath.ToSlash(strings.TrimPrefix(clean, best.path+string(os.PathSeparator)))
		safe, err := safeRel(rel)
		if err != nil {
			return nil, err
		}
		return best.root.Open(filepath.FromSlash(safe))
	}
}

// ErrRootExists is Add's refusal when the name is already in the set.
var ErrRootExists = errors.New("root already open")

// Add opens one more root into a live set — amendment A-12, ruling E-40.
//
// # Why this exists, given that OpenRoots was deliberately called once
//
// Ruling E-26's amendment A-11 made `POST /api/roots` restart-based, and the
// comment on `OpenRoots` above said so: the pool, the source factory and the
// scanner are all built over one `*RootSet` that startup produced. E-40
// overturns that for **addition only**, and this method is the whole mechanism.
// It works because those three collaborators hold a *pointer* to this set, not
// a copy of its contents — so a name inserted here is reachable from every one
// of them the moment the write lock is released, with no re-wiring anywhere.
//
// Removal is NOT the mirror of this and is not offered. Closing a handle that
// an in-flight page request is streaming through is a different problem with a
// different answer (reference counting the entries), and E-40 §4 explicitly
// keeps removal on A-11's revision R1 — the removed-set — where it already
// works without touching this map.
//
// The failure of `os.OpenRoot` is returned rather than recorded. That is the
// opposite of what `OpenRoots` does with the same error, and the asymmetry is
// deliberate: at startup an unreachable root must not stop the server booting
// (arch §4.9), but here a caller is asking for one specific directory and can
// be told, so `POST /api/roots` answers `400 not_readable` instead of adding a
// row that exists only to look broken.
func (s *RootSet) Add(name, path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.Clean(abs)

	// Opened before the lock is taken: os.OpenRoot touches the filesystem, and
	// holding the write lock across a syscall on a possibly-dead mount would
	// stall every page request in the process. The name is re-checked after.
	root, err := os.OpenRoot(abs)
	if err != nil {
		return fmt.Errorf("opening root %q: %w", name, err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.entries[name]; exists {
		// Two concurrent adds of the same name, or an add of a name startup
		// already opened. Closing our handle here is what keeps the loser of the
		// race from leaking a descriptor.
		_ = root.Close()
		return fmt.Errorf("%w: %q", ErrRootExists, name)
	}
	s.entries[name] = &rootEntry{name: name, path: abs, root: root}
	s.log.Info("a root was opened into the running server", "root", name, "path", abs)
	return nil
}

// Close releases every root handle.
func (s *RootSet) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	for _, e := range s.entries {
		if e.root == nil {
			continue
		}
		if cerr := e.root.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("closing root %q: %w", e.name, cerr)
		}
		e.root = nil
	}
	return err
}

// pageStream ties a page body to whatever must be released when the response
// finishes — a pooled handle, an open file.
type pageStream struct {
	rc      io.ReadCloser
	release func()
	once    sync.Once
}

func (p *pageStream) Read(b []byte) (int, error) { return p.rc.Read(b) }

func (p *pageStream) Close() error {
	err := p.rc.Close()
	p.once.Do(func() {
		if p.release != nil {
			p.release()
		}
	})
	return err
}

// seekablePageStream is a pageStream whose body can seek, which is what lets
// the HTTP layer answer Range requests for stored entries and dir pages.
type seekablePageStream struct {
	*pageStream
	rs io.ReadSeeker
}

func (s *seekablePageStream) Seek(off int64, whence int) (int64, error) {
	return s.rs.Seek(off, whence)
}

// wrapBody preserves seekability across the release wrapper. io.NopCloser and
// friends silently drop io.Seeker, which would cost Range support on every
// uncompressed page.
func wrapBody(rc io.ReadCloser, release func()) io.ReadCloser {
	ps := &pageStream{rc: rc, release: release}
	if rs, ok := rc.(io.ReadSeeker); ok {
		return &seekablePageStream{pageStream: ps, rs: rs}
	}
	return ps
}
