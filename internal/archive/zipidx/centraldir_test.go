package zipidx_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"shelf/internal/archive"
	"shelf/internal/archive/zipidx"
	"shelf/internal/kenc"
	"shelf/internal/testutil"
)

// ---------------------------------------------------------------------------
// The fixture corpus.
//
// Every shape impl-plan §6.1 lists for WP-04 lives here once and is reused by
// the parity test, the read-accounting test and the benchmarks, so a shape
// cannot be exercised by one of them and quietly skipped by another.
// ---------------------------------------------------------------------------

type fixture struct {
	name string
	data []byte
	// wantErr: our reader must report a failure for this archive.
	wantErr bool
	// wantNames: the decoded entry names, in central-directory order. nil
	// means "do not assert".
	wantNames []string
}

const (
	koreanCP949 = "슈퍼만화데생" // arch §4.4's golden vector, as raw CP949 bytes
	koreanUTF8  = "한글 페이지" // written as UTF-8 with and without the flag
)

func corpus(t testing.TB) []fixture {
	t.Helper()

	jpg := testutil.TinyJPEG(t, 16, 24)
	png := testutil.TinyPNG(t, 8, 8)

	return []fixture{
		{
			name: "stored_and_deflate",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
				{Name: "002.jpg", Data: jpg, Method: testutil.MethodDeflate},
				{Name: "010.png", Data: png, Method: testutil.MethodDeflate},
			}}),
			wantNames: []string{"001.jpg", "002.jpg", "010.png"},
		},
		{
			// FR-IDX-008 / AC-002: raw CP949 bytes, no UTF-8 flag. zipidx must
			// hand these to kenc untouched and get Korean back.
			name: "cp949_names_no_flag",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{RawName: testutil.CP949(t, koreanCP949+" 01.jpg"), Data: jpg, Method: testutil.MethodDeflate},
				{RawName: testutil.CP949(t, koreanCP949+" 02.jpg"), Data: jpg, Method: testutil.MethodDeflate},
			}}),
			wantNames: []string{koreanCP949 + " 01.jpg", koreanCP949 + " 02.jpg"},
		},
		{
			name: "utf8_names_with_flag",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: koreanUTF8 + " 1.jpg", Data: jpg, Flags: testutil.FlagUTF8},
			}}),
			wantNames: []string{koreanUTF8 + " 1.jpg"},
		},
		{
			// The branch decision D-24 exists for: modern archivers write UTF-8
			// and forget the flag. Guessing CP949 here would corrupt the name.
			name: "utf8_names_without_flag",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: koreanUTF8 + " 1.jpg", Data: jpg},
			}}),
			wantNames: []string{koreanUTF8 + " 1.jpg"},
		},
		{
			name: "nested_dirs_and_junk",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "vol1", Dir: true},
				{Name: "vol1/001.jpg", Data: jpg},
				{Name: "vol1/Thumbs.db", Data: []byte("junk")},
				{Name: "vol1/.DS_Store", Data: []byte("junk")},
				{Name: "vol1/desktop.ini", Data: []byte("junk")},
				{Name: "__MACOSX/", Dir: true},
				{Name: "__MACOSX/vol1/._001.jpg", Data: []byte("fork")},
				{Name: "vol1/empty.jpg", Data: nil},
				{Name: "vol1/002.jpg", Data: jpg},
			}}),
			wantNames: []string{
				"vol1/", "vol1/001.jpg", "vol1/Thumbs.db", "vol1/.DS_Store",
				"vol1/desktop.ini", "__MACOSX/", "__MACOSX/vol1/._001.jpg",
				"vol1/empty.jpg", "vol1/002.jpg",
			},
		},
		{
			// FR-IDX-010. The archive is structurally perfect; only the payload
			// is unreadable, so indexing must succeed and the *book* becomes
			// status='encrypted'.
			name: "encrypted_entries",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "001.jpg", Data: jpg, Method: testutil.MethodDeflate, Flags: testutil.FlagEncrypted},
				{Name: "002.jpg", Data: jpg, Method: testutil.MethodDeflate, Flags: testutil.FlagEncrypted},
			}}),
			wantNames: []string{"001.jpg", "002.jpg"},
		},
		{
			// Forces the second, 65 557-byte tail scan.
			name: "comment_40kib",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{
				Entries:     []testutil.Entry{{Name: "001.jpg", Data: jpg}},
				CommentSize: testutil.CommentSize40KiB,
			}),
			wantNames: []string{"001.jpg"},
		},
		{
			name:      "empty_archive",
			data:      testutil.BuildZIP(t, testutil.ZIPSpec{}),
			wantNames: []string{},
		},
		{
			// A self-extracting archive: offsets are relative to the payload,
			// not the file. baseOffset recovery, matching archive/zip's.
			name: "prefixed_offsets",
			data: prefixedZIP(t, jpg),
		},
		{
			// The mirror image of garbage_after_eocd, and a far nastier shape:
			// junk between the directory and the end record makes
			// base = endOffset-dirSize-dirOffset come out non-zero even though
			// every recorded offset is already absolute. archive/zip recovers by
			// probing the uncorrected offset for a real record, so we must too,
			// or the verdicts diverge (decision E-2, impl-plan §0.1 C-6).
			name:      "junk_before_eocd",
			data:      junkBeforeEOCD(t, jpg, 64),
			wantNames: []string{"001.jpg", "002.jpg"},
		},
		{
			name: "garbage_after_eocd",
			data: append(
				testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
					{Name: "001.jpg", Data: jpg},
				}}),
				bytes.Repeat([]byte{0xde, 0xad, 0xbe, 0xef}, 64)...,
			),
			wantNames: []string{"001.jpg"},
		},
		// ---- ZIP64 (FR-IDX-009) --------------------------------------------
		{
			name: "zip64_no_disk_field",
			data: testutil.BuildZIP64(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
				{Name: "002.jpg", Data: jpg, Method: testutil.MethodDeflate},
			}}, testutil.ZIP64Spec{}),
			wantNames: []string{"001.jpg", "002.jpg"},
		},
		{
			name: "zip64_with_disk_field",
			data: testutil.BuildZIP64(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "001.jpg", Data: jpg, Method: testutil.MethodDeflate},
			}}, testutil.ZIP64Spec{IncludeDiskField: true}),
			wantNames: []string{"001.jpg"},
		},
		{
			name: "zip64_local_headers",
			data: testutil.BuildZIP64(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{RawName: testutil.CP949(t, koreanCP949+".jpg"), Data: jpg, Method: testutil.MethodDeflate},
			}}, testutil.ZIP64Spec{LocalHeaders: true}),
			wantNames: []string{koreanCP949 + ".jpg"},
		},
		{
			// FR-IDX-009 / WP-04 acceptance 2: testutil.BuildZIP64 always
			// escalates all three 32-bit slots at once, which cannot tell a
			// by-need parser from a positional one. These two do — and the
			// offset-only shape is the one a real >4 GB archive writes for the
			// entries whose own sizes still fit in 32 bits.
			name:      "zip64_offset_escalated_only",
			data:      partialZIP64(t, jpg, zip64Slots{offset: true}),
			wantNames: []string{"001.jpg", "002.jpg"},
		},
		{
			name:      "zip64_sizes_escalated_only",
			data:      partialZIP64(t, jpg, zip64Slots{uncomp: true, comp: true}),
			wantNames: []string{"001.jpg", "002.jpg"},
		},
		// ---- malformed ------------------------------------------------------
		{
			// The real failure shape: 9 of 11 157 archives in the collection,
			// all interrupted downloads (arch §4.11).
			name: "truncated_tail_eocd_gone",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{
				Entries:      []testutil.Entry{{Name: "001.jpg", Data: jpg}},
				TruncateTail: 40,
			}),
			wantErr: true,
		},
		{
			// arch §4.3 step 1: the end record's comment-length field must be
			// consistent with the bytes that follow it. Here it is not — the
			// comment was cut short — and both implementations must refuse the
			// record rather than parse a directory out of the offsets it holds.
			name: "eocd_comment_runs_past_eof",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{
				Entries:      []testutil.Entry{{Name: "001.jpg", Data: jpg}},
				CommentSize:  1000,
				TruncateTail: 100,
			}),
			wantErr: true,
		},
		{
			name: "corrupt_eocd_signature",
			data: testutil.BuildZIP(t, testutil.ZIPSpec{
				Entries:              []testutil.Entry{{Name: "001.jpg", Data: jpg}},
				CorruptEOCDSignature: true,
			}),
			wantErr: true,
		},
		{
			name:    "corrupt_central_record",
			data:    corruptFirstCentralRecord(t, jpg),
			wantErr: true,
		},
		{
			name:    "empty_file",
			data:    []byte{},
			wantErr: true,
		},
		{
			name:    "not_a_zip",
			data:    bytes.Repeat([]byte("not a zip at all "), 200),
			wantErr: true,
		},
	}
}

