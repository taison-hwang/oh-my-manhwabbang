package verify

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite" // registers "sqlite", the driver internal/index uses

	"shelf/internal/source"
)

// Mode says how index.db was opened for the book list.
type Mode string

const (
	// ModeReadOnly is a `mode=ro` connection beside a running server: the WAL
	// and its -shm exist, so the reader joins them and sees every committed
	// row, including the ones not yet checkpointed into the main file.
	ModeReadOnly Mode = "ro"
	// ModeImmutable is `immutable=1`: SQLite reads the main file as a plain
	// file and touches nothing else. It is only correct when the main file is
	// the whole database, i.e. there is no WAL content beside it.
	ModeImmutable Mode = "immutable"
)

// ErrIndexUnsafe reports that the book list could not be read without either
// writing into the data directory or risking a stale read. The message says
// which, and what the operator can do about it.
var ErrIndexUnsafe = errors.New("index.db cannot be read safely right now")

// IndexPath is where the server keeps index.db. internal/app spells it the
// same way (app.go: `filepath.Join(a.cfg.Storage.DataDir, "index.db")`);
// internal/index takes the path as an option and does not name the file.
func IndexPath(dataDir string) string { return filepath.Join(dataDir, "index.db") }

// LoadBooks reads every book row of index.db into memory and closes the
// database before returning, so no container is read while it is open.
//
// It never writes. index.Open is not used — it creates the data directory,
// runs the migration ladder and ATTACHes user.db. And a plain `mode=ro` open
// is not used blindly either: measured on modernc.org/sqlite v1.54, a mode=ro
// connection to a WAL database with no sidecars *creates* `-shm` and an empty
// `-wal` in the data directory (and fails outright when that directory is not
// writable). So the choice is made from what is on disk first:
//
//	-wal has content, -shm present   a server is (or was) running: mode=ro
//	-wal has content, -shm absent    refuse: the last writes are only in the
//	                                 WAL, and reading them would create -shm
//	-wal absent or empty             immutable=1, then re-check (below)
//
// The immutable read is checked afterwards: if a -wal with content appeared,
// or the main file's size or mtime moved, a server started while the rows were
// being read and the list is discarded with ErrIndexUnsafe. A failed mode=ro
// open is never retried as immutable, because the WAL beside it is real.
//
// Known limit of the mode=ro branch: a server that shuts down between the
// sidecar check and the open removes -wal and -shm, and the open then creates
// a fresh -shm and an empty -wal — the measured behaviour above. Nothing can
// close that window without writing, so it is detected instead: the sidecars
// are re-checked after the read, and if either is now a different file than
// the one checked, or the -wal is empty, Loaded.Warning names them. verify
// never deletes them (a server that is starting may own them by then); once
// no server is running they are safe to remove. The check compares file
// identity, so it can miss the case where the filesystem hands the new file
// the inode number of the one just removed and the -wal is not empty.
//
// Also measured: beside a live server, the read itself writes into -shm —
// the read-mark slots every WAL reader takes, through its mmap — without
// changing its size or mtime. That is SQLite's lock table, not data, and the
// server's own readers do the same.
func LoadBooks(ctx context.Context, indexPath string) (Loaded, error) {
	return loadBooks(ctx, indexPath, loadHooks{})
}

// Loaded is what LoadBooks read.
type Loaded struct {
	Books []source.Book
	Mode  Mode
	// Warning is non-empty when the read may have left something behind in
	// the data directory; see LoadBooks.
	Warning string
}

// loadHooks are test seams around the index read; nil fields do nothing.
type loadHooks struct {
	// beforeRead runs after the sidecar check and before the database is
	// opened, so a test can stop a "server" at exactly that moment.
	beforeRead func()
	// afterRead runs between reading the rows and the safety re-check, so a
	// test can start a "server" at exactly that moment.
	afterRead func()
}

func loadBooks(ctx context.Context, indexPath string, h loadHooks) (Loaded, error) {
	books, mode, warning, err := loadBooksAt(ctx, indexPath, h)
	if err != nil {
		return Loaded{}, err
	}
	return Loaded{Books: books, Mode: mode, Warning: warning}, nil
}

