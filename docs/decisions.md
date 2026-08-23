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
| D-07 | ~~Nested archives (a ZIP of ZIPs)~~, ~~RAR/CBR~~, 7z **out**; `internal/archive.Reader` stays an interface. | prd §7.2. **Nested ZIPs are now IN — see D-70. RAR/CBR are now IN — see D-71.** 7z and friends remain out. The clause that survives all of it is the last one, and it is the reason the other two were cheap: the interface was kept. |
| D-70 | **Nested ZIPs are in, as books.** A ZIP whose entries are ZIPs becomes a series of `kind:"nestedzip"` volumes, one per inner archive. Supersedes the first clause of D-07 and narrows E-14. | 45 books — 623 volumes, 16.9 GB — were unreachable, `겟 벡커스 1~39완.zip` among them, and "the container is `empty`" was a true statement about a library the reader could not open. The cost turned out to be small: the inner archive is presented as an `io.ReaderAt` (`internal/archive/nested`), so the existing reader indexes and streams it unchanged, and the entries are stored JPEGs whose deflate ratio is measured at 1.0000, making the inflate very nearly a copy. Nothing is extracted and no cache is added. Only a book that produced **no pages of its own** is ever opened looking for volumes, so the ordinary path is untouched. |
| D-71 | **RAR 4 is in, as a book format.** `.rar`/`.cbr` on disk are `kind:"rar"`; inside a container they are `kind:"nestedrar"`. New package `internal/archive/rar4` implements `archive.Reader`. Supersedes D-07's RAR/CBR clause. **Solid archives, multi-volume sets, split entries, encrypted entries and RAR 5 are refused by name**, not attempted. | The measurement decided it, and it is the number a reader should check first if this is ever revisited. Across all **14** RAR archives in the collection (**2,914** entries, ~1.1 GB, none of them a book until now): **solid archives 0, solid entries 0, multi-volume 0, encrypted 0, RAR 5 0**; **2,685 entries stored, 229 packed** (method 0x31, in 3 books); packing ratio **1.0000**, because they are JPEGs. Solid is the one that matters — in a solid archive page N cannot be produced without decompressing 1..N-1, so FR-SRV-002's "one page, one seek" would be a lie and the format could not be admitted at all. None of these is solid, and 92% of their entries are *stored*, which is byte-for-byte the access pattern of a stored ZIP entry. So the stored path takes no dependency at all (an `io.SectionReader` over the container, seekable, so Range still works); only the packed 8% reaches `rardecode`. Indexing reads block headers only (FR-IDX-002), and serving reaches a packed entry by splicing `signature + the container's own main header + this entry's block` into a one-file archive — valid RAR, correct because non-solid, **O(1)** rather than O(entries), and needing no new column: `LocalHdrOff` keeps exactly the meaning FR-SRV-002 gives it. Measured: reaching entry 826 of a 385 MB archive costs **6 ms**. Verified against a whole-archive `rardecode` oracle over the real collection — 14 archives, 2,914 entries, every name, length and CRC-32 agreeing. Also in: `사모님은 학생회장.zip`'s 8 RAR volumes, which D-70 had to drop. |
| D-73 | **An archive whose pages live in per-chapter directories is one 권 per directory.** `books.kind:"nesteddir"`, `inner_path` = the directory inside the container; the container stops being a book. The rule fires only when, after the longest shared directory prefix is stripped, **two or more** directories remain; pages left at the container's top level become one more 권 (`inner_path:"."`, `… (loose pages)`, sorted first). Extends D-70's move to the case where the inner 권 are folders rather than archives. | **484 of the collection's 11,153 archives (4.3%) are this, holding 279,541 pages** — `여자친구 만들고파! 01~08권.zip` is 842 pages in eight per-volume folders, `배틀로얄 1~15 [완결].zip` 1,540 in fifteen, `암살교실 1~180화.zip` 3,534 in **182** folders literally named 화. Each was one book: a 권 list with a single unnavigable entry, and 6,097 volumes the reader could not address. **prd §2.2 row 2 had already decided this** for the same tree on disk — one 권 per image sub-folder — so the archive was the odd one out, not the folder. The cost is a pass over a page list the scan already holds (no extra read, no payload) plus one central-directory read per chapter, and it is paid only by a book that has the shape; each chapter goes through `indexUnit`, so FR-IDX-003 still skips an unchanged one and the 6,097 page-row sets are not rewritten every scan. The partition is **total** — measured 1,540 pages before and after on 배틀로얄 — which is why the loose cover image beside 29 volume folders in `야와라!` becomes a 권 rather than a dropped page, and why the one ambiguous shape (a shared wrapper folder *and* loose pages, which no archive in the collection has) is left unsplit rather than guessed at. An index that already exists splits on a **full** scan only: E-39 skips a container recorded `ok`. |
| D-72 | **A book that is one format this build cannot open reports the format, not `empty`.** `status:"unsupported"` with the format named in `books.error`, for a container whose entries are all foreign (`.hv3`, `.7z`, `.alz`, `.egg`, `.lzh`, …) — but **only when there are no pages and no volumes**, so a foreign entry beside readable content stays a footnote. Narrows D-29 and E-14. | `펌프킨 시저스 04.zip` was the last of the 48 books reporting `비어 있음 · no supported image entries`, and the only one where that sentence was **false** rather than merely unhelpful: it holds 39.5 MB in a single `.hv3`. HV3 is a proprietary container and this one is *encrypted* — the header carries an `ENCR` chunk (value 2), the `LIST` chunk is empty, and the body measures **7.9972 bits of entropy per byte** with **2** JPEG signatures in 39.5 MB. Nothing recovers that without the key and nothing here tries. What was wrong was the explanation: `비어 있음` sends an owner looking for damage in a file that is intact. E-14's `비둘기.zip` — one directory entry, 128 bytes — keeps `empty`, which is what it is. The "no volumes" half of the rule is not decoration: naming the `.7z` inside `사모님은 학생회장.zip` closed the container as `unsupported` before the scanner ever looked for its 15 volumes, and the e2e round caught it two tiers from the change. |

## Architecture & dependencies

| # | Decision | Rationale |
|---|---|---|
| D-08 | Dependency set frozen at the arch §1.1 versions: `modernc.org/sqlite v1.54.0`, `x/text v0.40.0`, `x/image v0.44.0`, `disintegration/imaging v1.6.2`, `gen2brain/avif v0.6.0`, `klippa-app/go-pdfium v1.19.6`, `nwaples/rardecode/v2 v2.3.0`, `wazero v1.12.0`, `yaml.v3`, `x/crypto`. | All verified to build and run `CGO_ENABLED=0` on this machine; pinning removes a class of "works on my box". `rardecode` was added by **D-71** and verified to the same standard (§1.1); `Makefile`'s `FROZEN_DEPS` and `make tidy` count **10**. |
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
| D-29 | A series whose root child holds only non-media files is listed rather than hidden. | Hiding directories the user can see in their file manager is more confusing than greying them out. **Narrowed by D-72**: the status is `empty` only when there is genuinely nothing in there. A child holding a format this build recognises and cannot open — `.hv3` was the original example here — is `unsupported` and says which format. |
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

## E-38 — 설정 대화상자 안에서 스캔 진행을 보여준다 (사용자 서명, 2026-08-06) — BINDING

> **서명 기록.** 이 절은 11세션차에 구현 에이전트가 초안으로 작성했고(2026-08-05), 12세션차 시작
> 시점에 **사용자가 서명했다(2026-08-06).** 서명 전까지는 구속력이 없었으므로 `docs/ui-spec.md`
> §8.6 §1과 `RootsPanel.tsx`의 `ScanProgress`는 그동안 근거 없이 서 있었다 — 지금부터는 이 절이
> 그 근거다. 초안 형식을 쓴 이유는 새 화면 요소를 판정 없이 명세에 밀어 넣지 않기 위해서였다:
> E-36 §2와 E-37이 같은 파일에서 진단한 병이 "자기를 폐기한 판정보다 오래 산 계약"이었고,
> 그 반대편이 "출처 없이 생긴 명세"다.

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

## E-39 — 증분 스캔의 건너뛰기는 **`status='ok'`인 권에만** 적용된다 (사용자 서명, 2026-08-06) — BINDING

> **서명 기록.** 이 절은 11세션차에 구현 에이전트가 초안으로 작성했고(2026-08-06), 12세션차 시작
> 시점에 **사용자가 서명했다(2026-08-06).** 이로써 `docs/arch-backend.md` §4.6의 증분 스킵 계약이
> 정식으로 바뀐다 — 스킵 대상은 이제 "크기·mtime이 같은 모든 권"이 아니라 **"크기·mtime이 같고
> `status='ok'`인 권"** 이며, `internal/scanner/incremental.go`의 `prior.Status != StatusOK`가
> 그 계약의 코드다. 거부되었더라면 "고친 파일을 다시 읽는 유일한 길은 `full: true`뿐"이 계약으로
> 남고 재스캔 버튼을 바꾸는 별도 판정이 뒤따라야 했다 — **그 후속 판정은 이제 필요 없다.**
> 문서화된 계약을 바꾸는 변경이었기 때문에 초안 형식을 썼다.

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

---

## E-40 — 루트 **추가**는 재시작 없이 반영된다. 폴더 선택기는 **화이트리스트 안에서만** 탐색한다 (사용자, 2026-08-06) — BINDING

**출처.** 사용자의 한 줄이다. 그대로 옮긴다:

> "루트추가후 서버재시작이 필요없도록 해주고, 추가하려는 루트폴더를 다이알로그를 통해 선택할 수 있도록
> 개선해주세요"

이 판정은 **개정 A-12**를 만들고, **구속 판정 E-26의 개정 A-11 제한 (1)을 부분적으로 뒤집는다.**

### 1. 실측 — 사용자는 이미 이 두 마찰을 겪은 뒤에 요청했다

| 사실 | 값 |
|---|---|
| `shelf.yaml`의 `server.allow_root_editing` | **이미 `true`** — 인수인계와 메모리가 적어 둔 `false`는 낡은 것이었다 |
| `shelf.yaml.bak`의 존재 | 잔재가 아니라 **설정 작성기가 남긴 백업**이다(§7.4의 원자적 쓰기) |
| `shelf.yaml`과 `.bak`의 차이 | 루트 `root`(「일반책」) **한 줄** — 사용자가 UI로 추가했다 |
| 그 편집 시각 / 실행 중인 서버의 기동 시각 | **01:30 / 01:23** — 서버가 7분 먼저 떴으므로 그 루트는 지금 `pending`이다 |

즉 요청은 가정이 아니라 **직접 겪은 두 가지**다: 추가한 루트가 재시작 전까지 읽히지 않았고, 경로를
손으로 쳐야 했다.

### 2. 판정 — 추가만 뜨겁다. 제거는 A-11의 R1 그대로다

**`POST /api/roots`는 파일에 쓴 뒤 그 루트를 이 프로세스에 연다.** `source.RootSet.Add`가 `os.Root`
핸들을 열고, `Scanner.AddConfigRoot`가 이름표에 넣고, `index.UpsertRoot`가 행을 쓰고, 그 루트만 대상으로
스캔이 시작된다. `GET /api/roots`의 행은 `pending: false`가 되고 `config_changed_on_disk`는 **거짓**이
된다 — 이 프로세스와 파일이 다시 일치하기 때문이다.

**제거는 뜨겁게 만들지 않는다.** 사용자가 "추가만"을 골랐고, 그것이 옳다: 열린 핸들을 닫는 것은
페이지를 스트리밍 중인 요청과 경합하므로 항목별 참조 계수가 필요하고, A-11의 R1(제거 집합)이 그것 없이
이미 제거를 즉시 반영한다. **A-11 제한 (1) 중 살아남는 부분이 정확히 이것이다.**

**채택이 실패하면 `201`은 그대로다.** 파일 쓰기는 사용자가 요청한 것이고, 서버가 방금 `stat`에 성공한
디렉터리를 열지 못한다고 해서 사용자의 편집을 되돌리는 것은 우리 사정으로 그들의 의도를 버리는 것이다.
그때는 **A-11의 동작이 그대로 대체 경로가 된다** — `pending` 행과 재시작 안내. 그래서 그 경로는
문서가 아니라 테스트로 고정돼 있다(`TestCreateRoot_fallsBackToPendingWhenTheRootCannotBeOpened`).

### 3. 판정 — 폴더 선택기는 새 엔드포인트 `GET /api/browse`이며, 두 겹으로 갇힌다

E-26은 탐색 API를 **값을 매기고 사지 않았다.** 그 논거는 두 가지였고 지금도 유효하다: 기본 배포가
`listen: 0.0.0.0` + `auth:` 블록 없음(판정 E-8)이라 **인증 없는 LAN 리스너**이고, 탐색은 *읽기*라서
누군가 쓰기 권한을 주기 **전에** 도달 가능하다.

**두 겹이 그 두 논거에 각각 답한다:**

1. **`server.browse_bases` 화이트리스트.** 엔드포인트는 기준 디렉터리이거나 그 아래인 것만 나열한다.
   "파일시스템에서 일부를 뺀 것"이 아니라 **기준에 먼저 걸리지 않으면 도달하는 경로가 없다.**
   기본값은 비어 있고, 비면 전부 거부한다 — 이 키를 모르는 운영자는 E-40 이전 제품을 그대로 받는다.
2. **읽기 자체가 `os.Root`를 통과한다.** 매칭된 기준 위에 연 핸들이고, 이는 모든 미디어 루트가 쓰는
   layer-3 핸들 그대로다. 요청의 `..`도, 기준 안에서 밖을 가리키는 심볼릭 링크도 **커널이 openat(2)
   에서 거부**한다 — 이 파일의 문자열 비교가 아니라.

그리고 엔드포인트는 **`gateRootEditing()` 뒤에** 있다. 즉 운영자가 이미 더 큰 권한(읽을 수 있는 임의
디렉터리를 루트로 걸기)을 준 경우에만 도달한다. **탐색이 설치본이 부여하는 첫 권한이 되어서는 안 된다.**

`browse_bases`가 `allow_root_editing`과 **별개의 키**인 이유가 이것이다. 편집이 켜진 곳에서 탐색은 새
권한이 아니지만(편집이 그것을 이미 포함한다), 편집이 꺼진 곳에서는 새 권한이다 — 그리고 운영자가 그
포함 관계를 스스로 따져 보게 만들어서는 안 된다.

**경로는 `/api/browse`이고 `/api/roots/browse`가 아니다.** 후자가 더 읽기 좋지만, `browse`는 §3.2의
알파벳이 허용하는 **적법한 루트 이름**이고 Go의 `ServeMux`는 리터럴 패턴을 `/api/roots/{name}`보다
우선하므로 — `browse`라는 이름의 루트가 삭제 불가가 된다. 데이터에 의존하는 조용한 `405`다.

### 4. 판정 — 노출 범위는 `127.0.0.1`로 좁힌다

**사용자가 골랐고, 이것이 위 두 판정의 값을 치른다.** 이 설치본의 `shelf.yaml`은 `listen`을
`"127.0.0.1"`로 바꾼다. `auth:`는 여전히 없다(E-8). **`0.0.0.0`으로 되돌리려면 `auth:` 블록과 함께여야
한다** — 그 문장은 설정 파일 안에 적혀 있다.

이것은 제품의 기본값이 아니라 **이 설치본의 선택**이다. `shelf.example.yaml`의 `listen` 기본값은
바뀌지 않았다.

### 5. 선택기가 §7.4의 표를 다시 구현해서는 안 된다

모든 항목은 `selectable`과 `reason`을 들고 온다. **서버가 `validateRootCreate`와 같은 규칙에서, 같은
헬퍼로 계산한다.** 프런트엔드가 자기 논리로 회색 처리하면 두 판단은 언젠가 어긋나고, 어긋남은 사용자가
서버가 거부할 폴더를 누를 때까지 보이지 않는다 — §6.5가 말하는 "엉뚱한 것을 보는 검사"의 모양 그대로다.
`browse_test.go`는 그래서 각 경우를 **두 번** 단언한다: 선택기가 보여주는 깃발과, 같은 경로에 대해
`POST /api/roots`가 실제로 내는 상태.

### 6. 이 판정이 낡게 만드는 것

