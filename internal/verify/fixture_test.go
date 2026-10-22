package verify

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"shelf/internal/index"
	"shelf/internal/source"
	"shelf/internal/userdata"
)

// Fixtures for this package are built only through internal/testutil and
// t.TempDir(): check-readonly forbids every os write primitive here, test files
// included, because this is a package that opens media roots.

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// liveIndex is index.db the way a running server holds it: user.db and
// index.db open through their own packages, so the schema is the real one and
// the file is in WAL mode with both sidecars present.
type liveIndex struct {
	path string
	ud   *userdata.DB
	idx  *index.DB
}

func openLiveIndex(t *testing.T, dataDir string) *liveIndex {
	t.Helper()
	ctx := context.Background()
	ud, err := userdata.Open(ctx, userdata.Options{Path: filepath.Join(dataDir, "user.db"), Logger: quietLogger()})
	if err != nil {
		t.Fatalf("userdata.Open: %v", err)
	}
	idx, err := index.Open(ctx, index.Options{
		Path:     IndexPath(dataDir),
		UserPath: filepath.Join(dataDir, "user.db"),
		Logger:   quietLogger(),
	})
	if err != nil {
		_ = ud.Close()
		t.Fatalf("index.Open: %v", err)
	}
	li := &liveIndex{path: IndexPath(dataDir), ud: ud, idx: idx}
	t.Cleanup(li.stop)
	return li
}

// stop closes both databases, which checkpoints the WAL and removes the
// sidecars: the state of a data directory whose server has exited cleanly.
func (li *liveIndex) stop() {
	if li.idx != nil {
		_ = li.idx.Close()
		li.idx = nil
	}
	if li.ud != nil {
		_ = li.ud.Close()
		li.ud = nil
	}
}

// insert writes book rows with a connection of its own. Only the columns
// verify reads matter; the rest carry NOT NULL placeholders.
func (li *liveIndex) insert(t *testing.T, books ...source.Book) {
	t.Helper()
	u := url.URL{Scheme: "file", Path: li.path, RawQuery: "_pragma=busy_timeout(5000)"}
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		t.Fatalf("opening the index to seed it: %v", err)
	}
	defer db.Close()
	for _, b := range books {
		_, err := db.Exec(`INSERT INTO books (id, series_id, root_name, rel_path, inner_path,
			display_name, sort_key, ord, kind, content_version, scan_gen)
			VALUES (?, 'S', ?, ?, ?, ?, x'00', 0, ?, 'v', 1)`,
			b.ID, b.RootName, b.RelPath, b.InnerPath, b.RelPath, string(b.Kind))
		if err != nil {
			t.Fatalf("seeding book %s: %v", b.ID, err)
		}
	}
}

// fileState is what dirSnapshot records for one file: enough to see it
// created, removed, resized, rewritten in place, or merely touched.
type fileState struct {
	size  int64
	mtime int64 // UnixNano
	sum   string
}

// dirSnapshot records every regular file under dir by its path relative to
// dir, for the assertion that verify wrote nothing — not a new name, and not a
// changed byte in an existing file either.
func dirSnapshot(t *testing.T, dir string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		h := sha256.Sum256(b)
		rel, _ := filepath.Rel(dir, p)
		out[rel] = fileState{size: fi.Size(), mtime: fi.ModTime().UnixNano(), sum: hex.EncodeToString(h[:])}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// sameSnapshot reports every difference between two dirSnapshots. A name in
// shared may change its bytes but nothing else: that is the -shm of a live
// database, whose read-mark slots every WAL reader writes through its mmap
// (measured with modernc.org/sqlite v1.54: same size, same mtime, new hash).
// It is SQLite's lock table, not data, and the server's own readers do the same.
func sameSnapshot(t *testing.T, before, after map[string]fileState, shared ...string) {
	t.Helper()
	for name, b := range before {
		a, ok := after[name]
		if ok && slices.Contains(shared, name) {
			a.sum = b.sum
		}
		switch {
		case !ok:
			t.Errorf("%s was removed", name)
		case a != b:
			t.Errorf("%s changed: %+v -> %+v", name, b, a)
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			t.Errorf("%s was created", name)
		}
	}
}

// dirNames lists a directory, for the assertion that verify created nothing.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name())
	}
	slices.Sort(out)
	return out
}

func fileSize(t *testing.T, p string) int64 {
	t.Helper()
	fi, err := os.Stat(p)
	if err != nil {
		return -1
	}
	return fi.Size()
}

func bookIDs(bs []source.Book) []string {
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.ID)
	}
	slices.Sort(out)
	return out
}
