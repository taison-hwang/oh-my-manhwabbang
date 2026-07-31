package testutil_test

import (
	"bytes"
	"image"
	"testing"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/tiff"

	"golang.org/x/image/webp"

	"shelf/internal/testutil"
)

func TestTiny_everyFormat_decodesAndStaysUnderOneKiB(t *testing.T) {
	t.Parallel()

	const maxBytes = 1024

	tests := []struct {
		name   string
		bytes  []byte
		format string
		w, h   int
	}{
		{"jpeg", testutil.TinyJPEG(t, 16, 24), "jpeg", 16, 24},
		{"png", testutil.TinyPNG(t, 16, 24), "png", 16, 24},
		{"gif", testutil.TinyGIF(t, 16, 24), "gif", 16, 24},
		{"bmp", testutil.TinyBMP(t, 8, 12), "bmp", 8, 12},
		{"tiff", testutil.TinyTIFF(t, 8, 12), "tiff", 8, 12},
		{"webp", testutil.TinyWebP(t), "webp", 2, 3},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if len(tc.bytes) == 0 {
				t.Fatal("fixture is empty")
			}
			if len(tc.bytes) > maxBytes {
				t.Errorf("fixture is %d bytes, want <= %d (arch §10.3 keeps the "+
					"whole fixture footprint under 200 KB)", len(tc.bytes), maxBytes)
			}

			cfg, format, err := image.DecodeConfig(bytes.NewReader(tc.bytes))
			if err != nil {
				t.Fatalf("image.DecodeConfig: %v", err)
			}
			if format != tc.format {
				t.Errorf("format = %q, want %q", format, tc.format)
			}
			if cfg.Width != tc.w || cfg.Height != tc.h {
				t.Errorf("dimensions = %dx%d, want %dx%d", cfg.Width, cfg.Height, tc.w, tc.h)
			}

			img, _, err := image.Decode(bytes.NewReader(tc.bytes))
			if err != nil {
				t.Fatalf("image.Decode: %v", err)
			}
			if got := img.Bounds().Dx(); got != tc.w {
				t.Errorf("decoded width = %d, want %d", got, tc.w)
			}
		})
	}
}

func TestTinyAnimatedWebP_isRejectedByXImageWebP(t *testing.T) {
	t.Parallel()

	raw := testutil.TinyAnimatedWebP(t)
	if len(raw) > 1024 {
		t.Errorf("fixture is %d bytes, want <= 1024", len(raw))
	}

	// This is the graceful-degradation trigger arch §5.5 depends on. If a
	// future x/image gains animated-WebP support this test fails loudly, which
	// is the right outcome: WP-07's 422 path would become dead code.
	if _, err := webp.Decode(bytes.NewReader(raw)); err == nil {
		t.Fatal("x/image/webp decoded the animated fixture; arch §5.5's " +
			"animated_webp -> 422 degradation no longer has a trigger")
	}

	// DecodeConfig still succeeds — the VP8X chunk carries the canvas size —
	// so the dimension pass of arch §5.8 must not be used as a decodability
	// probe. Pin that asymmetry so nobody "simplifies" it away.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("image.DecodeConfig on an animated webp: %v", err)
	}
	if cfg.Width != 2 || cfg.Height != 3 {
		t.Errorf("dimensions = %dx%d, want 2x3", cfg.Width, cfg.Height)
	}
}

func TestTinyAVIF_isAWellFormedAVIFContainer(t *testing.T) {
	t.Parallel()

	raw := testutil.TinyAVIF(t)
	if len(raw) > 1024 {
		t.Errorf("fixture is %d bytes, want <= 1024", len(raw))
	}
	// Decoding is deliberately not exercised here: gen2brain/avif's first
	// decode spins up a wazero runtime (~1 s, ~170 MiB) and D-2 keeps that off
	// every code path that does not need it, this suite included. Assert the
	// ISOBMFF brand instead; WP-07 owns the one test that really decodes it.
	if !bytes.Contains(raw[:32], []byte("ftypavif")) {
		t.Errorf("fixture does not start with an ftyp/avif box: % x", raw[:32])
	}
}