- `arch-backend.md` §7.4의 *"restart-based, not hot-reload"* — **추가에 한해** 폐기. §3.2에 `browse_bases`가 추가된다.
- `internal/httpapi/roots.go` 머리 주석의 규칙 1, `dto.go`의 `Root.Pending` 주석, `client.ts`의 `createRoot` 주석 — 전부 개정됐다.
- `AddRootForm.tsx`의 *"폴더 찾아보기는 제공하지 않습니다"* 와 *"서버를 다시 시작한 뒤 읽힙니다"* — 둘 다 교체됐다. **삭제가 아니라 교체다**: 왜 없었는지가 왜 생겼는지의 절반이다.
- `web/e2e/08-roots.spec.ts`의 *"실 라운드는 `allow_root_editing`을 끈 채로 둔다"* 는 여전히 참이다 — E-40은 기본값을 바꾸지 않았다.

## E-41 — 루트 **제거**도 채택 기준을 되돌린다. A-12가 추가 쪽만 옮겨 놓은 것을 마저 맞춘다 (사용자, 2026-08-07) — BINDING

**출처.** 13세션차가 세 세션 만에 `make e2e-synthetic`을 돌리자 08-roots가 네 뷰포트 전부에서 깨졌다.
낡은 단언 셋은 A-12 계약대로 고쳤는데 **하나가 남았다** — 추가한 루트를 제거한 뒤에도 설정 화면이
*"서버를 다시 시작하면 적용됩니다"* 를 계속 띄운다. 이것은 낡은 단언이 아니라 **제품 결함**이었다.

### 1. 메커니즘 — 대칭이 반쪽만 왔다

E-40은 `adoptRoot`에 기준 다이제스트를 **전진**시키는 단계를 넣었다(`roots.go:745-749`). 그래야
추가 직후 프로세스와 파일이 다시 일치하고 §7.8이 거짓 알림을 띄우지 않는다. **`handleDeleteRoot`에는
그 짝이 없었다.**

| 순서 | 파일 | `configDigest` | `config_changed_on_disk` | |
|---|---|---|---|---|
| 기동 | B0 | B0 | false | |
| 추가 | B1 | **B1** (A-12가 전진) | false | 맞다 |
| 제거 | **B0** (바이트 복원) | B1 (그대로) | **true** | **틀렸다** |

`dto.go:415`가 이 플래그를 정의하는 문장은 *"`ConfigPath`의 파일이 이 프로세스가 로드한 그것이
아니다"* 이다. 제거 뒤의 파일은 **로드한 그것 자체**다(`rootsfile.go`가 원시 줄을 스플라이스하므로
추가-제거는 바이트를 되돌린다). 즉 플래그가 자기 정의를 어겼다.

**같은 자리에 두 번째 것이 있었다 — 리뷰가 찾았다.** `handleDeleteRoot`는 `addedRoots`에서도 그 루트를
빼지 않는다. `configuredRoots()`는 `cfg.Roots + addedRoots`이고 제거 집합을 거르지 않으므로,
`GET /api/browse`가 **제거된 루트의 폴더를 계속 `duplicate`로**, 그 부모를 `overlaps`로 표시한다 —
같은 경로에 대해 `POST /api/roots`는 받아 주는데도. 선택기가 존재하는 이유가 *"클라이언트가 §7.4를
다시 유도하지 않는다"* 인데, 서버가 낡은 목록으로 답하면 **같은 드리프트가 서버의 권위를 달고** 나온다.

### 2. 판정 — 제거도 기준을 옮긴다

**`DELETE /api/roots/{name}`는 성공 시 `adoptRoot`와 같은 가드로 `configDigest`를 전진시키고,
`addedRoots`에서 그 이름을 뺀다.** 가드가 같은 이유도 같다: **내가 쓴 파일이 내가 이미 채택한
파일이었을 때만** 기준이 움직인다. 둘이 달랐다면 그 사이에 *다른 무엇*이 파일을 고친 것이고,
`config_changed_on_disk`는 그 변경에 대해 진실을 말하는 중이므로 내 쓰기가 그것을 침묵시켜서는 안 된다.

**이것은 A-11의 문장 하나를 뒤집는다.** A-11에서는 제거 뒤에도 재시작 안내가 떴다. 그러나 R1은
제거를 **즉시** 반영한다(제거 집합) — 그러므로 그 안내는 이미 적용된 변경에 대해 재시작을 요구하는
거짓말이었다. E-40이 추가에 대해 고친 것과 **같은 거짓말이고, 같은 이유로 고친다.**

**핫 리무브는 여전히 안 산다.** `releaseRoot`는 핸들을 닫지 않는다 — E-40 §2와 항목 `ae`가 그대로
유효하고, 스트리밍 중인 요청과 경합하는 항목별 참조 계수는 여전히 미구매다. 되돌리는 것은 **장부**뿐이다.

### 3. 왜 게이트 넷이 초록인 채로 살아남았나 — §6.5, 이음매에서

`TestCreateRoot_writesTheFileAndAdoptsIt`은 추가 쪽 절반을 지키고,
`TestSettings_configChangedOnDiskIsFalseUntilSomethingChanges`는 손으로 고친 파일에 대해서만 플래그를
본다. **둘 다 자기 절반을 정확히 보고 있었고, 결함은 그 사이에 있었다.** vitest·typecheck·lint·
`make test`가 전부 초록이었다.

**찾은 것은 `make e2e-synthetic`이고, 찾은 방식이 이 판정의 핵심이다** — 네 뷰포트 프로젝트가 서버
하나와 설정 파일 하나를 공유하므로, **첫 프로젝트의 추가-제거가 나머지 셋의 화면을 오염시켰다.**
결함이 한 사용자의 한 세션에 갇히지 않는다는 증거를 게이트가 스스로 만들어 낸 것이다.
**세 세션 연속 이 게이트를 안 돌린 대가가 이것이었다.**

### 4. 검사 — 뮤테이션으로 확인했다

`internal/httpapi/roots_test.go`에 둘을 넣고, `releaseRoot` 호출을 지운 상태에서 **둘 다 빨개지는 것을
확인했다**(무방비가 아니라 방어). e2e는 브라우저 계층에서 제거 뒤 선택기를 다시 열어 **그 폴더가 다시
선택 가능해지는 것**을 단언한다 — 와이어와 화면이 같은 답을 하는지가 선택기의 유일한 존재 이유이므로.

`config-changed-notice`의 `toHaveCount(0)` 둘은 **`GET /api/settings`의 값에 못박았다.** 요소가 없다는
것은 증거가 아니다 — `RootsPanel.tsx`의 알림 블록을 지우면 두 단언이 다 초록이 된다. §6.5 그 모양이다.

## E-42 — 컨트롤 표면은 **절대값**이다. 크림 알약은 두 테마 모두에 오고, 그림자는 테마가 아니라 표면을 따른다 (사용자, 2026-08-08) — BINDING

**출처.** E-36이 soft-UI의 나머지 절반을 채택하라고 판정하면서 두 가지를 남겼다 — §7의 `.radio .dot`
판정, 그리고 §4의 "뷰어의 컨트롤은 크림"이 앱 다크 테마에 대해 뜻하는 바. 14세션차가 둘 다 받았다.

### 1. 판정 z — `.radio:has(:checked) .dot`은 **채움은 accent-fill, hot은 inset 링**

프로토타입(`soft-ui.css:107`)은 `border-color: var(--color-hot) !important`다. 사용자 선택은
**절충안 C**: 몸통은 지금대로 `--accent-fill`이 지고, `--color-hot`은 그 위에 얇은 inset 링으로 얹는다.

```css
.radio[data-checked='true'] .dot {
  border-color: var(--accent-fill);
  background: var(--accent-fill);
  box-shadow:
    inset 0 0 0 1.5px var(--color-hot),
    inset 0 0 0 4px var(--color-bg);
}
```

**근거는 측정치다.** 다크 지면 `#263B38` 위에서 `--color-hot`은 **2.83**, 다크 표면 `#2F4A46` 위에서
**2.28** — UI 컴포넌트의 3:1 바닥에 미달한다. 프로토타입 값을 문자 그대로 옮기면 체크된 라디오의
형태를 지탱하는 것이 그 미달 색이 된다. `--accent-fill`은 다크에서 **6.21**(표면 5.01)이므로 형태는
그것이 지고, hot은 "현재/선택됨" 표식으로만 남는다.

**링 자체는 이 판정의 자기 기준으로 검증되지 않는다 — 그 사실을 적어 둔다.** 리뷰가 렌더로 기하를
확인하고(바깥부터 accent-fill 1.5px │ hot 1.5px │ `--color-bg` 2.5px │ accent-fill 코어) 링의 두 이웃을
쟀다: hot vs `--accent-fill`은 라이트 1.86 / **다크 2.18**, hot vs `--color-bg`는 라이트 3.25 /
**다크 2.72**. 즉 hot을 형태에서 물린 근거(2.83/2.28의 3:1 미달)가 링이 옮겨간 자리에도 해당한다.
**그럼에도 채택하는 근거는 링이 상태를 지지 않는다는 것이다** — WCAG 1.4.11이 3:1을 요구하는 것은
*상태를 식별하는 데 필요한* 부분이고, 여기서 그 일을 하는 것은 `--accent-fill` 몸통(다크 6.21)이다.
링은 그 위에 얹힌 표식이고, 적색 대 민트라는 **색상** 차이로 지각된다. 링을 못 봐도 체크 여부는
읽힌다 — 그것이 옵션 B를 물리고 C를 고른 이유이기도 하다.
덧붙여 `--color-bg` 도넛이 4px에서 **2.5px로 얇아졌다**(1.5px를 hot이 가져갔다). 의도된 결과다.

**그리고 `.card`에 대해 §7이 적은 것과 같은 실측을 이 판정 자신에게도 적용해 둔다: `.radio`는 소비자가
0건이다.** `components/ds/Radio.tsx`가 이 클래스의 유일한 생산자이고, 그 컴포넌트를 쓰는 화면이 없다.
**이 절의 판정은 화면을 바꾸지 않는다** — 판정 자체는 그래도 필요했다(E-36 §7이 명시적으로 남긴
질문이고, 답이 `.seg-opt`와의 문법 통일이라는 것이 다음에 라디오가 화면에 설 때의 계약이다).
한쪽 컴포넌트의 호출처 0만 공개하고 다른 쪽은 침묵하면, 그 비대칭 자체가 다음 사람을 오도한다.

**부수 효과가 이 안을 고른 진짜 이유다:** 이렇게 하면 `.seg-opt[data-checked='true']`(액센트 채움 +
`inset 0 0 0 2px var(--color-hot)`)와 **같은 문법**이 된다. E-32 §1이 hot에 준 역할이 제품 전체에서
한 가지 형태로 말해진다 — 채움이 형태를 지고, hot이 그 위에서 "이것이 선택된 것"이라고 말한다.

**E-32 §1의 여덟 곳 목록은 닫힌 목록이 아니라 열거였다는 뜻으로 읽는다.** 다만 이 판정은 그 목록을
넓히지 않는다 — hot이 새로 가는 자리는 여기 하나이고, 그 자리는 이미 목록에 있는
`.seg-opt` 선택 링과 같은 종류다.

### 2. 크림 컨트롤은 **전역**이다 — 뷰어의 예외가 아니라 제품의 규칙

E-36 §4는 뷰어에 대해서만 못박았다. 그러나 뷰어의 다크 스코프와 앱의 다크 테마는 **같은 토큰 블록**
(`[data-theme='dark']`)이고, tokens.css 머리말이 설명하듯 `:root[…]`로 둘을 가르는 것은 이 파일이
일부러 거부한 수법이다. 따라서 선택지는 둘뿐이었다 — 크림을 전역으로 올리거나, 스코프 분기를 도입해
컨트롤 언어를 둘로 가르거나.

**사용자 판정: 전역.** 라이트 앱·다크 앱·뷰어 세 곳에서 `.btn-secondary` · `.input` · `.seg`가
같은 크림 표면이다. 컨트롤 언어가 하나가 되고, E-36 §4가 요구한 뷰어가 예외가 아니라 규칙이 된다.
대가는 앱 다크 테마의 컨트롤이 눈에 띄게 바뀐다는 것이고, 그것이 이 판정이 사용자에게 간 이유다.

### 3. 새 토큰 여섯 + 그림자 셋 — 그리고 **그림자는 표면을 따른다**

절대값이라는 것은 **다크 블록에 짝이 없다**는 뜻이다. 짝을 만드는 순간 절대값이 아니게 된다
(tokens.css `absolutes` 블록의 `--on-accent` / `--on-hot`과 같은 부류).

| 토큰 | 값 | 근거 (그레인 씌운 뒤, 세 톤 중 최악) |
|---|---|---|
| `--control-fill` | `#F3EEE3` | 뷰어 지면과 **9.80** 분리. 9세션차가 프로토타입의 `뒤로`에서 실측한 값 |
| `--control-fill-hover` | `#F8F4EC` | E-36 §4가 "대응 토큰이 없다"고 적은 그 색 |
| `--control-well` | `#EFE9DC` | 파인 트랙(`.seg`). neutral-200을 고정한 것 |
| `--on-control` | `#0D3436` | 크림 위 **11.02**, well 위 10.55 |
| `--on-control-accent` | `#17595B` | 호버 잉크 — hover 크림 위 **7.06** |
| `--on-control-dim` | `#5F5849` | 플레이스홀더·미선택 세그 옵션 — 크림 위 **5.90**. 프로토타입의 neutral-500(2.37)은 E-32 §4가 이미 거부했다 |

**그림자를 고르는 규칙(이 판정이 세운다): 그림자는 테마를 따르지 않고, 그것이 떨어지는 표면을 따른다.**

- 페이지 지면·표면 위 → 기존 `--shadow-sm/md/lg` (테마 따라 뒤집힘)
- 크림 컨트롤 **안쪽** → `--shadow-control-inset` (라이트 inset 값을 고정)
- 크림 컨트롤 **위** → `--shadow-control-raised` (라이트 sm 값을 고정) — 크림 트랙 안의 선택된 세그 옵션
- 액센트 채움 **안쪽** → `--shadow-accent-inset` (다크 inset 값을 고정) — `.btn-primary:active`

이 규칙이 없으면 다크 테마에서 크림 알약 안쪽에 다크용 inset(검정 + accent-300 하이라이트)이 칠해진다.
표면이 뒤집히지 않는데 그림자만 뒤집히는 것이 정확히 그 오류다.

### 4. E-36 §3.1은 **토큰 이름이 바뀐 채로** 충족된다 — 기록해 둔다

E-36 §3의 완료 기준 1은 *"`base.css`에서 `var(--shadow-inset)`가 0회가 아니다"* 이다. §4의 절대값
판정 때문에 `.input`과 `.seg`가 실제로 쓰는 이름은 **`var(--shadow-control-inset)`**(라이트 값을 고정한
것)이다. 문자 그대로 세면 기준 1은 여전히 0회로 보인다. **이것은 미달이 아니라 이름의 대체이고,
그 사실을 여기 적는 이유는 E-36 §2가 진단한 병이 정확히 "기록 없는 차이"이기 때문이다.**
`--shadow-inset`은 Tailwind `shadow-inset` 유틸리티(커버 우물·스켈레톤·진행바 트로프)에서 계속 쓰인다 —
그것들은 테마를 따라 뒤집히는 지면 위에 있다.

### 5. 채택하지 않은 행 — 이유와 함께 남긴다

E-36 §5.7.6의 전수표 33행 중 셋과, `ui-spec` §2.3의 ⟳ 행 하나의 반쪽은 채택하지 않는다.
**기록 없는 미채택이 이 사태의 원인이었으므로**(E-36 §2) 여기 적는다.
(§8 ①의 `.seg` `overflow: hidden`이 그 "반쪽"이고, 실측 근거는 거기 있다.)

