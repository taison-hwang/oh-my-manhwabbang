package verify

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwaples/rardecode/v2"

	"shelf/internal/config"
	"shelf/internal/source"
	"shelf/internal/testutil"
)

// ---- fixture plumbing -------------------------------------------------------

// library is one synthetic collection: media roots built by testutil, an
// index.db built by the real index package and then closed (a stopped server).
type library struct {
	cfg *config.Config
}

// newLibrary builds roots (name → layout) and an index holding books.
func newLibrary(t *testing.T, roots map[string]map[string]any, books ...source.Book) *library {
	t.Helper()
	cfg := &config.Config{
		Storage: config.Storage{DataDir: t.TempDir()},
		Scan:    config.Scan{Workers: 2},
	}
	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		cfg.Roots = append(cfg.Roots, config.Root{Name: name, Path: testutil.BuildTree(t, roots[name]), Enabled: true})
	}
	li := openLiveIndex(t, cfg.Storage.DataDir)
	li.insert(t, books...)
	li.stop()
	return &library{cfg: cfg}
}

// defect is one parsed BROKEN line.
type defect struct {
	bookID, path, pageNo, pageName, cause string
}

// result is one run of Run with its output parsed.
type result struct {
	sum       Summary
	defects   []defect
	unchecked []string // "root\treason" of each UNCHECKED line
	summary   string
	err       error
}

func (l *library) run(t *testing.T, opts Options) result {
	t.Helper()
	var out bytes.Buffer
	opts.Config = l.cfg
	opts.Out = &out
	if opts.Logger == nil {
		opts.Logger = quietLogger()
	}
	sum, err := Run(context.Background(), opts)
	return parseRun(t, sum, err, out.String())
}

func parseRun(t *testing.T, sum Summary, err error, out string) result {
	t.Helper()
	r := result{sum: sum, err: err}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		switch {
		case f[0] == "BROKEN" && len(f) == 6:
			r.defects = append(r.defects, defect{f[1], f[2], f[3], f[4], f[5]})
		case f[0] == "UNCHECKED" && len(f) == 3:
			r.unchecked = append(r.unchecked, f[1]+"\t"+f[2])
		case f[0] == "SUMMARY":
			r.summary = line
		default:
			t.Fatalf("unexpected output line %q", line)
		}
	}
	return r
}

func (r result) pageDefects() []defect {
	var out []defect
	for _, d := range r.defects {
		if d.pageNo != "-" {
			out = append(out, d)
		}
	}
	return out
}

func (r result) bookDefects() []defect {
	var out []defect
	for _, d := range r.defects {
		if d.pageNo == "-" {
			out = append(out, d)
		}
	}
	return out
}

func zipBook(id, rel string) source.Book {
	return source.Book{ID: id, Kind: source.KindZIP, RootName: "m", RelPath: rel}
}

func u32p(v uint32) *uint32 { return &v }

// page returns n bytes that occur nowhere else in a fixture, so a test can
// find the payload in the container by searching for it.
func page(tag string, n int) []byte {
	b := bytes.Repeat([]byte(tag+"|"), n/(len(tag)+1)+1)
	return b[:n]
}

// patchCentralSizes rewrites the compressed and uncompressed sizes of one
// central-directory record. The local header is left alone: page serving
// reads geometry from the directory (as the index recorded it), which is the
// disagreement this builds.
func patchCentralSizes(t *testing.T, zip []byte, name string, comp, size uint32) {
	t.Helper()
	sig := []byte{'P', 'K', 1, 2}
	for off := 0; ; {
		i := bytes.Index(zip[off:], sig)
		if i < 0 {
			t.Fatalf("no central record for %q", name)
		}
		rec := zip[off+i:]
		nameLen := int(binary.LittleEndian.Uint16(rec[28:]))
		if string(rec[46:46+nameLen]) == name {
			binary.LittleEndian.PutUint32(rec[20:], comp)
			binary.LittleEndian.PutUint32(rec[24:], size)
			return
		}
		off += i + 4
	}
}

// patchCentralMethod rewrites the compression method of one central-directory
// record, which is where page serving reads it from.
func patchCentralMethod(t *testing.T, zip []byte, name string, method uint16) {
	t.Helper()
	rec := findRecord(t, zip, []byte{'P', 'K', 1, 2}, 28, 46, name)
	binary.LittleEndian.PutUint16(rec[10:], method)
}

