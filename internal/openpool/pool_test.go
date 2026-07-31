package openpool_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"shelf/internal/archive/zipidx"
	"shelf/internal/openpool"
	"shelf/internal/testutil"
)

// ---------------------------------------------------------------------------
// A fake File that records its own lifecycle, so "was a descriptor closed
// while somebody was reading it?" is a question the test can answer directly
// rather than infer.
// ---------------------------------------------------------------------------

type fakeFile struct {
	name   string
	data   []byte
	mtime  time.Time
	closed atomic.Bool
	reads  atomic.Int64
}

func (f *fakeFile) ReadAt(p []byte, off int64) (int, error) {
	if f.closed.Load() {
		// This is the failure the pool exists to make impossible.
		return 0, fmt.Errorf("read from %s after close", f.name)
	}
	f.reads.Add(1)
	if off >= int64(len(f.data)) {
		return 0, io.EOF
	}
	n := copy(p, f.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (f *fakeFile) Close() error {
	if f.closed.Swap(true) {
		return errors.New("double close of " + f.name)
	}
	return nil
}

func (f *fakeFile) Stat() (fs.FileInfo, error) { return fakeInfo{f}, nil }

type fakeInfo struct{ f *fakeFile }

func (i fakeInfo) Name() string       { return i.f.name }
func (i fakeInfo) Size() int64        { return int64(len(i.f.data)) }
func (i fakeInfo) Mode() fs.FileMode  { return 0o644 }
func (i fakeInfo) ModTime() time.Time { return i.f.mtime }
func (i fakeInfo) IsDir() bool        { return false }
func (i fakeInfo) Sys() any           { return nil }

// fakeFS hands out fakeFiles and remembers every one it made.
type fakeFS struct {
	mu     sync.Mutex
	files  map[string][]*fakeFile
	opens  atomic.Int64
	mtime  time.Time
	sizeOf func(string) int
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		files: make(map[string][]*fakeFile),
		mtime: time.Unix(1457947614, 0),
	}
}

func (fs *fakeFS) open(name string) (openpool.File, error) {
	size := 4096
	if fs.sizeOf != nil {
		size = fs.sizeOf(name)
	}
	f := &fakeFile{name: name, data: make([]byte, size), mtime: fs.mtime}
	for i := range f.data {
		f.data[i] = byte(i)
	}
	fs.mu.Lock()
	fs.files[name] = append(fs.files[name], f)
	fs.mu.Unlock()
	fs.opens.Add(1)
	return f, nil
}

func (fs *fakeFS) all(name string) []*fakeFile {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return append([]*fakeFile(nil), fs.files[name]...)
}

func (fs *fakeFS) openDescriptors() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	n := 0
	for _, list := range fs.files {
		for _, f := range list {
			if !f.closed.Load() {
				n++
			}
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestAcquire_hit_reusesTheHandle(t *testing.T) {
	t.Parallel()
	ffs := newFakeFS()
	p := openpool.New(openpool.Options{Max: 4, Open: ffs.open})
	t.Cleanup(func() { _ = p.Close() })

	for i := 0; i < 5; i++ {
		ref, err := p.Acquire(t.Context(), "a.zip", 0, 0)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		ref.Release()
	}
	if got := ffs.opens.Load(); got != 1 {
		t.Errorf("opened the file %d times, want 1 — the pool is not reusing handles (FR-SRV-004)", got)
	}
	st := p.Stats()
	if st.Hits != 4 || st.Misses != 1 {
		t.Errorf("stats hits/misses = %d/%d, want 4/1", st.Hits, st.Misses)
	}
}

// The LRU order must be by *use*, not by insertion: the whole point is that
// the archive a reader is paging through stays open.
func TestAcquire_evictsLeastRecentlyUsed(t *testing.T) {
	t.Parallel()
	ffs := newFakeFS()
	p := openpool.New(openpool.Options{Max: 2, Open: ffs.open})
	t.Cleanup(func() { _ = p.Close() })

	acquireRelease := func(name string) {
		t.Helper()
		ref, err := p.Acquire(t.Context(), name, 0, 0)
		if err != nil {
			t.Fatalf("Acquire %s: %v", name, err)
		}
		ref.Release()
	}

	acquireRelease("a.zip")
	acquireRelease("b.zip")
	acquireRelease("a.zip") // a is now MRU, b is the tail
	acquireRelease("c.zip") // over capacity -> b is evicted

	if n := len(ffs.all("b.zip")); n != 1 || !ffs.all("b.zip")[0].closed.Load() {
		t.Errorf("b.zip should have been evicted and closed")
	}
	for _, name := range []string{"a.zip", "c.zip"} {
		if ffs.all(name)[0].closed.Load() {
			t.Errorf("%s was closed but should still be pooled", name)
		}
	}

	// And a re-acquire of the evicted path opens a fresh descriptor.
	acquireRelease("b.zip")
	if n := len(ffs.all("b.zip")); n != 2 {
		t.Errorf("b.zip opened %d times, want 2 (evicted, then re-opened)", n)
	}
	if st := p.Stats(); st.Evictions == 0 {
		t.Error("Stats().Evictions = 0, want at least 1")
	}
}

// The invariant the whole design turns on: a descriptor is never closed while
// a page stream is reading from it.
func TestEviction_neverClosesABorrowedHandle(t *testing.T) {
	t.Parallel()
	ffs := newFakeFS()
	p := openpool.New(openpool.Options{Max: 1, Open: ffs.open})
	t.Cleanup(func() { _ = p.Close() })

	held, err := p.Acquire(t.Context(), "held.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// Churn far past capacity while the first handle is still borrowed.
	for i := 0; i < 20; i++ {
		ref, err := p.Acquire(t.Context(), fmt.Sprintf("other-%d.zip", i), 0, 0)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		// Reading through the held reference must keep working throughout.
		buf := make([]byte, 16)
		if _, err := held.ReadAt(buf, 0); err != nil {
			t.Fatalf("read through the held handle after %d evictions: %v", i, err)
		}
		ref.Release()
	}

	if ffs.all("held.zip")[0].closed.Load() {
		t.Fatal("the borrowed descriptor was closed under a live reader")
	}
	held.Release()

	// Once released it is a normal eviction candidate again.
	ref, err := p.Acquire(t.Context(), "trigger.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ref.Release()
	if n := ffs.openDescriptors(); n > 1 {
		t.Errorf("%d descriptors still open, want at most Max=1", n)
	}
}

func TestInvalidate_dropsTheEntryAndClosesWhenIdle(t *testing.T) {
	t.Parallel()
	ffs := newFakeFS()
	p := openpool.New(openpool.Options{Max: 8, Open: ffs.open})
	t.Cleanup(func() { _ = p.Close() })

	ref, err := p.Acquire(t.Context(), "a.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ref.Release()

	p.Invalidate("a.zip")
	if !ffs.all("a.zip")[0].closed.Load() {
		t.Error("Invalidate on an idle handle should close it")
	}

	ref, err = p.Acquire(t.Context(), "a.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire after Invalidate: %v", err)
	}
	ref.Release()
	if n := len(ffs.all("a.zip")); n != 2 {
		t.Errorf("a.zip opened %d times, want 2 — Invalidate did not force a re-open", n)
	}
}

// Invalidating an archive that is being streamed must not truncate the stream:
// the in-flight reader is already committed to the old offsets, which are the
// ones matching the descriptor it holds.
func TestInvalidate_whileBorrowed_defersTheClose(t *testing.T) {
	t.Parallel()
	ffs := newFakeFS()
	p := openpool.New(openpool.Options{Max: 8, Open: ffs.open})
	t.Cleanup(func() { _ = p.Close() })

	ref, err := p.Acquire(t.Context(), "a.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	p.Invalidate("a.zip")

	if ffs.all("a.zip")[0].closed.Load() {
		t.Fatal("Invalidate closed a borrowed descriptor")
	}
	buf := make([]byte, 32)
	if _, err := ref.ReadAt(buf, 0); err != nil {
		t.Fatalf("reading after Invalidate: %v", err)
	}

	// A new acquirer gets a fresh descriptor immediately.
	fresh, err := p.Acquire(t.Context(), "a.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire after Invalidate: %v", err)
	}
	if len(ffs.all("a.zip")) != 2 {
		t.Errorf("a.zip opened %d times, want 2", len(ffs.all("a.zip")))
	}
	fresh.Release()

	ref.Release()
	if !ffs.all("a.zip")[0].closed.Load() {
		t.Error("the invalidated descriptor should close on its last release")
	}
}

func TestAcquire_staleFile_isServedAndFlagged(t *testing.T) {
	t.Parallel()
	ffs := newFakeFS()
	ffs.sizeOf = func(string) int { return 1000 }
	p := openpool.New(openpool.Options{Max: 4, Open: ffs.open})
	t.Cleanup(func() { _ = p.Close() })

	// Matching (mtime, size): not stale.
	ref, err := p.Acquire(t.Context(), "a.zip", ffs.mtime.Unix(), 1000)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if ref.Stale() {
		t.Error("Stale() = true for a matching file")
	}
	ref.Release()

	for _, tc := range []struct {
		name        string
		mtime, size int64
	}{
		{"size changed", ffs.mtime.Unix(), 999},
		{"mtime changed", ffs.mtime.Unix() + 1, 1000},
	} {
		ref, err := p.Acquire(t.Context(), "a.zip", tc.mtime, tc.size)
		if err != nil {
			t.Fatalf("%s: Acquire: %v", tc.name, err)
		}
		if !ref.Stale() {
			t.Errorf("%s: Stale() = false, want true", tc.name)
		}
		// FR-SRV-004 does not say "refuse"; arch §5.2 says "still serve, but
		// tag it" so the API can answer 409 and enqueue a rescan.
		if _, err := ref.ReadAt(make([]byte, 8), 0); err != nil {
			t.Errorf("%s: a stale handle must still serve: %v", tc.name, err)
		}
		ref.Release()
	}
	if p.Stats().Stale != 2 {
		t.Errorf("Stats().Stale = %d, want 2", p.Stats().Stale)
	}
}

func TestRelease_isIdempotent(t *testing.T) {
	t.Parallel()
	ffs := newFakeFS()
	p := openpool.New(openpool.Options{Max: 1, Open: ffs.open})
	t.Cleanup(func() { _ = p.Close() })

	ref, err := p.Acquire(t.Context(), "a.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ref.Release()
	ref.Release()
	ref.Release()

	// A refcount driven negative would let the next eviction close a handle
	// somebody else is holding.
	other, err := p.Acquire(t.Context(), "a.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	for i := 0; i < 5; i++ {
		ref, err := p.Acquire(t.Context(), fmt.Sprintf("x-%d.zip", i), 0, 0)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		ref.Release()
	}
	if _, err := other.ReadAt(make([]byte, 4), 0); err != nil {
		t.Fatalf("the held handle was closed after a double Release: %v", err)
	}
	other.Release()
}

func TestClose_thenAcquire_isErrClosed(t *testing.T) {
	t.Parallel()
	ffs := newFakeFS()
	p := openpool.New(openpool.Options{Max: 4, Open: ffs.open})

	ref, err := p.Acquire(t.Context(), "a.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Close must not yank the descriptor from a response already in flight.
	if _, err := ref.ReadAt(make([]byte, 4), 0); err != nil {
		t.Errorf("reading a borrowed handle after Close: %v", err)
	}
	ref.Release()
	if !ffs.all("a.zip")[0].closed.Load() {
		t.Error("the last release after Close should close the descriptor")
	}
	if _, err := p.Acquire(t.Context(), "b.zip", 0, 0); !errors.Is(err, openpool.ErrClosed) {
		t.Errorf("Acquire after Close = %v, want openpool.ErrClosed", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestAcquire_cancelledContext_returnsCtxErr(t *testing.T) {
	t.Parallel()
	p := openpool.New(openpool.Options{Max: 4, Open: newFakeFS().open})
	t.Cleanup(func() { _ = p.Close() })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Acquire(ctx, "a.zip", 0, 0); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestAcquire_openFailure_isWrapped(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("no such file")
	p := openpool.New(openpool.Options{
		Max:  4,
		Open: func(string) (openpool.File, error) { return nil, sentinel },
	})
	t.Cleanup(func() { _ = p.Close() })
	_, err := p.Acquire(t.Context(), "gone.zip", 0, 0)
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want it to wrap %v", err, sentinel)
	}
	if !strings.Contains(err.Error(), "opening archive") {
		t.Errorf("err = %q, want it to say what it was doing", err)
	}
}

// CON-004 end to end: 8 goroutines × 300 page reads through the pool, against
// a real archive, with zero errors and no descriptor churn. This is arch
// §5.1's verified measurement, kept as a test.
func TestPool_concurrentPageReads_realArchive(t *testing.T) {
	t.Parallel()
	const (
		goroutines = 8
		perReader  = 300
		pages      = 60
	)

	entries := make([]testutil.Entry, 0, pages)
	want := make([][]byte, 0, pages)
	for i := 0; i < pages; i++ {
		payload := []byte(fmt.Sprintf("page-%03d-", i) + strings.Repeat("x", 900+i))
		want = append(want, payload)
		method := testutil.MethodDeflate
		if i%3 == 0 {
			method = testutil.MethodStore
		}
		entries = append(entries, testutil.Entry{
			Name:   fmt.Sprintf("%04d.jpg", i+1),
			Data:   payload,
			Method: method,
		})
	}
	root := testutil.BuildTree(t, map[string]any{
		"series/vol1.zip": testutil.BuildZIP(t, testutil.ZIPSpec{Entries: entries}),
	})
	path := filepath.Join(root, "series", "vol1.zip")

	p := openpool.New(openpool.Options{Max: 4})
	t.Cleanup(func() { _ = p.Close() })

	// One central-directory read for the whole test — exactly as the scanner
	// does it once and the server never repeats it (FR-SRV-002).
	ref, err := p.Acquire(t.Context(), path, 0, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	ix, err := zipidx.ReadCentralDirectory(t.Context(), ref, ref.Size())
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	ref.Release()

	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	start := time.Now()
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perReader; i++ {
				idx := (g*7 + i) % pages
				ref, err := p.Acquire(t.Context(), path, 0, 0)
				if err != nil {
					errs <- err
					return
				}
				rc, err := zipidx.OpenEntry(t.Context(), ref, ix.Entries[idx].Ref())
				if err != nil {
					ref.Release()
					errs <- err
					return
				}
				got, err := io.ReadAll(rc)
				_ = rc.Close()
				ref.Release()
				if err != nil {
					errs <- err
					return
				}
				if string(got) != string(want[idx]) {
					errs <- fmt.Errorf("page %d came back corrupted", idx)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	total := goroutines * perReader
	t.Logf("%d page reads by %d goroutines in %v (%.3f ms/page); pool stats %+v",
		total, goroutines, time.Since(start),
		float64(time.Since(start).Milliseconds())/float64(total), p.Stats())

	// The pool did its job if it opened the archive a handful of times rather
	// than 2 400 times.
	if st := p.Stats(); st.Misses > 8 {
		t.Errorf("pool misses = %d for %d reads of one archive, want a handful", st.Misses, total)
	}
}

// The pool's File interface deliberately has no Seek. That is not a style
// choice: a shared cursor is exactly what CON-004 forbids, and the type system
// is a better guarantee than a code-review convention.
func TestFileInterface_hasNoSeeker(t *testing.T) {
	t.Parallel()
	var f openpool.File = &fakeFile{name: "x"}
	if _, ok := f.(io.Seeker); ok {
		t.Fatal("openpool.File exposes io.Seeker; concurrent readers could share a cursor (CON-004)")
	}
	ref, err := openpool.New(openpool.Options{Open: newFakeFS().open}).Acquire(t.Context(), "a.zip", 0, 0)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer ref.Release()
	if _, ok := any(ref).(io.Seeker); ok {
		t.Fatal("openpool.Ref exposes io.Seeker; page streams must be pread-only")
	}
}
