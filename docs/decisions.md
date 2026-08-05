# Decision log — SHELF

One line per decision. `prd.md` wins every conflict; where a decision deviates from it, the deviation is
stated explicitly. Status: **D** = decided, **E** = escalated to the human (decided provisionally, but
being wrong is expensive).

Sources: `prd.md` (URD, authoritative) · `design.md` · `ui-spec.md` · `arch-backend.md` · `data-survey.md`
· `impl-plan.md` (§0 carries the full reconciliation table).

---

## Product & scope

| # | Decision | Rationale |
|---|---|---|
| D-01 | Product name **SHELF**, module **`shelf`** (bare path, no host prefix), binary `shelf`. | Matches the UI prototype's branding. *Originally `github.com/hwangtaeseung/shelf`; superseded by the binding **E-1 ruling** below and by the landed `go.mod`. All docs realigned 2026-07-28 — see D-51.* |
| D-02 | Scope = prd §9 **stages 1 + 2**; stage 3 only where trivially free. | Task mandate. Full in/out list per FR id in `impl-plan.md` §1. |
| D-03 | **PDF ships in this build**, not deferred. | Stage 2 is in scope and AC-004 requires it; arch measured 296 ms/page on a real 284-page Korean PDF, so prd §10's top risk is retired. |
| D-04 | **FR-STT-004 export/import: backend endpoints in, UI out.** | ~80 lines, and it protects the only authored data in the system; the UI is genuinely stage 3. |
| D-05 | **Docker image out of scope**; `make release` cross-compilation in scope. | prd §9 stage 3 calls Docker "부가적"; NFR-OPS-003 (Linux/Windows/macOS binaries) is a hard NFR. |
| D-06 | Basic touch (tap zones + horizontal swipe) **in**; gesture *optimisation* out. | FR-VWR-011 is 권장 and NFR-CMP-002 needs the basics; "모바일 제스처 최적화" is the stage-3 item. |
| D-07 | Nested archives (a ZIP of ZIPs), RAR/CBR/7z **out**; `internal/archive.Reader` stays an interface. | prd §7.2. The one real container-of-ZIPs series indexes as `status:"empty"` rather than crashing. |

## Architecture & dependencies

| # | Decision | Rationale |
|---|---|---|
| D-08 | Dependency set frozen at the arch §1.1 versions: `modernc.org/sqlite v1.54.0`, `x/text v0.40.0`, `x/image v0.44.0`, `disintegration/imaging v1.6.2`, `gen2brain/avif v0.6.0`, `klippa-app/go-pdfium v1.19.6`, `wazero v1.12.0`, `yaml.v3`, `x/crypto`. | All verified to build and run `CGO_ENABLED=0` on this machine; pinning removes a class of "works on my box". |
| D-09 | **No HTTP router dependency** — Go 1.22+ `net/http.ServeMux`. | Method+wildcard patterns, 405s and `StripPrefix` under a base path were all verified; a router would be cost for zero capability. |
| D-10 | `imaging.Lanczos` for downscaling, **not** `x/image/draw`. | 18.7 ms vs 196.9 ms measured at our ratios — a 10× regression nobody would have noticed. |
| D-11 | **Ship our own ZIP central-directory reader** (`internal/archive/zipidx`); keep `archive/zip` as a permanent differential-test oracle. **Deviates from prd 6.1.** | `zip.File` does not expose the local-header offset FR-SRV-002 (필수) needs; the stdlib route did not finish in 10 min vs 32.3 s for ours. prd 6.1 is a technology hint, FR-SRV-002 is a requirement. *(See E-2.)* |
| D-12 | Go **1.25.0** floor with `GOTOOLCHAIN=auto`. **Widens prd 6.1's "1.22 이상".** | `os.Root` (Go 1.24+) is path-traversal layer 3 for NFR-SEC-001 and is worth the floor; the toolchain auto-downloads here. |
| D-13 | Two SQLite files: `index.db` (derived, disposable) and `user.db` (authored, never rebuilt), joined by `ATTACH` on every index connection; **no transaction spans both**. | NFR-DAT-001/004. Verified under 64 goroutines against an 8-connection pool with zero failures. |
| D-14 | Ids are `SHA-256(IDVersion ‖ domain ‖ root name ‖ NormalizeRel(root-relative path))` truncated to 80 bits, lowercase base32, 16 chars, NUL-separated. `IDVersion = "shelf-id/1"`; `domain ∈ {"series","book"}`. | FR-CFG-004 / AC-006: identity depends only on the config file and the filesystem, so a rebuild reproduces it byte-identically. The version tag is *inside* the hash so bumping it necessarily changes every id, which is what lets startup refuse a `meta.id_version` mismatch instead of silently orphaning progress. *(Sharpened 2026-07-28 — see D-51.)* |
| D-15 | `pages` is `WITHOUT ROWID` keyed `(book_id, page_no)`, and `GET /api/books/{bid}` returns **all** pages at once. | 1 071 pages ≈ 110 KB of JSON; one fetch makes every jump instant, which is literally AC-008. |
| D-16 | **Polling (1 s) for scan status, not SSE.** | A full cold scan is 32 s ⇒ ~32 requests against an atomic snapshot; SSE would permanently hold one of six HTTP/1.1 connections the viewer needs for prefetch. |
| D-17 | Page URLs carry `?v={content_version}`; `Cache-Control: immutable` **only** when it matches. | `immutable` is a promise the bytes never change — a path-derived `book_id` alone cannot keep it. |
| D-18 | Thumbnails are **JPEG only** in v1; the format string is part of the cache hash. | CON-003. A later switch to WebP/AVIF is then a pure cache-invalidation event with no migration. |
| D-19 | Cache invalidation is **structural**: `content_version` is an input to the thumbnail hash. | FR-THM-006 with no invalidation code that can be forgotten. |
| D-20 | pdfium and AVIF wasm runtimes are **lazily initialised and idle-torn-down**; wazero uses a persistent on-disk compilation cache. | Keeps idle RSS inside NFR-PRF-005's 200 MB (init drops 3.885 s/299 MiB → 135 ms/43 MiB warm). |
| D-21 | Four independent path-traversal layers: opaque ids only → `filepath.IsLocal` on write → `os.Root` per root → a final prefix assertion. | NFR-SEC-001. Layer 1 alone is sufficient; the rest are cheap insurance. |
| D-22 | **No built-in TLS**; reverse proxy + `base_path` + `trusted_proxy_headers`. | NFR-SEC-003 already anticipates a proxy; certificate management is not this product's job. |
| D-23 | Auth is all-or-nothing and covers static assets; `SameSite=Lax` + no state-changing `GET` removes the need for CSRF tokens. | NFR-SEC-002 with the smallest correct surface. |

## Data-driven adjustments (from the real 414 GB collection)

| # | Decision | Rationale |
|---|---|---|
| D-24 | CP949 decoding is a **critical MVP path**, and the decision rule probes for valid UTF-8 **before** trying CP949. | 14 630 of 14 630 flagless non-ASCII names are CP949; and `korean.EUCKR` never returns an error (it substitutes U+FFFD), so the error return is useless and a UTF-8-first probe is what stops modern flagless-but-UTF-8 archives being corrupted. |
| D-25 | All seven FR-IDX-011 formats implemented, but **AVIF is lazily initialised, 1-permit-serialised and killable**; animated WebP thumbnails degrade to `422`. | Zero `.webp` and zero `.avif` exist in the collection, yet FR-IDX-011 is 필수 — implement, but never on the critical path. |
| D-26 | ZIP64 implemented and tested **only** against a synthetic fixture. | FR-IDX-009 is 필수; no real ZIP64 archive exists (largest is 1.48 GB). |
| D-27 | **≤3 loose images beside real books are covers, not a one-page book** (`scan.cover_max_loose_images: 3`). | The "mixed" row of prd §2.2 occurs 47 times and is always "N archives + 1 cover jpg". |
| D-28 | **Duplicate books are all shown**, natural-sorted, with no dedup heuristic. | `군계` really holds `01권/` *and* `01권.zip`, and three copies of `07권`; silently picking one risks hiding the only readable copy. |
| D-29 | A series whose root child holds only `.txt`/`.hv3` is listed with `status:"empty"`, not hidden. | Hiding directories the user can see in their file manager is more confusing than greying them out. |
| D-30 | Two-level series (`건담 시리즈` → 8 sub-dirs) **flatten** into one series with ~60 books, with the sub-path carried in the book display name. | prd §1.3 defines a series as the root's direct child. Follow it literally. |
| D-31 | Natural sort ships as **two agreeing representations** — `Compare()` for Go and a `Key() []byte` BLOB SQLite orders under BINARY collation — with a property test asserting they agree. | Mixed zero-padding is pervasive; sorting in SQL without a user-defined function is what keeps `sort=name` cheap at 10⁴ series. |

## UI decisions

| # | Decision | Rationale |
|---|---|---|
| D-32 | Wire enum values are **`spread`** (not `double`) and **`contain`** (not `screen`); sort keys are the API's `name\|mtime\|recent\|size\|books\|added`. | The API contract is frozen and shared; the Korean labels (양면 / 화면) are unaffected. |
| D-33 | **Roots are read-only in the UI.** Settings shows them with per-root 재스캔 and the config path; onboarding says "설정 파일 위치 보기". **Overrides design.md 화면 4.** | prd FR-CFG-001 makes YAML the source of roots and prd 6.3 has no `POST /api/roots`; writing YAML from a web UI is a security surface prd never asked for. |
| D-34 | Search and the command palette query the **server** (`/api/series?q=`, 150 ms debounce); the client `chosung()` helper survives only for highlighting. **Overrides ui-spec §8.4.** | FR-LIB-007 means the client never holds all series, so a client-side filter is wrong by construction. |
| D-35 | Reading direction persists **per book** via `/api/books/{bid}/prefs`; the series-detail `.seg` is a client-only seed stored in `localStorage`. **Overrides ui-spec §5.1's "manga root ⇒ rtl" heuristic.** | prd FR-VWR-002 says 권 단위, and no metadata exists to key a root heuristic on. |
| D-36 | Sidebar smart lists are served by a **new `progress=any\|reading\|done\|unread` parameter** on `GET /api/series` (amendment A-4). | The alternative — filtering client-side — breaks under FR-LIB-007. |
| D-37 | Thumbnail widths **`[120, 240, 400, 640]`**, derived from the ui-spec's real rendered sizes at 2× DPR; the frontend always sends an explicit `w` from that set. | The server snaps *up*, so a mismatched set silently doubles bandwidth. Closes arch OQ-7. |
| D-38 | Default fit mode **`height`** (was `contain` in arch). | The prototype capture is live evidence; the config default was a guess. |
| D-39 | **Archivo vendored latin-only** (`@fontsource/archivo` 400/600/800); Korean uses the system fallback stack. The DS stylesheet's Google Fonts `@import` is deleted. | NFR-OPS-001/002 forbid a runtime external dependency; a subsetted Korean face is 1.5–4 MB against an 18 MB binary. |
| D-40 | **Zero corner radius everywhere**; Tailwind's `borderRadius` is *overridden*, not extended, so `rounded-lg` cannot exist. The radio dot and the viewer spinner are the only circles. | The Modernist DS sets `--radius-*: 0px` deliberately; enforcement beats discipline. |
| D-41 | Dark theme derived by flipping **semantic** tokens (`--ink`, `--rule`, `--fill-*`) while the raw ramps stay constant; the viewer is `data-theme="dark"` in both app themes. | NFR-CMP-003 with one palette, not two. |
| D-42 | A **responsive layer must be built from scratch** (240px → 56px rail → off-canvas drawer; list drops columns at 768; viewer controls move to a bottom sheet below 768). | The prototype has none — captured breakage at 768 and 400 px. |
| D-43 | Frontend stack: **React Router v7** (`createBrowserRouter`, basename from `<base href>`) + **TanStack Query v5** for server state + **Zustand** for UI state + **@tanstack/react-virtual** for both list and grid. | Clean split (server data never in Zustand, UI state never in Query); virtualisation is FR-LIB-007 and AC-008. |
| D-44 | **One typed API module**, `web/src/api/client.ts`, is the only place `fetch` appears; enforced by ESLint. | It is the single reconciliation point between the parallel backend and frontend builds. |

## Process decisions

| # | Decision | Rationale |
|---|---|---|
| D-45 | 14 work packages in 5 waves, **file ownership is exclusive**. | Parallel agents; a shared file is a merge conflict waiting to happen. |
| D-46 | Consumers declare interfaces (`internal/httpapi/deps.go`); producers return concrete types. | Lets wave-2 packages compile before wave-3 exists. |
| D-47 | The frozen contract is `arch-backend.md §7` **plus amendments A-1…A-11** in `impl-plan.md` §0.3; reconciliation artefact = WP-12's golden JSON diffed against WP-06's `types.ts`. *(Was A-1…A-7; **A-8** was added 2026-07-28 to carry ruling **E-9** — `GET /api/series?scope=added` + `user.db.first_seen_at`. It is the one amendment newer than the landed `types.ts` and `internal/index`, so both carry follow-up work listed in impl-plan §0.3.)* *(Range corrected 2026-07-30: it had said A-1…A-8 since that note was written, while **A-9** (E-13), **A-10** (E-25) and **A-11** (E-26) had already landed in §0.3. The table in impl-plan §0.3 is and has always been the authority on which amendments are in force; this row only points at it, and a stale range here could be misread as excluding three that are binding.)* | Backend and frontend must never need to talk to each other mid-build. |
| D-48 | The E2E test uses a **root pointed at the real collection constrained by `scan.include_globs`** (new config key), naming 10 specific series (~5.1 GB), copying nothing. | **Symlinks are impossible**: `os.Root` refuses any symlink escaping its root (verified) and `follow_symlinks` defaults false — a symlink farm would index as an empty library. Copying would destroy the 2012–2018 mtimes that `content_version`, incremental scan and FR-THM-006 all key off, and hard links cannot cross the `/mnt/big-data` → `/mnt/data` filesystem boundary. |
| D-49 | A `--synthetic` E2E mode reproduces the same ten shapes in ~12 MB, **plus** an encrypted ZIP and a ZIP64 archive that the real collection does not contain. | The suite must be runnable without the media volume, and two required behaviours have no real sample. |
| D-50 | FR-CFG-005 is enforced by a `make lint` grep guard (no filesystem mutation primitive may appear in `internal/{scanner,source,archive,openpool}`) **and** by an integration test asserting `find "$ROOT" -newermt "$START"` is empty. | A read-only guarantee that is only a convention will not survive six parallel agents. |
| D-51 | **(2026-07-28, post-wave-1 doc realignment.)** The item-id hash input is **versioned and domain-separated**: `IDVersion ‖ 0x00 ‖ domain ‖ 0x00 ‖ root name ‖ 0x00 ‖ NormalizeRel(rel)` with `IDVersion = "shelf-id/1"`. Where a document and `internal/ids` disagree, **the code is authoritative** for this scheme. arch §3.4's worked example and §10.1, and impl-plan §3 WP-02 acceptance 1 and §6.1, were recomputed to `SeriesID("mangga","[만화] 군계 1~25") = ruzwlotzngls2ua5` and `BookID("mangga","[만화] 군계 1~25/군계(軍鷄) 01권.zip") = yvtfrny77ehkt2we`. | The pre-existing example strings (`gzj75n6x7rir6but` / `ox74tfcrwwnfopch`) came from a spike that hashed `root ‖ rel` with **both tags dropped**, contradicting the construction printed three paragraphs above them. Untagged, `SeriesID == BookID` for every input — which for the 291 single-file series of arch §4.2 ("series == its own single book") means one id for two entities — and `meta.id_version` would describe nothing. D-14 and impl-plan §3 WP-02 acceptance 1 both require the domain tag, so the tagged construction wins on precedence. Fixing it in wave 1, with no `user.db` on disk anywhere, costs two constants; fixing it later is a `user.db` migration. Values recomputed by running `internal/ids`, not by hand; `TestIDs_hashInput_isTheArchSpecString` rebuilds the byte diagram from literals so the doc and the code cannot silently diverge again. |

---

## Escalations — decided provisionally, expensive if wrong

| # | Question | Provisional answer (what the build assumes) | Cost of being wrong | Wanted from the human |
|---|---|---|---|---|
| **E-1** | **Module path and product name.** | `github.com/hwangtaeseung/shelf`, binary `shelf`, UI brand "SHELF". | Low **now** (one `go.mod` line + a `sed`), high after wave 1 — it touches every import in every package. | Confirm or replace **before WP-00 lands**. |
| **E-2** | **Deviating from prd 6.1 by shipping our own ZIP central-directory reader** instead of `archive/zip`. | Deviate; keep `archive/zip` as a permanent differential-test oracle that must agree on every fixture and every real archive. | If rejected, FR-SRV-002 (필수, seek straight to a stored offset) becomes unimplementable at acceptable speed — the stdlib route did not finish in 10 minutes vs 32.3 s. We would have to renegotiate FR-SRV-002 instead. | Accept the deviation, or tell us to renegotiate FR-SRV-002. |
| **E-3** | **Roots are read-only in the UI**, contradicting design.md 화면 4 ("루트 관리: 목록, 추가/제거"). | Read-only: list + per-root rescan + the config path, with `+ 루트 추가` and `제거` removed from the UI. | Medium. If the human actually wants add/remove, it needs `POST/DELETE /api/roots`, a YAML writer with atomic rename and comment preservation, path validation against traversal, and an onboarding flow that can browse the filesystem — roughly a whole extra work package, and a new security surface. | Confirm read-only for this build. |
| **E-4** | **Two-level series flatten** — `[만화] 기동전사 건담 시리즈` becomes one series with ~60 books rather than 8 sub-series. | Flatten, per prd §1.3's literal definition of a series. | Medium. Changing it later changes `series_id` for those directories, which **orphans their reading progress**. Cheap now, a migration later. | Confirm, or say "auto-promote a sub-directory to a series when it contains ≥N books". |
| **E-5** | **Duplicate books are all listed** — `군계` shows `01권/`, `01권.zip`, `07권.zip`, `07권.repair.zip`, `07권 (2).repair.zip`. | Show them all, natural-sorted. | Low-medium. It makes one real series look messy, but any dedup rule risks hiding the *only* readable copy (two of the three `07권` files are truncated). | Confirm, or supply a preference rule (e.g. prefer `.zip` over the folder, prefer non-`.repair`). |
| **E-6** | **`scan.include_globs` is a new config key** invented for the E2E subset (amendment A-3) and is not in prd. | Add it. It is ~15 lines and genuinely useful to users. | Low. Removing it later is trivial; but if the human objects to inventing config surface, the E2E plan needs rework (there is no alternative that avoids copying — see D-48). | Accept, or approve copying ~5 GB into a dedicated test root instead. |
| **E-7** | **No Korean webfont is vendored** — Hangul renders in the system fallback. | Latin-only Archivo bundled; Korean falls back to Pretendard / Apple SD Gothic Neo / Noto Sans KR / system-ui. | Low-medium, but *visible*: the product is Korean-first and the typography will differ across Linux, macOS and Windows. Vendoring a subsetted face costs 1.5–4 MB on an 18 MB binary. | Confirm the fallback is acceptable, or approve the binary-size hit. |
| **E-8** | **Auth is in scope but unconfigured by default.** | The `auth:` block is omitted from `shelf.example.yaml`, i.e. **no password by default**, with a comment explaining how to enable it. | Medium if the server is ever exposed beyond a LAN. NFR-SEC-002 says "선택적", so defaulting to open matches the requirement — but it is worth an explicit human "yes". | Confirm open-by-default, or ask for a first-run password prompt. |

---

## Escalation rulings (orchestrator, 2026-07-28) — BINDING

These supersede the provisional answers above. Every implementer reads this table as authoritative.

| # | Ruling | Basis |
|---|---|---|
| **E-1** | Module path is the bare `shelf`. Binary `shelf`, UI brand "SHELF". **No `github.com/<user>/…` prefix** — the app is embedded and never `go get`-ed, and inventing a repo host in 200 import lines is worse than a later `sed`. | NFR-OPS-001/002 (single binary, no external deps). Prototype title supplies "SHELF". |
| **E-2** | **Accept the deviation.** Ship our own ZIP central-directory reader; keep `archive/zip` as a permanent differential-test oracle that must agree on every fixture and every real archive. | FR-SRV-002 is 필수 and `archive/zip` exposes no local-header offset — measured 32.3 s vs >10 min. prd 6.1 is a 기술 방침, FR-SRV-002 is a requirement; the requirement wins. |
| **E-3** | **Roots are read-only in the UI.** List + per-root manual rescan + show the config file path. No add/remove, no `POST/DELETE /api/roots`, no filesystem browser. | prd 5.2 UI-004 states the settings screen scope literally: "루트 목록 조회 및 수동 재스캔 실행". design.md's 화면 4 is broader; per impl-plan §0.1's own rule, prd wins. |
| **E-4** | **Flatten.** A series is exactly one direct child of a root; a two-level directory becomes one series with all its books. | prd 1.3 and 2.1 define 시리즈 as "루트의 직계 자식 1개 항목" with no promotion rule. Record the `series_id` sensitivity in README so a future change is a conscious migration. |
| **E-5** | **List every book, natural-sorted**, including `.repair` and ` (2)` duplicates. No dedup heuristic. | prd 7.2 puts collection editing out of scope; two of three `07권` files are truncated, so any dedup rule risks hiding the only readable copy. Surface `status` per book instead (FR-IDX-010). |
| **E-6** | **Accept `scan.include_globs`.** Documented in `shelf.example.yaml`, empty by default (= scan everything). | Needed for the E2E subset without copying 5 GB; FR-CFG-003 already establishes config-driven scan tuning. ~15 lines, trivially removable. |
| **E-7** | **Latin Archivo vendored; Hangul falls back to the system stack** (Pretendard → Apple SD Gothic Neo → Malgun Gothic → Noto Sans KR → system-ui). Do not vendor a Korean face in this build. | Keeps the binary near 18 MB per NFR-OPS-001. Reversible by dropping one woff2 into `web/src/styles/fonts/` and one `@font-face` block. Must be listed in README as a known cross-platform typography variance. |
| **E-8** | **No password by default.** The `auth:` block ships commented-out in `shelf.example.yaml` with enabling instructions. No first-run prompt. | NFR-SEC-002 says 선택적. Single-user LAN tool per prd 1.4. |

### Also settled
- **PDF ships in v1**, not deferred. `data-survey.md`'s "defer to Phase 2" is overruled by the arch spike's measurement (296 ms/page on a real 284-page Korean PDF, cgo-free, +8.34 MB). prd 9 puts PDF in stage 2 and both stages are in scope. `nopdf` build tag remains as the escape hatch.
- **Go commands.** Every `go` invocation in every Makefile target, script and agent shell MUST carry
  `GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org GOTOOLCHAIN=auto`.
  Bare `go build` fails on this machine with "GOPROXY list is not the empty string, but contains no entries".

---

## Wave-2 rulings (orchestrator, 2026-07-28) — BINDING