// patchLocalFlags ORs flags into one local header's general-purpose bits and
// leaves the central directory alone: listing sees a plain entry, opening the
// page sees the local header.
func patchLocalFlags(t *testing.T, zip []byte, name string, flags uint16) {
	t.Helper()
	rec := findRecord(t, zip, []byte{'P', 'K', 3, 4}, 26, 30, name)
	binary.LittleEndian.PutUint16(rec[6:], binary.LittleEndian.Uint16(rec[6:])|flags)
}

func findRecord(t *testing.T, zip, sig []byte, nameLenAt, nameAt int, name string) []byte {
	t.Helper()
	for off := 0; ; {
		i := bytes.Index(zip[off:], sig)
		if i < 0 {
			t.Fatalf("no %x record for %q", sig, name)
		}
		rec := zip[off+i:]
		nameLen := int(binary.LittleEndian.Uint16(rec[nameLenAt:]))
		if string(rec[nameAt:nameAt+nameLen]) == name {
			return rec
		}
		off += i + 4
	}
}

func flipByteOf(t *testing.T, container, payload []byte, at int) {
	t.Helper()
	i := bytes.Index(container, payload)
	if i < 0 {
		t.Fatal("payload not found in the container")
	}
	container[i+at] ^= 0xff
}

// ---- E-58 cases ---------------------------------------------------------------

func TestRun_cleanZIP_reportsNothing(t *testing.T) {
	t.Parallel()
	z := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: page("one", 300)},
		{Name: "02.jpg", Data: page("two", 5000), Method: testutil.MethodDeflate},
	}})
	lib := newLibrary(t, map[string]map[string]any{"m": {"a/01.zip": z}}, zipBook("B1", "a/01.zip"))

	r := lib.run(t, Options{})
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if len(r.defects) != 0 {
		t.Fatalf("a clean book reported defects: %+v", r.defects)
	}
	want := Summary{Books: 1, Pages: 2}
	if r.sum != want {
		t.Errorf("summary = %+v, want %+v", r.sum, want)
	}
	if r.summary != "SUMMARY\tbooks=1\tpages=2\tdefective_books=0\tdefective_pages=0\tunverifiable_books=0\tunverifiable_pages=0\troots_not_checked=0" {
		t.Errorf("summary line = %q", r.summary)
	}
}

func TestRun_zipCRCOverridden_reportsExactlyThatPage(t *testing.T) {
	t.Parallel()
	z := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: page("one", 100)},
		{Name: "02.jpg", Data: page("two", 100), CRC32Override: u32p(0x12345678)},
		{Name: "03.jpg", Data: page("three", 100)},
	}})
	lib := newLibrary(t, map[string]map[string]any{"m": {"a/01.zip": z}}, zipBook("B1", "a/01.zip"))

	r := lib.run(t, Options{})
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if len(r.defects) != 1 {
		t.Fatalf("defects = %+v, want exactly one", r.defects)
	}
	d := r.defects[0]
	if d.bookID != "B1" || d.path != "m/a/01.zip" || d.pageNo != "2" || d.pageName != "02.jpg" {
		t.Errorf("defect = %+v, want B1 m/a/01.zip page 2 02.jpg", d)
	}
	if !strings.HasPrefix(d.cause, "crc mismatch: want 12345678 got ") {
		t.Errorf("cause = %q", d.cause)
	}
	want := Summary{Books: 1, Pages: 3, DefectiveBooks: 1, DefectivePages: 1}
	if r.sum != want {
		t.Errorf("summary = %+v, want %+v", r.sum, want)
	}
}

// A stored entry whose directory record claims more bytes than the file holds
// after its payload — the interrupted-download shape with the directory still
// in place. Reading it runs into EOF early.
func TestRun_storedEntryCutShort_isAShortRead(t *testing.T) {
	t.Parallel()
	two := page("two", 400)
	z := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: page("one", 400)},
		{Name: "02.jpg", Data: two},
	}})
	dataOff := bytes.Index(z, two)
	claim := uint32(len(z)-dataOff) + 1
	patchCentralSizes(t, z, "02.jpg", claim, claim)
	lib := newLibrary(t, map[string]map[string]any{"m": {"a/01.zip": z}}, zipBook("B1", "a/01.zip"))

	r := lib.run(t, Options{})
	pd := r.pageDefects()
	if len(pd) != 1 || pd[0].pageNo != "2" {
		t.Fatalf("page defects = %+v, want page 2 only", pd)
	}
	want := "short read: " + strconv.Itoa(len(z)-dataOff) + " of " + strconv.Itoa(int(claim)) + " bytes"
	if pd[0].cause != want {
		t.Errorf("cause = %q, want %q", pd[0].cause, want)
	}
}

