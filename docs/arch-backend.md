# Backend Architecture — SHELF (Archive-Based Comic/Book Web Reader)

| | |
|---|---|
| Document | Backend architecture & implementation spec |
| Version | v1.0 |
| Date | 2026-07-28 |
| Status | Approved for implementation |
| Source of truth | `docs/prd.md` (URD v1.0). Every design decision below cites the FR-/NFR-/CON-/AC- id it satisfies. |
| Audience | Backend implementers, and the frontend team (§7 is a binding contract) |

> **How to read this.** §1–§2 fix the dependency set and the repository shape. §3–§6 are the engine. **§7 is the HTTP contract** — the frontend is built against it in parallel and it is normative down to the field name. §8–§11 are security, observability, testing and build.
>
> Everything in §1 marked **VERIFIED** was executed against this machine and, where noted, against the real 5 TB collection at `/mnt/big-data/pds/taison-data/02. books/01. mangga` (965 top-level entries → 963 series, 11,157 ZIP archives, 1,356,011 ZIP entries). Numbers are measured, not estimated.

---

## 0. Measured facts about the target collection

These drove several decisions and are recorded so future readers know the design was not speculative.

| Measurement | Value |
|---|---|
| Root direct children | 672 directories + 293 files = 965 candidates → **963 series** (291 of the files are `.zip`; 1 `.rar` and 1 `.DS_Store` are ignored) |
| ZIP archives found (recursive) | **11,157** |
| ZIP entries in total | **1,356,011** |
| Central-directory-only scan of all 11,157 archives, 16 workers | **32.3 s**, 105 MB read total, **9.4 KB and 2.0 `ReadAt` calls per archive** |
| Structurally broken archives | **9 / 11,157 (0.08 %)** — all truncated downloads; `archive/zip` and our reader agree on every one |
| Entries with encryption bit set | 0 in a 508-archive sample |
| Non-ASCII entry names lacking the UTF-8 flag (bit 11) | **14,630**; **0** were valid UTF-8; **0** produced U+FFFD when decoded as CP949 |
| Entry extension mix (sample: **56,293** of the 1,356,011 entries) | `.jpg` 53,410 · `.gif` 1,349 · `.png` 946 · `.jpeg` 214 · `.db` 125 (`Thumbs.db`) · `.bmp` 19 · `.txt` 17 · `.zip` 12 · `.tif` 1. **Zero `.webp`, zero `.avif` in the sample** — a second, independent pass agreeing with `docs/data-survey.md`'s 500-ZIP scan. Two samples, not a census. |
| Pages per archive | min 2 · p50 **96** · p90 184 · p99 **570** · max **1,071** (AC-008 is a real case, not hypothetical) |
| Max internal nesting inside an archive | 2 directory levels |
| Max directory depth under the root | 3 levels |
| Top-level directory shapes | zips-only **592** · **mixed 47** · subdirs-only 26 · empty/non-media 5 · images-direct 1 · pdfs-only 1 |
| Shape of the "mixed" case in practice | Almost always *N archives + exactly 1 loose image* — i.e. **a cover file, not a book** (`강철의 연금술사 00 Cover.jpg`, `[cover].jpg`) |
| Real duplicate books observed | `군계(軍鷄) 01권/` (folder) **and** `군계(軍鷄) 01권.zip` in the same series; also `07권.zip`, `07권.repair.zip`, `07권 (2).repair.zip` |
| Non-media clutter | As first surveyed: `.hv3` 26, `.txt` 19, `.rar` 8, `.part` 2, `.info` 1. **Re-measured 2026-08-12** against the collection as it now stands: `.rar` **14 on disk** plus **9 inside ZIPs** (8 in `사모님은 학생회장.zip`, 1 in `야이바 03권.zip`) — all of it books since D-71, not clutter. `.hv3` is down to **1** in the library, the sole entry of `펌프킨 시저스 04.zip` — a book since **E-51**, not clutter. Re-measured 2026-08-24 across the whole machine: of **55** `.hv3` files, **54 are RAR archives wearing the extension** (the `궁` series, all of them in the trash) and that one is the only genuine HV3 there is. `.cbz`/`.cbr` 0, `.7z` 0. |

---

## 1. Dependencies — decided and verified

Toolchain: **Go 1.25** minimum (`go 1.25.0` in `go.mod`), built with `GOTOOLCHAIN=auto`. Verified that `go1.23.0`, `go1.24.0`, `go1.25.0` and `go1.26.0` all auto-download on this machine; latest stable on the proxy is `go1.26.5`. Go ≥1.22 is required by prd 6.1; we require ≥1.25 additionally for `os.Root` (§8.1) and mature `net/http.ServeMux` patterns.

> Every `go` command in this project **must** be prefixed:
> ```
> GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org GOTOOLCHAIN=auto
> ```
> `GOPROXY` resolves to empty in the operator's shell otherwise.

### 1.1 Accepted dependencies

| Module | Version | Why | License | cgo-free | Verified how — **VERIFIED** |
|---|---|---|---|---|---|
| `modernc.org/sqlite` | **v1.54.0** | Index DB + user DB. Mandated by prd 6.1 / CON-001. | BSD-3-Clause | **Yes, pure Go** | Built a static ELF with `CGO_ENABLED=0` (`statically linked`, no interpreter). `PRAGMA journal_mode` returned `wal`; `synchronous=1`; `foreign_keys=1`; `sqlite_version()` = **3.53.3**. 200,000-row insert in a single tx: **1.67 s**. 2,000 point queries concurrent with 200 UPDATEs: **205 ms**, zero `SQLITE_BUSY`. `-wal`/`-shm` files present on disk. Cross-compiled clean to `linux/arm64`, `linux/arm`, `windows/amd64`, `darwin/arm64`. |
| `golang.org/x/text` | **v0.40.0** | CP949/EUC-KR entry-name fallback (FR-IDX-008, AC-002). | BSD-3-Clause | Yes | Decoded **14,630 real non-ASCII CP949 entry names** from the collection with **zero** U+FFFD. Also probed decoder semantics: `korean.EUCKR.NewDecoder()` **never returns an error** — it substitutes U+FFFD. This changes the fallback rule (see §4.4). |
| `golang.org/x/image` | **v0.44.0** | `bmp`, `tiff`, `webp` decoders + `draw` scalers. | BSD-3-Clause | Yes | Encoded and decoded 1600×2400 images in every format through `image.Decode`. `x/image/webp` decoded a libwebp-produced still WebP correctly, and **correctly rejects animated WebP** (`webp: invalid format`) — this is the graceful-degradation trigger in §5.5. |
| `github.com/disintegration/imaging` | **v1.6.2** | Downscaling. **10× faster than `x/image/draw`** at our ratios. | MIT | Yes | 1600×2400 → 300 px wide, scale step only: `imaging.Lanczos` **18.7 ms**, `imaging.Box` 12.0 ms, `imaging.Linear` 12.5 ms vs `draw.CatmullRom` **196.9 ms**, `draw.ApproxBiLinear` 5.6 ms (poor quality). `imaging` parallelises internally — see §5.4 for worker-pool interaction. |
| `github.com/gen2brain/avif` | **v0.6.0** | AVIF decode for thumbnails (FR-IDX-011). No pure-Go AVIF decoder exists; this wraps libavif/dav1d in **wazero**, so it is still `CGO_ENABLED=0`. | MIT (libavif/dav1d: BSD-2) | **Yes, via wazero** | Encoded and decoded a 1600×2400 AVIF. Decode **1.07–1.12 s/image**. Runtime is **lazily initialised on first decode** (RSS 6 MiB → 726 MiB peak → settles at **~170 MiB**). Adds **1.58 MB** to the binary. |
| `github.com/klippa-app/go-pdfium` | **v1.19.6** | PDF rasterisation (FR-SRV-006), `webassembly` mode as prd 6.1 requires. | MIT (bundled `pdfium.wasm`: BSD-3) | **Yes, via wazero** | See §1.3 — rendered a real 36 MB / 284-page Korean manga PDF. |
| `github.com/nwaples/rardecode/v2` | **v2.3.0** | Unpacking **packed** RAR 4 entries (D-71). Not on the path for a stored entry, which is 2,685 of the collection's 2,914 — those are an `io.SectionReader` over the container and touch no dependency at all. | BSD-2-Clause | **Yes, pure Go** (21 files, no cgo, no `unsafe`) | Built a static ELF with `CGO_ENABLED=0`. Decoded **all 14** real RAR archives — **2,914 entries**, 0 size mismatches, 0 non-UTF-8 names, RAR4 Unicode filenames (`平井和正×泉谷あゆみ`) correct where the OEM prefix is question marks. Throughput **202 MB/s**. The number that decided the design: random access through `io.ReaderAt` + `io.SectionReader` — the pooled path, never `OpenReader(path)`, which would bypass `openpool` and the `os.Root` guard of §8.1 — reaches **entry #825 of a 385 MB archive in 6 ms**, because a non-solid archive lets it seek past packed data instead of inflating it. Splicing one entry into a one-file archive and decoding it alone was checked against the whole-archive decode: 118/118 CRC-clean. |
| `github.com/tetratelabs/wazero` | **v1.12.0** | Transitive under avif + pdfium; used **directly** to install a persistent compilation cache. | Apache-2.0 | Yes | `wazero.NewCompilationCacheWithDir` cut pdfium pool init from **3.885 s / 299 MiB RSS** to **135 ms / 43 MiB RSS** (19 MB on-disk cache). Decisive; see §6.3. |
| `gopkg.in/yaml.v3` | **v3.0.1** | Config file (FR-CFG-001..003). | MIT + Apache-2.0 | Yes | Compiles; standard. |
| `golang.org/x/crypto` | **v0.54.0** | `bcrypt` for the optional password (NFR-SEC-002). | BSD-3-Clause | Yes | Compiles cgo-free. |

Standard library only for: HTTP (`net/http`), routing (`net/http.ServeMux`), ZIP inflate (`compress/flate`), JPEG/PNG/GIF (`image/*`), hashing (`crypto/sha256`, `hash/crc32`), logging (`log/slog`), embedding (`embed`).

### 1.2 Rejected / deferred, with reasons

| Candidate | Verdict | Reason |
|---|---|---|
| `github.com/go-chi/chi`, `gorilla/mux`, `gin` | **Rejected** | Go 1.22+ `net/http.ServeMux` is sufficient. **VERIFIED**: `"GET /api/books/{bid}/pages/{n}"` and `"PUT /api/books/{bid}/progress"` route correctly with `r.PathValue()`; wrong method yields **405**, not 404; `"GET /{$}"` gives exact-root matching; `http.StripPrefix(base, mux)` mounted under `/reader` routes every case correctly (§8.3). Adding a router would be dependency cost for zero capability. |
| `archive/zip` as the *scan-time* reader | **Rejected for indexing, kept as an oracle** | `zip.File` **does not export the local-header offset**, which FR-SRV-002 requires us to persist. The only stdlib way to get an offset is `zip.File.DataOffset()`, which performs **one random read per entry**. A parity run that called `DataOffset()` for all 1.36 M entries **did not finish in 10 minutes**; the same scan without it takes **32 s**. We therefore ship our own central-directory reader (§4.3) and keep `archive/zip` as a differential-test oracle. |
| `github.com/gen2brain/webp` | **Deferred (optional fallback)** | Works and decodes animated WebP, but pulls a second wazero module (~55 MiB RSS) for a format that **does not occur even once** in the target collection. `x/image/webp` covers still WebP with no wasm. Policy in §5.5. |
| `github.com/gen2brain/jpegxl` | **Rejected** | JPEG XL is not in FR-IDX-011. |
| `x/image/draw` as the primary scaler | **Rejected as primary** | 10× slower than `imaging` at our downscale ratios (196.9 ms vs 18.7 ms). Retained as a zero-alloc fallback path only. |
| `github.com/ncruces/go-sqlite3` | **Rejected** | A credible wazero-based alternative, but prd 6.1 names `modernc.org/sqlite` explicitly and it verified cleanly. No reason to deviate. |
| Native cgo pdfium (`go-fitz`, pdfium cgo mode) | **Deferred to CON-002** | Only if wasm rendering proves too slow in production. Not needed for v1 (§1.3). |

### 1.3 PDF verdict — **VIABLE, ship it**

prd §9 puts PDF in release 2 and prd §10 flags it as the top schedule risk. **That risk is retired.** Measured on `[만화] 미생 1~9 (완결 pdf)/미생 2권.pdf` (36.2 MB, 284 pages, Korean scanned manga):

```
CGO_ENABLED=0 build .................................... OK, static
wazero pool init, cold  ................. 3.885 s   RSS 299 MiB
wazero pool init, warm compilation cache  135 ms    RSS  43 MiB   <-- 29x faster
OpenDocument via streaming FileReader ...   2–36 ms  RSS unchanged
                                             (the 36 MB file is NOT slurped)
FPDF_GetPageCount ....................... instant, 284 pages
RenderPageInPixels @ width 1200 ......... 254 / 332 / 356 / 436 ms
random-access render, 5 pages, avg ...... 296 ms/page
JPEG encode of the result ...............  51–108 ms
steady-state RSS ........................ 61–310 MiB
```

**Decision.** PDF ships in v1 behind three guards, not deferred:

1. **Lazy init.** The pdfium pool is created on the *first* PDF request, never at startup. Idle memory therefore stays inside NFR-PRF-005 (≤200 MB) on installations with no PDFs — and only **2 of the 963** real series contain a PDF at all.
2. **Persistent wazero compilation cache** at `<cache_dir>/wazero/`, so the one-time 3.9 s cost is paid once per binary version, not per process (NFR-OPS-006).
3. **Rendered pages are cached to disk** exactly like thumbnails (§5.6), so ~300 ms is paid once per (page, width). This is prd §10's own prescribed mitigation.

A `nopdf` build tag removes pdfium entirely (**−8.34 MB** binary). The **API surface exists in both builds**: with `nopdf`, PDF books are indexed with `status:"unsupported"` and page requests return `501` with `code:"unsupported"` and a human message. No hand-waving, no silent 404s.

### 1.4 Binary size budget (`CGO_ENABLED=0 -trimpath -ldflags="-s -w"`, with `go:embed`)

| Build | Size |
|---|---|
| Full — sqlite + all image codecs + AVIF + pdfium + embedded web assets | **17.71 MB** |
| `-tags noavif` | 16.13 MB |
| `-tags "noavif nopdf"` | 7.79 MB |
| (reference) hello-world | 1.43 MB |

pdfium costs 8.34 MB; AVIF costs 1.58 MB. Ship the full build (NFR-OPS-001: a single file; 18 MB is fine for a NAS).

> **Superseded by measurement (ruling E-19, 2026-07-29).** The table above is the
> **spike** binary — a `main` that linked the dependencies, not the product. The
> shipped `cmd/shelf` with `internal/{httpapi,scanner,index,thumbs,userdata,…}`
> and the real SPA measures, with these exact flags:
>
> | Build | Size (bytes) |
> |---|---|
> | Full | **27 418 916** |
> | `-tags noavif` | 25 833 656 (AVIF = 1 585 260 — the estimate above was right) |
> | `-tags nopdf` | 19 689 764 (PDF = 7 729 152) |
> | `-tags "nopdf noavif"` | 15 286 456 |
>
> The estimate's error is entirely in its *base* term: 7.79 MB for a spike with
> neither codec, against **15.29 MB** for the product with neither — most of the
> difference being `modernc.org/sqlite`, whose pure-Go translation CON-001's
> `CGO_ENABLED=0` requires. `SIZE_BUDGET`, impl-plan §7.3 and `README.md` carry
> the resulting figure — E-19 as amended by **E-21** §4 — and
> `internal/buildinfo/release_budget_test.go` keeps the three in step; this
> paragraph deliberately does not restate it, because an unguarded fourth copy is
> how the number drifts. prd NFR-OPS-001 itself states no size.

---

## 2. Repository layout

Module path: **`shelf`** — a bare module path, no `github.com/<user>/…` prefix (binary name `shelf`, matching the "SHELF" branding of the UI prototype). *Ruled by the orchestrator on 2026-07-28, closing OQ-1/E-1: the app is embedded and never `go get`-ed, so inventing a repo host in 200 import lines is worse than a later `sed`. Every import in the landed tree reads `shelf/internal/…`.*

```
zip-viewer/
├── go.mod                          module shelf   (go 1.25.0)
├── go.sum
├── Makefile
├── shelf.example.yaml              committed, fully commented, = §3 schema
├── .gitignore                      web/node_modules, web/dist/*, dist/, *.db*
├── docs/
│   ├── prd.md                      (URD — source of truth)
│   ├── design.md                   (design handoff)
│   └── arch-backend.md             (this document)
│
├── cmd/
│   └── shelf/
│       └── main.go                 flag parsing, config load, wiring, graceful shutdown
│
├── internal/
│   ├── config/                     YAML schema, defaults, lookup order, validation
│   │   ├── config.go
│   │   └── config_test.go
│   ├── ids/                        series_id / book_id / thumb + pdf-page cache keys (§3.4, §5.6)
│   ├── natsort/                    Compare() + Key() + property test that they agree (§4.7)
│   ├── hangul/                     choseong extraction for FR-LIB-006
│   ├── kenc/                       ZIP entry-name decoding: UTF-8 / CP949 rule (§4.4)
│   │
│   ├── archive/                    the "archive reader" abstraction prd 7.2 asks for
│   │   ├── archive.go              type Reader interface { Format() string; ReadIndex(ctx,ReaderAt,size) (*Index,error); OpenEntry(ctx,ReaderAt,EntryRef) (io.ReadCloser,error) }
│   │   └── zipidx/                 our central-directory reader + entry opener (§4.3, §5.1)
│   │       ├── centraldir.go
│   │       ├── open.go
│   │       ├── centraldir_test.go
│   │       └── testdata/           hand-built fixtures incl. ZIP64 + truncated
│   │
│   ├── source/                     BookSource: uniform page access over zip | dir | pdf
│   │   ├── source.go               interface + registry
│   │   ├── zipsource.go
│   │   ├── dirsource.go
│   │   ├── pdfsource.go            //go:build !nopdf
│   │   └── pdfsource_stub.go       //go:build nopdf   -> ErrUnsupported
│   │
│   ├── openpool/                   LRU pool of open *os.File archive handles (§5.2)
│   ├── scanner/                    walk, classify, index, incremental, progress (§4)
│   │   ├── scanner.go
│   │   ├── classify.go             prd 2.2 table + the real-world "mixed" rules
│   │   ├── incremental.go          (mtime,size) skip + dir fingerprint
│   │   └── progress.go
│   ├── index/                      SQLite index DB: schema, migrations, queries (§3.4)
│   ├── userdata/                   SQLite user DB: progress, prefs, settings (§3.5)
│   ├── thumbs/                     cache paths, generator, worker pool, purge (§5.4–5.6)
│   ├── pdfium/                     //go:build !nopdf — pool lifecycle, wazero cache
│   ├── httpapi/                    the whole of §7
│   │   ├── router.go               ServeMux wiring + base_path + SPA fallback
│   │   ├── middleware.go           request id, slog, recover, auth, gzip-exempt
│   │   ├── errors.go               the single error envelope
│   │   ├── dto.go                  every struct in §7, with json tags
│   │   ├── series.go books.go pages.go thumbs.go progress.go scan.go settings.go cache.go auth.go
│   │   └── golden_test.go          JSON golden files the frontend can diff against
│   ├── auth/                       bcrypt verify, HMAC session cookie, login rate limit
│   ├── buildinfo/                  version, commit, date (ldflags)
│   └── testutil/                   synthetic fixture builders (§10.3)
│
└── web/                            *** both the Vite project AND a Go package ***
    ├── embed.go                    package web;  //go:embed all:dist
    ├── package.json  vite.config.ts  tsconfig.json  tailwind.config.ts  index.html
    ├── src/…                       React 18 + TS
    └── dist/                       vite build output (gitignored except .gitkeep)
        └── .gitkeep                so `//go:embed all:dist` always matches