| 행 | 미채택 이유 |
|---|---|
| ▪3 `.btn { font-weight: 600 }` | `ui-spec` §2.3의 `.btn` 행은 ⟳가 아니다 — 10세션차가 여덟 행을 개정하면서 이 행은 그대로 두었다. 버튼 라벨은 heading 패밀리이고 E-32는 800을 유지했다. `.tag`의 600은 §2.3이 ⟳로 적었으므로 **채택한다** |
| ▪28 `.seg-opt { padding: 5px 12px }` | §2.3이 *"5px가 타깃을 통과한다고 **측정되기 전에는** 제품의 7px를 유지"* 라고 적는다. 측정하지 않았다 |
| ⬛18 `.tag-neutral`의 램프 쌍 100/800 → 200/700 | 없는 것은 **그림자**이지 채움이 아니다(§2.3도 그렇게 적는다). 지금 쌍은 다크 짝과 측정된 대비(10.68)를 갖고 있고, 그것을 한 스텝 옮기는 데는 근거가 없다. 그림자만 채택 |
| ▪17 `.tag-accent`를 프로토타입의 `accent` 채움 / `#F6F2E9` 잉크로 | 전수표 자신이 이 행을 *"Modernist 원본과 한 글자도 다르지 않다"* 고 강조한다 — 즉 프로토타입이 **바꾸지 않은** 자리다. 제품의 `accent-100`/`accent-800` 쌍은 다크 짝(`800`/`100`)과 측정된 대비 11.36을 갖고 있고, 프로토타입 값은 그 둘 다 없다. 바꿀 이유가 없으므로 바꾸지 않는다. **이 행은 리뷰가 지적하기 전까지 채택도 미채택도 기록되지 않은 유일한 행이었다** — 미기록 미채택을 금지하는 절 자신이 하나를 빠뜨린 것이고, 그래서 여기 적는다 |
| ▪32·33의 **알파 절반** — `--scrim-modal` `.50 → .34` | `backdrop-filter: blur(2px)`는 채택했다. 알파는 아니다: `--scrim-modal`은 다이얼로그 전용 토큰이 아니라 **`.drawer-backdrop`이 같이 쓴다.** 프로토타입 값을 넣으면 드로어의 스크림까지 얇아지는데, 드로어에는 그 차이를 메울 블러가 없다 — 전화 화면 전체에 거는 블러는 그 프레임에서 가장 비싼 연산이라 일부러 안 걸었다. **프로토타입에는 드로어가 아예 없다**(데스크톱 전용, ui-spec §0.5). 즉 프로토타입은 이 질문에 답한 적이 없고, 그 값을 그대로 옮기는 것은 근거 없이 다른 화면을 바꾸는 일이다. 알파를 옮기려면 다이얼로그 전용 스크림 토큰을 새로 유도해야 하고, 그것은 이 판정의 범위 밖이다 |

### 6. 이 판정이 뒤집지 않는 것

E-32 §4, E-35 §7, E-36 §8의 거부 목록은 전부 그대로다. `--color-hot`이 브랜드 색으로 돌아오지 않는다는
E-32 §1도 그대로다 — §1의 hot은 여기서도 마커로만 쓰인다.

### 7. E-36 §5.3의 철거 목록은 불완전했다 — 그리고 그것이 §6.5의 모양이다

E-36 §5.3은 죽는 것으로 `border-neutral-700` **5곳**만 열거했다. 실제로 세어 보니 테두리 유틸리티는
**7곳**이었고, 더 중요한 것은 **테두리가 아니라 잉크**였다. 컨트롤이 크림으로 채워지는 순간, 그 위에
얹힌 잉크 유틸리티는 다크 테마에서 밝은 색을 가리키고 있으므로 **글자가 사라진다.** 측정된 것:

| 자리 | 얹힌 것 | 크림 위 대비 |
|---|---|---|
| `ViewerBottomBar.tsx:111` | `text-accent-text` (다크 `#9BC3C1`) | **1.65** |
| `PageError.tsx:45` | `text-ink` (다크 `#EAE3D4`) | **1.10** |
| `VolumeTile.tsx:202` | `bg-bg` — 채움을 덮어써 죽이고, 새 잉크가 지면 위에 남는다 | **1.13** |
| `CommandPalette.tsx:156` | `bg-transparent` — 우물을 없애고 잉크만 남긴다 | **1.40** |
| `TopBar.tsx:109`·`:126` | `.input` **위에 겹쳐 놓인 형제 스팬**의 `text-ink-dim` | **1.50** |

**뒤의 셋은 판정에도, 첫 번째 조사에도 없었다.** 특히 마지막 것은 컨트롤의 `className`이 아니라
그 위에 절대 배치된 **다른 요소**라서, 클래스 목록을 훑는 어떤 grep에도 걸리지 않는다.

**메커니즘은 하나다: `base.css`는 `@layer components` 안에 있고 Tailwind 유틸리티는 그 뒤에 온다.**
그러므로 유틸리티 오버라이드는 **예외 없이** 컴포넌트 클래스의 채움과 잉크를 이긴다. 컨트롤의 채움을
바꾸는 작업은 그 클래스의 규칙을 고치는 일이 아니라, **그 클래스 위에 얹힌 모든 것을 다시 세는 일**이다.

이것을 여기 적는 이유는 §6.5 그 자체이기 때문이다 — 판정이 열거한 것은 고쳐지고, 열거하지 않은 것은
그대로 남는다(E-36이 E-32에 대해 발견한 것과 **같은 문장**이다). 다음에 컨트롤 표면을 바꾸는 사람은
목록을 믿지 말고 **다시 세라.**

**손으로 칠한 가짜 secondary 하나도 같이 정리했다.** `features/library/SeriesCard.tsx`의 커버 호버
오버레이에서 `상세`는 `variant`가 없는 `plain` 버튼에 `bg-bg text-ink`를 손으로 칠한 것이었다 —
`.btn-secondary`가 투명 테두리였던 시절, 커버 아트 위에서 읽히려면 불투명 바탕이 필요했기 때문이다.
그 이유가 이 판정으로 사라졌다(`.btn-secondary`가 불투명 크림이고 잉크는 11.02). 그대로 두면
**제품에서 secondary처럼 생긴 유일한 비-크림 버튼**이 되고, 바로 위 형제는 이미 액센트 알약이다.
`variant="secondary"`로 옮겼다. 이런 것은 33행 감사표에 잡히지 않는다 — 그 표는 `soft-ui.css`와
`base.css`를 대조한 것이고, 이것은 **컴포넌트가 손으로 흉내 낸 클래스**이기 때문이다.

**덧붙여 실측 하나:** `components/ds/Card.tsx`는 `web/src` 어디에서도 import되지 않는다. `.card`에
`--shadow-md`를 넣는 것은 계약을 맞추는 일이지 화면을 바꾸는 일이 아니다 — 실제 카드 표면은 전부
손으로 쓴 Tailwind다(`useLibrary.ts`의 `LIST_CARD_CLASS`, `ContinueCard`, `NextVolumeCard`의 `elev-lg`).

### 8. 리뷰가 실측으로 잡은 것 — 이 라운드가 스스로 낸 결함 셋

구현 뒤 **다른 에이전트**가 리뷰했고, 세 건은 계산이나 렌더로 재현된 진짜 결함이었다. 전부 고쳤다.

**① `overflow: hidden`을 지운 것이 크림 후광을 어두운 바 위에 그렸다.** `ui-spec` §2.3의 ⟳ 행이
*"`overflow:hidden` goes with `padding`+`gap`"* 라고 지시하고 구현이 따랐는데, 그 선언은 모서리만
자르던 것이 아니라 **승강도 자르고 있었다.** 선택된 옵션의 `--shadow-control-raised`는 up-left lobe가
`rgb(255 253 246 / 0.9)`, 도달거리 11px(offset 3 + blur 8)인데 트랙 padding은 3px다 — 클립이 없으면
근백색이 트랙 밖 8px까지 나간다. 실제 Chrome 렌더에서 뷰어 바 대비 **3.29:1**, 즉 3:1을 넘어 은은한
그림자가 아니라 **형태로 읽히는 밝은 테두리**다. `tokens.css`가 다크 elevation 블록에서 스스로
규탄하는 *"a white outline around every card"* 바로 그것이다.
**이것은 E-36 §4의 경고가 두 번째로 실현된 것이다** — 다크 팔레트가 없는 프로토타입의 지시를,
프로토타입이 가진 적 없는 스코프에 그대로 옮긴 것. `overflow: hidden`은 되돌렸고 §2.3의 그 반쪽은
**미채택**으로 기록한다.

**② 절대 표면 위의 테마 상대 tint — 다크에서 무동작이었다.** `.seg-opt[data-checked='false']:hover`가
`background: var(--hover-tint)`를 쓰는데 그 아래 `--control-well`은 뒤집히지 않는다. 다크에서
근백색 8% wash를 근백색 크림 위에 얹으면 **1.00:1** — 배경 호버가 일어나지 않았다. §3의 규칙을 어긴
유일한 자리였고, 프로토타입은 원래 **색만으로** 호버한다. tint를 지우고 잉크 호버만 남겼다.

**③ forced-colors에서 이 스킨은 통째로 증발한다.** 경계를 전부 `box-shadow`로 옮긴 것이 이 판정의
핵심인데, **forced-colors는 `box-shadow`를 `none`으로 강제한다.** 게다가 E-36 §3이 `.input`의 포커스
표시를 base 층의 `outline`에서 `box-shadow`로 옮겼으므로, 그 모드에서 **포커스된 입력 필드에 표시가
하나도 남지 않았다.** 리뷰가 Playwright `forcedColors: 'active'`로 실측했다. 저장소 전체에
`forced-colors` grep은 그때까지 **0건** — 이 모드를 다룬 적이 한 번도 없었다.
`@media (forced-colors: active)` 블록을 넣어 `.btn-secondary`·`.input`·`.seg`·`.tag-neutral`·
`.tag-outline`·`.card`에 `border: 1px solid ButtonBorder`를, `.input:focus-visible`에
`outline: 2px solid Highlight`를 준다. **일반 렌더에는 영향이 0이고, 이것은 테두리 상자의 귀환이
아니다** — 완료 기준 3의 세 번째 범주(마커 / 구조선 / forced-colors 폴백)이며, 검사는 그 예외를
**`@media (forced-colors: active)` 안에 있을 때만** 허용한다.

**그리고 기록만 하는 것 셋:**

- **`--control-fill`은 라이트 `--color-surface`와 같은 색이다(1.00).** 즉 라이트 테마에서 다이얼로그
  안의 `.btn-secondary`는 다이얼로그와 **완전히 같은 색**이고, 버튼으로 만드는 것은 `--shadow-sm`
  하나뿐이다. soft-UI의 의도한 모습이지만, 계약서가 측정한 쌍은 전부 어두운 지면(쉬운 이웃)이었다.
- **`.btn-ghost:hover`는 다크 `--color-surface` 위에서 3.90 washed다** — AA 미달이고 **이 라운드가
  만든 것이 아니다**(호버 잉크는 전부터 `--accent-text`였다). 사이드바·다이얼로그의 고스트 버튼이
  해당한다. 항목으로 연다.
- **비활성 `.btn-secondary`는 라이트에서 지면 대비 1.05로 사실상 사라진다**(라벨 2.55 washed).
  WCAG 1.4.3이 비활성 컴포넌트를 면제하므로 위반은 아니다.

### 9. 둘째 리뷰가 잡은 것 — **UA가 칠하는 잉크**, 그리고 그림자가 옮긴 바닥

컴포넌트·문서·검사 계층을 **또 다른 에이전트**가 리뷰했고(합성 픽스처로 실제 빌드를 띄워 계측했다),
넷이 진짜였다. 전부 고쳤다.

**① 다크에서 검색 필드의 네이티브 ✕가 크림 위 근백색 — 1.14:1.** `.input`이 절대 크림이 됐는데
`tokens.css`의 다크 블록은 여전히 `color-scheme: dark`를 선언한다.
`::-webkit-search-cancel-button`은 저자 CSS가 아니라 **UA가 스킴에서 색을 뽑아** 그리므로, 크림
필드 위에 근백색 ✕가 앉았다. 채움이 뒤집히던 동안에는 옳았던 선언이 채움을 고정한 순간 거짓이 된
것이다. **`.input`에 `color-scheme: light`를 선언해 닫았다** — 표면이 절대 라이트라고 말한 이상 그
안쪽의 UA 렌더링도 라이트여야 하고, 이 한 줄이 ✕·caret·네이티브 `<select>` 팝업을 함께 정합시킨다.
`appearance: none`은 증상을 컨트롤째 지우는 것이라 쓰지 않았다.
**이 자리가 중요한 이유는 §7이 세운 방법론이 닿지 못하는 곳이라는 것이다** — 잉크가 저자 CSS에
없으므로 grep에도, 토큰 스캐너에도, 클래스 목록 파싱에도, DOM 워크에도 걸리지 않는다. §7의 다섯
자리는 전부 저자 유틸리티였다. **여섯 번째 형태가 있다: UA가 칠하는 것.**

**② forced-colors에서 사라지는 것은 경계와 포커스만이 아니었다 — 선택 상태도였다.** §8③이 폴백을
넣으면서 테두리와 포커스만 복원했는데, 체크된 세그의 마커는 `inset 0 0 0 2px var(--color-hot)`,
즉 **그림자**다. 그 모드에서 체크된 옵션과 안 체크된 옵션이 계산 스타일까지 동일했다 — 뷰어의 표시
모드가 무엇인지 알 방법이 없었다는 뜻이다. `outline`은 forced-colors에서 살아남으므로 마커를
`outline: 2px solid Highlight; outline-offset: -2px`로 다시 세웠고, `[aria-pressed='true']`에도 같이
걸었다. `.btn-primary`에도 테두리를 줬다 — 없으면 제품에서 경계가 하나도 없는 유일한 것이
**주 동작 버튼**이 된다.
**§8③이 "닫았다"고 적은 범주가 그 문장 때문에 닫힌 목록으로 읽혔다는 것이 이 건의 교훈이다** —
§7이 E-36에 대해 지적한 바로 그 형태를, 그 지적을 쓴 라운드가 자기 문장으로 재현했다.

**③ 그림자가 잉크의 바닥을 옮겼고, 쌍 스캐너는 그림자를 못 본다.** ⌘K 힌트 칩은 14.8px인데
`--shadow-control-inset`은 36px 컨트롤용이다(offset 3 + blur 7 = 양쪽에서 10px). 두 로브가 가운데서
만나 **칩 안에 `--control-well` 픽셀이 하나도 없다** — 좌상단은 오커, 우하단은 근백색이다. 실제
픽셀에 대고 재면 dim 잉크는 **4.55 washed / 4.44 peak**로 11px 텍스트의 AA 바닥 아래인데,
선언된 토큰 쌍으로 재면 5.65라 통과처럼 보인다. 칩의 잉크를 `--on-control`로 올려 닫았다
(가장 어두운 픽셀에서도 10.4).
**아이러니가 기록할 값어치가 있다: 이 쌍은 §6.5를 닫으려고 이번 라운드가 `FILL` 정규식을 넓혀
새로 스캔하게 만든 유일한 쌍이고, 그 검사가 5.65로 인증했다.** 그림자를 경계 언어로 삼은 스킨에서는
"채움과 잉크의 쌍" 모델이 부분적으로 거짓이다.

**④ 포커스 링이 "우물 안쪽"이라던 주석이 렌더와 반대였다.** `0 0 0 2px`는 `inset`이 아니므로
border-box **바깥** 2px 밴드에 그려진다 — `outline-offset: 0`이 그리던 자리와 픽셀 단위로 같다.
주석 셋이 *"inside the well"*, *"flush to the well's edge rather than 2px outside it"* 라고 적고
있었다. 사실로 정정했다. 파생으로 `CommandPalette`의 필드가 그 틀린 기하를 근거로 `outline-offset: 0`을
골랐는데 실제로는 각진 링이 다이얼로그의 6px 라운드 코너 밖으로 튀어나온다 — `-2px`로 고쳤다.
**틀린 근거로 한 교체가 두 번째 규칙(§8③의 forced-colors 폴백)을 필요하게 만든 형태다.**

**그리고 리뷰가 문서에서 잡은 둘도 여기 적는다:** §5의 미채택 표에 ▪17이 빠져 있었고(위에서 추가),
`ui-spec` §0.2·§0.4·§2.3이 **E-36 §2가 지목한 바로 그 절인데 이번 라운드에 개정되지 않았다.**
§0.2는 지금 *"`var(--shadow-inset)`가 0이면 스킨이 적용되지 않은 것"* 이라고 적는데 §4가 기록한
이름 대체 때문에 0이 정답이고 검사가 그것을 단언한다 — **명세와 검사가 같은 grep에 대해 정반대를
지시하는 상태**였다. §1을 다시 쓰면서 지목되지 않은 절만 고치고 지목된 절을 남긴 것이므로,
같은 세션 안에서 개정했다.