// The recorded size disagrees with the stored payload while the CRC-32 is the
// payload's own: nothing but the length comparison can see it.
func TestRun_recordedSizeLargerThanAnIntactPayload_isAShortRead(t *testing.T) {
	t.Parallel()
	one := page("one", 256)
	z := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{{Name: "01.jpg", Data: one}}})
	patchCentralSizes(t, z, "01.jpg", uint32(len(one)), uint32(len(one)+8))
	lib := newLibrary(t, map[string]map[string]any{"m": {"a/01.zip": z}}, zipBook("B1", "a/01.zip"))

	r := lib.run(t, Options{})
	pd := r.pageDefects()
	if len(pd) != 1 {
		t.Fatalf("page defects = %+v, want one", pd)
	}
	if want := "short read: 256 of 264 bytes"; pd[0].cause != want {
		t.Errorf("cause = %q, want %q", pd[0].cause, want)
	}
}

// No end record: zipidx rebuilds the list from local headers and says so.
// That is a book-level defect, and the recovered pages are still hashed.
func TestRun_missingEOCD_isABookDefectAndRecoveredPagesAreStillChecked(t *testing.T) {
	t.Parallel()
	z := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{Name: "01.jpg", Data: page("one", 100)},
			{Name: "02.jpg", Data: page("two", 100), CRC32Override: u32p(0xcafef00d)},
			{Name: "03.jpg", Data: page("three", 100)},
		},
		CorruptEOCDSignature: true,
	})
	lib := newLibrary(t, map[string]map[string]any{"m": {"a/01.zip": z}}, zipBook("B1", "a/01.zip"))

	r := lib.run(t, Options{})
	bd := r.bookDefects()
	if len(bd) != 1 || !strings.Contains(bd[0].cause, "rebuilt from local file headers") {
		t.Fatalf("book defects = %+v, want one naming the salvage", bd)
	}
	if bd[0].pageName != "-" {
		t.Errorf("a book-level defect names a page: %+v", bd[0])
	}
	pd := r.pageDefects()
	if len(pd) != 1 || pd[0].pageNo != "2" || !strings.HasPrefix(pd[0].cause, "crc mismatch: want cafef00d") {
		t.Fatalf("page defects = %+v, want page 2's CRC", pd)
	}
	want := Summary{Books: 1, Pages: 3, DefectiveBooks: 1, DefectivePages: 1}
	if r.sum != want {
		t.Errorf("summary = %+v, want %+v", r.sum, want)
	}
}

func TestRun_hv3CorruptedPayloadByte_isAPageDefect(t *testing.T) {
	t.Parallel()
	one, two := page("hv-one", 200), page("hv-two", 200)
	h := testutil.BuildHV3(t, testutil.HV3Spec{Encr: testutil.HV3EncrNone, Entries: []testutil.HV3Entry{
		{Name: "01.jpg", Data: one},
		{Name: "02.jpg", Data: two},
	}})
	flipByteOf(t, h, two, 17)
	lib := newLibrary(t, map[string]map[string]any{"m": {"v.hv3": h}},
		source.Book{ID: "H", Kind: source.KindHV3, RootName: "m", RelPath: "v.hv3"})

	r := lib.run(t, Options{})
	pd := r.pageDefects()
	if len(pd) != 1 || pd[0].pageNo != "2" || !strings.HasPrefix(pd[0].cause, "crc mismatch") {
		t.Fatalf("defects = %+v, want page 2's CRC", r.defects)
	}
	if r.sum.Pages != 2 {
		t.Errorf("pages checked = %d, want 2", r.sum.Pages)
	}
}

func TestRun_rarStoredEntryWithWrongCRC_isAPageDefect(t *testing.T) {
	t.Parallel()
	one, two := page("rar-one", 300), page("rar-two", 300)
	rar := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
		{Name: []byte("01.jpg"), Data: one},
		{Name: []byte("02.jpg"), Data: two},
	}})
	flipByteOf(t, rar, one, 3)
	lib := newLibrary(t, map[string]map[string]any{"m": {"r.rar": rar}},
		source.Book{ID: "R", Kind: source.KindRAR, RootName: "m", RelPath: "r.rar"})

	r := lib.run(t, Options{})
	pd := r.pageDefects()
	if len(pd) != 1 || pd[0].pageNo != "1" || !strings.HasPrefix(pd[0].cause, "crc mismatch") {
		t.Fatalf("defects = %+v, want page 1's CRC", r.defects)
	}
}

