//go:build noavif

package thumbs

// avifEnabled is false in a `-tags noavif` build: the gen2brain/avif package
// and its embedded wasm module are not linked in at all, which is worth roughly
// a megabyte of binary and removes the wazero runtime from the process.
//
// The behavioural contract is unchanged — an AVIF page still streams its
// original bytes from /pages/{n}, and every target browser (NFR-CMP-001)
// decodes it natively. Only the server-side thumbnail degrades, to
// `422 thumb_unavailable` with `detail.reason: "avif_disabled"`.
const avifEnabled = false

// AVIFSupported exports the compile-time half of the gate.
//
// Ruling E-21 makes `noavif` the DEFAULT build, so this is the shipped answer,
// and `thumbnails.avif_enabled` defaults to true — which means the config key
// alone would have `/api/health` and `/api/settings` advertise a decoder the
// binary does not contain while Service.decode returns 422 avif_disabled for
// every .avif. That is the same lie httpapi.Server.pdfEnabled exists to prevent
// for PDF, so the two endpoints AND this in exactly as they AND in
// pdfium.Supported().
func AVIFSupported() bool { return avifEnabled }
