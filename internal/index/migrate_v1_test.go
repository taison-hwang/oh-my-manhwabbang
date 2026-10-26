package index_test

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"

	"shelf/internal/index"
	"shelf/internal/userdata"
)

// v1BookRow is one books row, every column the v2 rung copies plus the two it
// introduces (inner_path at v2, name_encoding at v3 — ruling E-56).
type v1BookRow struct {
	ID, SeriesID, RootName, RelPath, DisplayName string
	SortKey                                      []byte
	Ord                                          int64
	Kind                                         string
	PageCount, TotalBytes, FileSize, FileMtime   int64
	DirFingerprint                               sql.NullString
	ContentVersion, DimsState, Status            string
	Error                                        sql.NullString
	ScanGen                                      int64
	InnerPath                                    string
	NameEncoding                                 sql.NullString
}

// A genuine schema-v1 index.db — schemaV1 applied to an empty file, stamped
// schema_version 1 under the current id scheme — climbs the whole ladder on
// Open and keeps every books row with every value the v2 rebuild copies.
//
// The v2 rung rebuilds `books` (CREATE books_v2, INSERT … SELECT, DROP,
// RENAME) because the UNIQUE key widens; schema.go says "Nothing needs
// re-scanning after this runs", which holds only if the copy is complete. The
// rows below use a non-default value in every column, and the two books differ
// in every column, so a dropped row, a column replaced by its default or a
// column copied into its neighbour all show.
func TestOpen_v1Index_migratesToV3KeepingEveryBookColumn(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dir := t.TempDir()
	idxPath := filepath.Join(dir, "index.db")
	userPath := filepath.Join(dir, "user.db")

	ud, err := userdata.Open(ctx, userdata.Options{Path: userPath, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("userdata.Open: %v", err)
	}
	t.Cleanup(func() { _ = ud.Close() })

	want := []v1BookRow{
		{
			ID: "bkA0000000000000", SeriesID: "sA00000000000000", RootName: "manga",
			RelPath: "S/1.zip", DisplayName: "1권", SortKey: []byte{0x00, 0xff, 0x01},
			Ord: 3, Kind: "zip", PageCount: 41, TotalBytes: 1_234_567, FileSize: 1_300_000,
			FileMtime: 1_690_000_001, ContentVersion: "cv-a", DimsState: "done",
			Status: "ok", ScanGen: 7,
		},
		{
			ID: "bkB0000000000000", SeriesID: "sA00000000000000", RootName: "manga",
			RelPath: "S/2", DisplayName: "2권", SortKey: []byte{0x7f, 0x80},
			Ord: 4, Kind: "folder", PageCount: 0, TotalBytes: 17, FileSize: 999,
			FileMtime:      1_690_000_002,
			DirFingerprint: sql.NullString{String: "fp-b", Valid: true},
			ContentVersion: "cv-b", DimsState: "partial", Status: "error",
			Error:   sql.NullString{String: "crc mismatch", Valid: true},
			ScanGen: 8,
		},
	}

	raw := openRaw(t, idxPath)
	if _, err := raw.Exec(index.SchemaV1); err != nil {
		t.Fatalf("applying schema v1: %v", err)
	}
	for _, q := range []string{
		`INSERT INTO meta (key, value) VALUES
		    ('schema_version', '1'), ('id_version', 'shelf-id/1'),
		    ('app_version', 'v1-era'), ('created_at', '1690000000')`,
		`INSERT INTO roots (name, path, enabled) VALUES ('manga', '/m', 1)`,
		`INSERT INTO series (id, root_name, rel_path, display_name, sort_key, search_key,
		    choseong_key, kind, mtime, added_at, status, scan_gen)
		 VALUES ('sA00000000000000', 'manga', 'S', 'S', x'53', 's', 's', 'folder', 1, 1, 'ok', 7)`,
		`INSERT INTO pages (book_id, page_no, name, entry_path, ext, size)
		 VALUES ('bkA0000000000000', 1, '001.jpg', 'S/001.jpg', 'jpg', 100),
		        ('bkA0000000000000', 2, '002.jpg', 'S/002.jpg', 'jpg', 200)`,
	} {
		if _, err := raw.Exec(q); err != nil {
			t.Fatalf("seeding the v1 file (%s): %v", q, err)
		}
	}
	for _, b := range want {
		if _, err := raw.Exec(`INSERT INTO books (id, series_id, root_name, rel_path, display_name,
		    sort_key, ord, kind, page_count, total_bytes, file_size, file_mtime,
		    dir_fingerprint, content_version, dims_state, status, error, scan_gen)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			b.ID, b.SeriesID, b.RootName, b.RelPath, b.DisplayName,
			b.SortKey, b.Ord, b.Kind, b.PageCount, b.TotalBytes, b.FileSize, b.FileMtime,
			b.DirFingerprint, b.ContentVersion, b.DimsState, b.Status, b.Error, b.ScanGen); err != nil {
			t.Fatalf("inserting v1 book %s: %v", b.ID, err)
		}
	}
	if cols := tableColumns(t, raw, "books"); len(cols) != 18 {
		t.Fatalf("v1 books has %d columns %v, want 18 (the premise: no inner_path yet)", len(cols), cols)
	}
	_ = raw.Close()

	idx, err := index.Open(ctx, index.Options{Path: idxPath, UserPath: userPath, Logger: quietLogger()})
	if err != nil {
		t.Fatalf("Open on a v1 index: %v", err)
	}
	if v, err := idx.SchemaVersion(ctx); err != nil || v != 3 {
		t.Errorf("schema version after migrating = %d (%v), want 3", v, err)
	}
	if v, _, _ := idx.Meta(ctx, "id_version"); v != "shelf-id/1" {
		t.Errorf("id_version = %q, want shelf-id/1", v)
	}
	if v, _, _ := idx.Meta(ctx, "created_at"); v != "1690000000" {
		t.Errorf("created_at = %q, want the v1 file's 1690000000 (set only when absent)", v)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw = openRaw(t, idxPath)
	rows, err := raw.Query(`SELECT id, series_id, root_name, rel_path, display_name, sort_key,
	    ord, kind, page_count, total_bytes, file_size, file_mtime, dir_fingerprint,
	    content_version, dims_state, status, error, scan_gen, inner_path, name_encoding
	  FROM books ORDER BY id`)
	if err != nil {
		t.Fatalf("reading migrated books: %v", err)
	}
	defer rows.Close()
	var got []v1BookRow
	for rows.Next() {
		var b v1BookRow
		if err := rows.Scan(&b.ID, &b.SeriesID, &b.RootName, &b.RelPath, &b.DisplayName,
			&b.SortKey, &b.Ord, &b.Kind, &b.PageCount, &b.TotalBytes, &b.FileSize,
			&b.FileMtime, &b.DirFingerprint, &b.ContentVersion, &b.DimsState, &b.Status,
			&b.Error, &b.ScanGen, &b.InnerPath, &b.NameEncoding); err != nil {
			t.Fatalf("scanning migrated book: %v", err)
		}
		got = append(got, b)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterating migrated books: %v", err)
	}
	// inner_path '' and name_encoding NULL are the documented compatibility
	// story of the v2 and v3 rungs; want already carries both as zero values.
	if len(got) != len(want) {
		t.Fatalf("books after migrating = %d rows, want the %d the v1 file held", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Errorf("book %d after migrating:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}

	var pages, series int
	if err := raw.QueryRow(`SELECT count(*) FROM pages WHERE book_id = 'bkA0000000000000'`).Scan(&pages); err != nil {
		t.Fatalf("counting pages: %v", err)
	}
	if err := raw.QueryRow(`SELECT count(*) FROM series`).Scan(&series); err != nil {
		t.Fatalf("counting series: %v", err)
	}
	if pages != 2 || series != 1 {
		t.Errorf("after migrating pages = %d, series = %d; want 2 and 1 (the rungs never touch them)", pages, series)
	}
}