| # | Question (raised by the wave-2 wiring agent) | Ruling | Basis |
|---|---|---|---|
| **E-9** | **`최근 추가` has no backing filter.** The sidebar item is required but the frozen contract has no `added` scope, so the count currently shows the whole-library total (24 vs the prototype's 11) — a visibly wrong number. | **Amend the contract (A-8).** Add `scope=added` to `GET /api/series` and a `first_seen_at` timestamp per series. **`first_seen_at` lives in `user.db`, not `index.db`** — set on first sighting, never overwritten, so it survives `--rebuild-index`. Default window 14 days, configurable as `library.recently_added_days`. Doing this in `index.db` would reset every series to "new" after a rebuild, which is the opposite of what the label means. | prd 5.2 UI-001 and design.md 화면 1 both require the sidebar item; NFR-DAT-004 requires user-meaningful state to survive an index rebuild; AC-005/AC-006. WP-12 is unwritten, so the contract is still cheap to change. |
| **E-10** | At 768px the built UI shows a 56px icon rail (ui-spec §7) while the reference screenshot shows the full 240px sidebar. | **Follow ui-spec §7 — the icon rail is correct.** The prototype shipped **no responsive layer at all**; its 768px screenshot is the desktop layout clipped, not a design. | design.md 반응형 기준 (768–1024: 사이드바 접힘); ui-spec §7 documents the prototype's breakage with `*-broken.png` evidence. |
| **E-11** | `formatBytes` computes binary (MiB) but labels decimal (MB) — every size in the product reads ~4.6% low (799,000,000 B → `762 MB`; the prototype shows `799 MB`). | **Decimal units with decimal labels.** `1 MB = 1000² B`, matching the prototype and ordinary user expectation for media sizes. | The prototype is the design reference; prd 5.2 UI-002 lists 총 용량 as a user-facing figure, and a silently wrong number is worse than either convention. |
| **E-12** | Volume rows print `완독` (state) immediately followed by `안읽음` (the toggle action), both bare red — reads as a contradiction. | **State and action must be visually distinct.** Keep the state as a badge; render the manual toggle (FR-VWR-012) as a real button with button chrome and an unambiguous label. Never two bare accent words in a row. | prd FR-VWR-012 requires a manual override; ui-spec §2 reserves the accent for the primary action and small emphasis, not for two adjacent meanings. |

---

## Wave-3 rulings (orchestrator, 2026-07-28) — BINDING

| # | Question | Ruling | Basis |
|---|---|---|---|
| **E-13** | **arch §8.2 mandates a 429 rate limit but §7.2's `ErrorCode` enum has no value for it.** `web/src/api/errors.ts` already carries a workaround. | **Add `rate_limited` to the `ErrorCode` enum as amendment A-9.** Update arch §7.2, the Go error codes, the golden JSON, and `web/src/api/errors.ts` so the workaround becomes the contract. | A response the contract cannot name is a contract defect; §8.2 already requires the behaviour. |
| **E-14** | **`series.status` is undefined when every book in a series is `encrypted`/`unsupported`.** §3.5 allows `ok\|empty\|error`; §7.3 types it `ItemStatus`. | **`error`** when the series has ≥1 book and *no* book is `ok`. `empty` stays reserved for "no books at all". | FR-IDX-010 requires the failure to be visible, and design.md 화면 2 requires an error state with a reason. A series the user cannot read anything in must not present as healthy. |
| **E-15** | `Settings.library_sort` is typed `string` where the union is known. | **Type it as the closed union** (the FR-LIB-004 sort set) on both the wire spec and in `web/src/api/types.ts`. | impl-plan §5.2 bans loose typing; FR-LIB-004 fixes the set of sorts. An open string invites a silent 400. |
| **E-16** | **A partial first-seen bootstrap can never recover.** WP-14's fix correctly withholds the bootstrap marker when a run is cancelled/restricted/partial, but `FirstSeenBootstrapNeeded` *also* requires `series_seen` to be empty (arch §3.6 rule 6) — so once one batch has landed, the recovering run is not treated as a bootstrap and the remaining series get stamped "added today". | **Relax rule 6 to marker-only.** The withheld marker is now the authoritative signal; the emptiness precondition is redundant and actively harmful. Update arch §3.6. | It reproduces exactly the wrong number E-9 was raised about, on the most common real path (a first scan of 414 GB interrupted once). |
| **E-17** | WP-12 made the auth gate unconditional (including static assets, per arch §8.2 / WP-12 acceptance 6) and added a **server-rendered** 401 login form — which leaves WP-05's React `LoginScreen.tsx` unreachable on cold entry. | **Keep both, with distinct jobs.** Server-rendered form = cold entry, because the SPA bundle itself is gated. `LoginScreen.tsx` = **in-app re-authentication** when a session expires mid-session, so the reader is not thrown back to a full page load. Verify it is actually wired to the 401 path; if it is not, wire it. Dead code is not acceptable — if it cannot be wired, delete it and record that. | arch §8.2 is explicit that static assets are gated, so a client-rendered cold login is impossible. Losing in-place re-auth would be a real UX regression for a long reading session. |
| **E-18** | The bootstrap run seeds `first_seen_at = min(runStart, series.mtime)` — an extension beyond E-9 that WP-14 flagged in-doc rather than hiding. | **Accept.** Without it, day one stamps the entire pre-existing collection as "최근 추가", which is the failure E-9 exists to prevent. | Same basis as E-9. The behaviour is documented at the point of definition, which is what made it reviewable. |

---

## Wave-4 rulings (E2E round 3, 2026-07-29) — BINDING

Raised by the round-3 repair pass with measurements, not opinion. **E-19 changes a
number the orchestrator set; it is recorded here so the divergence is explicit
rather than silent, and it is flagged for confirmation in the round-3 repair
report.**

| # | Question | Ruling | Basis |
|---|---|---|---|
| **E-19** | **The `≤ 20 MB` release-size budget of impl-plan §7.3 is unreachable while shipping the 필수 feature set.** `make release` has failed on it identically in rounds 1, 2 and 3 — the linux/amd64 binary is **27,418,916 B**, 30.8 % over the 20 MiB gate — so the first §7.3 checkbox can never be ticked, and the only levers the Makefile can offer are `-tags nopdf` (FR-SRV-006, and therefore AC-004) and `-tags noavif` (FR-IDX-011). | **Raise the budget to 28 MiB (`29,360,128 B`) and keep the gate hard.** The 20 MB figure was never a requirement: prd NFR-OPS-001 asks for *a single executable with the frontend embedded* and states **no size at all**, and prd wins every conflict (§0 precedence). 20 MB was impl-plan §7.3 rounding up arch-backend §1.2's "18 MB is fine for a NAS", an estimate whose *base* term — everything that is neither pdfium nor AVIF — was ~7 MB low. Measured, `CGO_ENABLED=0 -trimpath -ldflags "-s -w"`: **full 27,418,916** · `-tags noavif` 25,833,656 (**AVIF = 1,585,260**, against the estimate's 1.58 MB — correct) · `-tags nopdf` 19,689,764 (**PDF = 7,729,152**) · both off **15,286,456**. So the base the estimate put at ~8 MB is really **15.3 MB**, most of it `modernc.org/sqlite` — the pure-Go SQLite that **CON-001's `CGO_ENABLED=0` requires**. Nothing regressed; the estimate was wrong on the day it was written. The new number is the measured build plus 7.1 %, which is **less than the cost of either build tag**, so the gate still cannot be satisfied by anything that matters and still trips on a lost `-s -w`, an accidentally embedded asset tree, or the 4 MB end of E-7's Korean webfont. | prd NFR-OPS-001 (single file, no size) · §0 precedence (prd > impl-plan) · CON-001 (`CGO_ENABLED=0`) · FR-SRV-006 + AC-004 and FR-IDX-011 are both 필수, so neither tag is a free win. Compressing the vendored `pdfium.wasm` (5,225,611 B → 2,401,643 B gzipped) is the only other lever and saves 2.8 MB — it lands at ~24.6 MB, still over, at the cost of vendoring a third-party binary blob into the repo. |

> **SUPERSEDED on the number only, by E-21 §4 (below).** The `28 MiB (29,360,128 B)` of this row was
> confirmation-flagged and never confirmed; the orchestrator's E-19 row in the next section says
> **32 MiB (`33,554,432`)**, and **E-21 §4 resolves the divergence in favour of 33,554,432**, which is what
> `Makefile`'s `SIZE_BUDGET` and impl-plan §7.3 now carry. Everything else in this row — the measurements,
> the reasoning, and "keep the gate hard" — still stands. Do not re-litigate this; the two rows are a
> recorded divergence, not a mistake to be fixed.

---

## Wave-4 E2E rulings (orchestrator, 2026-07-29) — BINDING, read before any further repair

| # | Question | Ruling | Basis |
|---|---|---|---|
| **E-19** | **`make release` fails the 20 MiB binary-size gate** (linux/amd64 = 27,418,916 B, 30.8% over). Raised as a failure in E2E rounds 1, 2 and 3; the E2E engineer twice flagged it as needing a decision rather than a patch. | **Raise the gate to 32 MiB and document the breakdown.** Do NOT shrink by dropping features, and do NOT delete the gate. Set `SIZE_BUDGET` in the `Makefile` to 33,554,432 and correct `docs/impl-plan.md` §7.3, adding a comment naming what occupies the space (pdfium WASM ≈8.34 MB, AVIF WASM ≈1.58 MB, pure-Go SQLite, the embedded SPA). Keep `nopdf` and `noavif` as the documented small-build path and record their sizes in the README. | **prd NFR-OPS-001 specifies a single self-contained binary — it states no size limit.** The 20 MiB figure was invented by the planning agent, not derived from a requirement, and it is now failing a build that satisfies every actual requirement. The real resource requirement is NFR-PRF-005 (idle memory ≤200 MB), which is unaffected. A gate that a correct build cannot pass is a broken gate, and this one has burned three E2E rounds. |
| **E-20** | **`web/e2e/` (the Playwright browser suite) is owned by no work package**, so impl-plan §6.3 step 6 and §7.4's `docs/e2e-shots/` deliverable were never built and `scripts/e2e.sh` step 11 silently self-skips. | **In scope — assign it explicitly.** The next repair agent owns `web/e2e/**` and `scripts/e2e.sh`. Build the twelve browser assertions across the four viewport projects and produce `docs/e2e-shots/`. **A step that self-skips must fail loudly instead** — a skipped check that reports success is worse than a missing one. | prd §8 acceptance and impl-plan §6.3/§7.4 both require it; ownership was a planning gap, not a scope decision. FR-VWR-* and FR-LIB-* are only end-to-end-verifiable in a browser. |

**Note to repair agents:** neither of these is fixed by editing product code. E-19 is a one-line `Makefile`
constant plus a doc correction. E-20 is new test-suite work. Do not attempt to shrink the binary by removing
PDF or AVIF support — decisions E-2/OQ-2 put PDF in v1 deliberately, on measured evidence.

---

## E-21 — the shipped binary is not static (orchestrator, 2026-07-29) — BINDING

**Found by the orchestrator during final verification, after the E2E rounds had ended.** `make build` produces a
binary that reports `CGO_ENABLED=0` in its build metadata yet is **dynamically linked** against
`libc.so.6`, `libdl.so.2` and `libpthread.so.0`.

Measured on linux/amd64:

| Build | Linkage | Size |
|---|---|---|
| default (current) | **dynamic** | 27,418,916 B |
| `-tags noavif` | **static** | 25,833,656 B |
| `-tags "noavif nopdf"` | static | 15,286,456 B |

Cause: `internal/thumbs` → `github.com/gen2brain/avif` → `github.com/ebitengine/purego`, which emits dynamic
import directives regardless of `CGO_ENABLED`. **`nopdf` does not help — pdfium/wazero is innocent.**

### Ruling
**The default build is static: `noavif` becomes a default build tag.** AVIF ships as an explicitly documented
opt-in variant, and `make release` emits **both** a static default artefact and an `-avif` variant per platform,
with the difference stated in the README and in `SHA256SUMS`' companion notes.

### Basis, and the requirement conflict this resolves
- **prd CON-001** does not merely ask for `CGO_ENABLED=0`; it names the *purpose*: "정적 단일 바이너리와 손쉬운
  크로스 컴파일을 확보한다". We currently satisfy the flag and miss the goal. A glibc-linked binary is the exact
  failure CON-001 exists to prevent.
- **prd NFR-OPS-003** makes NAS the primary deployment target. Synology/QNAP/Alpine frequently run musl or an
  older glibc, where this binary will not start at all. This is a deployment-blocking defect, not a nicety.
- **prd FR-IDX-011 lists `avif` as 필수**, so this is a genuine conflict between two requirements, not a free
  choice. It is resolved on evidence rather than preference: **every sample taken of the collection contains
  zero `.avif` and zero `.webp` files** — two independent passes, `docs/data-survey.md`'s 500-ZIP scan (≈4.5 %
  of the 11,157 archives) and arch-backend §1.1's ~56k-entry extension census (98.7% JPEG, 2014–2018 vintage).
  It is a well-corroborated sample, not a census of all 414 GB, and it is the sole evidentiary basis for this
  필수-vs-필수 resolution, so it is stated as what it is. The default build
  therefore loses nothing real, while the dynamic linkage costs the primary deployment target.
- Degradation is already graceful: `internal/thumbs` treats an undecodable page as a logged per-page failure with
  a placeholder (never a crash, never a failed scan), so an AVIF file in a default build is a visible,
  self-explanatory placeholder rather than a broken product.

### Required work
1. `Makefile`: default `TAGS ?= noavif`; `release` builds both variants for all seven targets; both in `SHA256SUMS`.
2. **A test that fails if the default binary is ever dynamically linked again** — this defect survived four
   review passes and a full E2E cycle precisely because nothing asserted it. Assert on the ELF headers of the
   built artefact, not on the build flags.
3. `README.md` + `docs/arch-backend.md` §11 + `docs/impl-plan.md` §7.3: document both variants, their sizes,
   and why the default excludes AVIF.
4. `SIZE_BUDGET` is currently 29,360,128 (28 MiB), set without a ruling. Align it to **33,554,432 (32 MiB)** per
   E-19 so the gate does not flap on the next small addition.

---

## E-22 — `GET /api/books/{bid}` for a book with no index row is **404**; the E2E "got 200" was a foreign server (2026-07-29) — BINDING

**Do not re-open this. It has been adjudicated against the contract, the code and the packet capture, and the
answer is that nothing in the product was wrong.**

`scripts/e2e.sh` step 10 reported:

```
FAIL  expected 404 for a book with no index row, got 200
```

`docs/HANDOFF.md` §5.2 originally speculated that the *test* was wrong — that impl-plan §4's
`status != "ok"` → `200 + pages: [] + error` rule might cover this case. It does not, and the 200 did not
come from our server at all.

### Ruling

1. **The contract mandates 404, unambiguously.** A book id with no row in `index.db` is a "well-formed but
   unknown id".
   - `docs/arch-backend.md:1391` (§7.1): *"All ids are `[a-z2-7]{16}`. A syntactically invalid id is `400`; a
     well-formed but unknown id is `404`."*
   - `docs/arch-backend.md:1758` (§7.6): *"`404` for an unknown `bid`. Books with `status != "ok"` still return
     200 with `pages: []` and a populated `error` …"* — **one sentence, two disjoint cases.**
2. **impl-plan §4 #4 does not reach this case.** `docs/impl-plan.md:744`: *"Books with `status != \"ok\"` return
   **200** with `pages: []` and a populated `error`, not an HTTP error."* `status` is a **column on the `books`
   row** (`internal/index/books.go:36-38`: `"ok" | "error" | "encrypted" | "empty" | "unsupported"`). **A book
   with no row has no status**, so the rule is inapplicable by its own terms. The 200 case is "the row exists and
   says the book is broken"; the 404 case is "there is no row". Conflating them is the error §5.2 made.
3. **The code already implements the contract.** `internal/httpapi/books.go:40-46` — `s.idx.GetBook` →
   `errors.Is(err, index.ErrNotFound)` → `notFound(...)`. There is **no fallback branch**: the only other exits
   are `503` (`s.idx == nil`) and `500`. `internal/index/books.go:95-105` maps `sql.ErrNoRows` → `ErrNotFound`.
   `internal/httpapi/api_test.go:138-169` (`TestIDs_invalidIs400_wellFormedUnknownIs404`) already asserts
   `GET /api/books/aaaaaaaaaaaaaaaa` → `404 not_found`, and it passes.
4. **The assertion is correct and stays verbatim.** `scripts/e2e.sh:632-634`. Its `probe_book` is a *real* id
   read from the live index before the wipe (`:600-608`), i.e. a book that exists on disk but has no row —
   exactly §7.1's "unknown". There is no rescan race to excuse a 200: the E2E config sets
   `scan.on_start: false` (`test/shelf.e2e.yaml.tmpl:38`) and passes no `--rebuild-index`, so
   `internal/app/app.go:405` starts no background scan at restart.
5. **What actually produced the 200: the run never owned its port.** A `shelf` process left over from an earlier
   debugging session was bound to `:8791` with `data_dir: /tmp/shelf-e2e-dev/data`. Our binary could not bind,
   exited, and every request in the run was answered by that foreign server — whose `index.db` was never
   deleted, because step 10 deleted `/tmp/shelf-e2e-3286544/data/index.db*`.

### Evidence (recorded so this cannot be argued about later)

- `docs/e2e-last-run.log:8` — the run's state dir is `/tmp/shelf-e2e-3286544`; `:23` — its scan is
  `run_id f6a067bd84cab711`.
- The **same `run_id` is logged by the other process**: `/tmp/shelf-e2e-dev/server.log:1182`
  `msg="scan started" run_id=f6a067bd84cab711`, in a server whose config is `port: 8791` /
  `data_dir: /tmp/shelf-e2e-dev/data`.
