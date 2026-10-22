package verify

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"shelf/internal/source"
	"shelf/internal/testutil"
)

var seedBooks = []source.Book{
	{ID: "B1", Kind: source.KindZIP, RootName: "m", RelPath: "a/01.zip"},
	{ID: "B2", Kind: source.KindNestedZIP, RootName: "m", RelPath: "a/box.zip", InnerPath: "v2.zip"},
	{ID: "B3", Kind: source.KindDir, RootName: "n", RelPath: "c"},
}

// (a) The server is running: index.db has a live -wal holding rows that are
// not in the main file yet. A read-only connection must see them.
func TestLoadBooks_serverRunning_readsTheWALReadOnly(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	li := openLiveIndex(t, dataDir)
	li.insert(t, seedBooks...)
	if fileSize(t, li.path+"-wal") <= 0 || fileSize(t, li.path+"-shm") <= 0 {
		t.Fatalf("precondition: a running server has both sidecars (wal %d, shm %d)",
			fileSize(t, li.path+"-wal"), fileSize(t, li.path+"-shm"))
	}
	before := dirSnapshot(t, dataDir)

	got, err := LoadBooks(context.Background(), li.path)
	if err != nil {
		t.Fatalf("LoadBooks: %v", err)
	}
	books, mode := got.Books, got.Mode
	if mode != ModeReadOnly {
		t.Errorf("mode = %q, want %q", mode, ModeReadOnly)
	}
	if got, want := bookIDs(books), bookIDs(seedBooks); !slices.Equal(got, want) {
		t.Errorf("books = %v, want %v", got, want)
	}
	for _, b := range books {
		if b.ID == "B2" && (b.Kind != source.KindNestedZIP || b.InnerPath != "v2.zip" || b.RelPath != "a/box.zip" || b.RootName != "m") {
			t.Errorf("B2 read back as %+v", b)
		}
	}
	sameSnapshot(t, before, dirSnapshot(t, dataDir), "index.db-shm")
}

// (b) The server is stopped and exited cleanly: no sidecars. A mode=ro open
// would create -shm and -wal in the data directory (measured with
// modernc.org/sqlite v1.54), and fail outright when the directory is not
// writable. With no WAL there is nothing outside the main file, so it is read
// immutable — and nothing is created.
func TestLoadBooks_serverStopped_readsImmutableAndCreatesNothing(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	li := openLiveIndex(t, dataDir)
	li.insert(t, seedBooks...)
	li.stop()
	if fileSize(t, li.path+"-wal") >= 0 || fileSize(t, li.path+"-shm") >= 0 {
		t.Fatal("precondition: a cleanly stopped server leaves no sidecars")
	}
	before := dirSnapshot(t, dataDir)

	got, err := LoadBooks(context.Background(), li.path)
	if err != nil {
		t.Fatalf("LoadBooks: %v", err)
	}
	books, mode := got.Books, got.Mode
	if mode != ModeImmutable {
		t.Errorf("mode = %q, want %q", mode, ModeImmutable)
	}
	if got, want := bookIDs(books), bookIDs(seedBooks); !slices.Equal(got, want) {
		t.Errorf("books = %v, want %v", got, want)
	}
	sameSnapshot(t, before, dirSnapshot(t, dataDir))
}

// (c) A -wal is on disk but nothing is serving it (a crash, or a server that
// was killed). Its frames may hold rows the main file does not, so an
// immutable read could be stale; a read-only open would have to create -shm
// and run recovery. Neither is allowed: refuse, and say what to do.
func TestLoadBooks_leftoverWAL_refusesTheImmutableRead(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	li := openLiveIndex(t, dataDir)
	li.insert(t, seedBooks...)
	li.stop()
	testutil.BuildTreeAt(t, dataDir, map[string]any{"index.db-wal": []byte("frames nobody checkpointed")})
	before := dirSnapshot(t, dataDir)

	_, err := LoadBooks(context.Background(), li.path)
	if !errors.Is(err, ErrIndexUnsafe) {
		t.Fatalf("err = %v, want ErrIndexUnsafe", err)
	}
	sameSnapshot(t, before, dirSnapshot(t, dataDir))
}

