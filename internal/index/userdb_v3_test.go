package index_test

import (
	"errors"
	"path/filepath"
	"testing"

	"shelf/internal/index"
)

// index.Open must refuse a user.db left at schema version 2 — before E-69 §2.5
// added book_covers and series_prefs — rather than let the post-scan cleanup
// fail later with "no such table: ud.book_covers" (or ud.series_prefs).
func TestOpen_userDBWithoutV3Tables_isRefusedAtStartup(t *testing.T) {
	t.Parallel()
	for _, table := range []string{"book_covers", "series_prefs"} {
		t.Run(table, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			_, ud, _ := openDBsIn(t, dir, nil)
			if err := ud.Close(); err != nil {
				t.Fatalf("closing user.db: %v", err)
			}

			raw := openRaw(t, filepath.Join(dir, "user.db"))
			if _, err := raw.Exec(`DROP TABLE ` + table); err != nil {
				t.Fatalf("simulating a version-2 user.db: %v", err)
			}
			if err := raw.Close(); err != nil {
				t.Fatalf("closing the raw handle: %v", err)
			}

			db, err := index.Open(t.Context(), index.Options{
				Path:     filepath.Join(dir, "index2.db"),
				UserPath: filepath.Join(dir, "user.db"),
				Logger:   quietLogger(),
			})
			if err == nil {
				_ = db.Close()
			}
			if !errors.Is(err, index.ErrUserDBNotReady) {
				t.Fatalf("Open with a user.db missing %s = %v, want ErrUserDBNotReady", table, err)
			}
		})
	}
}