```

### 2.1 Frontend embedding (NFR-OPS-001)

`go:embed` cannot reach outside its own package directory, so `web/` is *simultaneously* the Vite project and a Go package:

```go
// web/embed.go
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Dist returns the built SPA rooted at dist/. It is empty (but valid) when the
// frontend has not been built yet; the HTTP layer detects the missing
// index.html and serves a "run `make web`" placeholder instead of a 404 storm.
func Dist() fs.FS {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		panic(err) // impossible: dist/ is guaranteed to exist by .gitkeep
	}
	return sub
}
```

`all:dist` also matches dot-files, so the committed `web/dist/.gitkeep` guarantees the pattern never fails to match on a clean checkout. `make build` runs `pnpm build` first (§11).

Serving rules:
* `GET {base}/assets/*` — hashed Vite filenames → `Cache-Control: public, max-age=31536000, immutable`.
* `GET {base}/` and any unmatched non-`/api/` path → `index.html` with `Cache-Control: no-cache` (SPA fallback for client-side routing).
* Anything under `{base}/api/` that does not match a route → JSON 404 in the §7.2 envelope, **never** the SPA fallback.

---

## 3. Configuration and data model

### 3.1 Config file lookup order (FR-CFG-001..003)

First path that exists wins. Nothing is merged.

1. `--config <path>` (explicit; if given and missing → fatal)
2. `$SHELF_CONFIG`
3. `./shelf.yaml`
4. `$XDG_CONFIG_HOME/shelf/shelf.yaml` — default `~/.config/shelf/shelf.yaml`
5. Unix only: `/etc/shelf/shelf.yaml`

If none exists, the process prints the path it would use, writes a commented starter file there when `--init-config` was passed, and exits non-zero otherwise. AC-007 ("single binary + config file") is satisfied by `shelf --config ./shelf.yaml`.

### 3.2 YAML schema (complete)

> **Amended — read this first.** `internal/config` landed in wave 1 and is the executable form of this
> schema; `shelf.example.yaml` is the round-tripped copy its test suite asserts against. Three keys below
> are superseded by `impl-plan.md` §0.3 and are shown corrected inline:
> **A-1** `thumbnails.widths` → `[120, 240, 400, 640]`, **A-2** `reader.fit_mode` → `"height"`,
> **A-3** new key `scan.include_globs: []`. There is **no `server.max_open_archives` key** — see §5.2.
>
> **A-8 adds one key that `internal/config` does not yet have:** the whole `library:` block below
> (`library.recently_added_days: 14`). It is **not** in the landed `config.Config` and **not** in
> `shelf.example.yaml`; both are follow-up work for the package that owns config (WP-01). Until it
> lands, no consumer may assume `cfg.Library` exists.
> *(2026-07-30: it has since landed — `config.Library` exists and `internal/httpapi/settings.go` reads
> `cfg.Library.RecentlyAddedDays`. The paragraph is kept because it is what the A-8 hand-off said.)*
>
> **A-11 adds one key that `internal/config` does not have yet:** `server.allow_root_editing: false`
> below, the gate on §7.4's root-editing endpoints (ruling **E-26**). Same status as A-8's on the day it
> was written — not in `config.Config`, not in `shelf.example.yaml`, follow-up work for WP-01 listed in
> `impl-plan.md` §0.3. Until it lands, the capability is off and `Settings.server.root_editing_enabled`
> is `false`, which is the correct default answer anyway.

```yaml
# ---- shelf.yaml -----------------------------------------------------------
# Every key is optional except roots[].name and roots[].path.

server:
  listen: "0.0.0.0"          # bind address.            default "0.0.0.0"
  port: 8080                 # int 1..65535.            default 8080
  base_path: ""              # NFR-SEC-003. e.g. "/reader". Leading slash added,
                             #   trailing slash stripped. "" = mounted at root.
  read_header_timeout: "10s" # duration.                default 10s
  # NOTE: no write timeout — page streams and SSE must not be cut off.
  shutdown_grace: "10s"      # duration.                default 10s
  trusted_proxy_headers: false  # honour X-Forwarded-Proto / -For when behind a
                                #   reverse proxy.      default false
  allow_root_editing: false  # AMENDMENT A-11 (ruling E-26).  default false.
                             #   Lets POST /api/roots and DELETE /api/roots/{name}
                             #   (§7.4) add and remove entries in the `roots:` list
                             #   BELOW, in this file. With it false both verbs are
                             #   403 forbidden and the settings screen renders no
                             #   추가/제거 controls at all.
                             #   Leave it false unless you know who can reach the
                             #   port: `listen` defaults to every interface and the
                             #   `auth:` block is omitted by default (ruling E-8), so
                             #   an ungated write API would let anyone on the LAN
                             #   make this server open any directory it can read —
                             #   and §7.4 + §7.5 would then serve the contents.
                             #   Turning it on grants no authority that the person
                             #   editing this file does not already have.
                             #   An ADDED root is opened at once (A-12, ruling
                             #   E-40); a REMOVED one leaves the library at once
                             #   but its handle is released at shutdown.
  browse_bases: []           # AMENDMENT A-12 (ruling E-40).  default [].
                             #   Bounds the settings screen's folder picker,
                             #   GET /api/browse: it lists these directories and
                             #   anything beneath them and refuses everything else,
                             #   INCLUDING their parents.  Empty means no picker at
                             #   all and the path is typed, exactly as before E-40.
                             #   A separate key from allow_root_editing on purpose:
                             #   editing already grants the power to mount any
                             #   readable directory, so browsing adds no authority
                             #   where editing is on — but it would where editing is
                             #   off, and no operator should have to work that out.
                             #   Entries must be absolute; they are NOT required to
                             #   exist at startup (§4.9), and one that does not is
                             #   listed and greyed out rather than fatal.

roots:                       # FR-CFG-001. At least one required.
  - name: "manga"            # REQUIRED. Stable identity — see §4.1. Changing a
                             #   name orphans that root's reading progress.
                             #   [a-zA-Z0-9._-]{1,64}, unique across roots.
    path: "/mnt/big-data/pds/taison-data/02. books/01. mangga"   # REQUIRED, absolute
    enabled: true            # FR-CFG-002.              default true
    label: "만화"             # optional display name; falls back to name

storage:
  data_dir: ""               # index.db + user.db + session.key.
                             #   default: $XDG_DATA_HOME/shelf
                             #            ~/.local/share/shelf            (linux)
                             #            ~/Library/Application Support/shelf (macOS)
                             #            %LOCALAPPDATA%\shelf            (windows)
  cache_dir: ""              # thumbs/ + pdf/ + wazero/. FR-CFG-003.
                             #   default: $XDG_CACHE_HOME/shelf
                             #            ~/.cache/shelf                  (linux)
                             #            ~/Library/Caches/shelf          (macOS)
                             #            %LOCALAPPDATA%\shelf\cache      (windows)

scan:
  on_start: true             # FR-IDX-001. run an incremental scan at boot
  workers: 0                 # FR-CFG-003. 0 => min(8, max(2, NumCPU/2)).
                             #   I/O-bound; measured 346 archives/s at 16.
  max_depth: 3               # how deep below a series we look for books (§4.2).
                             #   Real collection needs 3. 0 = unlimited (unwise).
  follow_symlinks: false     # default false
  cover_max_loose_images: 3  # a directory holding <= this many loose images
                             #   ALONGSIDE other books treats them as cover
                             #   candidates, not as a book (§4.2).
  exclude_globs: []          # extra excludes, matched against the root-relative
                             #   slash path, e.g. ["**/*.part", "**/@eaDir/**"]
  include_globs: []          # AMENDMENT A-3 (ruling E-6). Empty = scan everything.
                             #   When non-empty, only a root's direct children whose
                             #   BASE NAME matches at least one path.Match pattern
                             #   become series. Applied before classification.

thumbnails:
  widths: [120, 240, 400, 640]  # AMENDMENT A-1 (D-37; was [240,320,640]).
                             #   FR-CFG-003. Sorted ascending, deduped, 32..2048.
                             #   Requests snap UP to the nearest configured width.
  quality: 82                # JPEG quality 1..100.     default 82
  format: "jpeg"             # CON-003: jpeg only in v1. Any other value is a
                             #   config error. Changing it later only needs a
                             #   cache purge (format is in the hash input).
  workers: 0                 # FR-THM-005. 0 => min(4, NumCPU). CPU+RAM bound:
                             #   ~25 MiB peak RSS per in-flight decode.
  cover_first: true          # FR-THM-003
  avif_enabled: true         # decode .avif for thumbnails. ~1.1 s and ~170 MiB
                             #   RSS on first use; serialised to 1 at a time.
  max_source_bytes: 67108864 # refuse to decode a single page larger than 64 MiB

pdf:
  enabled: true              # false => same behaviour as the `nopdf` build tag
  workers: 1                 # concurrent pdfium instances. Each is ~60–300 MiB.
  default_width: 1400        # px, when the client sends no ?w=
  max_width: 3000            # clamp
  cache_renders: true        # persist rasterised pages under <cache_dir>/pdf/

library:                     # AMENDMENT A-8 (ruling E-9). Library-listing policy.
  recently_added_days: 14    # int 1..3650.              default 14
                             #   The window behind GET /api/series?scope=added and
                             #   the 사이드바 "최근 추가" count (§7.5). A series is
                             #   "recently added" while
                             #     now - first_seen_at <= recently_added_days * 86400.
                             #   first_seen_at lives in user.db (§3.6) and is written
                             #   ONCE, on first sighting, so this window is NOT reset
                             #   by --rebuild-index. 0 and negatives are a config
                             #   error, not "disabled": an empty smart list with no
                             #   explanation is worse than a wrong window.

reader:                      # server-side DEFAULTS; the user can override them
                             # per book at runtime (stored in user.db)
  prefetch: 4                # FR-VWR-006. 0..20.       default 4
  reading_direction: "ltr"   # "ltr" | "rtl"            default "ltr"
  display_mode: "single"     # "single" | "spread" | "vertical"   default "single"
  fit_mode: "height"         # AMENDMENT A-2 (D-38; was "contain").
                             #   "width" | "height" | "original" | "contain"
  theme: "system"            # "light" | "dark" | "system"

auth:                        # NFR-SEC-002. Omit the whole block to disable auth.
  password: ""               # plaintext; hashed with bcrypt at startup, never
                             #   stored. Prefer password_hash.
  password_hash: ""          # bcrypt hash, e.g. from `shelf hash-password`
  session_ttl: "720h"        # 30 days
  session_key_file: ""       # default <data_dir>/session.key, 32 random bytes,
                             #   created 0600 on first boot

log:
  level: "info"              # debug | info | warn | error       NFR-OPS-005
  format: "text"             # text | json
  http_requests: true        # log one line per request
```

**Validation at startup (fail fast, exit 2 with a precise message):** duplicate or malformed `roots[].name`; relative `roots[].path`; `path` not an existing directory; `thumbnails.format != "jpeg"`; empty `widths`; unwritable `data_dir`/`cache_dir`; `base_path` containing `..`; `auth.password` and `auth.password_hash` both set; **`library.recently_added_days` outside 1..3650 (A-8)**. A disabled root (`enabled: false`) is **kept in the index** and simply excluded from listings — disabling must not destroy the user's progress.

**A-11 adds nothing to that list, deliberately.** `server.allow_root_editing` is a boolean and strict decoding (`KnownFields(true)`) already rejects a typo in the key. **A-12 adds two rules and no more**: every `server.browse_bases[i]` must be non-empty and absolute, and each is cleaned in place. Cleaning at load is not cosmetic — `GET /api/browse` decides containment by comparing these strings, and an uncleaned `/mnt/x/` would fail to contain `/mnt/x/y`, refusing everything under a base the operator did configure. Existence is deliberately **not** checked, for the same reason a root's path is not: §4.9 forbids an unmounted drive from keeping the server down. In particular, a configuration file that *lives inside* a `roots[].path` is **not** a startup error: it disables root editing at request time (§7.4's gate, `detail.reason: "config_inside_root"`) and nothing else. Making it fatal would stop an existing installation from booting over a feature it never asked for, which is a worse answer than switching the feature off — and every rule in the list above is one that makes the server *unable to run correctly*, which this one does not.

### 3.3 FR-CFG-005 / NFR-DAT-002 — never write to media volumes

The scanner and page server call `os.Open`, `os.Stat`, `os.ReadDir`, and `ReaderAt.ReadAt` only. There is no `os.Create`, `os.Rename`, `os.Remove`, `os.Chtimes` or `os.Mkdir` reachable from any code path rooted at a `roots[].path`. This is enforced by a lint rule in CI (`grep`-based guard in `make lint`, listed in §11) and by an integration test that mounts a fixture root read-only and runs a full scan + a full read of one book. AC-001 (no temp files anywhere but cache + media) follows: pages are streamed from offsets and never extracted.

### 3.4 Identity scheme (FR-CFG-004, FR-STT-003, AC-006)

All identifiers are derived **purely from `(root name, root-relative path)`** — never from a database row id, never from an absolute path. Moving a root on disk, or rebuilding the index, reproduces byte-identical ids.

```go
// internal/ids/ids.go — as shipped in wave 1; this is the authoritative surface.
package ids

// IDVersion is exported: WP-03 writes it into meta.id_version in BOTH databases
// (§3.5, §3.6) and startup refuses an unknown value (§11 step 3).
const IDVersion = "shelf-id/1"

const Length = 16                                        // 80 bits of base32
const Alphabet = "abcdefghijklmnopqrstuvwxyz234567"       // RFC 4648, lowercased

var enc = base32.NewEncoding(Alphabet).WithPadding(base32.NoPadding)

// relPath is the item's path relative to the ROOT directory (not to the
// series). It is normalised by NormalizeRel before hashing, so `\` and `/`
// spellings of one path agree. NUL cannot occur in a path component on any
// supported OS, nor in a root name, so it is an unambiguous separator.
func derive(domain, rootName, relPath string) string {
	sum := sha256.Sum256([]byte(IDVersion + "\x00" + domain + "\x00" + rootName +
		"\x00" + NormalizeRel(relPath)))
	return enc.EncodeToString(sum[:10]) // 80 bits -> exactly 16 lowercase chars
}

func SeriesID(rootName, relPath string) string { return derive("series", rootName, relPath) }
func BookID(rootName, relPath string) string   { return derive("book",   rootName, relPath) }

// NormalizeRel canonicalises a root-relative path: `\`→`/`, path.Clean, no
// leading slash, and "" for the root itself.
func NormalizeRel(relPath string) string

// Valid reports whether s is syntactically an id (exactly Length chars from
// Alphabet). The HTTP layer uses it to separate 400 bad_request from 404
// not_found (§7.1) and it is traversal layer 1 of §8.1.
func Valid(s string) bool
```

**Exact hash input string**, spelled out because it is a compatibility surface:

```
"shelf-id/1" ‖ 0x00 ‖ "series" ‖ 0x00 ‖ <root name> ‖ 0x00 ‖ <root-relative slash path>
"shelf-id/1" ‖ 0x00 ‖ "book"   ‖ 0x00 ‖ <root name> ‖ 0x00 ‖ <root-relative slash path>
```
Algorithm: **SHA-256**, truncated to the **first 10 bytes**, encoded with **RFC 4648 base32 using the lowercase alphabet `abcdefghijklmnopqrstuvwxyz234567`, no padding** → a **16-character** `[a-z2-7]{16}` token.

*Worked example* (**VERIFIED against the shipped `internal/ids`**, wave 1 — these are the exact strings `go test ./internal/ids/` pins as golden vectors):
```
SeriesID("mangga", "[만화] 군계 1~25")                        = ruzwlotzngls2ua5
BookID  ("mangga", "[만화] 군계 1~25/군계(軍鷄) 01권.zip")      = yvtfrny77ehkt2we
```
The full input bytes behind the first line, so the example can be checked by hand:
```
"shelf-id/1" 0x00 "series" 0x00 "mangga" 0x00 "[만화] 군계 1~25"
└─ SHA-256 ─┘ → take bytes[0:10] → base32(abcdefghijklmnopqrstuvwxyz234567, no pad) → "ruzwlotzngls2ua5"
```

> **Erratum (2026-07-28).** Earlier revisions of this section printed `gzj75n6x7rir6but` /
> `ox74tfcrwwnfopch` here. Those came from the pre-decision spike, which hashed `root ‖ 0x00 ‖ rel`
> with **no version tag and no domain tag** — i.e. not the construction this very section specifies
> three paragraphs above, and not what any shipped code computes. D-14 and impl-plan §3 WP-02
> acceptance 1 both require the domain tag, so the tagged construction won and the literals were
> recomputed from `internal/ids`. **The code is authoritative for this scheme**; `TestIDs_hashInput_isTheArchSpecString`
> rebuilds the byte diagram above from literals and fails if this doc and the code ever diverge again.
> `web/src/api/fixtures.ts` still uses the two old strings as *opaque mock ids* — that is harmless
> (they are shape-valid `[a-z2-7]{16}`) and is not an identity claim.

Notes:
* The `domain` tag is what keeps a single-file series (`foo.zip` as both series and its own book) from colliding with itself. Concretely, for the single-file series `[만화] 군계 1~25/군계(軍鷄) 01권.zip` the *same* `(root, rel)` pair yields `SeriesID = yqxql2yvlswv4qcy` and `BookID = yvtfrny77ehkt2we`. Drop the tag and those two collapse to one id for two entities.
* `relPath` is normalised before hashing (`\` → `/`, `path.Clean`, no leading slash, `"."` → `""`), so a root indexed on Windows and the same root indexed on Linux agree on every id.
* 80 bits: with 10⁶ items the birthday collision probability is ≈ 4×10⁻¹³. A `UNIQUE` constraint on the id column turns the impossible into a loud scan-log error rather than silent corruption.
* **Page identity** is the pair `(book_id, page_no)` where `page_no` is **1-based** and assigned by the natural sort of §4.7. It is *positional*, not name-based: inserting a page into an archive renumbers subsequent pages. That is the correct behaviour for a reader (progress means "how far through", not "which file"), and it is why `progress` also stores `page_count` so the UI can show a stale-progress hint if the count changed.

**Proof that progress survives an index rebuild (AC-006 / FR-STT-003).** **VERIFIED** end-to-end in the spike:
1. `book_id = f(root name, rel path)` is a pure function of two values that live in the **config file** and on the **filesystem** — neither of which the index owns.
2. Progress rows live in `user.db`, a **physically separate file** (NFR-DAT-004). `--rebuild-index` deletes `index.db`, `index.db-wal`, `index.db-shm` and nothing else.
3. The spike created `user.db` with `progress('yvtfrny77ehkt2we', last_page=42)`, deleted `index.db*`, reopened `user.db`, and read back `last_page=42` for the same id.
4. A rescan recomputes `yvtfrny77ehkt2we` from the same inputs, so the `LEFT JOIN` on the rebuilt index finds the row again.

*Corollary and warning to be documented in `shelf.example.yaml`:* renaming a root in the YAML **is** an identity change and orphans that root's progress. `shelf migrate-root --from old --to new` (phase 3) will rewrite `user.db` ids; until then the config comment must say so.

### 3.5 Index database — `<data_dir>/index.db` (derived, disposable, NFR-DAT-001)

Opened as:
```
file:<data_dir>/index.db?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)
  &_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)&_txlock=immediate
```
(NFR-DAT-003. **VERIFIED**: `PRAGMA journal_mode` → `wal`.)

```sql
-- ============================ index.db ============================
-- Derived cache. Deleting this file loses nothing. Rebuilt by a scan.

PRAGMA journal_mode = WAL;          -- NFR-DAT-003
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS meta (
    key    TEXT PRIMARY KEY,
    value  TEXT NOT NULL
);
-- rows: schema_version='1', id_version='shelf-id/1', app_version, created_at

CREATE TABLE IF NOT EXISTS roots (
    name            TEXT PRIMARY KEY,       -- config roots[].name, the identity anchor
    path            TEXT NOT NULL,          -- absolute path AS OF the last scan
    label           TEXT,
    enabled         INTEGER NOT NULL DEFAULT 1,
    series_count    INTEGER NOT NULL DEFAULT 0,
    book_count      INTEGER NOT NULL DEFAULT 0,
    page_count      INTEGER NOT NULL DEFAULT 0,
    total_bytes     INTEGER NOT NULL DEFAULT 0,
    last_scan_start INTEGER,                -- unix seconds
    last_scan_end   INTEGER,
    last_scan_error TEXT
);

CREATE TABLE IF NOT EXISTS series (
    id              TEXT PRIMARY KEY,       -- ids.SeriesID(root_name, rel_path)
    root_name       TEXT NOT NULL REFERENCES roots(name) ON DELETE CASCADE,
    rel_path        TEXT NOT NULL,          -- slash path, relative to the ROOT
    display_name    TEXT NOT NULL,          -- base name of rel_path
    sort_key        BLOB NOT NULL,          -- natsort.Key(display_name)   §4.7
    search_key      TEXT NOT NULL,          -- lowercase NFC fold of display_name
    choseong_key    TEXT NOT NULL,          -- §4.8, for FR-LIB-006
    kind            TEXT NOT NULL,          -- 'folder' | 'zip' | 'pdf'
    book_count      INTEGER NOT NULL DEFAULT 0,
    page_count      INTEGER NOT NULL DEFAULT 0,
    total_bytes     INTEGER NOT NULL DEFAULT 0,
    mtime           INTEGER NOT NULL,       -- newest mtime among its books
    added_at        INTEGER NOT NULL,       -- first time we ever saw it
    cover_kind      TEXT,                   -- 'page' | 'file' | NULL
    cover_book_id   TEXT,                   -- when cover_kind='page'
    cover_page_no   INTEGER,                -- 1-based, when cover_kind='page'
    cover_rel_path  TEXT,                   -- when cover_kind='file' (root-relative)
    status          TEXT NOT NULL DEFAULT 'ok',  -- 'ok'|'empty'|'error' — the fold below
    error           TEXT,                   -- the reason, whenever status <> 'ok'
    scan_gen        INTEGER NOT NULL,       -- generation stamp, §4.9
    UNIQUE (root_name, rel_path)
);
CREATE INDEX IF NOT EXISTS ix_series_root_sort  ON series(root_name, sort_key);
CREATE INDEX IF NOT EXISTS ix_series_sort       ON series(sort_key);
CREATE INDEX IF NOT EXISTS ix_series_mtime      ON series(mtime DESC);
CREATE INDEX IF NOT EXISTS ix_series_added      ON series(added_at DESC);
CREATE INDEX IF NOT EXISTS ix_series_bytes      ON series(total_bytes DESC);
CREATE INDEX IF NOT EXISTS ix_series_books      ON series(book_count DESC);
CREATE INDEX IF NOT EXISTS ix_series_search     ON series(search_key);
CREATE INDEX IF NOT EXISTS ix_series_gen        ON series(scan_gen);

CREATE TABLE IF NOT EXISTS books (
    id              TEXT PRIMARY KEY,       -- ids.NestedBookID(root_name, rel_path, inner_path)
    series_id       TEXT NOT NULL REFERENCES series(id) ON DELETE CASCADE,
    root_name       TEXT NOT NULL,
    rel_path        TEXT NOT NULL,          -- slash path, relative to the ROOT
    inner_path      TEXT NOT NULL DEFAULT '',
                                            -- the entry INSIDE rel_path that is this
                                            --   book, for kind='nestedzip' (§4.5.1) or the
                                            --   chapter directory for kind='nesteddir'
                                            --   ('.' = the container's top level, §4.5.2).
                                            --   '' for every other book, which is what
                                            --   keeps its id unchanged
    display_name    TEXT NOT NULL,          -- name shown in the UI
    sort_key        BLOB NOT NULL,          -- natsort.Key over the SERIES-relative path
    ord             INTEGER NOT NULL,       -- 0-based position within the series,
                                            --   materialised from sort_key so the
                                            --   API never has to re-sort
    kind            TEXT NOT NULL,          -- the source.Kind* constants: 'zip' | 'dir' |
                                            --   'pdf' | 'rar' | 'nestedzip' | 'nestedrar' |
                                            --   'nesteddir' (D-70, D-71, D-73)
    page_count      INTEGER NOT NULL DEFAULT 0,
    total_bytes     INTEGER NOT NULL DEFAULT 0,   -- sum of uncompressed page bytes
    file_size       INTEGER NOT NULL DEFAULT 0,   -- container size; 0 for kind='dir'
    file_mtime      INTEGER NOT NULL DEFAULT 0,   -- container mtime, unix seconds
    dir_fingerprint TEXT,                   -- kind='dir' only, §4.6
    content_version TEXT NOT NULL,          -- 16 hex chars, §5.7 — the cache buster
    dims_state      TEXT NOT NULL DEFAULT 'none',  -- 'none'|'partial'|'done'
    status          TEXT NOT NULL DEFAULT 'ok',
        -- 'ok' | 'error'        broken/unreadable container   (FR-IDX-010)
        -- 'encrypted'           password-protected ZIP        (FR-IDX-010)
        -- 'empty'               no qualifying pages
        -- 'unsupported'         PDF in a nopdf build, or an unknown method
    error           TEXT,                   -- human-readable, shown in the UI
    scan_gen        INTEGER NOT NULL,
    UNIQUE (root_name, rel_path, inner_path)
);
CREATE INDEX IF NOT EXISTS ix_books_series ON books(series_id, ord);
CREATE INDEX IF NOT EXISTS ix_books_gen    ON books(scan_gen);
CREATE INDEX IF NOT EXISTS ix_books_status ON books(status) WHERE status <> 'ok';

-- One row per page. 1.36M rows on the reference collection.
-- WITHOUT ROWID: the PK IS the storage order, so "give me pages 40..48 of book X"
-- is a single contiguous B-tree range scan  (AC-008).
CREATE TABLE IF NOT EXISTS pages (
    book_id       TEXT NOT NULL,
    page_no       INTEGER NOT NULL,         -- *** 1-BASED ***
    name          TEXT NOT NULL,            -- decoded display name (§4.4)
    entry_path    TEXT NOT NULL,            -- zip: full decoded entry path
                                            -- dir: path relative to the BOOK dir
                                            -- pdf: '' (empty)
    ext           TEXT NOT NULL,            -- lowercase, with dot, e.g. '.jpg'
    size          INTEGER NOT NULL,         -- uncompressed bytes
    comp_size     INTEGER NOT NULL DEFAULT 0,   -- zip only
    method        INTEGER NOT NULL DEFAULT 0,   -- zip: 0=stored, 8=deflate
    local_hdr_off INTEGER NOT NULL DEFAULT 0,   -- zip: local file header offset  ***FR-SRV-002***
    crc32         INTEGER NOT NULL DEFAULT 0,   -- zip: from the central directory (free)
    mtime         INTEGER NOT NULL DEFAULT 0,   -- dir: file mtime
    width         INTEGER,                  -- NULL until known (§5.8) — FR-VWR-004
    height        INTEGER,
    PRIMARY KEY (book_id, page_no)
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS scan_log (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    ts         INTEGER NOT NULL,            -- unix seconds
    run_id     TEXT NOT NULL,               -- groups one scan run
    level      TEXT NOT NULL,               -- 'info' | 'warn' | 'error'
    root_name  TEXT,
    rel_path   TEXT,
    message    TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_scanlog_ts  ON scan_log(id DESC);
CREATE INDEX IF NOT EXISTS ix_scanlog_run ON scan_log(run_id, id DESC);
-- retention: keep the most recent 5,000 rows, trimmed at the end of each run
```

#### `series.status` — the fold over the books (ruling E-14)

`books.status` has five values, one per container: `ok`, `error`, `encrypted`, `empty`, `unsupported`
(FR-IDX-010). `series.status` has **three**, and is a fold over them, decided once by the scanner and
stored:

| The series holds | `series.status` | `series.error` |
|---|---|---|
| no books at all | `empty` | why — e.g. "no readable books" |
| ≥ 1 book, **at least one** `ok` | `ok` | `NULL` |
| ≥ 1 book, **none** of them `ok` | `error` | the first failing book's reason |

The middle row is the important one and the reason the badge belongs on the volume: a 25-volume series
with two truncated archives is still a series you can read, so it stays `ok` and the two volumes carry
their own `error` (§4.11).

The last row is what E-14 settled. `encrypted` and `unsupported` are book-only verdicts, so before the
ruling a series whose every volume was password-protected had no defined status at all — §3.5 allowed
only `ok|empty|error` while §7.3 typed the field `ItemStatus`. It is **`error`**: a series the reader
cannot open a single page of must not present as healthy, and design.md 화면 2 requires a reason on
screen. `empty` stays reserved for "no books at all", which is arch §4.2's five text-novel directories —
a series with nothing *in* it, not a series with nothing *readable* in it.

> **Implementation status.** Two of the three rows are what `internal/scanner`'s `seriesStatus`
> already computes, and the third is complete for `error`/`encrypted`/`unsupported`. It is **not** yet
> complete for one case: when *every* book is specifically `empty`, the fold still returns `empty`. That
> is the 1.44 GB `[만화] 엔젤하트 전32권 완결.zip` of impl-plan D-10 — a ZIP of 33 nested ZIPs and no images —
> where the *book* is legitimately `empty` (nested archives are out of scope, prd §7.2) but the *series*
> reads `error` under this table, because the reader cannot open a single page of it.
>
> This table is the binding answer; the code is what lags. The gap is a one-line change in
> `internal/scanner/classify.go` (`seriesStatus`'s final `worst == StatusEmpty` branch) plus the tests and
> E2E assertions that pin today's answer. It is tabled file by file, with owners, in **impl-plan §0.3
> "E-14 follow-up work"**, and is blocking for the E2E acceptance run. D-10, §6.2's I-10 and §6.3's
> curated-subset expectation have been reconciled with this table; WP-16 owns none of the code files.

### 3.6 User database — `<data_dir>/user.db` (NFR-DAT-004, the file that must never be lost)

```sql
-- ============================= user.db =============================
-- The ONLY authored data in the system. Never touched by --rebuild-index.
-- No foreign keys into index.db: rows are allowed to reference books that do
-- not currently exist (a temporarily unplugged drive must not destroy history).

PRAGMA journal_mode = WAL;

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);   -- schema_version='2' under A-8 (was '1'), id_version='shelf-id/1',
     -- first_seen_bootstrap=<unix seconds> once a bootstrap run has COMPLETED.
     --   Its absence is the only signal that one is still needed (rule 6, E-16).

CREATE TABLE IF NOT EXISTS progress (            -- FR-STT-001
    book_id     TEXT PRIMARY KEY,                -- ids.BookID(...) — computed, not FK
    series_id   TEXT NOT NULL,                   -- denormalised for FR-STT-002 aggregation
    root_name   TEXT NOT NULL,                   -- for the migrate-root tool
    book_path   TEXT NOT NULL,                   -- root-relative; lets us rebuild ids
                                                 --   after a rename without the index
    last_page   INTEGER NOT NULL,                -- 1-based
    page_count  INTEGER NOT NULL,                -- the length the reader last saw: set by
                                                 --   the first write, then moved only by
                                                 --   an ACKNOWLEDGED one; a mismatch
                                                 --   against the index means the file
                                                 --   changed -> UI shows a hint. A 0 on
                                                 --   EITHER side is "length unknown"
                                                 --   (§4.11), not a mismatch: a broken
                                                 --   book defers the hint until it can be
                                                 --   read again (§7.3 `stale`).
                                                 --   AMENDMENT A-14 (ruling E-45): this is
                                                 --   a baseline, not a measurement of the
                                                 --   last write. An ordinary page turn
                                                 --   leaves it alone; only a PUT carrying
                                                 --   stale_seen:true and a known length
                                                 --   rebaselines it (§7.6).
                                                 --   Overwriting it on every write erased
                                                 --   the evidence the hint is derived from,
                                                 --   so the hint fired once and never again
    completed   INTEGER NOT NULL DEFAULT 0,      -- 0|1   FR-VWR-012
    started_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS ix_progress_updated   ON progress(updated_at DESC);
CREATE INDEX IF NOT EXISTS ix_progress_series    ON progress(series_id);
CREATE INDEX IF NOT EXISTS ix_progress_continue  ON progress(updated_at DESC)
    WHERE completed = 0;                          -- FR-LIB-010 "continue reading"

CREATE TABLE IF NOT EXISTS book_prefs (          -- FR-VWR-002 per-book memory
    book_id     TEXT PRIMARY KEY,
    reading_dir TEXT,        -- 'ltr' | 'rtl' | NULL (= inherit the global default)
    display_mode TEXT,       -- 'single' | 'spread' | 'vertical' | NULL
    fit_mode    TEXT,        -- 'width' | 'height' | 'original' | 'contain' | NULL
    updated_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS settings (            -- UI-mutable subset of `reader:`
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,                    -- JSON scalar
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS view_state (          -- FR-LIB-002 sticky view mode etc.
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

-- AMENDMENT A-8 (ruling E-9) — schema_version 2. Backs GET /api/series?scope=added
-- and the sidebar's 최근 추가 count (§7.5). It lives HERE, not in index.db, because
-- --rebuild-index deletes index.db and nothing else: keeping it in the index would
-- make every series "newly added" after a rebuild, which is the exact opposite of
-- what the label means (NFR-DAT-004, AC-006).
CREATE TABLE IF NOT EXISTS series_seen (
    series_id     TEXT PRIMARY KEY,              -- ids.SeriesID(...) — computed, not FK
    root_name     TEXT NOT NULL,                 -- for the migrate-root tool
    series_path   TEXT NOT NULL,                 -- root-relative; lets us re-derive the
                                                 --   id after a rename, like progress
    first_seen_at INTEGER NOT NULL               -- unix seconds. *** WRITE-ONCE ***
);
CREATE INDEX IF NOT EXISTS ix_series_seen_first ON series_seen(first_seen_at DESC);
```

#### `first_seen_at` — the write rule (A-8), stated as rules because it is a compatibility surface

1. **Written once, on first sighting, and never overwritten.** The *only* statement any code may issue
   against this column is
   ```sql
   INSERT INTO series_seen (series_id, root_name, series_path, first_seen_at)
   VALUES (?, ?, ?, ?) ON CONFLICT(series_id) DO NOTHING;
   ```
   There is no `UPDATE series_seen`, no `DELETE FROM series_seen`, and no `INSERT … DO UPDATE`, anywhere
   — enforced by a `make lint` grep sitting beside the `ud.` read-only guard of §3.7 (reject any SQL
   string that contains `series_seen` together with `UPDATE`, `DELETE` or `DO UPDATE`). `root_name` and
   `series_path` are part of the write-once row: if the series moves, the id changes and this row is not
   the one that matches any more (see rule 5).
2. **Who writes it: the scanner** (WP-08), through the `userdata` package (WP-03) — never through the
   index connection, because no transaction may span both databases (§3.7). One call per scan run per
   root, batched with the writer's normal commit cadence:
   ```go
   // internal/userdata — idempotent, batched, ON CONFLICT DO NOTHING.
   type SeriesSeen struct {
       SeriesID, RootName, SeriesPath string
       FirstSeenAt                    int64
   }
   func (db *DB) MarkSeriesSeen(ctx context.Context, rows []SeriesSeen) error
   ```
   Every series the run *lists* gets a row, including `status='empty'` and `status='error'` ones — they
   are listed in the library (§4.2), so they can be recently added.
3. **The value is the scan run's start time**, not `time.Now()` per row: one run produces one identical
   timestamp for every series it discovers, so a 32 s scan cannot straddle the window boundary and split
   a batch, and a test can assert an exact number.
4. **Not deleted by an index rebuild, and not deleted by the generation sweep.** `--rebuild-index`
   removes `index.db`, `index.db-wal`, `index.db-shm` from a hard-coded allowlist (§3.7) and never opens
   `user.db`. §4.9's `DELETE FROM series … WHERE scan_gen < ?` is an **`index.db`** statement and must
   not grow a `user.db` counterpart. Rows for series that no longer exist are deliberately kept, exactly
   like orphaned `progress` rows: a temporarily unplugged drive must not rewrite history. A row is ~80
   bytes; 10⁴ dead series cost under 1 MB. (A `shelf prune-user-db` is phase 3, and must be an explicit
   operator action, never automatic.)
5. **A series that vanishes and later returns keeps its original `first_seen_at`.** The row survives the
   absence; on its return the scanner recomputes the same `series_id` from the same `(root name,
   root-relative path)` (§3.4), the `ON CONFLICT DO NOTHING` fires, and 최근 추가 does **not** light up
   again. Restoring a backup, remounting a drive, or a full rebuild are therefore all invisible here —
   which is the entire point of the amendment.
   The corollary is the same one §3.4 already states for progress: **renaming or moving a series is an
   identity change.** The new path derives a new `series_id`, gets a fresh row, and the series legitimately
   appears as newly added — the same event that orphans its reading progress. Nothing new is being risked;
   it is one more reason `shelf migrate-root` must rewrite `series_seen` alongside `progress`.
6. **Cold start.** The first scan on a fresh install would otherwise stamp an entire pre-existing
   collection as "added today" and show 963 in a badge that means "new" — the same class of visibly
   wrong number that E-9 was raised about. So a run against a `user.db` whose `meta.first_seen_bootstrap`
   is **unset** is a **bootstrap run**: every row it writes uses
   `first_seen_at = min(run_started_at, series.mtime)` — `series.mtime` being the newest mtime among the
   series' books (§3.5), i.e. the best evidence the filesystem offers about when the material arrived.
   A 2012 series therefore starts outside the window, and a series copied in yesterday starts inside it.
   *That `min()` is the one place A-8 goes beyond the literal text of ruling E-9, which fixes only "set
   once on first sighting". It was flagged in-doc rather than hidden, and **ruling E-18 accepted it**:
   without it, day one stamps the whole pre-existing collection as 최근 추가, which is the failure E-9
   exists to prevent. It stays deliberately isolated to a single `min()`, so deleting it is a one-line
   change if the human ever prefers "everything is new on day 1".*

   **The marker alone decides it (ruling E-16).** This rule used to require `series_seen` to be empty
   *as well*, and that extra condition could not be satisfied by the very run that needed it. A bootstrap
   run writes each root's rows as it finishes that root; it stamps the marker only at the very end, and
   only if it actually did the whole job — a run that was cancelled, that was given `--root` or a
   targeted rescan, that failed to reach a root, or that lost a batch to a locked `user.db` deliberately
   leaves the marker unset so the next run can finish (rule 6's own safety property). But such a run has
   usually already committed *some* rows, so the emptiness precondition was false, and the recovering run
   was not treated as a bootstrap: it stamped every remaining series with its own wall clock and flooded
   최근 추가 with a decade-old library. On a first scan of 414 GB, being interrupted once is the normal
   path. The withheld marker is now the whole signal:
   `FirstSeenBootstrapNeeded() == (meta.first_seen_bootstrap is unset)`.
   A `user.db` restored from a backup that predates A-8 has no marker and correctly bootstraps; one whose
   bootstrap completed has the marker and correctly does not, however many rows have since come and gone.

   Once the run does finish, it writes `meta.first_seen_bootstrap = <run_started_at>` — write-once, like
   the rows it describes — and every later run uses `run_started_at` unconditionally.
7. **A missing row is not an error.** A series with no `series_seen` row (a `user.db` restored from an
   older build, or the window between indexing and the next `MarkSeriesSeen` commit) is simply excluded
   from `scope=added` — see §7.5. Under-reporting is the safe direction, and the next scan fixes it.
8. **Schema version.** Adding this table takes `user.db` `meta.schema_version` from `'1'` to `'2'`, as an
   **append-only migration rung** (`migrations = […, {to: 2, sql: schemaV2}]`). `user.db` is authored data:
   the rung may only `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS`. A file at version 2
   opened by a build that only knows version 1 still refuses to start (`ErrSchemaTooNew`, §6.3) — which is
   correct and is why the rung is additive.

### 3.7 Connecting the two databases

Physical separation is a hard requirement, but the series list needs progress in the same query (FR-LIB-003, FR-LIB-004 "recently read" ordering). We `ATTACH` `user.db` onto **every** index connection via the driver's connection hook. **VERIFIED**: with `modernc.org/sqlite`'s `RegisterConnectionHook`, 64 concurrent goroutines hammering an 8-connection pool executed cross-database joins with **0 failures**.

```go
sqlite.RegisterConnectionHook(func(c sqlite.ExecQuerierContext, dsn string) error {
	if !isIndexDSN(dsn) { return nil }
	_, err := c.ExecContext(context.Background(),
		`ATTACH DATABASE `+sqliteQuote(userDBPath)+` AS ud`, nil)
	return err
})
```

Rules that keep the separation real:
* **No transaction ever spans both databases.** SQLite offers no two-phase commit across attached WAL databases, and we do not need it: index writes come only from the scanner, and every user write goes through the dedicated `userdata` handle.
  *Amended by A-8:* the scanner is now a `user.db` writer too — it calls `userdata.MarkSeriesSeen` for `series_seen.first_seen_at` (§3.6). That call is a **separate transaction on the `userdata` handle**, never part of the index writer's transaction, so the rule above is unchanged. It is also the only `user.db` write the scanner may ever make, and it is write-once and idempotent, so a crash between the two commits is harmless in both orders: an index row without a `series_seen` row is simply not "recently added" until the next run, and a `series_seen` row without an index row is invisible to every query.
* `--rebuild-index` deletes `index.db*` **only**, and the file list is a hard-coded allowlist, not a glob. It never opens `user.db`, which is what makes `first_seen_at` (and progress) survive it.
* `ud.` is read-only from the index connection by convention; user writes go through the dedicated `userdata` handle. Enforced by a `make lint` check that no SQL string containing `ud.` also contains `INSERT`/`UPDATE`/`DELETE`.

Connection pool sizing: index DB `SetMaxOpenConns(max(4, NumCPU))`, `SetMaxIdleConns(4)`; user DB `SetMaxOpenConns(4)`. The scanner uses **one dedicated writer connection** it owns exclusively (§4.9), so reader/writer contention never appears.

---

## 4. Scanner

### 4.1 Pipeline

```
        per root (sequential over roots, parallel inside)
        ───────────────────────────────────────────────────────────────
        walkRoot ──> classify ──> [ scan_workers ]──> results chan ──> writer
     (1 goroutine)  (in-line)      read central dirs   (buffered 512)  (1 goroutine,
      os.ReadDir                   / readdir / pdf                      owns the write
      40 ms for 965                page count                           connection)
      direct children
      children                     346 archives/s @16
                                        │
                                        └──> coverQ (priority) ──> [ thumb_workers ]
                                             pageQ   (lazy)         67 covers/s @16
```

* **One walker goroutine per root.** Directory enumeration is negligible: `filepath.WalkDir` over the entire 11,157-archive tree took **39 ms**.
* **`scan.workers` archive readers.** I/O-bound. **VERIFIED** throughput: 4 workers = 147 archives/s (cold page cache), 16 workers = **346 archives/s** over the full 11,157 archives in **32.3 s**. Default `min(8, max(2, NumCPU/2))`.
* **Exactly one writer goroutine** owns the index write connection and commits in transactions of 200 books (or every 2 s, whichever comes first). This removes SQLite write contention by construction rather than by retry.
* Backpressure: the results channel is bounded (512); readers block when the writer falls behind, which bounds memory.
* The scan honours `context.Context`; `POST /api/scan/cancel` cancels it and the writer commits what it has.

**NFR-PRF-004** (incremental scan of 1,000 series in ≤30 s): the reference collection is 963 series / 11,157 archives and a **full cold** scan takes 32.3 s. An incremental scan skips the central-directory read for every unchanged archive, leaving only `stat` calls — comfortably inside budget.

### 4.2 Series/book classification — prd 2.2, implemented exactly

For each **direct child `C`** of an enabled root `R` (definition of "series" per prd 1.3):

```
if C is a regular file:
    ext(C) in {.zip, .cbz}          -> Series{kind:"zip"}, exactly one Book{kind:"zip", rel: C}
    ext(C) == .pdf                  -> Series{kind:"pdf"}, exactly one Book{kind:"pdf", rel: C}
    otherwise                       -> ignored; one scan_log(level=info) row
                                       (accounts for the .rar and .DS_Store at top level)

if C is a directory:
    Series{kind:"folder"}; books = collectBooks(C, depth=0)
```

`collectBooks(D, depth)` — this is the single function that realises every row of the prd 2.2 table, including "mixed":

```
1. entries := readdir(D) filtered by the exclusion rules of §4.5
2. partition into
       A = archives (.zip, .cbz)
       P = pdfs     (.pdf)
       I = loose image files (ext in the FR-IDX-011 set)
       S = subdirectories
3. sub := []              // books contributed by subdirectories
   if depth < scan.max_depth:
       for each s in S (natural-sorted):  sub = append(sub, collectBooks(s, depth+1)...)
4. books := []
   for each a in A (natural-sorted):  books += Book{kind:"zip", rel: a}
   for each p in P (natural-sorted):  books += Book{kind:"pdf", rel: p}
   books += sub
5. handle the loose images I:
       if len(I) == 0:
           (nothing)
       else if len(books) == 0:
           // prd 2.2 row 3: "images directly inside the folder"
           //   -> the directory ITSELF is one book
           books += Book{kind:"dir", rel: D, pages: I}
       else if len(I) <= scan.cover_max_loose_images:
           // *** the real-world "mixed" case: 47/672 directories are
           //     "N archives + exactly one cover image".  These are COVERS. ***
           coverCandidates = I         // consumed by §4.10, never a book
       else:
           // genuinely mixed: loose pages alongside other books
           books += Book{kind:"dir", rel: D, pages: I, name: D.base + " (loose pages)"}
6. return books
```

Consequences, checked against the real tree:

| prd 2.2 row | Real count | Result |
|---|---|---|
| folder containing many ZIPs | 592 | one book per ZIP |
| folder of subfolders, each holding images | 26 (+ nested cases) | one book per subfolder, recursively to `max_depth` |
| folder with images directly inside | 1 | the series is a single book |
| a single ZIP file | 291 | series == its own single book |
| a single PDF file | (0 at top level; 12 nested) | series == its own single book |
| **mixed** | 47 | every element becomes its own book and they are merged into one series — with the one refinement that ≤3 loose images beside real books are covers, not a one-page book |

`[만화] 기동전사 건담 시리즈` (8 sub-directories, each holding 6–23 archives, plus one archive at the top) flattens into **one series with ~60 books**, because prd 1.3 defines a series as *the root's direct child*. Book display names carry the sub-path so the UI can still show the grouping: `크로스본 건담 / 크로스본 건담 01권.zip`. *This is OQ-5 in §12 if a different grouping is wanted.*

A series that ends with zero books gets `status='empty'` (the 5 text-novel directories). It is still listed — silently swallowing 5 directories the user can see in their file manager is worse than showing them greyed out with a reason.

### 4.3 ZIP central-directory reading (FR-IDX-002, FR-IDX-009)

We ship `internal/archive/zipidx`, a purpose-built reader. Its whole reason for existing is that **`archive/zip` does not expose the local-header offset**, which FR-SRV-002 requires us to persist (see §1.2 for the 10-minute-vs-32-second measurement that settled this).

Algorithm:

1. **Locate the EOCD.** Read a **1 KiB tail** first and scan backwards for `0x06054b50`, validating that the record's comment-length field is consistent with the remaining bytes. Only if that fails, re-read a **65,557-byte** tail (max comment + 22) and scan again. **VERIFIED**: this two-step keeps the whole scan at **2.0 `ReadAt` calls and 9.4 KB per archive**.
2. **ZIP64 (FR-IDX-009).** If the entry count is `0xffff`, or the CD size/offset is `0xffffffff`, read the 20-byte ZIP64 EOCD locator immediately before the EOCD (`0x07064b50`), follow it to the ZIP64 EOCD (`0x06064b50`), and take the 64-bit entry count, CD size and CD offset from there.
3. **One sequential `ReadAt` of the entire central directory** into a single buffer, then parse records in place.
4. **Per record (`0x02014b50`, 46-byte fixed part)** capture: general-purpose flags (bit 0 = encrypted, bit 11 = UTF-8), method, DOS mtime, CRC-32, compressed size, uncompressed size, **relative offset of local header**, and the **raw undecoded name bytes**.
5. **ZIP64 extended-information extra field (`0x0001`).** Its members are present *only* for the 32-bit slots that held `0xffffffff`, **in the fixed order** uncompressed → compressed → local-header-offset → disk number. Parsed exactly that way.
6. Malformed record, bad signature, or a length that would run past the buffer → return the entries parsed so far **plus** an error. The caller records `books.status='error'` and continues (FR-IDX-010).

**Differential validation.** `zipidx` is checked against `archive/zip` over the whole collection: identical entry count, identical names, identical method/CRC/sizes, and — on a sampled subset — `DataOffset()` agreeing byte-for-byte. **VERIFIED** on the first sample archive (104 entries, exact parity) and over all 11,157 archives for the error verdict: **the same 9 archives fail in both implementations, with no case where one succeeds and the other does not.** This differential test stays in the suite permanently (§10.1).

Never at any point is an entry payload read during a scan.

### 4.4 Entry-name decoding — the CP949 rule (FR-IDX-008, AC-002)

The prd says "if the UTF-8 flag is absent, reinterpret as CP949". The measured behaviour of the decoder forces one refinement: **`korean.EUCKR.NewDecoder()` never returns an error** — it silently emits U+FFFD for unmappable bytes. **VERIFIED**: `transform.String` returned `err=<nil>` for `"\xff\xfe.jpg"` while producing `"��.jpg"`. Checking `err` is therefore useless; we must inspect the output.

The rule, precisely:

```go
// internal/kenc/kenc.go
//
// utf8Flag is general-purpose bit 11 (0x0800) of the ZIP entry.
func DecodeEntryName(raw []byte, utf8Flag bool) (name string, enc string) {
	// 1. The producer declared UTF-8. Trust it; repair only if it lied.
	if utf8Flag {
		if utf8.Valid(raw) { return string(raw), "utf-8" }
		return strings.ToValidUTF8(string(raw), "�"), "utf-8-invalid"
	}

	// 2. No flag, but the bytes are already valid UTF-8 -> it IS UTF-8.
	//    This test MUST come first. A UTF-8 Korean name run through the CP949
	//    decoder can produce plausible-looking mojibake ("한글.jpg" -> "?쒓?.jpg"),
	//    so guessing CP949 unconditionally corrupts flagless-but-UTF-8 archives.
	//    Pure-ASCII names take this branch too, which is correct and free.
	if utf8.Valid(raw) { return string(raw), "utf-8" }

	// 3. Not valid UTF-8 -> decode as CP949/EUC-KR (x/text's EUCKR is the
	//    Unified Hangul Code superset, i.e. CP949).
	dec, _, _ := transform.Bytes(korean.EUCKR.NewDecoder(), raw)   // err is always nil
	if !bytes.ContainsRune(dec, '�') { return string(dec), "cp949" }

	// 4. CP949 produced replacement characters -> neither encoding fits.
	//    Keep the bytes lossily so the page is still readable, and flag it.
	return strings.ToValidUTF8(string(raw), "�"), "unknown"
}
```

**Why step 2 is not optional** — measured on the collection:

| Population | Count |
|---|---|
| Entry names containing non-ASCII bytes | 16,081 |
| ...of which carry the UTF-8 flag | 1,451 |
| ...of which do **not** carry the flag | **14,630** |
| ...of those 14,630, how many are nevertheless valid UTF-8 | **0** |
| ...of those 14,630, how many yield U+FFFD under CP949 | **0** |

So on this collection step 3 fires cleanly 14,630 times out of 14,630 and AC-002 holds. Step 2 costs nothing here but protects the many flagless-yet-UTF-8 archives produced by modern tools. (Across *all* 33,456 flagless entries, 18,826 were valid UTF-8 — those are the pure-ASCII `001.jpg` names, which step 2 also handles correctly.)

The chosen encoding is recorded per book so the UI can surface it, and `pages.name` stores the **decoded** string. Page access itself never needs the name again — it uses offsets — but the raw bytes are kept on `archive.Entry.RawName` for the archive-level pass below.

#### 4.4.1 Where the per-entry rule stops: Shift_JIS (extends FR-IDX-008)

Step 3 assumes the only legacy encoding in the collection is CP949. A survey of **all 11,196 indexed ZIPs (1.35 M entry names)** — not the 508-archive sample §4.4 is measured on — found that assumption wrong for four archives, and wrong in a way the per-entry rule *cannot* detect:

| Archive population (all 11,196) | Count |
|---|---|
| Nothing but UTF-8 or ASCII names | 6,757 |
| Read completely by CP949, and by nothing else | 1,871 |
| Read completely by **Shift_JIS** and not by CP949 | **4** |
| Read completely by **both** | 2,554 |
| Read by neither | 1 |

The four are the three `[文月晃] 海の御先` volumes and `天上天下 20.zip`, 728 entry names in total. The last one is the instructive case: **160 of its 189 flagless names decode as CP949 with zero U+FFFD, and all 160 readings are wrong** — `"밮밮-20-001.jpg"` is the CP949 misreading of `"天天-20-001.jpg"`. Nothing about that one record gives it away. What does is the *other* 28 names, which CP949 cannot read at all and Shift_JIS can.

So the decision is made **once per archive**, in `zipidx.resolveArchiveNames`, after the whole central directory has parsed:

1. If no name came back `unknown`, stop. CP949 read everything, and nothing is decoded twice — this is the path 11,192 of 11,196 archives take, at zero cost.
2. Otherwise hand every legacy name (`cp949` + `unknown`; UTF-8 ones are not evidence) to `kenc.ArchiveFallback`.
3. If it names an encoding, re-decode **all** of them in it — including the ones CP949 "read", which is the whole point.

`ArchiveFallback` accepts Shift_JIS only when *every* name decodes without substituting **and** the result contains no halfwidth katakana **and** contains at least one fullwidth kana/kanji. The middle condition is what keeps Korean archives safe, and it is not a heuristic reach: Shift_JIS reads plenty of Korean bytes happily — `CP949("한글.jpg")` comes back as `"ﾇﾑｱﾛ.jpg"` — because the CP949 lead byte of an ordinary Hangul syllable (0xB0–0xC8) is exactly Shift_JIS's single-byte halfwidth range (0xA1–0xDF). Measured: the 4 Japanese archives contain **0** halfwidth katakana across all 794 names and 11,175 fullwidth kana/kanji; every Korean name tried the same way yields 4–15 halfwidth katakana and essentially no fullwidth CJK.

Shift_JIS is the **only** candidate, deliberately. Each extra candidate is another chance to misread one of the 1,871 Korean archives, and the 2,554 that both codecs read cleanly are settled by CP949 winning by default — verified safe, since in none of them does the Shift_JIS reading contain kana while the CP949 reading lacks Hangul.

The one archive neither codec reads is `BM 넥타 09.zip`, whose 95 names are not mis-encoded but **damaged**: the leading bytes of UTF-8 NFD jamo sequences are missing (`"BM _\x82\xe1\x85\xa6_…"`). No decoder recovers that. It stays `unknown`, which is the honest answer — the book still opens and its pages still serve.

### 4.5 Exclusion rules (FR-IDX-006)

A ZIP entry or directory child is **excluded from the page list** when any of these hold. Applied to the *decoded* name.

| Rule | Test |
|---|---|
| Directory entry | name ends with `/`, or the external-attributes directory bit is set, or `DirEntry.IsDir()` |
| macOS resource forks | path equals `__MACOSX` or starts with `__MACOSX/` or contains `/__MACOSX/`; also any base name starting with `._` |
| Hidden files | base name starts with `.` **and is not itself a page** — see below |
| Hidden directories | any *directory* component of the path starts with `.` (`.git/`, `.thumbnails/`) |
| System junk | base name case-insensitively equals `.DS_Store`, `Thumbs.db`, `desktop.ini`, `Desktop.ini` |
| Zero-byte entries | `size == 0` |
| Non-image extension | `ext` not in `{.jpg .jpeg .png .gif .webp .bmp .avif}` (FR-IDX-011). `.tif/.tiff` are decoded if present but are **not** advertised as supported. |
| Encrypted entry | general-purpose bit 0 set → the whole **book** becomes `status='encrypted'` (FR-IDX-010) |
| User globs | matches any `scan.exclude_globs` pattern against the root-relative slash path |

`Thumbs.db` alone accounts for **125 excluded entries** in a 508-archive sample; the rules are load-bearing, not theoretical.

**The hidden-file rule is narrower than "starts with a dot"** (FR-IDX-006 요구사항 "숨김 파일"). A ZIP entry has no hidden attribute — a leading dot only means "hidden" by a filesystem convention the entry was never subject to — and every artefact that convention is really aimed at is already caught by a rule that names it (`._*` forks, `.DS_Store`). What was left over was costing a whole book: `엽기인 Girl 스나코 26권.zip` holds 80 pages named `.▶스나코_26권◀_Scan11192010_193728.jpg`, the rule dropped all 80, and the volume indexed as `비어 있음` with `page_count=0`.

So a dot-name with a supported image extension and a non-empty stem is a page; a dot-name without one is still hidden, as is anything under a dot-prefixed **directory**. Measured across all 11,196 indexed ZIPs and the whole filesystem tree, this changes what is listed for **exactly one book**: it is the only archive with a dot-prefixed image entry, there is no archive with an image under a dot-prefixed directory, and there is no dot-prefixed image file anywhere in the directory books either.

#### 4.5.1 Nested archives — a container of volumes (D-70, supersedes D-07's first clause)

45 books in the collection are not books. They are containers: a ZIP whose entries are all more ZIPs, 623 volumes and 16.9 GB in total, with `겟 벡커스 1~39완.zip` (1.4 GB, 39 volumes) the largest. Each indexed as one book with `status='empty'` and no pages, which was a true statement about a library the reader could not open.

Each inner archive is now its own 권, `books.kind='nestedzip'`, identified by `(root_name, rel_path, inner_path)` — the container plus the entry name.

**How it is read, and why nothing is extracted.** `internal/archive/nested` presents an inner archive as an `io.ReaderAt`, which is what every layer above already speaks. The existing `zipidx` then indexes it and streams its pages *unchanged*: the exclusion rules, the natural page order and the CP949/Shift_JIS name decoding are literally the same code an ordinary book gets. There is no second format, no temporary file, and no cache directory to size or evict.

| Inner archive | Count | How it is served |
|---|---|---|
| `stored` | 13 | `io.SectionReader` over the outer file — true random access, zero cost |
| `deflate` | 610 | one inflate stream, advanced by discarding; the last 2 MiB kept once inflated, which is where the central directory is |

The deflated case is far cheaper than it sounds because the entries are already-compressed JPEGs: **16.9 GB uncompressed is stored in 16.9 GB, a ratio of 1.0000**, with 618 of the 623 above 0.99. Measured on `겟 벡커스`: ~500 ms to index a 107-page volume, ~13 ms to stream a mid-volume page.

**What it costs an ordinary book: nothing.** Expansion is attempted only when a book has produced no pages of its own — the branch that was about to write `비어 있음` anyway. A book with pages is never reopened, and an unchanged book is never opened at all (`unchanged` still skips on `(size, mtime)`, and `status != 'ok'` still forces a re-read, which is what makes the 45 pick themselves up with no migration).

**Bounds.** Only one level: a volume inside a container is not itself opened looking for containers. Only `.zip`/`.cbz`: prd §7.2 keeps RAR/7z out and this build cannot read them, so listing them would produce books that cannot open — `사모님은 학생회장.zip` (7 ZIPs + 8 RARs) yields its 7 readable volumes rather than nothing, and a container holding *only* RARs stays `empty`, exactly as D-07 says.

The container itself stops being a book. A 권 list reading "39 volumes, plus one broken volume, which is the thing holding the other 39" is a worse answer than the one the reader asked for.

#### 4.5.2 Chapter directories — a container of 권 that are folders (D-73)

484 of the collection's 11,153 archives (4.3%) hold **nothing but per-chapter directories**: `여자친구 만들고파! 01~08권.zip` is 842 pages in eight of them, `배틀로얄 1~15 [완결].zip` is 1,540 in fifteen, and `암살교실 1~180화.zip` is 3,534 in 182 directories literally named 화. 279,541 pages are in this shape. Each indexed as **one** book — a 842-page 권 that no reader can navigate and no 권 list can describe.

This is not a new rule. prd §2.2 row 2 already says that a *folder* whose sub-folders hold images is one 권 per sub-folder; `collectBooks` has done that since wave 1. An archive of exactly that tree behaved differently only because nothing had ever looked inside one for directories. §4.5.2 makes the two agree.

Each directory is its own 권, `books.kind='nesteddir'`, identified by `(root_name, rel_path, inner_path)` — the container plus the directory path. The kind does not name a format, because a directory has no format: the reader comes from the *container's* extension, so a chapter of a `.rar` works by construction.

**The partition.** `source.Chapters` runs over the page list the book has already produced — no extra read, no payload:

1. strip the longest directory prefix every page shares (a container packed with one wrapper folder is one volume, not one chapter);
2. group by the first path element after it; pages left at the top level form the group `.`;
3. **two or more directory groups** → split; otherwise the book is unchanged.

It is **total**: every page belongs to exactly one 권 and none is dropped. That is why the stray cover image beside 29 volume directories in `야와라! - YAWARA! (1-29).zip` becomes a 권 of its own — `inner_path='.'`, named `… (loose pages)`, sorted first, which is also what lets the cover ladder (§4.10 rule 3) pick it up. `.` rather than `''`: an empty inner path is what every non-nested book has, so the two would collide on one id.

**Refused, by measurement.** A wrapper directory *and* loose pages together is the one shape where "which directory is this page's chapter" has two defensible answers; no archive in the collection is it, so it stays one book. Paths separated by `\` are not split — the prefixes would match no entry name. Only a top-level container splits: a chapter inside a nested volume needs two inner paths and `books.inner_path` is one column.

**What it costs.** Detection is one pass over a slice. Indexing is one central-directory read per chapter, through the handle the pool already holds, because each chapter goes back through `indexUnit` — which is where FR-IDX-003 lives, so an unchanged chapter is recognised by the container's `(size, mtime)` and its page rows are never touched. Deriving the chapters in place instead would have rewritten every page row of a 6,097-book library on every scan.

**Migration.** A container already recorded `status='ok'` is skipped by §4.6/E-39 on an incremental scan, so an existing index only splits on a **full** scan (`--rebuild-index`, or `POST /api/scan {"full": true}`). The 재스캔 button does not send it — see the E-39 note below.

Reading progress recorded against the *container* does not move to a chapter, and cannot: the book it was about — one 842-page volume — no longer exists, and there is no page number in it that means anything in the eight books that replaced it. The `user.db` row is orphaned rather than destroyed (§3.6: nothing there is ever rewritten by a scan), so it costs the reader their place in that one archive and nothing else. `index.db` is derived and disposable; `user.db` is not, and this is the one case where that distinction is visible to a reader rather than only to an operator.

### 4.6 Incremental scan (FR-IDX-003)

> **Amended by E-39 (BINDING — 사용자 서명 2026-08-06; `docs/decisions.md` carries the text).** The first bullet below is new
> and it widens this section: **the skip applies only to a book whose recorded `books.status` is `'ok'`.**
> Any other status is re-examined even when `(size, mtime)` have not moved. What this replaces is a rule
> that skipped on timestamps alone with one narrow exception for `'unsupported'` — an exception that was
> right for the wrong scope. `'empty'` and `'error'` are not properties of the file either: a listing read
> from a handle the pool still had open on a since-replaced inode (§5.2) and a transient I/O failure both
> write a verdict the bytes on disk do not support, and both were then unreachable for ever, because the
> `(size, mtime)` they were recorded with are the file's real ones. Measured on the real library: `궁 24.zip`
> was repaired on disk and stayed `비어 있음` through every later 재스캔; `full: true` was the only escape and
> the 재스캔 button does not send it. **The ruling was signed, so this is now the section's contract** —
> the follow-up ruling that a refusal would have forced (making the 재스캔 button send `{"full": true}`)
> is not needed.

* **Any book whose recorded `status` is not `'ok'`** — never skipped. It costs one open plus one
  central-directory read per non-ok book per scan (57 of 11,261 books in the real collection: 0.5%), reads no
  entry payload (FR-IDX-002), and is exactly the cost the `'unsupported'` exception already accepted. *(E-39.)*
* **Archive and PDF books** — skip entirely when `stat.Size() == books.file_size && stat.ModTime().Unix() == books.file_mtime`. The central directory is not read, `pages` rows are left untouched, `scan_gen` is stamped forward. This is the whole of NFR-PRF-004.
* **Directory books** — `stat` on a directory does not change when a nested file changes, so we compute a **fingerprint** over one `os.ReadDir` of the book directory: FNV-1a-64 over the natural-sorted tuples `(name, size, mtimeUnix, isDir)` of its *direct* children, rendered as 16 hex chars. Unchanged fingerprint → skip page re-enumeration.
* **Series level** — the same fingerprint over the series directory's direct children decides whether `collectBooks` re-runs. Unchanged → we still descend to let each book run its own cheap check, because a nested archive can change without the series directory's fingerprint moving.
* `--rebuild-index` / `POST /api/scan {"full": true}` bypasses every skip.

### 4.7 Natural sort (FR-IDX-007)

Two representations that are required to agree:

* `natsort.Compare(a, b string) int` — used in Go.
* `natsort.Key(s string) []byte` — a byte string whose `bytes.Compare` order is identical, stored in `series.sort_key` / `books.sort_key` so SQLite's default BINARY collation does the ordering with no user-defined function.

A property test asserts `sign(Compare(a,b)) == sign(bytes.Compare(Key(a),Key(b)))` over a large generated corpus plus every real name in the collection.

**Comparison algorithm.** Walk both strings simultaneously in *chunks*; a chunk is either a maximal run of ASCII digits or a single non-digit rune.

1. **Both chunks are digit runs.** Strip leading zeros from each. Compare by **length of the stripped run first** (that is the numeric comparison, and it cannot overflow — verified against a 22-digit input), then lexicographically. If the numeric values are equal but the **leading-zero counts differ**, the one with *fewer* leading zeros sorts first (`1 < 01 < 001`) — an arbitrary but total and stable choice, so zero-padding never makes the order nondeterministic.
2. **One is a digit, the other is not.** Digits sort **before** non-digits (`1권` before `가`). This keeps numbered volumes ahead of prose-named extras.
3. **Neither is a digit.** Compare a folded key: ASCII → `unicode.ToLower`; fullwidth Latin `U+FF21–FF3A` / `U+FF41–FF5A` → lowercase ASCII; fullwidth digits `U+FF10–FF19` → ASCII digits; anything else → `unicode.ToLower`. Hangul, Hanja and Kana fall through to raw code-point order, which for Hangul syllables `U+AC00–D7A3` **is** dictionary order — so `가 < 나 < 다` comes out right for free. If the folded keys tie but the raw runes differ, compare raw runes so the result is a total order.
4. Prefix exhausted → the shorter string sorts first.

**Key encoding** (`natsort.Key`), which reproduces the above under plain `memcmp`:

```
for each chunk, left to right:
    digit run  -> 0x01 ‖ two ASCII hex chars for len(stripped) ‖ stripped digits
                       (0x01 is below every digit 0x30 and every letter, which is
                        what makes rule 2 fall out automatically)
    non-digit  -> the UTF-8 encoding of foldKey(rune)
finally append 0x00 ‖ the original string
    (the suffix breaks ties from case-folding and leading zeros, guaranteeing a
     total order identical to Compare; 0x00 is legal inside a SQLite BLOB)
```

**VERIFIED** outputs (real inputs from the collection in bold):

```
[10.jpg 1.jpg 2.jpg 20.jpg 3.jpg]              -> [1.jpg 2.jpg 3.jpg 10.jpg 20.jpg]
[001.jpg 10.jpg 1.jpg 01.jpg 002.jpg 2.jpg]    -> [1.jpg 01.jpg 001.jpg 2.jpg 002.jpg 10.jpg]
[page-9 page-10 page-1 page-100]               -> [page-1 page-9 page-10 page-100]
[vol 2 ch 10, vol 2 ch 2, vol 10 ch 1, vol 1 ch 30]
                                               -> [vol 1 ch 30, vol 2 ch 2, vol 2 ch 10, vol 10 ch 1]
**[군계 10권 군계 2권 군계 1권 군계 25권]**       -> **[군계 1권 군계 2권 군계 10권 군계 25권]**
**[강철의 연금술사 27, ...3, ...10]**            -> **[강철의 연금술사 3, 10, 27]**
[b.jpg A.jpg a.jpg B.jpg]                      -> [A.jpg a.jpg B.jpg b.jpg]
[999999999999999999999 1000000000000000000000 2]  -> [2, 999…9, 1000…0]     (no overflow)
[01권 (완).zip 1권.zip 10권.zip 2권 (2).zip]     -> [1권.zip 01권 (완).zip 2권 (2).zip 10권.zip]
[cover.jpg 0001.jpg z.jpg 가.jpg]               -> [0001.jpg cover.jpg z.jpg 가.jpg]
```

Applied to: page order inside a book, book order inside a series (`books.ord` is materialised from it), and `sort=name` in `GET /api/series`.

### 4.8 Korean initial-consonant search (FR-LIB-006)

`series.choseong_key` is computed at scan time:

```
for each rune r:
    AC00 <= r <= D7A3  ->  choseong[(r - 0xAC00) / 588]   from "ㄱㄲㄴㄷㄸㄹㅁㅂㅃㅅㅆㅇㅈㅉㅊㅋㅌㅍㅎ"
    3131 <= r <= 314E  ->  r                              (already a jamo)
    otherwise          ->  unicode.ToLower(r)
```
**VERIFIED**: `강철의 연금술사` → `"ㄱㅊㅇ ㅇㄱㅅㅅ"`, `군계` → `"ㄱㄱ"`, `20세기소년` → `"20ㅅㄱㅅㄴ"`, `Attack on Titan` → `"attack on titan"`. Query `ㄱㅊ` matches the first; `ㄱㄱ` matches the second.

Search behaviour for `GET /api/series?q=…`: if the query consists only of jamo/ASCII/space, match `choseong_key LIKE '%'||q||'%'` **OR** `search_key LIKE '%'||q||'%'`; otherwise match `search_key` only. With ~10³–10⁴ series a table scan is well under 10 ms, so no FTS5 index is introduced in v1.

### 4.9 Generation stamping and deletion

Every scan run gets `run_id` (UUIDv4-ish random hex) and a monotonic `scan_gen` (`meta.scan_gen + 1`). Every row the run touches gets stamped. At the end of each **root**, in one transaction:

```sql
DELETE FROM books  WHERE root_name = ?1 AND scan_gen < ?2;
DELETE FROM series WHERE root_name = ?1 AND scan_gen < ?2;   -- cascades
```
Both statements are **`index.db` only**. There is deliberately no `user.db` counterpart: `progress`,
`book_prefs` and (A-8) `series_seen` rows for vanished items are kept forever — see §3.6 rule 4.

Rows for disabled or unreachable roots are never swept — an unmounted drive must not silently erase a third of the library. If a root's `path` does not exist or `os.ReadDir` fails at the top level, the run **aborts that root** with `roots.last_scan_error` set and deletes nothing.

### 4.10 Cover selection (FR-THM-003, FR-LIB-008)

Per series, in order:

1. A loose image in the series directory whose base name (case-insensitive, extension removed) matches `^(\[)?(cover|folder|poster|thumb|thumbnail)(\])?$` or **contains** `cover` → `cover_kind='file'`. Catches `[cover].jpg` and `강철의 연금술사 00 Cover.jpg` from the real tree.
2. Otherwise, if `collectBooks` produced exactly one cover candidate (§4.2 step 5) → `cover_kind='file'` with that file.
3. Otherwise **page 1 of the first book by `ord`** whose `status='ok'` → `cover_kind='page'`. This is prd's literal "first page of the first volume".
4. Otherwise `cover_kind=NULL` → `GET /api/series/{sid}/cover` returns **404**, and the frontend renders the name-text placeholder required by FR-LIB-008. The API never fabricates an image.

### 4.11 Error isolation (FR-IDX-010)

Every per-book unit runs inside a function that recovers panics and converts every failure into a status, never a scan abort:

| Failure | `books.status` | `books.error` (example) |
|---|---|---|
| EOCD not found / truncated | `error` | `zip: end of central directory not found` |
| Truncated central directory | `error` | `zip: truncated central directory at entry 812` |
| Any entry has GP bit 0 | `encrypted` | `zip: archive is password-protected` |
| Unsupported compression method | `error` | `zip: unsupported compression method 14 (LZMA)` |
| No qualifying pages after exclusions | `empty` | `no supported image entries` |
| PDF in a `nopdf` build / `pdf.enabled=false` | `unsupported` | `PDF support is not enabled in this build` |
| `os.Open` fails (permissions, vanished) | `error` | the `*PathError` message |

Each also emits one `scan_log` row at `warn`. Observed rate on the reference collection: **9 of 11,157 archives (0.08 %)**, all truncated downloads:
`군계(軍鷄) 07권.zip`, `군계(軍鷄) 23권.zip`, `유레카26.zip`, `체SA레 07권 …zip`, `최종병기그녀 06권.zip`, `타부(Taboo) 01.zip`, `국수집 딸, 업어치기! 10권.zip`, `D.N.Angel 08권.zip` (0 bytes), `[화보집] 쓰르라미 …zip`. The scan completes and the other 11,148 are fully usable — which is exactly what FR-IDX-010 demands and what prd §10's risk row asks for.

### 4.12 Progress reporting (FR-IDX-004)

The scanner maintains one `atomic.Pointer[ScanStatus]` snapshot, updated at most every 200 ms. `GET /api/scan/status` reads it lock-free. Phases: `walking` (children discovered) → `indexing` (books done / total) → `covers` (covers done / total) → **`idle`**.

> **Correction (2026-07-28, A-8 review).** Earlier revisions ended this chain with a `done` phase. **There is no `"done"` state on the wire** — `ScanStatus.state` is exactly `idle | walking | indexing | covers | cancelling` (§7.10), and a finished run is `state:"idle"` with `finished_at` set and `run_id` retained until the next run starts. §7.10 is the contract and wins; the frontend's polling loop keys off `state !== "idle"` and a sixth value would break it. Shape in §7.10.

---

## 5. Page serving and thumbnails

### 5.1 Serving a ZIP entry without extraction (FR-SRV-001, FR-SRV-002, NFR-PRF-006, AC-001)

Given `pages.local_hdr_off`, `comp_size`, `method` from the index and a pooled `*os.File`:

**As landed (wave 1, `internal/archive/zipidx`)** — call this signature, not the sketch's:
```go
// The EntryRef fields are exactly the pages(local_hdr_off, comp_size, size, method, crc32)
// columns of §3.5, so serving a page never needs the central directory again.
func OpenEntry(ctx context.Context, r io.ReaderAt, ref archive.EntryRef) (io.ReadCloser, error)
func DataOffset(ctx context.Context, r io.ReaderAt, localHdrOff int64) (int64, error)
```
The returned reader is a `*zipidx.SectionReadCloser` for **stored** entries — it implements `io.ReadSeeker`, so the HTTP layer type-asserts and hands it to `http.ServeContent` for free `Range` support (§5.3). Deflated entries are forward-only, which is why `Accept-Ranges: bytes` is advertised only for stored entries and `dir` pages.

The mechanism, as pseudo-code:
```go
func OpenEntry(r io.ReaderAt, p Page) (io.ReadCloser, error) {
	// ONE 30-byte read to learn the local header's variable-length fields.
	// The central directory is NOT re-parsed. This is FR-SRV-002 literally:
	// seek straight to the stored offset, touch nothing else.
	var lfh [30]byte
	if _, err := r.ReadAt(lfh[:], p.LocalHdrOff); err != nil { return nil, err }
	if binary.LittleEndian.Uint32(lfh[0:]) != 0x04034b50 {
		return nil, fmt.Errorf("zip: bad local header at %d", p.LocalHdrOff)
	}
	nameLen  := int64(binary.LittleEndian.Uint16(lfh[26:]))
	extraLen := int64(binary.LittleEndian.Uint16(lfh[28:]))
	dataOff  := p.LocalHdrOff + 30 + nameLen + extraLen

	sec := io.NewSectionReader(r, dataOff, p.CompSize)   // pread only; no seek state
	switch p.Method {
	case 0: // stored — FR-SRV-003: hand the bytes straight through
		return io.NopCloser(sec), nil
	case 8: // deflate
		return flate.NewReader(sec), nil               // 32 KiB window, streaming
	default:
		return nil, ErrUnsupportedMethod
	}
}
```

**VERIFIED** on a real 19.7 MB / 104-entry archive from the collection:

| Claim | Evidence |
|---|---|
| Only the needed bytes leave the disk (NFR-PRF-006) | bytes read = `comp_size + exactly 30`, for the first, middle and last page |
| Correctness | CRC-32 of the inflated stream equals the central-directory CRC for every page tested |
| Central directory is never re-read at serve time | 10–47 `ReadAt` calls per page, all inside the entry's own byte range |
| Indexing reads ~nothing | central directory read = **0.365 %** of the archive, 2 `ReadAt` calls |
| CON-004 — concurrent reads share one handle safely | **300 pages pulled by 8 goroutines through a single `*os.File`: 483 ms, 1.61 ms/page, 117.8 MB/s, zero errors** |

CON-004 is satisfied *structurally*: everything goes through `io.ReaderAt`, which on POSIX is `pread(2)` and on Windows is `ReadFile` with an explicit `OVERLAPPED` offset. There is **no shared seek cursor**, so no lock is needed and no handle needs duplicating.

`kind='dir'` pages (FR-SRV-005): open `<root>/<book rel>/<page entry_path>` through the root's `os.Root` handle (§8.1) and `http.ServeContent` it — free `Range`, `Last-Modified` and `If-Modified-Since`.

### 5.2 Archive handle LRU pool (FR-SRV-004)

**As landed (wave 1, `internal/openpool`)** — the public surface, which is what WP-07 and WP-12 call:
```go
func New(opts Options) *Pool                 // Options.Max defaults to openpool.DefaultMax == 64
func (p *Pool) Acquire(ctx context.Context, path string, wantMtime, wantSize int64) (*Ref, error)
func (p *Pool) Invalidate(path string)
func (p *Pool) Stats() Stats
func (p *Pool) Close() error

// Ref is a borrowed handle. Callers MUST call Release exactly once and must not
// use the Ref afterwards. It implements io.ReaderAt and nothing more, so it is
// passed straight to zipidx.OpenEntry.
func (r *Ref) ReadAt(b []byte, off int64) (int, error)
func (r *Ref) Release()
func (r *Ref) Stale() bool                   // size/mtime disagree with the index -> 409 stale (§7.6)
func (r *Ref) Size() int64
func (r *Ref) ModTime() int64
```
Note the two differences from earlier drafts of this section: `Acquire` takes a `context.Context` and returns a single `*Ref` rather than an `(io.ReaderAt, func(), error)` triple, and the staleness verdict is read off the `Ref` (`Stale()`) instead of being signalled out-of-band.

Internally the pool is the obvious refcounted LRU:
```go
type Pool struct {                      // internal/openpool — unexported fields, sketch only
	mu    sync.Mutex
	max   int                            // Options.Max; default openpool.DefaultMax = 64
	lru   *list.List                     // MRU at front
	items map[string]*list.Element        // key: absolute container path
}

type handle struct {
	path     string
	f        *os.File
	size     int64
	mtime    int64
	refs     int32      // guarded by Pool.mu
	evicted  bool
}
```

Rules:
* Hit → move to MRU front, `refs++`.
* Miss → `os.Open`, `Stat`; if `size`/`mtime` disagree with the index, still serve but tag the response so the API can answer `409 stale` (§7.6) and enqueue a rescan of that book.
* Over capacity → walk from the LRU tail and evict entries with `refs == 0`. An entry with `refs > 0` is marked `evicted` and removed from the map immediately (new acquirers open a fresh handle) but its `*os.File` is closed only when the last in-flight reader releases it. **A file descriptor is never closed underneath an active stream.**
* Invalidation: an explicit `Pool.Invalidate(path)` is called by the scanner whenever it rewrites a book, so a changed archive can never be served from a stale offset.
* Metrics: hits, misses, evictions, current size, exposed at `GET /api/health?verbose=1`.

> **Corrected 2026-08-06 — a hit does not re-stat, and until now nothing called `Invalidate`.** Two things
> this section said were not true of the landed code. (1) The "Miss →" rule above is the *only* place
> `(size, mtime)` are ever compared against the file: on a **hit** `Acquire` answers from the descriptor it
> already holds, and `Ref.Stale()` compares the caller's numbers against the ones recorded when that
> descriptor was opened. After `mv 궁\ 24.zip.new 궁\ 24.zip` the path is a new inode while the pool holds a
> live descriptor on the old, unlinked one — so the read is not a stale read, it is a read of a file the user
> deleted. (2) `grep -rn Invalidate` found **no non-test caller at all**; the scanner's promise in the bullet
> above had never been implemented.
>
> **The listing path and the serving path now part company, deliberately.** Serving keeps the tolerance this
> section describes — a page stream is committed to the offsets the index recorded, and those belong to the
> descriptor the pool is holding, so refusing to serve would be worse (§7.6 answers `409` and enqueues a
> rescan). **Listing may not.** `source.zipSource.List` is the scanner's only door into an archive and its
> result is written down *beside* the `(size, mtime)` it was handed, so a listing of some other inode is a
> wrong verdict that looks like a measured one. On a stale ref it now drops the handle
> (`Pool.Invalidate` — the bullet above, honoured at last), re-opens once, and if the fresh descriptor still
> disagrees fails that one book with `source.ErrContainerChanged` → `books.status='error'` (FR-IDX-010),
> which the next scan re-reads because the recorded `(size, mtime)` no longer match the disk.

Default 64 is deliberately far below typical `ulimit -n` (1024). At startup we log the current `RLIMIT_NOFILE` and warn if `Options.Max + 64 > limit`.

> **No config key for this (as landed).** Earlier drafts of this section said the bound came from `server.max_open_archives`, but that key is **not** in the §3.2 schema, not in `config.Server`, and not in `shelf.example.yaml`. The bound is `openpool.Options.Max`, defaulting to the exported `openpool.DefaultMax` (64); WP-13's composition root passes it. If the key is wanted, it is a WP-01 change plus a §3.2 amendment — not something a consumer may assume exists.

### 5.3 HTTP caching for pages (FR-SRV-007) — and why the URL carries a version

`Cache-Control: immutable` is a **promise that the bytes at this URL will never change**. A page URL keyed only by `(book_id, page_no)` cannot honour that promise: `book_id` is path-derived, so re-downloading a better scan of `01권.zip` changes the content behind a URL the browser has been told to cache for a year.

The contract therefore puts the content version **in the URL**:

* `books.content_version` = first 16 hex chars of `SHA-256(kind ‖ 0x00 ‖ file_size ‖ 0x00 ‖ file_mtime)` for `zip`/`pdf`, or the directory fingerprint (§4.6) for `dir`. It is returned as `cv` on every book payload.
* The frontend appends `?v={cv}` to **every** page and thumbnail URL.

| Request | Response headers |
|---|---|
| `?v=` present **and equal to the book's current `cv`** | `Cache-Control: public, max-age=31536000, immutable` + strong `ETag` |
| `?v=` absent | `Cache-Control: public, max-age=60, must-revalidate` + strong `ETag` |
| `?v=` present but **stale** | `409` `{code:"stale_version"}` with the current `cv` in the body, so the client can refetch metadata |

Strong ETag forms (quoted, per RFC 9110):

```
zip page   "p1-<book_id>-<page_no>-<crc32 as 8 lowercase hex>"      // CRC is free from the CD
dir page   "f1-<book_id>-<page_no>-<size hex>-<mtime hex>"
pdf raster "r1-<book_id>-<page_no>-<width>-<cv>"
thumbnail  "t1-<thumb hash>"                                        // the hash already covers everything
```

**VERIFIED** with `httptest`: a plain `GET` returns 200 with the ETag and `Cache-Control`; `If-None-Match` with the same ETag returns **304** with an empty body; `Range: bytes=0-99` returns **206** with `Content-Range: bytes 0-99/100000`.

**Range support policy.** `stored` entries and `dir` pages go through `http.ServeContent` over an `io.ReadSeeker` and support `Range` fully. `deflate` entries are a forward-only stream, so we set `Content-Length` from `pages.size`, **omit `Accept-Ranges`**, and answer 200 to a `Range` request. This is legal (a server may ignore `Range`) and correct; browsers displaying `<img>` do not need ranges.

`Content-Type` comes from `pages.ext` via a fixed table (never from sniffing). Every image response also gets `X-Content-Type-Options: nosniff`.

**FR-SRV-008**: original bytes only. There is no resize, re-encode or EXIF stripping on `/pages/{n}` — the response body is byte-identical to the archive member, proven by the CRC-32 check above.

### 5.4 Thumbnail generation (FR-THM-001..008)

Pipeline per thumbnail: acquire handle → open entry → `io.ReadAll` capped at `thumbnails.max_source_bytes` → `image.Decode` → `imaging.Resize(img, w, 0, imaging.Lanczos)` (skipped when the source is already narrower than `w`) → `jpeg.Encode` at `thumbnails.quality` → write `<path>.tmp` → `os.Rename` (atomic publish; concurrent readers never see a partial file).

**VERIFIED** end-to-end against real archives, generating both a 320 px and a 640 px thumbnail from the true first page of each:

| Workers | Rate | Extrapolated for all 11,157 books | Peak RSS |
|---|---|---|---|
| 4 | 21.0 covers/s | 531 s | 229 MiB |
| 8 | 50.9 covers/s | 219 s | 260 MiB |
| 16 | **66.9 covers/s** | **167 s** | 359 MiB |

0 failures in 302 archives. ~47 KB per thumbnail on average.

Worker policy:
* `thumbnails.workers` defaults to `min(4, NumCPU)`. Each in-flight decode holds the compressed source plus an RGBA buffer — a 1600×2400 page is ~15 MiB of RGBA alone, so **peak RSS ≈ 25 MiB × workers**. The 4-worker default keeps the steady state near NFR-PRF-005's 200 MB; 16 workers reached 359 MiB, which is fine for a one-off warm-up but not as a default.
* `imaging` parallelises internally across `NumCPU`. Combined with our pool that oversubscribes the scheduler; it is harmless (Go's scheduler handles it) but it means raising `thumbnails.workers` past ~8 buys little. Documented in the config comment.
* **Single-flight** on the cache path: concurrent requests for the same thumbnail block on one generation, they do not each decode.
* **Two queues.** `coverQ` (unbounded, drained first) is fed by the scanner as each series completes — FR-THM-003, covers appear while the scan is still running. `pageQ` (bounded, dropped-oldest) is fed by `GET …/thumbs/{n}` misses — FR-THM-004, lazy.
* AVIF decode is serialised behind a **1-permit semaphore** regardless of `workers` (1.1 s and ~170 MiB per decode). PDF rasterisation is serialised behind `pdf.workers`.

**Reference for the full-collection cost.** Page thumbnails for *everything* would be 1.36 M images ≈ 5.6 h at 67/s. That is precisely why FR-THM-004 makes page thumbs lazy and FR-THM-003 makes only covers eager: covers are **167 s**.

### 5.5 Format support and graceful degradation (FR-IDX-011, CON-003)

| Format | Serve original (`/pages/{n}`) | Server-side thumbnail | Path |
|---|---|---|---|
| JPEG / PNG / GIF | yes | yes | stdlib |
| BMP / TIFF | yes | yes | `x/image/bmp`, `x/image/tiff` |
| WebP (still) | yes | yes | `x/image/webp`, pure Go |
| **WebP (animated)** | **yes** | **no** → see below | `x/image/webp` **VERIFIED** to reject it: `webp: invalid format` |
| **AVIF** | **yes** | yes, slow path | `gen2brain/avif` (wazero), 1 at a time, lazily initialised |
| PDF page | rasterised | yes | pdfium/wazero |

**Graceful-degradation policy, stated plainly.** Serving is never affected — `/pages/{n}` streams the original bytes for every format including animated WebP and AVIF, and every target browser (NFR-CMP-001) decodes both natively. Only *thumbnail generation* can fail. When it does:

* `GET …/thumbs/{n}` returns **`422`** with `{"error":{"code":"thumb_unavailable","message":"...","detail":{"reason":"animated_webp"}}}`. A negative result is cached in memory for 10 minutes so we do not retry the decode on every scroll.
* The frontend falls back to the **original image scaled by the browser** for that one item, or to the placeholder cover of FR-LIB-008. It must handle 422 on any thumbnail endpoint.
* If a series *cover* cannot be generated, `cover_kind` is left set but `GET /api/series/{sid}/cover` returns 422 with the same shape; FR-LIB-008's text placeholder covers it.
* Enabling `github.com/gen2brain/webp` (already verified: it decodes animated WebP correctly, returning frame 1) upgrades animated WebP from 422 to a real thumbnail at the cost of a second wazero module. Deferred; zero WebP files exist in the target collection.

CON-003: thumbnails are **JPEG only** in v1. The literal string `"jpeg"` is part of the cache-hash input (§5.6), so switching to WebP/AVIF later changes every hash and is a pure cache-invalidation event with no migration.

### 5.6 Thumbnail cache layout (FR-THM-002, FR-THM-006, FR-THM-007)

Exact hash input, spelled out:

```
"shelf-thumb/1" ‖ 0x00 ‖ <book_id> ‖ 0x00 ‖ <page_no decimal> ‖ 0x00
                ‖ <width decimal> ‖ 0x00 ‖ <format> ‖ 0x00 ‖ <quality decimal>
                ‖ 0x00 ‖ <content_version>
```
SHA-256 → first 10 bytes → the same lowercase base32 as §3.4 → 16 chars `h`. Path:

```
<cache_dir>/thumbs/<h[0:2]>/<h[2:4]>/<h>.jpg          e.g.  thumbs/2k/q5/2kq5mshjlgisgk4l.jpg
```

**VERIFIED**: the spike produced exactly this layout (`2k/q5/2kq5mshjlgisgk4l.jpg`, `6h/nn/6hnnlicenkpkywx4.jpg`). This is FR-THM-002's `ca/che/<hash>.jpg` two-level fan-out; at 1.36 M files the leaves hold ~1,300 files each — comfortable on ext4, XFS, APFS and NTFS.

Rendered PDF pages use the identical scheme under `<cache_dir>/pdf/` with domain `"shelf-pdfpage/1"` and `<width>` in place of the thumbnail width.

* **FR-THM-006 (mtime invalidation) is structural.** `content_version` is derived from `(file_size, file_mtime)`. A changed source file ⇒ a different `cv` ⇒ a different hash ⇒ a different path. There is no invalidation *code* that can be forgotten or get it wrong; the old file simply becomes unreferenced and is removed by the sweeper or by an explicit purge.
* **FR-THM-007 (cache deletable at any time).** Nothing reads the cache without falling back to generation. `rm -rf <cache_dir>` while the server runs costs latency, not correctness. AC-005 is the union of this and NFR-DAT-001.
* **FR-THM-008 (usage + purge).** `GET /api/cache/usage` walks the cache once per 60 s (cached result) and reports per-kind file counts and bytes. `DELETE /api/cache?kind=thumbs|pdf|wazero|all` removes the subtree. `wazero` is listed separately because deleting it costs a 3.9 s pdfium re-compile.
* **Orphan sweeper (phase 2).** With `storage.cache_max_bytes` set, a low-priority goroutine evicts by access time until the budget is met. v1 ships explicit purge only.

### 5.7 PDF rasterisation (FR-SRV-006)

* One lazily-created `webassembly.Pool` with `MaxTotal = pdf.workers`, `RuntimeConfig` carrying `wazero.NewCompilationCacheWithDir(<cache_dir>/wazero)`.
* Documents are opened with `requests.OpenDocument{FileReader: f, FileReaderSize: n}` — **VERIFIED** to open a 36.2 MB file in 2–36 ms without slurping it, which is how NFR-PRF-006 is upheld for PDFs too.
* `page_count` comes from `FPDF_GetPageCount` at scan time (cheap; the pool spins up once for the whole scan and is torn down after).
* `GET /api/books/{bid}/pages/{n}?w=` renders via `RenderPageInPixels{Width: w}`, JPEG-encodes at `thumbnails.quality`, writes it to the PDF page cache, and streams it. `w` is clamped to `pdf.max_width` and **snapped to the nearest 100 px** so a slider drag cannot spawn hundreds of distinct cache entries.
* `res.Cleanup()` is **mandatory** in wasm mode to release the module's bitmap. Every call site uses `defer`.
* The pool is closed after `pdf.idle_timeout` (default 5 min) with no requests, releasing ~60–300 MiB back to the OS. Reopening costs the 135 ms warm-cache init.
* Response `Content-Type: image/jpeg`; the fact that the *source* is a PDF is invisible to the viewer, which is AC-004.

### 5.8 Page dimensions (FR-VWR-004)

Spread mode must know *before* layout whether a page is a double-page scan. Reading dimensions for all 1.36 M pages at scan time would mean one random seek per page — the same pattern that blew past 10 minutes in §1.2. So:

* `pages.width` / `pages.height` start `NULL`; `books.dims_state='none'`.
* They are filled **for free** whenever a thumbnail is generated (we already decoded the image).
* Opening a book (`GET /api/books/{bid}`) with `dims_state='none'` enqueues a **background dimension pass** at low priority: stream each entry only until `image.DecodeConfig` succeeds (**VERIFIED** at 23 µs for JPEG, 10 µs for PNG, 91 µs for WebP, 135 µs for AVIF — the cost is the seek, not the parse), then abort the read. `dims_state` goes `partial` → `done`.
* Until then `PageInfo.w`/`h` are `null` and the viewer uses each image's natural size once loaded, treating spread mode as single-page for unknown pages. No blocking, no CLS.

---

## 6. Concurrency, memory and startup

### 6.1 Goroutine budget

| Pool | Default | Bound |
|---|---|---|
| Scan walkers | 1 per enabled root | small |
| Scan archive readers | `min(8, max(2, NumCPU/2))` | `scan.workers` |
| Index writer | **1** | fixed — this is why there is no write contention |
| Thumbnail workers | `min(4, NumCPU)` | `thumbnails.workers` |
| AVIF decode | 1 | hard semaphore |
| pdfium instances | 1 | `pdf.workers` |
| HTTP handlers | Go default | `server.max_concurrent_pages` (default 32) semaphore around page/thumb handlers so a burst cannot fan out to hundreds of concurrent inflates |

### 6.2 Idle memory (NFR-PRF-005 ≤ 200 MB)

Measured contributions: base process + sqlite ≈ 33 MiB RSS; pdfium pool +43 MiB **only after the first PDF request**; AVIF runtime +~170 MiB **only after the first AVIF decode**. With both lazily initialised and with `thumbnails.workers=4`, a library of ZIP+folder series — 961 of the 963 real series — sits far under 200 MB. The two wasm runtimes are torn down after their idle timeouts, so idle RSS returns to baseline.

### 6.3 Startup sequence (NFR-OPS-006)

1. Parse flags, load and validate config.
2. Create `data_dir`, `cache_dir`, `cache_dir/thumbs`, `cache_dir/pdf`, `cache_dir/wazero`.
3. Open `user.db`, migrate. Open `index.db` (registering the ATTACH hook first), migrate. If `meta.schema_version` or `meta.id_version` is unknown/newer → refuse to start with a clear message rather than corrupt data.
4. Reconcile `roots` with the config: insert new, update `path`/`label`/`enabled`, and **log a warning** for index rows whose `name` is no longer in the config (never delete — see §4.9).
5. Start the HTTP server. **The library is served immediately from the existing index** — the whole point of NFR-OPS-006.
6. If `scan.on_start`, kick off an incremental scan in the background. `GET /api/scan/status` reports it; the UI shows the indicator from prd UI-001.
7. `SIGINT`/`SIGTERM` → cancel the scan context, `Server.Shutdown` with `shutdown_grace`, checkpoint both WALs (`PRAGMA wal_checkpoint(TRUNCATE)`), close pools.

---

## 7. HTTP API — **normative contract**

The frontend is built against this section in parallel with the backend. It is binding.

> **Binding as amended.** The contract is this section **plus amendments A-1…A-11** in `impl-plan.md` §0.3
> (D-47). Amendments in force: **A-4** `GET /api/series?progress=any|reading|done|unread`; **A-5**
> `Settings.library_scope: string`; **A-6** default `w` on `/cover` and `/thumbs/{n}` is **120**
> (`widths[0]` under A-1's `[120, 240, 400, 640]`); **A-8** `GET /api/series?scope=all|added` backed by
> `user.db`'s write-once `first_seen_at`, plus `Settings.server.recently_added_days` (§7.5, §7.8);
> **A-9** `ErrorCode` gains `rate_limited` for §8.2's `429` (§7.2); **A-10** `Settings.server.config_path:
> string`, the absolute path of the loaded `shelf.yaml` (§7.8, ruling **E-25**); **A-11** `POST /api/roots`
> and `DELETE /api/roots/{name}` writing the `roots:` list of that file, gated by the new
> `server.allow_root_editing` and adopted only at restart, with `ErrorCode` gaining `forbidden` and
> `Settings.server` gaining `root_editing_enabled` and `config_changed_on_disk` (§7.2, §7.4, §7.8, §3.2,
> §12 OQ-3, ruling **E-26**). **A-7** freezes everything else verbatim, and requires this table to be
> updated before anything else moves — which is what A-8, A-9, A-10 and A-11 did.
> Rulings **E-14** (the `series.status` fold, §3.5/§7.3) and **E-15** (`Settings.library_sort` is the closed
> FR-LIB-004 set, §7.8) are *sharpenings*, not amendments: they narrow a type to the values the server
> already produced and accepted, so no client that was correct becomes incorrect.
> `web/src/api/types.ts` is the TypeScript form of this section. It carries every **response** shape as
> amended — `scripts/contractcheck` diffs every golden file against it on every run — plus A-8's
> **request** field `SeriesListParams.scope?: "all" | "added"`, serialised by `urls.ts`. A-9, E-14 and
> E-15 landed in it with this ruling; A-10 landed in it with E-25, together with the two consumers that
> are the whole point of the field (`features/overlays/RootsPanel.tsx`, `features/library/Onboarding.tsx`). What remains of A-8 is the two *consumers* impl-plan §0.3 assigns
> to **WP-09** (`features/library/useLibrary.ts`, `router.tsx`'s `counts.added`). Until those land,
> 최근 추가 still counts the whole library — the divergence WP-09 flagged and ruling E-9 exists to fix —
> but the parameter is now nameable by the client, so `contractcheck`'s `SeriesListParams` diff is green.

### 7.1 Conventions

* Base URL: `{base_path}/api`. With `base_path: "/reader"`, everything below is prefixed `/reader`.
* Request and response bodies are `application/json; charset=utf-8` unless stated. Image endpoints return `image/*`.
* **All page numbers everywhere in this API are 1-based.** `n` ∈ `[1, page_count]`. There is no page 0.
* Unknown query parameters are ignored. Unknown JSON body fields are **rejected** with `400 bad_request` (strict decoding) so typos surface immediately.
* All ids are `[a-z2-7]{16}`. A syntactically invalid id is `400`; a well-formed but unknown id is `404`.
* Timestamps are **integer Unix seconds, UTC**. Byte counts are integers. Durations in JSON are milliseconds (integer).
* Every response carries `X-Request-Id`.
* CORS is not enabled: the SPA is same-origin by construction.

### 7.2 Error envelope

Every non-2xx response, including from image endpoints, has this body:

```ts
interface ErrorResponse {
  error: {
    code: ErrorCode;
    message: string;                     // English, human-readable, safe to display
    detail?: Record<string, unknown>;    // machine-readable extras
  };
}

type ErrorCode =
  | "bad_request"        // 400  malformed input
  | "unauthorized"       // 401  auth enabled and the session is missing/expired
  | "forbidden"          // 403  understood, well-formed, and refused by configuration
                         //        -- AMENDMENT A-11
  | "not_found"          // 404  unknown id, or page out of range
  | "conflict"           // 409  e.g. a scan is already running
  | "stale_version"      // 409  ?v= does not match the book's current cv
  | "unprocessable"      // 422  understood but cannot be produced
  | "thumb_unavailable"  // 422  the source cannot be decoded server-side (§5.5)
  | "rate_limited"       // 429  too many login attempts (§8.2)  -- AMENDMENT A-9
  | "unsupported"        // 501  feature absent from this build (e.g. nopdf)
  | "unavailable"        // 503  media volume unreachable / shutting down
  | "internal";          // 500
```

**Amendment A-9 (ruling E-13) — `rate_limited`.** §8.2 has always required the login limiter to answer
`429`, but this enum had no value for it, so the one response the server is *mandated* to produce was the
one the contract could not name. `POST /api/auth/login` beyond 5 attempts per minute per IP is therefore
`429 rate_limited`, with a `Retry-After` header and the same integer in `detail.retry_after` so a client
that reads only the body can still back off. It is the only endpoint that produces this code.

**Amendment A-11 (ruling E-26) — `forbidden`.** The root-editing endpoints of §7.4 are gated by
`server.allow_root_editing` (§3.2), and a request made while the gate is shut is **`403 forbidden`**. The
enum had no value for that status, which is the same defect A-9 fixed: a ruling mandated a response the
contract could not name. Three existing codes were checked against it and none fits.
`unauthorized` means the session is missing or expired and drives the frontend's re-authentication path
(`web/src/api/errors.ts` `isAuthError`, ruling E-17), so it would send an already-authenticated user — or a
user of the default password-less server (ruling E-8) — to a login screen for a refusal no login can lift.
`unsupported` is the nearest neighbour and has a real claim, since §7.6 already answers `501` for a PDF page
"with `pdf.enabled: false`"; but it is documented everywhere as *"feature absent from this build"*, and the
two answers differ in the only respect the caller acts on — the remedy is "set one key in the file whose
absolute path this API publishes as `Settings.server.config_path`", not "run a different binary". Pairing it
with 403 would also make one code map to two statuses. `bad_request` describes a malformed request, and a
well-formed `POST` to a server that has the feature switched off is not one. `forbidden` is named after its
status, like every other member of this enum, and stays reusable if a second configuration-gated write is
ever added. The body carries `detail.reason` so the UI can give the right instruction: see §7.4.

The code→status mapping above is the default, not a law: two responses pair a code with a different
status because there is no better one. `405 Method Not Allowed` carries `bad_request` (this verb does not
exist here) with `Allow` and `detail.allow`; `202 Accepted` on the image endpoints is not an error at all
and carries no envelope.

### 7.3 Shared types

```ts
type ID = string;                                        // /^[a-z2-7]{16}$/
type Unix = number;                                      // integer seconds, UTC

type BookKind   = "zip" | "dir" | "pdf";
type SeriesKind = "folder" | "zip" | "pdf";

// A BOOK's status — one container's verdict (FR-IDX-010).
type ItemStatus = "ok" | "empty" | "error" | "encrypted" | "unsupported";

// A SERIES' status — RULING E-14. Strictly narrower: `encrypted` and
// `unsupported` describe one container and are book-only. The fold is defined
// once, in §3.5:
//   no books at all               -> "empty"
//   >=1 book, at least one "ok"   -> "ok"
//   >=1 book, none of them "ok"   -> "error", with `error` carrying the reason
type SeriesStatus = "ok" | "empty" | "error";

type ReadingDir = "ltr" | "rtl";
type DisplayMode = "single" | "spread" | "vertical";
type FitMode    = "width" | "height" | "original" | "contain";
type SortKey    = "name" | "mtime" | "recent" | "size" | "books" | "added";  // FR-LIB-004, §7.5

interface Root {
  name: string;            // stable identity from the config
  label: string;           // display name; equals name when no label is set
  path: string;            // absolute path — shown only on the detail screen (UI 5.3)
  enabled: boolean;
  series_count: number;
  book_count: number;
  page_count: number;
  total_bytes: number;
  available: boolean;      // false when the path is currently unreachable
  last_scan_start: Unix | null;
  last_scan_end: Unix | null;
  last_scan_error: string | null;
  pending: boolean;        // AMENDMENT A-11 / R2 (ruling E-26, revised 2026-07-30).
                           //   TRUE for a root that is in the configuration FILE ON
                           //   DISK and has no index row: `POST /api/roots` (§7.4)
                           //   wrote it, and the running server cannot open a root
                           //   without the restart §7.4 asks for. Such a row carries
                           //   zero counts, null scan timestamps and
                           //   `available: false`, and the UI must offer no 재스캔 for
                           //   it. It is one boolean rather than a second endpoint
                           //   because the alternative was a row that is either
                           //   invisible until the restart — which contradicts the
                           //   design the requirement's owner chose — or present and
                           //   lying about being loaded.
}

interface Progress {
  book_id: ID;
  series_id: ID;
  last_page: number;       // 1-based
  page_count: number;      // the book's length as the reader last saw it: set by the
                           //   FIRST write, then moved only by an ACKNOWLEDGED one —
                           //   AMENDMENT A-14 (ruling E-45). A plain page turn does not
                           //   move it; only `stale_seen: true` does, and only when the
                           //   length is known (§7.6). It is the baseline `stale` below
                           //   is computed against, so a write that moved it would be a
                           //   write that deleted the warning.
  completed: boolean;
  started_at: Unix;
  updated_at: Unix;
  stale: boolean;          // true when page_count no longer matches the index
                           //   -> the UI may warn that the file changed.
                           //   SYMMETRIC (A-14, ruling E-45): 0 on EITHER side is
                           //   false, because 0 is "length unknown" (§4.11), not a
                           //   length. A book that is unreadable NOW therefore does
                           //   not warn — the screen already says the file cannot be
                           //   opened and there is no place to resume at — and the
                           //   warning is DEFERRED, not lost: the baseline survives,
                           //   so a repair to a different length raises it then.
}

interface SeriesProgress {                 // FR-STT-002, aggregated over books
  books_total: number;
  books_completed: number;
  books_started: number;   // started but not completed
  percent: number;         // 0..100, rounded to 1 dp: pages_read/pages_total*100
                           //   (ruling E-47 — it was books_completed/books_total,
                           //   which could not move until a whole 권 was finished).
                           //   pages_read counts a completed book at its full length
                           //   and a started one at its last read page, clamped to the
                           //   length the INDEX reports (E-45 §6: never the progress
                           //   row's stale baseline); pages_total is series.page_count.
                           //   *** exactly 100 only when books_completed === books_total ***
                           //   — otherwise capped at 99.9, so `percent >= 100` and the
                           //   progress=done scope can never disagree about 완독.
                           //   *** exactly 0 when there are no pages to divide by ***
                           //   (an empty or broken series, §4.11) — never NaN, never null
  last_read_at: Unix | null;
  last_book_id: ID | null; // the book "Continue reading" should open
  last_page: number | null;
}

interface SeriesSummary {
  id: ID;
  root_name: string;
  name: string;            // display name
  path: string;            // root-relative slash path (supporting text only)
  kind: SeriesKind;
  book_count: number;
  page_count: number;
  total_bytes: number;
  mtime: Unix;
  added_at: Unix;          // AMENDMENT A-8: this is now
                           //   COALESCE(user.db series_seen.first_seen_at, index added_at),
                           //   so it survives --rebuild-index. Same name, same type,
                           //   sharpened source. `sort=added` orders by this expression;
                           //   `scope=added` filters on first_seen_at alone (§7.5).
  status: SeriesStatus;    // RULING E-14 — three values, never a book-only verdict.
  error: string | null;    // non-null whenever status !== "ok" (design.md 화면 2)
  has_cover: boolean;      // false -> render the FR-LIB-008 text placeholder
  cover_cv: string | null; // append as ?v= to the cover URL
  progress: SeriesProgress;
}

interface BookSummary {
  id: ID;
  series_id: ID;
  name: string;
  path: string;            // root-relative slash path
  kind: BookKind;          // drives the ZIP/폴더/PDF badge (FR-LIB-009)
  ord: number;             // 0-based position in the series
  page_count: number;
  total_bytes: number;
  file_size: number;       // 0 for kind:"dir"
  mtime: Unix;
  cv: string;              // content version — append as ?v= to page/thumb URLs
  status: ItemStatus;
  error: string | null;
  progress: Progress | null;
}

interface PageInfo {
  n: number;               // 1-based
  name: string;            // decoded entry/file name, for the thumbnail panel
  ext: string;             // ".jpg"
  size: number;            // uncompressed bytes
  w: number | null;        // null until known (§5.8)
  h: number | null;
}
```

### 7.4 Roots and health

| | |
|---|---|
| `GET /api/health` | `200 {ok: true, version: string, commit: string, started_at: Unix, uptime_ms: number, pdf_enabled: boolean, avif_enabled: boolean}`. Add `?verbose=1` for pool counters. Never requires auth. |
| `GET /api/roots` | `200 {items: Root[]}` — from the **index**, so it also lists a root that has left the configuration (`available: false`), minus any root removed by `DELETE` in this process's lifetime. **AMENDMENT A-11 / R2**: plus one `pending: true` row per root that is in the configuration **file on disk** with no index row yet — including one whose name this process removed, if something has since put an entry back (see §7.4). |
| `POST /api/roots` | `201 RootEntry` — **AMENDMENT A-11** (ruling E-26). Adds an entry to the `roots:` list of the configuration file, and — **AMENDMENT A-12** (ruling E-40) — opens it into the running server and scans it. |
| `DELETE /api/roots/{name}` | `204` — **AMENDMENT A-11**. Removes one entry from that list, purges its index rows (R1), and — **AMENDMENT A-13** (ruling E-41) — drops it from A-12's added set and moves the adopted digest back. It still closes no handle. |
| `GET /api/browse` | `200 BrowseResponse` — **AMENDMENT A-12** (ruling E-40). Lists directories under `server.browse_bases`, for the settings screen's folder picker. |

#### Amendment A-11 — writing the `roots:` list (ruling E-26)

**These two endpoints edit `shelf.yaml`.** Roots were opened exactly once, at startup
(`internal/app/app.go` step 6, `source.OpenRoots`), with no reload path — the open-file pool, the source
factory and the scanner are all built over that one set — so a change took effect only at the next restart.
That is what `Settings.server.config_changed_on_disk` (§7.8) tells the UI to say, and it was true after any
successful write here.

> **AMENDED 2026-08-06 — AMENDMENT A-12 (ruling E-40): `POST` now opens the root it wrote.** The paragraph
> above stands for `DELETE` and is kept because it is still the shape of this API; what changed is that the
> sentence *"they do not open or close a root in the running server"* is now true of removal only.
>
> A successful `POST` opens an `os.Root` into the live set (`source.RootSet.Add`), makes the name selectable
> (`Scanner.AddConfigRoot`), writes the `roots` row (`index.UpsertRoot`) and starts a scan **of that root
> alone**. The row is then not `pending`, `available` is true, and `config_changed_on_disk` is **false** —
> this process and the file agree again. Those four steps run in that order: the one that can genuinely fail
> is the open, and it must fail before anything claims the root is live.
>
> **Removal is deliberately not the mirror of this.** Closing a handle that an in-flight page request is
> streaming through needs per-entry reference counting, and A-11's revision R1 — the removed-set — already
> makes a removal take effect at once without touching the open set. E-40 §2 keeps it there.
>
> **A failed adoption is still `201`.** The file write is what the user asked for; rolling it back to work
> around the server's own inability to open a directory it had just stat-ed would discard their edit. The
> fallback is A-11's behaviour verbatim — a `pending` row and a true `config_changed_on_disk` — which is why
> that path is pinned by a test rather than merely described here.

> **AMENDED 2026-08-07 — AMENDMENT A-13 (ruling E-41): `DELETE` moves the adopted digest back.** A-12 taught
> the `POST` to move `Server.configDigest` forward and gave `DELETE` no matching step, so the two verbs
> disagreed about what this process had adopted. A successful `DELETE` now does the mirror, under the same
> guard: it drops the name from A-12's added set, and — **only when the file it just wrote was the file this
> process had already adopted** — re-reads the file and moves the baseline to it.
>
> **What that fixes is two false answers, not one.** After add-then-remove the file is byte-identical to the
> one startup loaded (`internal/config/rootsfile.go` splices raw lines rather than re-emitting the document),
> yet `config_changed_on_disk` stayed **true** and the settings screen asked for a restart that had nothing to
> apply — flatly contradicting the field's own definition in §7.8. And `configuredRoots()` kept the removed
> root forever, so `GET /api/browse` (§7.4a) went on marking its directory `duplicate` and its parent
> `overlaps` while `POST /api/roots` would have accepted either — the picker-vs-endpoint drift that section
> forbids, with the server on the wrong side of it.
>
> **This overturns one A-11 sentence and no more.** Under A-11 a removal also left the restart notice up.
> R1 makes a removal take effect *immediately*, so that notice was always asking the user to restart for a
> change already applied; E-40 fixed exactly that lie for the add, and E-41 fixes it for the remove.
>
> **Hot remove is still not bought.** `releaseRoot` closes no handle. E-40 §2 stands: an `os.Root` that an
> in-flight page request is streaming through needs per-entry reference counting, and R1's removed-set already
> makes the removal visible without it. What is undone here is bookkeeping.

> **REVISED 2026-07-30 — R1 and R2 (decisions.md E-26, "REVISION 2026-07-30").** This paragraph originally
> continued: *"and **`GET /api/roots` is deliberately unchanged until then** — a `POST` that appeared to work
> and then served nothing would be worse than one that says what it did."* The user directed the screen to
> follow the Claude Design prototype, in which the trash button makes the row disappear and an added root
> appears at once. Two consequences, and no others:
>
> * **R1 — `DELETE` purges the index rows and takes effect immediately.** See the `DELETE` section below.
> * **R2 — `POST` produces a *pending* row.** `GET /api/roots` reports a root that is in the configuration
>   file on disk with no index row as `pending: true` (§7.3), with zero counts, null scan timestamps and
>   `available: false`. The original sentence's reasoning is kept and is what forces the flag: a row that says
>   it is not loaded yet is honest, a row that claims to be loaded is the failure it guarded against. The
>   file's `roots:` list is read by the same code path that already re-hashes the file for
>   `config_changed_on_disk`, so there is one reader of the file on disk and not two.
>
> Neither buys hot-add or hot-remove: the open root set, the pool and the source factory are not touched.
> *(Superseded for the add by A-12 / ruling E-40 — see the amendment note above. R2's `pending` row survives
> as the honest report of an addition this process could not open, and is what the fallback renders.)*

Both endpoints sit behind the auth gate of §8.2 like every other `/api/*` route when a password is configured.
Writes are serialised server-side and each one re-reads the file from disk before editing it, so a root added
by hand between two requests is never silently discarded.

**The gate.** Both verbs answer **`403 forbidden`** unless `server.allow_root_editing` is `true` (§3.2)
**and** this server was started from a configuration file **and** that file is not inside any configured
`roots[].path`. The three conditions are one capability, reported as one boolean —
`Settings.server.root_editing_enabled` (§7.8) — exactly as `pdf_enabled` folds the `nopdf` build tag together
with `pdf.enabled`. The second condition exists because A-10 already documents `config_path: ""` as possible
and there is then no file to edit; the third because writing a file under a media volume is precisely what
FR-CFG-005 / NFR-DAT-002 forbid, and `internal/config/validate.go` already refuses `storage.data_dir` and
`storage.cache_dir` inside a root for the same reason. `detail.reason` distinguishes the three
(`"disabled"` · `"no_config_file"` · `"config_inside_root"`) so the UI can give the instruction that actually
applies.

#### `POST /api/roots` — AMENDMENT A-11

```ts
// request
interface RootCreate {
  path: string;            // REQUIRED. Absolute, and a readable directory on the server's host.
  label?: string;          // optional display name; defaults to the base name of `path`
}

// 201 Created  +  Location: {base_path}/api/roots/{name}
interface RootEntry {
  name: string;            // SERVER-GENERATED — see below. The stable identity every id hashes.
  path: string;            // absolute and cleaned, exactly as written to the file
  label: string;           // as written; the base name of `path` when the request omitted it
  enabled: boolean;        // always true: the key is not written, so §3.2's default applies
}
```

`RootEntry` is **not** §7.3's `Root`, and the difference is deliberate: the new root has no index row and no
open handle, so `available`, the four counts and the two scan timestamps would all have to be invented. What
this endpoint creates is a *configuration entry*, and that is what it returns. `201` rather than `200` because
a new addressable resource now exists at a URL **the client did not choose**; `Location` is the only way the
client learns the generated name without re-reading the file.

**`label` is written even when it was derived.** An omitted `label` becomes the base name of `path`, and that
string is written to the file — *not* left absent. §3.2's own fallback for a missing `label` is the `name`, and
the `name` here is a slug that dropped every Hangul character in the directory's name; `[만화] 군계 1~25` is a
better shelf label than `root-2`. The operator can still edit or delete the line afterwards, and §7.3's
"`label` equals `name` when no label is set" is unchanged for roots that genuinely have none.

**`enabled` is not settable and not editable.** Neither is `name`, `path` or `label` of an existing root: there
is no `PUT` and no `PATCH` here. Changing `name` orphans that root's reading progress (§3.4) and changing
`path` re-points every id under it, so both stay operations a human performs in the file, deliberately.

**Validation, before anything is written.** The governing rule is that **this endpoint must never write a file
the server would refuse to start from** — it tells the user to restart, so handing them `exit 2` would be a
defect of the endpoint, not of their configuration. Every root-related rejection of §3.2's startup validation
is therefore applied here first, plus one rule of A-11's own. `path` is cleaned and its symlinks resolved
**for comparison only**; the value written to the file is the cleaned absolute path the caller supplied, so the
file keeps saying what the operator meant.

| `detail.reason` | Rule | Why it is a rule |
|---|---|---|
| `missing` | `path` absent or empty | The one required field. `detail.field` is `"path"` like every other row. |
| `not_absolute` | `path` is relative | A relative path is ambiguous against the server's working directory, which the user cannot see. §3.2 requires absolute. |
| `does_not_exist` | nothing at `path` | Startup validation refuses it (§3.2), so writing it guarantees a failed restart. |
| `not_a_directory` | `path` is a file | Same. |
| `not_readable` | the directory cannot be opened | Same in effect: the root would index as empty and `available: false`. |
| `duplicate` | the resolved path is already a configured root | Two names for one directory means two `series_id`s for one file. `detail.conflicts_with` names the existing root. |
| `overlaps` | the resolved path is an **ancestor or descendant** of a configured root | The same file would sit under two roots and get two series identities, one of which the scanner would resolve first arbitrarily. Checked against **disabled roots too** — a disabled root is still in the file and still collides when it is re-enabled. `detail.conflicts_with` names it. |
| `contains_storage` | `storage.data_dir` or `storage.cache_dir` is inside `path` | The mirror of the startup rule that keeps SHELF from writing to a media volume (FR-CFG-005). |
| `too_long` / `control_characters` | `label` over 128 bytes, or containing control characters | Same bound and same reasoning as `library_scope` in §7.8: the one free-form field is kept from becoming a place to store arbitrary data, and control characters would end up in a YAML file the server re-parses at startup. `detail.field` is `"label"`. |
| `control_characters` | `path` contains a control character | **Checked before anything touches the filesystem, and on `path` as well as on `label`.** A directory named `media\nb` is legal on Linux, and the writer of §7.4 is a text splicer, not a YAML emitter: it escapes `\` and `"` and copies every other byte through. A line break inside the resulting double-quoted scalar *folds to a space* when the file is read back, so the file would name a directory that does not exist and the next start would be `exit 2` — from a request that answered `201` echoing the correct path. `\a` is worse: `yaml.Unmarshal` refuses the whole document ("control characters are not allowed"), so the server cannot parse what this endpoint wrote. Checking `label` alone is not enough, because an omitted `label` is derived as the base name of `path` *after* validation and inherits the same bytes. `detail.field` is `"path"`. |

Every rejection is `400 bad_request` with `detail: {field, reason}` — `field` is `"path"` or `"label"` — plus
`detail.conflicts_with: string` on `duplicate` and `overlaps`. **The rejected value is not echoed back.** The
caller sent it, and keeping absolute host paths out of the envelope is what lets the failure be pinned in a
golden file at all (`internal/httpapi/harness_test.go` names `Root.path` and `CacheUsage.cache_dir` as the two
values it cannot pin because they are genuinely absolute).

| Status | When |
|---|---|
| `201` | the entry was written and fsync'd; `Location` carries the new name |
| `400 bad_request` | any row of the table above; also malformed JSON and an unknown body field (§7.1 strict decoding) |
| `403 forbidden` | the gate is shut, `detail.reason` says which of the three conditions failed |
| `409 conflict` | the file on disk cannot be edited. **`detail.reason` is one of `unparseable` · `not_a_block_sequence` · `file_missing` · `duplicate`** — see the table below. Nothing is written and no `.bak` is taken. |
| `500 internal` | the file could not be written (permissions, read-only filesystem, no space). The message names no path — §8.4 — and the client already holds `Settings.server.config_path`. |

**Every `409` carries a `detail.reason`, and they are four different problems with four different remedies.** The
status alone told the UI nothing, so it printed one vague sentence covering all of them; each row below is a
sentence the user can act on.

| `detail.reason` | When | What the UI can say |
|---|---|---|
| `unparseable` | the file is no longer valid YAML, or `roots:` is present but is not a sequence at all | The file is broken. Nothing was written and no `.bak` was taken: a writer that cannot understand the file cannot promise to preserve it, and overwriting it would destroy an edit the user is halfway through. |
| `not_a_block_sequence` | `roots:` is a **flow** sequence — `roots: [{name: "manga", path: "/m"}]` | **This file is not broken.** It is valid YAML, it starts the server, and `GET /api/roots` is listing its entries on the same screen at the same moment — so "the file cannot be read" is a sentence the user can only disbelieve. The writer refuses it because it splices lines and re-emitting the document is exactly what it exists to avoid (it would destroy the 15 KB of documentation the file ships inside itself). The remedy is specific and belongs in the message: **rewrite `roots:` as a block list.** |
| `file_missing` | the file is no longer where this server loaded it from | It was moved, renamed or deleted after startup. `Settings.server.config_path` is the path to check. |
| `duplicate` | the generated `name` was taken between this request's read and its write (`POST` only) | A lost race with a hand-edit or another client. Retrying works, which is why the writer re-reads and re-checks rather than trusting the list the caller read a moment ago. |

`file_missing` is a `409` and not a `404`: `404` on these endpoints means *the root* is not there (see `DELETE`
below), and the resource this request could not reach is the configuration file. Conflating them would tell the
client to stop asking about a root that is fine.

#### `DELETE /api/roots/{name}` → `204` — AMENDMENT A-11

Removes that entry from the `roots:` list **and purges that root's rows from `index.db`**.

> **REVISED 2026-07-30 — R1 (decisions.md E-26, "REVISION 2026-07-30").** This section originally read:
> *"Removes that entry from the `roots:` list. **It removes nothing else.** The series, the books, the pages
> and — the point of the rule — the reading progress all stay exactly where they are, which is what
> `App.reconcileRoots` (`internal/app/app.go`) already does when a root disappears from the configuration…
> **The UI must say so before the user confirms**, and it must not promise that the row disappears:
> `GET /api/roots` reads the index, so a removed root keeps being listed after the restart, with
> `available: false`."* That produced a root the user removed, restarted for as instructed, and still saw —
> with its series still in the library, because `GET /api/series` has no configured-root filter either. It is
> not what 제거 means and not what the design shows.

**What is removed, and what is not.** The YAML entry goes, and so do that root's rows in `index.db` — series,
books, pages and the `roots` row — in the single transaction `index.DeleteRoot` already performs. `index.db`
is **derived and disposable** (§3.5; `shelf.example.yaml` says "delete it, restart, and it rebuilds"), so
nothing authored is at stake. **`user.db` is not touched**: reading progress, per-book preferences and
`series_seen` survive, and they *reattach* if the same directory is added again — which is exactly why the
name generator below uniquifies against the **configuration** and not the index.

**The order of the two writes is deliberate: the index purge runs first, then the YAML.** If the YAML write
then fails the request is `500`, the root is still configured, and the next scan re-indexes it completely —
`index.db` is the half that is allowed to be rebuilt. The other order fails the other way: a root gone from
the file with its rows orphaned in an index that `App.reconcileRoots` will keep forever, which is the exact
defect R1 exists to remove.

**Before the restart**, the running process honours the removal through an in-memory removed-set: the root is
excluded from the index-derived rows of `GET /api/roots`, `POST /api/scan {"roots":["<name>"]}` for it is
**`404`**, and a full scan skips it. Nothing else is hot-swapped.

**The removed-set does not suppress an R2 pending row, and that asymmetry is deliberate** (corrected
2026-07-30 — it read "excluded from `GET /api/roots`" without qualification, and the code matched). The
index-derived rows are where a stale row can survive a removal, which is what the set is for. An R2 row is
derived from the configuration file *as it is at that instant*, so a removed name appearing there at all means
something has put an entry back — a hand-edit, a restored `.bak`, or `POST /api/roots` regenerating the
retired name, which the generator below does **on purpose** so a re-added directory reattaches the progress
`user.db` kept. Suppressing it produced this sequence, which is the ordinary one for an operator moving a
library to a new disk (both directories slug to the same name):

```
POST   /api/roots  → 201, Location: /api/roots/manga
GET    /api/roots  → the row is not there
POST   /api/roots  → 400 duplicate, conflicts_with: "manga"
```

A root that is in the file, that this server put there, that it refuses to add twice, and that it will not
show. `pending` — in the file, not open, opened by a restart — is true of it, and is what it now reports. The
scan side is unchanged and stays `404`: this process's open handle for that name is still the *old*
directory, and no control reaches it, because a pending row has no 재스캔 (ui-spec §8.6).

**`App.reconcileRoots` is unchanged.** Its warn-and-keep answer for a root that vanished from a hand-edited
file is still correct — *"absence from one run is never evidence of absence on disk"* (§4.9) — and the
difference is evidential: an explicit `DELETE` is evidence of intent, a missing line in a file is not.

**The UI must still say what happens before the user confirms**, but the sentence changes: **the reading
progress is kept**, and the index rows are not.

`{name}` is a configuration identity, not one of §7.1's `[a-z2-7]{16}` ids; it is validated against §3.2's
`[a-zA-Z0-9._-]{1,64}` and follows the same split as §7.1 — syntactically invalid is `400`, well-formed but
absent is `404`.

| Status | When |
|---|---|
| `204` | the entry was removed and the file fsync'd |
| `400 bad_request` | `{name}` is not a well-formed root name; `detail: {param: "name", value}` as for any path wildcard |
| `403 forbidden` | the gate is shut |
| `404 not_found` | no root of that name is **in the configuration file on disk**. This includes a root that `GET /api/roots` is listing from the index alone — a hand-edit removed it from the file already — which is the honest answer, and the one that stops the button reporting success while changing nothing. It also includes a root already removed by an earlier `DELETE` in this process, so the verb stays idempotent in status terms rather than purging an index twice. |
| `409 conflict` | **`detail.reason` is one of `last_root` · `unparseable` · `not_a_block_sequence` · `file_missing`.** `last_root` is this verb's own: §3.2 requires at least one root and `internal/config/validate.go` exits 2 without one, so a removal that emptied the list would tell the user to restart into a server that never comes back. The other three are the file-level failures of the `409` table under `POST` and mean exactly the same thing here. |
| `500 internal` | the file could not be written |

**The purge and the file write are not one transaction, and the order is what decides how that fails.** If
`index.DeleteRoot` succeeds and the YAML write then fails, the answer is `500`, the root is **still configured**
and its **index rows are gone** — the next scan rebuilds them, because `index.db` is the derived, disposable
half (§3.5). That is the state the order above is chosen to produce, and it is asserted as such
(`TestDeleteRoot_purgesTheIndexBeforeItWritesTheFile` makes the write fail and checks that the rows went and
the file did not). Until 2026-07-30 nothing enforced it: swapping the two blocks was green, and the swapped
order fails the other way — a root gone from the file with its rows orphaned in an index `App.reconcileRoots`
keeps forever, the exact defect R1 exists to remove.

#### How a root `name` is generated (A-11)

`name` is not the caller's to choose, because it is hashed into every `series_id` and `book_id` (§3.4, D-14 /
D-51): a client that picked it could silently reattach a new directory to another root's reading progress, or
break its own by picking a name that is one character different from last time. The server derives it:

1. Slugify `label`, else the base name of `path`: lowercase ASCII letters and digits pass through, `.`, `_` and
   `-` are kept, every other byte — including all Hangul, which is the common case here — is dropped, runs of
   dropped bytes collapse to a single `-`, leading and trailing `-` are trimmed, and the result is truncated to
   64 bytes. It satisfies §3.2's `[a-zA-Z0-9._-]{1,64}` by construction.
2. An empty result (a purely Korean label over a purely Korean directory name) falls back to `root`.
3. Collisions with a name **in the current configuration** get `-2`, `-3`, … appended, trimming the stem to
   stay inside 64 bytes.

Step 3 checks the configuration and **not** the index, deliberately. An ex-configured root's name is exactly
the name a re-added directory should get back: the same label over the same directory produces the same slug,
which is what reattaches the reading progress that `DELETE` went out of its way to keep. **R1 is what makes
this rule load-bearing rather than theoretical**: after R1 the index rows are gone, so `user.db` is the only
thing left to reattach to, and it keys on `(root name, root-relative path)` (§3.4). The cost is that a
*different* directory whose label happens to slug to a retired name inherits that root's rows — a narrow
mis-attachment whose worst outcome is a wrong 완독 badge, and one that hand-editing the file has always had.

#### `GET /api/browse` → `200 BrowseResponse` — AMENDMENT A-12 (ruling E-40)

The directory picker behind 설정 → 루트 관리 → 찾아보기.

```ts
// GET /api/browse            → the synthetic top level: the configured bases
// GET /api/browse?path=/abs  → one directory under a base
interface BrowseResponse {
  path: string              // "" at the top level. Never "/".
  parent: string | null     // null at a base and at the top level
  self: BrowseEntry | null  // `path` itself as a candidate; null at the top level
  entries: BrowseEntry[]    // immediate SUB-DIRECTORIES, natural-sorted
  truncated: boolean        // the per-directory cap was hit
}
interface BrowseEntry {
  name: string
  path: string              // absolute and cleaned — what POST /api/roots wants
  selectable: boolean       // false when POST /api/roots would reject it
  reason: string | null     // §7.4's vocabulary; null exactly when selectable
}
```

**This is the only endpoint in the API that takes a filesystem path**, so it replaces NFR-SEC-001 layer 1
(opaque ids resolved through the index) with two limits that are load-bearing rather than defence in depth:

1. **`server.browse_bases` (§3.2) is an allowlist and an empty one refuses everything.** There is no path
   that reaches a listing without first matching a configured base. The parents of the bases are outside it.
2. **The read goes through `os.Root`** opened on the matched base — the same layer-3 handle every media root
   uses. A `..` in the request, or a symlink inside a base pointing at `/etc`, is refused by the kernel at
   openat(2) and not by a string comparison. Symlinks are additionally dropped from listings, so one is never
   offered in the first place.

It is behind the **same gate as the write verbs** (`gateRootEditing()`), because browsing must never be the
first privilege an installation grants — it is a *read*, and would otherwise be reachable before anyone
turned on `allow_root_editing`.

| status | when |
|---|---|
| `403 forbidden` `disabled` · `no_config_file` · `config_inside_root` | §7.4's gate, unchanged |
| `403 forbidden` `no_browse_bases` | the gate is open but `server.browse_bases` is empty |
| `403 forbidden` `outside_browse_bases` | the path is not a base and not under one — **and this is also the answer for a path that does not exist outside the allowlist**, so the error codes cannot be used as a filesystem existence oracle |
| `400 bad_request` `not_absolute` · `control_characters` | the request's own shape, as in §7.4 |
| `400 bad_request` `does_not_exist` · `not_a_directory` · `not_readable` | inside a base, but unreadable |

`selectable` and `reason` are computed by the server from `validateRootCreate`'s own rules, from the same
helpers. The frontend must not re-derive them: a picker that greyed rows out by its own reasoning would drift
from the endpoint, and the drift would be invisible until a user clicked a folder the server then refused.

**The route is `/api/browse`, not `/api/roots/browse`.** `browse` is a legal root name under §3.2's alphabet,
and Go's `ServeMux` prefers a literal pattern over `/api/roots/{name}` — the nested spelling would have made
a root actually called `browse` undeletable, as a silent, data-dependent `405`.

### 7.5 Series

#### `GET /api/series`

| Param | Type | Default | Notes |
|---|---|---|---|
| `root` | string, repeatable | all enabled | FR-LIB-005 |
| `q` | string | — | FR-LIB-006; matches name substring, and choseong when the query is jamo/ASCII |
| `status` | `ok\|empty\|error\|all` | `all` | |
| `progress` | `any\|reading\|done\|unread` | `any` | **AMENDMENT A-4.** `reading` = has a progress row with `completed=0`; `done` = every book completed (and `book_count > 0`); `unread` = no progress row for any book. |
| `scope` | `all\|added` | `all` | **AMENDMENT A-8.** `added` = `first_seen_at` inside the recently-added window. See below. |
| `sort` | `name\|mtime\|recent\|size\|books\|added` | `name` | FR-LIB-004. `name` = natural sort (§4.7). `recent` = `progress.last_read_at`, series never read sort last. `added` = the `added_at` expression of A-8. |
| `order` | `asc\|desc` | `asc` for `name`, `desc` otherwise | |
| `offset` | int ≥ 0 | 0 | FR-LIB-007 |
| `limit` | int 1..200 | 60 | `limit=1` is the count idiom (A-8); `limit=0` is **not** legal |

```ts
// 200
interface SeriesListResponse {
  items: SeriesSummary[];
  total: number;      // matching the filter, before offset/limit
  offset: number;
  limit: number;
}
```
`400` on an unknown `sort`/`order`/`status`/`progress`/`scope` value or `limit` out of range, with
`detail: {param: "<name>"}`.

---

#### Amendment A-8 — `scope=added`, `first_seen_at`, and the 최근 추가 count

*Ruling **E-9** (`decisions.md` → "Wave-2 rulings"), binding. Tabled as **A-8** in `impl-plan.md` §0.3.*

**What it is for.** ui-spec §4.1 fixes three smart lists in the sidebar; two of them (읽는 중, 완독) are
served by A-4's `progress=`. The third, **최근 추가**, had no backing filter, so its badge showed the
whole-library total — 24 where the prototype shows 11. A-8 gives it a real one.

**The parameter.**

```
GET /api/series?scope=added
```

`scope` is a **filter**, not a sort and not a view mode. Allowed values are exactly `all` (the default,
identical to omitting the parameter) and `added`. **Any other value is `400 bad_request`** with
`detail: {param: "scope"}` — including `reading`, `done` and a root name, which are deliberately *not*
accepted here. The sidebar's five entries map to three different wire parameters and must never be
conflated:

| Sidebar entry (ui-spec §4.1) | Wire |
|---|---|
| 전체 시리즈 | *(no filter parameter)* |
| 읽는 중 | `progress=reading` (A-4) |
| **최근 추가** | **`scope=added`** (A-8) |
| 완독 | `progress=done` (A-4) |
| a root | `root={name}` (FR-LIB-005) |

The client-side `Scope` union of ui-spec §9 (`'all' | 'reading' | 'added' | 'done' | RootId`) is a
*UI* concept that fans out into those three parameters. One meaning, one spelling: nothing on the wire
may express the same set two ways.

**The predicate, exactly**, so two implementations cannot disagree:

```
scope=added   ⟺   series_seen.first_seen_at >= now − (library.recently_added_days × 86400)
```

* `now` is the server's wall clock at the moment the request is served, integer Unix seconds UTC (§7.1).
  There is no client-supplied "as of".
* The comparison is `>=` — the window is the half-open interval `[now − N·86400, ∞)`.
* It is evaluated **per request and never cached**: with the 14-day default a series leaves the list on
  the 15th day with no scan, no restart and no cache purge.
* `first_seen_at` is read from **`user.db`** (§3.6), joined as `ud.series_seen` on the index connection
  (§3.7). It is write-once, so `--rebuild-index` cannot resurrect a series into this list.
* **A series with no `series_seen` row is excluded.** `NULL >= x` is not true, and that is the intended
  behaviour: under-reporting is the safe direction, and the next scan writes the row (§3.6 rule 7).
* `library.recently_added_days` is validated to 1..3650 at startup (§3.2), so the window is never zero,
  negative or unbounded at request time.

**How it composes.** `scope` is `AND`-ed with every other filter and changes nothing else about the
endpoint:

* `root`, `q`, `status` and `progress` all still apply and compose freely.
  `?scope=added&root=manga&progress=unread&q=군계&status=ok` is legal and means the intersection of all
  five conditions.
* `total` keeps its meaning verbatim: **rows matching the whole filter, before `offset`/`limit`**.
* `offset` / `limit` are applied after filtering, unchanged (`limit` 1..200, default 60). Paging through
  `scope=added` is ordinary paging.
* **`sort` and `order` are independent of `scope`.** `scope=added` does **not** change the default sort;
  it stays `name`/`asc` as for any other request. The frontend sends `sort=added&order=desc` explicitly
  for the 최근 추가 list. (A parameter whose default silently depends on another parameter is exactly
  the kind of thing two teams implement differently.)
* `scope=added` with `sort=name` is legal and useful; so is `sort=added` with `scope=all` (which is what
  C-15 originally specified and remains valid — it re-orders the whole library rather than filtering it).

**One meaning of "added".** Before A-8 the index carried `series.added_at` ("first time we ever saw it",
§3.5), which a rebuild resets. A-8 makes `first_seen_at` the authority and `added_at` its fallback, and
every use of the word *added* in this API resolves to the same expression:

```sql
added := COALESCE(ud.series_seen.first_seen_at, s.added_at)
```

* `SeriesSummary.added_at` (§7.3) reports `added`. Same field name, same type, sharpened source.
* `sort=added` orders by `added`, default direction `desc`, ties broken by `sort_key ASC, id ASC` like
  every other sort.
* `scope=added` filters on **`first_seen_at` alone**, never the `COALESCE` — otherwise a rebuilt index
  would push the whole library into the smart list through the fallback, which is the failure A-8 exists
  to prevent.
* `index.db`'s `series.added_at` column stays: it is the fallback, and it is what a library with no
  `user.db` writes yet still has to sort by.

**The sidebar count — decided, and this is the whole design.** The count is the **`total` field of the
ordinary `SeriesListResponse`**, fetched with `limit=1`:

```
GET /api/series?scope=added&limit=1
→ 200 {"items":[ …one SeriesSummary… ], "total": 11, "offset": 0, "limit": 1}
                                                  ^^^^^^^^^^ the badge
```

There is **no** separate count endpoint, **no** counts object, and **no** new field on any existing
payload. Reasons, recorded so this is not "improved" later:

1. `total` is *already* defined as the pre-pagination match count, so it is already exactly the number
   the badge wants — for every combination of filters, by construction.
2. It is *already* how 읽는 중 and 완독 get their counts (`progress=reading|done` with `limit=1`), and
   how the section header (ui-spec §4.4) gets its `24개 시리즈`. One mechanism, four call sites.
3. The sidebar therefore never fetches every series — it transfers one `SeriesSummary` (~400 bytes) per
   smart list, which is what FR-LIB-007 requires and what E-9 asked for.
4. A `GET /api/library/counts` aggregate would be a fourth way to ask a question three parameters
   already answer, and it would have to be kept consistent with the list under every filter combination
   — a second implementation of the same `WHERE` clause, and a second thing to get wrong.

`limit=0` is **not** made legal for this: the range stays 1..200 and the count idiom is `limit=1`. The
one wasted row is cheaper than a special case in the pagination contract.

The four sidebar requests, in full, are therefore:

```
GET /api/series?limit=1                      -> total = 전체 시리즈
GET /api/series?progress=reading&limit=1     -> total = 읽는 중
GET /api/series?scope=added&limit=1          -> total = 최근 추가        <- A-8
GET /api/series?progress=done&limit=1        -> total = 완독
```
plus `series_count` per root from `GET /api/roots` (§7.4), which is unchanged.

---

#### `GET /api/series/{sid}`

```ts
// 200
interface SeriesDetail extends SeriesSummary {
  books: BookSummary[];         // natural-sorted by ord
  encoding: string | null;      // "utf-8" | "cp949" | "mixed" | null — diagnostics
}
```
`404 not_found` for an unknown `sid`.

#### `GET /api/series/{sid}/cover`

| Param | Type | Default |
|---|---|---|
| `w` | int | `thumbnails.widths[0]` — **120** under amendments A-1/A-6 (was `widths[1]` = 320). Snapped **up** to the nearest configured width, clamped to the largest |
| `v` | string | the series' `cover_cv`; enables `immutable` caching (§5.3) |

`200 image/jpeg` · `202` with `Retry-After: 1` while the cover is queued but not yet generated (the frontend shows a skeleton and retries) · `404` when the series has no cover at all (FR-LIB-008 placeholder) · `422 thumb_unavailable` when the source cannot be decoded.

§5.3's `?v=` rules apply here unchanged and are normative: `v` equal to the current `cover_cv` ⇒
`immutable`; `v` absent ⇒ `max-age=60, must-revalidate`; **`v` present but stale ⇒ `409 stale_version`
with `detail: {cv: "<current>"}`**, so the client refetches the series rather than caching a superseded
cover for a year. Same for `/thumbs/{n}` in §7.6 against the book's `cv`.

#### `POST /api/series/{sid}/rescan`

Body: none. `202 {run_id: string}` · `409 conflict` if a scan is already running. UI-002's "이 시리즈 재스캔".

### 7.6 Books and pages

#### `GET /api/books/{bid}`

```ts
// 200
interface BookDetail extends BookSummary {
  series_name: string;
  root_name: string;
  pages: PageInfo[];                    // natural-sorted, n = 1..page_count
  dims_state: "none" | "partial" | "done";
  prev_book_id: ID | null;              // FR-VWR-010 — previous book in the series
  next_book_id: ID | null;              // FR-VWR-010 — next book, null on the last
  prefs: BookPrefs;                     // effective values (book override ?? global default)
}

interface BookPrefs {
  reading_direction: ReadingDir;
  display_mode: DisplayMode;
  fit_mode: FitMode;
  is_override: boolean;                 // false => these are the global defaults
}
```
A 1,071-page book returns 1,071 `PageInfo` objects ≈ 110 KB of JSON — well inside AC-008 and far cheaper than paginating page metadata. Fetching this once is what makes arbitrary page jumps instant.

`404` for an unknown `bid`. Books with `status != "ok"` still return 200 with `pages: []` and a populated `error` — the UI needs the reason to render the badge required by design.md screen 2.

#### `GET /api/books/{bid}/pages/{n}` — **the hot path**

| Param | Type | Applies to | Notes |
|---|---|---|---|
| `v` | string | all | the book's `cv`; enables `immutable` (§5.3) |
| `w` | int | **PDF only** | render width in px, clamped to `pdf.max_width`, snapped to 100 px. Ignored for zip/dir. |

* `200` with the **original bytes** for zip/dir (FR-SRV-008), or `image/jpeg` for PDF.
* Headers: `Content-Type` from the extension, `Content-Length`, strong `ETag`, `Cache-Control` per §5.3, `X-Content-Type-Options: nosniff`, and `Accept-Ranges: bytes` only for stored entries and `dir` pages.
* `304` on a matching `If-None-Match`. `206` on `Range` where supported.
* `404` unknown book, or `n` outside `[1, page_count]`.
* `409 stale_version` when `v` is present and no longer current, with `detail: {cv: "<current>"}`.
* `501 unsupported` for a PDF page in a `nopdf` build or with `pdf.enabled: false`.
* `503 unavailable` when the media volume cannot be opened.

#### `GET /api/books/{bid}/thumbs/{n}`

| Param | Type | Default |
|---|---|---|
| `w` | int | `thumbnails.widths[0]` — **120** under amendments A-1/A-6 (was 240) — snapped up to a configured width |
| `v` | string | the book's `cv` |

`200 image/jpeg` · `202` + `Retry-After: 1` when queued · `404` · `422 thumb_unavailable` (§5.5). The thumbnail strip (FR-VWR-008) requests these lazily as it scrolls; the 202 path is what keeps a 1,071-page strip from blocking.

#### `PUT /api/books/{bid}/progress` (FR-VWR-009, FR-STT-001)

```ts
// request
interface ProgressUpdate {
  page: number;              // 1-based, clamped server-side to [1, page_count].
                             //   *** page_count === 0 means "length unknown" (a book with
                             //   status != "ok", §4.11): only the lower bound applies, so
                             //   the clamp is [1, ∞). It is NOT a 400 and NOT an empty
                             //   range. This matches the landed userdata.clampPage.
  completed?: boolean;       // omitted => auto: true when page === page_count (FR-VWR-012).
                             //   With page_count === 0 the auto rule cannot fire, so
                             //   completed stays false unless sent explicitly.
  stale_seen?: boolean;      // AMENDMENT A-14 (ruling E-45). The reader has SEEN the
                             //   "the file changed" hint. Omitted or false => an ordinary
                             //   write. true => also rebaseline the stored page_count to
                             //   the index's current length, which retires the hint —
                             //   but only when that length is known: with page_count === 0
                             //   the baseline is preserved instead (see below). No viewer
                             //   sends it in that state, because a book with no length
                             //   shows no hint (§7.3 `stale` is symmetric).
}
// 200 -> Progress
```
Idempotent; safe to send on every page turn. The frontend should debounce to ~1 s. `404` unknown book.

**AMENDMENT A-14 (ruling E-45) — an unacknowledged write preserves `page_count`.** The stored
`progress.page_count` is a *baseline*: it is the length the reader last agreed the book had, and
`Progress.stale` (§7.3) is derived by comparing it with the index. So the storage rule is:

- **INSERT** (first write for this book) stores the length it was given. There is no baseline to
  protect and the reader has just seen the book at exactly that length.
- **UPDATE** stores it **only when `stale_seen: true` and the index's length is known (`> 0`)**. Every
  other write leaves the column exactly as it was — the same treatment `started_at` already gets, and
  for the same reason: neither column describes *this* write.
- **An acknowledgement with `page_count === 0` preserves the baseline too.** `0` means "length unknown"
  (§4.11), which is what a scan leaves behind when a file goes bad, and an unknown length is not
  something a reader can agree to: what they saw was *the file changed*, not *this book is zero pages
  long*. Storing the `0` would be **permanent** — a recorded `0` is never `stale` (§7.3), so the
  baseline would match every length the book is ever repaired to and the hint could never fire again on
  any device, with `DELETE /progress` or an import the only way back. It is also what keeps
  `page_count === 0` on the wire meaning exactly one thing.

  **No client sends this**: `stale` is symmetric (§7.3), so a book with no current length shows no hint
  and there is nothing to acknowledge. The rule is here for the request that did not come from a screen
  — a hand-made call, a script — because that is the one the contract has to hold the line against. The
  two rules together are what make the warning *deferred* rather than *lost*: nothing warns while the
  file is broken, the recorded length survives it, and a repair to a different length raises the hint
  then.
- **`page` and `completed` are unaffected.** Both are still computed from the index's **current**
  length: the clamp is `[1, current]` and the auto-complete rule is `page === current`, so a reader
  can reach page 190 of a file that grew to 190 pages. What is preserved is one recorded column, not
  the number the server computes with.

The acknowledgement is an explicit field rather than an inference from "the page moved" because the
viewer writes progress on a timer whether or not anybody looked at the hint. A reader who closes the
tab one second in sends no acknowledgement, the baseline survives, and the next entry warns again —
which is the intended outcome. The field is optional, so a client that predates it never
acknowledges anything and therefore never loses a warning; strict decoding (§7.1) turns a
misspelling into `400 bad_request` rather than a silently ignored acknowledgement.

#### `DELETE /api/books/{bid}/progress` → `204`. Removes the row ("mark as unread").

#### `GET /api/books/{bid}/prefs` → `200 BookPrefs`
#### `PUT /api/books/{bid}/prefs` (FR-VWR-002)

```ts
// request — every field optional; null clears the override and falls back to the default
interface BookPrefsUpdate {
  reading_direction?: ReadingDir | null;
  display_mode?: DisplayMode | null;
  fit_mode?: FitMode | null;
}
// 200 -> BookPrefs
```

### 7.7 Continue reading (FR-LIB-010)

```
GET /api/continue?limit=20        // limit 1..50, default 20
```
```ts
// 200
interface ContinueResponse { items: ContinueItem[]; }

interface ContinueItem {
  book: BookSummary;
  series_id: ID;
  series_name: string;
  has_cover: boolean;
  progress: Progress;     // completed === false. AMENDED BY E-37: the shelf is ordered by the
                          //   SERIES' MAX(updated_at) over its unfinished books, not by this
                          //   row's own updated_at. See the amendment note below.
}
```
An empty `items` array is the signal to hide the whole shelf (design.md: "진행 중인 항목이 없으면 영역 자체를 숨김").

> **Amended by E-37 — at most ONE item per series.** The survivor is the later volume (뒷화 우선).
> The election is `ORDER BY (books.status = 'ok') DESC, books.ord DESC, books.id DESC`, taken per
> series: a volume that can actually be READ first, then the greatest `books.ord`, then the greatest
> `books.id` as a deterministic tie-break. Not the most recently read and not the furthest read —
> those come apart in practice, and the reported case is exactly that shape.
> Consequently **`limit` now bounds distinct series, not rows**: the de-duplication runs inside the SQL
> *before* `LIMIT`, precisely so that a `limit=20` whose first page happens to hold six volumes of one
> series still returns twenty cards. Filtering in Go after the query would silently return fourteen.
>
> **Corrected 2026-08-05 (still E-37 — this is that ruling's record, not a new one).** This paragraph
> said the readability key did not exist and that `ORDER BY p.updated_at DESC, b.id ASC` over the
> surviving rows was "unchanged". Both were wrong, and the second was wrong about the one thing the
> amendment did change. The shelf is ordered by the **series'** activity — `MAX(progress.updated_at)`
> over that series' *unfinished* books, `b.id ASC` to break a tie — not by the elected card's own
> `updated_at`. Once one card per series is elected by `ord`, that card's timestamp stops being a
> statement about the series: a reader who peeked at 07권 a month ago and read 01권 five minutes ago
> would see the series sink to the bottom of a shelf that shows five cards. Both keys — readability
> and series activity — were added when a review of the first implementation found the two defects
> they close; see `decisions.md` E-37 §2. Normative source: `internal/index/books.go`
> (`latestPerSeries`, `seriesActivity`).

### 7.8 Settings (UI-004)

```
GET /api/settings   -> 200 Settings
PUT /api/settings   -> 200 Settings          // partial; only the sent keys change
```
```ts
interface Settings {
  // user-mutable (persisted in user.db)
  reading_direction: ReadingDir;
  display_mode: DisplayMode;
  fit_mode: FitMode;
  prefetch: number;               // 0..20                        FR-VWR-006
  theme: "light" | "dark" | "system";
  library_view: "grid" | "list";  // FR-LIB-002 — sticky across sessions
  library_sort: SortKey;          // RULING E-15 — the closed FR-LIB-004 set of §7.5's
                                  //   `sort=`, not a free string. Anything else is
                                  //   `400 bad_request` with `detail.field="library_sort"`.
  library_order: "asc" | "desc";

  // read-only mirror of the YAML, so the settings screen can show it
  server: {
    thumbnail_widths: number[];
    scan_workers: number;
    thumb_workers: number;
    pdf_enabled: boolean;
    avif_enabled: boolean;
    auth_enabled: boolean;
    base_path: string;
    version: string;
    recently_added_days: number;  // AMENDMENT A-8 — library.recently_added_days.
                                  //   Read-only like the rest of this block. It exists
                                  //   so the 최근 추가 empty state can say "최근 14일"
                                  //   instead of hard-coding 14 in the client, and so
                                  //   the settings screen can show the window.
    config_path: string;          // AMENDMENT A-10 — the configuration file this server
                                  //   loaded, ABSOLUTE and cleaned. Read-only like the
                                  //   rest of this block. C-5 / ruling E-3 make the
                                  //   settings and onboarding screens say "shelf.yaml을
                                  //   편집한 뒤 재시작하세요"; the lookup order of §3.2
                                  //   has four candidates ($SHELF_CONFIG, ./shelf.yaml,
                                  //   $XDG_CONFIG_HOME/shelf/shelf.yaml,
                                  //   /etc/shelf/shelf.yaml), so without this field that
                                  //   sentence names no file. It is `config.Config`'s
                                  //   FilePath resolved against the process working
                                  //   directory — never the raw FilePath, which is
                                  //   relative whenever entry 2 wins — and "" only for a
                                  //   server built from a configuration with no file.
    root_editing_enabled: boolean;   // AMENDMENT A-11 — may this server write the
                                  //   `roots:` list of that file (§7.4)? It is the
                                  //   CAPABILITY, not the key: true iff
                                  //   `server.allow_root_editing` is on AND
                                  //   config_path != "" AND that file is not inside a
                                  //   configured root. One boolean for three
                                  //   conditions, exactly as `pdf_enabled` folds
                                  //   `-tags nopdf` together with `pdf.enabled` — a
                                  //   client that had to AND three fields would get it
                                  //   wrong in one of the three. The UI renders the
                                  //   추가/제거 controls only when this is true; when it
                                  //   is false the settings screen is what C-5 and
                                  //   ruling E-3 have always described.
    config_changed_on_disk: boolean; // AMENDMENT A-11 — the file at config_path is no
                                  //   longer byte-identical to the one this process
                                  //   has ADOPTED. The server hashes it at load and
                                  //   compares on every read of this endpoint.
                                  //   AMENDMENTS A-12/A-13 (rulings E-40/E-41): the
                                  //   baseline is no longer the load-time bytes alone —
                                  //   a successful §7.4 write moves it forward, so
                                  //   neither verb leaves a restart notice behind for a
                                  //   change it has already applied — but
                                  //   it is NOT "a write happened": it is equally true
                                  //   when the user hand-edited the file, which is the
                                  //   workflow C-5 has been telling them to use all
                                  //   along, and it therefore survives a browser reload
                                  //   because it is the server's state, not the tab's.
                                  //   A deleted file reads true (it differs); an absent
                                  //   config_path reads false (there is nothing to
                                  //   differ from). It flips on a comment edit too, so
                                  //   the UI must say "the configuration file changed —
                                  //   restart to apply it", never "you must restart".
  };
}
```
`PUT` accepts only the user-mutable keys; sending a `server.*` key is `400 bad_request`. Every closed-set key above — `reading_direction`, `display_mode`, `fit_mode`, `theme`, `library_view`, `library_sort`, `library_order` — rejects an out-of-set value with `400 bad_request` and `detail.field`. None of them is coerced, and none is stored and then silently ignored on the way out (ruling E-15). `library_scope` is the one deliberate exception: it may be a root name, so it is validated as non-empty, ≤128 bytes and free of control characters instead. Roots are **not** editable *here* — this endpoint never touches `roots[]`, and both new `server.*` booleans are rejected on `PUT` by the same whole-block rule as every other key in the mirror. **Amendment A-11 (ruling E-26) changed where roots are editable, not whether this endpoint edits them**: `POST /api/roots` and `DELETE /api/roots/{name}` (§7.4) write the configuration file, gated by `server.allow_root_editing`, and OQ-3 in §12 records the 2026-07-30 reversal alongside the read-only answer it replaced. `server.config_path` (A-10) remains what makes the file findable, and is now also what the client joins to a `500` from §7.4, whose message deliberately names no path (§8.4).

### 7.9 Cache (FR-THM-008)

```
GET    /api/cache/usage
DELETE /api/cache?kind=thumbs|pdf|wazero|all
```
```ts
interface CacheUsage {
  computed_at: Unix;            // the walk is cached for 60 s
  entries: {
    kind: "thumbs" | "pdf" | "wazero";
    files: number;
    bytes: number;
  }[];
  total_bytes: number;
  cache_dir: string;
}
// DELETE -> 200 {deleted_files: number, freed_bytes: number}
```

### 7.10 Scan (FR-IDX-001, FR-IDX-004)

```
POST /api/scan            body: {roots?: string[], full?: boolean}
GET  /api/scan/status
POST /api/scan/cancel
GET  /api/scan/log?limit=200&level=info|warn|error&run_id=&since_id=
```
```ts
// POST /api/scan -> 202 {run_id: string}   |   409 conflict {code:"conflict"}

interface ScanStatus {
  state: "idle" | "walking" | "indexing" | "covers" | "cancelling";
  run_id: string | null;
  full: boolean;
  started_at: Unix | null;
  finished_at: Unix | null;
  roots: string[];              // roots included in this run
  current_root: string | null;
  current_item: string | null;  // root-relative path of the item being read
  total: number;                // books discovered so far (grows during "walking")
  done: number;
  errors: number;
  covers_total: number;
  covers_done: number;
  elapsed_ms: number;
  eta_ms: number | null;        // null until a rate can be estimated
  last_error: string | null;
}

interface ScanLogEntry {
  id: number;                   // monotonic; use as since_id for incremental fetch
  ts: Unix;
  run_id: string;
  level: "info" | "warn" | "error";
  root_name: string | null;
  rel_path: string | null;
  message: string;
}
// GET /api/scan/log -> 200 {items: ScanLogEntry[]}
```

**`POST /api/scan` — every status, including the two `roots[]` produces.** The body is optional (`{}`,
`{"full": true}`, or nothing at all) and `roots` is the only part of it that can be wrong. Both of its
refusals were undocumented until 2026-07-30, and A-11's revision R1 widened one of them.

| Status | When |
|---|---|
| `202` | accepted; `{run_id}` |
| `400 bad_request` | a name in `roots[]` is **not in the configuration** — `detail: {param: "roots"}`. A bad parameter and not a missing resource: the request cannot be made to work by retrying it, because the client built it out of nothing. Also malformed JSON and an unknown body field (§7.1 strict decoding). |
| `404 not_found` | **AMENDMENT A-11 / R1** — a name in `roots[]` *was* a configured root and this process has since removed it by `DELETE /api/roots/{name}`. Deliberately not the `400` above: this name was right, and the resource has gone. It is the same split §7.1 draws between a malformed id and an unknown one. |
| `409 conflict` | a scan is already running (one writer goroutine, §4.1). Also the case where **every** configured root has been removed in this process's lifetime and the body named none: an empty `roots[]` means "all enabled roots" to the scanner, which is the opposite of what that caller asked for. |
| `503 unavailable` | the server is shutting down |

A **full** scan (no `roots` in the body) silently skips the removed ones rather than refusing: it is a request
about the library as it now stands, and the removed root is not part of it. Naming one explicitly is a
statement about that root, which is why it gets an answer instead.

**The `202` is not sent before the run is visible.** `Scanner.Start` publishes this run's `ScanStatus`
— `run_id`, `state:"walking"`, `started_at` — **before it returns**, so the handler cannot answer `202` while
the snapshot still describes the previous run. That ordering is part of the contract, not an implementation
detail: the client answers a `202` by invalidating `['scan','status']` and polling once, and its poll stops
the moment it reads `idle`. A poll that landed on the previous run's idle snapshot was therefore the *last*
poll of the run, and the UI sat at `스캔 대기` for the whole scan with no second chance — `refetchOnWindowFocus`
is off and the query is mounted at the router root, so nothing re-armed it. Any snapshot a caller can reach
after the `202` now belongs to the run that `202` announced. *(Fixed 2026-08-06; regression test
`TestScan_start_publishesThisRunsStatusBeforeItReturns`.)* One consequence worth naming: the `400` row above
was **unreachable** until this change — `Start` resolved `roots[]` inside its background goroutine, so an
unknown root was logged and answered `202`. It is resolved on the caller's goroutine now, which is what makes
`httpapi.scanStartError`'s `ErrUnknownRoot` branch fire.

**Polling, not SSE — and why.** `GET /api/scan/status` is the normative mechanism. The frontend polls at **1 s while `state !== "idle"`** and stops when idle (re-arming on a user-initiated scan). Rationale: a full cold scan of the reference collection takes **32 s**, so a whole run costs ~32 requests against an in-memory atomic snapshot — a rounding error. Meanwhile SSE would permanently consume one of the browser's six HTTP/1.1 connections per origin (the viewer needs those for page prefetch, FR-VWR-006), needs `X-Accel-Buffering`/`proxy_buffering off` handling behind the reverse proxy that NFR-SEC-003 explicitly anticipates, and needs reconnect logic. 1 s polling satisfies FR-IDX-004's "real time" for a job with second-scale granularity. **SSE is explicitly out of scope for v1**; if it is added later it will carry the identical `ScanStatus` payload at `GET /api/scan/stream`, and the frontend must keep working without it.

### 7.11 Progress export/import (FR-STT-004)

```
GET  /api/progress/export      -> 200 ProgressExport   (Content-Disposition: attachment)
POST /api/progress/import      -> 200 {imported: number, skipped: number, conflicts: number}
```
```ts
interface ProgressExport {
  format: "shelf-progress/1";
  exported_at: Unix;
  id_version: "shelf-id/1";     // importer refuses a mismatch
  items: {
    book_id: ID;
    series_id: ID;
    root_name: string;
    book_path: string;          // lets an importer re-derive ids after a rename
    last_page: number;
    page_count: number;
    completed: boolean;
    started_at: Unix;
    updated_at: Unix;
  }[];
  prefs: { book_id: ID; reading_direction: ReadingDir | null;
           display_mode: DisplayMode | null; fit_mode: FitMode | null }[];
}
```
Import merges by `book_id`, keeping the row with the newer `updated_at`; `?strategy=replace` overwrites unconditionally.

### 7.12 Auth (NFR-SEC-002)

```
GET  /api/auth/status  -> 200 {auth_required: boolean, authenticated: boolean}   // never 401
POST /api/auth/login   body {password: string}  -> 204 + Set-Cookie   |  401 unauthorized
POST /api/auth/logout  -> 204
```
When auth is enabled, every other `/api/*` route and every static asset requires the session cookie and returns `401 unauthorized` without it. The SPA calls `/api/auth/status` first and renders a login screen when `auth_required && !authenticated`.

### 7.13 Endpoint summary against prd 6.3

| prd 6.3 | This spec | Status |
|---|---|---|
| `GET /api/roots` | §7.4 | as specified |
| — | `POST /api/roots`, `DELETE /api/roots/{name}` | **added by amendment A-11** (ruling E-26) — prd 6.3 has no write verb on roots, and D-33 / E-3 originally read that as a prohibition; the requirement's owner has since extended prd 5.2 UI-004. Off by default (`server.allow_root_editing`), restart-based, non-destructive. |
| `GET /api/series` | §7.5 | + filter/sort/pagination params |
| `GET /api/series/{sid}` | §7.5 | + `books[]` inline |
| `GET /api/series/{sid}/cover` | §7.5 | + `w`, `v` |
| `GET /api/books/{bid}` | §7.6 | + `pages[]`, prev/next, prefs |
| `GET /api/books/{bid}/pages/{n}` | §7.6 | + `v`, `w` (PDF) |
| `GET /api/books/{bid}/thumbs/{n}` | §7.6 | + `w`, `v` |
| `PUT /api/books/{bid}/progress` | §7.6 | as specified |
| `POST /api/scan` | §7.10 | + `roots`, `full` |
| `GET /api/scan/status` | §7.10 | as specified |
| `GET/PUT /api/settings` | §7.8 | as specified |
| — | `/api/health`, `/api/continue`, `/api/books/{bid}/prefs`, `DELETE progress`, `/api/scan/cancel`, `/api/scan/log`, `/api/cache/*`, `/api/progress/*`, `/api/auth/*`, `POST /api/series/{sid}/rescan` | **added** — each required by an FR that prd 6.3's overview table did not enumerate |

---

## 8. Security

### 8.1 Path traversal is impossible by construction (NFR-SEC-001)

There are **four independent layers**, and the first alone is sufficient.

**Layer 1 — no user-supplied path ever reaches the filesystem.** Every route takes an opaque 16-character id. Resolution is always:

```
bid ──SELECT root_name, rel_path FROM books WHERE id = ?──> (root_name, rel_path)
root_name ──config lookup──> absolute, cleaned root path
open(rootPath, rel_path)
```
The only strings that reach `open` are (a) an absolute path from the operator's own YAML and (b) a relative path **this program produced** by walking below that root. A caller cannot express `../`: `../../etc/passwd` is not a valid id, and an id that does not exist in the index is a 404. `pages/{n}` takes an **integer index**, never a name, so ZIP entry names — which may legitimately contain `../` — never touch the filesystem either; they are display strings and offsets.

**Layer 2 — `filepath.IsLocal` on the write path.** Before a `rel_path` is inserted, `filepath.IsLocal(rel)` must be true and no `/`-separated element may be `.` or `..`. **VERIFIED**: `IsLocal("../x")=false`, `IsLocal("/abs")=false`, `IsLocal("a/../../b")=false`, `IsLocal("")=false`. Note that `IsLocal(` `..\win` `)` is **true on Linux** because backslash is an ordinary filename byte there — hence the explicit element check on the slash-split path in addition, so the same data is safe when the index is carried to Windows.

**Layer 3 — `os.Root` (Go 1.24+) per root.** Each enabled root is opened once with `os.OpenRoot(path)` and every file access goes through `root.Open(rel)`, which the kernel-level `openat`-based implementation refuses to let escape — **including through symlinks**. **VERIFIED**:

```
root.Open("ok.txt")          -> OK
root.Open("../secret.txt")   -> REFUSED  openat ../secret.txt: path escapes from parent
root.Open("escape.txt")      -> REFUSED  (a symlink pointing outside the root)
root.Open("up/secret.txt")   -> REFUSED  (a symlink to "..")
root.Open("sub/../ok.txt")   -> OK       (stays inside; correctly allowed)
```
This also settles `scan.follow_symlinks: false` — a symlink out of the root is unreadable even if the index somehow named it.

**Layer 4 — final assertion.** `strings.HasPrefix(filepath.Clean(joined), rootAbs+string(os.PathSeparator))` immediately before any open, returning `500 internal` and logging at `error` if it ever fails. It never should; if it does, that is a bug worth screaming about.

### 8.2 Optional password auth (NFR-SEC-002)

* At startup: `auth.password_hash` is used as-is; `auth.password` is bcrypt-hashed in memory (cost 12) and the plaintext is zeroed. `shelf hash-password` prints a hash for the operator so the plaintext need never sit in YAML.
* `POST /api/auth/login` compares with `bcrypt.CompareHashAndPassword` (constant-time by construction) and applies a **per-IP token bucket: 5 attempts per minute, burst 5**; beyond that, `429` with `Retry-After`. Failures always take ≥250 ms.
* On success, `Set-Cookie: shelf_session=<token>; Path={base_path}/; HttpOnly; SameSite=Lax; Max-Age=<session_ttl>` and `Secure` when the request arrived over TLS (directly, or via `X-Forwarded-Proto: https` when `server.trusted_proxy_headers` is on).
* Token = `base64url(payload) + "." + base64url(HMAC-SHA256(key, payload))`, `payload = {"iat":…,"exp":…,"v":1}` JSON. Verified with `hmac.Equal`. The key is 32 random bytes at `<data_dir>/session.key`, mode `0600`, generated on first boot. Deleting it invalidates every session.
* `SameSite=Lax` plus the fact that **no state-changing endpoint is a `GET`** (all mutations are `POST`/`PUT`/`DELETE`, which `Lax` does not send cross-site) removes the need for CSRF tokens in v1.
* Auth is **all-or-nothing**: with it enabled, static assets are protected too, so an unauthenticated visitor cannot even enumerate the SPA's routes. `/api/health` and `/api/auth/*` are the only exemptions.

### 8.3 Base path (NFR-SEC-003)

`server.base_path` is normalised at startup (ensure a leading `/`, strip trailing `/`, reject `..`) and the whole application is mounted with `http.StripPrefix`. **VERIFIED** under `/reader`: `GET /reader/api/books/{bid}/pages/{n}` → 200 with correct `PathValue`s; `PUT /reader/api/books/{bid}/progress` → 200; `GET /reader/` → index; `GET /reader/series/xyz` → SPA fallback; `POST` to a `GET`-only route → **405**.

The base path is injected into the served `index.html` as `<base href="{base_path}/">` and exposed at `settings.server.base_path`, so the SPA builds URLs without hard-coding it. A request to `{base_path}` without the trailing slash is `308`-redirected to `{base_path}/`.

### 8.4 Miscellaneous hardening

* `X-Content-Type-Options: nosniff` on every response; `Referrer-Policy: same-origin`; `Content-Security-Policy: default-src 'self'; img-src 'self' data: blob:; style-src 'self' 'unsafe-inline'; object-src 'none'; frame-ancestors 'none'` (`unsafe-inline` only until the Vite build is verified to emit no inline styles).
* `server.read_header_timeout` guards slowloris. There is deliberately **no** `WriteTimeout` — it would truncate large page streams.
* Request bodies are capped with `http.MaxBytesReader` (1 MiB for JSON, 32 MiB for `progress/import`).
* Error messages returned to the client never contain absolute filesystem paths outside the `roots[].path` already visible in `GET /api/roots`.

---

## 9. Observability (NFR-OPS-005)

`log/slog`, no logging dependency. Handler chosen by `log.format`: `slog.NewTextHandler` or `slog.NewJSONHandler`, level from `log.level`, exposed as a `slog.LevelVar` so a future `PUT /api/settings` can change it live.

Level policy:

| Level | Used for |
|---|---|
| `debug` | per-entry scanner decisions, cache hit/miss, LRU evictions, skipped non-media files |
| `info` | startup config summary (**passwords and the session key are never logged**), scan start/finish with counts and duration, scan progress every 5 s, cache purge, shutdown |
| `warn` | a book that failed to index (with root, path, reason), a thumbnail that could not be generated, an unreachable root, a `409 stale_version`, an index row whose root left the config |
| `error` | database errors, a layer-4 path assertion failure, a panic recovered in a handler |

Standard attributes: `req_id`, `method`, `path`, `status`, `bytes`, `dur_ms`, `remote`; and for scan work `run_id`, `root`, `rel_path`, `book_id`. One line per HTTP request when `log.http_requests` is on, with image endpoints demoted to `debug` so a 1,071-page read does not drown the log.

Scan diagnostics are additionally persisted to `scan_log` and surfaced at `GET /api/scan/log` for the UI-004 "스캔 로그 열람" panel — the operator should not need shell access to find out why a series is broken.

No metrics endpoint in v1; `GET /api/health?verbose=1` returns the pool counters (LRU size/hits/misses, thumb queue depths, goroutine count, `runtime.MemStats.HeapAlloc`/`Sys`, process RSS) which is enough for a single-user deployment.

---

## 10. Test strategy

### 10.1 Unit tests (no external data, run on every commit)

| Package | What is asserted |
|---|---|
| `natsort` | The full table from §4.7 verbatim, including the real Korean names. Property test: `sign(Compare(a,b)) == sign(bytes.Compare(Key(a),Key(b)))` over 100k generated strings mixing digits, zero-padding, ASCII case, Hangul, Hanja and fullwidth forms. Total-order laws: antisymmetry, transitivity, and stability of `sort.SliceStable` under shuffling. Overflow: 22-digit numbers. |
| `kenc` | The §4.4 decision table: UTF-8 flag set/valid, flag set/invalid, no flag + ASCII, no flag + valid UTF-8 (**must return the UTF-8 string, not mojibake**), no flag + CP949 bytes, no flag + garbage. Golden vectors captured from the real collection: `"\xbd\xb4\xc6\xdb\xb8\xb8\xc8\xad\xb5\xa5\xbb\xfd"` → `"슈퍼만화데생"`, `"\xc7\xd1\xb1\xdb.jpg"` → `"한글.jpg"`. Regression guard asserting the decoder still returns `nil` error on garbage, so the U+FFFD check cannot be silently removed. |
| `ids` | Golden vectors pinning the exact strings from §3.4 (`ruzwlotzngls2ua5`, `yvtfrny77ehkt2we`). A second test rebuilds §3.4's hash-input byte diagram from literals and asserts the package agrees, so the *construction* is pinned and not just two opaque strings. `IDVersion == "shelf-id/1"`. Determinism across runs and platforms; `series` vs `book` domain separation on the **same** rel path; that a Windows-style backslash path and its slash form produce the same id; `Valid` accepts exactly `[a-z2-7]{16}`. |
| `archive/zipidx` | Central-directory parsing against hand-built fixtures: stored + deflate, UTF-8-flagged and CP949 names, nested directories, `__MACOSX/`, `Thumbs.db`, a 0-byte entry, an encrypted entry, an archive with a 40 KiB trailing comment (forcing the 64 KiB tail path), a truncated file, a file with a bogus EOCD, and **a real ZIP64 archive**. Plus `DataOffset` arithmetic and the 30-byte local-header parse. |
| `zipidx` **differential** | For every fixture and every archive under `SHELF_TEST_ROOT` when set: entry count, names, method, CRC, sizes and `DataOffset` must equal `archive/zip`'s, and the *error verdict* must agree. This test is what lets us replace the stdlib reader with confidence. |
| `scanner/classify` | Synthetic trees built in `t.TempDir()` reproducing **every row of prd 2.2** plus each real-world shape from §0: `N zips + 1 cover image`, `N zips + subdirs of images`, two-level nesting, images-only, pdf-only, an empty/`.txt`-only directory, and a directory holding both `01권/` and `01권.zip`. |
| `scanner/incremental` | Touching a file changes the fingerprint; touching nothing skips; `full: true` never skips. |
| `thumbs` | Cache path derivation is exactly `<h[0:2]>/<h[2:4]>/<h>.jpg`; a changed `content_version` yields a different path (FR-THM-006); the `.tmp` + rename publish is atomic under concurrent readers; single-flight coalesces N concurrent misses into one decode. |
| `hangul` | The §4.8 vectors; jamo passthrough; non-Hangul passthrough. |
| `httpapi` | Every endpoint via `httptest`: status codes, the §7.2 error envelope, strict JSON decoding, id validation, `base_path` mounting, 405 on the wrong method, auth gating. **JSON golden files** under `internal/httpapi/testdata/golden/` that the frontend can diff to catch contract drift. |
| `config` | Defaults, lookup order, every validation rejection, and that `shelf.example.yaml` round-trips to the documented defaults. |

### 10.2 Integration tests (`-tags integration`, opt-in)

Gated on `SHELF_TEST_ROOT`; skipped when unset so CI never depends on the 5 TB volume.

1. **Full scan** of the real root: completes without panic; `books.status='error'` count ≤ 0.2 % (baseline **0.08 %**); series count within ±2 of 963.
2. **AC-002**: sample 1,000 indexed page names; assert zero contain U+FFFD and that names from the known-CP949 archives match golden strings.
3. **AC-003**: pick one folder-type and one zip-type series; the `SeriesDetail` → `BookDetail` → `pages/1` flow must be byte-identical in shape.
4. **AC-004**: the same flow for `[만화] 미생 1~9 (완결 pdf)`, returning `image/jpeg`.
5. **NFR-PRF-006 / AC-001**: read all pages of a 500+-page book while watching `RSS`; assert peak RSS growth < 64 MiB and that `find <tmpdir> -newer <marker>` is empty.
6. **AC-005 / AC-006**: write progress, delete `index.db*` and the whole cache, restart, rescan, assert progress and prefs are intact and covers regenerate.
7. **NFR-PRF-004**: a second scan immediately after the first completes in < 30 s.
8. **AC-008**: 50 random page jumps in the 1,071-page archive, p95 time-to-first-byte < 100 ms warm.

### 10.3 Synthetic fixtures (`internal/testutil`)

So unit tests are hermetic and fast:

As landed in wave 1. Note every helper takes `testing.TB`, not `*testing.T`, so it is usable from benchmarks and fuzz targets:

```go
func BuildZIP(t testing.TB, spec ZIPSpec) []byte                 // full control of GP flags, method,
                                                                 // raw name bytes, comment length
func BuildZIP64(t testing.TB, spec ZIPSpec, z64 ZIP64Spec) []byte // forces the 0x0001 extra field and
                                                                 // the ZIP64 EOCD even for tiny files,
                                                                 // by writing the archive by hand
func CP949(t testing.TB, s string) []byte          // raw CP949 name bytes for kenc fixtures

func TinyJPEG(t testing.TB, w, h int) []byte       // ~200 bytes
func TinyPNG(t testing.TB, w, h int) []byte
func TinyGIF(t testing.TB, w, h int) []byte
func TinyBMP(t testing.TB, w, h int) []byte
func TinyTIFF(t testing.TB, w, h int) []byte
func TinyWebP(t testing.TB) []byte
func TinyAnimatedWebP(t testing.TB) []byte         // the §5.5 422 thumb_unavailable path
func TinyAVIF(t testing.TB) []byte

func BuildTree(t testing.TB, layout map[string]any) string             // a series tree in t.TempDir()
func BuildTreeAt(t testing.TB, dir string, layout map[string]any) string
func Touch(t testing.TB, path string, d time.Duration)                 // shift mtime — incremental-scan tests

var Dir = map[string]any{}                         // the BuildTree marker for an empty directory
const CommentSize40KiB = 40 * 1024                 // forces the 64 KiB EOCD tail path
```
All fixtures are byte-generated, not committed binaries, except a handful of hand-crafted malformed archives under `testdata/` (bad EOCD, truncated CD, 40 KiB comment) that must be exact. Total fixture footprint < 200 KB; the unit suite runs in a couple of seconds.

### 10.4 Benchmarks

`BenchmarkCentralDir` (per archive), `BenchmarkOpenEntry` (per page), `BenchmarkThumbnail` (decode + Lanczos + encode), `BenchmarkNatsortKey`, `BenchmarkSeriesList` (1,000 rows with the progress join). Run with `-benchmem` in CI on a fixed fixture set; a >20 % regression fails the build.

---

## 11. Build

Every `go` invocation carries the proxy prefix. `CGO_ENABLED=0` everywhere (CON-001).

```bash
export GOPROXY=https://proxy.golang.org,direct
export GOSUMDB=sum.golang.org
export GOTOOLCHAIN=auto

# frontend first — go:embed needs web/dist to exist
cd web && pnpm install --frozen-lockfile && pnpm build && cd ..

# single static binary (NFR-OPS-001)
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-s -w \
    -X shelf/internal/buildinfo.Version=$(git describe --tags --always) \
    -X shelf/internal/buildinfo.Commit=$(git rev-parse --short HEAD) \
    -X shelf/internal/buildinfo.Date=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o dist/shelf ./cmd/shelf
```

Cross-compile targets (NFR-OPS-003; **VERIFIED** to build cgo-free for the sqlite spike on `linux/arm64`, `linux/arm`, `windows/amd64`, `darwin/arm64`):

| GOOS | GOARCH | Note |
|---|---|---|
| linux | amd64, arm64, arm (GOARM=7) | primary NAS targets |
| windows | amd64, arm64 | `.exe` |
| darwin | amd64, arm64 | |

### Makefile sketch

```makefile
GO       ?= go
GOENV    := GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org GOTOOLCHAIN=auto
BUILDENV := $(GOENV) CGO_ENABLED=0
PKG      := shelf
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     := $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS  := -s -w -X $(PKG)/internal/buildinfo.Version=$(VERSION) \
                  -X $(PKG)/internal/buildinfo.Commit=$(COMMIT) \
                  -X $(PKG)/internal/buildinfo.Date=$(DATE)
TAGS     ?= noavif      # E-21: the default artefact must be static

.PHONY: all web build run test test-int bench lint fmt tidy release clean check-readonly

all: build

web:
	cd web && pnpm install --frozen-lockfile && pnpm build

build: web
	$(BUILDENV) $(GO) build -trimpath -tags "$(TAGS)" -ldflags "$(LDFLAGS)" -o dist/shelf ./cmd/shelf

# fast loop: skip the frontend build, serve whatever is already in web/dist
dev:
	$(BUILDENV) $(GO) run ./cmd/shelf --config ./shelf.yaml --log-level debug

# CGO_ENABLED=1 here and ONLY here: the race detector is written in C, so
# `go test -race` under CGO_ENABLED=0 fails outright with "-race requires cgo".
# CON-001 constrains the shipped BINARY, which build/release produce cgo-free;
# it does not constrain the test runner. Two passes: untagged is the superset
# that carries a real AVIF decoder, `-tags "$(TAGS)"` is what ships (E-21).
test:
	$(GOENV) CGO_ENABLED=1 $(GO) test ./... -race -count=1
	$(GOENV) CGO_ENABLED=1 $(GO) test -tags "$(TAGS)" ./... -race -count=1

test-int:
	$(GOENV) CGO_ENABLED=0 SHELF_TEST_ROOT="$(SHELF_TEST_ROOT)" \
		$(GO) test -tags integration ./... -count=1 -timeout 30m

bench:
	$(GOENV) CGO_ENABLED=0 $(GO) test ./... -run '^$$' -bench . -benchmem

lint: check-readonly
	$(GOENV) $(GO) vet ./...
	$(GOENV) $(GO) run honnef.co/go/tools/cmd/staticcheck@latest ./...

# FR-CFG-005 / NFR-DAT-002 guard: nothing under internal/{scanner,source,archive,openpool}
# may reference a filesystem mutation primitive.
check-readonly:
	@! grep -rnE '\bos\.(Create|Remove|RemoveAll|Rename|Mkdir|MkdirAll|Chtimes|Chmod|Truncate|WriteFile|OpenFile)\b' \
		internal/scanner internal/source internal/archive internal/openpool \
		|| (echo "FR-CFG-005 violation: write primitive in a media-reading package"; exit 1)

fmt:
	$(GOENV) $(GO) fmt ./...

tidy:
	$(GOENV) $(GO) mod tidy

# Two variants per target: the static default, and the opt-in `-avif` build.
AVIF_TAGS := $(filter-out noavif,$(TAGS))

RELEASE_TARGETS := linux/amd64 linux/arm64 linux/arm windows/amd64 windows/arm64 \
                   darwin/amd64 darwin/arm64

# The E-21 gate, run against the artefact rather than the flags. NO `-run`
# filter: a pattern that matches nothing makes `go test` print "[no tests to
# run]" and exit 0, so the caller succeeds with the gate never having executed.
STATICCHECK := $(GOENV) CGO_ENABLED=0 $(GO) test ./internal/buildinfo -count=1

# Which artefacts the gates assert on, derived from the target list so that
# "every linux target" is not a claim anyone has to maintain by hand.
LINUX_TARGETS    := $(filter linux/%,$(RELEASE_TARGETS))
STATIC_ARTEFACTS := $(foreach t,$(LINUX_TARGETS),dist/shelf-$(VERSION)-$(subst /,-,$(t)))
AVIF_ARTEFACTS   := $(foreach t,$(LINUX_TARGETS),dist/shelf-$(VERSION)-$(subst /,-,$(t))-avif)

release: web
	@mkdir -p dist
	@rm -f dist/shelf-*-* dist/SHA256SUMS dist/ARTIFACTS.txt   # own the output set
	@for t in $(RELEASE_TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; ext=""; [ "$$os" = "windows" ] && ext=".exe"; \
	  for v in default avif; do \
	    tags="$(TAGS)"; sfx=""; \
	    if [ "$$v" = "avif" ]; then tags="$(AVIF_TAGS)"; sfx="-avif"; fi; \
	    GOOS=$$os GOARCH=$$arch GOARM=7 $(BUILDENV) $(GO) build -trimpath -tags "$$tags" \
	      -ldflags "$(LDFLAGS)" -o dist/shelf-$(VERSION)-$$os-$$arch$$sfx$$ext ./cmd/shelf || exit 1; \
	  done; \
	done
# Deliberately OUTSIDE the loop, on its own recipe line: inside it the verdict
# depended on a trailing `|| exit 1` (this recipe has no `set -e`), and deleting
# those five characters shipped a dynamic artefact with `make release` reporting
# success. On its own line, make's error handling is the enforcement.
	@SHELF_STATIC_ARTEFACTS="$(STATIC_ARTEFACTS)" \
	 SHELF_AVIF_ARTEFACTS="$(AVIF_ARTEFACTS)" $(STATICCHECK)
	@cd dist && sha256sum shelf-$(VERSION)-* > SHA256SUMS   # + dist/ARTIFACTS.txt

clean:
	rm -rf dist web/dist/* && touch web/dist/.gitkeep
```

### Build tags and linkage (ruling E-21)

`nopdf` (−8.34 MB, PDF endpoints return `501 unsupported`) · `noavif` (−1.58 MB, `.avif` thumbnails return `422 thumb_unavailable` with `detail.reason: "avif_disabled"`).

**`noavif` is on by default, and the reason is linkage, not size.** `CGO_ENABLED=0` does not by itself produce a static binary: `internal/thumbs` blank-imports `github.com/gen2brain/avif`, which depends on `github.com/ebitengine/purego`, and purego emits dynamic import directives independently of cgo. Measured on linux/amd64:

| Build | Linkage | Size |
|---|---|---|
| default (`-tags noavif`) | **static** — no `PT_INTERP`, no `DT_NEEDED` | 25 833 656 B |
| `-avif` variant (no tags) | dynamic — `libc.so.6`, `libdl.so.2`, `libpthread.so.0` | 27 418 916 B |
| `-tags "noavif nopdf"` | static | 15 286 456 B |

CON-001 names the *purpose* — "정적 단일 바이너리와 손쉬운 크로스 컴파일" — and NFR-OPS-003 makes NAS (Synology/QNAP/Alpine: musl, or an older glibc) the primary target, where the dynamic artefact does not start. FR-IDX-011 lists `avif` as 필수, so this is a real requirement conflict, resolved on evidence: zero `.avif` files appear in **every sample taken** — two independent passes, `docs/data-survey.md`'s 500-ZIP scan (≈4.5 % of the 11,157 archives) and §1.1's ~56k-entry extension census — and degradation is graceful (the original bytes still stream from `/pages/{n}`; only the server-side thumbnail is refused). `make release` therefore emits **both** variants for all seven targets, both in `SHA256SUMS`, described in `dist/ARTIFACTS.txt`.

Because the default build cannot decode AVIF, `avif_enabled` on `/api/health` (§7.4) and `/api/settings` (§7.11) is `thumbnails.avif_enabled && thumbs.AVIFSupported()` — the same two-halves gate `pdf_enabled` uses for `-tags nopdf`. The config key defaults to true, so reporting it alone would advertise a decoder the shipped binary does not contain.

This is asserted, not assumed: `internal/buildinfo/staticlink_test.go` reads the produced artefact's ELF headers (`debug/elf`, never `ldd`) and fails on a `PT_INTERP`/`.interp` or any `DT_NEEDED` entry. Where it runs matters, and is easy to overstate:

* In `make build-go` and `make release` it is **mandatory**. Both pass `SHELF_STATIC_ARTEFACTS` (and `release` also `SHELF_AVIF_ARTEFACTS`) naming exactly what they just linked, where a missing or dynamic file is fatal. `release` derives that list as `$(filter linux/%,$(RELEASE_TARGETS))`, so all three linux arches are covered — the `DT_NEEDED` sets differ between them. The `-avif` list gets the **inverse** assertion (`TestAVIFVariantCarriesTheDecoder`): each of those artefacts must contain the `github.com/gen2brain/avif` symbols and, being ELF, must be dynamic. Without it, `AVIF_TAGS := $(TAGS)` would emit seven binaries that `SHA256SUMS` lists and `ARTIFACTS.txt` describes as carrying a decoder they do not carry, with every other gate green.
* In `make test` it **skips** unless `dist/` happens to be populated: `dist/` is gitignored and linking a 26 MB binary (which first needs `pnpm build`) is not a unit test. What never skips there is `TestDefaultBuildIsConfiguredStatic`, which asserts the *configuration* — that `make print-TAGS` still expands to something containing `noavif`, that both recipes still invoke the gate, and that the gate is not filtered down with a `-run` pattern (a non-matching one makes `go test` exit 0 with "[no tests to run]").

The defect it guards survived four review passes because every earlier check looked at build flags. `make test` runs the whole suite **twice** — untagged, then `-tags "$(TAGS)"` — because nothing else executes the tests in the configuration that ships; `make lint` runs `go vet -tags "$(TAGS)"` for the same reason, but vet does not run tests.

Docker (NFR-OPS-004, phase 3): `FROM scratch`, copy the **default** (static) binary plus `/etc/ssl/certs/ca-certificates.crt`, `VOLUME /config /data /cache`, `EXPOSE 8080` — the `-avif` variant cannot run on `scratch`.

---

## 12. Risks and open questions

### 12.1 Retired risks (prd §10)

| prd risk | Status |
|---|---|
| WASM PDF rasterisation too slow | **Retired.** 296 ms/page on a real 284-page file, cached to disk after the first render, 135 ms warm init. Ship in v1 (§1.3). |
| Pure-Go image resize throughput | **Retired.** 67 covers/s at 16 workers → all 11,157 covers in 167 s. `imaging` is 10× faster than the obvious `x/image/draw` choice; picking the wrong one here would have been a 10× regression nobody would have noticed. |
| Many broken/non-standard ZIPs | **Quantified.** 9 of 11,157 (0.08 %), all truncated, all isolated to a `status='error'` book. |
| Go ecosystem maturity for PDF/images | **Retired.** Every format in FR-IDX-011 including AVIF decodes with `CGO_ENABLED=0`. |

### 12.2 Live risks

| Risk | Impact | Mitigation |
|---|---|---|
| AVIF decode is 1.1 s and ~170 MiB resident | A single `.avif` page could dominate the thumbnail queue | Lazy init, 1-permit semaphore, `thumbnails.avif_enabled` kill switch. Zero AVIF files exist in the target collection today. |
| 1.36 M `pages` rows | `index.db` grows to a few hundred MB | `WITHOUT ROWID` + a covering PK. Measured: 200k inserts in 1.67 s, point queries 0.1 ms under concurrent writes. |
| Cold-storage latency (spinning disk / NAS) | Cold scan 147 archives/s at 4 workers vs 346 at 16 | `scan.workers` is tunable; the default scales with `NumCPU`. |
| Positional page ids | Inserting a page into an archive shifts progress | `progress.page_count` is stored; the API returns `stale: true` so the UI can warn instead of silently jumping. |
| `imaging` internal parallelism vs our worker pool | Oversubscription at high `thumbnails.workers` | Documented; default 4. |

### 12.3 Decisions requested from the orchestrator

> **All eight are now ANSWERED** — see the binding *"Escalation rulings (orchestrator, 2026-07-28)"* table at the end of `decisions.md`, plus D-37 (OQ-7). This table is kept for the reasoning, not as an open list. Where a recommendation below was overruled, the ruling wins.

| # | Question | Recommendation |
|---|---|---|
| **OQ-1** | ~~**Module path and product name.**~~ **CLOSED (E-1):** the module path is the bare **`shelf`**, binary `shelf`, brand "SHELF". This document originally assumed `github.com/hwangtaeseung/shelf`; every reference has been corrected. | Ruled: bare path. The app is embedded and never `go get`-ed. |
| **OQ-2** | **PDF in v1 or v2?** prd §9 defers PDF to release 2, but it is proven working today (§1.3) and AC-004 requires it. | **Ship PDF in v1.** The risk that justified deferring it no longer exists. Keep the `nopdf` tag as an escape hatch. |
| **OQ-3** | **Root management from the UI.** design.md screen 4 says "루트 관리: 목록, 추가/제거", but FR-CFG-001 makes the YAML the source of roots and there is no `POST /api/roots` in prd 6.3. Writing YAML from a web UI is a meaningful security and correctness surface. | **ANSWER CHANGED 2026-07-30 — ruling E-26 / amendment A-11: add and remove are IN, gated and restart-based.** `POST /api/roots` and `DELETE /api/roots/{name}` (§7.4) write the `roots:` list of the configuration file; the running server adopts nothing until restart; both verbs are `403 forbidden` unless `server.allow_root_editing` (§3.2) is on, which is **false** by default. ~~Removal keeps every index row and all reading progress.~~ **REVISED the same day — R1**: removal purges that root's `index.db` rows in one transaction and keeps **all reading progress** (`user.db` is untouched), because a root that stayed listed after the restart is not what 제거 means; **R2**: an added root appears at once as a `pending: true` row. Both are in decisions.md E-26 "REVISION 2026-07-30", and neither adopts the root into the running server. The "proper config-writer" this row parked in phase 3 is the writer that landed: atomic rename, preserved comments, `.bak`. `Settings.server` reports the capability and whether the file has changed under the running process (§7.8). **The 2026-07-28 answer is kept below verbatim, because it is why `internal/httpapi/roots.go` was written the way it was and it remains the behaviour whenever the gate is shut.** — ~~**v1: read-only.** `GET /api/roots` + per-root rescan; the settings screen shows roots with an "edit shelf.yaml to change" note. **The note must name the file** — `Settings.server.config_path`, amendment **A-10** / ruling **E-25**, the absolute path this server loaded, shown by `RootsPanel` and by the onboarding screen. Read-only is only an answer if the reader can find the file, and the lookup order has four candidates. Promote to phase 3 with a proper config-writer if wanted.~~ **What did not change**: the YAML is still the source of truth (FR-CFG-001), the settings screen is still not a general configuration editor, there is still no filesystem browser and no directory-listing endpoint, and everything else in prd 5.2 UI-004 stands. |
| **OQ-4** | **Duplicate books.** `군계` really contains `01권/` (folder) **and** `01권.zip`, plus `07권.zip`, `07권.repair.zip`, `07권 (2).repair.zip` — the same content 2–3 times. | **v1: show them all**, natural-sorted, no magic. Add a `duplicate_of` hint in phase 3 once we have a rule that will not hide the *good* copy. Silently picking one risks hiding the only readable version. |
| **OQ-5** | **Two-level series.** `[만화] 기동전사 건담 시리즈` holds 8 sub-directories that each look like a series. prd 1.3 says series = the root's direct child, so this flattens into one series with ~60 books. | **Follow prd literally: flatten**, and carry the sub-path in the book's display name. Revisit only if the UI feels wrong with a real 60-book series. |
| **OQ-6** | **Non-media series.** 5 top-level directories contain only `.txt`/`.hv3` (text novels, a proprietary format) — 26 `.hv3` and 19 `.txt` files. | **List them** and let the frontend grey them out; hiding directories the user can see in their file manager is more confusing than showing them. Add `?status=ok` for a clean view. **Amended by D-72:** the status is `status:"empty"` only when there is genuinely nothing readable in there. A book that is one container this build cannot open reports `status:"unsupported"` naming the format — `.7z`, `.alz`, `.egg`, `.lzh`. **Amended again by E-51:** `.hv3` was the example that produced D-72 and is no longer one of those formats. `ENCR` 2 is a keyless byte-position XOR, not encryption, and the 7.9972 bits/byte was 104 JPEGs measuring what JPEGs measure; the one HV3 in the collection is a 104-page volume. |
| **OQ-7** | ~~**Default thumbnail widths.**~~ **CLOSED (D-37 / amendment A-1):** the set is **`[120, 240, 400, 640]`**, derived in impl-plan §0.4 from the ui-spec's real rendered sizes at 2× DPR. The `[240, 320, 640]` guess in §3.2 below is superseded; `internal/config` and `shelf.example.yaml` both ship the new set. | Ruled. Amendment A-6 also moves the default `w` on `/cover` and `/thumbs/{n}` to **120** (`widths[0]`). |
| **OQ-8** | **TLS.** NFR-SEC-003 anticipates a reverse proxy. Should the binary also terminate TLS directly (`--tls-cert/--tls-key` or autocert)? | **No TLS in v1.** Reverse proxy only; `server.trusted_proxy_headers` handles `X-Forwarded-Proto` for the `Secure` cookie flag. |

---

## Appendix A — Requirement coverage map

| Requirement | Where |
|---|---|
| FR-CFG-001..003 | §3.1, §3.2, §7.4 (A-11 writes the `roots:` list; the file stays the source of truth) |
| FR-CFG-004 | §3.4 (exact hash input + verified vectors) |
| FR-CFG-005 | §3.3, §11 `check-readonly` |
| FR-IDX-001 | §4.1, §7.10 |
| FR-IDX-002 | §4.3 (0.365 % of the archive read, 2 `ReadAt` calls) |
| FR-IDX-003 | §4.6 |
| FR-IDX-004 | §4.12, §7.10 (polling, justified) |
| FR-IDX-005 | §3.7, §3.4 proof, §6.3 |
| FR-IDX-006 | §4.5 |
| FR-IDX-007 | §4.7 (algorithm + verified outputs) |
| FR-IDX-008 | §4.4 (rule corrected for the decoder's real behaviour) |
| FR-IDX-009 | §4.3 step 2, §5 fixtures |
| FR-IDX-010 | §4.11 (9/11,157 measured) |
| FR-IDX-011 | §5.5 |
| FR-SRV-001/002 | §5.1 (`comp_size + 30` bytes, CRC-verified) |
| FR-SRV-003 | §5.1 stored path |
| FR-SRV-004 | §5.2 |
| FR-SRV-005 | §5.1, §8.1 layer 3 |
| FR-SRV-006 | §5.7 |
| FR-SRV-007 | §5.3 (`?v=` makes `immutable` honest) |
| FR-SRV-008 | §5.3, §7.6 |
| FR-THM-001..008 | §5.4, §5.6, §7.5, §7.6, §7.9 |
| FR-LIB-001..011 | §7.5, §7.7, §4.7, §4.8, §4.10 |
| FR-VWR-002 | §7.6 `BookPrefs` |
| FR-VWR-004 | §5.8 |
| FR-VWR-006 | §7.8 `prefetch` |
| FR-VWR-008 | §7.6 thumbs |
| FR-VWR-009 | §7.6 progress |
| FR-VWR-010 | §7.6 `prev_book_id`/`next_book_id` |
| FR-VWR-012 | §7.6 `completed` |
| FR-STT-001..004 | §3.6, §7.6, §7.11 |
| NFR-PRF-001..006 | §5.1, §5.3, §7.6, §4.1, §6.2 |
| NFR-OPS-001..006 | §2.1, §11, §9, §6.3 |
| NFR-DAT-001..004 | §3.5, §3.6, §3.7 |
| NFR-SEC-001..003 | §8.1, §8.2, §8.3 |
| CON-001..004 | §1, §5.5, §5.6, §5.1 |
| AC-001..008 | §5.1, §4.4, §4.2, §5.7, §3.7, §3.4, §11, §7.6 |

## Appendix B — Reproducing the verification spike

The throwaway module lives at `/tmp/spike` (module `spike`, Go 1.25). Programs, each runnable standalone:

| Path | Proves |
|---|---|
| `cmd/sqlitetest` | modernc sqlite: `CGO_ENABLED=0` static build, WAL, insert/read throughput, cross-compilation |
| `cmd/zipprobe`, `cmd/encprobe` | CP949 statistics over the real collection; the decoder's never-errors behaviour |
| `cmd/structprobe`, `cmd/extprobe` | series/book shape distribution; entry extension mix; pages-per-archive percentiles |
| `cmd/zipserve` (+ `zipidx.go`) | our central-directory parser vs `archive/zip`; offset-only serving; CRC verification; 8-goroutine shared-handle throughput |
| `cmd/bulkparity`, `cmd/badzip` | full-collection differential parity; the 9 broken archives |
| `cmd/scanbench` | 11,157 archives in 32.3 s at 2.0 `ReadAt` calls each |
| `cmd/coverbench` | the real thumbnail pipeline at 4/8/16 workers, with the `<h[0:2]>/<h[2:4]>/<h>.jpg` layout |
| `cmd/imgtest`, `cmd/rsstest` | every FR-IDX-011 codec; scaler comparison; RSS of the wasm runtimes |
| `cmd/pdftest` | pdfium/wazero rendering a real 284-page PDF; the compilation-cache result |
| `cmd/natsort` | the natural-sort and choseong tables in §4.7/§4.8 |
| `cmd/misc` | animated-WebP rejection; ServeMux patterns under a base path; ETag/304/206 semantics |
| `cmd/final`, `cmd/hook` | `os.Root` containment; two-database ATTACH; `RegisterConnectionHook` under a hammered pool; the AC-006 rebuild proof |

Run any of them with the standard prefix, e.g.:

```bash
cd /tmp/spike
GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org GOTOOLCHAIN=auto \
  CGO_ENABLED=0 go run ./cmd/scanbench "/mnt/big-data/pds/taison-data/02. books/01. mangga" 16
```
