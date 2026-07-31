package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"shelf/internal/archive/zipidx"
	"shelf/internal/auth"
	"shelf/internal/config"
	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/natsort"
	"shelf/internal/openpool"
	"shelf/internal/pdfium"
	"shelf/internal/scanner"
	"shelf/internal/source"
	"shelf/internal/testutil"
	"shelf/internal/thumbs"
	"shelf/internal/userdata"
)

// The fixture library. It is small, but every shape the contract has to
// describe is in it: a folder series holding a ZIP volume, a directory volume
// and a loose cover file; a single-ZIP series whose only book is corrupt
// (FR-IDX-010); and a PDF series, which is the `501 unsupported` path by
// default and — under `withPDF()` — a real document that the rasteriser
// actually renders.
const (
	rootName = "manga"

	seriesFolderPath = "[만화] 군계 1~25"
	seriesCloverPath = "[만화] Clover 클로버 (총4권)"
	seriesBrokenPath = "[만화] 손상.zip"
	seriesPDFPath    = "[만화] 미생 1~9 (완결 pdf)"

	bookZipPath    = seriesFolderPath + "/군계(軍鷄) 01권.zip"
	bookDirPath    = seriesFolderPath + "/군계(軍鷄) 02권"
	bookCloverPath = seriesCloverPath + "/클로버 01권.zip"
	bookBrokenPath = seriesBrokenPath
	bookPDFPath    = seriesPDFPath + "/미생 01권.pdf"

	coverRelPath = seriesFolderPath + "/[cover].jpg"

	// Content versions are chosen rather than derived so the golden files are
	// byte-stable. The scanner computes them from (size, mtime); nothing in the
	// HTTP layer cares where the string came from, only that it is the one the
	// client must echo as ?v=.
	cvZip    = "a1b2c3d4e5f60718"
	cvDir    = "0f1e2d3c4b5a6978"
	cvClover = "c10bec10bec10be0"
	cvBroken = "deadbeefdeadbeef"
	cvPDF    = "1234567890abcdef"
)

// A fixed clock. Every timestamp in a golden file comes from it, so the files
// are reproducible on any machine at any time.
var (
	fixedNow     = time.Date(2026, 7, 28, 9, 0, 0, 0, time.UTC)
	fixedStarted = fixedNow.Add(-90 * time.Second)
	fixedMtime   = time.Date(2016, 3, 14, 1, 59, 26, 0, time.UTC)
)

// env is one fully wired server over real SQLite databases, a real archive on
// disk and a real thumbnail cache. Nothing here is a mock except the scanner,
// whose whole job is asynchronous work that a golden file cannot pin.
type env struct {
	t     *testing.T
	dir   string
	media string
	// secondRoot is withSecondRoot()'s directory: configured, empty, and never
	// scanned, so it has no index row.
	secondRoot string
	cfg        *config.Config
	idx        *index.DB
	user       *userdata.DB
	thumbs     *thumbs.Service
	// dims is the Thumbs the server is actually wired to: a pass-through
	// decorator over `thumbs` that records the EnsureDims calls the HTTP layer
	// makes, so the arch §5.8 scheduling rule is assertable (see dims_test.go).
	dims *thumbsSpy
	scan *fakeScanner
	srv  *Server

	seriesFolderID string
	seriesCloverID string
	seriesBrokenID string
	seriesPDFID    string
	bookZipID      string
	bookDirID      string
	bookCloverID   string
	bookBrokenID   string
	bookPDFID      string

	// zipPages is the page metadata read out of the real archive's central
	// directory, so the offsets a page request seeks to are genuine.
	zipPages    []index.Page
	zipPayloads [][]byte
	dirPayloads [][]byte
}

type envOption func(*envConfig)

type envConfig struct {
	basePath string
	password string
	static   bool
	// pdf turns on `pdf.enabled`, wires a real rasteriser into the source
	// factory and replaces the fixture's stub PDF with a genuine 9-page
	// document. Without it the PDF book is a file that only ever produces
	// `501 unsupported` (arch §7.6), which leaves the whole render path — the
	// §5.3 raster ETag, the `w` clamp, the render cache — unexecuted.
	pdf bool
	// noAVIFConfig sets `thumbnails.avif_enabled: false`. The key defaults to
	// TRUE (config/defaults.go), so the harness's normal env is the case that
	// matters for ruling E-21: a config asking for AVIF in a build that may not
	// contain a decoder. This option exercises the other half of the gate.
	noAVIFConfig bool
	// rootEditing sets `server.allow_root_editing: true` (amendment A-11).
	// The key defaults to FALSE, so the harness's normal env is the shut gate —
	// which is the case ruling E-26's whole security argument rests on, and the
	// one every other test in this package therefore exercises for free.
	rootEditing bool
	// configInsideRoot writes the fixture's configuration file *inside* the
	// media root, which is the third condition of §7.4's gate.
	configInsideRoot bool
	// secondRoot adds a second, empty, never-scanned root to the configuration.
	// It buys two things no single-root fixture can express: a `DELETE` that is
	// not of the last root (§7.4's `409`), and a root that is in the file with
	// no index row, which is revision R2's pending case arrived at by hand
	// editing rather than by `POST`.
	secondRoot bool
}