// A server that starts while the immutable read is in progress invalidates
// it: the list is discarded rather than trusted.
func TestLoadBooks_walAppearsDuringTheImmutableRead_discardsTheList(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	li := openLiveIndex(t, dataDir)
	li.insert(t, seedBooks...)
	li.stop()

	got, err := loadBooks(context.Background(), li.path, loadHooks{afterRead: func() {
		testutil.BuildTreeAt(t, dataDir, map[string]any{"index.db-wal": []byte("a server started")})
	}})
	books := got.Books
	if !errors.Is(err, ErrIndexUnsafe) {
		t.Fatalf("err = %v, want ErrIndexUnsafe", err)
	}
	if books != nil {
		t.Errorf("a list read under a changing index was returned: %v", bookIDs(books))
	}
}

func TestLoadBooks_mainFileChangesDuringTheImmutableRead_discardsTheList(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	li := openLiveIndex(t, dataDir)
	li.insert(t, seedBooks...)
	li.stop()

	_, err := loadBooks(context.Background(), li.path, loadHooks{afterRead: func() {
		testutil.Touch(t, li.path, time.Hour)
	}})
	if !errors.Is(err, ErrIndexUnsafe) {
		t.Fatalf("err = %v, want ErrIndexUnsafe", err)
	}
}

func TestLoadBooks_noIndex_failsWithoutCreatingOne(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	_, err := LoadBooks(context.Background(), IndexPath(dataDir))
	if err == nil {
		t.Fatal("a missing index.db was read as an empty one")
	}
	if names := dirNames(t, dataDir); len(names) != 0 {
		t.Errorf("verify created %v", names)
	}
}

// An interrupt that lands while the read-only query beside a live server is
// running is still an interrupt: the caller maps context.Canceled to its own
// exit code, so the cause must survive the ErrIndexUnsafe wrapping.
func TestLoadBooks_cancelledDuringTheReadOnlyRead_isStillCancellation(t *testing.T) {
	t.Parallel()
	li := openLiveIndex(t, t.TempDir())
	li.insert(t, seedBooks...)
	if fileSize(t, li.path+"-wal") <= 0 {
		t.Fatal("precondition: the WAL holds the rows, so the mode=ro branch is taken")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := LoadBooks(ctx, li.path)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to match context.Canceled", err)
	}
	if !errors.Is(err, ErrIndexUnsafe) {
		t.Errorf("err = %v, want it to still match ErrIndexUnsafe", err)
	}
}

// The mode=ro read is the one place verify can leave something behind: a
// server that shuts down between the sidecar check and the open removes -wal
// and -shm, and the read-only open then creates fresh ones (measured, see
// LoadBooks). verify does not delete them; it says so.
func TestLoadBooks_serverStopsJustBeforeTheReadOnlyRead_warnsAboutTheSidecars(t *testing.T) {
	t.Parallel()
	li := openLiveIndex(t, t.TempDir())
	li.insert(t, seedBooks...)

	got, err := loadBooks(context.Background(), li.path, loadHooks{beforeRead: li.stop})
	if err != nil {
		t.Fatalf("LoadBooks: %v", err)
	}
	if got.Mode != ModeReadOnly {
		t.Errorf("mode = %q, want %q", got.Mode, ModeReadOnly)
	}
	if ids, want := bookIDs(got.Books), bookIDs(seedBooks); !slices.Equal(ids, want) {
		t.Errorf("books = %v, want %v", ids, want)
	}
	if fileSize(t, li.path+"-shm") < 0 {
		t.Skip("this SQLite build did not recreate the sidecars; nothing to warn about")
	}
	if got.Warning == "" || !strings.Contains(got.Warning, "index.db-shm") {
		t.Errorf("warning = %q, want one naming the sidecars this read may have created", got.Warning)
	}
}

func TestLoadBooks_serverKeepsRunning_noWarning(t *testing.T) {
	t.Parallel()
	li := openLiveIndex(t, t.TempDir())
	li.insert(t, seedBooks...)
	got, err := LoadBooks(context.Background(), li.path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Warning != "" {
		t.Errorf("warning = %q beside a server that never stopped", got.Warning)
	}
}