// A directory and a PDF carry no integrity data: they are counted, not
// reported. A PDF is ErrUnsupported in a nopdf build and when no renderer is
// wired, and verify wires none; both are the same verdict.
func TestRun_dirAndPDF_areUnverifiableNotDefective(t *testing.T) {
	t.Parallel()
	lib := newLibrary(t, map[string]map[string]any{"m": {
		"d/01.jpg": testutil.TinyJPEG(t, 8, 8),
		"d/02.jpg": testutil.TinyJPEG(t, 8, 8),
		"p.pdf":    []byte("%PDF-1.4\n%%EOF\n"),
	}},
		source.Book{ID: "D", Kind: source.KindDir, RootName: "m", RelPath: "d"},
		source.Book{ID: "P", Kind: source.KindPDF, RootName: "m", RelPath: "p.pdf"},
	)

	r := lib.run(t, Options{})
	if r.err != nil || len(r.defects) != 0 {
		t.Fatalf("err %v, defects %+v", r.err, r.defects)
	}
	want := Summary{Books: 2, Unverifiable: 2}
	if r.sum != want {
		t.Errorf("summary = %+v, want %+v", r.sum, want)
	}
}

// Failing to open a book at all is a defect for every kind, including the
// ones whose pages cannot be verified.
func TestRun_missingContainerOrDirectory_isABookDefect(t *testing.T) {
	t.Parallel()
	ok := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{{Name: "01.jpg", Data: page("mc", 40)}}})
	// One indexed book is still there, so the root is mounted and the two
	// that are gone are gone from it.
	lib := newLibrary(t, map[string]map[string]any{"m": {"here.zip": ok}},
		zipBook("H", "here.zip"),
		zipBook("Z", "gone.zip"),
		source.Book{ID: "D", Kind: source.KindDir, RootName: "m", RelPath: "gone"},
	)
	r := lib.run(t, Options{})
	got := []string{}
	for _, d := range r.bookDefects() {
		got = append(got, d.bookID)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"D", "Z"}) || len(r.pageDefects()) != 0 {
		t.Fatalf("defects = %+v, want book-level for D and Z", r.defects)
	}
	if r.sum.DefectiveBooks != 2 || r.sum.Unverifiable != 0 {
		t.Errorf("summary = %+v", r.sum)
	}
}

func TestRun_nestedVolumePath_namesTheInnerPath(t *testing.T) {
	t.Parallel()
	inner := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: page("n1", 100), CRC32Override: u32p(1)},
	}})
	box := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{{Name: "v1.zip", Data: inner}}})
	lib := newLibrary(t, map[string]map[string]any{"m": {"box.zip": box}},
		source.Book{ID: "N", Kind: source.KindNestedZIP, RootName: "m", RelPath: "box.zip", InnerPath: "v1.zip"})

	r := lib.run(t, Options{})
	if len(r.defects) != 1 || r.defects[0].path != "m/box.zip!v1.zip" || r.defects[0].pageNo != "1" {
		t.Fatalf("defects = %+v, want page 1 of m/box.zip!v1.zip", r.defects)
	}
}

// ---- concurrency, roots, setup ---------------------------------------------

func manyBooks(t *testing.T) (map[string]any, []source.Book) {
	layout := map[string]any{}
	var books []source.Book
	for i := range 12 {
		name := "b" + strconv.Itoa(i) + ".zip"
		var entries []testutil.Entry
		for p := range 4 {
			e := testutil.Entry{Name: strconv.Itoa(p+1) + ".jpg", Data: page(name+strconv.Itoa(p), 2000)}
			if (i+p)%5 == 0 {
				e.CRC32Override = u32p(uint32(i*10 + p))
			}
			entries = append(entries, e)
		}
		spec := testutil.ZIPSpec{Entries: entries, CorruptEOCDSignature: i%4 == 3}
		layout[name] = testutil.BuildZIP(t, spec)
		books = append(books, zipBook("B"+strconv.Itoa(i), name))
	}
	return layout, books
}

func defectSet(r result) []string {
	out := make([]string, 0, len(r.defects))
	for _, d := range r.defects {
		out = append(out, strings.Join([]string{d.bookID, d.path, d.pageNo, d.pageName, d.cause}, "\t"))
	}
	slices.Sort(out)
	return out
}