// secondRootName is the fixture's second configured root (see envConfig).
const secondRootName = "docs"

func withBasePath(p string) envOption { return func(c *envConfig) { c.basePath = p } }
func withPassword(p string) envOption { return func(c *envConfig) { c.password = p } }
func withStatic() envOption           { return func(c *envConfig) { c.static = true } }
func withPDF() envOption              { return func(c *envConfig) { c.pdf = true } }
func withoutAVIFConfig() envOption    { return func(c *envConfig) { c.noAVIFConfig = true } }
func withRootEditing() envOption      { return func(c *envConfig) { c.rootEditing = true } }
func withSecondRoot() envOption       { return func(c *envConfig) { c.secondRoot = true } }
func withConfigInsideRoot() envOption {
	return func(c *envConfig) { c.rootEditing, c.configInsideRoot = true, true }
}

// newEnv builds the harness.
func newEnv(t *testing.T, opts ...envOption) *env {
	t.Helper()
	ec := envConfig{}
	for _, o := range opts {
		o(&ec)
	}
	ctx := t.Context()

	e := &env{t: t, dir: t.TempDir()}
	e.buildMedia(ec)
	e.cfg = e.loadConfig(ec)
	e.openDatabases(ctx)
	e.seed(ctx)

	rootSet, err := source.OpenRoots(ctx, e.cfg.Roots, discard())
	if err != nil {
		t.Fatalf("opening roots: %v", err)
	}
	t.Cleanup(func() { _ = rootSet.Close() })

	pool := openpool.New(openpool.Options{Open: rootSet.PoolOpener(), Logger: discard()})
	t.Cleanup(func() { _ = pool.Close() })

	// The renderer is wired only when the fixture holds a real document.
	// pdfium.New has the same signature under `-tags nopdf`, where every render
	// is ErrUnsupported, so this needs no build tag of its own.
	var renderer *pdfium.Renderer
	if ec.pdf {
		renderer = pdfium.New(pdfium.Options{
			Workers:  1,
			CacheDir: filepath.Join(e.cfg.Storage.CacheDir, "wazero"),
			Logger:   discard(),
		})
		t.Cleanup(func() { _ = renderer.Close() })
	}

	factory := source.NewFactory(source.Options{
		Roots:       rootSet,
		Pool:        pool,
		PDF:         renderer,
		PDFWidth:    e.cfg.PDF.DefaultWidth,
		PDFMaxWidth: e.cfg.PDF.MaxWidth,
		PDFQuality:  e.cfg.Thumbnails.Quality,
		Logger:      discard(),
	})

	e.thumbs, err = thumbs.New(ctx, thumbs.Options{
		CacheDir: e.cfg.Storage.CacheDir,
		Widths:   e.cfg.Thumbnails.Widths,
		Quality:  e.cfg.Thumbnails.Quality,
		Format:   e.cfg.Thumbnails.Format,
		Workers:  1,
		Index:    e.idx,
		Sources:  factory,
		Roots:    rootSet,
		Logger:   discard(),
		Now:      func() time.Time { return fixedNow },
	})
	if err != nil {
		t.Fatalf("thumbs.New: %v", err)
	}
	t.Cleanup(func() { _ = e.thumbs.Close() })
	e.dims = &thumbsSpy{Thumbs: e.thumbs}

	e.scan = newFakeScanner()

	var authenticator *auth.Authenticator
	if ec.password != "" {
		key := make([]byte, auth.KeyLength)
		for i := range key {
			key[i] = byte(i)
		}
		hash, herr := passwordHash(ec.password)
		if herr != nil {
			t.Fatalf("hashing the fixture password: %v", herr)
		}
		authenticator, err = auth.New(auth.Options{
			PasswordHash: hash,
			SessionKey:   key,
			BasePath:     e.cfg.Server.BasePath,
			Logger:       discard(),
			Sleep:        func(time.Duration) {},
		})
		if err != nil {
			t.Fatalf("auth.New: %v", err)
		}
	}

	var static = os.DirFS(filepath.Join(e.dir, "nonexistent-dist"))
	if ec.static {
		static = os.DirFS(e.buildDist())
	}

	e.srv, err = New(Options{
		Config:       e.cfg,
		Index:        e.idx,
		UserData:     e.user,
		Scanner:      e.scan,
		Thumbs:       e.dims,
		Sources:      factory,
		Roots:        rootSet,
		Pool:         pool,
		Auth:         authenticator,
		Static:       static,
		Logger:       discard(),
		Now:          func() time.Time { return fixedNow },
		StartedAt:    fixedStarted,
		ConfigDigest: e.configDigest(),
	})
	if err != nil {
		t.Fatalf("httpapi.New: %v", err)
	}
	return e
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

// errSecret stands in for an internal failure whose text must never reach the
// client (arch §8.4).
var errSecret = errors.New("opening /var/lib/s3cr3t/index.db: permission denied")

// tmpRootPlaceholder is what the golden files carry in place of the test's
// temporary directory. Two contract fields are genuinely absolute paths —
// `Root.path` (the settings screen shows it, C-5) and `CacheUsage.cache_dir` —
// so they have to appear in the snapshots, and their values cannot be pinned.
const tmpRootPlaceholder = "/tmp/shelf-golden"

// redact replaces the per-run temporary directory so a golden file is
// reproducible on any machine.
func (e *env) redact(body []byte) []byte {
	return bytes.ReplaceAll(body, []byte(filepath.Dir(e.dir)), []byte(tmpRootPlaceholder))
}

// passwordHash keeps the fixture cheap: bcrypt at the shipping cost 12 is
// ~250 ms per comparison and this harness is built dozens of times.
// TestHashPassword_usesCost12 in internal/auth pins the shipping cost.
func passwordHash(plain string) ([]byte, error) {
	return bcrypt.GenerateFromPassword([]byte(plain), bcrypt.MinCost)
}

// buildMedia materialises the fixture tree on disk.
func (e *env) buildMedia(ec envConfig) {
	e.t.Helper()

	// Three page payloads, deliberately mixing compression methods: a stored
	// entry exercises FR-SRV-003's passthrough and the Range path, a deflated
	// one exercises the forward-only path that must omit Accept-Ranges.
	p1 := testutil.TinyJPEG(e.t, 16, 24)
	p2 := testutil.TinyJPEG(e.t, 20, 30)
	p3 := testutil.TinyPNG(e.t, 8, 12)
	e.zipPayloads = [][]byte{p1, p2, p3}

	zipBytes := testutil.BuildZIP(e.t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: p1, Method: testutil.MethodStore, Modified: fixedMtime},
		{Name: "002.jpg", Data: p2, Method: testutil.MethodDeflate, Modified: fixedMtime},
		{Name: "010.png", Data: p3, Method: testutil.MethodDeflate, Modified: fixedMtime},
	}})

	d1 := testutil.TinyJPEG(e.t, 12, 18)
	d2 := testutil.TinyJPEG(e.t, 14, 21)
	e.dirPayloads = [][]byte{d1, d2}

	e.media = testutil.BuildTree(e.t, map[string]any{
		seriesFolderPath: map[string]any{
			"군계(軍鷄) 01권.zip": testutil.File{Data: zipBytes, ModTime: fixedMtime},
			"군계(軍鷄) 02권": map[string]any{
				"01.jpg": testutil.File{Data: d1, ModTime: fixedMtime},
				"02.jpg": testutil.File{Data: d2, ModTime: fixedMtime},
			},
			"[cover].jpg": testutil.File{Data: testutil.TinyJPEG(e.t, 24, 36), ModTime: fixedMtime},
		},
		// The other common cover shape: no loose image, so the cover is page 1
		// of the first volume (cover_kind='page') and therefore carries a cv.
		seriesCloverPath: map[string]any{
			"클로버 01권.zip": testutil.File{Data: zipBytes, ModTime: fixedMtime},
		},
		// A real truncated archive: the container opens and the central
		// directory does not parse, which is books.status='error' (FR-IDX-010).
		seriesBrokenPath: testutil.File{Data: zipBytes[:12], ModTime: fixedMtime},
		seriesPDFPath: map[string]any{
			"미생 01권.pdf": testutil.File{Data: e.pdfBytes(ec), ModTime: fixedMtime},
		},
	})

	e.zipPages = pagesFromZIP(e.t, zipBytes)
}

