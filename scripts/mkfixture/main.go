// Command mkfixture builds the hermetic twin of the curated E2E subset
// (impl-plan §6.3 "Hermetic fallback", decision D-49).
//
// `scripts/e2e.sh --synthetic` runs the *identical* assertion set against this
// tree, so the suite can run on a machine where /mnt/big-data is not mounted.
// The ten directory names are the real ones, byte for byte, because two of the
// assertions are about those bytes: the `[` and `]` are `path.Match` character
// classes that `scan.include_globs` has to escape, and the entry names inside
// the archives are genuine CP949 with the UTF-8 flag clear (AC-002).
//
//	go run ./scripts/mkfixture -out /tmp/shelf-e2e-synthetic
//
// Two shapes exist here that the real collection does not contain and that
// D-49 asks for anyway: an archive with the encryption flag set, and a ZIP64
// archive. Both are hand-built below.
//
// The one deliberate omission is scale. The real 배틀로얄 archive is 1.34 GB of
// scans; this one has the same 1 540 entries, each an 852-byte JPEG, so AC-008's
// "arbitrary jumps in a 500+ page volume" is exercised at full page count for
// 1.22 MB. (Both figures measured off a built tree, 2026-07-29; the whole tree
// is 1.34 MB against D-49's ~12 MB budget.)
//
// What is NOT omitted, since one round of `make e2e-synthetic` was lost to it:
// page *orientation*. See the 군계 block in build().
package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"

	"shelf/internal/testutil"
)

// fixedMtime sits inside the 2014–2018 window AC-002 is about, so the tree is
// byte-reproducible and content_version is stable across runs.
var fixedMtime = time.Date(2016, time.March, 14, 9, 26, 54, 0, time.UTC)