## E-43 — 종이 질감을 두 배로 올린다. 그리고 **마커는 종이를 받지 않는다** (사용자, 2026-08-08) — BINDING

**출처.** 사용자가 종이 질감이 "하나도 안 들어갔다"고 했다. 측정해 보니 들어가 있었다 — 그때의
강도에서 라이브러리 지면은 크림 234에서 **227.2로** 내려앉고 10단계 폭으로 흔들리고 있었다
(첫 측정에서 인용한 225 / 210~241은 화면 전체를 잰 값이라 콘텐츠가 섞여 있었다. §1 표의 값이
평평한 지면만 잰 것이고 그쪽이 맞다). **다만 눈에 "종이"로 읽히기엔 약했다.**
`--paper-intensity: 0.5`는 프로토타입의 값을 그대로 가져온 것이고, 이 제품에서 그 값이 옳은지는
아무도 물어본 적이 없다.

### 1. 판정 — 강도는 **1**이다. 네 단계를 실제로 렌더해 고른 값이다

숫자로 고르지 않았다. 소스를 고치지 않고 런타임에 값만 갈아끼워 **같은 화면을 네 강도로 렌더**하고,
평평한 지면을 4배 확대해 나란히 놓고 골랐다.

| | intensity / 진폭 | 지면 평균 | 흔들림 폭 | 눈에 |
|---|---|---|---|---|
| A (그때까지) | 0.5 / .126 | 227.2 | 10 | 거의 매끈. 알갱이가 개별로 안 보인다 |
| B | 0.75 / .126 | 224.1 | 16 | 고운 모래가 **처음으로 보인다** |
| **C (채택)** | **1.0 / .126** | **221.0** | **21** | 명확히 거칠다. 알갱이가 분간되고 지면이 한 톤 내려앉는다 |
| D | 1.0 / **.20** | 213.7 | 34 | 갱지. **질감을 넘어 잡티**에 가깝고 크림이 탁해진다 |

**모델이 실제 렌더와 맞는다는 것도 이 라운드에 확인했다.** 찍힌 픽셀에서 역산한 합성 알파는
0.0310 / 0.0466 / 0.0620 / 0.0989로, `tokens.test.ts`의 산술이 내는 0.0315 / 0.0473 / 0.0630 / 0.1000과
**2% 이내**로 일치한다. 이 파일의 대비 수치는 이론이 아니라 화면에 나오는 값이다.

**다크 테마는 이 판정에 거의 반응하지 않는다 — 그 사실을 적어 둔다.** 씻는 색이 근검정이라 이미 어두운
지면에는 더 어둡게 할 여지가 없다: 다크 라이브러리는 네 단계에서 평균 36.2 → 34.6, 편차 0.43 → 0.90,
뷰어 바는 36.9 → 34.9다. **종이결은 본질적으로 라이트 테마의 효과다.** 다크에서도 질감을 원한다면
움직여야 하는 것은 강도가 아니라 `--paper-tone`이고, 그것은 별개의 판정이다(항목으로 연다).

### 2. 마커는 종이를 받지 않는다 — `--color-hot`을 움직이는 대신

강도를 올리자 세 쌍이 AA 아래로 갔다. 둘은 토큰을 옮겨 되찾았다:

| 쌍 | 전 | 후 |
|---|---|---|
| 다크 `--ink-faint` on `--color-surface` | `#A9BAB6` — washed **4.42** | `#AFC0BC` — **4.71** (peak 4.43) |
| 다크 `--ink-th` on `--color-surface` | α .72 — washed **4.45** | α **.75** — **4.68** (peak 4.39) |

**셋째는 토큰으로 고칠 수 없다.** `--on-hot`은 **이미 순검정**이고, 그것이 이 팔레트의 절대 천장이다
(E-35가 이미 그 자리까지 밀어 올렸다 — 밝은 잉크는 순백조차 4.20이라 애초에 못 간다). 씻기면 4.46이다.
길은 둘뿐이었다.

- **`--color-hot`의 상대 휘도를 올린다** — 실측으로는 **+2.0%면 4.533**이라 충분하다(이 저장소가
  여러 곳에 적어 온 "~4%"는 강도 0.5 시절의 값이고, 그 자리들도 이 판정과 함께 정정했다).
  그래도 E-32 §1이 못박은 은퇴한 브랜드 레드를 바꾸는 것이고,
  **그것은 텍스처의 부수 효과가 아니라 그 자체로 판정**이다(`tokens.test.ts`가 이미 그렇게 적어 두었다).
- **마커가 씻김을 받지 않는다.**

**후자를 택한다. 근거는 이 파일에 이미 있는 선례다:** 읽기 무대는 *"그림은 인쇄할 종이가 아니다"* 라는
사용자 판정으로 그레인에서 면제돼 있다. 같은 모양으로, **장식으로 얹은 텍스처가 AA 바닥을 넘기는
주체가 되어서는 안 된다.**

**예외의 범위를 정확히 적어 둔다 — "마커는 종이를 받지 않는다"는 일반론이 아니다.** 면제되는 것은
**AA 텍스트 쌍을 지고 있으면서 그 쌍을 옮길 잉크가 남아 있지 않은 원소** 하나다. 같은 바 안의 다른 hot
마커 — `.seg-opt` 선택 옵션의 inset 링 — 은 **여전히 씻긴다**(리뷰가 픽셀로 확인했다: 그레인을 끄면
그 링이 정확한 hot 988픽셀, 켜면 0픽셀). 그 링은 텍스트가 아니고, 3:1 미달은 E-42 §1이 이미 근거와
함께 수용했다. 다음 사람이 이 절을 "hot이면 종이를 안 받는다"로 읽지 않도록 못박는다.

구현은 한 규칙이다. 그레인은 바의 `::after`가 `--z-texture`에 그리는 것이므로, 칩을 그보다 한 단 위에
그리면 씻김 다음에 그려진다 —
`[data-role='viewer-override-chip'] { position: relative; z-index: calc(var(--z-texture) + 1) }`.
새 색도, 새 토큰도 필요 없다.

**예외는 검사가 지킨다 — 양방향으로.** `tokens.test.ts`의
`lifts the hot marker out of the wash rather than moving the brand red`가 ① 이 쌍이 씻기면 **실제로
4.5 아래**라는 것(예외가 필요하다는 증거)과 ② 마르면 4.5 이상이라는 것, 그리고 ③ `base.css`에 그
규칙이 **실재한다**는 것을 함께 단언한다. 규칙을 지우면 빨개지는 것을 뮤테이션으로 확인했다.
반대로 강도가 낮아져 씻긴 값이 4.5를 넘으면 ①이 실패한다 — **예외가 불필요해진 순간 누군가 지우라고
말하는 검사**다. painted-pair 스캐너도 이 한 쌍만 dry로 읽되, 그 이유를 예외 주석에 적고 나머지는
그대로 washed로 잰다.

### 3. 값을 치른 것을 적어 둔다 — peak는 전부 4.5 아래다

이 판정 이후 재유도한 네 쌍의 washed 값은 **평균 4.61~4.76 / 피크 4.39~4.46**이다. **피크가 전부
바닥 아래**이고, 그것이 사용자가 고른 강도의 값이다. 이 저장소는 피크를 **보고하되 게이트로 걸지
않는다** — 그 이유 셋(WCAG는 지정 색에 정의된다 / 피크는 무작위장의 극값이라 정의에 따라 흔들린다 /
11px 글리프에서는 안티에일리어싱이 지배한다)은 `tokens.test.ts`에 그대로 서 있고 이 판정이 뒤집지
않는다. 다만 **A에서는 피크가 4.5 언저리였고 지금은 전부 그 아래**라는 사실은 숨기지 않는다.

### 4. 이 판정이 뒤집지 않는 것

E-32 §1의 **hot은 마커이고 브랜드 색이 아니다**는 그대로다 — 이 판정은 오히려 그 값을 **지키기 위해**
예외를 골랐다. E-35의 읽기 무대 면제도 그대로이고, 이 예외는 그것의 확장이지 대체가 아니다.
E-42의 절대 컨트롤 토큰은 강도 1에서도 여유가 크다(`--on-control-dim` 5.71, `--on-control` 10.44).

## E-44 — 맞춤 `화면`이 돌아온다. E-27 §1을 **절반** 뒤집는다 — UI와 강제변환은 되돌리고, 계약과 기하는 손대지 않는다 (사용자, 2026-08-09) — BINDING

**출처.** 사용자가 디자인 프로토타입에 네 번째 옵션 **`화면`을 되돌려 놓고** 그것을 구현하라고 지시했다.
E-27 §1이 그 옵션을 지운 근거는 제품 논리가 아니라 **출처**였다 — *"프로토타입이 네 번째 옵션
`fitS`(화면)를 삭제했다"*(`:605`). 그 판정은 화면이 중복이라거나 아무도 안 쓴다고 주장한 적이 **없다.**
같은 종류의 증거가 반대 방향을 가리키게 됐으므로, 판정도 같은 방향으로 간다.

### 1. E-27 §1의 네 행 중 **둘**을 뒤집는다. 나머지 둘은 그대로 선다

| E-27 §1의 행 | E-44 |
|---|---|
| **UI** — *"뷰어 상단 바의 맞춤 `.seg`는 **너비 · 높이 · 원본** 세 개다"*(`:610`) | **뒤집는다.** 네 개다 — **너비 · 높이 · 화면 · 원본**. **화면이 셋째**이고, 아이콘은 lucide **`Maximize`**다(`features/viewer/ViewerTopBar.tsx`의 `FIT_OPTIONS`. 나머지 셋은 `MoveHorizontal` / `MoveVertical` / `Image`) |
| **기존 데이터** — *"`contain`으로 저장된 책은 **'높이'로 열린다**"*(`:612`) | **뒤집는다.** `store/viewer.ts`의 `openingFit`에서 강제변환을 없앤다. `contain`으로 저장된 책은 **`contain`으로 열린다** |
| **계약** — *"`arch-backend.md` §7은 바뀌지 않는다"*(`:611`) | **그대로.** 이 판정도 §7을 건드리지 않는다 |
| **기하** — *"`fit.ts`의 `contain` 기하는 그대로 두고 그대로 테스트한다 … 사라진 것은 도달 경로지 계산이 아니다"*(`:613`) | **그대로.** 새로 유도할 기하가 없다 |

**아래 두 행이 이 판정을 싸게 만든 것이고, 그것은 E-27이 의도한 것이다.** 계약 쪽은
`arch-backend.md` `:407`·`:726`·`:1559`가 네 값을 내내 적고 있었고
`internal/httpapi/api_test.go:832-846`이 `PUT /api/books/{id}/prefs`에 `{"fit_mode":"contain"}` →
**200**을 계속 단언해 왔다. 기하 쪽은 `web/src/features/viewer/fit.test.ts:185,201,210,225,247`이
`contain`을 계속 재고 있었다. **되살릴 것은 없고 다시 이어 붙일 것만 있다** — 제품 코드에서 움직이는
자리는 `FIT_OPTIONS`의 항목 하나와 `openingFit`의 강제변환 한 줄, **둘뿐이다.**

**셋째라는 위치는 실측이고, 저장소의 사본들과 어긋난다 — 어긋나는 쪽이 낡은 것이다.** 근거는
2026-08-09에 라이브 디자인 프로젝트에서 직접 받은 `만화방 v2 soft.dc.html`이고, 그 맞춤 `.seg`는
`isFitW · isFitH · **isFitS** · isFitO`를 낸다. **화면은 원래 넷째였다** — `docs/ui-html/…프로토타입.zip`
(2026-07-28)의 세 파일과 `docs/ui-shots/viewer-overlay-visible-1440.png`, `design.md` 화면 3은 전부
`너비 · 높이 · 원본 · 화면`이고, 그것이 E-27 이전의 배치다. 즉 사용자는 옵션을 되돌리면서 **자리도
옮겼고**, 저장소의 사본은 이제 두 번 낡았다(옵션이 없던 것으로 한 번, 자리로 또 한 번).
**순서는 장식이 아니라 키보드 매핑이다.** `Seg`에는 keydown 핸들러가 **하나도 없다** — 옵션들이
`name`을 공유하는 진짜 라디오 그룹이라(`components/ds/Seg.tsx:57-61`, 의도는 `:8-9`에 적혀 있다)
탭 스톱 하나와 ←/→ 순회가 **브라우저에서 온다.** 그래서 배열 순서가 곧 키 순서다.
캡처를 보고 넷째로 되돌리는 것은 판정 없이 키 매핑을 바꾸는 일이다.
**그리고 그 순회는 이 저장소의 어느 티어도 시험한 적이 없다**(15세션차 리뷰가 확인) — 네 번째
라디오가 키보드로 닿지 않아도 게이트는 전부 초록이다. 이 판정이 넓힌 공백이므로 같은 라운드에서 닫는다.

### 2. 강제변환은 컨트롤 부재의 **따름정리**였다. 전제가 사라지면 정리도 사라진다

E-27이 강제변환을 넣은 이유는 `contain`이라는 값에 대한 판단이 **아니었다.** 그 판정이 스스로 적은
이유는 이것 하나다: *"컨트롤만 지우면 `contain`으로 저장된 독자는 **자기가 어느 맞춤에 있는지 볼 수도
없고 거기서 빠져나올 수도 없다** — 어떤 라디오도 선택돼 있지 않은 세그먼트를 보게 된다"*(`:615-616`).
컨트롤이 돌아오면 그 문장은 주어를 잃는다. 라디오는 켜져 보이고, 나가려면 옆의 셋 중 하나를 누르면 된다.

**남겨 두는 쪽이 이제 결함이다.** 화면 버튼이 눌리고 저장까지 되는데 다시 열면 높이로 열리는 상태 —
제품이 독자가 방금 저장한 설정을 조용히 무시하는 상태가 된다. E-27이 막으려던 것과 같은 종류의
"보이는 것과 실제가 다르다"이고, 방향만 반대다.

### 3. PRD 개정 — FR-VWR-005의 **두 번째** 개정이고, 현재는 이쪽이다

E-27 §1은 스스로 *"이것은 패치가 아니라 **PRD 개정**이다"*(`:606`)라고 적었다. 되돌리는 것도 같은 값을
치른다. **`docs/prd.md` FR-VWR-005는 다시 네 종이다** — 너비 맞춤 · 높이 맞춤 · **화면 맞춤** · 원본 크기,
우선순위 `필수`는 그대로. 그 줄과 `HANDOFF.md` §3의 우선순위 목록은 **E-27 → E-44** 순서로 이력을 달고
있어야 한다. 한 요구사항이 두 번 개정된 이상, **어느 쪽이 현재인지가 그 줄 자체에서 읽혀야 한다.**

**E-27이 틀렸다고 적지 않는다.** 가진 증거에 대해 옳았고, 계약과 기하를 일부러 남겨 이 문을 닫지
않았다 — 그 두 행이 없었다면 이 판정은 스키마와 기하를 다시 유도하는 일이었을 것이다. 바뀐 것은
판정의 품질이 아니라 그 아래의 사실이다. E-26이 D-33 / E-3에 대해 적은 문장과 같은 모양이다 —
*"Neither was wrong on the evidence it had"*(`:408`).

### 4. 이 판정이 바꾸지 않는 것

- **설정 화면.** 애초에 맞춤 컨트롤이 없다 — `web/src/features/overlays/ReadDefaultsPanel.tsx`는
  읽기 방향 · 표시 모드 · 프리페치 · 테마 넷만 낸다. E-27의 *"설정 화면에는 애초에 맞춤 컨트롤이 없어
  변경 없음"*(`:610`)은 지금도 그대로 참이다.
- **`arch-backend.md` §7.** `:407`·`:726`·`:1559`가 이미 네 값을 적고 있고 한 번도 줄어든 적이 없다.
  **이 파일은 한 줄도 고치지 않는다.**
- **기본 맞춤은 `height`다** — C-13 / 개정 **A-2**, `store/viewer.ts`의 `DEFAULT_FIT`. 옵션이 넷이
  됐다고 기본값이 움직이지 않는다.
