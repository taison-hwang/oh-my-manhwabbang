package index

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Default batching thresholds. impl-plan WP-03 acceptance 5: commit every 200
// books or 2 s. Small enough that a concurrent dimension fill or scan-log append
// never waits long for the write permit; large enough that the spike's 200 k
// inserts in 1.67 s class is preserved.
const (
	defaultBatchBooks = 200
	defaultBatchAge   = 2 * time.Second
)

// Writer is the single-writer transactional API the scanner uses. It owns the
// process write permit while a transaction is open and releases it on every
// commit, so readers and the dimension filler interleave at batch boundaries.
//
// A Writer is NOT safe for concurrent use — that is the point. Create one per
// scan run and Close it.
//
// One rule, and it is load-bearing: while a Writer has an uncommitted batch, the
// goroutine driving it must not call a DB-level write (UpsertRoot, UpdateDims,
// DeleteBook, …). Those take the same single write permit, so the call would
// wait for a commit that only this goroutine can make. Route the write through
// the Writer, or Flush first.
//
// Be precise about what a mistake costs: acquireWrite honours the caller's
// context, so it ends in a deadline only if that context *has* one. A scan
// context typically does not, and then the goroutine blocks forever. acquireWrite
// logs a warning every 30 s of waiting so the hang is at least visible in the
// log rather than silent.
//
// The one call FR-IDX-010 forces into the middle of a batch — one scan_log warn
// row per isolated failure — therefore has a Writer method of its own
// (Writer.AppendLog), so the scanner never needs the DB-level form mid-scan.
type Writer struct {
	db  *DB
	tx  *sql.Tx
	rel func()

	batchBooks  int
	batchAge    time.Duration
	pending     int
	afterCommit []func()
	openedAt    time.Time

	insertPageChunk *sql.Stmt
	insertPageOne   *sql.Stmt
	closed          bool
}

// WriterOptions tunes the commit cadence. Zero values select the defaults.
type WriterOptions struct {
	BatchBooks int
	BatchAge   time.Duration
}

// Writer returns a new single-writer handle.
func (db *DB) Writer(opts WriterOptions) *Writer {
	w := &Writer{db: db, batchBooks: opts.BatchBooks, batchAge: opts.BatchAge}
	if w.batchBooks <= 0 {
		w.batchBooks = defaultBatchBooks
	}
	if w.batchAge <= 0 {
		w.batchAge = defaultBatchAge
	}
	return w
}

// begin opens a transaction if none is open, taking the write permit.
func (w *Writer) begin(ctx context.Context) error {
	if w.closed {
		return ErrWriterClosed
	}
	if w.tx != nil {
		return nil
	}
	rel, err := w.db.acquireWrite(ctx)
	if err != nil {
		return err
	}
	tx, err := w.db.wconn.BeginTx(ctx, nil)
	if err != nil {
		rel()
		return fmt.Errorf("beginning scan transaction: %w", err)
	}
	w.tx, w.rel, w.openedAt, w.pending = tx, rel, time.Now(), 0
	return nil
}

// maybeCommit flushes when the batch is full or stale.
func (w *Writer) maybeCommit(ctx context.Context) error {
	if w.tx == nil {
		return nil
	}
	if w.pending >= w.batchBooks || time.Since(w.openedAt) >= w.batchAge {
		return w.Flush(ctx)
	}
	return nil
}

// AfterCommit registers fn to run once the rows written so far are visible to
// other connections — that is, after the open batch commits, or immediately when
// no batch is open because they already are.
//
// It exists for callers that publish work *about* rows they have just written.
// The scanner's cover enqueue (FR-THM-003) is the motivating one: the thumbnail
// worker resolves a page cover by reading `books`/`pages` back through the read
// pool, and mid-batch those rows do not exist there yet, so an enqueue made
// inside the batch fails with [ErrNotFound] — once per width, with no retry —
// for every series whose cover comes from a page rather than a loose file.
//
// Callbacks run in registration order, on the goroutine driving the Writer, and
// only after Commit has actually succeeded. A rollback (or a failed commit)
// discards them, which is precisely right: the rows they describe do not exist.
//
// A callback must not call back into this Writer — it runs while the batch is
// already closed but Flush has not yet returned.
func (w *Writer) AfterCommit(fn func()) {
	if fn == nil {
		return
	}
	if w.tx == nil {
		fn()
		return
	}
	w.afterCommit = append(w.afterCommit, fn)
}

