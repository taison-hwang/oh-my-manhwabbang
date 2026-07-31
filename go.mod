module shelf

go 1.25.0

// The dependency set is frozen at the versions verified in docs/arch-backend.md §1.1
// (decisions.md D-08). Do not float these. There is deliberately NO HTTP router
// dependency: Go 1.22+ net/http.ServeMux covers the whole contract (D-09).
//
// These are `require`d before any package imports them so that wave-1 work packages
// start from a byte-identical, already-downloaded module graph. A bare `go mod tidy`
// prunes every one that no package imports yet — seven of the nine, today — so use
// `make tidy`, which rolls the change back and fails if any pin would be lost.
require (
	github.com/disintegration/imaging v1.6.2 // downscaling — imaging.Lanczos (D-10)
	github.com/gen2brain/avif v0.6.0 // AVIF decode via wazero, cgo-free
	github.com/klippa-app/go-pdfium v1.19.6 // PDF rasterisation, webassembly mode
	github.com/tetratelabs/wazero v1.12.0 // persistent wasm compilation cache
	golang.org/x/crypto v0.54.0 // bcrypt for the optional password
	golang.org/x/image v0.44.0 // bmp / tiff / webp decoders
	golang.org/x/text v0.40.0 // CP949 (EUC-KR) entry-name decoding
	gopkg.in/yaml.v3 v3.0.1 // config file
	modernc.org/sqlite v1.54.0 // pure-Go SQLite (CON-001)
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/ebitengine/purego v0.10.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jolestar/go-commons-pool/v2 v2.1.2 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	modernc.org/libc v1.74.1 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
)