- **Go 계층 전부.** 골든 픽스처, 마이그레이션, `contractcheck`, `internal/**` — **변경 0건.**
- **C-2.** 와이어 값은 `contain`이고 `screen`은 제품 어디에도 없다. `api_test.go:832-846`이 `screen`에
  대해 **400**을 단언하며 그것을 지킨다. 프로토타입의 식별자 `fitS`/`screen`은 이번에도 들어오지
  않는다 — 돌아오는 것은 **라벨과 도달 경로**지 이름이 아니다.

### 5. 검사 — 뒤집힌 두 행에는 뒤집힌 검사가 필요하다

5세션차의 뮤테이션 기록에 *"`contain` 강제변환을 없애면 1건 빨감"* 이 있다(`HANDOFF.md` §4.7).
**그 뮤테이션이 이제 정답이므로 그 단언은 반대 방향으로 다시 서야 한다** — `openingFit('contain')`이
`'contain'`이라는 것. 강제변환을 지우고 그 테스트만 지우면 이 판정을 지키는 것이 아무것도 남지 않는다.

컨트롤 쪽은 §6.5의 모양을 피할 것: **옵션이 넷이라는 사실은 스토어가 아니라 화면에서 단언해야 한다.**
`FIT_OPTIONS`에 항목을 넣고도 그 세그를 렌더하지 않는 화면은 스토어 테스트를 전부 통과한다 —
E-33 §2가 씨앗에 대해 겪은 그것이다.

## E-45 — `파일이 변경되었습니다`에 수명을 준다. 그리고 진행률 쓰기는 그 알림의 근거를 지우지 않는다 (사용자, 2026-08-10) — BINDING

**출처.** 15세션차가 항목 `r`을 실측했고, 16세션차 시작 시점에 사용자가 *"수명 판정 + 저장 계층 둘 다"*
를 지시했다. 인수인계의 옛 설명 *"알림이 사실상 뜨지 않는다"* 는 **틀렸다** — 알림은 **약 1초, 딱 한 번**
뜨고 그 뒤 **영구히** 안 뜬다. 실측: GET `stale:true` → PUT 응답 `stale:false` → GET `stale:false`,
기록된 `page_count` **99 → 3**.

**이 판정은 계약을 뒤집지 않는다. 비어 있던 칸을 처음 채운다.** `docs/prd.md:166`의 FR-VWR-009는
*"마지막으로 읽은 페이지를 자동 저장하고, 재진입 시 해당 페이지에서 재개해야 한다"* 뿐이고 알림을 한
글자도 규정하지 않는다. `arch-backend.md:1596-1597`은 *"the UI **may** warn"* 이라고 허가만 하고,
`ui-spec.md:1496-1503`은 **배치만** 정하며(크롬 있을 때 같은 컬럼의 행, 없을 때 `top:56px` 오버레이),
`impl-plan.md:687`은 *"a one-line 힌트"* 라고만 적는다. **네 문서 중 어느 것도 이 알림이 얼마나
오래 떠 있어야 하는지 말한 적이 없다.** 그 공백이 곧 결함의 서식지였다.

### 1. 수명 — **3400 ms, 진입당 한 번.** 여는 힌트와 **같은 계약**이다

뷰어에는 한 줄 알림이 둘 있다. 하나(여는 힌트)에는 쓰인 수명이 있고 하나(변경 알림)에는 없다.
같은 화면의 같은 형태에 규칙이 둘일 이유가 없으므로, **있는 쪽을 없는 쪽에 그대로 적용한다.**

`ui-spec.md:1300`이 여는 힌트에 대해 적은 것: *"it is **timed, not dismissible** — **3400 ms**
(`CHROME_HINT_MS`, `store/viewer.ts:31`) — because a hint that has to be closed is a second thing to
learn"*, 그리고 `:1301-1303` *"armed **once per entry**"*, 다음 권은 진입이 아니라 **연속**(E-28 §3).

**변경 알림도 같다:** 타이머로 사라지고, 닫기 버튼은 없다. ~~그리고 **책 진입당 한 번** 무장된다.
연속(같은 화면에서 다음 권으로 넘어가는 것)은 진입이 아니다 — `store/viewer.ts:254`의 `continuing`
판정을 **그대로 재사용한다.** 새 판정 기준을 만들지 않는다.~~

> **REVISION 2026-08-10 — 위 두 문장은 틀렸다. 무장 단위는 진입이 아니라 책이다.**
> *(같은 날 교차 리뷰가 찾았다. 취소선 부분이 원문이고, 아래가 현재다.)*
>
> **두 알림은 서로 다른 것을 묻는다.** 여는 힌트는 *"이 독자에게 이미 인사했는가"* 이고, 그래서
> 다음 권으로 이어지는 것이 옳다(E-28 §3). **변경 알림은 *"이 파일이 바뀌었는가"* 이고, 다음 권은
> 다른 파일이다.** 같은 기준을 쓸 수 없다. *"새 판정 기준을 만들지 않는다"* 는 원문의 절약이
> 여기서는 **두 결함을 동시에 만들었다.**
>
> 1. **승인이 엉뚱한 책으로 나간다.** `open()`이 `continuing`일 때 `clearStale()`을 부르지 않으므로
>    1권에서 무장된 타이머가 2권에서도 살아 있다가 `staleSeen`을 세우고, 화면의 승인 이펙트는 그것을
>    **지금의 `bookId`(=2권)** 로 승인한다(`useProgressSync`가 현재 권에 묶여 있다).
>    **2권의 기준선이, 독자가 본 적도 없는 경고에 대해 소각된다** — E-45 §2가 막으려던 그 파괴가
>    다른 책에서 재생산된다. 평범한 경로다: 1권 재개 → 경고 → 3.4초 안에 `다음 권 읽기`.
>    실측된 PUT: `{"bid":"nextbook…","body":{"page":1,"stale_seen":true}}`.
> 2. **정작 2권이 변경된 책이어도 경고가 안 뜬다.** 연속을 "진입이 아님"으로 친 결과다.
>
> **그러므로 stale 래치는 책마다 다시 평가한다.** `open()`은 `continuing` 여부와 **무관하게**
> 이전 책의 stale 타이머를 지우고 새 책의 `stale`로 다시 무장한다. 여는 힌트의 `continuing` 처리는
> **그대로 둔다** — 그 알림에는 원래 맞는 기준이었다.
>
> **집안 패턴도 다른 쪽이 맞았다.** §1이 인용한 둘 중 옳은 것은 `continuing`이 아니라
> **`ViewerPage.tsx:264`의 `openedRef`** — 불리언 래치가 아니라 **"마지막으로 연 `bookId`" 값 래치**이고,
> `bookId`가 바뀌면 비교가 자동으로 실패해 **책당 한 번이 공짜로 따라온다.**
>
> **그리고 승인은 무장된 책에 묶인다.** 래치 옆에 그때의 `bookId`를 함께 들고, 승인은
> `staleSeen && staleBookId === bookId`일 때만 나간다. 위 수정만으로도 타이머가 남의 책에서 발화할 수
> 없게 되지만, **그것은 두 파일이 협력해야 성립하는 불변식이고 이 판정은 그 불변식을 한 자리에서
> 단언할 수 있기를 요구한다.** 승인이 어느 책에 대한 것인지는 승인 자신이 알아야 한다.

**수명은 파생값이 아니라 상태여야 한다.** 지금 `ViewerPage.tsx:715`의 `stale`은
`detail?.progress?.stale === true`, 즉 React Query 캐시의 **매 렌더 파생값**이다. 그래서
`useSaveProgress`의 `onSuccess`(`queries.ts:436-438`)가 PUT 응답의 `progress` 객체로 캐시를 통째로
덮는 순간 알림이 언마운트된다. **현재의 "약 1초"는 설계된 수명이 아니라 저장 경로의 부작용이다.**
`hintVisible`이 스토어 상태라 서버 데이터와 무관하게 사는 것과 대조된다(`store/viewer.ts:92`).
그러므로 **stale을 래치된 상태로 승격한다** — 모듈 스코프 타이머 + `clear` 헬퍼 + `dismiss` 액션,
`:173`·`:198-203`·`:397-400`과 같은 모양으로.

**`role="status"`를 붙인다.** 여는 힌트에는 있고(`ViewerPage.tsx:824`) 이 알림에는 없다(`:874`).
스스로 사라지는 알림에 라이브 리전이 없으면 스크린리더 사용자에게는 **수명이 0**이다.

### 2. 저장 — **기준선은 승인 없이 덮이지 않는다.** 파괴 지점은 두 줄이다

| 줄 | 지금 | E-45 |
|---|---|---|
| `internal/httpapi/progress.go:57` | 서버가 **인덱스의 현재** `page_count`를 넘긴다. 클라이언트는 이 값을 보낼 수 없다(`progressUpdateBody`는 `page`/`completed` 둘뿐, `DisallowUnknownFields`) | **그대로 넘긴다** — 클램프와 완료 판정에는 현재 길이가 맞다 |
| `internal/userdata/progress.go:132` | `page_count = excluded.page_count` — 매 쓰기가 기준선을 현재값으로 리셋한다 | **승인이 실린 쓰기에서만** 덮는다 |

**이 파일은 "덮지 않는 컬럼"을 이미 안다.** 같은 UPSERT에서 `started_at`은 일부러 갱신하지
않는다(`progress.go:102`). `page_count`도 같은 종류의 컬럼이었어야 했다.

**문서의 의도도 처음부터 기준선이었다.** `arch-backend.md:505`(§3.4): *"it is why `progress` also
stores `page_count` **so the UI can show a stale-progress hint if the count changed**"*, 그리고
`:2852`(§12.2)가 위험 완화책으로 같은 것을 적는다. **구현이 그 기준선을 매 쓰기마다 지우고 있었다.**

**승인의 모양 — `stale_seen`.** `progressUpdateBody`(`dto.go:516-522`)에 선택 필드 하나를 더한다:

```go
type progressUpdateBody struct {
	Page      *int  `json:"page"`
	Completed *bool `json:"completed"`
	StaleSeen *bool `json:"stale_seen"` // 독자가 변경 알림을 보았다 ⇒ 기준선을 현재 길이로 다시 잡는다
}
```

클라이언트는 **알림이 수명을 다 채웠을 때만** `true`를 보낸다. 페이지를 넘겼다는 것은 승인이 아니다 —
`useProgressSync.ts:48-51`의 자동 쓰기는 독자가 아무것도 하지 않아도 나가기 때문이다.
**독자가 1초 만에 탭을 닫으면 승인은 나가지 않고, 기준선이 살아남아 다음 진입에서 다시 경고한다.**
그것이 옳은 결말이다.

**암묵적 승인을 쓰지 않는 이유.** "페이지가 실제로 움직였으면 승인으로 친다" 같은 휴리스틱은 이 저장소가
반복해서 대가를 치른 §6.5의 모양이다 — 검사도 계약도 **알림이 보였는지**가 아니라 **그 옆의 무언가**를
보게 된다. 승인은 승인이라고 말하는 필드로 실린다.

**승인은 길이를 아는 경우에만 기준선을 옮긴다 — `page_count > 0`일 때만.**
*(2026-08-10 교차 리뷰가 찾은 구멍. 위 문단만으로는 이 경로가 열려 있었다.)*

책이 깨지면 스캐너가 인덱스 행을 `status='error', page_count=0`으로 둔다
(`internal/scanner/scanner.go:1157-1165`). 기록이 99인 독자에게 `isStale(99, 0)`은 **true**이므로
경고가 뜬다. 그런데 그 승인이 현재 길이를 무조건 기록하면 **기준선이 0이 되고**, `isStale(0, n)`은
**모든 `n`에 대해 false**다(§3의 "기록된 0은 stale이 아니다"). **책이 고쳐져 99쪽으로 돌아와도 경고는
영영 뜨지 않는다.** 실측: `index length → 0` 에서 `stale=true` → 승인 → `repaired to 7`에서도
`page_count=0 stale=false`. 회복 수단은 `DELETE /progress`나 임포트뿐이다.

**이것은 E-45가 고친 결함과 같은 종류다 — 쓰기가 자기 경고의 근거를 지운다.** 범위만 좁다.

**그러므로 승인은 `page_count > 0`일 때만 기준선을 옮긴다.** 알 수 없는 길이는 승인할 수 없다 —
독자가 본 것은 "파일이 바뀌었다"이지 "이 책은 이제 0쪽이다"가 아니다. 깨진 동안에는 승인이 붙지 않고,
고쳐지면 그때 옛 기준선(99)과 새 길이가 정직하게 비교된다. **와이어의 `page_count = 0`이 "길이 미상"
(arch §4.11) 하나만 뜻하도록 지키는 일이기도 하다** — 승인이 0을 쓸 수 있으면 그 값이 "독자가 길이 0을
승인함"이라는 둘째 뜻을 겸하게 되고, `arch-backend.md:1602`의 한 줄 주석은 그 상태를 설명하지 못한다.

**그리고 그 위에, 뿌리를 끊는다 — `isStale`은 대칭이다. 어느 쪽이든 0이면 stale이 아니다.**

두 리뷰가 같은 구멍의 양쪽 끝을 각각 찾았다: 서버 쪽은 *"승인이 기준선을 0으로 만든다"*, 화면 쪽은
*"깨진 책이 진입할 때마다 영원히 경고하고 승인은 원리적으로 불가능하다"*(`progressReady`가
`pageCount > 0`을 요구하므로 진행률 훅 자체가 꺼져 있다). **둘 다 증상이고, 원인은 하나다 —
`isStale(99, 0)`이 true인 것.**

**`isStale`의 주석이 이미 답을 적어 두고 있었다. 한쪽에만 적용했을 뿐이다.**
`convert.go:129-133`은 기록된 0에 대해 이렇게 말한다 — *"A recorded 0 … means the book had no known
length … and calling that 'the file changed' would **put a warning on the screen for a condition the user
cannot act on**"*. **깨진 책이 정확히 그 조건이다.** 그 화면은 이미 *"열 수 없는 파일"* 을 말하고 있고,
독자에게는 재개할 자리조차 없다. 거기에 *"저장된 자리가 옮겨졌을 수 있다"* 를 얹는 것은
**그 주석이 금지한 바로 그 일**이다.

**그러므로 `isStale`은 `recorded == 0 || current == 0`이면 false다.** 오염 판정은 **두 길이를 다 알 때만**
성립한다. 이 하나로 위의 두 증상이 함께 사라진다 — 경고가 안 뜨므로 승인도 나갈 일이 없고, 책이
7쪽으로 복구되면 그때 `isStale(99, 7)`이 true가 되어 정직하게 경고한다. **승인의 `page_count > 0`
게이팅은 그래도 남긴다** — 손으로 만든 API 호출은 화면을 거치지 않으므로, 계약이 스스로를 지켜야 한다.

**계약 영향.** `arch-backend.md`의 §7.3 `stale` 주석과 §3.6 DDL 주석이 "현재 0"의 경우를 말하도록
개정한다(`A-14`에 포함). **골든은 여전히 변경 0건** — 36개 중 `stale: true`가 하나도 없고, 이 변경은
true를 false로 바꾸는 방향뿐이다.

### 3. 이 판정이 바꾸지 않는 것

- **`isStale`(`convert.go:134-139`).** 판정 기준은 그대로다 — 기록된 길이 ≠ 현재 길이. 기록된 0은
  여전히 stale이 아니다(`status != "ok"` 책, arch §4.11). **고치는 것은 기준의 정의가 아니라 기준선의 수명이다.**
- **스키마.** 컬럼을 더하지 않는다. `arch-backend.md:834-838`(§3.6 규칙 8)이 마이그레이션 rung을
  `CREATE TABLE/INDEX IF NOT EXISTS`로만 제한하는데, **이 판정은 그 규칙을 건드릴 필요가 없다.**
  `schemaVersion`은 2에 머문다.
- **클램프와 자동 완료.** `[1, page_count]` 클램프와 `page === page_count ⇒ completed`(FR-VWR-012,
  arch §7.6)는 **인덱스의 현재 길이**로 계속 계산한다. 독자는 190쪽짜리가 된 파일의 190쪽에 갈 수 있어야
  한다. 보존되는 것은 **기록되는 컬럼 하나**지 계산에 쓰는 값이 아니다.