func loadBooksAt(ctx context.Context, indexPath string, h loadHooks) ([]source.Book, Mode, string, error) {
	abs, err := filepath.Abs(indexPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("resolving %s: %w", indexPath, err)
	}
	mainBefore, err := os.Stat(abs)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, "", "", fmt.Errorf("there is no index at %s; start the server once so it scans the roots", abs)
		}
		return nil, "", "", fmt.Errorf("reading %s: %w", abs, err)
	}
	walBefore, walSize, err := sidecar(abs + "-wal")
	if err != nil {
		return nil, "", "", err
	}
	shmBefore, shmSize, err := sidecar(abs + "-shm")
	if err != nil {
		return nil, "", "", err
	}

	if walSize > 0 {
		if shmSize < 0 {
			return nil, "", "", fmt.Errorf("%w: %s-wal holds %d bytes of writes that are not in the main file, "+
				"and there is no -shm beside it, so no server is serving it. Start the server (verify reads "+
				"alongside it) or start and stop it once so it checkpoints, then run verify again",
				ErrIndexUnsafe, abs, walSize)
		}
		if h.beforeRead != nil {
			h.beforeRead()
		}
		books, err := readBooks(ctx, dsn(abs, "mode=ro&_pragma=busy_timeout(5000)"))
		if err != nil {
			return nil, "", "", fmt.Errorf("%w: opening %s read-only beside its write-ahead log: %w. "+
				"If the server is not running, start and stop it once so it checkpoints, then run verify again",
				ErrIndexUnsafe, abs, err)
		}
		return books, ModeReadOnly, sidecarWarning(abs, walBefore, shmBefore), nil
	}

	books, err := readBooks(ctx, dsn(abs, "mode=ro&immutable=1"))
	if err != nil {
		return nil, "", "", fmt.Errorf("reading %s: %w", abs, err)
	}
	if h.afterRead != nil {
		h.afterRead()
	}
	_, walAfter, err := sidecar(abs + "-wal")
	if err != nil {
		return nil, "", "", err
	}
	mainAfter, err := os.Stat(abs)
	if err != nil {
		return nil, "", "", fmt.Errorf("re-reading %s: %w", abs, err)
	}
	if walAfter > 0 || mainAfter.Size() != mainBefore.Size() || !mainAfter.ModTime().Equal(mainBefore.ModTime()) {
		return nil, "", "", fmt.Errorf("%w: %s changed while its book list was being read (did the server start?). "+
			"Run verify again; with the server running it reads alongside it", ErrIndexUnsafe, abs)
	}
	return books, ModeImmutable, "", nil
}

// sidecar stats a sidecar: its FileInfo and length, or nil and -1 when it
// does not exist.
func sidecar(p string) (fs.FileInfo, int64, error) {
	fi, err := os.Stat(p)
	switch {
	case err == nil:
		return fi, fi.Size(), nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, -1, nil
	default:
		return nil, 0, fmt.Errorf("checking %s: %w", p, err)
	}
}

// sidecarWarning re-checks the sidecars after a mode=ro read (see LoadBooks):
// a -wal or -shm that is now a different file than the one checked before the
// read, or a -wal that is now empty, may have been created by the read.
func sidecarWarning(abs string, walBefore, shmBefore fs.FileInfo) string {
	var suspect []string
	for _, c := range []struct {
		name   string
		before fs.FileInfo
	}{{"-wal", walBefore}, {"-shm", shmBefore}} {
		after, _, err := sidecar(abs + c.name)
		if err != nil || after == nil {
			continue // gone again, or unreadable: nothing this read left behind
		}
		if c.before == nil || !os.SameFile(c.before, after) || (c.name == "-wal" && after.Size() == 0) {
			suspect = append(suspect, filepath.Base(abs)+c.name)
		}
	}
	if len(suspect) == 0 {
		return ""
	}
	return fmt.Sprintf("%s changed while the book list was being read (did the server stop?). "+
		"If no server is running now, %s in %s may have been created by this read; verify does not "+
		"remove them, and they are safe to delete while no server is running",
		strings.Join(suspect, " and "), strings.Join(suspect, " and "), filepath.Dir(abs))
}

// dsn renders an absolute path as a SQLite file: URI; url.URL escapes spaces,
// '?' and '#', which a data directory may contain.
func dsn(abs, query string) string {
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(abs), RawQuery: query}
	return u.String()
}

// readBooks runs the one query verify needs and closes the database.
func readBooks(ctx context.Context, dsn string) ([]source.Book, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	rows, err := db.QueryContext(ctx,
		`SELECT id, kind, root_name, rel_path, inner_path FROM books ORDER BY root_name, rel_path, inner_path`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []source.Book
	for rows.Next() {
		var b source.Book
		var kind string
		if err := rows.Scan(&b.ID, &kind, &b.RootName, &b.RelPath, &b.InnerPath); err != nil {
			return nil, err
		}
		b.Kind = source.Kind(kind)
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
