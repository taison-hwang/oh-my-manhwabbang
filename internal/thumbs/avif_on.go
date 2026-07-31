//go:build !noavif

package thumbs

import (
	// gen2brain/avif registers itself with image.RegisterFormat in its init, so
	// a blank import is all that is needed to make image.Decode handle AVIF.
	//
	// Importing it costs nothing at startup: the wazero runtime is created on
	// the FIRST decode, not at init (D-20/D-25). That is why the import is
	// unconditional here while the DECODE is still gated twice — by
	// `thumbnails.avif_enabled` and by the one-permit semaphore in
	// Service.decode. A single AVIF costs ~1.1 s and ~170 MiB, and the
	// reference collection contains exactly zero of them (data-survey §4), so
	// FR-IDX-011 is satisfied without ever putting AVIF on a critical path.
	_ "github.com/gen2brain/avif"
)

// avifEnabled reports whether this build carries an AVIF decoder at all. It is
// the compile-time half of the gate; Options.AVIFEnabled is the runtime half.
const avifEnabled = true

// AVIFSupported exports the compile-time half so that the API can report the
// capability the binary actually has rather than the one the config asked for.
// It is the AVIF twin of pdfium.Supported(); see the comment in avif_off.go.
func AVIFSupported() bool { return avifEnabled }