- **임포트(`export.go:161-172`).** 여기의 `excluded.page_count`는 인덱스가 아니라 **내보낸 파일이
  실어 온 기준선**이다(`ExportItem`, `export.go:22-33`). 이미 옳으므로 **한 줄도 고치지 않는다.**
  같은 모양의 SQL이라는 이유로 함께 고치지 말 것.
- **E-27의 배치 판정.** stale은 크롬에 게이팅되지 않는다(`decisions.md:637-638`). 수명이 생겨도
  크롬 가시성과는 여전히 무관하다.
- **골든 픽스처.** `progress.json`이 나오는 시나리오는 길이가 안 바뀐 책이라 `stale`은 계속 `false`다.
  **36개 골든 중 변경되는 것은 없다.**

### 4. 검사 — 지금 이 결함을 지키는 검사가 하나도 없다. 그중 하나는 이름으로 지킨다고 말한다

| 티어 | 지금 |
|---|---|
| Go 단위 | `api_test.go:782-793` `TestProgress_staleWhenTheIndexLengthMoved` — **순수 함수 `isStale`만 호출하고 HTTP를 한 번도 거치지 않는다.** 그래서 PUT이 기준선을 지우는 것을 원리적으로 못 본다 |
| 골든 | 36개 중 `stale: true`인 것이 **0건** |
| `contractcheck` | 요청 바디를 **의도적으로 검사하지 않는다**(`scripts/contractcheck/main.go:48-53`). `stale_seen`은 이 검사가 볼 수 없는 자리에 있다 — 알고 넣는다 |
| vitest | `ViewerPage.test.tsx:1663` `'warns **once** when the recorded progress no longer matches the file'` — **이름이 once라고 말하지만 `getByText`가 있다는 것만 단언한다.** 타이머도, 사라짐도, 재진입도 재지 않는다. 1초 디바운스가 끝나기 전에 단언이 끝나서 통과한다 |
| e2e | `web/e2e/` 전체에서 이 알림을 단언하는 것이 **0건** |

**그러므로 이 판정은 다음 넷을 함께 요구한다.**

1. **Go 핸들러 테스트** — 인덱스 길이를 움직인 뒤 PUT을 보내고, 응답과 그 다음 GET이 **여전히
   `stale:true`** 인 것. 그리고 `stale_seen:true`를 보낸 뒤에야 `false`가 되는 것. 순수 함수가 아니라
   **HTTP를 지나야 한다.**
2. **vitest** — 가짜 타이머로 **3400 ms**를 실제로 지나 보내고 사라지는 것, 그 전에는 PUT 성공이
   캐시를 덮어도 **살아남는** 것, 재진입에서 다시 뜨지 않는 것. `:1663`의 이름값을 그 테스트가 갚는다.
3. **e2e — 그리고 이것이 `stale_seen` 이음매를 지키는 **유일한** 검사다.** 여는 힌트의
   `09-viewer-chrome.spec.ts:540-575`가 본이다. 거기에는 `CHROME_HINT_MS` 미러 상수(`:76`)와 하한 근거
   `HINT_FLOOR_MS`(`:90-97`)가 이미 있다. **같은 모양으로 세운다.**

   > **왜 유일한가 (2026-08-10 교차 리뷰).** `contractcheck`는 요청 바디를 안 보고, Go 테스트는
   > *"서버가 `stale_seen`을 받는다"* 만, vitest는 *"클라이언트가 `stale_seen`을 보낸다"* 만 고정한다.
   > **둘을 비교하는 것이 없다.** Go 쪽 태그를 `staleSeen`으로 개명하고 `api_test.go`를 같이 고치면
   > **다섯 게이트가 전부 초록**인데, 실제 승인 PUT은 `DisallowUnknownFields`(`params.go:108`)에 걸려
   > **400**이 되고 경고는 영원히 안 사라진다. 반대 방향(TS가 필드를 조용히 누락)은 200이 나와서 더
   > 조용하다. **양쪽을 각각 지키는 검사 둘이 있다는 것은 이음매를 지킨다는 뜻이 아니다** — §6.5의
   > 그 행 그대로다. 그러므로 이 e2e는 "알림이 뜬다"만이 아니라 **승인이 실제로 먹혔는지**(수명 뒤
   > 서버가 `stale:false`를 준다)를 단언해야 한다. 그것이 이음매 검사다.
4. **뮤테이션** — `page_count` 보존을 되돌리면 위 1이 빨개져야 하고, 래치를 되돌리면 위 2가 빨개져야 한다.
   **되돌렸는데 초록이면 그 테스트는 이 판정을 지키지 않는 것이다.**

### 5. 문서 개정 — `A-14`

- **`arch-backend.md:710-711`(§3.6 DDL 주석)의 `-- as of the last write`.** 이 문구는 **현재 구현과
  문자적으로 일치하므로**, 고치지 않으면 문서가 결함을 계속 승인한다. `-- as of the last acknowledged
  write` 쪽으로 간다. **같은 문구가 §7.3에도 한 번 더 있다** — `:1593`의
  `// as recorded when progress was written`. 두 곳 다 고친다. 한 곳만 고치면 낡은 사본이 남아
  다음 세션이 그것을 근거로 쓴다.
- **`arch-backend.md:2221-2237`(§7.6)** — `ProgressUpdate`에 `stale_seen?: boolean`과 그 의미,
  그리고 "승인 없는 쓰기는 `page_count`를 보존한다"는 저장 규칙을 명시한다.
- **`ui-spec.md:1496-1503`** — 배치 문단 아래에 수명 문단을 `:1300`과 **같은 모양**으로 붙인다.
- `docs/prd.md`는 **고치지 않는다.** FR-VWR-009는 이어보기 요구사항이고, 이 알림은 그 요구사항의
  구현 세부다. PRD 줄을 늘리는 것은 E-44가 FR-VWR-005에 한 것과 달리 여기서는 필요하지 않다.

### 6. 따름정리 — **`progress.page_count`는 분모가 아니다.** 겸직이 드러났으므로 갈라놓는다

**16세션차 구현 중에 발견됐고, 사용자가 같은 라운드에서 고치라고 지시했다.**

`progress.page_count`는 두 일을 겸하고 있었다:

1. **오염 감지의 기준선** — `isStale`이 현재 길이와 비교하는 값.
2. **"얼마나 왔나"의 분모** — 화면이 `last_page / page_count`로 비율과 카운터를 만드는 값.

**E-45 §2 이전에는 이 겸직이 보이지 않았다.** 매 쓰기가 기준선을 현재 길이로 리셋했으므로 두 값이 항상
같았기 때문이다. 기준선을 보존하는 순간 둘이 갈라지고, **분모 자리에 옛 길이가 들어간다.**

**분모는 인덱스의 현재 길이여야 한다.** 파일이 10쪽에서 190쪽으로 늘었고 독자가 10쪽까지 읽었다면
그는 100%가 아니라 **5%** 다. 반대로 190쪽이 10쪽으로 줄어 독자가 10쪽으로 클램프됐다면 그는 100%다.
**두 방향 모두 현재 길이가 옳은 답을 준다.** 옛 길이는 어느 쪽에서도 옳지 않다.

| 자리 | 지금 | E-45 |
|---|---|---|
| `web/src/features/series/volume.ts:80-86` `volumeProgressRatio` | `progress.last_page / progress.page_count` | 분모를 **`book.page_count`** 로. 이 함수는 **이미 `book: BookSummary`를 받는다** — 인자를 늘릴 필요가 없다 |
| `web/src/features/library/ContinueCard.tsx:44` | `const total = item.progress.page_count` | `item.book.page_count`. `ContinueItem.book`은 `BookSummary`다(`types.ts:481`) |
| `web/src/features/overlays/CommandPalette.tsx:97` | `formatContinueCounter(item.progress.last_page, item.progress.page_count)` | 같은 자리에 `item.book.page_count` |

**`Math.min(1, …)` 클램프가 이미 있어서 터지지는 않았다**(`volume.ts:85`). 그래서 이것은 크래시가 아니라
**조용히 틀린 숫자**였고, 기준선이 리셋되던 동안에는 우연히 옳았다. §6.5의 그 모양이다 — 검사도 화면도
"분모가 무엇인가"를 물은 적이 없다.

**계약은 바뀌지 않는다.** `Progress.page_count`는 와이어에 그대로 남고 의미도 그대로다(승인된 쓰기
시점의 기록된 길이). 바뀌는 것은 **화면이 어느 값을 분모로 고르는가**뿐이다.

**검사.** `volume.test.ts:85`는 `42/187`을 기대하는데 픽스처의 책 길이와 기록된 길이가 **둘 다 187**이라
분모를 바꿔도 통과한다 — **그 테스트는 이 구분을 지키지 못한다.** 둘이 **다른** 픽스처를 세워
현재 길이 쪽이 나오는 것을 단언해야 한다.

**따름정리의 따름정리 — `완독`이라는 낱말은 비율이 아니라 `completed`를 따른다.**
*(2026-08-10 교차 리뷰가 찾았다. §6의 분모 교정이 새로 도달 가능하게 만든 상태다.)*

190쪽이 10쪽으로 줄고 독자가 10쪽으로 클램프되면 비율은 **1.0**이 된다 — §6이 옳다고 판정한 값이다.
그런데 그 1.0이 **낱말**까지 끌고 간다: `volumeStateLabel`이 **`완독`** 을 내고, `VolumeRow.tsx:246`의
`isTerminalState('완독')`이 참이 되어 **터미널 상태 배지**로 그려지며 진행바가 `tone='done'`이 된다.
같은 행의 버튼은 여전히 **`읽음 표시`** 다(`progress.completed`가 `false`이므로). 그리드 쪽은 또 다르다 —
`VolumeTile`은 `volumeTone`이 `started`라 **꽉 찬 진행바**를 그리는데, **진짜 완독 볼륨에는 바가 아예
없다**(`:115`). 한 책이 리스트에서는 "완독", 그리드에서는 "읽는 중", 버튼으로는 "아직 안 읽음"이 된다.

**§6은 비율만 판정했고 낱말·색조·컨트롤의 정합은 말한 적이 없다.** 이것은 **E-12**가 겨냥한
"상태와 액션을 혼동시키지 말 것"이다.

**그러므로: `completed`가 거짓인 책은 어떤 표면에서도 `완독`으로 읽히지 않는다.** 비율 1.0은 그대로
두되(정직한 값이다) 터미널 낱말과 터미널 배지는 `progress.completed`에만 반응한다. 셋이 —
**라벨 · 색조 · 버튼** — 한 화면에서 서로 모순되지 않는 것이 이 판정이 요구하는 불변식이고,
`VolumeRow`와 `VolumeTile` **양쪽에서** 단언되어야 한다.

**새 테스트가 이 어긋남을 침묵으로 승인했다.** `volume.test.ts:126-133`은 `완독`만 단언하고 tone은
단언하지 않는다 — **바로 위 두 케이스에서는 tone을 단언하는데 여기만 빠졌다.** 검사가 결함을 통과시킨
것이 아니라, 검사가 결함을 **기록**한 것이다.

## E-46 — **서고 스킨을 전면 채택한다.** 제품명은 `석교만화방`, 서체는 명조, 인주는 액센트이자 마커다 (사용자, 2026-08-21) — BINDING

**출처.** 사용자가 Claude Design 프로젝트의 `만화방 v3 서고.dc.html`을 지목하며 *"이 디자인을 반영해
주세요. 오직 디자인만 반영해주세여. 기능은 현재 구현을 그대로 사용하여 주세요"* 라고 지시했고, 이어서
폰트(고운바탕 Regular/Bold)와 제품명(`석교만화방`)을 지정했다. 다크 램프의 처리는 그 자리에서 물어
**"어두운 종이로 새로 유도"** 로 판정받았다.

**이 판정은 E-32를 대체한다 — 폐기가 아니라 색과 서체에 한해서다.** E-32가 세운 것 중 살아남는 것이
많다: 컨트롤이 **절대값**이라는 E-42 §2, 그림자가 테마가 아니라 **표면**을 따른다는 규칙, 램프가
절대 밝기 척도라는 것, 그리고 *"프로토타입과 측정이 어긋나면 측정이 이긴다"* 는 E-32 §4의 관행.
바뀌는 것은 팔레트·서체·기하, 그리고 아래 §1의 한 가지 되돌림이다.

### 1. `--color-hot`은 이제 액센트 **자신**이다 — E-32 §1을 의도적으로 뒤집는다

E-32 §1은 둘을 떼어 놓았고 `tokens.test.ts`는 *"같아지는 순간 브랜드 레드가 되돌아온 것"* 이라며
**분리를 단언**했다. 근거는 그 팔레트의 사정이었다: 액센트가 짙은 청록이고 마커가 은퇴한 브랜드 레드
`#EC3013`이었으므로, 둘이 같다는 것은 빨강이 다시 브랜드 색으로 새어 들어왔다는 뜻일 수밖에 없었다.

서고 스킨에는 빨강이 **하나**고 두 일을 모두 시킨다. 그래서 테스트는 이제 **동일성을 단언**한다 —
바뀐 쪽을 못박아야 다음에 사고로 되돌아가지 않는다. 같은 보호를 반대 방향으로 거는 것이다.

### 2. `--on-hot`이 뒤집힌다. 그리고 E-43의 그레인 면제는 **가독성 근거를 잃는다**

E-43은 `--on-hot`을 순검정으로 못박아야 했다. `#EC3013`은 밝아서 밝은 잉크가 AA에 **닿지도 못했고**
(`#F6F2E9`가 3.76, 순백조차 4.20), 검정이 씻긴 상태에서 겨우 0.23의 여유로 통과했다. 그래서 칩을
바의 그레인 위로 들어 올리는 면제가 **가독성의 유일한 근거**였다.

`#A2382A`에서는 위치가 정확히 반대다. **검정이 2.88 씻김으로 명백히 실패**하고, 실패하던 크림이
**5.62**로 1.12의 여유를 가진다. 그래서:

- `--on-hot`은 `--on-accent`와 **같은 크림**이 된다(마커와 액센트가 한 색이므로 당연한 귀결이다).
- `base.css`의 그레인 면제 규칙은 **남긴다.** 다만 이유가 바뀐다 — 대비가 아니라 **모양**이다.
  낙관은 종이 위에 눌린 인주지 종이의 결 밑에 깔린 것이 아니다. `tokens.test.ts`의 해당 테스트는
  *"씻김이 이 쌍을 AA 아래로 끌지 못한다"* 를 단언하도록 뒤집혔고, 규칙이 왜 남는지를 본문에 적었다.
  **자기 조건이 성립하면 지우라고 스스로 적어 둔 테스트였고, 그 조건이 성립했다.**

### 3. 다크 램프는 **어두운 종이**로 새로 유도했다 — E-32가 쓴 방법 그대로

프로토타입은 `:root` 하나뿐이고 다크 블록이 없다. E-32도 같은 상황에서 *"다크 램프는 새로 유도한다"*
고 판정했으므로 그 방법을 그대로 쓴다: **지면은 라이트의 잉크(`#221E1A`), 잉크는 라이트의 지면
(`#DED5C4`)**, 나머지 의미 토큰을 그 위에 다시 뒤집는다. 램프는 그대로 둔다.

다크를 **없애는 것은 선택지가 아니었다.** 그것은 기능 제거이고, 사용자는 기능을 그대로 두라고 했다.

### 4. 서체 — 고운바탕을 **벤더링한다.** 한글이 처음으로 벤더 서체로 그려진다

프로토타입은 Google Fonts를 부른다. NFR-OPS-001/002가 런타임 외부 의존을 금하고 빌드 단언이
`dist/`에서 폰트 CDN URL을 찾으므로 그 `<link>`는 삭제한다. 대신 사용자가 지정한 저장소에서
고운바탕 Regular/Bold를 받아 서브셋한다.

**크기 예측이 틀렸고, 틀린 방향이 좋았다.** `fonts.css`는 오래도록 *"서브셋한 Pretendard/Noto Sans KR은
1.5–4 MB"* 라며 한글 서체를 벤더링하지 않는 이유로 삼았고, 그 결과 *"크로스플랫폼 타이포그래피 편차"* 를
알려진 이슈로 안고 있었다. 실측: **현대 한글 11,172자 전부 + 라틴 + CJK 문장부호**를 두 굵기에 담아
**888 KB**다. 바이너리는 26.47 MB → **27.19 MB**.

