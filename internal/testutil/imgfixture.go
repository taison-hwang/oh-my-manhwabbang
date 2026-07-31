package testutil

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

// This file supplies one tiny image per format of FR-IDX-011, plus the two
// formats arch §5.5 needs for the graceful-degradation path (TIFF, animated
// WebP). Every fixture is under 1 KB, so the whole unit suite stays hermetic
// and fast without a single committed binary blob.
//
// JPEG, PNG, GIF, BMP and TIFF are generated at call time from the pinned
// encoders. WebP and AVIF have no Go encoder in the frozen dependency set
// (x/image/webp is decode-only; gen2brain/avif's encoder costs a ~1 s wazero
// init), so those two are base64 constants produced once — see the comment on
// each — and verified by TestTiny_everyFormat_decodes.

// gradient builds a deterministic w×h image with enough variation that a
// downscaler has something to do and a JPEG does not compress to nothing.
func gradient(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8((x * 255) / max(w-1, 1)),
				G: uint8((y * 255) / max(h-1, 1)),
				B: uint8((x + y) * 8 % 256),
				A: 255,
			})
		}
	}
	return img
}

// TinyJPEG returns a w×h baseline JPEG. At 16×16 it is roughly 500 bytes.
func TinyJPEG(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, gradient(w, h), &jpeg.Options{Quality: 60}); err != nil {
		t.Fatalf("testutil: encoding %dx%d jpeg: %v", w, h, err)
	}
	return buf.Bytes()
}

// TinyPNG returns a w×h 8-bit RGBA PNG.
func TinyPNG(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := png.Encoder{CompressionLevel: png.BestCompression}
	if err := enc.Encode(&buf, gradient(w, h)); err != nil {
		t.Fatalf("testutil: encoding %dx%d png: %v", w, h, err)
	}
	return buf.Bytes()
}

// TinyGIF returns a w×h single-frame GIF.
//
// NumColors is pinned to 16: the default 256-entry Plan 9 palette alone is a
// 768-byte global colour table, which would push even a 2×3 GIF past the 1 KB
// fixture budget.
func TinyGIF(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := gif.Encode(&buf, gradient(w, h), &gif.Options{NumColors: 16}); err != nil {
		t.Fatalf("testutil: encoding %dx%d gif: %v", w, h, err)
	}
	return buf.Bytes()
}

// TinyBMP returns a w×h BMP. Keep w and h small: BMP is uncompressed.
func TinyBMP(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := bmp.Encode(&buf, gradient(w, h)); err != nil {
		t.Fatalf("testutil: encoding %dx%d bmp: %v", w, h, err)
	}
	return buf.Bytes()
}

// TinyTIFF returns a w×h deflate-compressed TIFF. TIFF is not in FR-IDX-011's
// extension list but arch §5.5 requires the thumbnailer to decode it.
func TinyTIFF(t testing.TB, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := tiff.Encode(&buf, gradient(w, h), &tiff.Options{Compression: tiff.Deflate}); err != nil {
		t.Fatalf("testutil: encoding %dx%d tiff: %v", w, h, err)
	}
	return buf.Bytes()
}

// tinyWebPStill is a 38-byte lossless 2×3 VP8L WebP.
//
// Produced once with libwebp 1.3.2 via Pillow:
//
//	Image.new('RGB', (2,3), (200,40,40)).save('x.webp', lossless=True, method=6)
//
// x/image/webp decodes it (verified); it is the "WebP (still) → thumbnail yes"
// row of arch §5.5.
const tinyWebPStill = "UklGRh4AAABXRUJQVlA4TBEAAAAvAYAAAAdQlCIXpf+BiOh/AAA="

// tinyWebPAnimated is a 144-byte 2-frame animated 2×3 WebP (VP8X + ANIM +
// 2×ANMF), produced by the same Pillow call with save_all=True.
//
// This is the graceful-degradation trigger of arch §5.5: x/image/webp reports
// `webp: invalid format` for it (verified), which WP-07 turns into
// ErrUndecodable{reason:"animated_webp"} and WP-12 into 422 thumb_unavailable.
// Note that image.DecodeConfig still succeeds on it — the VP8X chunk carries
// the canvas size — so a dimension pass must not be used as a decodability
// probe.
const tinyWebPAnimated = "UklGRogAAABXRUJQVlA4WAoAAAACAAAAAQAAAgAAQU5JTQYAAAAAAAAAAABBTk1GKgAAAAAAAAAAAAEAAAIAAGQAAAJWUDhMEQAAAC8BgAAAB1CUIhel/4GI6H8AAEFOTUYqAAAAAAAAAAAAAQAAAgAAZAAAAFZQOEwRAAAALwGAAAAHUJSiFLn/gYjofwAA"

// tinyAVIF is a 300-byte AV1-still 2×3 AVIF, produced once with
// ImageMagick 7 / libheif 1.17.6:
//
//	convert -size 2x3 xc:'#c82828' avif:x.avif
//
// gen2brain/avif decodes it (verified). Decoding costs a lazy wazero init of
// roughly a second, so tests that only need "an .avif file exists" should use
// these bytes without decoding them.
const tinyAVIF = "AAAAHGZ0eXBhdmlmAAAAAGF2aWZtaWYxbWlhZgAAAOptZXRhAAAAAAAAACFoZGxyAAAAAAAAAABwaWN0AAAAAAAAAAAAAAAAAAAAAA5waXRtAAAAAAABAAAAImlsb2MAAAAAREAAAQABAAAAAAEOAAEAAAAAAAAAHgAAACNpaW5mAAAAAAABAAAAFWluZmUCAAAAAAEAAGF2MDEAAAAAamlwcnAAAABLaXBjbwAAABNjb2xybmNseAABAA0ABoAAAAAMYXYxQ4FAbAAAAAAUaXNwZQAAAAAAAAACAAAAAwAAABBwaXhpAAAAAAMMDAwAAAAXaXBtYQAAAAAAAAABAAEEAYIDBAAAACZtZGF0EgAKCFgAcxoCGg3CMhAYAA4444QAALATX0Lm5GU8"

// TinyWebP returns a 2×3 still WebP that x/image/webp decodes.
func TinyWebP(t testing.TB) []byte { return decodeB64(t, "webp", tinyWebPStill) }

// TinyAnimatedWebP returns a 2×3 two-frame animated WebP that x/image/webp
// refuses with `webp: invalid format`.
func TinyAnimatedWebP(t testing.TB) []byte { return decodeB64(t, "animated webp", tinyWebPAnimated) }

// TinyAVIF returns a 2×3 AVIF still.
func TinyAVIF(t testing.TB) []byte { return decodeB64(t, "avif", tinyAVIF) }

func decodeB64(t testing.TB, what, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("testutil: the embedded %s fixture is corrupt: %v", what, err)
	}
	return b
}