// Flush commits the open transaction, if any, and releases the write permit.
func (w *Writer) Flush(_ context.Context) error {
	if w.tx == nil {
		return nil
	}
	tx, rel, hooks := w.tx, w.rel, w.afterCommit
	w.tx, w.rel, w.pending, w.afterCommit = nil, nil, 0, nil
	w.closePageStmts()
	err := tx.Commit()
	rel()
	if err != nil {
		return fmt.Errorf("committing scan transaction: %w", err)
	}
	for _, fn := range hooks {
		fn()
	}
	return nil
}

// rollback aborts the open transaction and releases the permit. Used when a
// statement inside the batch fails: the batch is atomic, so a partially applied
// series is never visible.
func (w *Writer) rollback() {
	if w.tx == nil {
		return
	}
	tx, rel := w.tx, w.rel
	// The AfterCommit hooks go with the batch: they describe rows that are
	// about to stop existing.
	w.tx, w.rel, w.pending, w.afterCommit = nil, nil, 0, nil
	w.closePageStmts()
	_ = tx.Rollback()
	rel()
}

// Close flushes and invalidates the Writer. Calling it twice is safe.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	err := w.Flush(context.Background())
	if err != nil {
		w.rollback()
	}
	w.closed = true
	return err
}

// exec runs one statement inside the batch, rolling the batch back on failure.
func (w *Writer) exec(ctx context.Context, query string, args ...any) error {
	if err := w.begin(ctx); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, query, args...); err != nil {
		w.rollback()
		return err
	}
	return nil
}

const upsertSeriesSQL = `
INSERT INTO series (id, root_name, rel_path, display_name, sort_key, search_key,
    choseong_key, kind, book_count, page_count, total_bytes, mtime, added_at,
    cover_kind, cover_book_id, cover_page_no, cover_rel_path, status, error, scan_gen)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
    root_name      = excluded.root_name,
    rel_path       = excluded.rel_path,
    display_name   = excluded.display_name,
    sort_key       = excluded.sort_key,
    search_key     = excluded.search_key,
    choseong_key   = excluded.choseong_key,
    kind           = excluded.kind,
    book_count     = excluded.book_count,
    page_count     = excluded.page_count,
    total_bytes    = excluded.total_bytes,
    mtime          = excluded.mtime,
    added_at       = min(series.added_at, excluded.added_at),
    cover_kind     = excluded.cover_kind,
    cover_book_id  = excluded.cover_book_id,
    cover_page_no  = excluded.cover_page_no,
    cover_rel_path = excluded.cover_rel_path,
    status         = excluded.status,
    error          = excluded.error,
    scan_gen       = excluded.scan_gen`

// UpsertSeries inserts or refreshes a series row. added_at is never moved
// forward: the first sighting is what "최근 추가" means.
func (w *Writer) UpsertSeries(ctx context.Context, s Series) error {
	err := w.exec(ctx, upsertSeriesSQL,
		s.ID, s.RootName, s.RelPath, s.DisplayName, s.SortKey, s.SearchKey,
		s.ChoseongKey, s.Kind, s.BookCount, s.PageCount, s.TotalBytes, s.Mtime, s.AddedAt,
		nullString(s.CoverKind), nullString(s.CoverBookID), nullInt(s.CoverPageNo),
		nullString(s.CoverRelPath), s.Status, nullString(s.Error), s.ScanGen)
	if err != nil {
		return fmt.Errorf("upserting series %q: %w", s.ID, err)
	}
	return w.maybeCommit(ctx)
}

