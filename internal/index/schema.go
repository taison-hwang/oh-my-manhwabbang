package index

// schemaVersion is the DDL revision this build writes and understands. An
// index.db stamped with a *higher* number was written by a newer SHELF and is
// refused (ErrSchemaTooNew) rather than silently mis-read; a lower number is
// migrated forward through the ladder below.
const schemaVersion = 2

// idVersion pins the identifier scheme of arch-backend §3.4. It is duplicated
// here rather than imported from internal/ids on purpose: the storage layer must
// be able to detect that the ids in a *derived* index were produced by a
// different scheme without taking a dependency on the package that produces
// them. A mismatch makes every row in index.db meaningless, so the file is
// rebuilt from scratch (it is disposable — NFR-DAT-001).
const idVersion = "shelf-id/1"

// Meta keys stored in the `meta` table.
const (
	metaSchemaVersion = "schema_version"
	metaIDVersion     = "id_version"
	metaAppVersion    = "app_version"
	metaCreatedAt     = "created_at"
	metaScanGen       = "scan_gen"
)

// migration is one rung of the schema ladder. `to` is the schema_version the
// database carries once `sql` has been applied. Migrations run in order, each
// inside its own transaction, and only when the stored version is below `to`.
type migration struct {
	to  int
	sql string
}

// migrations is the whole migration path, starting from an empty file.
// Appending a rung is the only supported way to change the schema.
var migrations = []migration{
	{to: 1, sql: schemaV1},
	{to: 2, sql: schemaV2},
}

// schemaV1 is the DDL of arch-backend §3.5, verbatim.
//
// Note that `pages` deliberately has no foreign key to `books`: it is
// WITHOUT ROWID and carries 1.36 M rows on the reference collection, and the FK
// index would double its cost. Every deletion path in write.go therefore removes
// the page rows explicitly, and index_test.go asserts no orphan survives.
const schemaV1 = `
CREATE TABLE IF NOT EXISTS meta (
    key    TEXT PRIMARY KEY,
    value  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS roots (
    name            TEXT PRIMARY KEY,
    path            TEXT NOT NULL,
    label           TEXT,
    enabled         INTEGER NOT NULL DEFAULT 1,
    series_count    INTEGER NOT NULL DEFAULT 0,
    book_count      INTEGER NOT NULL DEFAULT 0,
    page_count      INTEGER NOT NULL DEFAULT 0,
    total_bytes     INTEGER NOT NULL DEFAULT 0,
    last_scan_start INTEGER,
    last_scan_end   INTEGER,
    last_scan_error TEXT
);

CREATE TABLE IF NOT EXISTS series (
    id              TEXT PRIMARY KEY,
    root_name       TEXT NOT NULL REFERENCES roots(name) ON DELETE CASCADE,
    rel_path        TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    sort_key        BLOB NOT NULL,
    search_key      TEXT NOT NULL,
    choseong_key    TEXT NOT NULL,
    kind            TEXT NOT NULL,
    book_count      INTEGER NOT NULL DEFAULT 0,
    page_count      INTEGER NOT NULL DEFAULT 0,
    total_bytes     INTEGER NOT NULL DEFAULT 0,
    mtime           INTEGER NOT NULL,
    added_at        INTEGER NOT NULL,
    cover_kind      TEXT,
    cover_book_id   TEXT,
    cover_page_no   INTEGER,
    cover_rel_path  TEXT,
    status          TEXT NOT NULL DEFAULT 'ok',
    error           TEXT,
    scan_gen        INTEGER NOT NULL,
    UNIQUE (root_name, rel_path)
);
CREATE INDEX IF NOT EXISTS ix_series_root_sort  ON series(root_name, sort_key);
CREATE INDEX IF NOT EXISTS ix_series_sort       ON series(sort_key);
CREATE INDEX IF NOT EXISTS ix_series_mtime      ON series(mtime DESC);
CREATE INDEX IF NOT EXISTS ix_series_added      ON series(added_at DESC);
CREATE INDEX IF NOT EXISTS ix_series_bytes      ON series(total_bytes DESC);
CREATE INDEX IF NOT EXISTS ix_series_books      ON series(book_count DESC);
CREATE INDEX IF NOT EXISTS ix_series_search     ON series(search_key);
CREATE INDEX IF NOT EXISTS ix_series_gen        ON series(scan_gen);

CREATE TABLE IF NOT EXISTS books (
    id              TEXT PRIMARY KEY,
    series_id       TEXT NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    root_name       TEXT NOT NULL,
    rel_path        TEXT NOT NULL,
    display_name    TEXT NOT NULL,
    sort_key        BLOB NOT NULL,
    ord             INTEGER NOT NULL,
    kind            TEXT NOT NULL,
    page_count      INTEGER NOT NULL DEFAULT 0,
    total_bytes     INTEGER NOT NULL DEFAULT 0,
    file_size       INTEGER NOT NULL DEFAULT 0,
    file_mtime      INTEGER NOT NULL DEFAULT 0,
    dir_fingerprint TEXT,
    content_version TEXT NOT NULL,
    dims_state      TEXT NOT NULL DEFAULT 'none',
    status          TEXT NOT NULL DEFAULT 'ok',
    error           TEXT,
    scan_gen        INTEGER NOT NULL,
    UNIQUE (root_name, rel_path)
);
CREATE INDEX IF NOT EXISTS ix_books_series ON books(series_id, ord);
CREATE INDEX IF NOT EXISTS ix_books_gen    ON books(scan_gen);
CREATE INDEX IF NOT EXISTS ix_books_status ON books(status) WHERE status <> 'ok';

CREATE TABLE IF NOT EXISTS pages (
    book_id       TEXT NOT NULL,
    page_no       INTEGER NOT NULL,
    name          TEXT NOT NULL,
    entry_path    TEXT NOT NULL,
    ext           TEXT NOT NULL,
    size          INTEGER NOT NULL,
    comp_size     INTEGER NOT NULL DEFAULT 0,
    method        INTEGER NOT NULL DEFAULT 0,
    local_hdr_off INTEGER NOT NULL DEFAULT 0,
    crc32         INTEGER NOT NULL DEFAULT 0,
    mtime         INTEGER NOT NULL DEFAULT 0,
    width         INTEGER,
    height        INTEGER,
    PRIMARY KEY (book_id, page_no)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS scan_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         INTEGER NOT NULL,
    run_id     TEXT NOT NULL,
    level      TEXT NOT NULL,
    root_name  TEXT,
    rel_path   TEXT,
    message    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_scanlog_ts  ON scan_log(id DESC);
CREATE INDEX IF NOT EXISTS ix_scanlog_run ON scan_log(run_id, id DESC);
`