이 스킨에서는 그 차이가 지난 스킨보다 크다. **명조가 곧 디자인**이고, 시스템 산세리프 대체는 조금 다른
서고가 아니라 **다른 제품**이다.

**낙관의 藏은 따로 벤더링한다 — 2,148 바이트.** 고운바탕에는 **한자가 0자**다(서브셋 선택이 아니라
원본이 그렇다). 그대로 두면 CJK 세리프가 없는 기기에서 브랜드 마크가 두부(tofu) 상자가 된다. 한 글자를
위해 한자 서체 전체를 넣지도, 디자이너의 글자를 한글로 슬쩍 바꾸지도 않고, 본명조에서 그 한 글자만 뽑았다.

### 5. 측정이 프로토타입을 이긴 자리 — E-32 §4의 관행대로 적어 둔다

| 자리 | 프로토타입 | 실측 | 채택 |
|---|---|---|---|
| 스크롤바 썸 | neutral-400 | 지면 대비 **1.40** | neutral-500 (거부) |
| `--control-border` | `--color-divider` | 표면 대비 **1.92**, 직전 스킨은 2.32 | `#A5967E`로 직전 수치 유지 |
| ⌘K 칩 잉크 | neutral-600 | 최대 그레인에서 **4.39** (AA 미달) | `--on-control-dim` (6.37) |
| 낙관 색 | `--color-accent` | 라이트 4.33 씻김 / **다크 2.15** | `--accent-text` (6.18 / 6.00) |

마지막 행은 프로토타입이 답한 적 없는 경우다 — 라이트 전용 문서에는 다크의 낙관이 없다.
**e2e 대비 스캐너가 찾았다**(항목 `v`·`ar`, 같은 세션에 처음 선 장치다).

### 6. 이 판정이 만든 결함 셋, 그리고 무엇이 잡았나

전부 이 세션에 고쳤고, **셋 다 화면을 보고는 알 수 없는 것**이었다.

1. **다크에서 `text-ink`가 액센트 채움 위 4.33** (`PageError.tsx`, `ViewerPage.tsx`) — 옛 다크의
   `--ink`는 청록 위에서 통과했다. `--on-accent`가 이 자리를 위해 존재한다. **유닛 티어의 컴포넌트
   스캐너가 잡았다.**
2. **낙관이 다크에서 2.15** — 위 §5. **e2e 대비 스캐너가 잡았다.**
3. **藏 서브셋이 CSP에 막혀 로드되지 않았다.** Vite가 2,148바이트 파일을 `data:` URI로 인라인했고
   arch §8.4의 `default-src 'self'`가 그것을 거부한다. 화면에는 藏이 **보였다** — 시스템 본명조가
   대신 그렸을 뿐이고, 정확히 벤더링으로 막으려던 실패가 조용히 성립해 있었다. **e2e의 콘솔 가드가
   잡았다.** `vite.config.ts`에 `assetsInlineLimit: 0`.

### 7. 팔레트 다음 — 스킨이 요구하는 **표시**들, 그리고 비켜 간 두 자리

§1–§6은 색·서체·기하를 옮겼다. 프로토타입이 그리는 **표시(mark)** 는 그 위에 따로 온다. 사용자가
`이어보기 각 만화카드의 배경`, `도장처럼 보이는 완도 표시`, `읽은부분 표시(예: 10/214p)`,
`늘어진 리분으로 책의 읽은 부분을 표시하는 방법`을 지목했고, 같은 문장에서 상단 바의 정렬·보기
컨트롤을 **오른쪽 정렬**로 옮겨 달라고 했다. 셋 다 `ui-spec.md` §4.2 / §4.3 / §4.4 / §4.5에
개정으로 적혀 있다. 요지만:

- **완독은 이름표가 아니라 낙관이다** — `DoneSeal`. 完讀 두 글자를 위해 본명조에서 3,360바이트를
  더 뽑았다(藏과 같은 이유·같은 방법, `unicode-range`). **접근성 이름은 한글 `완독`** 그대로다:
  한자는 `aria-hidden`이고 카탈로그 낱말이 DOM에 남아 있으므로, 한자를 모르는 독자만 읽을 수 있는
  것은 제품 어디에도 없다.
- **진행률은 커버 발치의 레일이 아니라 위에서 늘어진 갈피다** — `ReadRibbon`. 모양만 바뀌고
  **정보는 그대로**다: `role="progressbar"`와 같은 `aria-valuenow`를 계속 낸다. 그래서 매트
  바깥에 그린다 — 카드 상자에 `overflow:hidden`이 있으면 위로 넘긴 4px이 잘린다.
- **이어보기 카드는 서류철의 한 장이다** — 크라프트 지면, 펀치 구멍, 22px 괘선, 도장 찍힌 쪽수
  (`PageStamp`), 발치의 2px 괘선(`ProgressBar track="rule"`).

**비켜 간 두 자리, 그리고 왜.** 프로토타입 카드는 336px에 커버 52px·좌여백 34px이다. 이 카드는
269px이고, 그 폭은 **E-37이 측정으로 못박았으며** e2e가 브라우저에서 잰다. 커버 96px 역시 *66px에서는
표지 속 제목을 읽을 수 없다*는 측정에 근거한다. 둘 중 하나를 프로토타입에 맞추면 본문 칸이 115px —
두 줄 제목이 들어가지 않는다. 그래서 **폭은 그대로 두고 좌여백만 12→22**로 열어 구멍을 놓았고,
본문 칸은 137→127px이 되었다. 프로토타입과 측정이 어긋나면 측정이 이긴다는 E-32 §4의 관행 그대로다.

---

## E-47 — **시리즈 진행률은 읽은 쪽 수로 센다.** 완독한 권 수가 아니다 (사용자, 2026-08-21) — BINDING

**출처.** E-46의 갈피(`ReadRibbon`)를 붙이고 나서, 읽는 중 49개 시리즈 중 리본이 그려지는 것이
**3개**뿐이라는 것이 실화면에서 드러났다. 원인을 재어 사용자에게 네 가지 정의를 실데이터 표로 제시했고
(A 완독한 권 수 / B 읽은 쪽 수 / C 권마다 분수의 평균 / D 지금 읽는 권의 위치), 사용자가 **B**를 골랐다.

**이 판정은 `prd.md` FR-STT-002를 개정한다.** 그 조항은 *"시리즈 진행률은 소속 권의 완독 수 기준으로
집계·표시해야 한다"* 이고, 이 판정은 그 기준을 바꾼다. 요구사항 자체의 개정이므로 prd 쪽에도 개정 이력을
남겼다(FR-VWR-005가 E-27·E-44를 안고 있는 것과 같은 방식).

### 1. 실측 — 계단이 안 올라가는 것이 문제였다

`books_completed / books_total`은 **권 하나를 다 읽기 전까지 분자가 0인 계단 함수**다. 40권짜리
`3X3 EYES`를 1권 13쪽까지 읽은 독자는 0%다. 컬렉션 전수(읽는 중 49개, 서버에서 직접 계산):

| 정의 | 3X3 EYES(40권) | Area88(23권) | 천상천하(23권) | 0.5% 이상인 시리즈 |
|---|---|---|---|---|
| A 완독한 권 수 *(구)* | 0.0% | 0.0% | 4.3% | **3 / 49** |
| **B 읽은 쪽 / 전체 쪽** *(채택)* | 0.2% | 0.9% | 4.2% | **19 / 49** |
| C 권마다 분수, 평균 | 0.1% | 0.9% | 7.0% | 19 / 49 |
| D 지금 읽는 권의 위치 | 5.3% | 20.2% | 4.1% | 26 / 49 |

**C가 아니라 B인 이유**는 천상천하의 4.2 대 7.0에 있다. C는 3쪽짜리 설정집 한 권과 400쪽 단행본 한 권을
같은 무게로 세고, "얼마나 읽었나"는 쪽으로 재는 말이다.

**D는 percent에 넣지 않는다.** 그것은 *시리즈*의 몫이 아니라 *지금 읽는 권*의 위치이고, 리스트의 진행률
칸이 `3X3 EYES 5%`를 시리즈의 5%로 읽히게 만든다. 그 값이 필요한 자리에는 이미 이어보기 카드의
`13 / 104p`가 있다.

### 2. 두 모서리는 방어 코드가 아니라 규칙이다

- **100은 "모든 권이 완독"에만 준다.** `progress=done`(완독 서가)은 `완독 권 수 >= book_count`이고
  프런트의 完讀 낙관은 `percent >= 100`이다. 쪽 수는 *완독 표시를 안 한 권*의 끝까지 갈 수 있으므로
  (실제로 `사쿠라통신`이 그 상태다) 클램프가 없으면 **완독 서가에 없는 시리즈에 낙관이 찍힌다.**
  그래서 완독이 아니면 99.9에서 멈춘다.
- **나눌 것이 없으면 정확히 0.** 계약(arch §7.3)이 명시하는 조건이고, NaN은 `encoding/json`이 거부해
  응답 전체가 500이 된다. 분모가 `books_total`에서 `pages_total`로 바뀌면서 **이 가지에 걸리는 시리즈가
  늘었다** — 권은 있는데 열리는 쪽이 하나도 없는 시리즈가 그것이다.

### 3. 분모는 인덱스의 현재 길이다 — E-45 §6을 그대로 따른다

`ud.progress.page_count`는 *stale 판정용 baseline*이고 파일과 어긋날 수 있다(양쪽으로). 롤업은
`books.page_count`를 조인해서 쓰고, 시작한 권의 기여분은 `MIN(last_page, books.page_count)`로 자른다.
baseline을 쓰면 늘어난 파일이 100%를 넘고 줄어든 파일이 영영 안 찬다.

### 4. 바뀌지 않는 것, 그리고 **프런트에서 하나 바뀐 것**

계약의 **모양**은 그대로다 — 새 필드가 없고 `contractcheck` 41개가 그대로 통과한다. `progress=reading`
/`done` 스코프도 그대로다(그 둘은 percent를 보지 않는다). 골든 4개에서 움직인 값은 한 자리 —
군계(5쪽 중 2쪽)가 `0` → `40`이고, 그것이 이 판정이 사려던 바로 그 변화다.