// The fixture PDF's shape. The page count matches what seed() writes into the
// index, so `n` runs 1..pdfPageCount and pdfPageCount+1 is a genuine 404; the
// media box is A4 at 72 dpi, which is what a scanned Korean volume looks like
// and what makes the rendered aspect ratio worth asserting.
const (
	pdfPageCount  = 9
	pdfPageWidth  = 595
	pdfPageHeight = 842
)

// pdfBytes is the fixture PDF's contents.
//
// With `withPDF()` it is a real document, so the whole of servePDFPage runs:
// the rasteriser, the §5.3 `r1-` ETag, the `w` clamp and the render cache. Any
// other environment gets a stub whose only job is to be a file with a `.pdf`
// extension, because those environments configure `pdf.enabled: false` and the
// handler never reaches the bytes.
func (e *env) pdfBytes(ec envConfig) []byte {
	e.t.Helper()
	if !ec.pdf {
		return []byte("%PDF-1.4\n% not a real document\n")
	}
	return minimalPDF(e.t, pdfPageCount, pdfPageWidth, pdfPageHeight)
}

// minimalPDF writes a valid multi-page PDF by hand.
//
// The frozen dependency set has no PDF *writer* and the real PDF series is
// 500 MB of copyrighted manga the hermetic suite may not touch (impl-plan
// §6.1), so the fixture is built the same way internal/pdfium and
// internal/source already build theirs: a catalog, a page tree, one content
// stream per page and a correct xref table. Each page is filled with a distinct
// colour, so a render that silently returns the wrong page differs in its bytes.
func minimalPDF(t testing.TB, pages, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	offsets := make(map[int]int)

	buf.WriteString("%PDF-1.4\n")
	obj := func(n int, body string) {
		offsets[n] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", n, body)
	}

	kids := ""
	for i := range pages {
		kids += fmt.Sprintf("%d 0 R ", 3+2*i)
	}
	obj(1, "<< /Type /Catalog /Pages 2 0 R >>")
	obj(2, fmt.Sprintf("<< /Type /Pages /Kids [ %s] /Count %d >>", kids, pages))
	for i := range pages {
		obj(3+2*i, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents %d 0 R /Resources << >> >>",
			w, h, 4+2*i))
		content := fmt.Sprintf("%0.2f 0.35 0.75 rg 10 10 %d %d re f\n", float64(i%10)/10.0, w-20, h-20)
		obj(4+2*i, fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
	}

	total := 2 + 2*pages
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", total+1)
	buf.WriteString("0000000000 65535 f \n")
	for n := 1; n <= total; n++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[n])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", total+1, xref)
	return buf.Bytes()
}