func main() {
	out := flag.String("out", "", "directory to build the fixture tree in (required)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "mkfixture: -out is required")
		os.Exit(2)
	}
	if err := build(*out); err != nil {
		fmt.Fprintf(os.Stderr, "mkfixture: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("mkfixture: built the synthetic curated subset in %s\n", *out)
}

func build(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	b := &builder{root: root}

	// 1 — folder of ZIPs, CP949 entry names, four volumes (prd §2.2 row 1).
	for i := 1; i <= 4; i++ {
		b.zipFile(fmt.Sprintf("Clover 클로버 (총4권)/Clover 클로버 %d.zip", i),
			cp949Pages(6))
	}

	// 2 — folder of image sub-folders: books of kind "dir" (prd §2.2 row 2).
	for v := 1; v <= 3; v++ {
		for p := 1; p <= 5; p++ {
			b.file(fmt.Sprintf("상처를 쫓는자 1-11 (완) 이케가미 료이치/%02d권/%03d.jpg", v, p), jpegPage())
		}
	}

	// 3 — images directly inside the series directory, with the mixed
	// zero-padding that makes natural sort load-bearing (prd §2.2 row 3, D-8).
	for _, n := range []int{1, 2, 3, 9, 10, 11, 99, 100, 101, 114, 122} {
		b.file(fmt.Sprintf("자살도114-122/%d.jpg", n), jpegPage())
	}

	// 4 — a single top-level ZIP: the series is its own book (prd §2.2 row 4).
	b.zipFile("바퀴.zip", cp949Pages(8))

	// 5 — "mixed" as it actually occurs: N archives + exactly one cover image,
	// which is a cover and not a one-page book (D-5, D-27).
	for i := 1; i <= 6; i++ {
		b.zipFile(fmt.Sprintf("강철의 연금술사 1~27권 완결/강철의 연금술사 %02d권.zip", i), cp949Pages(4))
	}
	b.file("강철의 연금술사 1~27권 완결/강철의 연금술사 00 Cover.jpg", jpegPage())

	// 6 — the messy one: a named cover file, a folder *and* a zip for the same
	// volume, three copies of 07권, and two truncated archives (D-6, FR-IDX-010).
	//
	// 01권 is the one volume in this tree whose *pages* are landscape, and that
	// is not decoration. Ruling E-23 (decisions.md) measured the real
	// 군계(軍鷄) 01권 at 104 / 104 landscape pages — 1072×813 or 1075×811, PIL
	// over 100 % of the volume — and names it "the **only** curated volume that
	// exercises FR-VWR-004's landscape auto-split". A portrait twin reproduces
	// the volume's name and not its shape, and the cost was measurable: with
	// every page in the tree portrait, `isLandscape` (web/src/features/viewer/
	// fit.ts) answered false for every page of `make e2e-synthetic`, so the
	// auto-split branch never ran and 04-viewer 6.6's FR-VWR-004 assertion was
	// really passing on the last-page clamp. Both 01권 twins therefore carry
	// landscape pages, and gungyeVol1Pages is what keeps §6.3 step 6.6's "→ five
	// times" landing on a page that still has a facing page (1 + 5 = 6 < 8) and
	// off the last page, where FR-VWR-010's end-of-volume scrim covers the
	// chrome. The other 군계 volumes stay portrait: E-23 measured 01권 only, and
	// nothing asserts the orientation of the rest.
	const gungyeVol1Pages = 8
	b.file("군계 1~25/[cover].jpg", jpegPage())
	for p := 1; p <= gungyeVol1Pages; p++ {
		b.file(fmt.Sprintf("군계 1~25/군계(軍鷄) 01권/%03d.jpg", p), landscapeJPEG())
	}
	b.zipFile("군계 1~25/군계(軍鷄) 01권.zip", cp949LandscapePages(gungyeVol1Pages))
	for i := 2; i <= 6; i++ {
		b.zipFile(fmt.Sprintf("군계 1~25/군계(軍鷄) %02d권.zip", i), cp949Pages(3))
	}
	b.zipFile("군계 1~25/군계(軍鷄) 07권.zip", cp949Pages(3))
	b.truncatedZip("군계 1~25/군계(軍鷄) 07권.repair.zip", cp949Pages(3))
	b.truncatedZip("군계 1~25/군계(軍鷄) 07권 (2).repair.zip", cp949Pages(3))

	// 7 — a 0-byte archive: an unopenable container the scan must isolate and
	// carry on past (FR-IDX-010; the real D.N.Angel 08권.zip is 0 bytes).
	for i := 1; i <= 5; i++ {
		if i == 3 {
			b.file("디엔엔젤 1-13권 연재중/D.N.Angel 03권.zip", nil)
			continue
		}
		b.zipFile(fmt.Sprintf("디엔엔젤 1-13권 연재중/D.N.Angel %02d권.zip", i), cp949Pages(3))
	}

	// 8 — PDFs (prd §2.2 row 5, AC-004, FR-SRV-006).
	for i := 1; i <= 3; i++ {
		b.file(fmt.Sprintf("미생 1~9 (완결 pdf)/미생 %02d권.pdf", i), pdfDocument(4))
	}

	// 9 — AC-008 at full page count, in the shape the real archive turned out to
	// have: fifteen directories of ~103 pages, one per volume (D-73), not one
	// flat run of 1 540. The twin has to carry that, or the synthetic round is
	// permanently green on a shape it never builds — which is the one way these
	// two rounds stop being the same gate.
	b.zipFile("배틀로얄 1~15 [완결].zip", cp949ChapterPages(15, 103))

	// 10 — a container of sub-archives and no images of its own. Since D-70
	// each inner archive is a 권 of its own, so this is a series of four books
	// rather than the one empty book D-10 used to make of it.
	var nested []entry
	for i := 1; i <= 4; i++ {
		inner, err := zipBytes(cp949Pages(2), 0)
		if err != nil {
			return err
		}
		nested = append(nested, entry{name: fmt.Sprintf("엔젤하트 %02d권.zip", i), data: inner})
	}
	b.zipFile("엔젤하트 전32권 완결.zip", nested)

	// 10b — an archive that opens cleanly and holds nothing that is a page, and
	// nothing that is a volume either: the `empty` book, and with it ruling
	// E-14's series-level `error`, which the container above no longer
	// demonstrates. The real collection's `비둘기.zip` is this shape — a single
	// directory entry and 128 bytes.
	b.zipFile("비둘기.zip", []entry{{name: "비둘기/", data: nil}})

	// 11 — RAR, added by D-71. The real collection has 14 of these holding
	// 2,914 pages, and until D-71 not one of them was a book.
	//
	// Three shapes, because three things can go wrong independently:
	//
	//   a. a plain RAR series, the ordinary case (12 of the 14 real archives
	//      are stored-only like this one);
	//   b. a container mixing ZIP and RAR volumes, which is
	//      `사모님은 학생회장.zip` — 7 ZIPs and 8 RARs, of which D-07 indexed 7;
	//   c. a solid RAR, which this build refuses on purpose. None of the 14 is
	//      solid; if one ever were, a page could not be read without reading
	//      every page before it, and NFR-PRF-006 would be a lie. The refusal
	//      has to be `unsupported`, never `error`: the file is not damaged.
	//
	// The names and shapes are the real ones, down to the `(完)` on 라제폰 3권.
	b.rarFile("라제폰 1-3권 완결/라제폰 1권[번역].rar", cp949Pages(3))
	b.rarFile("라제폰 1-3권 완결/라제폰 2권[번역].rar", cp949Pages(3))
	b.rarFile("라제폰 1-3권 완결/라제폰 3권[번역](完).rar", cp949Pages(3))

	// 울프가이 is the shape that matters most, because it is the one no unit
	// test can be: ZIP and RAR volumes side by side in one series folder, one
	// level down, so the two readers have to agree on page order, on naming and
	// on 권 numbering within a single 권 list. The RAR names are Shift_JIS,
	// which no per-entry test can identify — kenc.ArchiveFallback has to convict
	// the archive as a whole (the real v01 has 207 such entries).
	const wolf = "울프가이/[일어원문] Wolf Guy1-12권(완)/"
	b.rarFile(wolf+"Wolf_Guy_-_Wolfen_Crest_v01_JP.rar", shiftJISPages(3))
	b.zipFile(wolf+"Wolf_Guy_-_Wolfen_Crest_v02_JP.zip", shiftJISPages(3))
	b.rarFile(wolf+"Wolf_Guy_-_Wolfen_Crest_v03_JP.rar", shiftJISPages(3))

	mixed, err := mixedVolumes()
	if err != nil {
		return err
	}
	b.zipFile("사모님은 학생회장.zip", mixed)

	b.solidRar("솔리드 테스트.rar", cp949Pages(3))

	// 12 — a book that is one container this build has no reader for. The real
	// case is `펌프킨 시저스 04.zip`: 39.5 MB in a single `.hv3`, a proprietary
	// and (measurably) encrypted format. D-72 is that such a book reports the
	// format it holds rather than `비어 있음`, which is what 비둘기.zip above
	// still, correctly, reports.
	b.zipFile("펌프킨 시저스 1~13권/펌프킨 시저스 04.zip", []entry{
		{name: "펌프킨 시저스 04.hv3", data: hv3Blob()},
	})
	for i := 1; i <= 2; i++ {
		b.zipFile(fmt.Sprintf("펌프킨 시저스 1~13권/펌프킨 시저스 %02d.zip", i), cp949Pages(3))
	}

	// D-49's two extras, which the real collection has no sample of.
	b.encryptedZip("암호화 테스트.zip", cp949Pages(3))
	b.zip64("ZIP64 테스트.zip")

	return b.err
}

// mixedVolumes builds `사모님은 학생회장.zip`'s contents: volumes of both
// formats plus one this build cannot open, so the assertion set can see that
// the first two become books and the third does not.
func mixedVolumes() ([]entry, error) {
	zipVol, err := zipBytes(cp949Pages(2), 0)
	if err != nil {
		return nil, err
	}
	rarVol, err := rarBytes(cp949Pages(3), 0)
	if err != nil {
		return nil, err
	}
	return []entry{
		{name: "사모님은 학생회장! 11화.rar", data: rarVol},
		{name: "사모님은 학생회장! 12화.rar", data: rarVol},
		{name: "사모님은 학생회장! 19화.zip", data: zipVol},
		{name: "사모님은 학생회장! 1권 (번역).zip", data: zipVol},
		{name: "사모님은 학생회장! 특전.7z", data: []byte("7z\xbc\xaf\x27\x1cnot a real 7z")},
	}, nil
}

// hv3Blob is the head of an HV3 container, reproduced from the real
// `펌프킨 시저스 04.hv3` down to the chunk names: the magic, the version, the
// declared file size, a HEAD block carrying GUID/UUID/FTIM/TITL/MAKR, and the
// ENCR chunk that says the payload is encrypted. The body here is filler.
//
// Nothing in this product parses any of it — the extension is what classifies
// the book (FR-IDX-002 forbids reading a payload at index time). It is written
// faithfully anyway so that a future reader of this fixture can see what the
// real file is, rather than a placeholder that would teach them nothing.
func hv3Blob() []byte {
	var b bytes.Buffer
	const total = 4096
	b.WriteString("HV30")
	_ = binary.Write(&b, binary.LittleEndian, uint32(24))
	_ = binary.Write(&b, binary.LittleEndian, uint32(total-40))
	_ = binary.Write(&b, binary.LittleEndian, uint32(0))

	chunk := func(tag string, payload []byte) {
		b.WriteString(tag)
		_ = binary.Write(&b, binary.LittleEndian, uint32(len(payload)))
		b.Write(payload)
	}
	chunk("VERS", []byte{0x07, 0x04, 0x08, 0x20})
	chunk("FSIZ", []byte{0x00, 0x10, 0x00, 0x00})

	var head bytes.Buffer
	_ = binary.Write(&head, binary.LittleEndian, uint64(18528))
	sub := func(tag string, payload []byte) {
		head.WriteString(tag)
		_ = binary.Write(&head, binary.LittleEndian, uint32(len(payload)))
		head.Write(payload)
	}
	sub("GUID", make([]byte, 16))
	sub("UUID", make([]byte, 16))
	sub("FTIM", make([]byte, 8))
	sub("TITL", utf16le("펌프킨 시저스_Pumpkin Scissors_04"))
	sub("MAKR", utf16le("Scan by Q.H"))
	chunk("HEAD", head.Bytes())

	chunk("ENCR", []byte{0x02, 0x00, 0x00, 0x00})
	chunk("LIST", nil)

	for b.Len() < total {
		b.WriteByte(byte(b.Len() * 31 % 251))
	}
	return b.Bytes()
}

func utf16le(s string) []byte {
	out := make([]byte, 0, len(s)*2)
	for _, r := range utf16.Encode([]rune(s)) {
		out = append(out, byte(r), byte(r>>8))
	}
	return out
}

// ---------------------------------------------------------------------------
// Writing
// ---------------------------------------------------------------------------

type builder struct {
	root string
	err  error
}

func (b *builder) file(rel string, data []byte) {
	if b.err != nil {
		return
	}
	p := filepath.Join(b.root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		b.err = err
		return
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		b.err = err
		return
	}
	// A stable mtime keeps content_version and the incremental scan
	// reproducible across builds of the fixture.
	if err := os.Chtimes(p, fixedMtime, fixedMtime); err != nil {
		b.err = err
	}
}

func (b *builder) zipFile(rel string, entries []entry) {
	if b.err != nil {
		return
	}
	data, err := zipBytes(entries, 0)
	if err != nil {
		b.err = err
		return
	}
	b.file(rel, data)
}

// rarFile writes a RAR 4.x archive of stored entries, the shape 12 of the
// collection's 14 real RARs have.
//
// The bytes come from internal/testutil, the same writer the unit fixtures use,
// so a change to either is a change to both. Writing a second RAR writer here
// would produce a fixture that could agree with a bug the unit tests were also
// agreeing with.
func (b *builder) rarFile(rel string, entries []entry) {
	if b.err != nil {
		return
	}
	data, err := rarBytes(entries, 0)
	if err != nil {
		b.err = err
		return
	}
	b.file(rel, data)
}

// solidRar sets the archive-wide solid flag. Nothing is actually solid-packed —
// what the indexer reads is the flag, and the flag is what makes the book
// `status:"unsupported"` rather than `error`. Zero solid archives exist in the
// reference collection, which is why this has to be synthetic.
func (b *builder) solidRar(rel string, entries []entry) {
	if b.err != nil {
		return
	}
	data, err := rarBytes(entries, testutil.RARMainSolid)
	if err != nil {
		b.err = err
		return
	}
	b.file(rel, data)
}

func rarBytes(entries []entry, mainFlags uint16) ([]byte, error) {
	spec := testutil.RAR4Spec{MainFlags: mainFlags}
	for _, e := range entries {
		name := e.rawName
		if name == nil {
			name = []byte(e.name)
		}
		spec.Entries = append(spec.Entries, testutil.RAR4Entry{Name: name, Data: e.data})
	}
	return testutil.RAR4Bytes(spec)
}

// truncatedZip writes an archive whose tail — and therefore whose end-of-
// central-directory record — is missing. It is the shape of all nine real
// broken archives (data-survey D-4).
func (b *builder) truncatedZip(rel string, entries []entry) {
	if b.err != nil {
		return
	}
	data, err := zipBytes(entries, 0)
	if err != nil {
		b.err = err
		return
	}
	b.file(rel, data[:len(data)*2/3])
}

// encryptedZip sets general-purpose bit 0 on every entry. Nothing is actually
// encrypted — what the indexer reads is the flag, and the flag is what makes
// the book `status:"encrypted"` rather than `error`. Zero encrypted archives
// exist in the reference collection, which is why this has to be synthetic.
func (b *builder) encryptedZip(rel string, entries []entry) {
	if b.err != nil {
		return
	}
	data, err := zipBytes(entries, 0x0001)
	if err != nil {
		b.err = err
		return
	}
	b.file(rel, data)
}

type entry struct {
	name    string
	rawName []byte // exact bytes; wins over name
	data    []byte
}

// zipBytes writes a normal ZIP. extraFlags is OR-ed into every entry's
// general-purpose bit flag.
//
// Names are written through zip.FileHeader.Name with the UTF-8 flag left clear
// and the raw CP949 bytes smuggled through as a Latin-1 string, because that is
// exactly how the 2014–2018 Windows tools that produced the real collection
// wrote them: 14 630 of 14 630 flagless non-ASCII names are CP949 (D-1).
func zipBytes(entries []entry, extraFlags uint16) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		name := e.name
		if e.rawName != nil {
			// A Go string is a byte sequence, not a rune sequence, and
			// archive/zip writes `[]byte(name)` verbatim — so converting the
			// CP949 bytes straight to a string puts exactly those bytes in the
			// header. (Widening each byte to a rune instead would re-encode
			// them as UTF-8 and produce Latin-1 mojibake that kenc would then
			// correctly decode as UTF-8, which is not the case under test.)
			name = string(e.rawName)
		}
		fh := &zip.FileHeader{
			Name:     name,
			Method:   zip.Deflate,
			Modified: fixedMtime,
		}
		fh.Flags |= extraFlags
		fh.NonUTF8 = true // never set bit 11: the CP949 path is the point
		f, err := w.CreateHeader(fh)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// cp949Pages builds n portrait JPEG pages whose names are real CP949 bytes with
// the UTF-8 flag clear — the AC-002 shape.
func cp949Pages(n int) []entry { return cp949PagesOf(n, jpegPage) }

// cp949LandscapePages is the same archive shape with landscape page content, for
// the one volume that has to reproduce a two-page scan (군계 01권; see build()).
// The names are identical, so AC-002's 500-name sample is unaffected.
func cp949LandscapePages(n int) []entry { return cp949PagesOf(n, landscapeJPEG) }

// shiftJISPages is cp949Pages for a Japanese archive: the entry names are
// Shift_JIS, which no per-entry test can identify (Shift_JIS reads Korean bytes
// happily), so kenc.ArchiveFallback has to convict the whole archive. The
// collection's four 울프가이 RARs are this shape.
func shiftJISPages(n int) []entry {
	enc := japanese.ShiftJIS.NewEncoder()
	out := make([]entry, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("第01巻 狼の紋章/%04d頁.jpg", i)
		raw, err := enc.Bytes([]byte(name))
		if err != nil {
			raw = []byte(name)
		}
		out = append(out, entry{rawName: raw, data: jpegPage()})
	}
	return out
}

// cp949ChapterPages is one container holding `chapters` per-volume directories
// of `each` pages — the D-73 shape, and 4.3% of the real collection. The
// directory names carry Hangul for the same reason the page names do: the
// decoded name is what becomes books.inner_path and the volume's title, so a
// CP949 directory that decodes wrong is a 권 nobody can identify.
func cp949ChapterPages(chapters, each int) []entry {
	enc := korean.EUCKR.NewEncoder()
	out := make([]entry, 0, chapters*each)
	for c := 1; c <= chapters; c++ {
		for i := 1; i <= each; i++ {
			name := fmt.Sprintf("배틀로얄 %02d권/페이지%04d.jpg", c, i)
			raw, err := enc.Bytes([]byte(name))
			if err != nil {
				raw = []byte(name)
			}
			out = append(out, entry{rawName: raw, data: jpegPage()})
		}
	}
	return out
}

func cp949PagesOf(n int, page func() []byte) []entry {
	enc := korean.EUCKR.NewEncoder()
	out := make([]entry, 0, n)
	for i := 1; i <= n; i++ {
		name := fmt.Sprintf("페이지%04d.jpg", i)
		raw, err := enc.Bytes([]byte(name))
		if err != nil {
			raw = []byte(name) // cannot happen for Hangul; fall back to UTF-8
		}
		out = append(out, entry{rawName: raw, data: page()})
	}
	return out
}

// jpegPage is one tiny but genuine JPEG. Portrait, so the FR-VWR-004 landscape
// auto-split does not fire on it — which is what lets 자살도 (04-viewer 6.6b)
// and 미생 (05-pdf-and-large 6.8) prove the 양면 pairing half in synthetic mode.
// Every raster page in the tree is this one except 군계 01권's.
var cachedJPEG []byte

func jpegPage() []byte {
	if cachedJPEG != nil {
		return cachedJPEG
	}
	img := image.NewRGBA(image.Rect(0, 0, 40, 60))
	for y := range 60 {
		for x := range 40 {
			img.Set(x, y, color.RGBA{R: uint8(x * 6), G: uint8(y * 4), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		panic(err)
	}
	cachedJPEG = buf.Bytes()
	return cachedJPEG
}

// landscapeJPEG is jpegPage's counterpart: 60×40, so `w > h` and FR-VWR-004
// *does* fire. Used by 군계 01권 alone (see build()), which is the shape ruling
// E-23 measured at 104 / 104 landscape in the real collection. The multipliers
// differ from jpegPage's only so that neither channel overflows uint8 at the
// other aspect (59×4 = 236, 39×6 = 234).
var cachedLandscapeJPEG []byte

func landscapeJPEG() []byte {
	if cachedLandscapeJPEG != nil {
		return cachedLandscapeJPEG
	}
	img := image.NewRGBA(image.Rect(0, 0, 60, 40))
	for y := range 40 {
		for x := range 60 {
			img.Set(x, y, color.RGBA{R: uint8(x * 4), G: uint8(y * 6), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 70}); err != nil {
		panic(err)
	}
	cachedLandscapeJPEG = buf.Bytes()
	return cachedLandscapeJPEG
}

// ---------------------------------------------------------------------------
// A minimal PDF (AC-004, FR-SRV-006)
// ---------------------------------------------------------------------------

// pdfDocument writes a valid n-page PDF with a real cross-reference table. It
// is deliberately hand-assembled rather than produced by a library: the
// dependency set is frozen at the nine modules of arch §1.1 (D-08), and a PDF
// this simple is 40 lines.
func pdfDocument(pages int) []byte {
	var buf bytes.Buffer
	offsets := []int{0} // object 0 is the free-list head

	obj := func(body string) {
		offsets = append(offsets, buf.Len())
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", len(offsets)-1, body)
	}

	buf.WriteString("%PDF-1.4\n%\xe2\xe3\xcf\xd3\n")

	// 1 catalog, 2 pages tree, then per page: a page object and its content.
	kids := make([]string, 0, pages)
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", 3+i*2))
	}
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	obj(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pages))
	for i := range pages {
		content := fmt.Sprintf("BT /F1 36 Tf 72 700 Td (SHELF synthetic page %d) Tj ET\n", i+1)
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] "+
			"/Resources << /Font << /F1 %d 0 R >> >> /Contents %d 0 R >>",
			3+pages*2, 4+i*2))
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", len(content), content))
	}
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n0000000000 65535 f \n", len(offsets))
	for _, off := range offsets[1:] {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets), xref)
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// A hand-built ZIP64 archive (FR-IDX-009, D-26, D-49)
// ---------------------------------------------------------------------------

// zip64 writes a one-entry archive that uses the ZIP64 end-of-central-directory
// record, its locator, and a 0x0001 extra field in the central directory, with
// the 32-bit slots set to the 0xffffffff sentinel.
//
// It is written by hand because archive/zip only escalates to ZIP64 when a real
// value overflows 32 bits, and the smallest such archive is 4 GB. The largest
// real archive in the collection is 1.48 GB, so no ZIP64 sample exists and
// FR-IDX-009 — which is 필수 — has to be exercised synthetically.
func (b *builder) zip64(rel string) {
	if b.err != nil {
		return
	}
	name := []byte("zip64-page-0001.jpg")
	data := jpegPage()
	crc := crc32.ChecksumIEEE(data)
	size := uint64(len(data))

	var out bytes.Buffer
	le := binary.LittleEndian
	put16 := func(w *bytes.Buffer, v uint16) { _ = binary.Write(w, le, v) }
	put32 := func(w *bytes.Buffer, v uint32) { _ = binary.Write(w, le, v) }
	put64 := func(w *bytes.Buffer, v uint64) { _ = binary.Write(w, le, v) }

	const (
		sentinel32 = uint32(0xffffffff)
		sentinel16 = uint16(0xffff)
	)

	// ---- local file header (stored, so the payload is the file) ----------
	localOff := uint64(out.Len())
	put32(&out, 0x04034b50)
	put16(&out, 45) // version needed: 4.5 = ZIP64
	put16(&out, 0)  // flags: no UTF-8 bit, ASCII name
	put16(&out, 0)  // method 0: stored (FR-SRV-003's passthrough path)
	put16(&out, 0x4b1c)
	put16(&out, 0x4c6e)
	put32(&out, crc)
	put32(&out, uint32(size))
	put32(&out, uint32(size))
	put16(&out, uint16(len(name)))
	put16(&out, 0)
	out.Write(name)
	out.Write(data)

	// ---- central directory, with the 0x0001 extra --------------------------
	// The extra field carries the slots that held 0xffffffff, in the fixed
	// order of APPNOTE §4.5.3: uncompressed, compressed, local-header offset.
	var extra bytes.Buffer
	put16(&extra, 0x0001)
	put16(&extra, 24)
	put64(&extra, size)
	put64(&extra, size)
	put64(&extra, localOff)

	cdOff := uint64(out.Len())
	put32(&out, 0x02014b50)
	put16(&out, 45)
	put16(&out, 45)
	put16(&out, 0)
	put16(&out, 0)
	put16(&out, 0x4b1c)
	put16(&out, 0x4c6e)
	put32(&out, crc)
	put32(&out, sentinel32) // compressed size lives in the extra
	put32(&out, sentinel32) // uncompressed size lives in the extra
	put16(&out, uint16(len(name)))
	put16(&out, uint16(extra.Len()))
	put16(&out, 0)          // comment length
	put16(&out, 0)          // disk number start
	put16(&out, 0)          // internal attributes
	put32(&out, 0)          // external attributes
	put32(&out, sentinel32) // local header offset lives in the extra
	out.Write(name)
	out.Write(extra.Bytes())
	cdSize := uint64(out.Len()) - cdOff

	// ---- ZIP64 EOCD record, locator, and the classic EOCD ------------------
	z64Off := uint64(out.Len())
	put32(&out, 0x06064b50)
	put64(&out, 44) // size of the remainder of this record
	put16(&out, 45)
	put16(&out, 45)
	put32(&out, 0) // this disk
	put32(&out, 0) // disk with the central directory
	put64(&out, 1) // entries on this disk
	put64(&out, 1) // entries total
	put64(&out, cdSize)
	put64(&out, cdOff)

	put32(&out, 0x07064b50)
	put32(&out, 0)
	put64(&out, z64Off)
	put32(&out, 1)

	put32(&out, 0x06054b50)
	put16(&out, 0)
	put16(&out, 0)
	put16(&out, sentinel16)
	put16(&out, sentinel16)
	put32(&out, sentinel32)
	put32(&out, sentinel32)
	put16(&out, 0)

	b.file(rel, out.Bytes())
}