// prefixedZIP builds a self-extracting-style archive: a stub in front, and
// every recorded offset relative to the payload rather than to the file.
func prefixedZIP(t testing.TB, jpg []byte) []byte {
	t.Helper()
	stub := bytes.Repeat([]byte("#!/bin/sh\nexit 0\n"), 32)
	return testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{Name: "001.jpg", Data: jpg, Method: testutil.MethodDeflate},
			{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
		},
		Prefix:     stub,
		OffsetBias: -int64(len(stub)),
	})
}

// junkBeforeEOCD splices n bytes of junk between the end of the central
// directory and the end record, leaving the recorded directory size and offset
// untouched.
//
// The resulting archive is perfectly readable — every recorded offset is
// absolute and correct — but base = endOffset - dirSize - dirOffset now comes
// out as n rather than 0, so a reader that trusts that arithmetic parses the
// directory n bytes off and calls the book corrupt. archive/zip recovers by
// probing the uncorrected offset for a valid record; this is the fixture that
// makes us do the same.
func junkBeforeEOCD(t testing.TB, jpg []byte, n int) []byte {
	t.Helper()
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodDeflate},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore},
	}})
	at := eocdOffset(t, data)
	// 'Z' bytes: no 'P', so the filler cannot contain a ZIP signature.
	out := make([]byte, 0, len(data)+n)
	out = append(out, data[:at]...)
	out = append(out, bytes.Repeat([]byte{'Z'}, n)...)
	return append(out, data[at:]...)
}

// zip64Slots names which of a central-directory record's three 32-bit slots are
// replaced by the 0xffffffff sentinel and moved into the 0x0001 extra field.
type zip64Slots struct{ uncomp, comp, offset bool }