// pagesFromZIP reads the fixture archive's central directory so that the page
// rows carry genuine (local_hdr_off, comp_size, method, crc32) values. Seeding
// invented offsets would make every page test a test of the harness.
func pagesFromZIP(t *testing.T, raw []byte) []index.Page {
	t.Helper()
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("reading the fixture central directory: %v", err)
	}
	pages := make([]index.Page, 0, len(ix.Entries))
	for i, entry := range ix.Entries {
		pages = append(pages, index.Page{
			PageNo:      i + 1,
			Name:        entry.Name,
			EntryPath:   entry.Name,
			Ext:         source.Ext(entry.Name),
			Size:        entry.Size,
			CompSize:    entry.CompSize,
			Method:      int(entry.Method),
			LocalHdrOff: entry.LocalHdrOff,
			CRC32:       entry.CRC32,
			Mtime:       fixedMtime.Unix(),
		})
	}
	return pages
}

// buildDist writes a miniature `web/dist` so the SPA tests exercise real files
// rather than the embedded one, which a `go build ./...` checkout may not have.
func (e *env) buildDist() string {
	e.t.Helper()
	dist := filepath.Join(e.dir, "dist")
	mustMkdir(e.t, filepath.Join(dist, "assets"))
	mustWrite(e.t, filepath.Join(dist, "index.html"),
		"<!doctype html>\n<html lang=\"ko\">\n  <head>\n    <base href=\"/\" />\n    <title>SHELF</title>\n  </head>\n  <body><div id=\"root\"></div></body>\n</html>\n")
	mustWrite(e.t, filepath.Join(dist, "assets", "index-abc123.js"), "console.log('shelf')\n")
	return dist
}

func (e *env) loadConfig(ec envConfig) *config.Config {
	e.t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "server:\n  listen: \"127.0.0.1\"\n  port: 8790\n")
	if ec.basePath != "" {
		fmt.Fprintf(&b, "  base_path: %q\n", ec.basePath)
	}
	if ec.rootEditing {
		fmt.Fprintf(&b, "  allow_root_editing: true\n")
	}
	fmt.Fprintf(&b, "roots:\n  - name: %q\n    label: \"만화\"\n    path: %q\n", rootName, e.media)
	if ec.secondRoot {
		e.secondRoot = e.t.TempDir()
		fmt.Fprintf(&b, "  - name: %q\n    label: \"도서\"\n    path: %q\n", secondRootName, e.secondRoot)
	}
	fmt.Fprintf(&b, "storage:\n  data_dir: %q\n  cache_dir: %q\n",
		filepath.Join(e.dir, "data"), filepath.Join(e.dir, "cache"))
	fmt.Fprintf(&b, "library:\n  recently_added_days: 14\n")
	fmt.Fprintf(&b, "pdf:\n  enabled: %t\n  workers: 1\n  cache_renders: true\n", ec.pdf)
	if ec.noAVIFConfig {
		fmt.Fprintf(&b, "thumbnails:\n  avif_enabled: false\n")
	}
	fmt.Fprintf(&b, "log:\n  http_requests: true\n")
	if ec.password != "" {
		fmt.Fprintf(&b, "auth:\n  password: %q\n", ec.password)
	}

	// The configuration file is written to disk, not merely parsed from a
	// string. Amendment A-11 made the file a live surface — the settings
	// endpoint re-hashes it on every read for `config_changed_on_disk`, and
	// `GET /api/roots` reads its `roots:` list for pending rows — so a harness
	// whose `config_path` pointed at a file that was never there would exercise
	// only the error paths of both.
	//
	// `withConfigInsideRoot()` puts it under the media root instead, which is
	// the third condition of §7.4's gate.
	configPath := filepath.Join(e.dir, config.FileName)
	if ec.configInsideRoot {
		configPath = filepath.Join(e.media, config.FileName)
	}
	mustWrite(e.t, configPath, b.String())

	cfg, err := config.Parse([]byte(b.String()), configPath)
	if err != nil {
		e.t.Fatalf("parsing the fixture config: %v", err)
	}
	mustMkdir(e.t, cfg.Storage.DataDir)
	mustMkdir(e.t, cfg.Storage.CacheDir)
	return cfg
}