라이브러리의 카드와 행은 이미 `percent`를 그대로 읽으므로 손댈 것이 없었다. **그런데 시리즈 상세
헤더는 아니었다.** `volume.ts`의 `seriesProgressRatio`는 `books_completed / books_total`을 **화면에서
다시 계산**하고 있었고, 주석은 그렇게 하는 것이 *"완독 수 기준이라는 요구를 화면에서 단언 가능하게
만든다"* 고 적혀 있었다. 심지어 그 옆에는 **`ignores the server percent field, so a page-weighted
server cannot move it`** 이라는 이름의 테스트가 서 있었다 — 지금 이 판정을 정확히 겨냥해 미리 세워 둔
가드다.

E-47은 그 근거를 뒤집는다. 새 정의의 분자·분모(`pages_read`, `pages_total`)는 **와이어에 없다**.
그러니 화면에서 다시 계산한 값은 새 이름을 쓴 옛 정의일 수밖에 없고, 그러면 같은 시리즈를 두고
**서가의 갈피는 40%, 그 아래 상세 헤더는 0%** 라고 말하게 된다. `volumeStateLabel`이 E-12를 인용하며
막는 것과 같은 모양의 결함이다. 그래서 `seriesProgressRatio`는 `percent / 100`이 되었고, 가드
테스트는 **반대 방향으로 다시 세웠다**: 픽스처의 `percent`가 권 수와 일부러 어긋나 있으므로, 옛
공식이 돌아오는 순간 실패한다.

이건 "프런트는 안 고쳐도 된다"는 최초 판단이 틀렸던 자리이고, 찾아낸 것은 **유닛 스위트가 아니라
`percent`를 읽는 곳을 전수로 훑은 것**이다 — 스위트는 옛 동작을 초록으로 지키고 있었다.

### 5. 남은 것

**읽는 중 49개 중 30개는 B에서도 0%다.** 1쪽만 열어 본 시리즈들이고, 정의의 문제가 아니라 실제로 안
읽은 것이다. 리본을 그 자리에도 보이게 하려면 "시작했으면 최소 한 칸"이라는 **별개의** 판정이 필요하다
— `ReadRibbon`의 2% 하한은 *그린 길이*만 올리고 보고 값은 건드리지 않으므로, 0%면 리본을 아예 안 그린다.

> **정정 (E-48, 2026-08-22).** 이 문단의 진단은 틀렸다. 0%의 원인은 "1쪽만 열어 본 것"이 아니라 D-73이
> 만든 **고아 진행 행**이었다 — 2026-08-22 실측으로 0% 21건 중 **20건**이 그것이고, 진짜로 조금 읽은
> 것은 **1건**뿐이다. E-48이 그 20건을 되돌려 놓았고, 그 뒤 "시작했으면 최소 한 칸"이 걸리는 시리즈는
> 컬렉션 전체에서 `다이어트 고고(완)` **한 개**다. 판정은 아직 필요하지만 규모가 다르다.


---

## E-48 — **책의 모양이 바뀌면 진행률은 따라간다.** D-73이 끊어 놓은 60건을 쪽 수 산술로 되붙인다 (사용자, 2026-08-22) — BINDING

**출처.** E-47이 남긴 "읽는 중 30개가 0%" 항목을 판정하려고 현상을 전수 분류했더니, **전제가 틀렸다**는
것이 먼저 나왔다. 사용자에게 실측을 제시하고 네 방향(A 복구 마이그레이션 / B 읽기 시점 재부착 /
C 기록만 / D 추가 조사)을 물어 **A**를 골랐다.

### 1. 실측 — 0%는 안 읽은 것이 아니라 끊긴 것이었다

읽는 중 **56개** 중 0%가 **21개**. 그 21개를 `last_book_id`가 인덱스에 있는지로 갈랐다:

| 분류 | 건수 |
|---|---|
| `last_book_id`가 `books`에 없음 — **고아 진행 행** | **20** |
| 진짜로 조금 읽음 (`다이어트 고고`, 2,519쪽 중 1쪽) | **1** |

0%가 아닌 35개 중 고아는 **0건**. 고아 ⟺ 0%가 이 데이터에서 정확히 일치한다. 보고되던 값은 조금
어긋난 정도가 아니었다:

| 시리즈 | 실제로 읽은 위치 | 화면 |
|---|---|---|
| `데이트 어 파티` | 39쪽 / 48쪽 | **0%** |
| `에버그린 01~23 (완)` | 531쪽 / 768쪽 | **0%** |
| `리브니스(완결)` | 69쪽 / 104쪽 | **0%** |
| `누나 두근` | 109쪽 / 540쪽 | **0%** |

그리고 이 20개는 **이어보기 선반에서도 전부 빠져 있었다**(이어보기 36개 중 0개 포함).
`GET /api/books/<그 id>`는 404다. 독자 입장에서는 읽던 자리를 잃은 것이다.

### 2. 원인 — D-73은 책의 *정체*를 바꿨고, 진행률의 열쇠가 그 정체다

책 id는 `hash(root 이름, root 상대경로)`(D-14, arch §3.4)라 인덱스 재작성·라이브러리 이동·기계 교체를
견딘다. 견디지 못하는 것은 **책의 모양이 바뀌는 것**이다. `a94731d`(D-73, 8/21 16:59)가 아카이브 안의
화 디렉터리를 들여다보기 시작하면서 484개 컨테이너가 6,097권이 됐고, 컨테이너 자신의 id는 아무것도
가리키지 않게 됐다.

진행 행은 지워지지 않는다 — NFR-DAT-004가 그렇게 시켰고, 드라이브를 뽑았다고 독서 기록이 사라지면
안 되기 때문이다. 그래서 행은 남고, `series.go`의 E-47 롤업이 `LEFT JOIN`으로 NULL을 받아
`MIN(last_page, 0)` = **0**을 계산한다. `LEFT JOIN`은 의도된 설계였지만 **다시 붙여주는 쪽이 없었다.**

증거 셋이 맞물린다: ① 고아 행의 `page_count` 기준선이 **컨테이너 전체 쪽수**와 일치(생존게임 2069,
Judge 1214, 고우영 열국지 1207 — 전부 시리즈 총 쪽수), ② 고아 20개가 **전부** `nesteddir` 책을 가진
시리즈, ③ 8/21 16:45~16:55에 읽은 세 시리즈가 고아 — 커밋 **4분 전**이다.

전체로는 `progress` 149행 중 **60행이 고아**. 그중 23행이 D-73이 만든 것이고, 나머지 37행은 컨테이너
자체가 인덱스에 없다(더 오래된, 별개의 원인 — §5).

### 3. 매핑은 산술이지 추측이 아니다

분할은 같은 쪽 목록의 **전량 분할**이고 순서가 보존된다(D-73 커밋: 배틀로얄 1,540쪽 전후 동일). 그래서
컨테이너의 쪽 P는 누적 범위가 P를 포함하는 권 v의 `P - start(v) + 1`쪽이다. 이름·mtime·휴리스틱이
끼어들 자리가 없다.

세 관문이 이것을 희망이 아니라 산술로 유지한다. 하나라도 못 넘으면 **그 행은 손대지 않는다**:

1. 고아의 id가 `BookID(root, book_path)`여야 한다 — 컨테이너가 책이던 시절의 id. 아니면 다른 문제다.
2. 권들의 쪽 수 합이 고아 행의 `page_count` **기준선과 같아야** 한다. 그 칸은 독자가 마지막으로 이
   컨테이너를 그 길이라고 인정한 값이므로, 일치는 "권들이 독자가 실제로 읽던 쪽들의 분할"이라는 증명이다.
3. 쪽이 그 합 안에 있어야 한다.

실컬렉션 23행이 셋을 모두 통과했고, **두 번째 관문에서 예외 0건**이다 — 틀린 이론이었다면 여기서 걸렸다.

### 4. 지나온 권은 완독으로 찍는다. 그리고 **새 기록이 옛 추론을 이긴다**

권 하나만 쓰면 안 된다. E-47은 완독한 책을 전체 길이로, 시작한 책을 마지막 쪽으로 센다. `에버그린`에
15권 3쪽만 쓰면 **531/768이 3/768**이 되어 화면이 69.1% 대신 **0.4%**가 된다 — 0%와 다른 종류의
오답이지 복구가 아니다. **사본 검증이 이 결함을 잡았다**(유닛 테스트는 권 매핑만 보고 있었다). 지나온
권을 완독으로 찍는 것은 `PutProgress`의 자기 규칙이기도 하다: 책의 마지막 쪽에 닿으면 완독이 된다.

Σ(완독 권) + 지역쪽 == 원래 절대쪽. 이 항등식이 테스트가 단언하는 성질이다.

**충돌은 `updated_at`이 새 쪽이 이긴다.** 고아는 구조적으로 더 오래된 기록이므로, 덮어쓰면 독자를 옛
모양의 자리로 되돌리게 된다. 이 규칙은 지나온 권에도 적용되고, 그게 흥미로운 쪽이다: `고우영 삼국지`는
절대 239쪽(1권 완독을 함의)에서 끊겼지만 독자가 8/22에 **1권을 5쪽에서 다시 열었다.** 산술이 만든
추론이 독자가 남긴 기록에 진다 — 시리즈는 15%가 아니라 **4.4%** 로 읽히고, 그게 지금 라이브러리
모양에서 독자가 실제로 있는 자리다.

### 5. 결과와 남은 것

사본으로 앱을 통째로 띄워 잰 값(실서버 아님):

| | 복구 전 | 복구 후 |
|---|---|---|
| 읽는 중 0% | 21 | **1** (`다이어트 고고`, 진짜로 1쪽) |
| 이어보기 항목 | 36 | **50** (상한) |
| `에버그린` | 0% | **69.1%** |
| 되돌린 진행 행 | — | retired 23 / written 47 / kept 2 |

**남은 37행은 별개다.** 컨테이너가 인덱스에 아예 없다(`드래곤 헤드`, `궁`, `이누야샤` 등) — 파일이
옮겨졌거나 root가 바뀐, D-73보다 오래된 원인이다. 이 코드는 그 행들을 **건드리지 않고 세기만** 한다.
개별 로그를 찍지 않는 이유도 그것이다: 이 기계에서 할 수 있는 일이 없어 매 부팅마다 같은 37줄이 된다.
컨테이너를 찾았는데 숫자가 안 맞는 두 경우(`length-mismatch`, `page-out-of-range`)만 줄을 받는다.

**복구는 매 부팅마다 돈다.** 플래그 뒤에 한 번이 아니다 — 아직 일어나지 않은 스캔에 대해 플래그가 옳기가
어렵고, 이미 돈 라운드는 아무것도 못 찾고 두 문장만 쓴다. 이번 실행의 스캔이 만든 분할은 다음 부팅에서
복구된다.


---

## E-49 — **이동은 재스캔이 스스로 알아낸다.** 이름 규칙이 아니라 내용 지문으로 (사용자, 2026-08-23) — BINDING

**출처.** E-48이 이름 규칙(`[만화] ` 접두 태그 제거)으로 31건을 되붙인 뒤, 남은 6건을 놓고 규칙을
더 붙이려 하자 사용자가 막았다: *"마지막 6건 복구는 재스캔을 통해서 자동으로 되야지, 임의로
복구하는 게 아니고."* 그리고 그 앞에 *"재스캔의 동작을 개선해야겠어"*가 있었다. 판정은 규칙을
늘리는 것이 아니라 **메커니즘을 만드는 것**이다.

### 1. 먼저 실측 — 동기화 자체는 이미 되고 있었다

"재스캔이 폴더와 DB를 맞추느냐"를 양방향으로 전수 측정했다(2026-08-23, 실서버 DB):

| 방향 | 검사 | 결과 |
|---|---|---|
| 인덱스 → 디스크 | `books`의 서로 다른 경로 11,366개가 실제로 존재하는가 | **유령 0건** |
| | `series` 경로 1,112개 | **유령 0건** |
| 디스크 → 인덱스 | 루트 3개의 `.zip/.cbz/.rar/.cbr/.pdf` 11,242개 | **누락 0건** |

11,242(파일) + 124(`kind='dir'`) = 11,366으로 맞아떨어진다. `SweepRoot`(arch §4.9)가 매 스캔
세대 스윕을 돌고 있었다. **동기화되지 않는 것은 `user.db`의 진행률뿐이고, 그건 의도다**
(NFR-DAT-004): 이번 60건 중 54건이 "사라진 파일"이 아니라 **이름이 바뀐 파일**이었으므로,
같이 지웠으면 7,208쪽이 영구 소멸했을 것이다.

### 2. 지문은 이미 있었다

`contentVersion`(`internal/scanner/incremental.go`)은 `FNV-1a(size:mtime)`이고 **경로가 들어가지
않는다**. 캐시 무효화(D-17·FR-THM-006)를 위해 만든 값인데, 경로가 없다는 성질 덕분에 **파일을
옮기거나 이름을 바꿔도 값이 그대로**다. 이름 규칙이 필요 없는 이유가 여기 있다.

### 3. 스윕 직전이 유일한 순간이다

책 id는 `hash(root, 경로)`라 이름이 바뀌면 삭제 + 무관한 도착으로 보인다. 두 행이 동시에 존재하는
순간은 **워크가 새 경로에 행을 쓴 뒤, 스윕이 옛 세대 행을 지우기 전** 하나뿐이다. 그 트랜잭션
안에서 `content_version`으로 짝지으면 이동이 증명된다(`index.observeRelocations`).

**양쪽 1:1을 강제한다.** 같은 지문을 가진 사라지는 행이 둘이면 어느 것이 옮겨졌는지 말할 수 없고,
살아남은 행이 둘이면 어디로 갔는지 말할 수 없다. 손으로 모은 서가에서 **사본은 예외가 아니라
일상**이므로 이 엄격함이 방어가 아니라 본체다. 살아남은 쪽은 **모든 root**에서 찾는다 — root 간
이동이 실제 사례 중 하나였다.

### 4. 배선은 arch §3.7을 지킨다

스캐너는 `user.db`에 쓸 수 없다(`check-readonly` 게이트). 그래서 **스캔은 관측해서 보고하고**
(`RootResult.Relocations`), **app이 해석하고**, **userdata가 쓴다**. 새로 단 `AfterScan` 훅이
세 단계를 스캔 끝에 순서대로 돌린다: ① 증거 기반 이동 옮기기 → ② 경로 기반 고아 재부착(E-48,
증거가 이미 스윕된 과거분용) → ③ 잘못 묶인 `series_id` 교정.

**실증(합성 라이브러리, 실바이너리):** 진행률 3/5쪽(60%) 기록 → 파일명에서 `[만화] ` 제거 →
`POST /api/scan` 한 번 → **읽는 중 60% 유지, 이어보기 유지, 재시작 없음.**

### 5. 이 판정이 드러낸 결함 — `series_id`도 경로 해시다

E-48의 첫 판이 **`book_id`만 옮기고 `series_id`는 옛 것을 남겼다.** 이름이 바뀌면 시리즈 id도
바뀌는데(둘 다 경로 해시), 그 결과 행이 **닿기는 하고 보이지는 않는** 상태가 됐다: 이어보기는
책을 풀어 보여 주지만, 시리즈 백분율·`읽는 중`·`완독`은 전부 `series_id`로 묶으므로 못 본다.
**실서버에서 27건.** 아무것도 고장나 보이지 않는 형태라 [[checks-that-watch-the-wrong-thing]]의
전형이다.

고침은 두 겹이다. 앞으로는 목적지의 `series_id`를 옮기고(`SplitVolume.SeriesID`,
`Relocation.NewSeriesID`), 이미 어긋난 행은 `MisfiledProgress` → `RefileProgress`가 매 스캔
교정한다 — `books.series_id`가 인덱스 자신의 답이므로 추측이 아니라 조회다. 사본 실측: 27건 전부
교정, 잔여 0건, 읽는 중 55 → **68개**.

### 6. 남은 것

**고아 6건은 이 메커니즘으로도 안 살아난다.** 그 파일들의 옛 `books` 행은 이 코드가 있기 전의
스윕이 이미 지웠고, 지문을 이어 줄 증거가 그때 사라졌다. 여섯 파일 모두 디스크에 있지만 각각 다른
변환(상위 폴더 밖으로 이동 / `(사본)` 접미 제거 / ZIP이 폴더로 풀림)이라 이름 규칙으로는 여섯 개가
필요하다. **규칙을 더 붙이지 않는다** — 이 판정의 요지가 그것이다. 앞으로 일어나는 이동은 전부
자동으로 따라간다.


---

## E-50 — **완주한 스캔이 못 찾은 책의 진행률은 지운다.** NFR-DAT-004의 무조건 보존을 한정한다 (사용자, 2026-08-24) — BINDING

**출처.** 사용자가 E-49에 이어 정책을 완성했다: *"재스캔을 통해 이전 바뀐 경로를 알 수 있다면
경로를 바꿔주면 되고, 지워졌다면 DB에서 관련된 모든 정보를 지우면 되고."* 그리고 남은 고아
여섯 건이 **사용자가 파일 탐색기로 직접 바꾼 것들**임을 확인해 줬다.

### 1. 이 판정이 한정하는 것

NFR-DAT-004는 "책이 인덱스에서 사라져도 독서 기록은 지우지 않는다"였고, 그 근거는 **코드가
'삭제'와 '드라이브 안 붙음'을 구분할 수 없다**는 것이었다. 그 근거는 유효하다 — 2026-08-22에
"사라진 것처럼 보이던" 60건 중 **54건이 사실 이름 변경**이었고, 그때 지웠으면 7,208쪽이 소멸했다.

이 판정은 그 규칙을 뒤집지 않고 **구분이 가능해지는 조건을 명시한다.** 스캐너는 이미 그 조건을
쓰고 있다: `decideSweep`은 ① root 열거 실패 ② 스캔 취소 ③ 지정 시리즈만 훑은 실행, 이 셋 중
하나라도 있으면 인덱스 스윕을 **거부한다.** 부재가 "안 봤다"를 뜻할 수 있는 경우가 정확히 그
셋이기 때문이다. 그 셋이 아닌 root는 **끝에서 끝까지 걸었고 없었다**는 뜻이고, 그때 비로소
진행률도 지울 수 있다.

### 2. 순서가 가드의 일부다

정리는 스캔 뒤에만, 그리고 이 순서로 돈다:

1. **이동 이관** — 스캔이 `content_version`으로 증명한 이동 (E-49)
2. **고아 재부착** — 경로 산술 (E-48)
3. **`series_id` 교정** — 인덱스 조회
4. **삭제** — 위 셋이 전부 설명하지 못한 행

**시작 시에는 1·4를 안 돌린다.** 아무것도 걷지 않았으므로 모든 부재가 미설명이다. 삭제까지
왔다는 것은 시스템이 가진 모든 설명을 제시받고 하나도 안 맞았다는 뜻이다.

**빈 root 목록은 "전부"가 아니라 "없음"이다.** `VanishedProgress(nil)`은 아무것도 안 돌려준다 —
"완주한 root가 하나도 없음"이 "모든 독서 기록이 낡음"으로 읽히면 안 되고, 그 단언이 테스트의
첫 줄이다.

### 3. 지우는 것은 관련 정보 전부

`progress`와 **`book_prefs`를 같은 트랜잭션에서** 지운다. 열 수 없는 책에 걸린 읽기 방향은 이름만
다른 같은 고아다. 삭제되는 행은 **경로와 읽던 쪽까지 개별 로그**로 남긴다 — 개수만 찍는 정리는
엉뚱한 행을 가져간 정리와 구분되지 않고, 이건 제품 안에서 되돌릴 수 없는 유일한 연산이다.

### 4. 실증

**합성 라이브러리, 재스캔 한 번:**

| | 변경 전 | 디스크 조작 | 재스캔 후 |
|---|---|---|---|
| `[만화] 옮길책.zip` | 40% | 이름 변경 | **`옮길책.zip` 40%** |
| `지울책.zip` | 50% | 삭제 | **진행률 정리됨** |

**실 라이브러리 사본, 전체 스캔 완주**(1,112시리즈 · 17,617권 · 143만 쪽 · 112초):
정리 단계 로그 **한 줄도 없음**, `progress` **201행 → 201행**. 건강한 라이브러리에서 이
메커니즘은 침묵하고 아무것도 파괴하지 않는다.

### 5. 여섯 건은 사용자 확인으로 교정했다 (일회성)

메커니즘으로는 못 살린다 — 옛 `books` 행이 이전 스윕에 이미 지워져 지문이 없다. 사용자가
**직접 바꾼 것**이라고 확인해 주면서 기계에 없던 증거가 생겼고, 그래서 일회성으로 교정했다.
쪽 산술은 E-48과 동일하고, **여섯 건 전부 `Σ(완독 권) + 지역쪽 == 원래 절대쪽` 항등식을 통과**했다:

| 옛 경로 | → 지금 | 읽던 위치 |
|---|---|---|
| `[만화] 3X3 EYES 1~40(완).zip` | `3X3 EYES 1~40(완)/` 40권 | **4,055/7,823 (51.8%)** |
| `[만화] 4월은…(사본)/01권.zip` | `4월은 너의 거짓말 1~11권 완결/01권.zip` | 83/192 |
| `단편 만화/Akira Toriyama….zip` | `Akira Toriyama….zip` | 22/54 |
| `우마루/츠쿠시비요리.zip` | `츠쿠시비요리.zip` | 7/157 |
| `우마루/쓰레기의 본망.zip` | `쓰레기의 본망.zip` | 1/427 |
| `고다건 1-16권 완결.zip` | `고다건(완)/` 16권 | 1/1,520 (기준선 1,519와 1쪽 차이, 사용자 확인으로 진행) |

결과: **고아 0건 · `series_id` 불일치 0건 · 읽는 중 73개 · 0%는 진짜 1건**(`다이어트 고고`,
2,519쪽 중 1쪽). 백업은 `resources/shelf/user.db.backup-pre-manual-rename-fix-20260824-014447`.

**이 표는 기록이지 규칙이 아니다.** 앞으로 같은 일이 생기면 E-49가 자동으로 처리하고, 처리하지
못하는 것은 E-50이 지운다.
