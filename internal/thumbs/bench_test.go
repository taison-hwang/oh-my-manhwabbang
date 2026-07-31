package thumbs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"shelf/internal/ids"
	"shelf/internal/index"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// addZipBook writes another archive into the fixture root and indexes it, so a
// benchmark can work on a page of a realistic size without slowing every unit
// test down with a 1400×2000 encode.
func (h *harness) addZipBook(rel string, geom [][2]int) (bookID, cv string) {
	h.t.Helper()
	ctx := h.t.Context()

	entries := make([]testutil.Entry, 0, len(geom))
	for i, g := range geom {
		entries = append(entries, testutil.Entry{
			Name:   fmt.Sprintf("%03d.jpg", i+1),
			Data:   testutil.TinyJPEG(h.t, g[0], g[1]),
			Method: testutil.MethodDeflate,
		})
	}
	abs := filepath.Join(h.rootPath, filepath.FromSlash(rel))
	writeFile(h.t, abs, testutil.BuildZIP(h.t, testutil.ZIPSpec{Entries: entries}))

	fi, err := os.Stat(abs)
	if err != nil {
		h.t.Fatalf("stat %s: %v", rel, err)
	}
	bookID = ids.BookID(testRoot, rel)
	cv = contentVersion(h.t, "zip", abs)

	book := source.Book{
		ID: bookID, Kind: source.KindZIP, RootName: testRoot, RelPath: rel,
		FileSize: fi.Size(), FileMtime: fi.ModTime().Unix(),
	}
	bs, err := h.factory.Open(ctx, book)
	if err != nil {
		h.t.Fatalf("opening %s: %v", rel, err)
	}
	listing, err := bs.List(ctx)
	if err != nil {
		h.t.Fatalf("listing %s: %v", rel, err)
	}
	_ = bs.Close()

	w := h.idx.Writer(index.WriterOptions{})
	defer func() { _ = w.Close() }()
	if err := w.UpsertBook(ctx, index.Book{
		ID: bookID, SeriesID: h.seriesID, RootName: testRoot, RelPath: rel,
		DisplayName: filepath.Base(rel), SortKey: []byte(rel), Ord: 9,
		Kind: string(source.KindZIP), PageCount: int64(len(listing.Pages)),
		TotalBytes: listing.TotalBytes, FileSize: book.FileSize, FileMtime: book.FileMtime,
		ContentVersion: cv, DimsState: "none", Status: "ok", ScanGen: 1,
	}); err != nil {
		h.t.Fatalf("UpsertBook: %v", err)
	}
	rows := make([]index.Page, 0, len(listing.Pages))
	for _, p := range listing.Pages {
		rows = append(rows, index.Page{
			BookID: bookID, PageNo: p.No, Name: p.Name, EntryPath: p.EntryPath, Ext: p.Ext,
			Size: p.Size, CompSize: p.CompSize, Method: int(p.Method),
			LocalHdrOff: p.LocalHdrOff, CRC32: p.CRC32, Mtime: p.Mtime,
		})
	}
	if err := w.ReplacePages(ctx, bookID, rows); err != nil {
		h.t.Fatalf("ReplacePages: %v", err)
	}
	if err := w.Flush(ctx); err != nil {
		h.t.Fatalf("Flush: %v", err)
	}
	return bookID, cv
}

// BenchmarkThumbnail measures a COLD cover: open the archive, seek to page 1's
// local header, inflate it, decode, downscale with Lanczos and publish.
//
// Every iteration uses a fresh content version, so every iteration is a genuine
// cache miss at a distinct path — no purge is needed and the measurement never
// includes a directory removal.
//
// The reference points from arch §5.4, measured on the real collection with
// 1600×2400 pages: 21.0 covers/s at 4 workers, 66.9/s at 16, ~47 KB per
// thumbnail. impl-plan WP-07 acceptance 9 treats a >20 % regression here as a
// failure.
// benchGen numbers the synthetic content versions. It is process-global on
// purpose: a counter that restarted with each b.Loop() would replay the same
// keys on the second `-count` repetition of a sub-benchmark and measure cache
// hits — observed as 91 ms/op followed by 12 µs/op — silently turning the cold
// number this benchmark exists to produce into a warm one.
var benchGen atomic.Int64

func BenchmarkThumbnail(b *testing.B) {
	h := newHarness(b, func(o *Options) { o.noWorkers = true })
	// One 1400×2000 page: the p50 shape of the reference collection.
	bookID, cv := h.addZipBook("series/bench.zip", [][2]int{{1400, 2000}})

	for _, width := range []int{120, 640} {
		b.Run("w"+strconv.Itoa(width), func(b *testing.B) {
			ctx := b.Context()
			b.ReportAllocs()
			for b.Loop() {
				res, err := h.svc.Generate(ctx, Request{
					ID:             bookID,
					PageNo:         1,
					Width:          width,
					ContentVersion: cv + strconv.FormatInt(benchGen.Add(1), 10),
					Priority:       PriorityCover,
				})
				if err != nil {
					b.Fatalf("Generate: %v", err)
				}
				if res.Size == 0 {
					b.Fatal("published an empty thumbnail")
				}
			}
		})
	}
}

// BenchmarkCacheHit is the other half of the picture: what a warm request
// costs. It is what justifies answering from disk rather than keeping a decoded
// LRU in memory.
func BenchmarkCacheHit(b *testing.B) {
	h := newHarness(b, func(o *Options) { o.noWorkers = true })
	req := h.pageReq(1, 240)
	if _, err := h.svc.Generate(b.Context(), req); err != nil {
		b.Fatalf("warming the cache: %v", err)
	}

	ctx := b.Context()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := h.svc.Get(ctx, req); err != nil {
			b.Fatalf("Get: %v", err)
		}
	}
}

// BenchmarkProbeDims is the dimension pass's unit cost — arch §5.8 measured
// 23 µs for a JPEG, and the point of the number is that it is the seek, not the
// parse.
func BenchmarkProbeDims(b *testing.B) {
	data := testutil.TinyJPEG(b, 1400, 2000)
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := probeDims(newRepeatingReader(data)); err != nil {
			b.Fatalf("probeDims: %v", err)
		}
	}
}

// newRepeatingReader hands out the same bytes again on every call without
// re-allocating them.
func newRepeatingReader(data []byte) *sliceReader { return &sliceReader{data: data} }

type sliceReader struct {
	data []byte
	off  int
}

func (r *sliceReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, os.ErrClosed
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
