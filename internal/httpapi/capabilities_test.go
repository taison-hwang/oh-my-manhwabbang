package httpapi

// `pdf_enabled` and `avif_enabled` on `/api/health` (arch §7.4) and
// `/api/settings` (§7.11) are CAPABILITY claims, not configuration echoes.
//
// Each is two halves: the YAML key, and whether this build carries the codec at
// all (`-tags nopdf` / `-tags noavif`). Reporting the key alone advertises
// something the binary cannot do, and the client acts on it — the viewer offers
// a PDF it will get `501 unsupported` for, the library shows an AVIF thumbnail
// slot that only ever resolves to `422 thumb_unavailable`.
//
// This matters far more since ruling **E-21** made `noavif` the DEFAULT build
// tag (gen2brain/avif pulls ebitengine/purego, which links the binary against
// libc whatever CGO_ENABLED says, and prd NFR-OPS-003 makes musl/old-glibc NAS
// the primary target). `thumbnails.avif_enabled` still defaults to TRUE, so
// before this test the shipped default binary reported `avif_enabled: true` and
// refused every .avif. The PDF side already had the guard
// (`Server.pdfEnabled`); the AVIF side did not.
//
// The file carries no build tag on purpose. It has to compile and pass in all
// four tag combinations, because the whole point is that its verdict changes
// with the build.

import (
	"net/http"
	"testing"

	"shelf/internal/pdfium"
	"shelf/internal/thumbs"
)

// TestCapabilityFlags_neverExceedTheBuild is the invariant that catches the
// E-21 regression: reported ⟹ capable, on both endpoints, for both codecs.
//
// The environment asks for everything — `pdf.enabled: true`, and
// `thumbnails.avif_enabled` at its true default — so a handler that echoed the
// config would report true here regardless of what the binary contains.
func TestCapabilityFlags_neverExceedTheBuild(t *testing.T) {
	e := newEnv(t, withPDF())

	health := decodeBody[Health](t, e.get("/api/health"), http.StatusOK)
	settings := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)

	for _, tc := range []struct {
		flag     string
		compiled bool
		reported map[string]bool
		tag      string
	}{
		{
			flag:     "pdf_enabled",
			compiled: pdfium.Supported(),
			reported: map[string]bool{
				"/api/health":   health.PDFEnabled,
				"/api/settings": settings.Server.PDFEnabled,
			},
			tag: "nopdf",
		},
		{
			flag:     "avif_enabled",
			compiled: thumbs.AVIFSupported(),
			reported: map[string]bool{
				"/api/health":   health.AVIFEnabled,
				"/api/settings": settings.Server.AVIFEnabled,
			},
			tag: "noavif",
		},
	} {
		for endpoint, reported := range tc.reported {
			if reported && !tc.compiled {
				t.Errorf("%s reports %s: true, but this binary was built with "+
					"`-tags %s` and carries no decoder.\n"+
					"That is a capability the client acts on: it will request "+
					"something the server answers with 501/422 forever. The flag "+
					"is the config key AND the build tag — see Server.pdfEnabled "+
					"and Server.avifEnabled, and ruling E-21 in docs/decisions.md.",
					endpoint, tc.flag, tc.tag)
			}
			if !reported && tc.compiled {
				t.Errorf("%s reports %s: false in a build that DOES carry the "+
					"decoder, with the config key on. The capability is being "+
					"under-reported, which hides a working feature.", endpoint, tc.flag)
			}
		}
	}

	// The two endpoints are separate code paths (health.go and settings.go) and
	// have disagreed before; a client that reads one and not the other must not
	// see a different product.
	if health.PDFEnabled != settings.Server.PDFEnabled {
		t.Errorf("pdf_enabled disagrees: /api/health = %t, /api/settings = %t",
			health.PDFEnabled, settings.Server.PDFEnabled)
	}
	if health.AVIFEnabled != settings.Server.AVIFEnabled {
		t.Errorf("avif_enabled disagrees: /api/health = %t, /api/settings = %t",
			health.AVIFEnabled, settings.Server.AVIFEnabled)
	}
}

// The other half of each gate: the config key still turns a capable build off.
// Without this, "always report the build tag" would pass the test above.
func TestCapabilityFlags_configKeyStillGates(t *testing.T) {
	// `pdf.enabled: false` (newEnv's default) and `avif_enabled: false`.
	e := newEnv(t, withoutAVIFConfig())

	health := decodeBody[Health](t, e.get("/api/health"), http.StatusOK)
	settings := decodeBody[Settings](t, e.get("/api/settings"), http.StatusOK)

	for endpoint, got := range map[string]bool{
		"/api/health":   health.PDFEnabled,
		"/api/settings": settings.Server.PDFEnabled,
	} {
		if got {
			t.Errorf("%s reports pdf_enabled: true with `pdf.enabled: false`", endpoint)
		}
	}
	for endpoint, got := range map[string]bool{
		"/api/health":   health.AVIFEnabled,
		"/api/settings": settings.Server.AVIFEnabled,
	} {
		if got {
			t.Errorf("%s reports avif_enabled: true with `thumbnails.avif_enabled: false`",
				endpoint)
		}
	}
}