// configDigest is the fixture configuration file as it was when the harness
// loaded it — what the composition root hands to httpapi.New at startup.
func (e *env) configDigest() string {
	e.t.Helper()
	state, err := config.ReadFileState(e.cfg.AbsFilePath())
	if err != nil {
		e.t.Fatalf("digesting the fixture config: %v", err)
	}
	return state.Digest
}

// configFileRoots is what the configuration file on disk currently says, which
// after a §7.4 write is not what the running server loaded.
func (e *env) configFileRoots() []config.Root {
	e.t.Helper()
	state, err := config.ReadFileState(e.cfg.AbsFilePath())
	if err != nil {
		e.t.Fatalf("reading the fixture config: %v", err)
	}
	return state.Roots
}

func (e *env) openDatabases(ctx context.Context) {
	e.t.Helper()
	var err error
	e.user, err = userdata.Open(ctx, userdata.Options{
		Path:   filepath.Join(e.cfg.Storage.DataDir, "user.db"),
		Logger: discard(),
		Now:    func() time.Time { return fixedNow },
	})
	if err != nil {
		e.t.Fatalf("opening user.db: %v", err)
	}
	e.t.Cleanup(func() { _ = e.user.Close() })

	e.idx, err = index.Open(ctx, index.Options{
		Path:     filepath.Join(e.cfg.Storage.DataDir, "index.db"),
		UserPath: e.user.Path(),
		Logger:   discard(),
	})
	if err != nil {
		e.t.Fatalf("opening index.db: %v", err)
	}
	e.t.Cleanup(func() { _ = e.idx.Close() })
}

