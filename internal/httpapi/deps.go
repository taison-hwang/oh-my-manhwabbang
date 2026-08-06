// Package httpapi is the whole of arch-backend §7 — the frozen HTTP contract —
// plus the security, caching and logging rules of §5.3, §8 and §9.
//
// # The contract is the specification, not this code
//
// `arch-backend.md` §7.1–§7.13 as amended by `impl-plan.md` §0.3 (A-1…A-10) is
// normative. The frontend in `web/src/api/` was written against the same
// document, in parallel, with no contact between the two. The reconciliation
// artefact is `testdata/golden/*.json`: every response shape this package can
// produce is captured there, and WP-13 diffs those files against
// `web/src/api/types.ts`. If the two disagree, the specification decides which
// side is wrong.
//
// Five rules are restated because they are the ones most often gotten wrong
// (impl-plan §4):
//
//  1. **Every page number is 1-based.** There is no page 0 anywhere.
//  2. Every page/thumb/cover URL carries `?v={cv}`. With it the response is
//     `immutable` for a year; without it, cacheable for 60 s (§5.3).
//  3. `202` is a normal answer for a cover or a thumbnail — "queued, retry
//     after `Retry-After`" — not an error.
//  4. A book with `status != "ok"` answers **200** with `pages: []` and a
//     populated `error`. The UI needs the reason to render its badge.
//  5. Unknown JSON *body* fields are `400`; unknown *query* parameters are
//     ignored.
//
// # Path traversal is impossible by construction (NFR-SEC-001)
//
// No handler ever accepts a filesystem path. Every route takes an opaque
// 16-character identifier which is resolved through the index to a
// (root name, root-relative path) pair this program itself produced, and every
// open goes through that root's *os.Root. `pages/{n}` takes an integer index,
// never an entry name, so a ZIP member legitimately called `../../etc/passwd`
// is a display string and nothing more. See arch §8.1 for all four layers.
//
// # Dependencies are declared here, by the consumer
//
// The interfaces below are the narrow slices of internal/index, internal/userdata,
// internal/scanner, internal/thumbs and internal/source this package uses.
// Those packages return concrete types and know nothing about HTTP; declaring
// the seam on this side is what let waves 1 and 2 compile and ship before this
// package existed (impl-plan §5.1, decision D-46).
package httpapi

import (
	"context"
	"os"

	"shelf/internal/config"
	"shelf/internal/index"
	"shelf/internal/openpool"
	"shelf/internal/scanner"
	"shelf/internal/source"
	"shelf/internal/thumbs"
	"shelf/internal/userdata"
)

// Index is the catalogue. *index.DB satisfies it.
type Index interface {
	ListRoots(ctx context.Context) ([]index.Root, error)
	// DeleteRoot purges one root and everything derived from it — series, books
	// and pages — in a single transaction. It is here for `DELETE /api/roots`
	// (amendment A-11, revision R1): a root the user removed on purpose must
	// stop being in the library, and `index.db` is the derived, disposable half
	// (arch §3.5). It touches nothing in `user.db`, so reading progress survives
	// the removal and reattaches if the same directory is added again.
	DeleteRoot(ctx context.Context, name string) error
	// UpsertRoot writes one `roots` row. It is here for the hot add of amendment
	// A-12 (ruling E-40): the moment `POST /api/roots` opens a root into the
	// running server, that root stops being "in the file but not in this
	// process" and must stop rendering as `pending`. The row it writes carries
	// zero counts, which is true — nothing has been scanned yet — and the scan
	// this handler starts fills them in.
	UpsertRoot(ctx context.Context, r index.Root) error
	ListSeries(ctx context.Context, f index.SeriesFilter) (index.SeriesList, error)
	GetSeries(ctx context.Context, id string) (index.SeriesDetail, error)
	GetBook(ctx context.Context, id string) (index.BookRow, error)
	ListPages(ctx context.Context, bookID string) ([]index.Page, error)
	GetPage(ctx context.Context, bookID string, pageNo int) (index.Page, error)
	Neighbours(ctx context.Context, id string) (prev, next string, err error)
	ListContinue(ctx context.Context, limit int) ([]index.ContinueItem, error)
	ListLog(ctx context.Context, f index.LogFilter) ([]index.LogEntry, error)
}

// UserData is the authored half: progress, per-book preferences and settings.
// *userdata.DB satisfies it.
type UserData interface {
	GetProgress(ctx context.Context, bookID string) (userdata.Progress, error)
	PutProgress(ctx context.Context, u userdata.ProgressUpdate) (userdata.Progress, error)
	DeleteProgress(ctx context.Context, bookID string) error
	GetPrefs(ctx context.Context, bookID string) (userdata.Prefs, error)
	PutPrefs(ctx context.Context, bookID string, patch userdata.PrefsPatch) (userdata.Prefs, error)
	Settings() *userdata.KV
	Export(ctx context.Context) (userdata.Export, error)
	Import(ctx context.Context, doc userdata.Export, strategy userdata.ImportStrategy) (userdata.ImportResult, error)
}

// Scanner runs and reports scans. *scanner.Scanner satisfies it.
type Scanner interface {
	Start(ctx context.Context, req scanner.Request) (string, error)
	Cancel() bool
	Status() *scanner.ScanStatus
	// AddConfigRoot makes a root added since startup selectable by name
	// (amendment A-12, ruling E-40). Without it the scan this handler starts
	// immediately after the add answers ErrUnknownRoot, because the scanner's
	// copy of `roots:` is the one it was constructed with.
	AddConfigRoot(r config.Root)
}

// Thumbs is the derived-image cache. *thumbs.Service satisfies it.
//
// Note what is absent: thumbs.Service.Generate. It blocks until a decode
// finishes, and a handler that called it would hold a connection for the length
// of an archive read. The HTTP layer only ever calls Get, which answers
// immediately with a result, ErrQueued (→ 202) or ErrUndecodable (→ 422).
type Thumbs interface {
	Get(ctx context.Context, req thumbs.Request) (thumbs.Result, error)
	Widths() []int
	Usage(ctx context.Context) (thumbs.Usage, error)
	Purge(ctx context.Context, kind string) (thumbs.PurgeResult, error)
	LookupPDFPage(bookID string, pageNo, width int, contentVersion string) (thumbs.Result, bool)
	StorePDFPage(bookID string, pageNo, width int, contentVersion string, data []byte) (thumbs.Result, error)
	EnsureDims(bookID string)
	CacheDir() string
	Stats() thumbs.Stats
}

// Sources opens a book so one page's bytes can be streamed. *source.Factory
// satisfies it.
type Sources interface {
	Open(ctx context.Context, b source.Book) (source.BookSource, error)
}

// Roots resolves a configured root name to its *os.Root — path-traversal
// layer 3 (arch §8.1). *source.RootSet satisfies it.
//
// It answers `Root.available` in GET /api/roots without this package ever
// handling an absolute path itself, and since amendment A-12 it also opens one.
type Roots interface {
	Root(name string) (*os.Root, bool)
	// Add opens one more root into the live set (ruling E-40). This is the one
	// place the package hands a filesystem path *downwards*, and it is not a
	// weakening of NFR-SEC-001 layer 1: the path has already been through §7.4's
	// full validation table, and what comes back is an *os.Root — a handle that
	// refuses an escape at the openat(2) level exactly like every other root.
	Add(name, path string) error
}

// HandlePool is the archive file-handle LRU, for `GET /api/health?verbose=1`.
// *openpool.Pool satisfies it. It is optional — a Server built without one
// simply reports no pool counters.
type HandlePool interface {
	Stats() openpool.Stats
}
