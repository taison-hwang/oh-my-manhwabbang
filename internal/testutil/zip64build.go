package testutil

import "testing"

// ZIP64Spec asks for a specific flavour of ZIP64 escalation on an archive that
// is far too small to need one. FR-IDX-009 is 필수 but no ZIP64 archive exists
// in the target collection (data-survey D-3), so this hand-built fixture is the
// only thing WP-04's ZIP64 path is ever tested against.
type ZIP64Spec struct {
	// IncludeDiskField appends the optional 4-byte "disk start number" slot to
	// the 0x0001 extra field. A parser that reads the four slots positionally
	// instead of by remaining length gets this wrong, so both shapes exist.
	IncludeDiskField bool

	// LocalHeaders additionally puts a 0x0001 extra in every local file header
	// with 0xffffffff sizes. Real ZIP64 writers do this; readers that only look
	// at filename_len + extra_len when computing the data offset (as ours does)
	// must be unaffected by it.
	LocalHeaders bool
}

// BuildZIP64 renders spec into an archive that carries a ZIP64 end-of-central-
// directory record, a ZIP64 EOCD locator, and a 0x0001 extra field on every
// central-directory entry — regardless of how small the payload is.
//
// Concretely, the archive it produces has:
//
//   - every 32-bit size and local-header-offset slot in the central directory
//     set to 0xffffffff, with the real values in the 0x0001 extra field in the
//     APPNOTE §4.5.3 order (uncompressed, compressed, local offset[, disk]);
//   - a 56-byte ZIP64 EOCD record followed by a 20-byte locator;
//   - a legacy EOCD whose record count is 0xffff and whose directory size and
//     offset are 0xffffffff, so a reader has to follow the locator.
//
// The result is a valid ZIP64 archive that archive/zip opens, which is what
// makes it usable as a differential-test oracle (impl-plan C-6).
func BuildZIP64(t testing.TB, spec ZIPSpec, z64 ZIP64Spec) []byte {
	t.Helper()
	if len(spec.Entries) == 0 {
		t.Fatal("testutil: BuildZIP64 needs at least one entry")
	}
	return buildZIP(t, spec, zip64Options{
		force:        true,
		includeDisk:  z64.IncludeDiskField,
		localHeaders: z64.LocalHeaders,
	})
}