func TestRun_manyWorkers_findTheSameDefectsAsOne(t *testing.T) {
	t.Parallel()
	layout, books := manyBooks(t)
	lib := newLibrary(t, map[string]map[string]any{"m": layout}, books...)

	one := lib.run(t, Options{Workers: 1})
	many := lib.run(t, Options{Workers: 8})
	if one.err != nil || many.err != nil {
		t.Fatalf("errors: %v / %v", one.err, many.err)
	}
	if len(one.defects) == 0 {
		t.Fatal("the fixture produced no defects; the comparison proves nothing")
	}
	if a, b := defectSet(one), defectSet(many); !slices.Equal(a, b) {
		t.Errorf("1 worker:\n%s\n8 workers:\n%s", strings.Join(a, "\n"), strings.Join(b, "\n"))
	}
	if one.sum != many.sum {
		t.Errorf("summaries differ: %+v vs %+v", one.sum, many.sum)
	}
}

func TestRun_rootFilter_checksOnlyTheNamedRoots(t *testing.T) {
	t.Parallel()
	bad := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: page("x", 50), CRC32Override: u32p(7)},
	}})
	lib := newLibrary(t, map[string]map[string]any{"m": {"a.zip": bad}, "n": {"b.zip": bad}},
		zipBook("M1", "a.zip"),
		source.Book{ID: "N1", Kind: source.KindZIP, RootName: "n", RelPath: "b.zip"},
	)

	r := lib.run(t, Options{Roots: []string{"n"}})
	if r.err != nil {
		t.Fatal(r.err)
	}
	if len(r.defects) != 1 || r.defects[0].bookID != "N1" || r.sum.Books != 1 {
		t.Fatalf("defects = %+v, summary %+v; want only root n", r.defects, r.sum)
	}

	if r := lib.run(t, Options{Roots: []string{"nope"}}); r.err == nil || !strings.Contains(r.err.Error(), "nope") {
		t.Errorf("an unknown root = %v, want an error naming it", r.err)
	}
}

func TestRun_disabledRoot_isSkippedAndCannotBeNamed(t *testing.T) {
	t.Parallel()
	bad := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: page("x", 50), CRC32Override: u32p(7)},
	}})
	lib := newLibrary(t, map[string]map[string]any{"m": {"a.zip": bad}, "n": {"b.zip": bad}},
		zipBook("M1", "a.zip"),
		source.Book{ID: "N1", Kind: source.KindZIP, RootName: "n", RelPath: "b.zip"},
	)
	lib.cfg.Roots[1].Enabled = false

	r := lib.run(t, Options{})
	if len(r.defects) != 1 || r.defects[0].bookID != "M1" {
		t.Fatalf("defects = %+v, want only the enabled root", r.defects)
	}
	if r := lib.run(t, Options{Roots: []string{"n"}}); r.err == nil {
		t.Error("naming a disabled root was accepted")
	}
}

func TestRun_noIndex_isASetupError(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Roots:   []config.Root{{Name: "m", Path: t.TempDir(), Enabled: true}},
		Storage: config.Storage{DataDir: t.TempDir()},
	}
	var out bytes.Buffer
	_, err := Run(context.Background(), Options{Config: cfg, Out: &out, Logger: quietLogger()})
	if err == nil {
		t.Fatal("a missing index was not an error")
	}
	if errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
}

func TestRun_cancelled_stopsAndSaysSo(t *testing.T) {
	t.Parallel()
	layout, books := manyBooks(t)
	lib := newLibrary(t, map[string]map[string]any{"m": layout}, books...)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	_, err := Run(ctx, Options{Config: lib.cfg, Out: &out, Logger: quietLogger()})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if strings.Contains(out.String(), "BROKEN") {
		t.Errorf("a cancelled run reported cancellation as damage:\n%s", out.String())
	}
}

// ---- review round 1 (F2, F4, F6, roots) -------------------------------------