// partialZIP64 builds an archive whose central-directory records escalate only
// the requested slots into a ZIP64 extended-information extra field.
//
// APPNOTE §4.5.3 puts the members in a fixed order and includes each one *only*
// when its 32-bit counterpart held the sentinel, so "offset only" writes an
// 8-byte field holding the offset — a positional parser reads that as the
// uncompressed size. testutil.BuildZIP64 cannot produce this shape (it always
// escalates all three), which is why it is built here.
//
// The archive keeps a legacy end record with real values: none of the *end*
// record's fields need escalating for a fixture this small, and archive/zip
// opens the result, which is what makes it usable as a differential oracle.
func partialZIP64(t testing.TB, jpg []byte, sl zip64Slots) []byte {
	t.Helper()
	if !sl.uncomp && !sl.comp && !sl.offset {
		t.Fatal("partialZIP64: escalate at least one slot")
	}

	// Reserve a 0x0001 field of exactly the right size; the values go in below,
	// once the builder has decided where everything lands.
	body := 0
	for _, on := range []bool{sl.uncomp, sl.comp, sl.offset} {
		if on {
			body += 8
		}
	}
	extra := make([]byte, 4+body)
	binary.LittleEndian.PutUint16(extra, 0x0001)
	binary.LittleEndian.PutUint16(extra[2:], uint16(body))

	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg, Method: testutil.MethodDeflate, Extra: extra},
		{Name: "002.jpg", Data: jpg, Method: testutil.MethodStore, Extra: extra},
	}})

	// Central-directory field offsets (APPNOTE §4.3.12).
	const (
		compSizeAt   = 20
		uncompSizeAt = 24
		nameLenAt    = 28
		extraLenAt   = 30
		commentLenAt = 32
		localOffAt   = 42
	)
	rec := centralDirOffset(t, data)
	for i := 0; i < 2; i++ {
		nameLen := int(binary.LittleEndian.Uint16(data[rec+nameLenAt:]))
		extraLen := int(binary.LittleEndian.Uint16(data[rec+extraLenAt:]))
		commentLen := int(binary.LittleEndian.Uint16(data[rec+commentLenAt:]))
		if extraLen != len(extra) {
			t.Fatalf("record %d: extra field is %d bytes, want the %d reserved", i, extraLen, len(extra))
		}
		field := rec + centralHeaderLenTest + nameLen + 4 // past the 0x0001 tag+size
		// APPNOTE order: uncompressed, compressed, local-header offset.
		for _, slot := range []struct {
			on bool
			at int
		}{
			{sl.uncomp, rec + uncompSizeAt},
			{sl.comp, rec + compSizeAt},
			{sl.offset, rec + localOffAt},
		} {
			if !slot.on {
				continue
			}
			binary.LittleEndian.PutUint64(data[field:], uint64(binary.LittleEndian.Uint32(data[slot.at:])))
			binary.LittleEndian.PutUint32(data[slot.at:], 0xffffffff)
			field += 8
		}
		rec += centralHeaderLenTest + nameLen + extraLen + commentLen
	}
	return data
}

// corruptFirstCentralRecord flips one byte of the first central-directory
// record's signature, which is what a bit-rotted archive looks like.
func corruptFirstCentralRecord(t testing.TB, jpg []byte) []byte {
	t.Helper()
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg},
		{Name: "002.jpg", Data: jpg},
	}})
	off := centralDirOffset(t, data)
	out := append([]byte(nil), data...)
	out[off+2] ^= 0xff
	return out
}

// Record sizes, restated here so the tests do not depend on unexported
// constants of the package under test.
const (
	eocdLenTest          = 22
	centralHeaderLenTest = 46
)

// centralDirOffset reads the central-directory offset straight out of the EOCD
// of a well-formed fixture.
func centralDirOffset(t testing.TB, data []byte) int {
	t.Helper()
	return int(binary.LittleEndian.Uint32(data[eocdOffset(t, data)+16:]))
}

// eocdOffset returns the position of the end-of-central-directory signature.
func eocdOffset(t testing.TB, data []byte) int {
	t.Helper()
	for i := len(data) - eocdLenTest; i >= 0; i-- {
		if binary.LittleEndian.Uint32(data[i:]) == 0x06054b50 {
			return i
		}
	}
	t.Fatal("fixture has no EOCD")
	return 0
}

// ---------------------------------------------------------------------------
// Instrumentation
// ---------------------------------------------------------------------------

// countingReaderAt records every read the reader makes, which is how
// FR-IDX-002 and NFR-PRF-006 are asserted rather than asserted-about.
type countingReaderAt struct {
	r     io.ReaderAt
	calls atomic.Int64
	bytes atomic.Int64
	// spans records [off, off+n) of every call, for the "did it touch a
	// payload?" assertion.
	spans []span
}

type span struct{ off, end int64 }

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.calls.Add(1)
	c.bytes.Add(int64(n))
	c.spans = append(c.spans, span{off, off + int64(n)})
	return n, err
}