- The whole of step 10 appears in that foreign log, in order: `PUT …/progress` `00:41:41.454` →
  `PUT …/prefs` `.463` → `PUT /api/settings` `.471` → `GET /api/health` `.487` (this is `start_server`'s probe)
  → `GET /api/settings` `.497` → **`GET /api/books/i4kixpa2b2oxpcg5 status=200` `.536`**.
- That log has **no shutdown/startup pair between `00:19:51` and `00:49:52`** (`:630`, `:1374`): the server that
  answered was never restarted and its index was never deleted. The 16 ms between the last pre-stop request and
  the post-"restart" health probe is by itself impossible for a real SIGTERM + graceful shutdown + restart.
- Corroborating: `docs/e2e-last-run.log:22` reports "the full scan finished in 1s" — 374 ms for 101 books and
  13,033 pages is a warm-index rescan, not the cold rebuild step 10 is supposed to force.

### The real defect, and the guards now in place

`scripts/e2e.sh`'s `start_server()` polled `curl /api/health` **before** `kill -0 "$server_pid"`. A foreign
server answers the probe instantly, so the loop returned success having started nothing, and `stop_server`
then no-opped on an already-dead pid without a single FAIL. **A health probe cannot tell whose server answered
it** — that is the whole lesson. Four guards were added, and each was demonstrated to fire before landing:

1. **Pre-flight refusal** — `start_server` `die`s if anything answers `$BASE/api/health` *before* the spawn.
   After the spawn the two servers are indistinguishable, so this check has to come first.
2. **Liveness before health** — the poll loop asks "is our child alive?" before it asks "is the port up?". With
   the old order, a child that died on `address already in use` was never noticed.
3. **`uptime_ms` identity** — the responding server must prove its age: a process we spawned moments ago cannot
   have been up longer than we have been waiting for it (`/api/health.uptime_ms`, arch §7.4). This catches a
   squatter that appears *between* the pre-flight and the spawn. The 2026-07-29 squatter had been up 22 minutes.
4. **`stop_server` proves the port went quiet** — our child exiting is not the same as the port being free, and
   step 10's whole premise (delete the index, the book is gone) is void if it is not.

An independent review of those four then found one surviving hole and three ways a run could still report
success it had not earned. All four are closed, and each was demonstrated firing against functions extracted
verbatim from the shipped script (2026-07-29):

5. **Port ownership — the guard that must pass.** Guards 1–3 all lose to a *freshly started* foreign server:
   it binds *after* the pre-flight, our child is still alive when guard 2 asks (aliveness is not ownership),
   and its `uptime_ms` is far inside guard 3's 5 s budget. Reproduced: `start_server` returned 0 and steps 5–9
   would have printed PASS against it, guard 4 catching it only at step 10. `start_server` now asks the kernel
   rather than the process — the pid holding the listening socket (`ss -ltnpH "sport = :$port"`) must be our
   child, and `/proc/<pid>/exe` must be the `dist/shelf` we launched. `uptime_ms` stays as the near-free
   cross-check for a machine without `ss`; a missing `ss` is itself recorded as a failure, because "could not
   determine the owner" must never read as "the owner is us". Both port checks now allow 10 s for an answer
   and treat only "connection refused" as absence: a foreign server that is merely slow used to read as an
   empty port in the pre-flight and in `stop_server` alike.
6. **A signalled run can no longer report success.** `trap cleanup EXIT INT TERM` never exited: after a
   SIGTERM, `cleanup` deleted `$STATE` — and, with the default `keep=0`, `$CONFIG` — and the script then
   carried on for four more steps against a deleted state directory and exited 0. INT/TERM/HUP now clean up
   and leave with 130/143/129; EXIT keeps its cleanup-only behaviour, and `cleanup` is explicitly idempotent
   because both paths can reach it.
7. **Step 11's browser tree is reaped.** `cleanup` knew only about `$server_pid`, so a signal orphaned the
   whole `pnpm` → `node` → `chromium` tree; one was found still running 9.5 minutes after its orchestrator
   died, writing into the log of a run that no longer existed. Playwright now runs in its own process group
   (`setsid`, or job control as a fallback) in the background under `wait`, so the trap fires at once instead
   of being deferred until the browser run finishes, and one group kill takes the tree with it.
8. **The state directory is confirmed against the server, not assumed.** Step 10 wipes `$STATE/data/index.db*`
   from its own variable and never asked the server — the assumption that made this incident so hard to read.
   `start_server` now matches the `data_dir=` the server logs at startup against `$STATE/data`. The process on
   the port must also report the `version`/`commit` of the `dist/shelf` this round checked, and `--no-build`
   records a failure if that binary predates the sources it embeds.

### Standing rules

- **Never** change `internal/httpapi/books.go`, `web/src/api/types.ts` or any file in
  `internal/httpapi/testdata/golden/` on the strength of this E2E line. The frontend already expects 404
  (`web/src/api/types.ts:31`, `web/src/api/errors.ts:15`, `ViewerPage.tsx:300-303`) and separately consumes the
  200 + `error` path for `status != "ok"` (`SeriesDetailPage.tsx:100`, `features/series/volume.ts:62-63`). Both
  paths are implemented and both are correct. `scripts/contractcheck` diffs *shapes*, not status codes, so no
  status change is in its scope either.
- Widening this endpoint to resolve ids from the filesystem — so a book survives an index wipe — would be a
  **contract change** and requires a new `A-` amendment per `docs/impl-plan.md:737-739`, not a silent 200.
- **The 2026-07-29 E2E run is void in its entirety**, not just step 10. Steps 4–11 were all graded against a
  warm foreign index and cache, including the Playwright suite (see `docs/HANDOFF.md` §5.3). Re-run it, on a
  port proven free, before treating any of its numbers as evidence.
- Before blaming product code for an E2E-only failure, **prove which process answered**. The cost of not doing
  that here was one wrong bug report, one wrong suspect file, and a full 15–25 minute E2E cycle.

---

## Wave-5 rulings — the §5.5 / §5.6 close-out (orchestrator, 2026-07-29) — BINDING

Both rows below settle a question a previous document left open **in the wrong direction**: E-23 unblocks work
that `docs/HANDOFF.md` §5.5 had parked as "needs the user's call" on a premise that measurement disproves, and
E-24 answers an implementer who escalated a number that `impl-plan.md` had already fixed. Neither invents a
requirement; both stop one being re-litigated.

| # | Question | Ruling | Basis |
|---|---|---|---|
| **E-23** | **Does covering the RTL 양면 rule (ui-spec §6.2 / impl-plan §6.3 WP-11 acceptance 4) require adding a portrait volume to the §6.3 curated ten?** `docs/HANDOFF.md` §5.5 said it did — "명세 범위 변경이라 사용자 판단 없이 진행하지 않았다" — and blocked the work for two sessions on that basis. | **No. Do not change the §6.3 curated set for this, and do not re-open the question.** Two of the ten already-curated series are 100 % portrait **in both real and synthetic mode**, so the witness the rule needs was always inside D-48's subset: `[만화] 자살도114-122` (181 / 181 portrait pages, real; 11 / 11 synthetic) and `[만화] 미생 1~9 (완결 pdf)` → `미생 1권.pdf` (306 / 306 portrait, MediaBox ~378×548 pt; 4 × 595×842 synthetic). The coverage accordingly lives in **`04-viewer.spec.ts` 6.6b** (raster path, 자살도) and **`05-pdf-and-large.spec.ts` 6.8** (pdfium path, 미생), both with the two-frame branch made **unconditional**. `[만화] 군계 1~25` stays the subject of 6.6 and must not be swapped for a portrait book: at 0 / 104 portrait it is the **only** curated volume that exercises FR-VWR-004's landscape auto-split, and replacing it would trade one covered rule for another. Corollary, binding on any future E2E spec: **a two-frame check may not sit behind a guard that can silently skip** — assert the precondition (`toHaveCount(2)`) instead of testing it (`if (count === 2)`), and assert the frames are painted (`[data-status="ready"]`) before measuring any geometry. | Measured read-only over 100 % of the pages of every curated first volume (PIL for rasters, poppler `pdfinfo` MediaBox for the PDFs), not sampled; 군계's 104 / 104 landscape figure was independently re-confirmed at the same time. D-48 (the curated root) and D-49 (the synthetic twin reproduces the same ten shapes) already bind the set, and neither says anything about page orientation — so no amendment is needed, only the measurement nobody had taken. The guard corollary is `docs/HANDOFF.md` §6.5: at desktop-1440 the pre-existing `if (count === 2)` block passed against a frame that had **not painted** (an undecoded `PageFrame` has an absolutely-positioned `opacity:0` child, hence content width 0 — a zero-width box that still carries a bounding rect), proven to the pixel by scanning `docs/e2e-shots/step-06-8b-viewer-pdf-rtl-spread-desktop-1440.png`. |
| **E-24** | **Is `scripts/e2e-assert.py`'s AC-008 budget of 200 ms a ruling or a habit, and may the measured page set drop page 900?** The repair implementer read the 200 ms as unowned ("nothing in decisions.md or impl-plan.md rules on it") and escalated it, and the first rewrite derived the jump set from `page_count`, which silently dropped page 900. | **It is a ruling, and page 900 stays.** `docs/impl-plan.md:966` (§6.3 step 5) is the binding description of *this* script — `scripts/e2e.sh` names the step `7 · curl assertions (impl-plan §6.3 step 5)` — and it spells out `GET /api/books/{battle_royale}/pages/900` returning `200 image/jpeg` **in < 200 ms**. So: page 900 is measured **by name and unconditionally**, and "the volume reaches page 900" is a **precondition to assert**, never a guard (`{900} if pc >= 900 else set()` would make a named requirement disappear on exactly the volume that broke it). The 200 ms does **not** conflict with I-8's `p95 TTFB < 100 ms warm` (`docs/impl-plan.md:890`, measured by `integration/perf_test.go`): those are two different measurements of the same acceptance criterion — I-8 is the 95th percentile of time-to-**first**-byte over 50 warm jumps; this suite is the worst time-to-**last**-byte over a handful of **cold** ones, through CPython/urllib over loopback. **Neither number is `e2e-assert.py`'s to change.** No page under measurement may be pre-warmed: warm page 1 and never measure page 1, exactly as `integration/perf_test.go` does. | §0 precedence puts `impl-plan.md` above `arch-backend.md`, and `impl-plan.md` carries both numbers itself, so there is nothing to reconcile — only two measurements to keep labelled. prd AC-008 (`docs/prd.md:345`) is about an **arbitrary**, i.e. never-before-served, jump, which is why the old code measuring its own warm-up page was a defect and why the fix makes every measured page cold. A ZIP page has no server-side per-page cache at all (`serveArchivePage` streams from a stored offset), so "warm" can only ever mean the openpool handle and the index rows — which is what I-8 means by it. |

**Note on what was deliberately NOT ruled.** Two questions surfaced in the same close-out and are **not**
recorded as rulings, because nobody with spec authority answered them. They are carried as open items in
`docs/HANDOFF.md` §5.6.1 instead, so this log keeps only what was actually decided.

1. **The `?verbose=1` thumb counters' contract status** (§5.6.1 D). `scripts/e2e-assert.py` now polls
   `GET /api/health?verbose=1`'s `thumbs.{cover_depth,page_depth,active,inflight}` as a hard gate, while
   `arch-backend.md` §7.4 does not specify the `?verbose=1` payload at all and `internal/httpapi/dto.go` calls
   the block *"diagnostics, not contract"*. Four diagnostic fields are now load-bearing without anyone having
   decided they are contract. **A decision is needed before any of the four is renamed or removed.**
2. **What a touch device gets from the `SeriesCard` hover overlay** (§5.6.1 C). `ui-spec` §4.5 specifies the
   overlay as hover-revealed and says nothing about a pointer that cannot hover; `VolumeTile` ships an
   `[@media(hover:none)]` fallback and `SeriesCard` deliberately does not. `SeriesCard.tsx` argues the case in
   place — a touch user loses a shortcut but no destination, because `상세` and the cover share `onOpen` and the
   series screen's own always-visible `이어 읽기` is the same route — and its comment ends *"Changing that is a
   product ruling, not a component's call; escalated with no ruling in decisions.md yet."* ~~That is still true.~~
   **[2026-08-01] 판정 E-29가 이것을 닫았다: 폴백 없음, 지금 동작이 그대로 명세다.** 근거와, 뒤집을 경우
   반드시 지켜야 하는 전제(스크림은 무조건 `pointer-events-none`)는 E-29에 있다.

---

## E-25 — the settings screen must name the configuration file it is telling the user to edit (orchestrator, 2026-07-30) — BINDING

The row below closes an item that three earlier decisions all assumed was already closed. C-5, ruling E-3 and
arch OQ-3 each accept **read-only roots** on the strength of a sentence the UI shows the user — and the
sentence shipped without the one thing that makes it actionable. This is `docs/HANDOFF.md` §6.5 in its
purest form: the note was reviewed, the note was tested, and what the note omitted was neither.

| # | Question | Ruling | Basis |
|---|---|---|---|
| **E-25** | **Where does the resolved `shelf.yaml` path enter the contract, given that C-5, ruling E-3 and arch OQ-3 all promised the settings screen would show it and none of them said which payload carries it?** `RootsPanel.tsx` and `SettingsDialog.tsx` have carried an optional `configPath` prop since WP-10, documented as "unwired until the contract carries a config path"; `Overlays.tsx` and `LibraryPage.tsx` — the only things that mount them — never had a value to pass, so every user saw `shelf.yaml을 편집한 뒤 재시작하세요` beside no file name at all, and the onboarding screen's `설정 파일 위치 보기` copied the literal string `shelf.yaml`. | **`GET /api/settings` → `Settings.server.config_path: string`, amendment A-10**, absolute and cleaned, read-only like every other `server.*` key (a `PUT` carrying it is `400 bad_request` under the existing whole-block rule — no per-key allowlist exists and none is to be added). **Not `/api/health`**: its `?verbose=1` block is `internal/httpapi/dto.go`'s *"diagnostics, not contract"*, and the close-out note at the end of the Wave-5 section records that four of its fields are already load-bearing without a decision — this field is not to become the fifth. **Not `/api/roots`**: the path is a property of the server, not of a root, and `RootsResponse` would have to carry it once per row or grow a sibling key. The server computes it with the new **`(*config.Config).AbsFilePath()`**, and **`Config.FilePath` is never rewritten**: entry 2 of the lookup order (`./shelf.yaml`) makes it legitimately relative, but it is also the prefix of every message of `config.Error`, which quotes the path the user named. Both consumers read the field from `useSettings`, never from a prop supplied by a caller: `RootsPanel` fetches it exactly as it fetches its roots (a `useSettings()` in `SettingsDialog` or `Overlays` would fetch on every page, because `Overlays` mounts the dialog permanently), and `LibraryPage` passes it to the pure `Onboarding`. | The lookup order has **four** entries — `$SHELF_CONFIG`, `./shelf.yaml`, `$XDG_CONFIG_HOME/shelf/shelf.yaml`, `/etc/shelf/shelf.yaml` (`cmd/shelf/flags.go`) — so "edit shelf.yaml" identifies a file only for a reader who already knows which one won. An **absolute** path is the whole requirement and not a detail: the case the field exists for is a user who ran the binary from the directory holding the file, and for that user the raw `FilePath` is the four-character answer they already had. The Go check is therefore not `assertGolden` alone — a golden file will pin `""` as happily as a path, and it did during this work — but an explicit non-empty + `filepath.IsAbs` + basename assertion, plus a case that sets `FilePath` to a relative path and re-reads the endpoint. The frontend check drives the value MSW → `useSettings` → panel for the same reason: a test that passes `configPath` as a prop re-tests the one thing that was never broken. **Disclosure was weighed, and the answer is that it is not a new one.** An absolute path usually contains the OS user name, and `GET /api/settings` is unauthenticated whenever the configuration carries no `auth:` block (§8.2 — auth is optional, and when on it is all-or-nothing); but the same endpoint's neighbours already publish `Root.path` and `CacheUsage.cache_dir`, the two absolute paths `internal/httpapi/harness_test.go` names as "genuinely absolute paths" it cannot pin in a golden file. A-10 is therefore a third instance of an exposure the API already has — anyone who can read `/api/settings` can already read every root's location — and it changes no threat model; had it been a *new* class of disclosure, the answer would have been to gate it, not to ship it and hope no one asked. |

---

## E-26 — the settings screen may add and remove roots; D-33 and E-3 are overturned in part (orchestrator, 2026-07-30) — BINDING

**This is a reversal, and it is recorded as one.** D-33 and ruling **E-3** deleted `+ 루트 추가` and `제거` from
the settings screen and forbade `POST`/`DELETE /api/roots`. Neither was wrong on the evidence it had: prd 5.2
UI-004 scopes that screen literally to *"루트 목록 조회 및 수동 재스캔 실행"*, `impl-plan.md` §0.1 ranks prd above
`design.md`, and `design.md` 화면 4's *"루트 관리: 목록, **추가/제거**"* lost on precedence. That reading is not
being revised. **The owner of the requirement has extended the requirement** — §0 precedence exists to serve
the person who wrote the prd, so when that person answers the question directly, the answer is the top of the
stack rather than a fourth document competing with the other three.

The question was also asked under the right conditions. `docs/HANDOFF.md` §5.6.1 **J** parked it deliberately
behind **I** — *"먼저 I를 닫는 것이 훨씬 싸고, 사용자가 실제로 겪는 불편의 상당 부분을 해소할 가능성이 높다"* — on
the theory that most of the pain was not knowing *which* of four candidate files to edit. Ruling **E-25**
closed I, and the settings screen now shows the absolute path of the configuration this server loaded. J was
re-asked with that in hand, so what is being bought here is *"I would rather not open the file at all"*, not
*"I cannot find the file"*.

**What the reversal costs, written down once so nobody has to rediscover it:**

1. **A write path into the configuration file**, in a product whose one-line summary of itself has been "SHELF
   never writes". That guarantee (FR-CFG-005 / NFR-DAT-002) is about *media volumes* and is untouched — but it
   is now a sentence with an exception clause in it, and every future reader of `internal/config` has to learn
   which half applies to them.
2. **A new authority on a server that ships open.** See the Basis column: this is the reason the feature is
   off by default, and the reason the switch is a YAML key rather than a UI toggle.
3. **A new value in the frozen `ErrorCode` enum** — the fourth amendment to touch §7.2's contract, landing in
   `arch-backend.md` §7.2, `internal/httpapi/errors.go`, `web/src/api/types.ts` and `web/src/api/errors.ts`,
   plus a golden file. Ruling **E-13** / amendment **A-9** is the precedent and the cost estimate.
4. **A YAML round-tripper where there is none.** `internal/config` is read-only today: there is not one
   `yaml.Marshal` in product code. Comment preservation is not a nicety — `shelf.example.yaml` is 14 KB of
   which the overwhelming majority is explanatory comments, and a writer that reformats the user's file is a
   writer that destroys the documentation the product ships *inside* the file.
5. **A `제거` button whose effect is deliberately invisible for one restart, and partly invisible after it.**
   Decision 3 below keeps the data; the consequence is spelled out under *Deliberately not ruled*.

| # | Question | Ruling | Basis |
|---|---|---|---|
| **E-26** | **May the settings screen add and remove roots, reversing D-33 and ruling E-3?** The user raised the absence of `design.md` 화면 4's 추가/제거 (`docs/HANDOFF.md` §5.6.1 **J**), was told it was a ruling rather than a defect, and — after ruling E-25 landed the configuration path — has overruled the ruling. E-3 had already priced the reversal at *"roughly a whole extra work package, and a new security surface"*. | **Yes, as amendment A-11: `POST /api/roots` and `DELETE /api/roots/{name}`, under seven limits that are part of the ruling and not implementation detail.** (1) **Restart-based, not hot-reload.** The endpoints edit `shelf.yaml`; the running server does not adopt the change. (2) **Opt-in.** New config key **`server.allow_root_editing`**, default **false**; with it false both verbs are **403**, and the capability is mirrored read-only into `Settings.server` so the UI knows whether to render the controls at all. (3) **Removal keeps the data.** `DELETE` removes the entry from the file and nothing else; index rows and reading progress stay. The UI must say so at the point of removal. (4) **Path validation on `POST`**: absolute, cleaned, symlinks resolved for comparison; must exist, be a directory and be readable; must not duplicate an existing root; and **must not be an ancestor or descendant of one**. Each rejection is a `400` naming the rule it broke. (5) **`name` is server-generated** from the label or the directory's base name, uniquified against the current configuration. (6) **The write is atomic and comment-preserving** — temp file in the same directory, fsync, rename, mode preserved, `.bak` of the previous contents — and a round-trip test against the real `shelf.example.yaml` is mandatory. (7) **`Settings.server` gains a truthful restart notice**: a boolean saying the file on disk differs from the one this server loaded. Normative text: **arch §7.2, §7.4, §7.8, §3.2, §12 OQ-3**; amendment row and follow-up work: **impl-plan §0.3 A-11**. | **The gate is the whole security argument, and it is not theoretical.** The default deployment binds every interface — `shelf.example.yaml`'s `server:` block ships `listen: "0.0.0.0"` — and ruling **E-8** deliberately ships **no `auth:` block at all**, so the default server is an unauthenticated listener on a LAN. An ungated write API there would let anyone who can reach the port make the server open an arbitrary directory, and the rest of the API would then dutifully publish it: `GET /api/roots` returns `Root.path` absolute, and `GET /api/series` → `GET /api/books/{bid}/pages/{n}` serves the contents. **This is not a path-traversal defect** — NFR-SEC-001's layer 1 still holds, every read still resolves an opaque id against a configured root — **it is worse in kind, because the caller does not need to escape a root; they can add one.** Gating on a YAML key resolves it exactly: the privilege is granted by the one person who can already edit the YAML, i.e. by someone who could add the root by hand anyway, so the key confers no authority its holder did not already hold. That is the same shape as prd **FR-CFG-001** making the file the source of truth for *which* roots exist — it now makes it the source of *permission* as well. `false` by default is load-bearing rather than conservative decoration: with E-8 there is no password to fall back on, so a default-on switch would be a remote-configuration API with no authentication in front of it. |

### What survives of D-33 and E-3

Naming the surviving part is half the ruling. **All three of these still stand and are not reopened:**

* **The YAML remains the source of truth** (prd FR-CFG-001). The endpoints do not maintain a second copy of the
  root list, do not write roots into `index.db` or `user.db`, and do not take effect without the file. A root
  that exists is a root that is written in `shelf.yaml`; that sentence is exactly as true after A-11 as before.
* **The settings screen is still not a general configuration editor.** `roots[]` is the only block reachable
  over HTTP. Everything else in §3.2 — `server:`, `storage:`, `scan:`, `thumbnails:`, `pdf:`, `library:`,
  `auth:`, `log:` — stays file-only, and `Settings.server` stays a read-only mirror rejected on `PUT`
  (arch §7.8, unchanged). Even inside `roots[]` the reachable surface is add and remove: **`enabled` is not
  editable over HTTP** (prd FR-CFG-002 is 선택 and no screen asked for it), and neither are `name`, `path` or
  `label` of an existing root, because changing `name` orphans that root's reading progress (D-14, D-51) and
  changing `path` silently re-points every id in it.
* **There is still no filesystem browser, and no endpoint that lists directories.** E-3's cost estimate
  included "an onboarding flow that can browse the filesystem"; that part is **not** bought. `POST /api/roots`
  takes a path the user types or pastes. A browse API would hand the whole readable directory tree of the host
  to an unauthenticated LAN listener (ruling E-8), which is precisely the hazard the gate in decision 2 exists
  to contain — and it would be reachable *before* anyone had granted the privilege, since browsing is a read.
* **Nothing else in prd 5.2 UI-004 changes.** Cache usage and purge, the reading defaults, the theme, the
  prefetch count and the scan-log panel are all untouched. The per-root 재스캔 of E-3 is untouched. C-5's
  instruction sentence — `shelf.yaml을 편집한 뒤 재시작하세요` — is untouched and becomes *more* necessary, not
  less: with A-11 the server is one of the things that edits the file, so the user needs to be told when it did.

`arch-backend.md` **OQ-3** keeps its 2026-07-28 answer visible and dated, with the 2026-07-30 answer beside it.
Read-only was the right v1 answer for two years' worth of reasoning and about four days of calendar; deleting it
would leave the next reader wondering why `roots.go` was written the way it was.

### The error code, and the amendment it forces

Decision 2 fixes the status at **403**. §7.2's enum has **no code for it**, and this was checked against the
enum rather than assumed:

* **`unauthorized` (401) — rejected, and it would have been actively harmful.** Its meaning is "auth enabled
  and the session is missing or expired", and `web/src/api/errors.ts`'s `isAuthError()` keys the whole re-auth
  path of ruling **E-17** off it. A correctly authenticated user — or a user on the default server, which has
  no password at all — would be shown a login screen for a refusal that no login can lift.
* **`unsupported` (501) — the nearest neighbour, and still wrong.** It has a genuine claim: arch §7.6 already
  answers `501 unsupported` for a PDF page "in a `nopdf` build **or with `pdf.enabled: false`**", so a config
  key disabling a capability already produces this code. But its documented meaning in all four copies of the
  enum is *"feature absent from this build"*, and the two answers differ in the only way the user cares about —
  the remedy. `unsupported` means "run a different binary"; this means "set one key in the file whose absolute
  path this same API already publishes as `Settings.server.config_path`". Pairing it with 403 would also make
  one code map to two statuses, and `web/src/api/errors.ts`'s `STATUS_TO_CODE` a non-function in both
  directions.
* **`bad_request` (400) — rejected.** The precedent for a code/status mismatch is `405` carrying `bad_request`,
  and §7.2 justifies it as "this verb does not exist here", i.e. genuinely a malformed request produced by the
  *router*. A well-formed `POST` to a configured server that has the feature switched off is not malformed.

So **none of the ten fits, and A-11 adds `"forbidden"` (403)** to the enum. The name follows the enum's own
convention — every member is named after its status, not after its cause (`bad_request`, `not_found`,
`conflict`, `unprocessable`, `rate_limited`, `unavailable`) — and it stays reusable if a second gated write ever
lands, which a per-feature name like `root_editing_disabled` would not. **The precedent is ruling E-13 /
amendment A-9, and it is exact**: §8.2 mandated a `429` that §7.2 could not name, so the enum gained a name and
the client's status-only workaround became the contract. Here a ruling mandates a `403` that §7.2 cannot name.
A response the contract cannot name is a contract defect, and it is cheaper to fix now than after a client has
invented its own answer for it.

### Deliberately not ruled — the phantom row

**`GET /api/roots` lists roots from the index, not from the configuration** (`internal/httpapi/roots.go`,
`handleRoots` → `idx.ListRoots`), and `App.reconcileRoots` (`internal/app/app.go`) deliberately keeps the rows
of a root that has left the configuration: *"absence from one run is never evidence of absence on disk"*
(arch §4.9). Both are correct and neither changes here. The consequence is that **a removed root keeps
appearing in the settings list even after the restart the UI demanded**, with `available: false` — which is the
same payload an unplugged drive produces, because `rootAvailable` answers through the `*os.Root` handle set
that startup opened from the configuration.

That is not a new behaviour: hand-editing a root out of `shelf.yaml` has always done exactly this. What is new
is that a **button** now produces it, and "the user pressed 제거, restarted as instructed, and the row is still
there looking like a hardware fault" is the shape this project's own `docs/HANDOFF.md` §6.5 catalogues.
**Nobody with spec authority has ruled on what the settings screen should show for a root that is in the index
but not in the configuration**, so it is carried here as an open item rather than invented in an amendment. The
three candidate answers, none of them chosen: filter `GET /api/roots` to configured roots (hides indexed data
and changes a frozen endpoint); add a read-only `Root.configured: boolean` and let the UI label the row
(smallest honest change, but it is contract surface); or leave the payload alone and explain it in the removal
dialog only. **A-11 requires the third as a floor** — the confirmation dialog must state that the index rows and
the reading progress are kept — and settles nothing beyond it.

### REVISION 2026-07-30 (later the same day) — the row disappears: decisions 3 and 1 are narrowed

**This section is a revision of E-26, not a rewrite of it.** Everything above stands as it was written and is
what E-26 first said; the two changes below are named R1 and R2, and each one states what it replaces. The
trigger is the user's instruction that this screen follow the **Claude Design prototype**, in which pressing
the trash button makes the root's row *disappear*. That is a requirement about what the user sees, and two of
E-26's answers cannot produce it.

#### R1 — `DELETE` purges the removed root's index rows, and takes effect immediately

**What E-26 said** (decision 3, and §7.4's `DELETE`): *"Removes that entry from the `roots:` list. It removes
nothing else."* The phantom-row subsection above then priced the consequence honestly and left it open: the
row keeps appearing after the restart the UI demanded, with `available: false`, and *"nobody with spec
authority has ruled on what the settings screen should show"*.

**Somebody now has.** The prototype shows the row gone, and the three candidate answers parked above are no
longer equivalent: the second and third both leave the series in the library, because `GET /api/roots` reads
`idx.ListRoots` **and** because `GET /api/series` has no configured-root filter either — the removed root's
series stay listed, searchable and readable. "제거" that leaves the shelf exactly as it was is not the word's
meaning.

**What is now required.** `DELETE` writes the YAML **and** deletes that root's rows from `index.db`
(`index.DeleteRoot`, which is already one SQL transaction over `pages`, `books`, `series` and `roots`). This
is affordable precisely because `index.db` is **derived and disposable** — arch §3.5's own heading, and
`shelf.example.yaml`'s own words: *"delete it, restart, and it rebuilds"*. **`user.db` is not touched.**
Reading progress, per-book preferences and `series_seen` survive, and they *reattach* if the same directory is
added again under the same generated name — which is exactly why A-11 uniquifies names against the
**configuration** and not the index (§7.4, "How a root `name` is generated", step 3). That paragraph was
written for a rule that then had nothing to protect; R1 is what makes it load-bearing.

**The live server must stop scanning it before the restart.** The removal takes effect in the running process
through an **in-memory removed-set**, mutex-protected, which (a) excludes the root from `GET /api/roots`,
(b) makes `POST /api/scan {"roots":["<name>"]}` answer **`404`**, and (c) skips it in a full scan. That is the
whole of it: the open `*os.Root` set, the handle pool and the source factory are **not** hot-swapped, and
"restart-based" is unchanged for everything else. A page URL for a book under the removed root keeps working
until the restart, because the root's rows are gone from the index and the id no longer resolves — it 404s,
which is the same answer it will give after the restart.

**`App.reconcileRoots` does not change.** Its warn-and-keep behaviour for a root that has left a
**hand-edited** YAML is correct and stays exactly as it is: *"absence from one run is never evidence of absence
on disk"* (arch §4.9). The distinction R1 draws is evidential, not behavioural — **an explicit `DELETE` is
evidence of intent; a missing line in a file is not.** A typo in the YAML must still cost nothing. Pressing a
button labelled 제거 and confirming a dialog is a different act, and it is the only path that purges.

**Consequences for the ruling's own text.** Decision 3's *"Removal keeps the data"* now means *"removal keeps
the data you authored"* — `user.db`, not `index.db`. The confirmation dialog's mandated sentence changes with
it: it must say that **reading progress is kept** and must **not** promise that the index rows are.

#### R2 — `POST` makes the new root appear immediately, as a *pending* row

**What E-26 said** (§7.4): *"`GET /api/roots` is deliberately unchanged until then — a `POST` that appeared to
work and then served nothing would be worse than one that says what it did."* The reasoning is right and is
kept; the conclusion drawn from it is not the only one available. In the prototype the added root appears in
the list at once, and a row that is present and *labelled* as not yet loaded is not the failure that sentence
guards against — a row that *claims* to be loaded is.

**What is now required.** `GET /api/roots` also reports roots that are in the **configuration file on disk**
and have no index row, marked **pending**: no counts, no scan timestamps, `available: false`, and no rescan
offered. The `Root` DTO gains one boolean for it (§7.3 / §7.4), named in the house style and carrying its
reason in the comment. The server already re-reads and re-hashes that file on every `GET /api/settings` for
`config_changed_on_disk`; **the `roots:` list is read from that same code path**, not from a second one, so
there is exactly one place that knows what the file currently says.

A pending row is *not* a hot-add. Nothing is opened, nothing is scanned, and the row stays pending until the
restart — which is precisely why it must say so, and why the restart notice
(`Settings.server.config_changed_on_disk`) is what tells the user how to make it real.

#### What R1 and R2 do not change

The gate (decision 2), the default of `false`, the validation table, server-generated names, the atomic
comment-preserving write, "no filesystem browser", "no `PATCH`", and the fact that **the running server does
not adopt a new root** are all untouched. R1 and R2 are about what the user is *shown* and what a removal
*means*; neither buys the hot-reload the user declined.

## E-27 — 뷰어는 크롬 없이 열리고, 맞춤 '화면'은 컨트롤에서 사라진다 (오케스트레이터, 2026-07-31) — BINDING

**출처.** 사용자의 Claude Design 프로토타입 `만화방.dc.html`(프로젝트 `ad00fd5c`)이 갱신됐고, 사용자가
"클로드 디자인에서 수정한 부분을 반영"할 것을 지시했다. 아래 두 항목은 그 파일이 **기존 명세와 충돌하는**
지점이라 판정으로 남긴다. 나머지(브랜드·아이콘·치수)는 명세와 충돌하지 않아 판정 없이 반영했다.

### 1. 맞춤 모드는 세 종이다 — 너비 · 높이 · 원본

프로토타입이 네 번째 옵션 `fitS`(화면)를 삭제했다. `docs/prd.md` **FR-VWR-005는 네 종을 `필수`로**
규정하고 있었으므로 이것은 패치가 아니라 **PRD 개정**이다. 사용자가 개정을 택했다.

| | |
|---|---|
| UI | 뷰어 상단 바의 맞춤 `.seg`는 **너비 · 높이 · 원본** 세 개다. 설정 화면에는 애초에 맞춤 컨트롤이 없어 변경 없음 |
| 계약 | **`arch-backend.md` §7은 바뀌지 않는다.** `fit_mode`의 열거값은 여전히 `width\|height\|original\|contain`이고 `PUT /api/books/{id}/prefs`는 `contain`을 계속 받는다 |
| 기존 데이터 | `user.db`에 `contain`으로 저장된 책은 **'높이'로 열린다**(`store/viewer.ts`의 `openingFit`). 저장값 자체는 다시 쓰지 않는다 |
| 기하 | `fit.ts`의 `contain` 기하는 **그대로 두고 그대로 테스트한다**(`fit.test.ts`). 사라진 것은 도달 경로지 계산이 아니다 |

**왜 삭제와 강제변환이 한 쌍인가.** 컨트롤만 지우면 `contain`으로 저장된 독자는 **자기가 어느 맞춤에
있는지 볼 수도 없고 거기서 빠져나올 수도 없다** — 어떤 라디오도 선택돼 있지 않은 세그먼트를 보게 된다.
계약을 그대로 둔 이유는 반대쪽 대칭이다: 값을 거부하면 개정 이전에 쓰인 `user.db`가 읽히지 않는다.

### 2. 뷰어는 크롬 없이 열리고, 크롬은 마우스 이동으로 깨어나지 않는다

프로토타입이 뷰어의 표시 모델을 통째로 바꿨다. `design.md` 원칙 2("읽는 동안에는 UI가 없다")를 실제로
지키는 쪽으로 간 것이며, 종전 구현은 그 원칙을 **가장 중요한 순간에** 어기고 있었다 — 책의 첫 프레임이
페이지 위에 얹힌 컨트롤 세 줄이었다.

| 규칙 | 종전 | E-27 이후 |
|---|---|---|
| 열 때 | 크롬 표시 | **크롬 없음** + 힌트 한 줄(3 400 ms) |
| 마우스 이동 | 크롬을 깨움 | **아무 일도 하지 않음** (커서만 다시 나타남) |
| 페이지 넘김 · 세로 스크롤 | 크롬을 깨움 | **깨우지 않음** — 읽기는 인터페이스를 부르지 않는다 |
| 커서 숨김 | 크롬에 연동 | **포인터 유휴 1 600 ms**에 연동 |
| 크롬을 부르는 것 | 마우스 이동 · 중앙 탭 | **화면 상·하단 44px 가장자리 · 중앙 탭 · `H` 키** |
| 바 위 호버 | 2 200 ms 뒤 사라짐 | **자동숨김 보류(hold)**, 벗어나면 재무장 |
| 자동숨김 | 2 200 ms | **2 600 ms** |
| 크롬이 켜졌을 때 바 | 페이지를 덮는 오버레이 | **flex 흐름에 참여**해 스테이지를 밀어냄 |

**함께 가야 하는 것 둘.** ① `H` 키와 좌/우·중앙 클릭이 **단축키 시트에 들어간다.** 크롬이 스스로
나타나지 않는 이상, 그것을 부르는 방법은 어딘가에 적혀 있어야 한다. ② **`파일이 변경되었습니다`
경고(FR-VWR-009)는 크롬에서 분리한다.** 종전에는 `stale && chromeVisible`이었는데, 그대로 뒀다면 E-27이
그 경고를 조용히 삭제했을 것이다 — 크롬이 스스로 뜨지 않으니 아무도 보지 못한다.

**반영하지 않은 것 (근거 있음).**
- **`--grid-min`**: 프로토타입은 뷰포트별 120/136/150/168을 쓰지만, 로컬 표(150/224/150/152)는
  `ui-spec.md` §7이 명시한 **열 수**(2 / 3 / 4–5 / 6–8)에서 역산된 값이고 `useLibrary.test.ts`가
  `tokens.css`와의 일치를 고정한다. 프로토타입 값은 <768에서 3열이 되어 §7과 어긋난다.
- **표·목록의 가로 스크롤**: 프로토타입은 `min-width` + `overflow-x:auto`로 좁은 화면을 처리하지만,
  로컬은 이미 브레이크포인트별 컬럼 변형(`md:`/`lg:`)으로 같은 문제를 풀어 놓았다. 가로 스크롤로
  되돌리는 것은 후퇴다.

---

## E-28 — 상·하단 컨트롤은 접히지 않고 접히며, 권 전환은 진입이 아니다 (오케스트레이터, 2026-08-01) — BINDING

**출처.** 같은 프로토타입 `만화방.dc.html`(프로젝트 `ad00fd5c`)의 재갱신. 사용자의 지시는
"상하단 컨트롤영역에 대한 처리와, 만화전환처리에 개선이 있었습니다. 정확히 반영해주세요"다.
E-27이 크롬을 **언제** 보일지 정했다면, 이것은 크롬이 **어떻게 접히고 무엇 위에 놓이는지**를 정한다.
아래 두 항목만 기존 명세와 충돌하므로 판정으로 남기고, 나머지(슬라이더 치수, 썸네일 슬롯, 탭 존 비율,
힌트 문구)는 충돌이 없어 판정 없이 반영했다.

### 1. 좁은 화면에서 상단 바는 `⋯` 시트로 숨지 않고 **줄바꿈한다**

`ui-spec.md` §7의 <768 행은 *"Top overlay keeps only 뒤로 + title; all controls move to a bottom sheet
opened by a `⋯` button"* 이었다. 프로토타입은 시트를 갖고 있지 않고, 세 `.seg` 그룹을 **모든 너비에서
인라인으로 유지한 채 바를 여러 줄로 접는다** — 실측 55px @1440 · 103px @900 · 151px @500.
**§7의 그 행을 개정한다.**

| | |
|---|---|
| 상단 바 | `flex-wrap` + 각 `.seg`의 `flex-none whitespace-nowrap`. 그룹은 **통째로** 다음 줄로 내려간다 |
| 하단 바 | 컨트롤 행에도 `flex-wrap`. 좁아지면 슬라이더가 눌리는 대신 제 줄을 갖는다 |
| 스테이지 | E-27이 바를 flex 흐름에 넣어 뒀으므로, 세 줄이 된 바는 페이지를 덮지 않고 **스테이지를 줄인다** |
| 삭제 | `[data-role=viewer-control-sheet]`, `뷰어 컨트롤` 버튼, 그리고 그 시트를 떠받치던 세 장치 — 시트가 열린 동안의 크롬 고정, 크롬이 사라질 때의 시트 동반 폐쇄, 두 바의 z-index 금지 |

**시트를 없앤 이유는 취향이 아니라 ②의 전제다.** 시트는 상단 바의 자식이면서 `z-overlay`로 바 **바깥에**
그려져야 했고, 그래서 두 바 어느 쪽도 z-index를 가질 수 없었다(스태킹 컨텍스트가 생기면 시트의 탈출이
바 안에서 해소되고 하단 바가 그 위를 덮는다 — 400px에서 실측된 결함). 시트가 사라지면 그 금지도 사라진다.
`viewer-overlay-400-broken.png`가 잡았던 원래 문제(그룹이 넘치고 라벨이 세로로 깨짐)는 `flex-wrap` +
`whitespace-nowrap`이 같은 자리에서 해결한다.

### 2. 권 끝 카드는 크롬을 덮지 않는다 — 두 바가 스크림 **위**에 있다

프로토타입의 층 순서는 가장자리 스트립 `z-2` · 탭 존 `z-1` · 스테이지 · **권 끝 스크림 `z:auto`** ·
**두 바 `z-3`** 이다. 즉 마지막 페이지에서도 뒤로 · 슬라이더 · 표시 모드 · 썸네일이 전부 살아 있다.
로컬 구현은 스크림이 DOM 마지막이고 어느 바에도 z-index가 없어 **스크림이 크롬 전체를 삼켰다** — 권의
끝에서 빠져나가는 길이 카드 자신의 버튼 두 개뿐이었다.

| | |
|---|---|
| 토큰 | `zIndex.chrome = 3` (뷰어 내부 층. 뷰어 자체는 여전히 `z-viewer` = 60) |
| 적용 | `[data-role=viewer-top-bar]` · `[data-role=viewer-bottom-bar]`에 `z-chrome` |
| 스크림 | z-index 없음. 스테이지 위, 바 아래 |

**바에 z-index를 주면 바 밑으로 들어가는 것이 하나 생긴다 — `파일이 변경되었습니다`(FR-VWR-009).**
종전에 이 경고는 `absolute top-14`(56px)로, 한 줄짜리 53px 상단 바를 **3px 차이로** 비켜 서 있었다.
바가 줄바꿈하기 시작하자(900에서 103px, 760에서 122px) 경고가 바의 상자 **안**으로 들어갔고,
`z-chrome`이 그 위를 덮었다. 고정 오프셋이 애초에 우연이었던 것이다. 그래서 이 경고도 **바와 같은 규칙을
따른다**: 크롬이 켜져 있으면 `order-first`를 공유하는 **흐름 속 한 줄**(DOM에서 바보다 뒤 → 항상 바 아래),
크롬이 없으면 다시 오버레이. 후자가 E-27이 이 경고를 크롬에서 분리한 이유이므로 그대로 둔다.

### 3. `다음 권 읽기`는 **진입이 아니라 계속**이다 (판정 아님, 기록)

프로토타입의 `goNextVol`은 권과 페이지만 바꾸고 그 외 아무것도 건드리지 않는다. 로컬 구현에서 권 전환은
같은 화면에 도착하는 **두 번째 `open()`** 이었고, 그것을 진입으로 취급한 탓에 권을 넘길 때마다 독자가
직접 올려 둔 크롬이 다시 내려가고, 썸네일 스트립이 닫히고, **한 번만 읽히면 되는 힌트가 3 400 ms 동안
다시 떴다.** `open()`은 이제 *이미 다른 책이 열려 있는 상태에서의 open*을 계속으로 보고 `chromeVisible` ·
`hintVisible` · `stripOpen`을 그대로 잇는다. `close()`가 `bookId`를 비우므로 뷰어를 나갔다 오면 다시
진입이다. 어느 문서와도 충돌하지 않아 판정이 아니라 기록으로 남긴다.

**함께 반영한 것 (충돌 없음, 프로토타입 실측치).**
- **슬라이더 상자**: 기본 24px, <768에서 44px(`--touch-min`). 종전에는 `PageSlider`가 **모든 너비에서**
  44px을 인라인으로 박아 데스크톱 하단 바가 설계보다 12px 높았다. 트랙은 뷰어에서 `.on-dark`로
  `--color-neutral-600`까지 올린다 — 어두운 바닥에서 `--color-divider`는 배경과 구분되지 않는다.
- **썸네일 스트립**: 셀이 `56×84 / md:48×72`인데 스트립의 슬롯(52px)과 트랙 높이(72px)가 고정이라
  <768에서 셀이 4px씩 겹치고 아래가 잘렸다. 슬롯 60/52 · 트랙 84/72로 셀을 따라가게 했다.
  **그리고 `virtualizer.measure()`를 슬롯 변경에 걸어야 한다** — `virtual-core`는 측정값을
  `[count, paddingStart, scrollMargin, getItemKey, enabled]` + 크기 캐시로 메모하고 **`estimateSize`는
  그 키에 없다.** 즉 `estimateSize`만 바꾸면 아무 일도 일어나지 않는다. 900→700에서 스트립을 연 채로
  실측: 셀은 56px로 커졌는데 피치는 52px 그대로여서 썸네일마다 4px씩 겹쳤고, 트랙은 97페이지가 필요로
  하는 5,820px 대신 5,044px에 머물러 꼬리 776px에 닿을 수 없었다. `measure()`는 크기 캐시를 새 Map으로
  갈아끼우고, 그것은 메모 키에 있다.
- **탭 존**: 30/40/30 → **32/36/32**(1440px 실측 461/518/461). 페이지를 넘기는 두 존이 큰 쪽이다.
  좌·우 존 위에서는 포인터가 깨어 있는 동안 `cursor:pointer`.
- **스와이프**: 임계 40 → **44px**, 수직 기각 `|dy| > |dx|`(종전 0.75배), 그리고 **600 ms** 제한.
- **힌트 문구**: `좌·우 클릭으로 페이지 · 중앙 클릭 또는 상하 가장자리로 컨트롤`.

---

## E-29 — `SeriesCard`의 호버 오버레이에 터치 폴백을 넣지 않는다 (오케스트레이터, 2026-08-01) — BINDING

**출처.** `docs/HANDOFF.md` §5.6.1 항목 C가 요청한 제품 판정. 같은 질문이 Wave-5 말미의
"판정하지 않은 것" **2번**으로도 남아 있고, `web/src/features/library/SeriesCard.tsx`의 근거 주석은
*"Changing that is a product ruling, not a component's call; escalated with no ruling in decisions.md yet"*
로 끝난다. 이 판정이 그 줄을 닫는다. **동작은 바뀌지 않는다 — 바뀌는 것은 지위다.** 지금까지 이것은
"근거가 적힌 미판정 사항"이었고, 이제부터는 명세다.

**판정한 사람은 사용자다**(8세션차, 2026-08-01). 항목 C가 여섯 세션을 열린 채 버틴 이유가 바로 이것이
컴포넌트가 정할 수 있는 문제가 아니었기 때문이므로, 세 선택지(폴백 없음 · 터치 전용 코너 칩 ·
`VolumeTile`과 같은 전체 폴백)를 실제 화면 모양과 함께 제시하고 물어서 결정했다. 아래 §2의 근거는
그 결정을 뒷받침하는 것이지 그것을 대신한 것이 아니다.

### 1. 판정 — 폴백 없음. 지금 동작이 그대로 명세다

| | |
|---|---|
| 동작 | 커버 위 오버레이(`이어 읽기`/`읽기 시작` + `상세`)는 `group-hover`·`group-focus-within`에서만 열린다. 호버할 수 없는 포인터에서는 열리지 않고 두 버튼은 도달 불가다 |
| 지위 | **의도된 동작.** `VolumeTile`의 `[@media(hover:none)]`을 여기로 옮기지 않는다 |
| 코드 | 변경 없음. `SeriesCard.tsx` 주석의 마지막 한 줄만 이 판정을 가리키도록 고친다 |
| 명세 | `ui-spec.md` §4.5 "Grid mode"가 **터치 기기가 무엇을 받는지 말해야 한다.** 종전에는 "호버로 드러난다"만 있고 그 반대편이 비어 있었다 — 판정이 필요했던 것은 그 공백이다 |
| 열려 있던 곳 | `docs/HANDOFF.md` §5.6.1 C, §5.7 우선순위 2번 |

### 2. 왜 — 터치 기기가 잃는 것은 목적지가 아니라 단축키다

- **`상세`와 커버는 같은 `onOpen` prop이다.** 오버레이의 두 번째 버튼은 커버를 탭하면 가는 곳과
  똑같은 곳으로 간다. 오버레이가 열리지 않아도 그 목적지는 잃지 않는다.
- **시작하지 않은 시리즈에서는 `onResume`도 `onOpen`으로 떨어진다** — `LibraryPage.tsx`의
  `resumeSeries`에 열 책 id가 없기 때문이다. 즉 그 경우 오버레이의 **두 버튼 모두** 커버와 같다.
- **진짜로 잃는 것은 하나뿐이다**: "시작한 시리즈의 마지막 읽던 권을 저장된 페이지에서 다시 연다".
  그런데 그것이 정확히 시리즈 화면의 **항상 보이는** `이어 읽기` 버튼이 하는 일이고
  (`SeriesHeader.tsx` → `resumeTarget`), 그 화면은 이 카드가 **이미 응답하는 탭 한 번** 뒤에 있다.
  이어보기 행(FR-LIB-010, `ContinueCard.tsx`)도 호버 게이트 없이 한 번의 탭으로 같은 일을 한다.
  하나의 목적지에 세 개의 경로가 있고, 호버 게이트에 걸리는 것은 그중 가장 짧은 하나다.

**기각한 대안 ① — `VolumeTile`의 `[@media(hover:none)]` 클래스를 그대로 옮긴다.**
`VolumeTile`이 폴백을 갖는 이유는 그 화면에 **두 번째 경로가 없기** 때문이고, 그쪽 오버레이는
코너의 66×29 px 버튼이다(`e2e/03-series-detail.spec.ts` 6.5 (guard)). 이쪽은 `--scrim-cover`
(ink 72 %, `tokens.css`)가 `inset-0` 전체를 덮는다. 같은 클래스를 옮기면 **모든 터치 기기에서
그리드의 모든 커버가 스크림으로 덮인다** — 라이브러리의 첫 화면이 통째로.
`docs/ui-shots/library-grid-card-hover-1440.png`가 그 카드 한 장이다. 두 오버레이는 이름이 같을 뿐
같은 물건이 아니고, 한쪽의 근거가 다른 쪽으로 옮겨가지 않는다.

**기각한 대안 ② — 터치에서 오버레이를 상시 표시한다.** ①의 같은 이유로 더 나쁘다. 그리고 이 카드의
디자인은 커버가 주인공이라는 것이다(design.md 원칙 1의 반대편) — 오버레이가 상시라면 커버 이미지는
읽을 수 없고, `FR-LIB-008`의 스트라이프 폴백과 `완독` 배지, 진행률 바가 전부 그 아래로 들어간다.

### 3. 뒤집을 경우의 전제 조건 (판정의 일부, 미래 세션용)

미래에 이 판정을 뒤집는 판정이 나오더라도, **스크림 div(`bg-scrim-cover`)는 무조건
`pointer-events-none`으로 남는다.** `pointer-events-auto`로 갈 수 있는 것은 **두 버튼뿐이고**, 그것도
오버레이를 *보이게* 만드는 것과 **정확히 같은 게이트** 아래에서만이다(보이지 않는데 클릭되는 버튼은
안 된다). 스크림은 `inset-0`이고 커버 버튼과 형제이며 z-index가 없어 **DOM 순서가 곧 페인트 순서**다 —
스크림이 포인터를 받는 순간 `docs/HANDOFF.md` §5.3 **1번**(커버 클릭이 죽던 결함)이 터치 기기에서
그대로 재발한다. 마우스는 클릭하기 전에 반드시 호버하기 때문이다.

> `library.test.tsx`가 이 불변식을 **jsdom이 볼 수 있는 형태로** 이미 고정하고 있다: 계산된 스타일이
> 아니라 **클래스 목록 자체**를 파싱해, 스크림을 무장시키는 변형이 하나도 없고(`pointer-events-auto`
> 게이트 집합이 공집합), 두 버튼의 무장 게이트 집합이 오버레이를 드러내는 게이트 집합과 **정확히
> 같음**을 단언한다. `css: false`가 계산 스타일을 무의미하게 만드는 tier에서 이것이 실제로 발화하는
> 유일한 형태다 — 뒤집는 세션은 이 단언을 지우지 말고 **뒤집힌 형태로 다시 쓸 것.**

**그리고 테스트 재작성이 폴백과 함께 가야 한다.** headless Chrome은 `(hover: none)`을 **참으로**
보고하므로, 클래스만 추가하면 오버레이 e2e 두 건이 **호버 게이트를 통째로 지워도** 통과한다
(`docs/HANDOFF.md` §5.7 2번). 뒤집기는 "클래스 한 줄"이 아니라 클래스 한 줄 + 그 두 검사의 재작성이다.

---

## E-30 — 크롬의 자동숨김 보류는 **포인터가 어디 있느냐**의 속성이다. 바가 구독하는 이벤트가 아니다 (오케스트레이터, 2026-08-01) — BINDING

**출처.** 이번 세션의 실측. E-27의 `바 위 호버 → 자동숨김 보류(hold)` 행이 **E-27이 같은 판정에서 새로
만든 경로에서 발화하지 않고 있었다** — 화면 가장자리 44px 스트립에서 깨운 크롬. `docs/HANDOFF.md`
§6.5의 모양 그대로다: 유닛도 e2e도 초록이었고, 초록인 이유가 정확성과 무관했다(둘 다 **크로싱이 있는**
호버만 재현하고 있었다). **수정 자체는 사용자가 승인했다** — 결함을 네 브레이크포인트에서 실측해
제시한 뒤 결정을 받았다. 아래 §2의 **터치 조항은 오케스트레이터의 판단**이고, 근거는 이 판정 자신의
논리다(E-27의 보류가 정당한 이유가 터치에는 존재하지 않는다).

### 1. 판정 — 규칙은 뷰어 루트에 한 번 진술된다

| | |
|---|---|
| 규칙 | 뷰어 루트의 `onPointerOver`/`onPointerOut` 하나. **지금 포인터 밑에 있는 노드**가 `[data-role="viewer-top-bar"]` 또는 `[data-role="viewer-bottom-bar"]` 안이면 `holdChrome()`, 아니면 `releaseChrome()` |
| 두 바 | `onMouseEnter`/`onMouseLeave`도, 스토어 구독도 **없다 — 이제 표현 전용(presentational)이다.** 두 바가 이 규칙에 기여하는 것은 자기 `data-role` 하나뿐이다 |
| 덮는 범위 | 스테이지에서 걸어 들어오기 · 스트립 호버 · 스트립 클릭 · `H` · 중앙 탭 — **크롬이 켜지는 모든 경로**가 규칙 하나로 덮인다. 경로가 늘어도 보류 배선은 늘지 않는다 |
| 코드 | `ViewerPage.tsx`의 `trackChromeHover`(그 긴 주석이 근거다), 스토어의 `holdChrome`/`releaseChrome`/모듈 스코프 `chromeHeld` |

**왜 종전 배선이 발화하지 않았나 — 리액트가 그 이벤트를 버린다.** 스트립은 **크롬이 없는 동안에만**
렌더된다. 그래서 스트립에서 깨우면 **같은 커밋에서** 스트립이 언마운트되고 바가 마운트되는데, 그동안
포인터는 **한 번도 움직이지 않는다.** 브라우저는 이것을 제대로 처리한다 — 레이아웃 변경 뒤 Chrome은
다시 히트테스트해서 약 10 ms 뒤 바에 `pointerover`/`mouseover`를 보낸다(**실측 22/24/24/26 ms @
1440/1024/768/400**). 버리는 쪽은 리액트다: `onMouseEnter`/`onPointerEnter`는 `mouseover`/`pointerover`
에서 **합성**되는데, 이벤트의 `relatedTarget`이 리액트가 관리하는 노드면 **"짝이 되는 `…out`에서 이미
쌍이 발송됐다"고 가정하고 조기 반환한다.** 여기서 그 `…out`은 **삭제되는 중인 스트립**으로 갔다.

**그래서 무슨 일이 일어났나 (네 폭 전부 실측).**

| 폭 | 종전 동작 |
|---|---|
| 768 / 400 | 포인터가 바 안에 놓여 있는 채로 **2 600 ms 뒤 크롬이 사라졌다** — 독자가 지금 누르려던 컨트롤이 손 밑에서 없어진다 |
| 1440 / 1024 | 크롬이 사라지고, 그 정지한 포인터 밑에 **스트립이 다시 마운트돼 약 13 ms 만에 다시 깨웠다.** 바가 **2.6초마다 무한히 깜빡였다** |

후자가 왜 검사를 통과했는지도 기록해 둔다: 진동은 `data-chrome`을 **읽는 거의 모든 순간에 `visible`로
답한다.** 상태 샘플링으로는 "보류됐다"와 "숨었다가 다시 불려 왔다"를 구별할 수 없다 — 그래서 브라우저
tier의 검사는 상태가 아니라 **전이 목록이 비었음**을 단언한다(`e2e/09-viewer-chrome.spec.ts`).

### 2. 제품이 바뀌는 지점 — 터치에서 탭 한 번은 더 이상 크롬을 붙잡지 않는다

**이것은 출하돼 있던 동작의 변경이므로 버그 픽스로 슬쩍 넘기지 않고 판정으로 적는다.** 바뀌는 것은
E-27의 `바 위 호버 → 보류` 행이 **터치 기기에서 무엇을 뜻하는가**다.

| | |
|---|---|
| 종전 (출하됨) | mobile-400에서 **하단 바 안을 한 번 탭하면 크롬이 영구히 열린 채 고정됐다.** Chrome의 호환 마우스 이벤트가 보류를 잡았고, 손가락에는 `mouseleave`가 없으므로 해제가 **영영 오지 않았다** — 실측 |
| E-30 이후 | **터치는 보류를 잡지 않는다**(`pointerType === 'touch'`). 바를 탭해도 크롬은 다른 모든 곳과 똑같이 **마지막 깨우기로부터 2 600 ms 뒤** 사라진다 |
| 근거 | 터치 화면에는 **"컨트롤 위에 머무는 포인터"라는 것이 없다.** 탭이 끝나는 순간 손가락은 사라지고, E-27이 보류에 대어 놓은 이유("독자가 지금 누르려는 컨트롤을 보고 있다")도 함께 사라진다. 이유가 없으면 규칙도 없다 |
| 왜 호환 이벤트로는 못 걸렀나 | Chrome이 탭 뒤에 보내는 호환 마우스 이벤트는 **자기가 손가락에서 왔다고 말하지 않는다.** 그것을 구별하는 유일한 것이 `pointerType`이고, 그것은 포인터 이벤트에만 있다 — 보류를 포인터 이벤트로 옮긴 §1이 이 조항을 **가능하게** 만들었다 |

### 3. 보류를 잡아 둔 채 방치하는 경로가 있어서는 안 된다 — 그래서 해제가 넷이다

**해제되지 않은 보류는 그 세션 내내 자동숨김을 무장 해제한다.** `chromeHeld`는 모듈 스코프이고 아무것도
그것을 렌더하지 않으므로, **독자가 볼 수도 고칠 수도 없는 상태**다. 그리고 §1의 보류는 애초에 **크로싱
없이** 잡혔으므로, 해제가 "짝이 되는 크로싱이 도착한다"에 기대는 것 자체가 같은 함정이다. 하나의
이벤트에 얹은 해제는 그 이벤트가 오지 않는 날 조용히 제품을 망가뜨린다.

| # | 해제 경로 |
|---|---|
| 1 | `pointerout`의 **목적지가 바가 아닐 때.** `relatedTarget`이 `null`인 경우 — 포인터가 창 밖으로 나감 — 도 여기에 포함되고, 해제한다 |
| 2 | 루트의 `onPointerLeave` — 포인터가 뷰어를 통째로 떠났다 |
| 3 | 스테이지 위의 평범한 `mousemove`(`nudgePointer`에 접어 넣음) — **경계에서 한 번 발화하는 대신 계속 도착하는** 다른 이벤트 계열이다. 경계 이벤트를 놓쳐도 이것은 놓칠 수 없다 |
| 4 | 스토어의 `open()`·`close()`가 모듈 스코프 `chromeHeld`를 되돌린다 — **떠난 뷰어나 밑에서 갈린 권이 보류를 물려주지 못한다.** 모듈 스코프 상태는 그것을 세운 컴포넌트보다 오래 산다 |

**1과 2가 실제로 독립이라는 것은 뮤테이션으로 증명했다**: `pointerout` 해제를 망가뜨려도 창을 벗어나는
경우는 루트의 `pointerleave`로 **여전히** 해제됐다. 겹쳐 보이는 두 경로가 실은 서로 다른 실패를 막고
있다는 뜻이고, 둘 중 하나를 "중복"이라며 지우는 것은 이 판정을 어기는 것이다.

### 4. `holdChrome`/`releaseChrome`의 멱등성은 계약이지 정리정돈이 아니다

파생 규칙은 **한 번의 여정에서 여러 번 발화한다** — 바 안에서 버튼과 버튼 사이를 지나가는 것만으로도
`pointerover`/`pointerout`이 연달아 온다. 조건 없이 재무장하는 `releaseChrome`이었다면 **페이지 위에서
마우스가 움직일 때마다 2 600 ms 마감이 뒤로 밀린다** — 독자의 손이 마우스에 얹혀 있는 한 마감을 넘겨
살아 있는 크롬, 즉 E-27이 없애려던 바로 그것이다. 그래서 **답을 바꾸지 않는 호출은 아무 일도 하지
않는다**: `holdChrome`은 이미 잡혀 있으면 반환하고, `releaseChrome`은 잡혀 있지 않으면 반환한다.

### 5. 기각한 대안 셋

**① 스트립도 `holdChrome`을 부르게 한다.** 스트립 두 경로(호버·클릭)만 고친다. 그러고 나면 **해제는
여전히 바의 `onMouseLeave` 하나에 통째로 얹혀 있고**, 그 `onMouseLeave`는 §1이 보인 것과 같은 이유로
오지 않을 수 있다 — 잡히기는 했는데 놓이지 않는 보류, §3이 말하는 바로 그 가닥이다. 게다가 §2의 터치
결함은 손도 대지 못한다.

**② 바가 `mousemove`에서 보류한다.** 실패하는 바로 그 경우에 **발화할 수 없다.** 그 경우의 정의가
"포인터가 움직이지 않았다"이기 때문이다. 실패 조건이 곧 이벤트의 부재인 설계는 검사할 수도 없다.

**③ 바 뒤에 스트립을 계속 마운트해 둔다** (그러면 언마운트/마운트가 없으니 합성 공백도 없다). 스트립을
크롬이 켜질 때 **언마운트하는 이유**를 되살린다 — 바 위에 얹힌 스트립이 `뒤로`의 **첫 클릭을 먹는다**
(§6.1이 이미 그 이유로 이 구조를 고정하고 있다). 그리고 그러고도 **보류는 생기지 않는다**: 포인터 밑에
있는 것은 스트립이지 바가 아니고, 스트립은 보류의 대상이 아니다.

### 6. 뒤집을 경우의 전제 조건 (판정의 일부, 미래 세션용)

보류를 다시 두 바 위로 옮기는 사람은 **리액트의 합성 공백을 그대로 되살린다.** 옮기기 전에,
**스트립에서 깨운 뒤의 보류를 검사하는 테스트가 자기 변경 없이 빨개지는 것을 먼저 보일 것.**
(`ViewerPage.test.tsx`의 *"holds the chrome a screen edge just summoned …, under a pointer that never
moved"* 두 케이스와, 히트테스트가 실제로 필요한 브라우저 tier의 `e2e/09-viewer-chrome.spec.ts`.)
그 테스트들이 초록인 채로 옮겨졌다면 옮긴 것이 아니라 **결함을 되돌린 것**이고, 초록인 이유는
그 검사가 **크로싱이 있는 호버만** 재현하고 있다는 뜻이다 — 이 판정이 시작된 자리로 정확히 되돌아간다.

같은 규칙이 §2에도 적용된다: 터치 조항을 지우는 세션은 **탭 한 번 뒤 2 600 ms에 크롬이 사라지는지를
mobile-400에서 실측**해서 보일 것. 유닛의 `pointerType: 'touch'` 케이스만 지우면 아무것도 빨개지지
않는다.

### 7. 고치지 않고 남긴 잔여 하나 — 기존 결함, 실측, 판정하지 않음

**포인터가 44px 가장자리 스트립 안에 머문 채 `H`를 눌러 크롬을 내리면 크롬이 내려가지 않는다.** 크롬이
사라지는 순간 스트립이 **그 정지한 포인터 밑에 다시 마운트돼** 곧바로 다시 깨우기 때문이다. E-30
이전에는 이것이 §1의 2.6초 주기 무한 깜빡임이었고, **지금은 첫 사이클에서 보이는 채로 보류돼 정착한다**
— 나빠진 것이 아니라 덜 나빠졌다.

옳다고 볼 여지가 있다(포인터는 **실제로** 바 안에 있고, 그러면 보류가 규칙대로 걸린 것이다). 하지만
결과적으로 **그 한 위치에서는 `H`가 듣지 않는 것처럼 보인다.** 이번 판정의 범위가 아니고 사용자에게
묻지 않았으므로 **판정하지 않는다.** 다음 세션이 이것을 새 결함으로 다시 발견하지 않도록,
**측정된 기지(旣知) 사항**으로 여기에 남긴다.

## E-31 — 가장자리 스트립은 **진입**으로만 깨운다. 자기 밑에 이미 있던 포인터로는 깨우지 않는다 (오케스트레이터, 2026-08-04) — BINDING

**출처.** E-30 §7이 판정하지 않고 남긴 잔여. 사용자에게 물었고 답은 "스트립은 '진입'으로만 깨운다"다.

포인터가 44px 가장자리 스트립 안에 머문 채 `H`를 누르면 크롬이 내려가지 않는다. 크롬이 사라지는 순간
스트립이 **그 정지한 포인터 밑에 다시 마운트돼** 곧바로 다시 깨우기 때문이다.

**판정.** 깨우기는 **사건**이지 상태가 아니다. 스트립이 마운트되는 시점에 포인터가 이미 그 영역 안에
있었다면 그것은 진입이 아니므로 **깨우지 않는다.** 포인터가 실제로 움직여 들어올 때만 깨운다.

E-30이 "보류는 포인터가 **어디 있느냐**의 속성"이라고 정한 것의 짝이다: 보류는 위치의 함수이고,
깨우기는 **이동**의 함수다. 둘을 같은 신호로 구현하면 이 결함이 다시 생긴다.

**적용 범위.** `H`만이 아니다. 크롬이 사라지는 모든 경로(자동숨김 포함)에서 같은 규칙이 선다.
자동숨김 경로는 E-30의 보류 때문에 지금은 이 상황에 도달하지 않지만, 규칙을 `H`에만 걸면
보류 규칙이 바뀌는 순간 조용히 되살아난다.

**뒤집을 경우의 전제 (판정의 일부).** 되돌리는 사람은 `H`를 누른 뒤 크롬이 숨은 채로 **남아 있는지**를
검사하는 테스트가 자기 변경 없이 빨개지는 것을 먼저 보일 것. 그 검사는 **"아무 일도 일어나지 않았다"**
가 주장이므로 `toHaveAttribute`로 쓰면 안 된다 — 그것은 재시도하므로 깨어났다가 2 600 ms 뒤 자동으로
숨은 크롬에 대고도 통과한다(8세션차에 실제로 그랬다). `MutationObserver`로 전이 목록을 받아
**빈 목록**을 단언할 것.

## E-32 — soft-UI 스킨을 전면 채택한다. **D-40을 폐기하고**, 다크 램프는 새로 유도한다 (오케스트레이터, 2026-08-04) — BINDING

**출처.** 사용자의 Claude Design 프로토타입 `만화방 v2 soft.dc.html`(프로젝트 `ad00fd5c`). 지시는
"새로운 클로드디자인 결과물을 철저히 적용해주세요. UI모양뿐 아니라 UI동작도 기존과 더 개선된 부분이
있으면 빼먹지말고 적용해주세요"다. 선택지 넷을 실측과 함께 제시했고 **전면 채택**으로 판정됐다.

### 1. 무엇이 바뀌는가

| | 이전 | 이후 |
|---|---|---|
| 바탕 / 표면 / 잉크 | `#f3f2f2` / `#eae9e9` / `#201e1d` | `#EAE3D4` / `#F3EEE3` / `#263B38` |
| 액센트 | `#ec3013` (적) | `#17595B` (딥 티일) — 9스텝 램프 전부 교체 |
| 2차 액센트 | 별개 coral 램프 | **액센트와 동일**. 2차 액센트는 사실상 소멸한다 |
| 중립 램프 | 중성 회색 | 온난 오커 |
| radius | `0px` (D-40) | `sm 3 / md 4 / lg 6 / pill 7 / full 999` |
| 그림자 | 단일 잉크 드롭 | **듀얼 라이트**(좌상단 하이라이트 + 우하단 그림자) + 신규 `--shadow-inset` |
| — | — | **신규 `--color-hot: #EC3013`** |

**`--color-hot`은 옛 브랜드 적색이고, 이제 브랜드가 아니라 "현재 / 선택됨 / 포커스" 마커 전용이다.**
사이드바 선택 행의 inset 링, 포커스된 라이브러리 카드, 현재 썸네일, 슬라이더 드래그 프리뷰, 오버라이드
칩, `:focus-visible` 아웃라인, 체크박스/라디오 `accent-color`, `.seg-opt:has(:checked)`의 inset 링.
**그 외 어디에도 쓰지 않는다** — 브랜드 색으로 되돌아가는 순간 이 판정의 요점이 사라진다.

### 2. D-40은 폐기된다

D-40("Zero corner radius everywhere")과 그 집행 장치가 함께 개정된다:
`web/tailwind.config.ts`의 `borderRadius` **override**, `web/src/lib/hygiene.test.ts`의
`border-radius` 화이트리스트(`{0, 0px, 50%, 9999px}`)와 `rounded-*` 유틸리티 전면 금지.
집행이 규율을 이긴다는 D-40의 근거 자체는 유효하므로, **금지를 푸는 것이 아니라 허용 집합을 토큰에
묶는다**: 새 화이트리스트는 `--radius-*` 토큰이 산출하는 값과 `50%` / `9999px`뿐이고, 임의의 px은
계속 금지한다.

### 3. D-41과 NFR-CMP-003은 **유지된다** — 다크 램프를 새로 유도한다

`soft-ui.css`는 라이트 팔레트만 정의한다. 그러나 NFR-CMP-003(prd), D-41, ui-spec §1.4는 구속력이 있고
이 판정은 그것을 건드리지 않는다. 따라서 **다크 램프는 티일/크림 기준으로 새로 유도한다.** 세 가지가
필수다:

1. `[data-theme='dark']` 블록을 티일 기준으로 다시 만든다. 뷰어는 두 앱 테마 모두에서 다크다.
2. **시맨틱 토큰 24종을 다시 유도한다.** 프로토타입은 이 층이 없어 원시 램프 스텝을 인라인으로 쓴다.
   램프는 "테마 불변 절대 명도"이므로 그 방식을 그대로 옮기면 다크에서 대비가 무너진다.
3. **듀얼 라이트 그림자의 하이라이트 로브를 다크용으로 재유도한다.** `rgba(255,253,246,.9)`는 크림
   지면에서만 하이라이트이고 다크 지면에서는 **흰 테두리**가 된다.

### 4. 채택하지 **않는** 것 — 판정의 일부

프로토타입에 있으나 반영하지 않는다. 다음 세션이 "빠뜨렸다"고 보지 않도록 이유와 함께 남긴다.

| 항목 | 반영하지 않는 이유 |
|---|---|
| `.btn { justify-content: center }` | ui-spec §0.3 flush-left. 프로토타입은 스킨 시트가 `ds-styles.css` **뒤에** 로드돼 `.btn-block`의 `flex-start`를 이긴다(실측 computed `center`) |
| 사이드바 행 `min-height: 44px → 42px` | NFR-CMP-002 터치 타깃 |
| 섹션 라벨·메타에 `--color-neutral-600` | 크림 위 대비 **3.31**. AA 실패. 시맨틱 `--ink-dim`을 새 지면에서 AA를 넘도록 유도해 쓸 것 |
| 완독 진행바 `--color-accent-300` | 트로프 위 대비 **1.38**. 사실상 비가시 |
| 오버라이드 칩의 `#F6F2E9` on `--color-hot` | **3.76**. AA 실패. 11px에 쓰이므로 전경을 조정할 것 |
| `<h6>`·`<h4>` → 인라인 `<div>` | 문서 개요가 사라져 스크린리더 헤딩 탐색이 죽고, `e2e/06-settings.spec.ts`의 `getByRole('heading')`이 깨진다. **시각 스타일만**(16px / -.01em) 가져오고 태그는 유지한다 |

### 5. 이 판정이 무효화하지 않는 것

리뷰 기준 스크린샷 `docs/ui-shots/` 36장은 이 변경으로 **전부 무효**가 된다. Playwright에 픽셀
베이스라인은 없으므로(`toHaveScreenshot` 0건) 자동으로 빨개지지는 않는다 — 그래서 **조용히 낡는다.**
갱신하거나, 낡았다고 명시할 것.

## E-33 — 읽기 설정 오버라이드는 **권 단위**를 유지한다. 시리즈 상세의 씨앗은 실제로 씨앗이 된다 (오케스트레이터, 2026-08-04) — BINDING

**출처.** 프로토타입 v2는 방향·모드·맞춤 오버라이드를 **시리즈 단위**로 다룬다. 제품은 **권 단위**다
(C-9, prd FR-VWR-002). 사용자 판정은 **권 단위 유지 + 씨앗 결함 수정**이다.

### 1. C-9과 prd FR-VWR-002는 유지된다

저장은 `PUT /api/books/{bid}/prefs` 그대로다. 계약도 스키마도 바뀌지 않는다. 프로토타입의 시리즈 단위
모델은 **반영하지 않는다.**

### 2. 출하된 결함 — 시리즈 상세의 `읽기 방향` 컨트롤은 아무것도 바꾸지 않는다

C-9은 그 `.seg`가 *"seeds the direction for books opened from that screen"* 이라고 못박는데,
**씨앗이 전달되지 않는다.** `store/seriesDir.ts`의 소비자는 `SeriesDetailPage` 하나뿐이고 그것도
자기 `.seg`의 표시 상태만 쓴다. `openBook`은 `?page=`만 실어 보내고, 뷰어는 언제나
`detail.prefs.reading_direction`으로 연다. R→L로 켜고 권을 열면 전역 기본값으로 열린다 —
localStorage에는 남고 세그먼트도 켜진 채라 **동작한 것처럼 보인다.**

§6.5 그 모양이다: *판정을 구현한 스토어가 있다는 것* 대 *화면이 그 값을 목적지까지 실어 보내는지*.
8세션차의 `step(delta)`와 같은 병이다.

**판정: 씨앗을 실제로 연결한다.** 그 화면에서 여는 책에 **권 오버라이드가 없을 때만** 씨앗이 초기값이
된다. 권 오버라이드가 있으면 그것이 이긴다 — 씨앗은 기본값을 대신하는 것이지 오버라이드를 이기는
것이 아니다.

**뒤집을 경우의 전제.** 씨앗을 다시 떼어내는 사람은, 시리즈 상세에서 방향을 바꾸고 권을 연 뒤 뷰어가
그 방향으로 열리는지 검사하는 테스트가 자기 변경 없이 빨개지는 것을 먼저 보일 것. 스토어를 단언하는
테스트로는 보이지 않는다 — 스토어는 결함이 있을 때도 올바른 값을 담고 있었다.

### 3. 오버라이드 배지와 리셋 — 문구는 "이 **권** 전용 설정"

서버는 `BookPrefs.is_override`를 이미 내려준다. **UI가 한 번도 읽지 않는다.**

- 뷰어 상단 바에 `--color-hot` 칩. 문구는 프로토타입의 "이 시리즈 전용 설정"이 아니라 **"이 권 전용 설정"**.
- 누르면 `PUT /api/books/{bid}/prefs`에 세 필드를 **`null`로** 보내 오버라이드를 지운다.
- **캐시만 갱신하면 반쪽이다.** `ViewerPage`의 `open()`은 `openedRef`로 책당 1회만 도는데 그것이 옳다
  (재실행은 독자를 이어보던 자리에서 튕겨낸다). 따라서 리셋은 `setMode`/`setDirection`/`setFit`을
  **명시적으로** 호출해 뷰어 스토어를 되돌려야 한다.

## E-34 — 뷰어의 `라이브러리` 버튼은 필터와 검색어를 지우지 않는다. 포커스 복귀는 가상화를 통과해야 한다 (오케스트레이터, 2026-08-04) — BINDING

**출처.** 프로토타입 v2의 `goLibrary`. 사용자 판정은 **버튼은 추가, 필터는 유지**다.

### 1. 지우지 않는다

프로토타입의 `goLibrary`는 `scope: 'all'`과 `q: ''`를 함께 쓴다. 제품에서 `library_scope`는 A-5
write-back 대상이라 서버와 localStorage에 **영구 저장**된다. 프로토타입에서는 휘발성 `setState`인
것이 제품에서는 "뷰어를 나갔더니 사이드바 필터가 영구히 풀렸다"가 된다. **`scope`도 `q`도 건드리지
않는다.**

### 2. 포커스 복귀는 `getElementById`로 하지 않는다

프로토타입은 `document.getElementById('card-' + id)`로 카드를 찾는다. **제품에서는 대부분 실패한다** —
그리드와 리스트가 **둘 다 가상화**돼 있어 창 밖 카드는 DOM에 없고, 무한 스크롤이라 아직 fetch되지
않았을 수도 있다. 올바른 구현은 `items`에서 인덱스를 찾아 virtualizer의 `scrollToIndex`로 스크롤한 뒤
**다음 프레임에** 포커스하는 것이다.

프로토타입의 **96px 오프셋도 그대로 쓰지 않는다.** 그것은 프로토타입의 스크롤 컨테이너 기준값이다.
제품의 스크롤 컨테이너는 라이브러리 화면 전체가 아니라 그리드/리스트 밴드이고, 이어보기 행과 섹션
헤더는 그 밖에 있다. 컨테이너 상단이 이미 헤더 아래이므로 96px을 그대로 쓰면 카드가 지나치게 내려간다.
`align: 'start'`로 두고, 필요하면 실측해서 정할 것.

## E-35 — 종이 질감은 다시 유도한다. 프로토타입의 구현은 이 제품에서 출하할 수 없다 (오케스트레이터, 2026-08-04) — BINDING

**출처.** Claude Design 프로젝트 `ad00fd5c`의 `만화방 v2 soft.dc.html`이 다시 갱신됐다. 세션 9
스냅샷과 바이트 단위로 대조한 결과 **바뀐 것은 셋뿐**이다 — 신규 `paper-texture.js`(전면 그레인
오버레이), 썸네일 스트립 자동 센터링, 썸네일 상한 60 제거. `soft-ui.css`·`ds-styles.css`·`support.js`·
`image-slot.js`·`_ds_bundle.js`는 **전부 동일**하다(`ds-styles.css`는 끝줄 개행 1바이트 차이뿐).

### 0. 셋 중 둘은 프로토타입이 제품을 따라온 것이다

**상한 60 제거와 "모든 페이지 변경에서 리센터"는 제품에 이미 있었다.** `ThumbnailStrip.tsx`는 처음부터
전체 페이지를 가상화하고(주석 `:10-14`가 *"프로토타입은 60에서 자른다"*고 명시), 리센터 이펙트가
`current`가 바뀔 때마다 `align:'center'`로 스크롤한다. **반영할 것이 없다.** 남은 진짜 델타는
**애니메이션의 구분** 하나다.

> **그 이펙트의 행 번호는 일부러 적지 않는다** — 이 판정을 낸 것과 **같은 커밋**이
> `ThumbnailStrip.tsx`를 +215줄 고쳐, 처음 쓴 `:94-97` 인용을 스스로 죽였다(현재는 `:249-276`).
> §6.5가 경고하는 그 모양이고, 판정 자신이 거기 걸렸다.

### 1. 텍스처 — 옮기는 것이 아니라 다시 유도한다

프로토타입의 `paper-texture.js`를 그대로 옮기는 것은 **불가능하다.** 실측(실브라우저):

| | 프로토타입 |
|---|---|
| 외부 의존 | 런타임에 `https://esm.sh/@paper-design/shaders@0.0.78` **2요청**. 실패는 조용히 삼켜져 콘솔 흔적 0 |
| 결정성 | **리로드만 해도 픽셀의 89.1 %가 달라진다.** `seed` 속성을 **무시**하고 `Math.random()`이 직접 들어간다 |
| 비용 | 폴백 타일 생성이 메인 스레드 **동기 21.7 ms**, 결과가 **약 198 KB짜리 인라인 `style` 속성** |
| 톤 전환 | 뷰어용 `#0d0c0c`가 **적용되지 않는다** — `_push()`가 조기 반환해 폴백 타일은 마운트 시점 톤으로 굽고 다시 만들지 않는다 |

이 제품은 **CDN 의존 0의 단일 정적 바이너리**이고(`hygiene.test.ts:136-141`이 폰트 CDN까지 금지),
`docs/ui-shots/`는 **사람이 눈으로 대조하는** 리뷰 기준이다. 비결정적 그레인은 그 기준을 못 쓰게 만든다.

**그러므로 제품의 구현은:** `body::after` 한 겹 · 인라인 SVG `feTurbulence`(고정 `seed`,
`stitchTiles`) · `feColorMatrix`가 **알파만** 써서 이미지는 마스크이고 색은 토큰 · **JS·캔버스·네트워크
0**. 실측 **리로드 간 변경 픽셀 0**(같은 탭 하드 리로드, 콜드 로드 교차 전부).

### 2. `mix-blend-mode: multiply`는 채택하지 않는다

프로토타입은 multiply로 합성한다. 실측 결과 **비용이 전부 블렌드 모드**이고(마스크는 공짜),
**얻는 것이 없다.**

| 구성 | fps | median 프레임 | vsync 놓침 |
|---|---|---|---|
| 레이어 off | 60.0 | 16.7 ms | 0.0 % |
| 레이어 on, 일반 합성 | 59.9 | 16.7 ms | 0.2 % |
| 레이어 on, **`multiply`** | **28.4** | 16.7 ms | **38.4 %** |
| 레이어 on, multiply, 마스크 없음 | 28.3 | 16.7 ms | 37.2 % |

**median이 어느 구성에서도 16.7 ms다** — multiply는 프레임을 느리게 만드는 것이 아니라 **38 %를
도착시키지 않는다**(매 프레임 배경 리드백 강제). 그리고 두 합성의 차이는
`opacity × maskAlpha × tone × (1 − backdrop/255)`이라 이 진폭·근검정 톤에서 **어떤 배경에서도 최대
2/255**다(0~255 전 회색 램프 + 크림 카드·neutral-100·액센트 필·hot·뷰어 지면·순검정·순백에서 실측).
**공짜가 아닌 것을 위해 아무것도 사지 않는 거래이므로 하지 않는다.**

### 3. 만화 그림 위에는 덮지 않는다 (사용자 판정)

프로토타입은 전 화면에 깐다. 그러나 프로토타입의 뷰어는 **어두운 자리표시자**라 이 효과가 드러난 적이
없다. 실제 만화 아트 위에서는 **픽셀의 100 %가 변형되고 평균 −3.0/255, 최대 13/255, 밝은 영역(원본
160~191)에서는 평균 −5.0**이다. **사용자에게 물었고 판정은 "읽기 화면에서는 뺀다"** 다.

경계는 이렇다 — **질감은 UI에 남는다**: 라이브러리 · 시리즈 상세 · 설정 · 커맨드 팔레트 · 온보딩,
그리고 **뷰어 크롬(상·하단 바, 썸네일 스트립, 권 끝 카드)까지.** 걷어내는 것은 **만화 그림이 그려지는
스테이지뿐**이다. 합격 기준은 픽셀이다 — 같은 프레임에서 **아트 변경 픽셀 0**, 크롬 바 96 %.

구현은 CSS만으로 한다(`features/**` 무수정): `body:has([data-role='viewer'])::after`로 전역 레이어를
끄고, 크롬의 불투명 표면마다 `::after`로 재도포한다 — 출하된 것은 **여섯 셀렉터**다(`viewer-top-bar` ·
`viewer-bottom-bar` · `viewer-chrome-hint` · `stale-progress > span` · `page-error` ·
`next-volume-card`). 썸네일 스트립은 하단 바의 자식이라 따로 필요 없다 — **스트립 자체에 걸면 가로 스크롤과 함께 흘러간다.** 권 끝 카드에는 `position: relative`가
함께 가야 한다. 없으면 카드의 `::after`가 전면 스크림에 걸려 **스테이지를 통째로 다시 칠한다.**

### 4. 대비 — 그레인은 AA 바닥을 뚫었다. 진폭이 아니라 토큰을 옮긴다

**출하 직전에 잡힌 구속 판정 위반이다.** E-32는 AA 4.5:1을 못박는데, 전면 오버레이는 전경과 배경을
함께 어둡게 하므로 **바닥에 붙어 있던 쌍이 아래로 내려간다.** 실측(합성식 모델과 렌더 픽셀 두 갈래가
±0.02 내 일치):

| 쌍 | 그레인 OFF | 그레인 ON | 재유도 후 |
|---|---|---|---|
| `--on-hot` on `--color-hot` (오버라이드 칩, 11 px) | 4.555 | **4.335** | **4.737** |
| `--ink-faint` on `--color-bg` (light) | 4.584 | **4.483** | **4.747** |
| `--ink-meta` on `--color-surface` (dark) | 4.603 | **4.468** | **4.830** |

**진폭을 낮추는 것은 답이 아니다** — `--on-hot`을 살리려면 `--paper-intensity ≤ 0.12`가 필요한데
그러면 텍스처가 사실상 사라진다. **씻긴 지면 기준으로 토큰을 다시 유도한다**(E-32 §3이 다크 램프에
했던 것과 같은 수순): `--on-hot` `#1B0B07` → **`#000000`**(hot 마커 위 이론 상한이 5.00이라 순검정
말고는 평균과 마스크 피크를 둘 다 넘기지 못한다), `--ink-faint` `#6C6453` → **`#68604F`**,
`--ink-meta`(dark) 알파 `0.7` → **`0.75`**.

> **이 위반이 통과할 수 있었던 이유가 더 중요하다.** 대비 테스트는 **토큰 값**을 보고 화면을 보지
> 않았으므로, 그레인을 두 배로 키워도 초록이었다. 그래서 **테스트가 그레인을 모델링하도록 만든다** —
> `washed(c) = c(1−a) + tone·a`, `a = --paper-intensity × (matrix[15] × 0.5)`이고 `matrix[15]`는
> 토큰에서 **파싱**한다. 바닥 판정은 평균 알파, 실패 메시지는 피크 알파를 함께 낸다. 톤은 셋(앱
> 라이트·앱 다크·뷰어) 중 **최악**을 쓴다. 이제 진폭을 건드리면 바닥이 따라 움직인다.

### 5. `prefers-contrast: more`에서는 끈다

텍스처는 최대 0.28의 대비비를 내주는 장식이다. OS에 더 높은 대비를 요청한 독자에게까지 물릴 이유가
없다. 네 레이어 전부 `display: none`.

### 6. 스트립 애니메이션 — 프로토타입의 **의도**를 따르고 문자 그대로의 동작은 따르지 않는다

프로토타입은 열 때 `centerThumb('auto')`를 넘기지만, 컨테이너에 `scroll-behavior: smooth`가 걸려 있어
CSSOM 규칙상 `'auto'`가 CSS에 위임된다. **즉 프로토타입은 열 때도 애니메이션한다 — 실측 1,541 ms /
29프레임.** 그 자리에만 `'auto'`를 명시적으로 넘긴 것이 저자의 의도를 드러내고, CSS가 그것을 무효화한
것이 결과다.

**제품이 채택하는 규칙은 하나다: 목표 오프셋과 현재 오프셋의 거리가 스트립 가시 폭 이하일 때만
smooth, 그 외에는 instant.** 이유는 미학이 아니라 **AC-008**이다 — 1,540쪽 볼륨에서 12쪽 → 1,200쪽
리센터 중 마운트되는 서로 다른 페이지 수가 `auto` **29** 대 `smooth` **998**이고, 페이지마다 별개
쿼리 키에 서버 썸네일은 lazy 생성이다. 이 파일 자신의 헤더 주석이 그것을 *"precisely the stall AC-008
forbids"* 라고 부른다. **프로토타입은 썸네일을 60개로 자르고 가상화가 없어 이 비용을 낼 일이 없었다 —
프로토타입에서 무해했던 CSS 버그가 제품에서는 무해하지 않다.**

**"항상 중앙"은 단언하지 않는다.** 실측상 202쪽 볼륨에서 **26쪽이 최대 675 px 어긋난다** — 클램프의
정상 동작이다. 그것을 "항상 중앙"으로 고정하는 테스트는 틀린 것을 지킨다.

### 7. 프로토타입에 있으나 채택하지 않는 것 (E-32 §4에 이어서)

| 항목 | 사유 |
|---|---|
| `<x-import>`의 `paperTexture` / `paperVariant` / `paperIntensity` 프롭 | 디자인 캔버스의 **설계 시점 노브**이지 제품 설정이 아니다. E-27 말미가 `--grid-min`에 내린 것과 같은 판정 |
| `mix-blend-mode: multiply` | §2 |
| 만화 아트 위 그레인 | §3, 사용자 판정 |
| `seed` 무시 · `Math.random()` | §1. 결정성이 `docs/ui-shots/`의 전제다 |
| `probe.html` | 프로토타입이 참조하지 않는 스크래치 파일이다 |

## E-36 — soft-UI의 나머지 절반, **컨트롤은 융기한 표면이다**를 마저 적용한다. 낡은 명세가 판정을 이기고 있었다 (오케스트레이터, 2026-08-05) — BINDING

**출처.** E-32는 soft-UI 스킨을 **전면 채택**으로 판정했다. 10세션차 감사가 프로토타입의 `soft-ui.css`와
제품의 `web/src/styles/base.css`를 대조한 결과, **약 30개의 컴포넌트 규칙이 반영되지 않은 채**
판정에도 소스 주석에도 **기록이 없다**. 전체 표는 인수인계에 있다. 이 판정은 그 발견과 원인,
그리고 다음 세션이 할 일을 못박는다.

### 1. 발견 — 빠진 30개는 무작위가 아니라 **한 가지**다

빠진 것들은 공통점이 하나다. **soft-UI를 soft-UI이게 하는 동작 — "컨트롤은 테두리 친 상자가 아니라
융기한 표면이다" — 이 아예 도착하지 않았다.** E-32가 바꾼 것 중 토큰·램프·radius는 전부 적용됐고,
그 토큰들을 **쓰는 쪽**만 옛 모양으로 남았다.

실측(10세션차 코드 기준):

| | 실측 |
|---|---|
| `base.css`의 `var(--shadow-inset)` | **0회.** 토큰은 `tokens.css`에 라이트·다크 양쪽으로 정의돼 있다(`:217`, `:359`). Tailwind `shadow-inset` 유틸리티로만 7곳 — 커버 우물·스켈레톤·진행바 트로프·온보딩 |
| `base.css`의 `var(--shadow-sm)` | **5회.** 슬라이더 thumb 2회(`:189`·`:202`, webkit/moz), 선택 마커 2회(`.seg-opt[data-checked='true']` `:654`, `.sidebar-nav-row[data-active='true']` `:904`), 그리고 통과 유틸리티 `.elev-sm`(`:700`). Tailwind `shadow-sm` 유틸리티가 컴포넌트 3곳에 더 있다 |
| `.btn-secondary` (`:478`) | `border-color: var(--control-border)` 하나. 그림자도 채움도 없다 |
| `.tag-outline` (`:536`) | `border-color` + `color`. 그림자도 채움도 없다 |
| `.seg` (`:625`) | `border: 1px solid var(--control-border)`. 그림자도 채움도 없다 |
| `.tag-neutral` (`:532`) · `.input` (`:575`) | **채움은 있다.** 없는 것은 그림자다 — `.tag-neutral`은 `--shadow-sm`, `.input`은 `--shadow-inset` |
| `.card` (`:666`) | 그림자 없음. `--shadow-md`가 있어야 한다 |

즉 `--shadow-inset`은 **정의만 되고 스타일시트에서 한 번도 쓰이지 않는다.** 그것이 이 판정이 존재하는
이유의 요약이다.

### 2. 원인 — 판정을 이긴 것은 `ui-spec`의 두 문장이다

구현자는 E-32를 읽고 토큰과 radius를 고쳤다. 그리고 컴포넌트를 만들려고 `ui-spec.md`를 폈고,
거기서 **E-32가 폐기한 계약이 여전히 현행으로 적혀 있는 것**을 보고 그대로 만들었다.

- **`ui-spec.md` §0.2** — *"Only three things carry a shadow: `.dialog`, the viewer's next-volume card,
  and the `.elev-*` utilities."* E-32는 이 문장을 **개정하지 않았다.**
- **`ui-spec.md` §2.3** — `.btn-secondary` · `.tag-outline` · `.input` · `.seg`(그리고 `.tag` ·
  `.seg-opt` · `.card`)의 계약이 **Modernist의 테두리 형태 그대로**였다.

증거는 코드에 있다. `web/src/components/ds/Card.tsx:6-8`이 그림자를 **거부**하면서 근거로
*"ui-spec §0.2"* 를 든다 — 폐기된 문장을, 충실하게 인용해서.

**§6.5가 문서 층위에서 그대로 재현된 것이다:** 낡은 계약이 자기를 폐기한 판정보다 오래 살았고,
그 계약을 읽은 사람은 판정을 어긴 줄도 모른 채 어겼다. 이 저장소의 §6.5 표에 있는 *"이전 세션의
인수인계 문장 / 인용된 코드의 지금 상태"* 와 *"상수가 스타일시트와 일치한다는 것 / 지금도 일치하는지"*
와 같은 병이고, 이번에는 그 낡은 쪽이 **명세 자신**이었다.

**두 곳 모두 이 판정과 함께 개정했다.** §0.2는 다섯 단계의 승강 언어로 교체하고 **옛 문장이 사고로
살아남았다고 명시**했다. §2.3은 여덟 행을 `soft-ui.css`에서 인용해 다시 쓰고 ⟳로 표시했으며, 절 제목의
*"verbatim from the DS"* 를 뗐다. 두 절 모두 **명세가 목표를 기술하고 구현이 아직 따라오지 못했다**고
못박고 이 판정을 가리킨다.

**같은 목록의 §0.1도 낡아 있었다.** *"Zero corner radius. Everywhere."* 는 E-32 §2가 D-40을 폐기하면서
함께 죽은 문장인데 그대로 서 있었다. 취소선 + `Superseded` 주석으로 함께 개정했다 — **지우지 않고
남긴다.** 그 두 문장이 같은 판정을 함께 살아남았다는 것이 §6.5의 교훈이기 때문이다. 이 개정으로
`ui-spec` §0의 다섯 항목 중 **셋**이 E-32의 사후 정정을 달게 됐다 — §0.1·§0.2, 그리고 10세션차 감사가
뒤늦게 찾은 **§0.4**다(*"Viewer background is literally `var(--color-text)` (#201e1d) … `var(--color-bg)`
(#f3f2f2)"* — E-32가 두 값을 `#263B38`/`#EAE3D4`로 바꿨다). **판정을 쓰면서 그 판정이 진단한 병을 같은
목록에서 하나 놓쳤다** — 감사가 아니었으면 §0.4는 그대로 남았을 것이다.

**아직 고치지 않은 것 하나를 명시한다: `ui-spec` §1의 토큰 표 전체가 여전히 pre-E-32다.** §1.1은
`#f3f2f2` / `#ec3013` / `--radius-*: 0px`를, §1.2는 `--shadow-sm: 0 1px 2px rgb(45 43 43 / .14)`를,
§1.4는 옛 다크 램프를 싣고 있고 **E-32를 가리키는 표시가 한 곳도 없다.** `--shadow-inset`도 `--color-hot`도
`--shadow-sidebar`도 그 표에 없다. 이 판정의 범위 밖이라 손대지 않았으나, **원인 §2와 정확히 같은
모양이므로 다음 세션이 §5의 1단계와 함께 처리한다.** 그때까지 토큰의 권위는 `web/src/styles/tokens.css`다.

### 3. 판정 — 나머지 절반도 채택된다. "적용됐다"의 정의

**E-32의 전면 채택은 유효하고, 컨트롤 융기 절반도 그 안에 있었다.** 다음 세션이 적용한다.
"적용됐다"는 다음 일곱 가지가 전부 참인 것을 말한다. 전부 grep으로 확인 가능한 형태로 쓴다.

1. `web/src/styles/base.css`에서 `var(--shadow-inset)`가 **0회가 아니다.** 최소한 `.input`과 `.seg`
   트랙이 그것을 쓴다.
2. `.btn-secondary` · `.tag-neutral` · `.tag-outline`이 `--shadow-sm`을, `.card`가 `--shadow-md`를 갖는다.
3. **컨트롤 클래스 중 `border: 1px solid var(--control-border)`만으로 정의되는 것이 0개다.** 테두리가
   남는 곳은 *마커*뿐이다 — `--color-hot` inset 링, 포커스 아웃라인, `.radio .dot`.
4. 눌리는 컨트롤의 `:active`가 `--shadow-inset` + `transform: translateY(1px)`이다.
5. `.seg-opt + .seg-opt` 규칙이 없다(§5 참조).
6. **E-32 §2와 hygiene은 그대로 선다:** 스타일시트 층의 하드코드 hex 0, `--radius-*`가 산출하지 않는
   임의 px 0. 프로토타입의 `5px`(`.tag`)·`8px`(`.seg`)은 **토큰 값이 아니다.** 그대로 옮기지 않는다.
7. 새로 유도한 시맨틱 토큰이 **라이트·다크 양쪽에 존재하고**, 램프 스텝을 칠하는 클래스마다
   `[data-theme='dark']` 짝이 있다.

### 4. 뷰어 크롬 — **지면은 어둡게, 컨트롤은 크림으로.** 그 채움 토큰은 절대값이다

여기가 가장 조심할 곳이다. 프로토타입에는 **다크 팔레트가 없고**, 제품의 뷰어는 두 앱 테마 모두에서
`data-theme="dark"`다(NFR-CMP-003, ui-spec §1.4). 따라서 `soft-ui.css`의
`.btn-secondary { background: var(--color-surface) }`를 문자 그대로 옮기면 라이브러리에서는 크림 알약,
**뷰어에서는 딥 티일 알약**이 된다 — 어두운 바 위의 어두운 버튼이다.

그러나 9세션차가 프로토타입의 뷰어를 **직접 실측해** 두 곳에 남겼다.

- `docs/ui-shots/README.md`(§"The viewer's bars are not cream") — 프로토타입의 `뒤로` 버튼 내부는
  1440에서 `(235, 230, 220)`이고 바는 `(37, 57, 55)`인 반면, **제품에서는 같은 픽셀이 `(37, 58, 55)`,
  즉 바 자신이다.** 채움은 `rgba(0,0,0,0)`. 델타는 *"컨트롤: 테두리 고스트 → 크림 채움"* 이라고
  그 파일 자신이 적고 있다(같은 문서의 divergence 목록 1번: *"이 세트에서 가장 큰 차이이고 네 폭 모두에서
  나타난다"*).
- 9세션차의 실측 노트 `design-v2/MEASURED.md` — `뒤로` 버튼 `bg #F3EEE3`(크림),
  `color rgb(13,52,54)` = accent-800, `border-radius 7px`. 그리고 그 문서의 결론이
  *"델타는 '뷰어가 밝아진다'가 아니다. 지면은 그대로 어둡고, 바뀌는 것은 컨트롤이 테두리 고스트에서
  크림 채움으로 바뀐다는 점"* 이다.

**판정: 뷰어의 지면은 어둡게 유지하고, 뷰어의 컨트롤은 크림으로 채운다.** 이것은 NFR-CMP-003 위반이
아니다 — 그 요구사항은 **지면**에 대한 것이고, 지면은 `#263B38` 그대로다. ui-spec §1.4의 "뷰어는 두 번째
팔레트가 아니라 같은 토큰의 재스코프"도 유지된다.

**그러므로 그 채움은 테마에 따라 뒤집히는 시맨틱 토큰이면 안 되고, `tokens.css`가 이미 갖고 있는
`--on-accent` / `--on-hot` / `--scrim-volume-end` / `--scrim-broken` 같은 절대값(`tokens.css:150-176`,
주석 *"absolutes — not theme-relative"*)이어야 한다.** 그 절대값들이 존재하는 이유가 정확히 이것이다:
"바탕이 뒤집히지 않는 표면 위의 전경". 컨트롤 알약도 같은 부류다.

유도할 때의 실측 근거(직접 계산):

| 쌍 | 대비 |
|---|---|
| accent-800 `#0D3436` on `#F3EEE3` (프로토타입의 `뒤로`) | **11.62** |
| `#F3EEE3` 알약 vs 뷰어 지면 `#263B38` (분리도) | **10.28** |
| accent-800 on `#F8F4EC` (호버) | **12.26** |
| accent `#17595B` on `#F8F4EC` (호버 전경) | **7.32** |

**`#F8F4EC`에는 대응하는 토큰이 없다.** `tokens.css`·`base.css` 전체에서 0회다. `#FBF8F1`(슬라이더
thumb)도 마찬가지로 0회다 — 제품은 그 자리에 `--accent-fill`을 쓰고, 다크에서 accent가 트로프 위
1.09:1이라는 이유가 `base.css`에 적혀 있으므로 **되돌리지 않는다.** `#F6F2E9`만 이미
`--on-accent`로 존재한다. 새로 유도할 때 **위 숫자들은 그레인 이전 값**이라는 점에 주의할 것(§5).

### 5. 다음 세션이 할 일 — 이 순서로

1. **시맨틱 토큰을 먼저 유도한다.** 라이트 + 다크 + §4의 절대값, 그리고 **종이 그레인을 씌운 뒤의**
   대비로 검증한다.
   *왜 먼저인가:* `base.css`를 먼저 손대면 원시 램프 스텝과 리터럴 hex를 임시로 넣게 되고, 그 순간
   hygiene의 hex 규칙 2개가 빨개진다 — 그러면 규칙을 우회할 압력이 생기는데, 그 규칙은 E-32 §3이
   기대고 있는 바로 그 집행 장치다.
   *왜 그레인 뒤인가:* **E-35 §4가 대비 바닥을 그레인을 모델링하도록 옮겼다.** 마른 지면 기준으로
   유도한 토큰은 통과한 뒤 씻긴 지면에서 실패한다. E-35 §4에서 `--on-hot`이 4.555 → **4.335**로
   내려간 것이 그 사례이고, 그때 답은 진폭이 아니라 **토큰**이었다.
2. **`base.css`에 적용한다.** §3의 일곱 항목이 판정 기준이다.
3. **이제야 죽은 것을 걷어낸다.**
   - 뷰어의 `border-neutral-700` 오버라이드 5곳: `ViewerTopBar.tsx:113`·`:177`·`:191`,
     `ViewerBottomBar.tsx:111`, `PageError.tsx:45`.
   - 프로토타입이 지운 `.seg-opt + .seg-opt` 구분선(`base.css:641-643`).
   *왜 마지막인가:* 그 오버라이드들은 **지금 실제로 일을 하고 있다** — 뷰어 컨트롤의 테두리 색을
   바꾼다. 2단계 전에 지우면 뷰어의 컨트롤이 테두리도 그림자도 없는 상태로 한 커밋 동안 존재한다.
   테두리를 없애는 것이 2단계이고, 그 뒤에야 이 오버라이드들이 dead가 된다.
   **`PageLoadingIndicator.tsx:34`의 `border-neutral-700`은 건드리지 않는다** — 그것은 컨트롤 테두리가
   아니라 스피너의 링 자체다. 같은 문자열을 grep으로 일괄 치환하면 스피너가 사라진다.

### 6. 커버리지 경고 — 게이트는 "끝났다"고도 "깨뜨렸다"고도 말하지 않는다

**약 30개 중 게이트가 보는 것은 6개뿐이다.** 직접 센 것이다:

- `web/src/styles/tokens.test.ts`의 `describe('a ramp step painted by a class flips with the theme…')`
  — `it` **4개**(`:1468`부터). `.tag-neutral`의 채움을 neutral-200으로 옮기고 다크 짝을 빠뜨리면
  이쪽이 빨개진다. 그 블록의 첫 `it`은 **캘리브레이션**이라 규칙 목록이 바뀌기만 해도 빨개진다.
- `web/src/lib/hygiene.test.ts`의 `describe('colour lives in the token layer…')` — `it` **2개**(`:96`).
  `#F8F4EC`를 그대로 넣으면 이쪽이 잡는다.

**나머지 24개에는 검사가 하나도 없다.** 모든 `box-shadow`, 모든 `transition`, 모든 `border: 0`,
`font-weight`, `:active`의 `translateY(1px)` 프레스가 그렇다. 다시 말해 — **게이트는 이 작업이 끝났다고
말해주지 않고, 되돌려도 깨졌다고 말해주지 않는다.** 이것은 각주가 아니라 **위험**이고 §6.5 그 모양이다:
검사가 출하되는 대상 옆의 것을 보고 있어서, 정확성과 무관한 이유로 초록이다. 실제로 이 30개가
**이번 세션까지 전 게이트 초록 아래에서 살아 있었다.**

따라서:

- **완료 판정을 게이트에 맡기지 말 것.** 기준은 §3의 일곱 항목이고, 그것을 사람이 grep으로 확인한다.
- 그리고 **그 일곱 항목을 검사로 만드는 것을 같은 세션 안에서 할 것.** 값싼 길이 이미 있다 —
  `tokens.test.ts`와 `hygiene.test.ts`가 쓰는 `allRules(BASE)` 스타일시트 파서가 그것이다.
  같은 기법으로 "컨트롤 클래스에 `border: 1px solid var(--control-border)`만 있으면 실패"를 쓸 수 있다.
  검사를 붙이지 않고 넘기면, 다음 세션은 이 판정을 **다시** 발견하게 된다.
- 리뷰 기준 스크린샷 `docs/ui-shots/`는 E-32 때 이미 무효화됐고(E-32 §5) 이 작업으로 **한 번 더**
  무효가 된다. 픽셀 베이스라인이 없으므로 자동으로 빨개지지 않는다 — 갱신하거나 낡았다고 명시할 것.

### 7. 판정하지 않고 남기는 것 — `.radio` `.dot`의 `--color-hot`

**이것은 진짜로 모호하므로 결정하지 않는다. 다음 세션은 판정을 받고 움직일 것.**

프로토타입은 `soft-ui.css:107`에서 이렇게 한다:

```css
.radio:has(:checked) .dot { border-color: var(--color-hot) !important; }
```

E-32 §1은 `--color-hot`이 허용되는 자리를 **여덟 개** 열거하고 *"그 외 어디에도 쓰지 않는다"* 로
끝난다. 그 여덟 개에 라디오가 들어 있긴 하지만 **네이티브 `accent-color`로서**이고,
커스텀 `.dot`은 목록에 없다.

- **닫힌 목록으로 읽으면 거부다** — "그 외 어디에도"가 문자 그대로 적용된다.
- **§4(채택하지 않는 것)에 대고 읽으면 누락이다** — E-32 §4는 반영하지 않을 것을 이유와 함께 전부
  적었는데, `.dot`은 거기에도 없다. 의도적 거부였다면 §4에 있었을 것이다.

현재 제품은 `.radio[data-checked='true'] .dot`에 `--accent-fill`을 쓰고(`base.css:618-622`), 그 이유가
주석에 있다 — 다크에서 `--color-accent`가 지면 대비 **1.09:1**이라 체크된 라디오가 체크 안 된 것처럼
보인다. 즉 **지금 값은 근거가 있는 값이고, 프로토타입 값으로 바꾸는 것은 그 근거를 대체할 다른 근거를
요구한다.** 어느 쪽으로 판정하든, hot 링과 accent-fill 채움은 **서로 다른 것을 사는 선택**이므로
다크에서의 대비를 먼저 재고 결정할 것.

### 8. 이 판정이 뒤집지 않는 것

E-32 §4의 여섯 항목과 E-35 §7의 다섯 항목은 **전부 그대로 거부**다. 특히
`.btn { justify-content: center }`는 계속 거부이고, `.btn-block`의 flush-left(ui-spec §0.3)는
§2.3의 어떤 개정 행도 건드리지 않는다. `.input::placeholder`의 `--color-neutral-500`도 채택하지
않는다 — surface 위 **2.37:1**(`tokens.css`의 `--ink-faint` 주석)이라, E-32 §4가 neutral-600을
3.31로 거부한 것과 같은 이유로 더 나쁘다.

## E-37 — 이어보기는 **시리즈당 한 장**, 채택 기준은 **뒷화**. 카드 폭은 ×0.8 (사용자, 2026-08-05) — BINDING

**출처.** 사용자의 스크린샷 두 장과 지시 두 줄이다. 그대로 옮긴다:

> "같은 만화작품에 대해서 두 개 이상의 이어보기가 생기는데, 하나만 생길 수 있도록 해주세요. 기준은
> 뒷화 우선으로 채택해주세요."

> "이어보기 카드의 넓이가 좀 깁니다. 20%정도 줄여주세요."

### 1. 판정 — 시리즈당 한 장. 채택은 **읽을 수 있는 권 우선**, 그 다음 `ord`가 가장 큰 권, 동률이면 `id`가 큰 쪽

`ORDER BY (b2.status = 'ok') DESC, b2.ord DESC, b2.id DESC` 위의
`ROW_NUMBER() OVER (PARTITION BY b2.series_id)` = 1.
`ord`는 스캐너가 매기는 시리즈 내부 순서이고, 이웃 권 이동이 이미 그것으로 항해한다. `id` 타이브레이크는
장식이 아니다 — `ord`는 스캐너가 물리는 값이고 동률을 금지하는 것이 없으므로, 없으면 셸프가 스캔마다
흔들린다.

> **정정 2026-08-05 — 여전히 E-37이다. 새 판정이 아니라 이 판정의 기록을 고치는 것이다.**
> 이 절은 원래 채택 규칙을 `ORDER BY b2.ord DESC, b2.id DESC`로 적었다. **가독성 키가 빠져 있었다.**
> 그 키(`(b2.status = 'ok') DESC`)와 §2.4의 시리즈 활동 정렬은 **첫 구현에 대한 리뷰가 결함 두 개를
> 찾았을 때 함께 들어왔다.** 왜 들어왔는지가 기록에 남아야 규칙이 다시 지워지지 않으므로, 두 결함을
> §2.4에 명시한다. 판정문이 코드보다 약하게 적혀 있으면 그 판정문을 읽고 구현을 되돌리는 사람이 생긴다
> — E-36 §2와 이 판정 §4가 같은 병을 두 번 진단했다.
>
> **`ord`는 `id`의 별칭이 아니다.** 이것도 이때 함께 못박혔다. 실제 `id`는
> 루트 상대 경로의 SHA-256을 base32로 자른 값(`internal/ids`)이라 `ord`와 아무 상관이 없는데,
> `index_test.go`의 모든 픽스처가 `ord`가 큰 권에 `id`도 큰 값을 주고 있어서 **`b2.ord DESC`를 지워도
> `TestListContinue` 17개 케이스(그리고 `internal/httpapi` 전체)가 전부 통과했다**. 즉
> `ORDER BY (b2.status='ok') DESC, b2.id DESC`라는 그럴듯한
> 오타가 시리즈마다 사실상 임의의 권을 출하하면서 검사를 통과했다. `ord` 순서와 `id` 순서가
> **어긋나는** 픽스처(`TestListContinue_electionRanksByOrd_whenTheIDOrderDisagrees`)가 그 구멍을
> 닫는다. **사용자의 요구(뒷화 우선)를 지키는 키는 실패할 수 있는 테스트를 가져야 한다.**

**진행률도 최근성도 기준이 아니다.** 이건 취향 문제가 아니라 셋이 실제로 갈라지기 때문이고, 사용자가
신고한 화면이 정확히 그 모양이다. 실측(제품 서버 `GET /api/continue`, 판정 전 바이너리):

| | `ord` | 읽은 위치 | `updated_at` |
|---|---|---|---|
| 「사랑」 **07권** — **채택** | 6 | **1 / 113p** | 1785768424 |
| 「사랑」 01권 — 탈락 | 0 | 24 / 116p | 1785759494 |

**셸프 16장 / 15개 시리즈, 중복은 정확히 이 한 건.** 즉 판정 후 16→15장이 된다.

이 실측 한 건이 죽이는 것은 **진행률 기준**뿐이라는 점을 명시한다 — 07권은 최근성으로도 이긴다.
`updated_at`을 기준으로 삼는 규칙은 이 데이터에서 **같은 답**을 내므로, 실데이터만으로는 구별되지 않는다.
구별은 테스트가 한다: `TestListContinue_laterVolumeWins_evenWhenTheEarlierIsFresherAndFurther`는 낮은
`ord` 쪽에 **더 늦은 `updated_at`과 더 많이 읽은 페이지를 둘 다** 주고도 져야 한다고 단언하며, 픽스처가
실제로 그 모양인지를 두 개의 가드로 다시 확인한다. **판정을 실데이터로 확인하고 테스트로 못박는다.**

### 2. 구현과 함정 — 넷 다 조용히 틀릴 수 있는 자리다

*(§2.4는 첫 구현에 대한 리뷰가 결함 두 개를 찾은 뒤 2026-08-05에 추가됐고, §2.2·§2.3도 같은 날
정정됐다. 판정 자체는 E-37 그대로다.)*

1. **중복 제거는 SQL 안, `LIMIT` 앞에서 한다.** Go에서 사후 필터링하면 `limit=N` 요청이 말없이 줄어든다
   — 한 시리즈의 여섯 권을 물어온 `limit=20`은 카드 15장을 그린다. 페이지네이션이 없어 눈에 안 띌 뿐,
   틀린 것은 틀린 것이다.
2. **서브쿼리 안의 `p2.completed = 0`은 필수다.** 빼면 마지막 권을 완독한 시리즈가 그 완독한 권을
   당선시키고, 그 권은 바깥 쿼리(`p.completed = 0`)에서 아무것도 매치하지 못해 **시리즈가 셸프에서 통째로
   사라진다** — 앞 권을 읽는 중이어도. 전용 회귀 테스트가 있다
   (`TestListContinue_seriesSurvivesWhenItsLastVolumeIsFinished`).
   반면 `r.enabled = 1`은 **일부러 반복하지 않는다**: 한 시리즈의 모든 권은 그 시리즈의 단일 루트에
   달리므로 파티션 안에서 enabled는 균일하다. 다만 그 논증은 **바깥 절이 제 일을 한다는 전제 위에**
   서 있는데 그 바깥 절에는 오래도록 테스트가 없었다 —
   `TestListContinue_disabledRoot_isOffTheShelf`가 지금 그것을 못박는다.
   바깥의 `p.completed = 0`은 반대로 **구조적으로 잉여**다(`progress`는 `book_id`가 키라 당선 서브쿼리가
   이미 같은 행을 걸렀다). 지워도 테스트가 전부 통과하지만 **지우지 않는다** — 계약을 읽는 자리에
   적혀 있는 편이 낫다. 대신 그것이 완독 책을 막는 장치라고 오해되지 않도록 소스에 그렇게 적었다.
3. **쿼리 플랜은 실측으로 나빠지고, 인덱스로는 못 고친다.** 중복 제거 전에는 부분 인덱스
   `ix_progress_continue`(`progress(updated_at DESC) WHERE completed = 0`)가 구동 루프였고 임시
   b-tree는 `b.id` 타이브레이크에만 필요했다. 지금은 `ORDER BY` 전체가 임시 b-tree로 정렬된다.
   **어떤 인덱스도 이걸 없애지 못한다** — 정렬 대상이 중복 제거된 후보 집합이고, 그것을 실체화하는
   인덱스는 존재하지 않기 때문이다.

   > **정정 2026-08-05 — 이 항목이 인용하던 4.4 ms는 1라운드 수치이고 폐기됐다.** 그 뒤 셸프의 정렬
   > 키가 §2.4로 바뀌었고, 2라운드가 같은 픽스처에서 잰 값은 6.33-6.44 ms였다. 즉 4.4 ms는 지금
   > 출하되는 쿼리를 설명하지 않는다. 더 중요한
   > 것은 **픽스처 기술 자체가 부족했다**는 점이다: "2 000 시리즈 / 10 000 권 / 미완 600행"에는
   > 정렬 비용을 실제로 결정하는 수 — **그 미완 행들이 덮는 서로 다른 시리즈의 수**(= `LIMIT` 이전의
   > 셸프 행 수) — 가 빠져 있다. 그 수만 움직여 재측정하면 `limit=5`가 **3.0 ms(60개) → 6.2 ms(200개)
   > → 10.9 ms(600개)** 로 갈린다. 소스 주석이 달고 있던 **다섯 형태의 속도 순위표도 이때 함께
   > 폐기했다**: 순위가 바로 그 빠진 변수에 따라 뒤집혔기 때문이다(같은 픽스처 안에서 한 대안이
   > 60개에서는 2 % 느리고 600개에서는 28 % 빠르다). **재측정이 뒤집는 순위는 사실이 아니므로 기록하지
   > 않는다.** 재측정으로 살아남은 주장은 하나다 — **측정한 모든 형태가 모든 `limit`에서 바이트 단위로
   > 동일한 행을 돌려준다.**
   >
   > **제품 크기에서의 정직한 비용**(964 시리즈 / 11 261 권, `limit=5`, best-of-25): 중복 제거가 없던
   > 쿼리 **0.40 ms** → 지금 쿼리 **0.95 ms**(미완 12행, 그럴듯한 서재). 미완이 늘면
   > 1.5 ms(50) · 5.0 ms(200) · 13.8 ms(600)이고, **같은 600행이라도 60개 시리즈에 몰려 있으면
   > 5.0 ms**다. 즉 **비용은 서재 크기가 아니라 "읽는 중인 시리즈 수"를 따라간다.** 배수는 진짜이고
   > 절대값은 무의미하다 — 다섯 장짜리 셸프가 알아챌 수 있는 값이 아니다. 되돌리려는 사람은 플랜이
   > 아니라 시계를 근거로 들 것. 실측 표는 `internal/index/books.go`의 `ListContinue` 주석에 있다.

4. **셸프의 정렬 키는 카드의 `updated_at`이 아니라 그 시리즈의 `MAX(updated_at)`이다** — 그것도 그
   시리즈의 **미완** 권들만 본다(`seriesActivity`). 이 항목과 §1의 가독성 키는 **첫 구현에 대한 리뷰가
   결함 두 개를 찾아서 함께 들어왔다.** 무엇을 고쳤는지 남긴다:

   - **결함 1 — 낡은 카드가 시리즈를 가라앉힌다.** `ord`로 한 장을 뽑고 나면 그 카드의 `updated_at`은
     더 이상 시리즈에 대한 진술이 아니다. 한 달 전에 07권을 잠깐 열고 5분 전에 01권을 읽은 독자에게
     07권은 여전히 뒷화 우선이 고르는 카드지만, 07권의 한 달 전 시각으로 셸프를 정렬하면 그 시리즈가
     맨 아래로 간다 — ui-spec §4.3이 트랙을 다섯 장으로 자르므로 **여섯 번째부터는 셸프에서 아예
     떨어진다**. 카드는 시리즈의 모양이 고르고, 셸프는 시리즈의 활동이 정렬한다
     (`TestListContinue_ranksBySeriesActivity_notByTheElectedCard`).
   - **결함 2 — 못 여는 권이 좋은 권을 영원히 가린다.** `status`가 `ok`가 아닌 권은 `page_count`가
     0이라 `userdata.PutProgress`가 자동 완독 처리를 할 수 없고, 따라서 **영구히 미완**으로 남아
     `ord`가 크다는 이유만으로 파티션을 계속 이긴다. 그래서 가독성을 **필터가 아니라 순위 키**로
     넣었다: 필터로 넣으면 시작한 권이 전부 깨진 시리즈가 셸프에서 통째로 사라진다
     (`TestListContinue_readableVolumeWins_overABrokenLaterOne`,
     `TestListContinue_seriesWithOnlyBrokenBooks_stillKeepsItsCard`).

   `seriesActivity` 안의 `p3.completed = 0`도 §2의 `p2.completed = 0`만큼 필수다. 빼면 **독자가 방금
   완독한 권이 그 시리즈를 셸프 위로 끌어올린다** — 이어보기는 남은 것의 셸프이므로 완독은 순위를
   올릴 근거가 될 수 없다. 이 절이 처음 쓰였을 때 그 절에는 테스트가 없었고 지워도 전부 통과했다.
   지금은 `TestListContinue_seriesActivity_ignoresFinishedVolumes`가 막는다.

### 3. 판정 — 카드 폭 ×0.8

272 → **218**, 336 → **269**. `flex-[0_0_218px] md:flex-[0_0_269px]`. 커버(96×144), 갭(12), 패딩(12)은
움직이지 않으므로 20%는 전부 **글자 열**에서 나온다: `width − 96 − 12 − 24`로 **140 → 86**(<768),
**204 → 137**(md). 크롬 실측으로 확인했다.

**작은 쪽에서 권 파일명이 잘린다. 이것은 받아들인 열화이지 깨진 것이 아니다** — 그 줄은 원래
`truncate whitespace-nowrap`이고, 카드가 사는 이유는 시리즈 이름·이어읽을 위치·표지이지 파일명이 아니다.
페이지 카운터는 잘리지 않는다: Archivo tabular 12px에서 `1,234 / 1,234p`가 **76.5px**, 86px 열에
들어간다(실측). 다섯 자리(`12,345 / 12,345p`, 90.2px)에서 처음으로 두 줄로 접히지만 카드 높이는 그대로다
— 높이를 정하는 것은 144px 커버다. **`truncate`를 붙이지 않는다**: 붙이면 그 경우가 줄바꿈 대신
`12,345 / 12,3…`이 되어 더 나쁘다.

### 4. 이 판정이 정정하는 **문서 사실**

ui-spec §4.3은 **첫 커밋부터** 카드를 `flex:0 0 300px`라고 적어 왔다. 제품은 같은 첫 커밋부터 272/336을
출하했다. **300px는 단 하루도 출하된 적이 없다.**

272/336과 96×144 커버는 **5세션차**에 들어왔고, `docs/HANDOFF.md` §1.0e가 그것을 **판정 없이** 반영했다고
기록한다(*"나머지(브랜드·아이콘·치수)는 충돌이 없어 판정 없이 반영했다"*). **E-32는 이 치수를 건드린 적이
없다** — E-32 커밋은 `flex-[0_0_272px] md:flex-[0_0_336px]`와 `h-[144px] w-[96px]`를 바이트 단위로
그대로 두고 지나가며, E-32 판정문에는 336도 272도 96도 없다. 그럼에도 이번 작업의 첫 초안은 두 치수를
E-32의 것이라고 적었고 그 문장을 ui-spec 두 곳과 소스 주석 한 곳에 복제했다. **감사가 git으로 뒤집었고,
네 곳 전부 정정했다.** 없는 판정을 지어내는 것은 낡은 판정을 방치하는 것보다 나쁘다 — 뒤의 것은 낡았다는
표시라도 남기지만, 앞의 것은 **근거가 있는 것처럼 보이게 만들어 재검증을 멈춘다.**

**이것이 E-36 §2가 진단한 병의 재발이고, 같은 파일에서다.** 자기를 폐기한 판정보다 오래 산 계약이 다시
발견됐다. 다만 이번 것은 더 나쁜 변종이다: `300px`를 반박할 판정이 애초에 **존재한 적이 없었다.**
5세션차가 판정 없이 넣었으므로 열 세션 동안 그 숫자와 충돌하는 문서가 하나도 없었고, 명세를 읽은 사람은
자기가 무엇을 어기는지 알 방법이 없었다. **E-37이 그 공백을 닫는다 — 이 치수는 이제 판정을 가진다.**

같은 절에서 **네 개의 값이 더 틀린 채로 서 있었고**(구역 `border-bottom:2px`, 트랙 `padding-bottom:4px`,
카드의 `border` + accent 호버, 제목 `Archivo 800`, 진행바 `height:3px` + `var(--color-accent)`), 전부
pre-E-32 계약이다. 취소선으로 실제 값과 함께 남겼다. **"확인했다"는 긍정 주장이 침묵보다 나쁘다**는 것이
여기서의 교훈이다: 첫 초안은 *"Nothing else in this section moved … all as stated"* 라고 적었고, 그
문장은 다섯 항목에 대해 거짓이었다. 재측정 없는 확인 문장은 쓰지 않는다.

### 5. 열린 채로 남기는 것 — 이 판정은 이것들을 폐기하지 **않는다**

ui-spec §7 반응형 표의 이어보기 열 아래 두 칸은 **미구현 요구사항**이다:

- **`<768`** — 전폭 카드, 화면당 한 장, **스냅 스크롤**. 트리 어디에도 `scroll-snap`이 없다(실측 0건).
- **768–1023** — **260px** 티어.

이 판정은 카드 폭을 정하지만 **반응형 계층을 짓지 않는다.** §7 서두("The prototype implements none of
this … Build the layer below")와 §0.5가 살아 있고, §0.2 개정이 명령하는 독법 — *명세가 명세고
스타일시트가 뒤처진 쪽* — 이 그대로 적용된다. **코드에 맞춰 이 두 칸을 고쳐 쓰는 것은 개정이 아니라
요구사항 삭제다.** 실제로 이번 작업의 첫 초안이 그렇게 했고 되돌렸다.

**이 구멍이 열 세션 살아남은 이유를 함께 기록한다: `web/e2e/07-responsive.spec.ts`에 이어보기 커버리지가
0이다.** 그 파일은 네 티어 전부에서 사이드바·그리드·리스트·뷰어를 몰지만 셸프는 한 번도 언급하지 않는다.
**보지 않는 검사는 없다고 보고하지 못한다**(§6.5). 이 두 칸을 닫는 순서는 400과 900에서의 테스트가
먼저이고 CSS가 나중이다.

---

## E-38 — **초안 · 미서명 · BINDING 아님.** 설정 대화상자 안에서 스캔 진행을 보여준다 (구현 에이전트 초안, 사용자 서명 대기, 2026-08-05)

> **이 절은 판정이 아니다.** `docs/ui-spec.md` §8.6 §1이 인용하는 초안 전문이며, 사용자 또는
> 오케스트레이터가 서명하기 전까지 아무 구속력이 없다. 서명되면 이 경고 블록을 지우고 제목을
> `— BINDING`으로 바꾼다. **거부되면 §8.6의 새 불릿 세 줄과 `RootsPanel.tsx`의 `ScanProgress`가
> 함께 빠진다.** 새 화면 요소를 판정 없이 명세에 밀어 넣지 않기 위해 이 형식을 쓴다 — E-36 §2와
> E-37이 같은 파일에서 진단한 병이 "자기를 폐기한 판정보다 오래 산 계약"이었고, 그 반대편이
> "출처 없이 생긴 명세"다.

**출처.** 사용자의 한 줄이다. 그대로 옮긴다:

> "재스캔이 있을 경우, 처리진행 상태를 볼 수 있어야 하는데 안 보임."

### 1. 실측 — FR-IDX-004는 이미 **두 번** 구현되어 있었다. 둘 다 스크림 뒤에 있었다

| 렌더러 | 위치 | 보이는가 |
|---|---|---|
| 96×2px 바 + `%` | `components/shell/TopBar.tsx:132` (상단 바) | `.dialog-backdrop` **아래** |
| 점 + `스캔 중 {done} / {total}` | `components/shell/ScanIndicator.tsx` ← `Sidebar.tsx:221` | `.dialog-backdrop` **아래** |
| `스캔 중` | `features/series/SeriesHeader.tsx:202` (시리즈 상세) | 다른 화면 |
| **재스캔 버튼** | `features/overlays/RootsPanel.tsx:90` | **설정 대화상자 안** |

즉 이것은 없는 기능이 아니라 **배선 결함**이다. 백엔드는 `GET /api/scan/status`로
`state · current_root · current_item · total · done · errors · covers_* · elapsed_ms · eta_ms`를 전부
내보내고 있고(arch §7.10), 클라이언트는 이미 1초마다 그것을 폴링하고 있었다(`api/queries.ts:503`).
**사용자가 스캔을 시작할 수 있는 유일한 화면이, 스캔을 볼 수 없는 유일한 화면이었다.**

**테스트가 이것을 잡지 못한 이유도 실측했다: 비-idle 스캔 상태를 프로바이더를 통해 렌더러까지 몰아본
테스트가 저장소에 하나도 없었다.** `router.test.tsx:66`은 idle 픽스처만 서빙하고
`SettingsDialog.test.tsx`는 `/api/scan/status`를 아예 등록하지 않았다. 요구사항은 자기가 가진 모든
테스트를 통과하면서 사용자에게는 아무것도 보여주지 않고 있었다 — **엉뚱한 것을 보는 검사**의 교과서적
사례다.

### 2. 판정 — 블록은 **루트 관리 안**, 실행 중에만, **전체 실행 단위**로

1. `<h6>루트 관리</h6>` **바로 아래**. 스캔 로그 절(§8.6 §3)이 의미상 더 맞지만 그것은 대화상자
   맨 아래이고 `overflow-y:auto`의 접힌 부분이다 — 사용자가 누른 버튼의 대답은 버튼 옆에 있어야 한다.
2. **`state !== "idle"`일 때만 렌더한다.** idle 문구(`스캔 대기 — {n}분 전 완료`)는 `ScanIndicator`의
   것이고, 두 벌을 두면 동기화해야 할 곳이 둘이 된다. arch §7.10에 `"done"` 상태는 없다 — 끝난 실행은
   `finished_at`이 찍힌 `idle`이므로 이 한 줄이 판정의 전부다.
3. **루트별 진행률은 만들지 않는다.** `ScanStatus`에 루트별 분해가 없다(`PerRoot`는 HTTP 경계에서
   버려지고 `Root`에는 스캔 상태 필드가 없다). `current_root`는 "지금 어느 루트 안에 있는가"이므로
   현재 항목 경로의 접두어로 찍고, 행 안에는 아무것도 넣지 않는다. **API가 대답할 수 없는 주장을
   화면이 하지 않는다.**
4. 숫자는 `lib/format.ts`의 `formatScanLabel`·`scanPercent`를 **재사용한다**. 사이드바와 상단 바가
   쓰는 바로 그 함수여야 세 자리가 반올림 규칙으로 어긋나지 않는다. 바는 `ds/ProgressBar` —
   `TopBar.tsx:134`의 손수 만든 `<span>`은 `role="progressbar"`가 없고, 그 실수를 복제하지 않는다.
5. 전송은 **D-16 그대로**: `GET /api/scan/status` 1초 폴링, SSE 없음. 두 번째 폴링 경로도 만들지
   않는다 — `useScanStatus`는 같은 쿼리 키를 공유하므로 관측자가 둘이어도 요청 루프는 하나다.

### 3. 함께 닫는 두 구멍 — 판정이 필요 없고, 같은 버튼의 것이다

- **`POST /api/scan`의 실패가 UI를 하나도 만들지 않았다.** `RootsPanel`은 `startScan.isPending`만
  읽었으므로 arch §7.10의 `400`·`404`·`409`·`503` 전부가 성공과 **바이트 단위로 같은 화면**을 냈다.
  `rootErrors.ts`와 같은 집 규칙(`role="alert"`, 상태마다 제 문장)으로 표면화한다. 이 패널의 `409`는
  **반드시 "이미 스캔 중"이다** — 나머지 하나의 `409`("설정된 루트가 전부 제거됨")는 서버
  `scanRoots`의 `len(requested) == 0` 가지에서만 나오고 이 패널은 언제나 루트 하나를 지명한다.
- **`재스캔`이 실행 내내 살아 있었다.** `scanDisabled`가 `isPending`뿐이어서, 요청이 날아가는
  밀리초만 꺼지고 스캔이 도는 **수 초** 동안 켜져 있었다. 아무 일도 안 일어난 것처럼 보일 때 사람이
  하는 가장 자연스러운 행동인 두 번째 클릭이 조용한 `409`로 갔다. 실행 중에는 끈다 — 이 비활성화는
  지킬 수 있는 약속이다(실행이 끝나면 폴이 idle을 보고하고 버튼이 돌아온다).
- **실행이 끝나도 아무것도 갱신되지 않았다.** `non-idle → idle` 전이를 보는 코드가 저장소에
  없었다. `useStartScan`은 `['scan','status']`만 무효화하고 그것은 폴을 다시 켤 뿐이다. 특히
  `useScanLog`에는 `refetchInterval`이 없고 그 키는 `['scan','log',params]`라 `['scan','status']`와
  **매치되지 않는다** — 사용자가 열어놓고 보는 로그 패널이 영영 움직이지 않는 이유가 그것이다.
  완료 시 `series`·`roots`·`continue`·`scan.log` 넷을 무효화한다.

### 4. 열린 채로 남기는 것

- **스캔 중 로그는 여전히 실시간이 아니다.** 완료 시점에 한 번 다시 읽을 뿐이다. 실행 중 폴링을
  붙이면 D-16의 1초 예산이 초당 두 요청이 되므로, 그것은 별도 판정이다.
- **`covers` 단계에서 바는 100%에 머문다.** `done`/`total`은 권 수이고 커버 생성은 그 뒤다.
  `covers_done`/`covers_total`로 분모를 바꾸면 이 블록과 사이드바가 서로 다른 숫자를 말하게 되므로
  하지 않았다. 두 단계를 나눠 보여줄지는 판정이 필요하다.
- **§9 #11 `ScanIndicator`의 `pct` prop은 실제 컴포넌트에 없다.** 표에 플래그만 달고 고치지 않았다
  (E-36 §2의 독법).

---

## E-39 — **초안 · 미서명 · BINDING 아님.** 증분 스캔의 건너뛰기는 **`status='ok'`인 권에만** 적용된다 (구현 에이전트 초안, 사용자 서명 대기, 2026-08-06)

> **이 절은 판정이 아니다.** `docs/arch-backend.md` §4.6이 인용하는 초안 전문이며, 사용자 또는
> 오케스트레이터가 서명하기 전까지 아무 구속력이 없다. 서명되면 이 경고 블록을 지우고 제목을
> `— BINDING`으로 바꾼다. **거부되면 §4.6의 첫 불릿과 `internal/scanner/incremental.go`의
> `prior.Status != StatusOK` 한 줄이 함께 빠진다** — 그리고 그때는 "고친 파일을 다시 읽는 유일한 길은
> `full: true`뿐"이 다시 계약이 되므로, 재스캔 버튼이 `{"full": true}`를 보내도록 바꾸는 별도 판정이
> 반드시 뒤따라야 한다. **문서화된 계약(arch §4.6)을 바꾸는 변경이므로 이 형식을 쓴다.**

**출처.** 사용자의 증상 한 줄이다. 그대로 옮긴다:

> "깨진 zip을 고쳐 넣고 재스캔했는데 여전히 `비어 있음 / 읽을 수 있는 페이지가 없습니다`."

### 1. 실측 — 한 증상, 세 개의 결함. 판정이 필요한 것은 그중 하나다

사용자의 실제 서재(11,261권)에서 측정했다.

| 사실 | 값 |
|---|---|
| `궁 24.zip`을 제품 자신의 리더(`zipidx.ReadIndex` → `source.Excluded`)로 읽은 결과 | `entries=94 kept=93 dropped={directory entry:1}, readErr=<nil>` — **디스크의 파일은 멀쩡하다** |
| 같은 권의 `index.db` 행 | `status='empty', page_count=0, error='no supported image entries'` |
| 그 행의 `file_size` / `file_mtime` | **디스크의 값과 정확히 일치**, `scan_gen`은 최신 세대 — 최신 스캔이 방문했고 다시 읽지 않고 지나갔다 |
| `scan_log`의 실패 기록 시각 / 파일 mtime | 20:32:00 / 20:30:52 — 스캔이 "비었다"고 선언했을 때 파일은 이미 68초 전에 완성돼 있었다 |
| 잘못 표시된 권 수 | 11,261권 중 **정확히 2권**, 둘 다 사용자가 **막 교체한** 파일 |
| 나머지 비-ok 57권 | 전부 진짜로 깨졌거나 진짜로 이미지가 없다 |

**폭발 반경이 정확히 "교체된 파일"이라는 것 자체가 원인을 가리킨다.**

1. **열린 핸들 풀이 교체된 파일의 서술자를 내준다.** `openpool.Pool.Acquire`는 캐시 히트에서
   재검증을 하지 않는다(`pool.go:180`). `ref()`는 호출자가 원한 `(mtime,size)`를 **핸들이 열릴 때
   기록된** 값과 비교해 `stale`을 세우고 경고를 찍은 뒤 **그 핸들을 그대로 돌려준다**(`:233`).
   `zipsource.List`는 `ref.Stale()`을 아예 보지 않았다(`zipsource.go:41`). `mv 궁\ 24.zip.new 궁\ 24.zip`
   뒤에 경로는 새 inode를 가리키지만 풀은 unlink된 옛 inode의 살아 있는 서술자를 쥐고 있다 — 이것은
   낡은 읽기가 아니라 **사용자가 지운 파일을 읽는 것**이고, 그 결과가 새 파일의 신원으로 기록된다.
   *(측정: `TestZipSource_List_afterTheFileIsReplaced_readsTheNewInode`는 수리 전 트리에서
   `listing the repaired archive: no supported image entries`로 실패한다 — 사용자가 본 바로 그 문장이다.)*
2. **잘못된 판정이 영구적이다.** `unchanged()`는 `unsupported`만 다시 살폈다(`incremental.go:98`).
   `empty`와 `error`는 기록된 `(size, mtime)`이 디스크와 이미 일치하므로 **영원히** 건너뛰어진다.
   빠져나갈 길은 `full: true` 하나뿐인데, 재스캔 버튼은 그것을 보내지 않는다.
3. **스캔 진행 폴이 영구히 무장에 실패할 수 있다.** `Start`는 `progress.begin` **전에** 실행 id를
   반환했다(`scanner.go:321` vs `:448`). 클라이언트는 202에서 폴을 한 번 돌리고 `idle`을 보면 멈춘다.
   *(측정: 64회 반복 중 **0회차에서 즉시** `idle/""`가 관측된다 — 확률이 아니라 사실상 결정적이다.)*

**세 결함은 하나의 증상으로 합쳐진다.** ①이 틀린 판정을 쓰고, ②가 그것을 영구화하고, ③이 사용자에게서
스캔이 돌았다는 증거마저 빼앗는다. 이 중 **판정이 필요한 것은 ②뿐이다** — arch §4.6이 문서화한 계약이기
때문이다. ①과 ③은 자기 명세를 어긴 구현이므로 §3에 함께 적는다.

### 2. 판정 — 건너뛰기는 **성공적으로 읽은 권에 대한 최적화**다

1. **`prior.Status != 'ok'`이면 건너뛰지 않는다.** `(size, mtime)`도, 디렉터리 지문도 그다음에 본다.
2. **근거는 이미 저장소 안에 있었다.** 옛 규칙에도 예외가 하나 있었고(`unsupported`), 그 주석이 든
   이유가 정확히 옳았다 — *"이 상태는 이 **빌드**가 못 읽는다는 뜻이지 파일의 성질이 아니다."*
   틀린 것은 이유가 아니라 **범위**다. `empty`와 `error`도 파일의 성질이 아니다: ①이 만든 판정이 그렇고,
   일시적 I/O 오류가 그렇고, 복사 중인 파일을 스친 스캔이 그렇다. 셋 다 디스크의 바이트가 뒷받침하지
   않는 판정을 쓰고, 셋 다 그 뒤로 도달 불가능해진다.
3. **비용을 정직하게 적는다.** 비-ok 권 하나당 스캔 하나당 `open` 한 번 + 중앙 디렉터리 읽기 한 번이다.
   실제 서재에서 **11,261권 중 57권(0.5%)**이고, 엔트리 페이로드는 하나도 풀지 않는다(FR-IDX-002).
   이것은 `unsupported` 예외가 이미 받아들인 바로 그 비용이며, NFR-PRF-004(무변경 재스캔 < 30초)의
   예산에 대해 측정 가능한 크기가 아니다. **병든 서재에서는 이 비용이 커진다** — 절반이 깨진 서재라면
   재스캔의 절반이 콜드 스캔이 된다. 그것을 받아들이는 이유는 §4에 적는다.
4. **대안을 검토하고 버렸다: "사용자가 시작한 스캔에서만 다시 살핀다."** `Request`에 필드 하나와
   HTTP 경계까지의 배선이 필요하고, 무엇보다 **자동 스캔은 영영 낫지 않는다**. 사용자가 파일을 고치는
   시점과 버튼을 누르는 시점이 같아야만 낫는 규칙은, 고친 사람이 버튼을 눌러야 한다는 것을 알아야만
   작동한다. 지금 이 증상이 바로 그 가정이 틀렸다는 증거다.
5. **`ok`가 아닌 상태의 목록을 열거하지 않는다.** 규칙은 `!= 'ok'`이지 `in ('empty','error','encrypted','unsupported')`가
   아니다. 상태가 하나 늘어날 때 규칙을 고쳐야 한다면, 고치는 것을 잊는 날이 온다. 기록이 비어 있는
   행(`status=''`)도 성공한 읽기의 증거가 아니므로 같은 가지로 떨어진다.

### 3. 함께 닫는 두 구멍 — 판정이 필요 없다. 구현이 자기 명세를 어기고 있었다

- **①은 arch §5.2가 이미 요구한 것이다.** 그 절은 *"`Pool.Invalidate(path)`는 스캐너가 권을 다시 쓸
  때마다 호출된다"*고 적고 있었지만, `grep -rn Invalidate`의 **비-테스트 호출자는 0개**였다. 목록 경로만
  고친다: `List`는 stale ref를 받으면 핸들을 버리고(`Invalidate`) **한 번** 다시 열며, 새 서술자마저
  어긋나면 그 한 권을 `source.ErrContainerChanged` → `status='error'`로 실패시킨다(FR-IDX-010).
  기록된 `(size, mtime)`이 디스크와 다르므로 다음 스캔이 반드시 다시 읽는다 — 스스로 낫는다.
  **서빙 경로의 관용(arch §5.2, §7.6)은 손대지 않는다.** 페이지 스트림은 색인이 기록한 오프셋에
  묶여 있고 그 오프셋이 가리키는 것은 풀이 쥔 서술자다. **읽기는 관용하고, 쓰기는 관용하지 않는다.**
- **③은 arch §7.10의 202가 뜻하는 바다.** `Start`가 실행 id를 돌려주기 **전에** 그 실행의 상태를
  게시한다. 부수 효과 하나를 명세에 적었다: `roots[]`가 배경 고루틴이 아니라 호출자 고루틴에서
  해석되므로, `httpapi.scanStartError`의 `ErrUnknownRoot` 가지(§7.10의 `400` 행)가 **비로소 도달
  가능해진다** — 그 전까지 미지의 루트는 로그 한 줄과 `202`였다. 클라이언트는 건드리지 않는다.
- **`integration/harness_test.go`의 스캔 대기 루프는 이 변경으로 *정직해진다*.** 그 루프는 첫 `idle`을
  "스캔 종료"로 읽는데, arch §7.10에 `done` 상태가 없으므로 `idle`은 "아직 시작 안 함"과 "끝남"을 겸한다.
  202 이전에 상태가 게시되므로 그 루프가 볼 수 있는 모든 스냅숏은 이 실행의 것이고, 따라서 `idle`은
  이제 반드시 "끝남"이다. 그 위에 `run_id` 비교를 더했다 — 게시 순서가 퇴행하면 조용한 오탐이 아니라
  **타임아웃**으로 드러난다.

### 4. 열린 채로 남기는 것

- **깨진 권이 많은 서재의 재스캔 비용에는 상한이 없다.** 규칙은 "비-ok 권을 다시 읽는다"이고, 비-ok
  권의 수는 서재가 정한다. 실측 0.5%에서는 무의미한 비용이지만 50%인 서재에서는 아니다. 상한(예:
  "실행당 N권까지, 오래된 것부터")은 **다른 판정**이며, 그것을 지금 넣지 않는 이유는 상한이 곧
  "어떤 권은 영영 다시 읽히지 않는다"의 다른 이름이고 그것이 바로 이 판정이 없애려는 병이기 때문이다.
- **`error` 권은 이제 스캔마다 `scan_log`에 warn 한 줄씩 남긴다.** 실측 서재에서 실행당 57줄이고
  `index.LogRetention`이 흡수한다. 로그가 "지금 상태"를 말하게 된 것은 오히려 이득이지만, 운영자가
  같은 57줄을 매번 보게 되는 것도 사실이다. 중복 억제는 별도 판정이다.
- **①의 서빙 쪽 사촌은 그대로 남아 있다.** 색인이 갱신된 뒤에도 풀이 옛 서술자를 쥐고 있으면
  `Stale()`이 계속 참이므로 `?v=`를 든 요청은 그 핸들이 축출될 때까지 `409`를 받는다. 스캐너의
  목록 경로가 `Invalidate`를 부르게 되면서 **재스캔을 거친 권에 대해서는** 사라지지만, 일반 규칙으로
  닫으려면 `Acquire`의 히트에서 재검증을 하거나 스캐너가 다시 쓴 모든 권을 무효화해야 한다.
  후자가 arch §5.2가 원래 적어둔 것이고, 그것은 이 작업의 범위가 아니다.