// A RAR whose chain reaches a password-protected (or solid) entry stops there
// and returns the entries before it with archive.ErrEncrypted. Those pages
// are readable and carry a CRC: they are checked, and the book is also counted
// unverifiable because it holds content that could not be checked.
func TestRun_partialListingWithUnverifiableError_stillChecksTheListedPages(t *testing.T) {
	t.Parallel()
	for name, flags := range map[string]uint16{
		"encrypted entry": testutil.RARFilePassword,
		"solid entry":     testutil.RARFileSolid,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			one, two := page("rp-one", 300), page("rp-two", 300)
			rar := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
				{Name: []byte("01.jpg"), Data: one},
				{Name: []byte("02.jpg"), Data: page("rp-ok", 300)},
				{Name: []byte("03.jpg"), Data: two, Flags: flags},
			}})
			flipByteOf(t, rar, one, 5)
			lib := newLibrary(t, map[string]map[string]any{"m": {"r.rar": rar}},
				source.Book{ID: "R", Kind: source.KindRAR, RootName: "m", RelPath: "r.rar"})

			r := lib.run(t, Options{})
			if r.err != nil {
				t.Fatal(r.err)
			}
			pd := r.pageDefects()
			if len(pd) != 1 || pd[0].pageNo != "1" || !strings.HasPrefix(pd[0].cause, "crc mismatch") {
				t.Fatalf("defects = %+v, want page 1's CRC", r.defects)
			}
			want := Summary{Books: 1, Pages: 2, DefectiveBooks: 1, DefectivePages: 1, Unverifiable: 1}
			if r.sum != want {
				t.Errorf("summary = %+v, want %+v", r.sum, want)
			}
		})
	}
}

// A page whose own entry this build cannot open — an unsupported method in
// the directory, or the encryption bit in the local header only — is the same
// verdict as a whole book that cannot be opened for that reason: unverifiable,
// not damaged. Its neighbours are still checked.
func TestRun_pageWithUnsupportedMethodOrEncryption_isUnverifiableNotADefect(t *testing.T) {
	t.Parallel()
	z := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: page("pu-one", 100)},
		{Name: "02.jpg", Data: page("pu-two", 100)},
		{Name: "03.jpg", Data: page("pu-three", 100)},
		{Name: "04.jpg", Data: page("pu-four", 100), CRC32Override: u32p(0xdeadbeef)},
	}})
	patchCentralMethod(t, z, "02.jpg", 14) // LZMA
	patchLocalFlags(t, z, "03.jpg", testutil.FlagEncrypted)
	lib := newLibrary(t, map[string]map[string]any{"m": {"a.zip": z}}, zipBook("B", "a.zip"))

	r := lib.run(t, Options{})
	if r.err != nil {
		t.Fatal(r.err)
	}
	pd := r.pageDefects()
	if len(pd) != 1 || pd[0].pageNo != "4" {
		t.Fatalf("defects = %+v, want only page 4's CRC", r.defects)
	}
	want := Summary{Books: 1, Pages: 2, DefectiveBooks: 1, DefectivePages: 1, Unverifiable: 1, UnverifiablePages: 2}
	if r.sum != want {
		t.Errorf("summary = %+v, want %+v", r.sum, want)
	}
	if !strings.Contains(r.summary, "\tunverifiable_pages=2") {
		t.Errorf("summary line = %q", r.summary)
	}
}

// More than one worker means more than one book in flight. Each book waits
// (bounded) at its start for a second one to arrive, so the peak is 2 or more
// exactly when the pool really runs books side by side.
func TestRun_manyWorkers_readBooksAtTheSameTime(t *testing.T) {
	t.Parallel()
	layout, books := manyBooks(t)
	lib := newLibrary(t, map[string]map[string]any{"m": layout}, books...)

	var cur, peak atomic.Int32
	overlap := make(chan struct{})
	var once sync.Once
	opts := Options{Workers: 4}
	opts.hooks.book = func(source.Book) func() {
		n := cur.Add(1)
		for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
		}
		if n >= 2 {
			once.Do(func() { close(overlap) })
		} else {
			select {
			case <-overlap:
			case <-time.After(3 * time.Second):
				once.Do(func() { close(overlap) })
			}
		}
		return func() { cur.Add(-1) }
	}
	r := lib.run(t, opts)
	if r.err != nil {
		t.Fatal(r.err)
	}
	if p := peak.Load(); p < 2 {
		t.Errorf("at most %d book in flight with 4 workers", p)
	}
	if r.sum.Books != len(books) {
		t.Errorf("books = %d, want %d", r.sum.Books, len(books))
	}
}