func newCounter(data []byte) *countingReaderAt {
	return &countingReaderAt{r: bytes.NewReader(data)}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestReadCentralDirectory_corpus_namesAndVerdicts(t *testing.T) {
	t.Parallel()
	for _, fx := range corpus(t) {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(fx.data), int64(len(fx.data)))
			if fx.wantErr {
				if err == nil {
					t.Fatalf("want an error, got a clean index with %d entries", len(ix.Entries))
				}
				if got := archive.StatusOf(err); got != archive.StatusError {
					t.Errorf("status = %q, want %q (err = %v)", got, archive.StatusError, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ReadCentralDirectory: %v", err)
			}
			if fx.wantNames == nil {
				return
			}
			got := make([]string, len(ix.Entries))
			for i, e := range ix.Entries {
				got[i] = e.Name
			}
			if len(got) != len(fx.wantNames) {
				t.Fatalf("entry names = %q, want %q", got, fx.wantNames)
			}
			for i := range got {
				if got[i] != fx.wantNames[i] {
					t.Errorf("entry %d name = %q, want %q", i, got[i], fx.wantNames[i])
				}
			}
		})
	}
}

// impl-plan WP-04 acceptance 1: the two-step tail scan averages at most two
// ReadAt calls and 16 KB per archive over the fixture corpus, and never reads
// an entry payload.
func TestReadCentralDirectory_corpus_readAccounting(t *testing.T) {
	t.Parallel()
	var totalCalls, totalBytes, archives int64

	for _, fx := range corpus(t) {
		if fx.wantErr || len(fx.data) == 0 {
			continue
		}
		c := newCounter(fx.data)
		if _, err := zipidx.ReadCentralDirectory(t.Context(), c, int64(len(fx.data))); err != nil {
			t.Fatalf("%s: %v", fx.name, err)
		}
		archives++
		totalCalls += c.calls.Load()
		totalBytes += c.bytes.Load()

		if c.calls.Load() > 3 {
			t.Errorf("%s: %d ReadAt calls, want at most 3 for any single archive", fx.name, c.calls.Load())
		}
	}

	if archives == 0 {
		t.Fatal("no well-formed fixtures in the corpus")
	}
	avgCalls := float64(totalCalls) / float64(archives)
	avgBytes := float64(totalBytes) / float64(archives)
	t.Logf("corpus: %d archives, %.2f ReadAt/archive, %.0f bytes/archive", archives, avgCalls, avgBytes)
	if avgCalls > 2.0 {
		t.Errorf("average ReadAt calls = %.2f, want <= 2.0", avgCalls)
	}
	if avgBytes > 16*1024 {
		t.Errorf("average bytes read = %.0f, want <= 16384", avgBytes)
	}
}

// FR-IDX-002 and NFR-PRF-006, stated in the only way that means anything:
// on an archive big enough for "the tail" and "the payloads" to be different
// places, indexing must touch the tail and nothing else.
//
// The corpus fixtures are a few kilobytes, so their 1 KiB tail read
// unavoidably overlaps the last entry's bytes; that is an artefact of the
// fixture size, not a payload read. This test removes the ambiguity.
func TestReadCentralDirectory_largeArchive_readsOnlyTheTail(t *testing.T) {
	t.Parallel()
	const pages = 300
	data, _ := bigArchive(t, pages, 8192, testutil.MethodStore)

	c := newCounter(data)
	ix, err := zipidx.ReadCentralDirectory(t.Context(), c, int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	if len(ix.Entries) != pages {
		t.Fatalf("entries = %d, want %d", len(ix.Entries), pages)
	}

	// The first entry's payload sits at the very front of a ~2.5 MB archive.
	// Nothing the indexer reads may come near it.
	firstPayloadEnd := ix.Entries[0].LocalHdrOff + 30 + ix.Entries[0].CompSize
	for _, s := range c.spans {
		if s.off < firstPayloadEnd {
			t.Errorf("indexing read [%d,%d), which is inside the first entry's payload (ends at %d)",
				s.off, s.end, firstPayloadEnd)
		}
	}

	read, total := c.bytes.Load(), int64(len(data))
	t.Logf("archive %d bytes, indexing read %d bytes (%.3f%%) in %d ReadAt calls",
		total, read, 100*float64(read)/float64(total), c.calls.Load())
	if pct := 100 * float64(read) / float64(total); pct > 2.0 {
		t.Errorf("indexing read %.3f%% of the archive, want under 2%% (arch §4.3 measured 0.365%%)", pct)
	}
	if got := c.calls.Load(); got > 2 {
		t.Errorf("ReadAt calls = %d, want at most 2", got)
	}
}

// A 40 KiB comment must not stop the end record being found, and must still
// cost no more than the documented two tail reads plus the directory.
func TestReadCentralDirectory_bigComment_usesSecondTailScan(t *testing.T) {
	t.Parallel()
	data := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries:     []testutil.Entry{{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8)}},
		CommentSize: testutil.CommentSize40KiB,
	})
	c := newCounter(data)
	ix, err := zipidx.ReadCentralDirectory(t.Context(), c, int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	if len(ix.Comment) != testutil.CommentSize40KiB {
		t.Errorf("comment length = %d, want %d", len(ix.Comment), testutil.CommentSize40KiB)
	}
	if got := c.calls.Load(); got != 2 {
		t.Errorf("ReadAt calls = %d, want exactly 2 (1 KiB tail miss, then the 65 557 B tail)", got)
	}
}