const upsertBookSQL = `
INSERT INTO books (id, series_id, root_name, rel_path, inner_path, display_name, sort_key, ord,
    kind, page_count, total_bytes, file_size, file_mtime, dir_fingerprint,
    content_version, dims_state, status, error, scan_gen)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
    series_id       = excluded.series_id,
    root_name       = excluded.root_name,
    rel_path        = excluded.rel_path,
    inner_path      = excluded.inner_path,
    display_name    = excluded.display_name,
    sort_key        = excluded.sort_key,
    ord             = excluded.ord,
    kind            = excluded.kind,
    page_count      = excluded.page_count,
    total_bytes     = excluded.total_bytes,
    file_size       = excluded.file_size,
    file_mtime      = excluded.file_mtime,
    dir_fingerprint = excluded.dir_fingerprint,
    content_version = excluded.content_version,
    dims_state      = excluded.dims_state,
    status          = excluded.status,
    error           = excluded.error,
    scan_gen        = excluded.scan_gen`

// UpsertBook inserts or refreshes a book row and counts towards the batch.
func (w *Writer) UpsertBook(ctx context.Context, b Book) error {
	dims := b.DimsState
	if dims == "" {
		dims = "none"
	}
	err := w.exec(ctx, upsertBookSQL,
		b.ID, b.SeriesID, b.RootName, b.RelPath, b.InnerPath, b.DisplayName, b.SortKey, b.Ord,
		b.Kind, b.PageCount, b.TotalBytes, b.FileSize, b.FileMtime,
		nullString(b.DirFingerprint), b.ContentVersion, dims, b.Status,
		nullString(b.Error), b.ScanGen)
	if err != nil {
		return fmt.Errorf("upserting book %q: %w", b.ID, err)
	}
	w.pending++
	return w.maybeCommit(ctx)
}

// pageColumnCount is the arity of the pages insert; pageChunk rows go into one
// statement. 50 × 13 = 650 bound parameters, comfortably below SQLite's
// conservative 999-variable floor, and it cuts the per-statement round trip of a
// 1 000-page archive by a factor of fifty.
const (
	pageColumnCount = 13
	pageChunk       = 50
)

var (
	insertPageOneSQL   = insertPagesSQL(1)
	insertPageChunkSQL = insertPagesSQL(pageChunk)
)

func insertPagesSQL(rows int) string {
	var b strings.Builder
	b.WriteString(`INSERT INTO pages (book_id, page_no, name, entry_path, ext, size,
		comp_size, method, local_hdr_off, crc32, mtime, width, height) VALUES `)
	row := "(" + placeholders(pageColumnCount) + ")"
	for i := range rows {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(row)
	}
	return b.String()
}

func appendPageArgs(dst []any, bookID string, no int, p Page) []any {
	return append(dst, bookID, no, p.Name, p.EntryPath, p.Ext, p.Size, p.CompSize,
		p.Method, p.LocalHdrOff, int64(p.CRC32), p.Mtime, p.Width, p.Height)
}