// Cancelled while pages are being read: Run returns promptly with every worker
// finished, says it was interrupted, and reports nothing the cancellation
// itself caused — every BROKEN line it did print is one a full run prints too.
func TestRun_cancelledWhileReadingPages_returnsCleanly(t *testing.T) {
	t.Parallel()
	layout, books := manyBooks(t)
	lib := newLibrary(t, map[string]map[string]any{"m": layout}, books...)
	full := lib.run(t, Options{Workers: 2})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var pages atomic.Int32
	var out bytes.Buffer
	opts := Options{Config: lib.cfg, Out: &out, Logger: quietLogger(), Workers: 2}
	opts.hooks.page = func(source.Book, int) {
		if pages.Add(1) == 6 {
			cancel()
		}
	}
	type ret struct {
		sum Summary
		err error
	}
	done := make(chan ret, 1)
	go func() {
		sum, err := Run(ctx, opts)
		done <- ret{sum, err}
	}()
	var got ret
	select {
	case got = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after a mid-run cancellation")
	}
	if pages.Load() < 6 {
		t.Fatalf("precondition: cancellation was meant to land mid-run, after %d pages", pages.Load())
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", got.err)
	}
	r := parseRun(t, got.sum, got.err, out.String())
	if r.summary == "" {
		t.Error("an interrupted run printed no SUMMARY")
	}
	if r.sum.Books >= len(books) {
		t.Errorf("books = %d: the cancellation did not stop the run", r.sum.Books)
	}
	fullSet := defectSet(full)
	for _, d := range defectSet(r) {
		if !slices.Contains(fullSet, d) {
			t.Errorf("an interrupted run reported a defect a full run does not: %q", d)
		}
	}
}

// A root that cannot be opened does not stop the others: every readable root
// is verified to the end, and the unreadable one is named with its reason.
func TestRun_unreadableRoot_isNamedAndTheOthersAreStillVerified(t *testing.T) {
	t.Parallel()
	bad := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: page("ur", 50), CRC32Override: u32p(9)},
	}})
	lib := newLibrary(t, map[string]map[string]any{"m": {"a.zip": bad}},
		zipBook("M1", "a.zip"),
		source.Book{ID: "O1", Kind: source.KindZIP, RootName: "off", RelPath: "b.zip"},
	)
	gone := filepath.Join(t.TempDir(), "unmounted")
	lib.cfg.Roots = append(lib.cfg.Roots, config.Root{Name: "off", Path: gone, Enabled: true})

	r := lib.run(t, Options{})
	if r.err != nil {
		t.Fatalf("Run: %v", r.err)
	}
	if len(r.defects) != 1 || r.defects[0].bookID != "M1" {
		t.Fatalf("defects = %+v, want M1's CRC only", r.defects)
	}
	if len(r.unchecked) != 1 || !strings.HasPrefix(r.unchecked[0], "off\t") || !strings.Contains(r.unchecked[0], "unmounted") {
		t.Errorf("unchecked = %q, want root off with its reason", r.unchecked)
	}
	want := Summary{Books: 1, Pages: 1, DefectiveBooks: 1, DefectivePages: 1, RootsNotChecked: 1}
	if r.sum != want {
		t.Errorf("summary = %+v, want %+v", r.sum, want)
	}
	if !strings.HasSuffix(r.summary, "\troots_not_checked=1") {
		t.Errorf("summary line = %q", r.summary)
	}
}

// An unmounted volume often leaves its mount point behind as an empty
// directory, which opens like any other. Reporting every one of its books as
// BROKEN would call a cable a library's worth of damage, so a root where not
// one indexed book exists is not checked and is named instead.
func TestRun_rootWhereNoIndexedBookExists_isUncheckedNotBroken(t *testing.T) {
	t.Parallel()
	bad := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "01.jpg", Data: page("nb", 50), CRC32Override: u32p(7)},
	}})
	for name, tree := range map[string]map[string]any{
		"empty mount point": {},
		"some other volume": {"unrelated/readme.txt": "x"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			lib := newLibrary(t, map[string]map[string]any{"m": {"a.zip": bad}, "off": tree},
				zipBook("M1", "a.zip"),
				source.Book{ID: "O1", Kind: source.KindZIP, RootName: "off", RelPath: "b.zip"},
				source.Book{ID: "O2", Kind: source.KindDir, RootName: "off", RelPath: "series/v1"},
			)
			r := lib.run(t, Options{})
			if r.err != nil {
				t.Fatalf("Run: %v", r.err)
			}
			if len(r.defects) != 1 || r.defects[0].bookID != "M1" {
				t.Fatalf("defects = %+v, want M1's CRC only — none from root off", r.defects)
			}
			if len(r.unchecked) != 1 || !strings.HasPrefix(r.unchecked[0], "off\t") || !strings.Contains(r.unchecked[0], "mounted") {
				t.Errorf("unchecked = %q, want root off with a reason that says to check the mount", r.unchecked)
			}
			want := Summary{Books: 1, Pages: 1, DefectiveBooks: 1, DefectivePages: 1, RootsNotChecked: 1}
			if r.sum != want {
				t.Errorf("summary = %+v, want %+v", r.sum, want)
			}
		})
	}
}