// seed writes the fixture library through the real storage API, so the wire
// shapes are produced by the same queries production uses.
func (e *env) seed(ctx context.Context) {
	e.t.Helper()
	t := e.t

	e.seriesFolderID = ids.SeriesID(rootName, seriesFolderPath)
	e.seriesCloverID = ids.SeriesID(rootName, seriesCloverPath)
	e.seriesBrokenID = ids.SeriesID(rootName, seriesBrokenPath)
	e.seriesPDFID = ids.SeriesID(rootName, seriesPDFPath)
	e.bookZipID = ids.BookID(rootName, bookZipPath)
	e.bookDirID = ids.BookID(rootName, bookDirPath)
	e.bookCloverID = ids.BookID(rootName, bookCloverPath)
	e.bookBrokenID = ids.BookID(rootName, bookBrokenPath)
	e.bookPDFID = ids.BookID(rootName, bookPDFPath)

	if err := e.idx.UpsertRoot(ctx, index.Root{
		Name: rootName, Path: e.media, Label: "만화", Enabled: true,
	}); err != nil {
		t.Fatalf("upserting the root: %v", err)
	}

	w := e.idx.Writer(index.WriterOptions{})
	defer func() { _ = w.Close() }()

	addedAt := fixedNow.Add(-30 * 24 * time.Hour).Unix()

	series := []index.Series{{
		ID: e.seriesFolderID, RootName: rootName, RelPath: seriesFolderPath,
		DisplayName: seriesFolderPath, SortKey: natsort.Key(seriesFolderPath),
		SearchKey: strings.ToLower(seriesFolderPath), ChoseongKey: "ㄱㄱ",
		Kind: "folder", BookCount: 2, PageCount: 5, TotalBytes: 4096,
		Mtime: fixedMtime.Unix(), AddedAt: addedAt,
		CoverKind: "file", CoverRelPath: coverRelPath,
		Status: "ok", ScanGen: 1,
	}, {
		ID: e.seriesCloverID, RootName: rootName, RelPath: seriesCloverPath,
		DisplayName: seriesCloverPath, SortKey: natsort.Key(seriesCloverPath),
		SearchKey: strings.ToLower(seriesCloverPath), ChoseongKey: "clover ㅋㄹㅂ",
		Kind: "folder", BookCount: 1, PageCount: 3, TotalBytes: 2048,
		Mtime: fixedMtime.Unix(), AddedAt: addedAt,
		CoverKind: "page", CoverBookID: ids.BookID(rootName, bookCloverPath), CoverPageNo: 1,
		Status: "ok", ScanGen: 1,
	}, {
		ID: e.seriesBrokenID, RootName: rootName, RelPath: seriesBrokenPath,
		DisplayName: seriesBrokenPath, SortKey: natsort.Key(seriesBrokenPath),
		SearchKey: strings.ToLower(seriesBrokenPath), ChoseongKey: "ㅅㅅ",
		Kind: "zip", BookCount: 1, PageCount: 0, TotalBytes: 0,
		Mtime: fixedMtime.Unix(), AddedAt: addedAt,
		Status: "error", Error: "central directory is truncated", ScanGen: 1,
	}, {
		ID: e.seriesPDFID, RootName: rootName, RelPath: seriesPDFPath,
		DisplayName: seriesPDFPath, SortKey: natsort.Key(seriesPDFPath),
		SearchKey: strings.ToLower(seriesPDFPath), ChoseongKey: "ㅁㅅ",
		Kind: "pdf", BookCount: 1, PageCount: pdfPageCount, TotalBytes: 1024,
		Mtime: fixedMtime.Unix(), AddedAt: addedAt,
		Status: "ok", ScanGen: 1,
	}}
	for _, s := range series {
		if err := w.UpsertSeries(ctx, s); err != nil {
			t.Fatalf("upserting series %s: %v", s.RelPath, err)
		}
	}

	// The container's real (size, mtime) matter: openpool compares them with
	// what the index recorded and reports a mismatch as stale, which the page
	// handler turns into `409 stale_version`. Seeding invented numbers would
	// make every page look like a changed file.
	statOf := func(rel string) (int64, int64) {
		fi, err := os.Stat(filepath.Join(e.media, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("stat %s: %v", rel, err)
		}
		return fi.Size(), fi.ModTime().Unix()
	}
	zipSize, zipMtime := statOf(bookZipPath)
	cloverSize, cloverMtime := statOf(bookCloverPath)
	brokenSize, brokenMtime := statOf(bookBrokenPath)
	pdfSize, pdfMtime := statOf(bookPDFPath)

	books := []index.Book{{
		ID: e.bookZipID, SeriesID: e.seriesFolderID, RootName: rootName, RelPath: bookZipPath,
		DisplayName: "군계(軍鷄) 01권.zip", SortKey: natsort.Key("군계(軍鷄) 01권.zip"), Ord: 0,
		Kind: "zip", PageCount: int64(len(e.zipPages)), TotalBytes: 2048,
		FileSize: zipSize, FileMtime: zipMtime,
		ContentVersion: cvZip, DimsState: "partial", Status: "ok", ScanGen: 1,
	}, {
		ID: e.bookDirID, SeriesID: e.seriesFolderID, RootName: rootName, RelPath: bookDirPath,
		DisplayName: "군계(軍鷄) 02권", SortKey: natsort.Key("군계(軍鷄) 02권"), Ord: 1,
		Kind: "dir", PageCount: 2, TotalBytes: 1024,
		FileSize: 0, FileMtime: fixedMtime.Unix(), DirFingerprint: "fp0001",
		ContentVersion: cvDir, DimsState: "none", Status: "ok", ScanGen: 1,
	}, {
		ID: e.bookCloverID, SeriesID: e.seriesCloverID, RootName: rootName, RelPath: bookCloverPath,
		DisplayName: "클로버 01권.zip", SortKey: natsort.Key("클로버 01권.zip"), Ord: 0,
		Kind: "zip", PageCount: int64(len(e.zipPages)), TotalBytes: 2048,
		FileSize: cloverSize, FileMtime: cloverMtime,
		ContentVersion: cvClover, DimsState: "none", Status: "ok", ScanGen: 1,
	}, {
		ID: e.bookBrokenID, SeriesID: e.seriesBrokenID, RootName: rootName, RelPath: bookBrokenPath,
		DisplayName: seriesBrokenPath, SortKey: natsort.Key(seriesBrokenPath), Ord: 0,
		Kind: "zip", PageCount: 0, TotalBytes: 0,
		FileSize: brokenSize, FileMtime: brokenMtime,
		ContentVersion: cvBroken, DimsState: "none",
		Status: "error", Error: "central directory is truncated", ScanGen: 1,
	}, {
		ID: e.bookPDFID, SeriesID: e.seriesPDFID, RootName: rootName, RelPath: bookPDFPath,
		DisplayName: "미생 01권.pdf", SortKey: natsort.Key("미생 01권.pdf"), Ord: 0,
		Kind: "pdf", PageCount: pdfPageCount, TotalBytes: 0,
		FileSize: pdfSize, FileMtime: pdfMtime,
		ContentVersion: cvPDF, DimsState: "none", Status: "ok", ScanGen: 1,
	}}
	for _, b := range books {
		if err := w.UpsertBook(ctx, b); err != nil {
			t.Fatalf("upserting book %s: %v", b.RelPath, err)
		}
	}

	if err := w.ReplacePages(ctx, e.bookZipID, e.zipPages); err != nil {
		t.Fatalf("writing zip pages: %v", err)
	}
	if err := w.ReplacePages(ctx, e.bookCloverID, e.zipPages); err != nil {
		t.Fatalf("writing clover pages: %v", err)
	}
	w120, h120 := 120, 180
	dirPages := []index.Page{{
		PageNo: 1, Name: "01.jpg", EntryPath: "01.jpg", Ext: ".jpg",
		Size: int64(len(e.dirPayloads[0])), Mtime: fixedMtime.Unix(),
		Width: &w120, Height: &h120,
	}, {
		PageNo: 2, Name: "02.jpg", EntryPath: "02.jpg", Ext: ".jpg",
		Size: int64(len(e.dirPayloads[1])), Mtime: fixedMtime.Unix(),
	}}
	if err := w.ReplacePages(ctx, e.bookDirID, dirPages); err != nil {
		t.Fatalf("writing dir pages: %v", err)
	}
	pdfPages := make([]index.Page, 0, pdfPageCount)
	for i := 1; i <= pdfPageCount; i++ {
		pdfPages = append(pdfPages, index.Page{
			PageNo: i, Name: fmt.Sprint(i), Ext: ".jpg", Mtime: fixedMtime.Unix(),
		})
	}
	if err := w.ReplacePages(ctx, e.bookPDFID, pdfPages); err != nil {
		t.Fatalf("writing pdf pages: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flushing the seed writer: %v", err)
	}
	if err := e.idx.RecountRoot(ctx, rootName); err != nil {
		t.Fatalf("recounting the root: %v", err)
	}
	if err := e.idx.MarkRootScanStart(ctx, rootName, fixedStarted.Unix()); err != nil {
		t.Fatalf("marking the scan start: %v", err)
	}
	if err := e.idx.MarkRootScanEnd(ctx, rootName, fixedStarted.Add(32*time.Second).Unix(), ""); err != nil {
		t.Fatalf("marking the scan end: %v", err)
	}
	if err := e.idx.AppendLog(ctx, index.LogEntry{
		TS: fixedStarted.Unix(), RunID: "run-0001", Level: index.LevelWarn,
		Root: rootName, RelPath: seriesBrokenPath,
		Message: "central directory is truncated; the book is listed with status=error",
	}); err != nil {
		t.Fatalf("appending a scan log row: %v", err)
	}

	// A-8: the 최근 추가 window is backed by user.db, so the fixture records a
	// first sighting for exactly one series. The other two have no row and are
	// therefore *excluded* from `scope=added` — that is the intended behaviour
	// (arch §7.5: "a series with no series_seen row is excluded").
	if err := e.user.MarkSeriesSeen(ctx, []userdata.SeriesSeen{{
		SeriesID: e.seriesFolderID, RootName: rootName, SeriesPath: seriesFolderPath,
		FirstSeenAt: fixedNow.Add(-3 * 24 * time.Hour).Unix(),
	}}); err != nil {
		t.Fatalf("marking a series seen: %v", err)
	}

	// One book part-read, so every progress-bearing payload has real numbers.
	if _, err := e.user.PutProgress(ctx, userdata.ProgressUpdate{
		BookID: e.bookZipID, SeriesID: e.seriesFolderID, RootName: rootName,
		BookPath: bookZipPath, Page: 2, PageCount: len(e.zipPages),
	}); err != nil {
		t.Fatalf("seeding progress: %v", err)
	}
}

// --- request helpers -------------------------------------------------------

// do issues a request against the server and returns the recorder.
func (e *env) do(method, target string, body io.Reader, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	r := httptest.NewRequest(method, target, body)
	r.RemoteAddr = "10.0.0.1:54321"
	for _, m := range mutate {
		m(r)
	}
	w := httptest.NewRecorder()
	e.srv.ServeHTTP(w, r)
	return w
}

// get is the common case.
func (e *env) get(target string, mutate ...func(*http.Request)) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(http.MethodGet, target, nil, mutate...)
}