// ReplacePages swaps a book's whole page list atomically: delete, then a batched
// multi-row insert through prepared statements, inside the running batch
// transaction. Page numbers are assigned positionally from the slice order
// (1-based), so the caller sorts once and the storage layer never re-sorts.
func (w *Writer) ReplacePages(ctx context.Context, bookID string, pages []Page) error {
	if err := w.begin(ctx); err != nil {
		return err
	}
	if _, err := w.tx.ExecContext(ctx, `DELETE FROM pages WHERE book_id = ?`, bookID); err != nil {
		w.rollback()
		return fmt.Errorf("clearing pages of book %q: %w", bookID, err)
	}
	if len(pages) == 0 {
		return w.maybeCommit(ctx)
	}
	if err := w.preparePageStmts(ctx); err != nil {
		return err
	}

	args := make([]any, 0, pageChunk*pageColumnCount)
	for start := 0; start < len(pages); start += pageChunk {
		end := min(start+pageChunk, len(pages))
		args = args[:0]
		for i := start; i < end; i++ {
			no := pages[i].PageNo
			if no == 0 {
				no = i + 1
			}
			args = appendPageArgs(args, bookID, no, pages[i])
		}
		if end-start == pageChunk {
			if _, err := w.insertPageChunk.ExecContext(ctx, args...); err != nil {
				w.rollback()
				return fmt.Errorf("inserting pages %d..%d of book %q: %w", start+1, end, bookID, err)
			}
			continue
		}
		// The tail is inserted a row at a time rather than preparing a
		// statement for every possible odd length.
		for i := range end - start {
			one := args[i*pageColumnCount : (i+1)*pageColumnCount]
			if _, err := w.insertPageOne.ExecContext(ctx, one...); err != nil {
				w.rollback()
				return fmt.Errorf("inserting page %d of book %q: %w", start+i+1, bookID, err)
			}
		}
	}
	return w.maybeCommit(ctx)
}

func (w *Writer) preparePageStmts(ctx context.Context) error {
	if w.insertPageChunk != nil {
		return nil
	}
	chunk, err := w.tx.PrepareContext(ctx, insertPageChunkSQL)
	if err != nil {
		w.rollback()
		return fmt.Errorf("preparing page insert: %w", err)
	}
	one, err := w.tx.PrepareContext(ctx, insertPageOneSQL)
	if err != nil {
		_ = chunk.Close()
		w.rollback()
		return fmt.Errorf("preparing page insert: %w", err)
	}
	w.insertPageChunk, w.insertPageOne = chunk, one
	return nil
}

func (w *Writer) closePageStmts() {
	if w.insertPageChunk != nil {
		_ = w.insertPageChunk.Close()
		w.insertPageChunk = nil
	}
	if w.insertPageOne != nil {
		_ = w.insertPageOne.Close()
		w.insertPageOne = nil
	}
}

// idChunk bounds how many ids go into one `IN (?,…)` list, for the same reason
// pageChunk bounds the page insert: SQLite's bound-variable ceiling is a
// compile-time constant (32 766 in the build modernc.org/sqlite ships, 999 in
// older ones) and crossing it is a hard "too many SQL variables" error.
//
// This list is the one that grows with the library. StampGen is handed one id
// per *unchanged* book, so a no-change rescan of the reference collection passes
// ~11 000 today and prd §5 sizes the target at thousands of series in total —
// and a StampGen that failed while the caller carried on would leave SweepRoot
// deleting every row it should have stamped.
const idChunk = 400

// StampGen moves rows forward to the current generation without rewriting them.
// This is the incremental-scan path (FR-IDX-003): an unchanged archive is never
// re-read, but it must not look stale to SweepRoot.
//
// The id lists are unbounded by contract, so they are issued in chunks — all of
// them inside the one batch transaction, which keeps the stamping atomic with
// respect to a concurrent reader exactly as a single statement would be.
func (w *Writer) StampGen(ctx context.Context, gen int64, seriesIDs, bookIDs []string) error {
	if err := w.stampTable(ctx, "series", gen, seriesIDs); err != nil {
		return fmt.Errorf("stamping series generation: %w", err)
	}
	if err := w.stampTable(ctx, "books", gen, bookIDs); err != nil {
		return fmt.Errorf("stamping book generation: %w", err)
	}
	return w.maybeCommit(ctx)
}

// stampTable updates one table's scan_gen in idChunk-sized batches. table is a
// package-internal literal, never caller input.
func (w *Writer) stampTable(ctx context.Context, table string, gen int64, ids []string) error {
	args := make([]any, 0, idChunk+1)
	for start := 0; start < len(ids); start += idChunk {
		end := min(start+idChunk, len(ids))
		args = append(args[:0], any(gen))
		for _, id := range ids[start:end] {
			args = append(args, id)
		}
		if err := w.exec(ctx,
			`UPDATE `+table+` SET scan_gen = ? WHERE id IN (`+placeholders(end-start)+`)`,
			args...); err != nil {
			return err
		}
	}
	return nil
}