// A root the index holds nothing for is not suspicious: there is nothing to
// find missing.
func TestRun_rootWithNoIndexedBooks_isNotUnchecked(t *testing.T) {
	t.Parallel()
	ok := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{{Name: "01.jpg", Data: page("ni", 40)}}})
	lib := newLibrary(t, map[string]map[string]any{"m": {"a.zip": ok}, "new": {}},
		zipBook("M1", "a.zip"),
	)
	r := lib.run(t, Options{})
	if r.err != nil || len(r.unchecked) != 0 || len(r.defects) != 0 {
		t.Fatalf("err = %v, unchecked = %q, defects = %+v; want a clean run", r.err, r.unchecked, r.defects)
	}
}

// The packed RAR cases run the real reader end to end: rar4 splices the entry,
// rardecode unpacks it, and CheckStream reads it through to the EOF where
// rardecode checks the recorded CRC. The bytes are libarchive's (see
// testutil.RARPackedSample); before them the packed path was pinned only by
// injecting rardecode's error at the reader boundary.

func TestRun_rarPackedEntry_isVerifiedAndClean(t *testing.T) {
	t.Parallel()
	rar := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
		testutil.RARPackedSampleEntry([]byte("01.jpg")),
		{Name: []byte("02.jpg"), Data: page("rar-stored", 300)},
	}})
	lib := newLibrary(t, map[string]map[string]any{"m": {"p.rar": rar}},
		source.Book{ID: "R", Kind: source.KindRAR, RootName: "m", RelPath: "p.rar"})

	r := lib.run(t, Options{})
	if r.err != nil || len(r.defects) != 0 {
		t.Fatalf("err %v, defects %+v", r.err, r.defects)
	}
	if r.sum.Pages != 2 || r.sum.Unverifiable != 0 {
		t.Errorf("summary = %+v, want both pages checked", r.sum)
	}
}

func TestRun_rarPackedEntryWithWrongCRC_isRardecodesChecksumDefect(t *testing.T) {
	t.Parallel()
	e := testutil.RARPackedSampleEntry([]byte("01.jpg"))
	e.CRC ^= 1
	rar := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
		e,
		{Name: []byte("02.jpg"), Data: page("rar-stored", 300)},
	}})
	lib := newLibrary(t, map[string]map[string]any{"m": {"p.rar": rar}},
		source.Book{ID: "R", Kind: source.KindRAR, RootName: "m", RelPath: "p.rar"})

	r := lib.run(t, Options{})
	pd := r.pageDefects()
	// rardecode, not CheckStream's own comparison, must be the one that
	// noticed: that is the packed path reaching its EOF.
	if len(r.defects) != 1 || len(pd) != 1 || pd[0].pageNo != "1" || !strings.Contains(pd[0].cause, rardecode.ErrBadFileChecksum.Error()) {
		t.Fatalf("defects = %+v, want page 1's rardecode checksum error", r.defects)
	}
}

func TestRun_rarPackedEntryWithDamagedStream_isAPageDefect(t *testing.T) {
	t.Parallel()
	sample := testutil.RARPackedSample()
	rar := testutil.BuildRAR4(t, testutil.RAR4Spec{Entries: []testutil.RAR4Entry{
		testutil.RARPackedSampleEntry([]byte("01.jpg")),
		{Name: []byte("02.jpg"), Data: page("rar-stored", 300)},
	}})
	flipByteOf(t, rar, sample, len(sample)/2)
	lib := newLibrary(t, map[string]map[string]any{"m": {"p.rar": rar}},
		source.Book{ID: "R", Kind: source.KindRAR, RootName: "m", RelPath: "p.rar"})

	r := lib.run(t, Options{})
	pd := r.pageDefects()
	if len(r.defects) != 1 || len(pd) != 1 || pd[0].pageNo != "1" {
		t.Fatalf("defects = %+v, want page 1 only", r.defects)
	}
}