func TestReadCentralDirectory_zip64_resolvesEverySlot(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 24, 32)
	specs := map[string]testutil.ZIP64Spec{
		"sizes_and_offset":   {},
		"with_disk_field":    {IncludeDiskField: true},
		"local_header_extra": {LocalHeaders: true},
		"both":               {IncludeDiskField: true, LocalHeaders: true},
	}
	for name, z64 := range specs {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := testutil.BuildZIP64(t, testutil.ZIPSpec{Entries: []testutil.Entry{
				{Name: "001.jpg", Data: jpg, Method: testutil.MethodStore},
				{Name: "002.jpg", Data: jpg, Method: testutil.MethodDeflate},
			}}, z64)

			ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
			if err != nil {
				t.Fatalf("ReadCentralDirectory: %v", err)
			}
			if !ix.ZIP64 {
				t.Error("Index.ZIP64 = false, want true")
			}
			if len(ix.Entries) != 2 {
				t.Fatalf("entries = %d, want 2", len(ix.Entries))
			}
			// Every 32-bit slot in this fixture holds the sentinel, so an
			// unresolved slot shows up as 0xffffffff rather than a real value.
			for _, e := range ix.Entries {
				if e.Size != int64(len(jpg)) {
					t.Errorf("%s: uncompressed size = %d, want %d", e.Name, e.Size, len(jpg))
				}
				if e.CompSize <= 0 || e.CompSize == int64(^uint32(0)) {
					t.Errorf("%s: compressed size = %d, want a resolved value", e.Name, e.CompSize)
				}
				if e.LocalHdrOff < 0 || e.LocalHdrOff >= int64(len(data)) {
					t.Errorf("%s: local header offset = %d, outside the %d-byte archive",
						e.Name, e.LocalHdrOff, len(data))
				}
			}
			// And the offsets must actually point at local headers.
			for _, e := range ix.Entries {
				if got := binary.LittleEndian.Uint32(data[e.LocalHdrOff:]); got != 0x04034b50 {
					t.Errorf("%s: bytes at offset %d are %#08x, not a local file header", e.Name, e.LocalHdrOff, got)
				}
			}
		})
	}
}

// The "the end record gave us a wrong base" recovery, which mirrors
// archive/zip's (reader.go, "We've seen files in which the directory end data
// gives us an incorrect baseOffset").
//
// Junk between the directory and the end record makes
// base = endOffset - dirSize - dirOffset non-zero although the recorded offsets
// are already absolute. archive/zip probes the uncorrected offset, finds a real
// record there and opens the archive; if we do not, the book is stored with
// status='error' and becomes unreadable for a container archive/zip reads
// fine — the verdict divergence decision E-2 and impl-plan §0.1 C-6 forbid.
func TestReadCentralDirectory_incorrectBaseOffset_believesTheRecordedOffset(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 16, 24)
	for _, junk := range []int{4, 64, 1024} {
		t.Run(fmt.Sprintf("junk=%d", junk), func(t *testing.T) {
			t.Parallel()
			data := junkBeforeEOCD(t, jpg, junk)
			r := bytes.NewReader(data)

			compareWithOracle(t, fmt.Sprintf("junk_before_eocd/%d", junk), r, int64(len(data)))

			ix, err := zipidx.ReadCentralDirectory(t.Context(), r, int64(len(data)))
			if err != nil {
				t.Fatalf("archive/zip opens this container; we must too: %v", err)
			}
			if len(ix.Entries) != 2 {
				t.Fatalf("entries = %d, want 2", len(ix.Entries))
			}
			if ix.BaseOffset != 0 {
				t.Errorf("BaseOffset = %d, want 0 (the recorded offsets are absolute)", ix.BaseOffset)
			}
			for _, e := range ix.Entries {
				if got := binary.LittleEndian.Uint32(data[e.LocalHdrOff:]); got != 0x04034b50 {
					t.Errorf("entry %q: bytes at offset %d are %#08x, not a local file header",
						e.Name, e.LocalHdrOff, got)
				}
			}
		})
	}
}

// The other half of the same fix. Once the recovery above fires, the directory
// read starts at the *raw* recorded offset, which on a large container sits far
// from EOF; reading from there to the end of the file would buffer the whole
// tail of the archive and breach NFR-PRF-006.
func TestReadCentralDirectory_incorrectBaseOffset_doesNotReadToEOF(t *testing.T) {
	t.Parallel()
	const junk = 2 << 20 // the gap the wrong base makes us jump over
	data := junkBeforeEOCD(t, testutil.TinyJPEG(t, 8, 8), junk)

	c := newCounter(data)
	ix, err := zipidx.ReadCentralDirectory(t.Context(), c, int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	if len(ix.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(ix.Entries))
	}

	read, total := c.bytes.Load(), int64(len(data))
	t.Logf("archive %d bytes, indexing read %d bytes (%.3f%%) in %d ReadAt calls",
		total, read, 100*float64(read)/float64(total), c.calls.Load())
	// The directory is ~100 bytes. Anything near the 2 MiB gap means the read
	// span ran to EOF.
	if read > 64<<10 {
		t.Errorf("indexing read %d bytes of a %d-byte archive, want under 64 KiB", read, total)
	}
}