// AppendLog writes scan-log rows through the open batch transaction, so the
// scanner can record FR-IDX-010's one-warn-row-per-failure without leaving the
// batch — which, from the goroutine holding it, would deadlock on the write
// permit rather than merely being slow.
//
// The rows are part of the batch: a batch that rolls back takes its log rows
// with it, together with the book rows they describe. That is the intended
// coupling — a scan_log row about a book row that was never committed would be
// the misleading half of the pair.
func (w *Writer) AppendLog(ctx context.Context, entries ...LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	for _, e := range entries {
		if err := w.exec(ctx, insertScanLogSQL, e.TS, e.RunID, e.Level,
			nullString(e.Root), nullString(e.RelPath), e.Message); err != nil {
			return fmt.Errorf("appending scan-log entry: %w", err)
		}
	}
	return w.maybeCommit(ctx)
}

// SweepResult counts what SweepRoot removed.
type SweepResult struct {
	Series int64
	Books  int64
	Pages  int64
}

// SweepRoot deletes everything in one root left behind at an older generation
// (arch §4.9). It runs in its own transaction, after Flush, because it is the
// destructive step and must never be half-applied.
//
// The caller must not call it for a root whose scan aborted: an unmounted drive
// must not silently erase a third of the library.
func (w *Writer) SweepRoot(ctx context.Context, rootName string, gen int64) (SweepResult, error) {
	if err := w.Flush(ctx); err != nil {
		return SweepResult{}, err
	}
	var res SweepResult
	err := w.db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// Pages carry no foreign key, so they go first — and the predicate must
		// also catch books whose *series* is about to be cascade-deleted.
		r, err := tx.ExecContext(ctx, `
			DELETE FROM pages WHERE book_id IN (
				SELECT b.id FROM books b
				LEFT JOIN series s ON s.id = b.series_id
				WHERE b.root_name = ? AND (b.scan_gen < ? OR s.scan_gen < ? OR s.id IS NULL))`,
			rootName, gen, gen)
		if err != nil {
			return fmt.Errorf("sweeping pages of root %q: %w", rootName, err)
		}
		res.Pages, _ = r.RowsAffected()

		r, err = tx.ExecContext(ctx,
			`DELETE FROM books WHERE root_name = ? AND scan_gen < ?`, rootName, gen)
		if err != nil {
			return fmt.Errorf("sweeping books of root %q: %w", rootName, err)
		}
		res.Books, _ = r.RowsAffected()

		r, err = tx.ExecContext(ctx,
			`DELETE FROM series WHERE root_name = ? AND scan_gen < ?`, rootName, gen)
		if err != nil {
			return fmt.Errorf("sweeping series of root %q: %w", rootName, err)
		}
		res.Series, _ = r.RowsAffected()
		return nil
	})
	if err != nil {
		return SweepResult{}, err
	}
	return res, nil
}

// DeleteBook removes one book and its pages. Used by the per-series rescan of
// UI-002 when a volume disappears.
func (db *DB) DeleteBook(ctx context.Context, bookID string) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM pages WHERE book_id = ?`, bookID); err != nil {
			return fmt.Errorf("deleting pages of book %q: %w", bookID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, bookID); err != nil {
			return fmt.Errorf("deleting book %q: %w", bookID, err)
		}
		return nil
	})
}

// DeleteSeries removes a series, its books and their pages.
func (db *DB) DeleteSeries(ctx context.Context, seriesID string) error {
	return db.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM pages WHERE book_id IN (SELECT id FROM books WHERE series_id = ?)`,
			seriesID); err != nil {
			return fmt.Errorf("deleting pages of series %q: %w", seriesID, err)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM series WHERE id = ?`, seriesID); err != nil {
			return fmt.Errorf("deleting series %q: %w", seriesID, err)
		}
		return nil
	})
}
