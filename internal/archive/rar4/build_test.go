package rar4

import (
	"testing"

	"shelf/internal/testutil"
)

// A thin shim over testutil.BuildRAR4, so the fixtures these tests read are
// produced by the same writer the scanner and e2e fixtures use. Two RAR writers
// could drift, and the one that drifted would be the one this package's tests
// were passing against.
//
// There is deliberately no packed-entry fixture checked in. Producing one means
// running a RAR compressor, which no Go library does, and the only packed RAR
// bytes available are pages of scanned manga that do not belong in a source
// tree. The packed path is covered instead by
// TestIntegration_realCollection_matchesUnpacker, which decodes all 229 packed
// entries in the collection against a whole-archive oracle.
type builder struct {
	t    testing.TB
	spec testutil.RAR4Spec
	// mainSet records that mainHeader() was called, so a fixture that omits it
	// on purpose — a file block with no main header before it — still builds.
	mainSet bool
}

func newBuilder(t testing.TB) *builder {
	return &builder{t: t, spec: testutil.RAR4Spec{OmitEndBlock: true}}
}

func (b *builder) mainHeader(flags uint16) *builder {
	b.spec.MainFlags = flags
	b.mainSet = true
	return b
}

type entryOpt struct {
	flags    uint16
	method   uint16
	rawName  []byte
	data     []byte
	unpSize  int64
	packSize int64
	highPack uint32
	highUnp  uint32
	dir      bool
}

func (b *builder) file(o entryOpt) *builder {
	b.spec.Entries = append(b.spec.Entries, testutil.RAR4Entry{
		Name:     o.rawName,
		Data:     o.data,
		Method:   o.method,
		Flags:    o.flags,
		Dir:      o.dir,
		UnpSize:  o.unpSize,
		PackSize: o.packSize,
		HighPack: o.highPack,
		HighUnp:  o.highUnp,
	})
	return b
}

func (b *builder) endArc() *builder {
	b.spec.OmitEndBlock = false
	return b
}

func (b *builder) raw(p ...byte) *builder {
	b.spec.Trailing = append(b.spec.Trailing, p...)
	return b
}

func (b *builder) bytes() []byte {
	out := testutil.BuildRAR4(b.t, b.spec)
	if b.mainSet {
		return out
	}
	// No main header wanted: drop the 13-byte block the writer always emits,
	// keeping the 7-byte marker. This is the "file block before any main
	// header" shape, which a writer has no reason to offer as an option.
	return append(append([]byte(nil), out[:7]...), out[7+13:]...)
}

// truncate cuts the archive to n bytes, the interrupted-download shape.
func (b *builder) truncate(n int) []byte {
	raw := b.bytes()
	if n > len(raw) {
		n = len(raw)
	}
	return raw[:n]
}

// The flag names these tests use, mapped onto testutil's. rar4's own constants
// are unexported and are what the production code reads; using testutil's here
// means a fixture and the parser cannot silently agree on a wrong bit.
const (
	tSolidMain    = testutil.RARMainSolid
	tVolumeMain   = testutil.RARMainVolume
	tPasswordMain = testutil.RARMainPassword

	tSplitBefore  = testutil.RARFileSplitBefore
	tSplitAfter   = testutil.RARFileSplitAfter
	tFilePassword = testutil.RARFilePassword
	tFileSolid    = testutil.RARFileSolid
	tFileUnicode  = testutil.RARFileUnicode
)