// WP-04 acceptance 2 / FR-IDX-009: the 0x0001 extra's members exist *only* for
// the 32-bit slots that held 0xffffffff, in the APPNOTE §4.5.3 order.
//
// testutil.BuildZIP64 always escalates all three slots at once, which a parser
// that reads them positionally passes just as happily as a correct one. These
// fixtures escalate one group at a time — "offset only" is what a real >4 GB
// archive writes for an entry whose own sizes still fit in 32 bits — and a
// positional parser reads the offset out of the uncompressed-size member.
func TestReadCentralDirectory_zip64_partialEscalation(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 20, 28)
	for name, sl := range map[string]zip64Slots{
		"offset_only":            {offset: true},
		"sizes_only":             {uncomp: true, comp: true},
		"uncompressed_size_only": {uncomp: true},
		"sizes_and_offset":       {uncomp: true, comp: true, offset: true},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data := partialZIP64(t, jpg, sl)
			r := bytes.NewReader(data)

			// Sizes, offsets and the resulting data offset, against the oracle.
			compareWithOracle(t, "zip64_partial/"+name, r, int64(len(data)))

			ix, err := zipidx.ReadCentralDirectory(t.Context(), r, int64(len(data)))
			if err != nil {
				t.Fatalf("ReadCentralDirectory: %v", err)
			}
			if !ix.ZIP64 {
				t.Error("Index.ZIP64 = false, want true (the records carry a 0x0001 extra)")
			}
			if len(ix.Entries) != 2 {
				t.Fatalf("entries = %d, want 2", len(ix.Entries))
			}
			for _, e := range ix.Entries {
				// A positional parser lands the local-header offset here.
				if e.Size != int64(len(jpg)) {
					t.Errorf("%s: uncompressed size = %d, want %d — a member was consumed for a slot that did not hold the sentinel",
						e.Name, e.Size, len(jpg))
				}
				if e.CompSize <= 0 || e.CompSize > int64(len(data)) {
					t.Errorf("%s: compressed size = %d, outside the %d-byte archive", e.Name, e.CompSize, len(data))
				}
				if e.LocalHdrOff < 0 || e.LocalHdrOff >= int64(len(data)) {
					t.Fatalf("%s: local header offset = %d, outside the %d-byte archive",
						e.Name, e.LocalHdrOff, len(data))
				}
				if got := binary.LittleEndian.Uint32(data[e.LocalHdrOff:]); got != 0x04034b50 {
					t.Errorf("%s: bytes at offset %d are %#08x, not a local file header",
						e.Name, e.LocalHdrOff, got)
				}
			}
		})
	}
}

// arch §4.3 step 1's comment-length consistency check, which is what stops a
// stray 0x06054b50 inside compressed data from being taken for the end record.
// Here the real record's comment is cut short, so the field is inconsistent
// with the bytes that follow: Info-ZIP and archive/zip both abandon the scan
// rather than parse a directory out of the offsets the record holds, and so
// must we.
func TestReadCentralDirectory_eocdCommentPastEOF_isNotAnEndRecord(t *testing.T) {
	t.Parallel()
	data := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries:      []testutil.Entry{{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8)}},
		CommentSize:  1000,
		TruncateTail: 100,
	})
	// The record itself is intact — only its comment is short. Without the
	// consistency check this parses as a perfectly good end record.
	at := eocdOffset(t, data)
	if got := int(binary.LittleEndian.Uint16(data[at+20:])); got != 1000 {
		t.Fatalf("fixture declares a %d-byte comment, want 1000", got)
	}
	if rest := len(data) - at - eocdLenTest; rest >= 1000 {
		t.Fatalf("fixture has %d bytes after the record, want fewer than the 1000 declared", rest)
	}

	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if !errors.Is(err, zipidx.ErrNoEOCD) {
		t.Fatalf("err = %v, want zipidx.ErrNoEOCD", err)
	}
	if ix != nil {
		t.Errorf("got an index of %d entries, want none", len(ix.Entries))
	}
}

func TestReadCentralDirectory_encrypted_isIndexedNotDecoded(t *testing.T) {
	t.Parallel()
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8), Flags: testutil.FlagEncrypted},
	}})
	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("indexing an encrypted archive must succeed: %v", err)
	}
	if !ix.Encrypted() {
		t.Fatal("Index.Encrypted() = false, want true")
	}
	if !ix.Entries[0].Encrypted {
		t.Error("Entry.Encrypted = false, want true")
	}
	// FR-IDX-010: flagged, never decoded.
	if _, err := zipidx.OpenEntry(t.Context(), bytes.NewReader(data), ix.Entries[0].Ref()); !errors.Is(err, zipidx.ErrEncrypted) {
		t.Errorf("OpenEntry err = %v, want zipidx.ErrEncrypted", err)
	}
	if got := archive.StatusOf(zipidx.ErrEncrypted); got != archive.StatusEncrypted {
		t.Errorf("StatusOf(ErrEncrypted) = %q, want %q", got, archive.StatusEncrypted)
	}
}

func TestReadCentralDirectory_partialDirectory_keepsWhatItParsed(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 8, 8)
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg},
		{Name: "002.jpg", Data: jpg},
		{Name: "003.jpg", Data: jpg},
	}})
	// Corrupt the *third* record so the first two still parse. arch §4.3 step
	// 6 requires them to come back with the error, not be thrown away.
	cd := centralDirOffset(t, data)
	third := cd
	for i := 0; i < 2; i++ {
		nameLen := int(binary.LittleEndian.Uint16(data[third+28:]))
		extraLen := int(binary.LittleEndian.Uint16(data[third+30:]))
		commentLen := int(binary.LittleEndian.Uint16(data[third+32:]))
		third += 46 + nameLen + extraLen + commentLen
	}
	broken := append([]byte(nil), data...)
	broken[third+3] ^= 0xff

	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(broken), int64(len(broken)))
	if err == nil {
		t.Fatal("want an error for a directory that goes bad at record 3")
	}
	if ix == nil {
		t.Fatal("want the partial index alongside the error, got nil")
	}
	if len(ix.Entries) != 2 {
		t.Errorf("partial entries = %d, want 2", len(ix.Entries))
	}
	if !errors.Is(err, archive.ErrCorrupt) {
		t.Errorf("err = %v, want it to wrap archive.ErrCorrupt", err)
	}
	if !errors.Is(err, zipidx.ErrBadCentralHeader) && !errors.Is(err, zipidx.ErrTruncatedCD) {
		t.Errorf("err = %v, want a central-directory sentinel", err)
	}
}