// jsonBody issues a request with a JSON body.
func (e *env) jsonBody(method, target, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.do(method, target, strings.NewReader(body), func(r *http.Request) {
		r.Header.Set("Content-Type", "application/json")
	})
}

// newRecorder and requestFor build a bare request/response pair for the few
// tests that exercise a middleware directly rather than through the router.
func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func requestFor(target string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.RemoteAddr = "10.0.0.1:54321"
	return r
}

// decode unmarshals a response body, failing the test on a bad status.
func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder, wantStatus int) T {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, wantStatus, w.Body.String())
	}
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decoding the response: %v; body: %s", err, w.Body.String())
	}
	return v
}

// errorBody decodes the §7.2 envelope and asserts the status and code.
func errorBody(t *testing.T, w *httptest.ResponseRecorder, wantStatus int, wantCode string) ErrorBody {
	t.Helper()
	if w.Code != wantStatus {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, wantStatus, w.Body.String())
	}
	var env ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("the response is not the §7.2 envelope: %v; body: %s", err, w.Body.String())
	}
	if env.Error.Code != wantCode {
		t.Fatalf("error.code = %q, want %q; body: %s", env.Error.Code, wantCode, w.Body.String())
	}
	if env.Error.Message == "" {
		t.Error("error.message is empty; §7.2 requires a human-readable message")
	}
	return env.Error
}