// schemaV2 adds books.inner_path: the path of a book *inside* its container,
// which is what makes a nested archive's volumes real books (prd §7.2 as
// widened for the 45 container-of-ZIPs books in the collection).
//
// It is empty for every ordinary book, and that is the whole compatibility
// story — the rows migrated below get `''` and mean exactly what they meant.
//
// The table has to be rebuilt rather than ALTERed because the uniqueness rule
// changes: 39 volumes of `겟 벡커스 1~39완.zip` share one (root_name, rel_path)
// and are told apart only by inner_path. SQLite cannot drop a table-level
// UNIQUE in place, so this is the standard 12-step rebuild, minus the steps
// that only matter with foreign keys pointing *at* this table — `pages` has
// none by design (see schemaV1's comment).
//
// Nothing needs re-scanning after this runs. The books whose volumes are about
// to appear are all `status='empty'` today, and scanner.unchanged never skips a
// book whose status is not 'ok'.
const schemaV2 = `
CREATE TABLE books_v2 (
    id              TEXT PRIMARY KEY,
    series_id       TEXT NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    root_name       TEXT NOT NULL,
    rel_path        TEXT NOT NULL,
    inner_path      TEXT NOT NULL DEFAULT '',
    display_name    TEXT NOT NULL,
    sort_key        BLOB NOT NULL,
    ord             INTEGER NOT NULL,
    kind            TEXT NOT NULL,
    page_count      INTEGER NOT NULL DEFAULT 0,
    total_bytes     INTEGER NOT NULL DEFAULT 0,
    file_size       INTEGER NOT NULL DEFAULT 0,
    file_mtime      INTEGER NOT NULL DEFAULT 0,
    dir_fingerprint TEXT,
    content_version TEXT NOT NULL,
    dims_state      TEXT NOT NULL DEFAULT 'none',
    status          TEXT NOT NULL DEFAULT 'ok',
    error           TEXT,
    scan_gen        INTEGER NOT NULL,
    UNIQUE (root_name, rel_path, inner_path)
);

INSERT INTO books_v2 (id, series_id, root_name, rel_path, inner_path, display_name,
                      sort_key, ord, kind, page_count, total_bytes, file_size,
                      file_mtime, dir_fingerprint, content_version, dims_state,
                      status, error, scan_gen)
     SELECT id, series_id, root_name, rel_path, '', display_name,
            sort_key, ord, kind, page_count, total_bytes, file_size,
            file_mtime, dir_fingerprint, content_version, dims_state,
            status, error, scan_gen
       FROM books;

DROP TABLE books;
ALTER TABLE books_v2 RENAME TO books;

CREATE INDEX IF NOT EXISTS ix_books_series ON books(series_id, ord);
CREATE INDEX IF NOT EXISTS ix_books_gen    ON books(scan_gen);
CREATE INDEX IF NOT EXISTS ix_books_status ON books(status) WHERE status <> 'ok';
`

// dropAllSQL removes every object schemaV1 creates. It backs Reset, the
// in-process half of FR-IDX-005 (`--rebuild-index`); the offline half is
// Destroy, which unlinks the three index.db files by name.
const dropAllSQL = `
DROP TABLE IF EXISTS scan_log;
DROP TABLE IF EXISTS pages;
DROP TABLE IF EXISTS books;
DROP TABLE IF EXISTS series;
DROP TABLE IF EXISTS roots;
DROP TABLE IF EXISTS meta;
`