func TestReadCentralDirectory_noEOCD_isErrNoEOCD(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"short", []byte("PK")},
		{"garbage", bytes.Repeat([]byte{0x00}, 4096)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(tc.data), int64(len(tc.data)))
			if !errors.Is(err, zipidx.ErrNoEOCD) {
				t.Errorf("err = %v, want zipidx.ErrNoEOCD", err)
			}
			if got := archive.StatusOf(err); got != archive.StatusError {
				t.Errorf("status = %q, want %q", got, archive.StatusError)
			}
		})
	}
}

func TestReadCentralDirectory_cancelledContext_returnsCtxErr(t *testing.T) {
	t.Parallel()
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: testutil.TinyJPEG(t, 8, 8)},
	}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := zipidx.ReadCentralDirectory(ctx, bytes.NewReader(data), int64(len(data))); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// The frozen malformed archives under testdata/ are byte-exact and independent
// of testutil, so a change in the fixture generator cannot silently change what
// "corrupt" means to this reader.
func TestReadCentralDirectory_frozenTestdata_verdicts(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		wantErr error
		wantMin int // entries that must survive
		wantOK  bool
	}{
		"bad-eocd.zip":     {wantErr: zipidx.ErrNoEOCD},
		"truncated-cd.zip": {wantErr: archive.ErrCorrupt},
		"big-comment.zip":  {wantOK: true, wantMin: 3},
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatalf("reading the frozen fixture: %v", err)
			}
			ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
			if want.wantOK {
				if err != nil {
					t.Fatalf("want a clean read, got %v", err)
				}
				if len(ix.Entries) < want.wantMin {
					t.Errorf("entries = %d, want at least %d", len(ix.Entries), want.wantMin)
				}
				return
			}
			if !errors.Is(err, want.wantErr) {
				t.Errorf("err = %v, want it to wrap %v", err, want.wantErr)
			}
		})
	}
}

// Found by FuzzReadCentralDirectory: a corrupt end record can make the base
// correction negative, which pushed an entry's local-header offset below zero.
// Such an entry can only ever produce a 500 once it is in the index, so it is
// counted (keeping the record tally, and with it the error verdict, intact)
// but never listed.
func TestReadCentralDirectory_entryOutsideTheArchive_isNotListed(t *testing.T) {
	t.Parallel()
	jpg := testutil.TinyJPEG(t, 8, 8)
	data := testutil.BuildZIP(t, testutil.ZIPSpec{Entries: []testutil.Entry{
		{Name: "001.jpg", Data: jpg},
		{Name: "002.jpg", Data: jpg},
	}})

	for _, tc := range []struct {
		name  string
		patch func([]byte, int)
	}{
		{
			name: "local header offset past EOF",
			patch: func(b []byte, cd int) {
				binary.LittleEndian.PutUint32(b[cd+42:], 0x7fffff00)
			},
		},
		{
			name: "compressed size larger than the archive",
			patch: func(b []byte, cd int) {
				binary.LittleEndian.PutUint32(b[cd+20:], 0x7ffffff0)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			broken := append([]byte(nil), data...)
			tc.patch(broken, centralDirOffset(t, data))

			ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(broken), int64(len(broken)))
			if err == nil {
				t.Fatal("want an error for a record pointing outside the archive")
			}
			if !errors.Is(err, archive.ErrCorrupt) {
				t.Errorf("err = %v, want it to wrap archive.ErrCorrupt", err)
			}
			if len(ix.Entries) != 1 {
				t.Fatalf("entries = %d, want 1 (the good one)", len(ix.Entries))
			}
			if ix.Entries[0].Name != "002.jpg" {
				t.Errorf("surviving entry = %q, want %q", ix.Entries[0].Name, "002.jpg")
			}
			for _, e := range ix.Entries {
				if e.LocalHdrOff < 0 || e.LocalHdrOff >= int64(len(broken)) {
					t.Errorf("entry %q has an unusable offset %d", e.Name, e.LocalHdrOff)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Archive-level name encoding (FR-IDX-008, extended for Shift_JIS)
// ---------------------------------------------------------------------------

// The Shift_JIS bytes of `天上天下 20.zip`, whose 189 names are the reason the
// decision cannot be made one entry at a time: CP949 reads 160 of them without
// substituting anything, and every one of those readings is wrong.
var (
	sjisAmbiguous = []byte("tenjou tenge v20/\x93\x56\x93\x56-20-001.jpg")
	sjisKana      = []byte("tenjou tenge v20/\x93\x56\x93\x56-20-008 \x82\xcc\x83\x52\x83\x73\x81\x5b2.jpg")
)

// TestReadCentralDirectory_shiftJISArchive_isDecidedForTheWholeArchive is the
// case that motivated resolveArchiveNames. The first name is readable as CP949
// and must still come out Japanese, because of what the second name proves
// about the archive it lives in.
func TestReadCentralDirectory_shiftJISArchive_isDecidedForTheWholeArchive(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	data := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{RawName: sjisAmbiguous, Data: page, Method: testutil.MethodDeflate},
			{RawName: sjisKana, Data: page, Method: testutil.MethodDeflate},
			// A flagged UTF-8 name in the same container must be left alone:
			// the archive-level decision covers the legacy names only.
			{Name: "표지.jpg", Data: page, Method: testutil.MethodStore, Flags: testutil.FlagUTF8},
			// So must a flagless one the UTF-8 probe already claimed.
			{Name: "001.jpg", Data: page, Method: testutil.MethodStore},
		},
	})

	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	want := []struct{ name, enc string }{
		{"tenjou tenge v20/天天-20-001.jpg", kenc.EncCP932},
		{"tenjou tenge v20/天天-20-008 のコピー2.jpg", kenc.EncCP932},
		{"표지.jpg", kenc.EncUTF8},
		{"001.jpg", kenc.EncUTF8},
	}
	if len(ix.Entries) != len(want) {
		t.Fatalf("entries = %d, want %d", len(ix.Entries), len(want))
	}
	for i, e := range ix.Entries {
		if e.Name != want[i].name || e.NameEncoding != want[i].enc {
			t.Errorf("entry %d = (%q, %q), want (%q, %q)", i, e.Name, e.NameEncoding, want[i].name, want[i].enc)
		}
	}
}

// TestReadCentralDirectory_koreanArchiveWithOneBadName_staysCP949 is the
// regression that matters most. One unreadable name is what triggers the
// fallback, and 1,871 archives in the collection are Korean against 4 that are
// Japanese — so a fallback that fires on a Korean archive would rewrite
// thousands of correct names into halfwidth-katakana nonsense.
func TestReadCentralDirectory_koreanArchiveWithOneBadName_staysCP949(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	data := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{RawName: testutil.CP949(t, "시티 헌터 완전판 08권/CS02-026.JPG"), Data: page, Method: testutil.MethodDeflate},
			{RawName: testutil.CP949(t, "시티 헌터 완전판 08권/CS02-027.JPG"), Data: page, Method: testutil.MethodDeflate},
			// The trigger: bytes no encoding reads.
			{RawName: []byte("\xff\xfe\x80.jpg"), Data: page, Method: testutil.MethodStore},
		},
	})

	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("ReadCentralDirectory: %v", err)
	}
	want := []struct{ name, enc string }{
		{"시티 헌터 완전판 08권/CS02-026.JPG", kenc.EncCP949},
		{"시티 헌터 완전판 08권/CS02-027.JPG", kenc.EncCP949},
		{"�.jpg", kenc.EncUnknown},
	}
	if len(ix.Entries) != len(want) {
		t.Fatalf("entries = %d, want %d", len(ix.Entries), len(want))
	}
	for i, e := range ix.Entries {
		if e.Name != want[i].name || e.NameEncoding != want[i].enc {
			t.Errorf("entry %d = (%q, %q), want (%q, %q)", i, e.Name, e.NameEncoding, want[i].name, want[i].enc)
		}
	}
}