// --- fakes -----------------------------------------------------------------

// fakeScanner stands in for internal/scanner. A real scan is asynchronous by
// design, so pinning `GET /api/scan/status` against one would be a test of
// timing rather than of the contract; the mapping from a snapshot to the wire
// shape is what this package owns and is what the fake exercises.
type fakeScanner struct {
	status    *scanner.ScanStatus
	startErr  error
	lastReq   scanner.Request
	starts    int
	cancels   int
	runID     string
	cancelled bool
}

func newFakeScanner() *fakeScanner {
	started := fixedStarted.Unix()
	finished := fixedStarted.Add(32 * time.Second).Unix()
	eta := int64(0)
	return &fakeScanner{
		runID: "run-0002",
		status: &scanner.ScanStatus{
			State:       scanner.PhaseIndexing,
			RunID:       "run-0002",
			Full:        true,
			StartedAt:   &started,
			FinishedAt:  &finished,
			Roots:       []string{rootName},
			CurrentRoot: rootName,
			CurrentItem: seriesFolderPath,
			Total:       4,
			Done:        3,
			Errors:      1,
			CoversTotal: 3,
			CoversDone:  2,
			ElapsedMs:   32000,
			ETAMs:       &eta,
			LastError:   "central directory is truncated",
		},
	}
}

func (f *fakeScanner) Start(_ context.Context, req scanner.Request) (string, error) {
	f.starts++
	f.lastReq = req
	if f.startErr != nil {
		return "", f.startErr
	}
	return f.runID, nil
}

func (f *fakeScanner) Cancel() bool {
	f.cancels++
	was := !f.cancelled
	f.cancelled = true
	return was
}

func (f *fakeScanner) Status() *scanner.ScanStatus { return f.status }

// --- small helpers ---------------------------------------------------------

// --- the unprivileged-only cases -------------------------------------------

// requireUnprivilegedEnv is the opt-in that turns "skipped because we are root"
// into a failure.
const requireUnprivilegedEnv = "SHELF_REQUIRE_UNPRIVILEGED"

// unprivilegedVerdict is what a case that can only run as a non-root user does.
type unprivilegedVerdict int

const (
	// runCase: the process cannot read/write past the mode, so the case is real.
	runCase unprivilegedVerdict = iota
	// skipCase: we are root, and nobody asked for that to be an error.
	skipCase
	// failCase: we are root and the environment demanded the coverage.
	failCase
)

// verdictForEUID is the whole decision, as a pure function so it can be tested
// by a suite that is not running as root — which is every suite that would
// notice the difference.
//
// Three cases in this package depend on a permission the caller does not have
// (`a directory the server cannot open`, `a path the server cannot stat`, and
// `TestDeleteRoot_purgesTheIndexBeforeItWritesTheFile`, which makes a YAML write
// fail by taking write permission off its directory). Root has every permission
// whatever the mode says, so those cases cannot be produced and skipping is the
// only honest answer.
//
// The hazard is not the skip, it is the silence: a containerised CI runs as
// uid 0 by default, loses three assertions and still reports green — a check
// watching the wrong thing, which is the shape of most defects that survive
// review here. `SHELF_REQUIRE_UNPRIVILEGED=1` lets such a CI demand the coverage
// it believes it has, and changes nothing for anyone who does not set it.
func verdictForEUID(euid int, require string) unprivilegedVerdict {
	if euid != 0 {
		return runCase
	}
	if require == "1" {
		return failCase
	}
	return skipCase
}

// skipUnlessUnprivileged applies verdictForEUID to the calling test.
func skipUnlessUnprivileged(t *testing.T, why string) {
	t.Helper()
	switch verdictForEUID(os.Geteuid(), os.Getenv(requireUnprivilegedEnv)) {
	case runCase:
		return
	case failCase:
		t.Fatalf("%s, so this case cannot be produced; %s=1 asked for it to run anyway. "+
			"Run the suite as an unprivileged user, or unset the variable to go back to skipping.",
			why, requireUnprivilegedEnv)
	case skipCase:
		t.Skip(why)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