// TestReadCentralDirectory_shiftJISNames_surviveAPartialDirectory: the decision
// runs on the error path too. A directory that goes bad partway still lists the
// entries that parsed, and those names must be as right as they would have been
// in a clean archive.
func TestReadCentralDirectory_shiftJISNames_surviveAPartialDirectory(t *testing.T) {
	t.Parallel()

	page := testutil.TinyJPEG(t, 8, 8)
	third := []byte("tenjou tenge v20/\x93\x56\x93\x56-20-003.jpg")
	data := testutil.BuildZIP(t, testutil.ZIPSpec{
		Entries: []testutil.Entry{
			{RawName: sjisKana, Data: page, Method: testutil.MethodDeflate},
			{RawName: sjisAmbiguous, Data: page, Method: testutil.MethodDeflate},
			{RawName: third, Data: page, Method: testutil.MethodDeflate},
		},
	})

	// Break the *third central-directory record* — the last occurrence of the
	// name, the first being its local header — so the parse stops there with
	// the end record still claiming three entries. Corrupting the local header
	// instead would leave the directory perfectly readable and this test would
	// silently assert nothing.
	broken := append([]byte(nil), data...)
	i := bytes.LastIndex(broken, third)
	if i < 0 {
		t.Fatal("fixture: third name not found in the central directory")
	}
	rec := i - 46 // centralHeaderLen, the fixed part preceding the name
	if rec < 0 || binary.LittleEndian.Uint32(broken[rec:]) != 0x02014b50 {
		t.Fatalf("fixture: no central-directory record at %d", rec)
	}
	broken[rec+3]++ // wreck the signature

	ix, err := zipidx.ReadCentralDirectory(t.Context(), bytes.NewReader(broken), int64(len(broken)))
	if err == nil {
		t.Fatal("want the partial-directory error, got a clean read")
	}
	if ix == nil || len(ix.Entries) != 2 {
		t.Fatalf("entries = %v, want the 2 that parsed", ix)
	}
	want := []string{
		"tenjou tenge v20/天天-20-008 のコピー2.jpg",
		"tenjou tenge v20/天天-20-001.jpg",
	}
	for i, e := range ix.Entries {
		if e.Name != want[i] || e.NameEncoding != kenc.EncCP932 {
			t.Errorf("entry %d = (%q, %q), want (%q, %q)", i, e.Name, e.NameEncoding, want[i], kenc.EncCP932)
		}
	}
}
