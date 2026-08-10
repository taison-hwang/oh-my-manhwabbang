# SHELF — Implementation Plan (authoritative, sequenced)

| | |
|---|---|
| Document | Merged implementation plan for parallel agents |
| Version | v1.0 |
| Date | 2026-07-28 |
| Status | **Authoritative.** Supersedes disagreements between the three specialist specs. |
| Inputs merged | [`prd.md`](./prd.md) (URD — wins every conflict), [`design.md`](./design.md), [`ui-spec.md`](./ui-spec.md), [`arch-backend.md`](./arch-backend.md), [`data-survey.md`](./data-survey.md) |
| Product / module | **SHELF** · Go module `shelf` (bare path, no host prefix — orchestrator ruling E-1, 2026-07-28) · binary `shelf` |

> **Read order for an implementer.** §0 (conflict resolutions — read even if you think you know your area)
> → §1 (scope, find your FR ids) → §2 (file tree, find your files) → §3 (your work package)
> → §4 (frozen API contract) → §5 (conventions — binding) → §6 (tests) → §7 (done).
>
> **Rule of the build:** a work package owns files, and **no file is owned by two packages**. If you feel
> you need to edit a file you do not own, you have found a plan bug — stop and report it rather than editing.

---

## 0. Conflict resolutions and adjustments

`prd.md` is the source of truth. Where a specialist spec disagrees with it, the prd wins. Where the data
survey showed reality breaking an assumption, the plan was adjusted. Every resolution below is binding.

### 0.1 Spec-vs-spec conflicts

| # | Conflict | Resolution | Rationale |
|---|---|---|---|
| **C-1** | **Display-mode enum.** `arch-backend §7.3` = `"single" \| "spread" \| "vertical"`. `ui-spec §9` = `'single' \| 'double' \| 'vertical'`. | **Wire value is `spread`.** The Korean UI label stays `양면`. `double` does not exist anywhere in code. | The API contract is frozen and shared; a label is not a type. |
| **C-2** | **Fit-mode enum.** arch = `"width"\|"height"\|"original"\|"contain"`. ui-spec = `…\|'screen'`. | **Wire value is `contain`.** UI label stays `화면`. | Same. |
| **C-3** | **Sort keys.** ui-spec = `name\|mtime\|read\|size\|vols`. arch = `name\|mtime\|recent\|size\|books\|added`. | **API names win**: `name\|mtime\|recent\|size\|books\|added`. UI labels 이름/수정일/최근 읽은 순/용량/권 수 unchanged. | Same. |
| **C-4** | **Book kind for an image folder.** ui-spec `Format='folder'`; arch `BookKind="dir"`. | **Wire value `dir`** for books, **`folder`** for series (`SeriesKind`). Badge text is `FOLDER` for both. | arch's split is correct: a series can be a folder while its books are zips. |
| **C-5** | **Root add/remove in the UI.** design.md 화면 4 wants 추가/제거; prd FR-CFG-001 makes YAML the source of roots and prd 6.3 has no `POST /api/roots`. | **prd wins — roots are read-only in the UI.** Settings lists roots with path, counts, per-root `재스캔`, and the resolved config-file path plus the text `shelf.yaml을 편집한 뒤 재시작하세요`. The `+ 루트 추가` and `제거` buttons are **removed**, not disabled. Onboarding's `루트 추가` button becomes `설정 파일 위치 보기` (copies the path). | Writing YAML from a web UI is a security + correctness surface prd never asked for. (arch OQ-3, accepted.) |
| **C-6** | **`archive/zip` for indexing.** prd 6.1 names `archive/zip`; arch ships a purpose-built central-directory reader. | **Deviate, with a guardrail.** `internal/archive/zipidx` is the indexer; `archive/zip` stays in the test suite as a **differential oracle** that must agree on every fixture and every archive under `SHELF_TEST_ROOT`. | `zip.File` does not expose the local-header offset that FR-SRV-002 (필수) requires; the only stdlib route (`DataOffset()` per entry) did not finish in 10 min vs 32.3 s. prd 6.1 is a technology hint; FR-SRV-002 is a requirement. Recorded as an escalation (E-2). |
| **C-7** | **Go version.** prd 6.1 says ≥1.22; arch requires ≥1.25 (`os.Root`, ServeMux patterns). | **`go 1.25.0` in `go.mod`, `GOTOOLCHAIN=auto`.** Satisfies "1.22 이상". | `os.Root` is path-traversal layer 3 (NFR-SEC-001) and is worth the floor. |
| **C-8** | **PDF timing.** prd §9 defers PDF to release 2; arch OQ-2 says ship in v1. | **Moot — stages 1+2 are both in scope for this build.** PDF ships. `nopdf` build tag retained. | AC-004 is an acceptance criterion regardless of stage. |
| **C-9** | **Reading-direction default.** ui-spec §5.1 invents a per-series `dirs[seriesId] ?? (manga root ? 'rtl' : 'ltr')` heuristic; prd FR-VWR-002 says **권 단위**; arch stores per-book prefs. | **prd wins.** Persisted direction is per-book (`PUT /api/books/{bid}/prefs`), falling back to the global default in `/api/settings`. The 읽기 방향 `.seg` on **series detail** is a client-only convenience: it seeds the direction for books opened from that screen and is kept in the Zustand store + `localStorage` under `shelf.seriesDir`. It never writes the server. | Keeps one persistence story; no "manga root" metadata exists to key a heuristic on. |
| **C-10** | **Chosung search location.** ui-spec §8.4 ports a client-side `chosung()` and filters the loaded list; FR-LIB-007 requires the list to be virtualised/paginated, so the client never holds all series. | **Server-side is authoritative.** Both the top-bar search and the command palette call `GET /api/series?q=…` (debounce 150 ms, `limit=8` for the palette). The client `chosung()` helper survives **only** for match highlighting. | With 963–10 000 series a client filter is wrong by construction once pagination lands. |
| **C-11** | **Scan progress transport.** prd FR-IDX-004 says "실시간"; arch chose 1 s polling over SSE. | **Polling, 1 s, while `state !== "idle"`.** SSE explicitly out of scope. | A full cold scan is 32 s ⇒ ~32 requests against an atomic snapshot. SSE would burn one of six HTTP/1.1 connections the viewer needs for prefetch. |
| **C-12** | **Thumbnail widths.** arch OQ-7 left `[240,320,640]` unconfirmed. | **`[120, 240, 400, 640]`** — resolved from the ui-spec's real rendered sizes at 2× DPR (§0.4). | Snapping *up* against the wrong set silently doubles bandwidth. |
| **C-13** | **Default fit mode.** arch config default `contain`; ui-spec §6.2 marks `height` as the prototype default. | **`height`.** `reader.fit_mode: "height"` in `shelf.example.yaml`. | The design evidence is a live capture; the config default was a guess. |
| **C-14** | **Font `@import` of Google Fonts** in the DS stylesheet. | **Deleted.** Vendor Archivo 400/600/800 via `@fontsource/archivo` (latin + latin-ext only). Korean uses the system fallback stack in ui-spec §1.2. No Korean webfont is vendored. | NFR-OPS-001/002 forbid a runtime external dependency. A subsetted Korean face is 1.5–4 MB and would dominate an 18 MB binary. |
| **C-15** | **Sidebar smart lists** (`읽는 중` / `최근 추가` / `완독`) have no matching API filter. | **API amendment A-4**: `GET /api/series` gains `progress=any\|reading\|done\|unread`. ~~`최근 추가` is `sort=added&order=desc`.~~ **Superseded for 최근 추가 by A-8 (ruling E-9):** a sort re-orders the whole library, so the row's count was the whole-library total. 최근 추가 is now `scope=added` (a filter) **and** `sort=added&order=desc` (the ordering within it). | The alternative — filtering client-side — breaks under FR-LIB-007. |

### 0.2 Adjustments forced by the real collection (data-survey)

| # | Finding | Adjustment |
|---|---|---|
| **D-1** | **CP949 is 100 % of non-ASCII entry names** (14 630 flagless non-ASCII names; **0** valid UTF-8; **0** produce U+FFFD under CP949). And `korean.EUCKR.NewDecoder()` **never returns an error** — it substitutes U+FFFD silently. | FR-IDX-008 is a **first-class MVP path, not a fallback**. The decision rule is the four-step function in arch §4.4 (UTF-8 flag → valid-UTF-8 probe → CP949 → lossy) and the error return is ignored; **the U+FFFD content check is the test**. A unit test asserts the decoder still returns `nil` on garbage so the check cannot be "cleaned up" later. Owner: **WP-02**. |
| **D-2** | **Zero `.webp`, zero `.avif` in every sample taken** — two independent passes, data-survey's 500-ZIP scan and arch §1.1's ~56k-entry extension census (of 1.36 M entries total); JPEG is 98.7 %. | FR-IDX-011 is 필수 and lists all seven, so **all seven are implemented**. But: AVIF decode costs 1.1 s and ~170 MiB, so it is **lazily initialised, 1-permit-serialised, and killable** (`thumbnails.avif_enabled`, `-tags noavif`). Animated WebP thumbnails degrade to `422 thumb_unavailable`; the original still streams. No format is on the critical path. Owner: **WP-07**. |
| **D-3** | **ZIP64 does not occur** (largest archive 1.48 GB). prd FR-IDX-009 is 필수. | Implemented anyway, tested **only** against a hand-built synthetic ZIP64 fixture (`testutil.BuildZIP64`) because no real sample exists. Owner: **WP-04**. |
| **D-4** | **9 broken archives / 11 157 (0.08 %)**, all truncated, plus one 0-byte `.zip`. Zero encrypted archives found. | FR-IDX-010 error isolation is exercised by real data for `error`; **encryption is only testable synthetically**. `testutil.BuildZIP` must be able to set GP bit 0. Owner: **WP-04** (fixture), **WP-08** (status), **WP-12** (surfacing). |
| **D-5** | **The "mixed" shape is really "N archives + exactly 1 cover image"** — 47 of 672 directories. | `scan.cover_max_loose_images: 3`: ≤3 loose images beside real books are **cover candidates, not a one-page book**. Above that they become a `(loose pages)` book. Owner: **WP-08**. Verified against `[만화] 강철의 연금술사 1~27권 완결` and `[만화] 군계 1~25` in E2E. |
| **D-6** | **Real duplicate books**: `군계(軍鷄) 01권/` (folder) *and* `군계(軍鷄) 01권.zip`; `07권.zip` + `07권.repair.zip` + `07권 (2).repair.zip`. | **Show them all**, natural-sorted, no dedup magic (arch OQ-4). Hiding the only readable copy is worse than showing three. `duplicate_of` deferred. |
| **D-7** | **Non-media series exist** — 5 top-level dirs hold only `.txt`/`.hv3`. | Indexed with `status:"empty"` and listed greyed out; `?status=ok` hides them. Never silently dropped (arch OQ-6). |
| **D-8** | **Mixed zero-padding is pervasive** (`1.jpg`, `10.jpg`, `100.jpg` in `[만화] 자살도114-122`; `MLM08-0062.jpg`; `sam 05 167.gif`). | Natural sort (FR-IDX-007) is a hard blocker, implemented as **two agreeing representations** (`Compare` for Go, `Key` as a SQLite-orderable BLOB) with a property test asserting they agree. Owner: **WP-02**. |
| **D-9** | **p99 = 570 pages, max 1 071 pages per archive**; one 1.34 GB archive holds 1 540 images. | AC-008 is a real case. `pages` is `WITHOUT ROWID` keyed `(book_id, page_no)`; `GET /api/books/{bid}` returns all `PageInfo` in one shot (~110 KB at 1 071 pages) so jumps need no round trip. The viewer thumbnail strip **must** be virtualised. |
| **D-10** | **One archive is a container of 33 sub-ZIPs** (`[만화] 엔젤하트 전32권 완결.zip`) → 0 image entries. | **SUPERSEDED by D-70 — nested ZIPs are now read, one book per inner archive; what follows is the pre-D-70 record.** Nested-archive reading was **out of scope** (prd 7.2 defers format expansion), so the **book** was `status:"empty"` and the scan must not abort or count it as an error. **Narrowed by ruling E-14:** the *series* over that one empty book is `status:"error"` with the book's reason, because it is a series the reader cannot open a single page of (`empty` is now reserved for "no books at all"). D-10's original "yields `status:\"empty\"`, not an error" was written before E-14 and referred to the book; at the series level `decisions.md` wins (§0 precedence). Listed as a known limitation in the E2E expectations. |
| **D-11** | **Symlinks cannot be used to build a test subset** — `os.Root` refuses any symlink that escapes the root (arch §8.1 layer 3, verified) and `scan.follow_symlinks` defaults false. | The curated E2E subset uses a **root pointed at the real collection plus a `scan.include_globs` allowlist** (new config key, amendment A-3). Nothing is copied. See §6.3. |

### 0.3 Amendments to the frozen API contract

`arch-backend.md §7` is normative **as amended here**. These are the only changes; WP-06 encodes them in
`web/src/api/types.ts` and WP-12 implements them.

| Id | Amendment |
|---|---|
| **A-1** | `thumbnails.widths` default becomes **`[120, 240, 400, 640]`** (§0.4). |
| **A-2** | `reader.fit_mode` default becomes **`"height"`** (C-13). |
| **A-3** | **New config key** `scan.include_globs: []`. When non-empty, only a root's direct children whose **base name** matches at least one `path.Match` pattern become series. Applied before classification. Purpose: curated test roots without copying terabytes; also genuinely useful to users. |
| **A-4** | `GET /api/series` gains **`progress=any\|reading\|done\|unread`** (default `any`). `reading` = has a progress row with `completed=0`; `done` = every book in the series completed; `unread` = no progress row for any book. Unknown value → `400 bad_request`. |
| **A-5** | `GET /api/settings` `Settings` gains **`library_scope: string`** (persisted sticky sidebar scope: `"all"`, `"reading"`, `"added"`, `"done"`, or a root name). User-mutable. |
| **A-6** | `GET /api/series/{sid}/cover` and `GET /api/books/{bid}/thumbs/{n}` default `w` becomes **`120`** (`widths[0]`), consistent with A-1. |
| **A-7** | Everything else in arch §7 — paths, verbs, status codes, field names, the error envelope, 1-based pages, `?v=` cache-version semantics, `202 + Retry-After` for queued thumbnails, `409 stale_version`, `422 thumb_unavailable`, `501 unsupported` — is **frozen verbatim**. Any further change requires updating this table first. |
| **A-8** | **`GET /api/series` gains `scope=all\|added`** (default `all`; any other value → `400 bad_request` with `detail.param="scope"`), backed by a **write-once `first_seen_at` per series stored in `user.db`, not `index.db`**, so it survives `--rebuild-index` (ruling **E-9**). Predicate: `first_seen_at >= now − library.recently_added_days × 86400`; a series with no row is excluded. `scope` is a *filter*, AND-ed with `root`/`q`/`status`/`progress`, and does **not** change the `sort`/`order` defaults. **The 최근 추가 sidebar count is `total` from `GET /api/series?scope=added&limit=1`** — no new endpoint, no new payload field, identical to how 읽는 중/완독 already count. `SeriesSummary.added_at` and `sort=added` both resolve to `COALESCE(first_seen_at, index added_at)`; `scope=added` filters on `first_seen_at` alone. Adds **config key `library.recently_added_days: 14`** (int 1..3650) and **`Settings.server.recently_added_days: number`** (read-only mirror). Adds `user.db` table `series_seen` at `schema_version` **2**. Full normative text: **arch §7.5 "Amendment A-8"**, write rule in **arch §3.6**, config in **arch §3.2**. |
| **A-9** | **`ErrorCode` gains `"rate_limited"`** (ruling **E-13**), the code carried by the `429` of arch §8.2's per-IP login limiter (5/min, burst 5). Status **429**, always with a `Retry-After` header and the same integer in `detail.retry_after`. §7.2 mandated the behaviour in §8.2 but gave the response no name, so the only answer the contract could express for it was a status; `web/src/api/errors.ts` carried a status-only fallback and the Go side an out-of-enum constant. Both are now the contract. Nothing else changes: the limiter, its status, its header and its message are unchanged, and no other endpoint returns this code. Normative text: **arch §7.2**; golden `internal/httpapi/testdata/golden/error_rate_limited.json`. |
| **A-10** | **`Settings.server.config_path: string`** (ruling **E-25**), the configuration file the running server loaded, **absolute and cleaned**. Read-only like every other `server.*` key: a `PUT /api/settings` carrying it is `400 bad_request` under the same whole-block strict-decoding rule, which needs no new branch. C-5 and ruling E-3 make the settings screen and the onboarding screen tell the user to edit `shelf.yaml` and restart, and arch OQ-3 accepts read-only roots on that basis — but the lookup order has **four** entries (`$SHELF_CONFIG`, `./shelf.yaml`, `$XDG_CONFIG_HOME/shelf/shelf.yaml`, `/etc/shelf/shelf.yaml`, `cmd/shelf/flags.go`), so the instruction shipped naming no file. The value is `config.Config.FilePath` resolved through the new `(*config.Config).AbsFilePath()`, **not** `FilePath` itself: entry 2 of the order makes `FilePath` legitimately relative, and `FilePath` is also the prefix of every configuration error message, so it is read and never rewritten. `""` only when the server was built from a configuration with no file. Normative text: **arch §7.8** and **arch §12 OQ-3**; golden `internal/httpapi/testdata/golden/settings.json`; consumers `web/src/features/overlays/RootsPanel.tsx` and `web/src/features/library/Onboarding.tsx`. |
| **A-11** | **`POST /api/roots` and `DELETE /api/roots/{name}`** (ruling **E-26**), reversing D-33 / E-3's "no `POST/DELETE /api/roots`" in part. **The endpoints edit `shelf.yaml` and nothing else** — roots are opened once at startup (`internal/app/app.go` step 6, `source.OpenRoots`) and there is no reload path, so a change is adopted at restart, which is the instruction C-5 already prints on the settings screen. **`POST`** body `{path: string, label?: string}` → **`201 RootEntry {name, path, label, enabled}`** + `Location: {base_path}/api/roots/{name}`; `name` is **server-generated** (a slug of `label`, else of the base name of `path`, uniquified against the current configuration) because `name` is what every `series_id`/`book_id` hashes (D-14, D-51). **`DELETE`** → **`204`**. ~~Removing the entry from the file only: index rows and reading progress stay, exactly as `App.reconcileRoots` already does for a root that leaves the configuration.~~ **REVISED 2026-07-30 (R1, decisions.md E-26 "REVISION")**: it removes the entry from the file **and purges that root's rows from `index.db`** (`index.DeleteRoot`, one SQL transaction), because `GET /api/roots` reads the index and `GET /api/series` has no configured-root filter, so file-only removal left the root listed and its series in the library after the restart the UI demanded. **`user.db` is not touched** — reading progress survives and reattaches if the same directory is re-added under the same generated name, which is what §7.4's "uniquify against the configuration, not the index" rule exists for. The live server also honours it before the restart through an in-memory, mutex-protected **removed-set**: excluded from `GET /api/roots`, `404` from `POST /api/scan {roots:[name]}`, and skipped in a full scan. The open root set, the pool and the source factory are **not** hot-swapped. `App.reconcileRoots` is unchanged: an explicit `DELETE` is evidence of intent, a missing line in a hand-edited file is not. **New config key `server.allow_root_editing: false`** gates both verbs; with it off they are **`403 forbidden`**. **`ErrorCode` gains `"forbidden"` (403)** — the enum had no value for the status this ruling mandates, and `unauthorized` would have driven the frontend's re-auth path (`isAuthError`, ruling E-17) for a refusal no login can lift; same defect and same fix as A-9. **`Settings.server` gains two read-only booleans**: `root_editing_enabled` (the *capability*: the key is on **and** this server has a configuration file **and** that file is not inside a media root — folded like `pdf_enabled` folds `-tags nopdf` with `pdf.enabled`) and `config_changed_on_disk` (the file at `config_path` is no longer byte-identical to the one this process loaded; it also covers the pre-existing hand-edit case, which is the workflow C-5 has been telling users to use all along). **No filesystem browser, no directory listing, no `PATCH`**: `enabled`, `name`, `path` and `label` of an existing root stay file-only. **REVISED 2026-07-30 (R2)**: `GET /api/roots` additionally reports roots that are in the **configuration file on disk** with no index row, marked by a new `Root` field, with no counts and no rescan — `POST` was otherwise invisible until the restart, which the Claude Design prototype contradicts. The file's `roots:` list is read from the same code path that already re-hashes it for `config_changed_on_disk`. Full normative text: **arch §7.2** (enum), **§7.3/§7.4** (both endpoints, request/response shapes, the pending field, every status and every `detail`), **§7.8** (the two settings fields), **§3.2** (the config key), **§12 OQ-3** (the changed answer, with the old one kept and dated); the two revisions in **decisions.md E-26 "REVISION 2026-07-30"**. |

| **A-12** | **`GET /api/browse`** and **`POST /api/roots` now opens the root it wrote** (ruling **E-40**). A-11 shipped an add that stayed invisible until a restart; A-12 opens the new root into the running server and scans it. Adds **config key `server.browse_bases: []`** (each entry non-empty, absolute, cleaned at load — `GET /api/browse` decides containment by string prefix, so an uncleaned `/mnt/x/` would refuse everything under it). Existence is deliberately **not** checked, for the same reason a root's path is not (§4.9). Normative text: **arch §7.4** — both endpoints and the amendment note, including `GET /api/browse` itself, which lives in §7.4 (`arch-backend.md:1951`) and not in §7.6 as this row said until 2026-08-10 — and **§3.2** (the config key). |
| **A-13** | **`DELETE /api/roots/{name}` moves the adopted digest back** (ruling **E-41**). A-12 taught the add path to change what the running server holds; the remove path did not follow, so removing a root left A-12's added-set and the adopted configuration digest stale. `DELETE` now drops the name from A-12's added set and — **only when the file it just wrote was the file this process loaded** — moves the adopted digest back, so `config_changed_on_disk` does not fire for the server's own edit. It still closes no handle. Normative text: **arch §7.4**. |
| **A-14** | **`ProgressUpdate` gains `stale_seen?: boolean`, and an unacknowledged write preserves `page_count`** (ruling **E-45**). The stored `page_count` is the baseline `stale` is derived from; before this it was overwritten on every progress write, so the `파일이 변경되었습니다` warning was destroyed about a second after it appeared and never returned on any device. `page`'s clamp and FR-VWR-012's automatic completion keep using the index's **current** length — only the persisted column holds the baseline. A plain page turn is **not** an acknowledgement: the viewer writes progress on load alone, so an implicit rule would clear the warning for a reader who did nothing. Clients that omit the field are unaffected. **`Progress.stale` is symmetric**: `0` on **either** side is `false`, because `0` is "length unknown" (§4.11) and a comparison needs two lengths. A book that is unreadable *now* therefore does not warn — the screen already says the file cannot be opened, the reader has no place to resume at, and no acknowledgement could ever be sent because the viewer never finishes loading it. The warning is **deferred, not lost**: the baseline survives and a repair to a different length raises it then. **An acknowledgement also rebaselines only when the length is known** (`page_count > 0`): no client can send that combination once `stale` is symmetric, so the rule exists for the hand-made call that never passed through a screen — defence in depth for the baseline the deferral depends on. **No schema change** (`schema_version` stays 2), and no golden changes — the symmetry only turns `true` into `false`, and none of the 36 goldens carry `stale: true`. Normative text: **arch §7.6**, the `Progress.page_count` and `Progress.stale` comments in **§7.3** (`arch-backend.md:1601`, `:1614`), and the DDL comment in **§3.6**. |

*A-9 supersedes A-7's "frozen verbatim" for §7.2's enum only, A-10 for §7.8's `server` block only, A-11
for §7.2's enum again plus §7.4's endpoint table, §7.8's `server` block and §3.2's `server:` block, A-12 for
§7.4 and §3.2's `server:` block, A-13 for §7.4, and A-14 for §7.6's `ProgressUpdate` — exactly as
A-7 requires in every case: the table is updated first.*

> **A-12, A-13 and A-14 were backfilled on 2026-08-10.** A-7 says any further change to arch §7 "requires
> updating this table first", and **A-12 and A-13 did not** — they landed as inline `AMENDMENT` notes in
> `arch-backend.md` (12세션차 and 13세션차) while this table stopped at A-11. The 16세션차 agent that wrote
> A-14 found the gap and refused to add A-14 alone, because a table missing three of its rows but ending at
> the newest one **looks complete** and is read as authoritative. That judgment was right and the rule it
> protects is this one: **when a ledger and the thing it records disagree, the ledger is the more dangerous
> copy, because it is the one people read instead of the source.**

#### A-8 follow-up work, file by file

A-8 was ruled after waves 0–2 landed, so unlike A-1…A-7 it is **not** yet encoded anywhere in the tree.
Nothing below is optional; each item is owned by the package that owns the file, and none of it belongs
to WP-12.

| File(s) | Owner | Change |
|---|---|---|
| `internal/config/config.go` (+ test) | **WP-01** | New `Library struct { RecentlyAddedDays int }` with default **14**, YAML key `library.recently_added_days`, validated 1..3650 → `exit 2` (arch §3.2). |
| `shelf.example.yaml` | **WP-01** | The commented `library:` block of arch §3.2, verbatim, so `config`'s round-trip test still passes. |
| `internal/userdata/schema.go` | **WP-03** | Migration rung `{to: 2, sql: schemaV2}` creating `series_seen` + `ix_series_seen_first`; `schemaVersion = 2`. Append-only — the rung may only `CREATE … IF NOT EXISTS`. |
| `internal/userdata/series_seen.go` (new) | **WP-03** | `MarkSeriesSeen(ctx, []SeriesSeen) error` — batched `INSERT … ON CONFLICT(series_id) DO NOTHING`. Plus the bootstrap-marker helpers over `meta.first_seen_bootstrap`. No update or delete path exists. |
| `internal/index/series.go` | **WP-03** | `SeriesFilter.Scope` (`""`/`"all"`/`"added"`, else `ErrInvalidFilter`) + `RecentlyAddedCutoff int64`; `seriesWhere` gains `ud.series_seen.first_seen_at >= ?`; `seriesColumns`' `s.added_at` becomes `COALESCE(sn.first_seen_at, s.added_at)` over a `LEFT JOIN ud.series_seen sn ON sn.series_id = s.id`; `seriesOrder`'s `SortAdded` expression follows it. |
| `internal/scanner/*` | **WP-08** | Call `MarkSeriesSeen` once per root per run with `first_seen_at = run start`, on the userdata handle in its own transaction (arch §3.7). Bootstrap run uses `min(runStart, series.mtime)` and then writes `meta.first_seen_bootstrap`. |
| `web/src/api/types.ts` | **WP-06** | `SeriesListParams.scope?: 'all' \| 'added'`; `Settings['server'].recently_added_days: number`. |
| `web/src/api/urls.ts` / `queries.ts` / `fixtures.ts` | **WP-06** | Serialise `scope`; add the field to the MSW settings fixture. |
| `web/src/features/library/useLibrary.ts` | **WP-09** | `libraryParams`: `scope === 'added'` ⇒ `{scope: 'added', sort: 'added', order: 'desc'}`. Drop the `SCOPE_PROGRESS.added = 'any'` workaround. |
| `web/src/router.tsx` (`ShellDataProvider`) | **WP-09** | `counts.added` becomes `useSeriesList({ scope: 'added', limit: 1 }).data?.total`, replacing today's whole-library `all.data?.total` and the comment explaining why it was wrong. |
| `Makefile` (`lint`) | **WP-00/WP-13** | Extend the SQL guard: reject any string containing `series_seen` together with `UPDATE`, `DELETE` or `DO UPDATE`. |
| `internal/httpapi/*` | **WP-12** | Parse and validate `scope`, pass the cutoff, surface `recently_added_days` in `Settings.server`, and add a `series_list_scope_added` golden file. This is the only part that is normal wave-3 work. |

#### E-14 follow-up work, file by file — **blocking hand-off**

Ruling **E-14** narrowed `series.status` to a three-row fold (arch §3.5): *no books at all* → `empty`;
*≥ 1 book, at least one `ok`* → `ok`; ***≥ 1 book, none of them `ok`* → `error`**. Rows 1 and 2 are what
`internal/scanner`'s `seriesStatus` already computes. Row 3 is complete for `error`/`encrypted`/
`unsupported` books but **not** for the case where every book is specifically `empty`: the fold still
returns `empty`. That is exactly D-10's `[만화] 엔젤하트 전32권 완결.zip` — a real 1.44 GB series of the
collection — and under E-14 the user must be shown an error with a reason (FR-IDX-010, design.md 화면 2)
rather than a grey 없음 badge.

The documents are now reconciled: arch §3.5, D-10, §6.2's I-10 and §6.3 all state `error` at the series
level and `empty` at the book level. **The code does not yet agree, and the E2E acceptance run will fail
its 엔젤하트 assertion until it does.** Each item below is owned by the package that owns the file; none of
it belongs to WP-16, which owns neither.

| File(s) | Owner | Change |
|---|---|---|
| `internal/scanner/classify.go` | **WP-08** | `seriesStatus`: the final `worst == StatusEmpty` branch must promote to `StatusError` when `len(results) > 0`. `empty` survives only for `len(results) == 0`. Keep the book-level verdict untouched — a nested-archive container is still an `empty` **book**. Update the doc comment, which currently cites D-10 for the pre-ruling answer. |
| `internal/scanner/classify_test.go` | **WP-08** | `TestSeriesStatus_foldsBookStatusesIntoTheSeriesStatus`: the case named *"a container of archives is empty, not an error"* asserts the pre-ruling answer. Rewrite it to assert `StatusError` with the book's message, and keep a separate zero-books case asserting `StatusEmpty` so the two rows stay distinguishable. |
| `internal/scanner/scanner_test.go` | **WP-08** | The `망가진 시리즈` **book** map (`"엔젤하트 전32권 완결.zip": StatusEmpty`) is correct and must not change; so is the `Errors == 4` count — `empty` is still not a scan failure. Add a series-level case: a series whose only book is `empty` reads `error`. |
| `integration/scan_test.go` | **WP-13** | Sub-test *"D-10 a container of sub-archives is empty, not broken"*: `mustSeries(s, angelHeart).Status` must be `"error"`, with a non-empty `Error`. Rename the sub-test; the D-10 limitation it proves is now "the nested archives are not read", not "the series looks empty". |
| `scripts/e2e-assert.py` | **WP-13** | The 엔젤하트 assertion must expect `status == "error"` and a non-empty `error` string. |

Nothing else moves: `books.status` keeps all five FR-IDX-010 values, the scan-error count is unchanged
(`empty` books are not failures), and no wire type changes — §7.3 already types `series.status` as the
three-value `ItemStatus` subset.

#### A-11 follow-up work, file by file

A-11 was ruled after every wave landed, so **none of it exists anywhere in the tree**; `internal/config` has no
`yaml.Marshal` in product code at all today. Each item is owned by the package that owns the file. Two rules
bind the whole list and are not negotiable per-file:

* **The endpoint must never write a file this server would refuse to start from.** `POST` therefore applies
  *every* root-related rejection of `internal/config/validate.go` before it writes — absolute `path`, an
  existing directory, no duplicate `name`, and `storage.data_dir`/`cache_dir` not inside the new root — plus
  A-11's own overlap rule; and `DELETE` refuses to remove the **last** root, because `validate.go` requires at
  least one and the UI would otherwise tell the user to restart into `exit 2`.
* **Only `internal/config` writes.** The HTTP layer calls it and never touches a file itself, so the
  `check-readonly` guard can assert that.

| File(s) | Owner | Change |
|---|---|---|
| `internal/config/config.go` (+ test) | **WP-01** | `Server.AllowRootEditing bool`, YAML key `allow_root_editing`, default **false**. One field; there is nothing to validate about a bool, and `KnownFields(true)` already rejects a typo. |
| `internal/config/rootsfile.go` (new) + test | **WP-01** | The comment-preserving writer: load the current file from disk, insert/remove one `roots[]` entry, write temp file **in the same directory** → `fsync` → `rename`, mode preserved, `.bak` of the previous contents. **Mandatory round-trip test against the real 14 KB `shelf.example.yaml`**: add a root then remove it, and the result must be byte-identical to the original. A writer that reformats the file destroys the documentation the product ships inside it. **MEASURED 2026-07-30, and the technique changed because of it:** this row said "over `yaml.Node` rather than the typed structs", but a `yaml.Unmarshal` → `yaml.Marshal` Node round trip of `shelf.example.yaml` yields **14 217 bytes / 252 lines from 15 281 bytes / 277 lines** (re-measured against the shipped file after A-11 added its own commented key; the pre-A-11 file measured 13 075 / 234 from 14 174 / 258 — the ratio is the point, not the absolute) — it drops every blank line inside the comment blocks, re-indents the whole document from 2 spaces to 4, and re-anchors continuation comment lines to the new indent so they no longer line up with the key they explain. `yaml.Node` is therefore used **only to locate** the `roots:` sequence and each entry's line span; the edit itself is a **surgical splice of the raw text lines**, and nothing outside the spliced range is ever re-emitted. |
| `internal/config/validate.go` | **WP-01** | Export the `isInside` helper (or a wrapper) so the HTTP layer can apply the storage-vs-roots and config-inside-root rules with the same code that enforces them at startup. Two implementations of one rule is how they diverge. |
| `shelf.example.yaml` | **WP-01** | The commented `allow_root_editing:` key in the `server:` block, verbatim from arch §3.2, so `config`'s existing round-trip test still passes — and so the switch is discoverable by the only person allowed to throw it. |
| `cmd/shelf/starter.yaml` | **WP-01** | **The same key, for the same reason, and it was missed the first time.** This is the file `--init-config` writes and the only configuration embedded in the binary, so it is the *only* thing a binary install ever sees; `shelf.example.yaml` lives in the source tree. Leaving the key out of it made "discoverable by the only person allowed to throw it" false for exactly the installation the row was written about. Same value (`false`), same warning in one paragraph rather than five, matching the file's terser voice. `cmd/shelf/main_test.go`'s starter-vs-defaults comparison stays green because the value written *is* the default. |
| `internal/httpapi/errors.go` | **WP-12** | `CodeForbidden = "forbidden"` and its `statusForCode` arm (403). |
| `internal/httpapi/dto.go` | **WP-12** | `RootCreate` (request), `RootEntry` (201 response), and `ServerSettings.RootEditingEnabled` / `.ConfigChangedOnDisk`. `RootEntry` is **not** §7.3's `Root`: the created root has no index row and no open handle, so `available`, the four counts and the scan timestamps would all be fabricated. **R2**: `Root.Pending bool` (`json:"pending"`), with the reason in the field comment. |
| `internal/httpapi/roots.go` | **WP-12** | The two handlers, the gate, the validator and the slug generator. **Its file comment currently states the old ruling** (*"Roots are read-only over HTTP: there is no POST and no DELETE, by ruling E-3"*) — replace it with E-26's terms, including why the change is not adopted until restart. **R1**: the mutex-protected removed-set, and `GET /api/roots` filtering on it. **R2**: `GET /api/roots` appends one pending row per configured-on-disk root with no index row. |
| `internal/httpapi/scan.go` | **WP-12** | **R1**: `POST /api/scan {roots:[name]}` naming a removed root is `404 not_found` — not the existing `400` for an unknown root, because the name *was* valid and the caller is not wrong about it; and a full scan (`roots` absent) passes the configured names minus the removed set, instead of `nil`. When nothing has been removed the request is left exactly as it is today, so the existing behaviour cannot regress. |
| `internal/httpapi/router.go` | **WP-12** | `/api/roots` gains `POST` (so a `GET`-only client now sees `405` + `Allow: GET, POST`), and a new `/api/roots/{name}` pattern carrying `DELETE` only. |
| `internal/httpapi/settings.go` | **WP-12** | `serverSettings()` computes both new fields. `config_changed_on_disk` re-hashes the file at `config_path` on every `GET /api/settings` and compares against the hash taken at load: a missing file is `true` (it differs), and `config_path == ""` is `false` (there is nothing to differ from). |
| `internal/app/app.go` | **WP-13** | Hash the configuration file at load and hand the digest to the HTTP server. Nothing else moves — in particular `source.OpenRoots` and `reconcileRoots` are untouched, which is the whole point of "restart-based". |
| `internal/httpapi/testdata/golden/` | **WP-12** | New `root_created.json` and `error_forbidden.json`; `settings.json` regenerated for the two new fields. Per ruling E-25's lesson, a golden file pins `false` as happily as `true` — the gate's tests must assert the 403 **and** the 201 against the same server with the key flipped, not one of them. |
| `web/src/api/types.ts` | **WP-06** | `ERROR_CODES` gains `'forbidden'`; `RootCreate`/`RootEntry`; the two `Settings['server']` booleans; **R2**'s `Root.pending`. *(Owned by the backend implementer in practice, because `make lint`'s contractcheck diffs it against the Go goldens.)* Adding three **required** fields breaks the object literals in `web/src/api/fixtures.ts` until WP-06 updates them — that is the gate working, not a defect. |
| `web/src/api/errors.ts` | **WP-06** | `STATUS_TO_CODE` gains `[403, 'forbidden']`. Leave `isAuthError` alone — a 403 must **not** trigger re-authentication (ruling E-17). |
| `web/src/api/urls.ts` / `queries.ts` / `fixtures.ts` | **WP-06** | The two mutations, invalidating the roots and settings queries; MSW fixtures for 201, 204, 400 and 403. |
| `web/src/features/overlays/RootsPanel.tsx` | **WP-10** | `+ 루트 추가` and `제거`, rendered **only** when `server.root_editing_enabled`; ~~the removal confirmation must state that the index rows and the reading progress are kept (ruling E-26, decision 3)~~ **REVISED (R1)**: the row disappears, so the confirmation must state that **the reading progress is kept** and must **not** promise the index rows are — they are purged; a restart notice driven by `server.config_changed_on_disk` that survives a browser reload because it is server state, not client state; **R2**: a row with `pending: true` renders as "재시작 후 적용" with no counts and no 재스캔 button. The panel already reads `useSettings()` directly (E-25) — keep it that way. |
| `web/src/features/library/Onboarding.tsx` | **WP-09** | Unchanged unless the capability is on; `설정 파일 위치 보기` stays the answer when it is off (C-5). Do not reintroduce a guessed default path — E-25 removed one. |
| `scripts/check-readonly.sh` | **WP-00/WP-13** | A fourth guard: no filesystem mutation primitive in `internal/httpapi`. The HTTP layer must reach the file only through `internal/config`, and a grep is what keeps that true across six agents. |
| `integration/` + `scripts/e2e.sh` | **WP-13** | Two behaviours no unit test can prove: the file round-trip against a real config, and **restart adoption** — `POST`, assert `GET /api/roots` ~~is *unchanged*~~ **(REVISED, R2)** now carries the root with `pending: true`, no counts and `available: false`; restart; assert the same row is now `pending: false` with real counts. The second half is the assertion that stops "restart-based" quietly becoming "never applied", and the first is what stops a pending row quietly becoming a lie. |

### 0.4 Thumbnail width derivation (closes arch OQ-7)

| Consumer | CSS px (ui-spec) | @2× DPR | Requests `w=` | Snaps to |
|---|---|---|---|---|
| Viewer thumbnail strip | 48 | 96 | 96 | **120** |
| List-row thumb | 24 | 48 | 48 | **120** |
| Continue card thumb | 66 | 132 | 132 | **240** |
| Slider drag preview | 68 | 136 | 136 | **240** |
| Volume tile (series detail) | 128 | 256 | 256 | **400** |
| Grid cover ≥1440 (`--grid-min:152`, renders 152–200) | ~178 | ~356 | 400 | **400** |
| Grid cover ≤768 (`--grid-min:224`) | 224 | 448 | 640 | **640** |
| Series-detail hero cover | 176 | 352 | 400 | **400** |

⇒ `widths: [120, 240, 400, 640]`. The frontend must send an explicit `w` from this set (see §5.4);
sending an arbitrary width is legal but wasteful because the server snaps **up**.

---

## 1. Scope for this build

**In scope: prd §9 stage 1 (MVP) + stage 2.** Stage 3 is out except where marked *trivially free*.
Every FR marked **필수** in stages 1–2 is in scope. Explicit per-FR list:

### 1.1 IN — functional requirements

| FR | Pri | Stage | Owner WPs | Note |
|---|---|---|---|---|
| FR-CFG-001 roots in YAML | 필수 | 1 | 01, 12 | |
| FR-CFG-002 per-root enable | 선택 | 1 | 01, 08, 12 | *trivially free* — one field + one filter |
| FR-CFG-003 cache dir / port / thumb size / workers | 필수 | 1 | 01 | |
| FR-CFG-004 path-derived `series_id` | 필수 | 1 | 02 | golden vectors pinned |
| FR-CFG-005 never write media volumes | 필수 | 1 | 04, 08, 13 | enforced by `make lint` grep guard + integration test |
| FR-IDX-001 scan all roots | 필수 | 1 | 08, 12 | |
| FR-IDX-002 central directory only | 필수 | 1 | 04, 08 | |
| FR-IDX-003 incremental scan | 필수 | 1 (opt. in 2) | 08 | |
| FR-IDX-004 live scan progress | 권장 | 1 | 08, 12, 09 | 1 s polling |
| FR-IDX-005 `--rebuild-index` | 필수 | 1 | 13, 03 | |
| FR-IDX-006 exclusion rules | 필수 | 1 | 08 | |
| FR-IDX-007 natural sort | 필수 | 1 | 02 | |
| FR-IDX-008 CP949 fallback | 필수 | 1 | 02 | **critical path** (D-1) |
| FR-IDX-009 ZIP64 | 필수 | 1 | 04 | synthetic fixture only (D-3) |
| FR-IDX-010 broken/encrypted isolation | 필수 | 1 | 04, 08, 12 | |
| FR-IDX-011 7 image extensions | 필수 | 1 | 04, 07 | AVIF guarded (D-2) |
| FR-SRV-001 stream from archive | 필수 | 1 | 04, 12 | |
| FR-SRV-002 seek by stored offset | 필수 | 1 | 04 | |
| FR-SRV-003 stored passthrough | 권장 | 1 | 04 | *free* |
| FR-SRV-004 handle LRU pool | 권장 | 1 | 04 | |
| FR-SRV-005 folder images | 필수 | 1 | 04, 12 | |
| FR-SRV-006 PDF rasterisation | 필수 | 2 | 04, 12 | |
| FR-SRV-007 ETag + immutable | 필수 | 1 | 12 | `?v=` makes `immutable` honest |
| FR-SRV-008 original bytes | 필수 | 1 | 12 | |
| FR-THM-001…007 cache, fan-out, cover-first, lazy pages, workers, invalidation, deletable | 필수 | 1 | 07 | |
| FR-THM-008 usage + purge UI | 권장 | 2 | 07, 12, 10 | |
| FR-LIB-001…005 grid, toggle, list columns, sorts, root filter | 필수 | 1 | 09, 12 | |
| FR-LIB-006 substring + 초성 search | 권장 | 2 | 02, 12, 09, 10 | server-side (C-10) |
| FR-LIB-007 virtualised list | 필수 | 1 | 09 | |
| FR-LIB-008 fallback cover | 필수 | 1 | 05, 09 | |
| FR-LIB-009 series detail volume list | 필수 | 1 | 10, 12 | |
| FR-LIB-010 이어보기 | 필수 | 1 | 09, 12 | |
| FR-LIB-011 command palette | 권장 | 2 | 10 | |
| FR-VWR-001 fullscreen reader | 필수 | 1 | 11 | |
| FR-VWR-002 reading direction, per book | 필수 | 1 | 11, 12 | |
| FR-VWR-003 single / spread / vertical | 필수 | 1 (vertical: 2) | 11 | |
| FR-VWR-004 landscape auto-split in spread | 권장 | 1 | 07, 11 | needs page dims (§5.8 of arch) |
| FR-VWR-005 fit modes | 필수 | 1 | 11 | |
| FR-VWR-006 prefetch 3–5 | 필수 | 1 | 11 | default 4 |
| FR-VWR-007 keyboard shortcuts | 필수 | 1 | 11 | incl. real `F` fullscreen |
| FR-VWR-008 thumbnail strip jump | 필수 | 1 | 11 | virtualised |
| FR-VWR-009 resume at last page | 필수 | 1 | 11, 12 | |
| FR-VWR-010 next volume | 필수 | 1 | 11, 12 | |
| FR-VWR-011 touch swipe + tap zones | 권장 | 1 | 11 | basic only; tuning is stage 3 |
| FR-VWR-012 completed auto + manual | 권장 | 1 | 10, 11, 12 | manual toggle lives on series detail + viewer end card |
| FR-STT-001 per-book progress | 필수 | 1 | 03, 12 | |
| FR-STT-002 series aggregate | 필수 | 1 | 03, 12 | |
| FR-STT-003 survives rebuild | 필수 | 1 | 02, 03, 13 | |
| FR-STT-004 export/import | 선택 | 3 | 03, 12 | **backend endpoints only** — *trivially free* (~80 lines) and it protects the only authored data. **No UI.** |

### 1.2 IN — non-functional

NFR-PRF-001…006, NFR-OPS-001/002/003/005/006, NFR-DAT-001…004, NFR-CMP-001/002/003,
NFR-SEC-001/002/003, CON-001…004. All in scope. Gates in §7.

### 1.3 OUT of scope

| Item | Why |
|---|---|
| **NFR-OPS-004 Docker image** | prd §9 stage 3, "부가적". `make release` cross-compilation (NFR-OPS-003) **is** in scope. |
| **FR-STT-004 export/import *UI*** | stage 3. API only (above). |
| **Mobile gesture *optimisation*** (pinch-zoom, momentum, edge-swipe tuning) | stage 3. Basic tap zones + horizontal swipe are in (FR-VWR-011). |
| **Archive format expansion** (RAR/CBR, 7z, nested ZIP) | prd §7.2 + stage 3. `internal/archive.Reader` is an interface so it can be added later; `.rar` files are logged and ignored. |
| **`duplicate_of` hints, manual volume reordering** | prd §10 stage 3. |
| **SSE `/api/scan/stream`** | C-11. |
| **Cache orphan sweeper / `cache_max_bytes`** | arch §5.6, phase 2 of that doc. Explicit purge only. |
| **`shelf migrate-root`** | arch §3.4 corollary, phase 3. The YAML comment must warn that renaming a root orphans its progress. |
| **Built-in TLS** | arch OQ-8. Reverse proxy + `base_path` only. |
| **Root add/remove from UI** | C-5. |
| **External metadata, multi-user, collection editing, OPDS, upscaling** | prd §7.2. |

---

## 2. File tree

Every file, its owning work package, and its purpose. **Ownership is exclusive.**

```
zip-viewer/
├── go.mod                                  [WP-00] module shelf, go 1.25.0  (bare path — ruling E-1)
├── go.sum                                  [WP-00] pinned deps (arch §1.1 versions, exact)
├── Makefile                                [WP-00] web/build/dev/test/test-int/bench/lint/fmt/tidy/release/e2e/clean
├── shelf.example.yaml                      [WP-00] fully commented config = arch §3.2 + amendments A-1..A-3
├── .gitignore                              [WP-00] web/node_modules, web/dist/*, dist/, *.db*, .e2e/
├── README.md                               [WP-00] build + run in 20 lines; points at docs/
├── docs/…                                  [—]     existing; only WP-13 may append an E2E results section
│
├── cmd/shelf/
│   ├── main.go                             [WP-13] entrypoint: flags → config → app.New → serve → graceful shutdown
│   ├── flags.go                            [WP-13] --config --init-config --rebuild-index --log-level --version --port
│   └── subcommands.go                      [WP-13] `hash-password`, `--init-config` writer, `--rebuild-index` runner
│
├── internal/
│   ├── app/app.go                          [WP-13] composition root: opens DBs, builds pools/scanner/thumbs/router
│   ├── buildinfo/buildinfo.go              [WP-00] Version/Commit/Date vars set by -ldflags
│   ├── testutil/
│   │   ├── zipbuild.go                     [WP-00] BuildZIP(tb, spec) — full control of GP flags, method, raw name bytes, comment length
│   │   ├── zip64build.go                   [WP-00] BuildZIP64(tb, spec, z64) — hand-written ZIP64 EOCD + 0x0001 extra field
│   │   ├── imgfixture.go                   [WP-00] TinyJPEG/PNG/GIF/BMP/TIFF/WebP/AnimatedWebP/AVIF byte fixtures (<1 KB each)
│   │   └── tree.go                         [WP-00] BuildTree(tb, layout)/BuildTreeAt/Touch — a series tree in t.TempDir()
│   │
│   ├── config/
│   │   ├── config.go                       [WP-01] YAML structs + Load() + lookup order (arch §3.1)
│   │   ├── defaults.go                     [WP-01] every default incl. A-1/A-2, OS-specific data/cache dirs
│   │   ├── validate.go                     [WP-01] fail-fast validation, exit-2 messages (arch §3.2)
│   │   ├── paths.go                        [WP-01] XDG/darwin/windows dir resolution, base_path normalisation
│   │   └── config_test.go                  [WP-01] defaults, lookup order, every rejection, example.yaml round-trip
│   │
│   ├── ids/{ids.go,ids_test.go}            [WP-02] SeriesID/BookID/ThumbKey/PDFPageKey/NormalizeRel/Valid + IDVersion — exact hash input of arch §3.4
│   ├── natsort/{natsort.go,key.go,natsort_test.go}   [WP-02] Compare() + Key() + agreement property test
│   ├── hangul/{choseong.go,choseong_test.go}         [WP-02] choseong extraction (arch §4.8)
│   ├── kenc/{kenc.go,kenc_test.go}                   [WP-02] ZIP entry-name decode: UTF-8 / CP949 rule (arch §4.4)
│   │
│   ├── index/                              *** index.db — derived, disposable ***
│   │   ├── schema.go                       [WP-03] DDL string of arch §3.5, verbatim
│   │   ├── open.go                         [WP-03] DSN + pragmas + ATTACH hook registration + pool sizing
│   │   ├── migrate.go                      [WP-03] meta.schema_version / id_version; refuse unknown-newer
│   │   ├── series.go                       [WP-03] list/detail queries incl. filters, sorts, progress join, A-4
│   │   ├── books.go                        [WP-03] book detail, prev/next by ord, per-series listing
│   │   ├── pages.go                        [WP-03] page range reads, dims update
│   │   ├── scanlog.go                      [WP-03] append + query + 5 000-row retention trim
│   │   ├── write.go                        [WP-03] single-writer tx API used by the scanner (upsert series/book/pages, gen sweep)
│   │   └── index_test.go                   [WP-03] schema applies, WAL on, queries, cross-db join
│   ├── userdata/                           *** user.db — authored, never rebuilt ***
│   │   ├── schema.go                       [WP-03] DDL of arch §3.6
│   │   ├── open.go                         [WP-03] separate handle, WAL, pool 4
│   │   ├── progress.go                     [WP-03] get/put/delete, series aggregation, continue list
│   │   ├── prefs.go                        [WP-03] per-book prefs with null-clears-override semantics
│   │   ├── settings.go                     [WP-03] settings + view_state key/value store
│   │   ├── export.go                       [WP-03] ProgressExport marshal/merge (FR-STT-004)
│   │   └── userdata_test.go                [WP-03] survives index deletion; merge-by-updated_at
│   │
│   ├── archive/
│   │   ├── archive.go                      [WP-04] Reader{Format,ReadIndex,OpenEntry}/Entry/EntryRef/Index/Status — the prd §7.2 abstraction seam
│   │   └── zipidx/
│   │       ├── centraldir.go               [WP-04] EOCD locate (1 KiB then 65 557 B tail) + CD parse
│   │       ├── zip64.go                    [WP-04] ZIP64 EOCD locator/record + 0x0001 extra field
│   │       ├── open.go                     [WP-04] OpenEntry(ctx, ReaderAt, archive.EntryRef) + DataOffset — 30-byte LFH read, stored/deflate
│   │       ├── errors.go                   [WP-04] typed errors mapping to books.status (arch §4.11)
│   │       ├── centraldir_test.go          [WP-04] hand-built fixtures incl. truncated / bogus EOCD / 40 KiB comment
│   │       ├── differential_test.go        [WP-04] parity vs archive/zip incl. DataOffset + error verdict (C-6)
│   │       └── testdata/                   [WP-04] 3 exact malformed archives (bad EOCD, truncated CD, big comment)
│   ├── openpool/{pool.go,pool_test.go}     [WP-04] LRU *os.File pool, refcounted, never closes under a live stream
│   ├── source/
│   │   ├── source.go                       [WP-04] BookSource interface + registry + ErrUnsupported
│   │   ├── zipsource.go                    [WP-04] pages from index rows → OpenEntry
│   │   ├── dirsource.go                    [WP-04] loose files via os.Root (traversal layer 3)
│   │   ├── pdfsource.go                    [WP-04] //go:build !nopdf — page count + rasterise
│   │   ├── pdfsource_stub.go               [WP-04] //go:build nopdf — returns ErrUnsupported
│   │   └── source_test.go                  [WP-04] byte-identity + CRC check per source kind
│   ├── pdfium/
│   │   ├── pool.go                         [WP-04] //go:build !nopdf — lazy wazero pool, compilation cache, idle teardown
│   │   ├── render.go                       [WP-04] //go:build !nopdf — RenderPageInPixels + mandatory Cleanup()
│   │   └── pool_stub.go                    [WP-04] //go:build nopdf
│   │
│   ├── thumbs/
│   │   ├── cache.go                        [WP-07] hash input (arch §5.6), 2-level fan-out path, atomic publish
│   │   ├── generate.go                     [WP-07] decode → imaging.Lanczos → jpeg.Encode; format policy of arch §5.5
│   │   ├── worker.go                       [WP-07] coverQ/pageQ, single-flight, AVIF semaphore, negative cache
│   │   ├── dims.go                         [WP-07] page width/height fill (free on thumb, background pass) — FR-VWR-004
│   │   ├── usage.go                        [WP-07] 60 s-cached usage walk + purge by kind (FR-THM-008)
│   │   └── thumbs_test.go                  [WP-07] path derivation, cv invalidation, single-flight, atomic rename
│   │
│   ├── scanner/
│   │   ├── scanner.go                      [WP-08] pipeline: walker → workers → single writer; context cancel
│   │   ├── classify.go                     [WP-08] prd §2.2 table for a root's direct child
│   │   ├── collect.go                      [WP-08] collectBooks() incl. the D-5 cover rule and max_depth
│   │   ├── incremental.go                  [WP-08] (mtime,size) skip + FNV-1a dir fingerprint (FR-IDX-003)
│   │   ├── exclude.go                      [WP-08] FR-IDX-006 rules + include_globs (A-3) + exclude_globs
│   │   ├── cover.go                        [WP-08] cover selection ladder (arch §4.10) + coverQ enqueue
│   │   ├── progress.go                     [WP-08] atomic.Pointer[ScanStatus] snapshot, 200 ms updates
│   │   ├── gen.go                          [WP-08] scan_gen stamping + per-root sweep; never sweeps an unreachable root
│   │   ├── classify_test.go                [WP-08] every prd §2.2 row + every real shape from data-survey
│   │   └── scanner_test.go                 [WP-08] incremental skip, cancel, error isolation, gen sweep
│   │
│   ├── auth/{auth.go,session.go,ratelimit.go,auth_test.go}   [WP-12] bcrypt verify, HMAC cookie, per-IP token bucket
│   └── httpapi/
│       ├── deps.go                         [WP-12] narrow consumer-side interfaces for index/userdata/scanner/thumbs/source
│       ├── router.go                       [WP-12] ServeMux, base_path StripPrefix, SPA fallback vs JSON 404
│       ├── middleware.go                   [WP-12] request id, slog, recover, auth gate, security headers, body caps
│       ├── errors.go                       [WP-12] the single §7.2 envelope + code→status mapping
│       ├── dto.go                          [WP-12] every struct of arch §7.3–§7.12 with exact json tags
│       ├── spa.go                          [WP-12] embed serving, hashed-asset immutable headers, <base href> injection
│       ├── health.go                       [WP-12] /api/health (+?verbose=1 pool counters)
│       ├── roots.go                        [WP-12] GET /api/roots
│       ├── series.go                       [WP-12] list (filters/sorts/A-4), detail, cover, rescan
│       ├── books.go                        [WP-12] book detail, prefs GET/PUT
│       ├── pages.go                        [WP-12] the hot path: ETag/304/206, ?v=, PDF ?w=, 409 stale
│       ├── thumbs.go                       [WP-12] page thumbs + 202/422 semantics
│       ├── progress.go                     [WP-12] PUT/DELETE progress, export, import
│       ├── continueread.go                 [WP-12] GET /api/continue
│       ├── settings.go                     [WP-12] GET/PUT settings (user-mutable only)
│       ├── cache.go                        [WP-12] GET usage, DELETE by kind
│       ├── scan.go                         [WP-12] POST scan/cancel, GET status/log
│       ├── authhandlers.go                 [WP-12] /api/auth/{status,login,logout}
│       ├── golden_test.go                  [WP-12] every endpoint via httptest + JSON golden diffing
│       └── testdata/golden/*.json          [WP-12] the contract snapshots the frontend can diff against
│
├── integration/                            *** -tags integration, gated on SHELF_TEST_ROOT ***
│   ├── scan_test.go                        [WP-13] full scan of the curated root; counts, error rate, CP949 names
│   ├── acceptance_test.go                  [WP-13] AC-001..AC-008 as Go tests
│   └── perf_test.go                        [WP-13] NFR-PRF-004 rescan <30 s; AC-008 p95 TTFB
│
├── scripts/
│   ├── e2e.sh                              [WP-13] build → write config → start server → scan → playwright → teardown
│   ├── e2e-config.sh                       [WP-13] emits test/shelf.e2e.yaml with the §6.3 allowlist
│   └── check-readonly.sh                   [WP-13] the FR-CFG-005 grep guard invoked by `make lint`
│
├── test/
│   └── shelf.e2e.yaml.tmpl                 [WP-13] template for the curated-subset config
│
└── web/                                    *** simultaneously a Vite project and a Go package ***
    ├── embed.go                            [WP-00] package web; //go:embed all:dist; Dist() fs.FS
    ├── package.json                        [WP-00] deps + scripts (dev/build/test/typecheck/lint/e2e)
    ├── pnpm-lock.yaml                      [WP-00] committed lockfile
    ├── vite.config.ts                      [WP-00] react plugin, /api proxy → :8790, build.outDir=dist, base:'./'
    ├── tsconfig.json                       [WP-00] strict + noUncheckedIndexedAccess + verbatimModuleSyntax
    ├── tsconfig.node.json                  [WP-00] for vite/tailwind configs
    ├── eslint.config.js                    [WP-00] typescript-eslint strict; bans `any`, default exports, `rounded-*`
    ├── vitest.config.ts                    [WP-00] jsdom env, setup file path
    ├── playwright.config.ts                [WP-00] channel:'chrome', baseURL from env, 4 viewport projects
    ├── index.html                          [WP-00] #root, <base href="/"> placeholder the server rewrites
    ├── dist/.gitkeep                        [WP-00] guarantees //go:embed all:dist always matches
    ├── postcss.config.js                   [WP-05] tailwind + autoprefixer
    ├── tailwind.config.ts                  [WP-05] the ui-spec §1.3 mapping, borderRadius OVERRIDDEN
    └── src/
        ├── main.tsx                        [WP-05] boot: read <base href>, mount router, QueryClientProvider
        ├── App.tsx                         [WP-05] shell: auth gate → onboarding → sidebar+topbar+outlet+portals
        ├── router.tsx                      [WP-05] createBrowserRouter, basename from base href
        ├── styles/tokens.css               [WP-05] ui-spec §1.2 + §1.4 verbatim, light + dark, --grid-min queries
        ├── styles/base.css                 [WP-05] ui-spec §2.4 app-shell CSS, scrollbars, range input, keyframes
        ├── styles/fonts.css                [WP-05] @fontsource/archivo 400/600/800 latin+latin-ext (C-14)
        ├── lib/basePath.ts                 [WP-05] resolve base path once from <base href>
        ├── lib/format.ts                   [WP-05] bytes/dates/percent/page-counter formatters (ui-spec §9)
        ├── lib/chosung.ts                  [WP-05] client chosung() — highlighting only (C-10)
        ├── lib/cn.ts                        [WP-05] class joiner
        ├── lib/useHotkeys.ts               [WP-05] global key dispatcher (Ctrl/Cmd+K, ?, Esc ladder)
        ├── lib/useMediaQuery.ts            [WP-05] breakpoint hook for the §7 responsive layer
        ├── store/ui.ts                     [WP-05] zustand: theme, view mode, scope, drawer, palette/settings/shortcuts open
        ├── store/viewer.ts                 [WP-05] zustand: page, chrome visibility, mode/dir/fit, strip open, dragging
        ├── components/ds/*.tsx             [WP-05] Button Tag Input Seg Radio Card Dialog Hr Table ProgressBar FormatBadge FallbackCover Skeleton EmptyState Spinner VisuallyHidden
        ├── components/shell/Sidebar.tsx    [WP-05] roots + smart lists + footer (scan indicator, 설정, ?)
        ├── components/shell/TopBar.tsx     [WP-05] back, search, scan progress, sort, view toggle
        ├── components/shell/ScanIndicator.tsx [WP-05] dot + label, polls scan status
        ├── components/shell/MobileDrawer.tsx  [WP-05] off-canvas sidebar <768 (ui-spec §7)
        ├── features/auth/LoginScreen.tsx   [WP-05] shown when auth_required && !authenticated
        ├── api/types.ts                    [WP-06] the frozen contract as TS types — arch §7 + §0.3 amendments
        ├── api/urls.ts                     [WP-06] URL builders incl. ?v= and ?w= discipline (§0.4, §5.4)
        ├── api/errors.ts                   [WP-06] ApiError class carrying code/status/detail
        ├── api/client.ts                   [WP-06] **the single typed fetch client** — every request goes through here
        ├── api/queries.ts                  [WP-06] TanStack Query hooks + keys + 202-retry + poll policies
        ├── api/client.test.ts              [WP-06] MSW-backed tests for envelope, 202, 409, 401
        ├── features/library/*.tsx|ts       [WP-09] LibraryPage SeriesGrid SeriesCard SeriesList SeriesRow ContinueRow ContinueCard SectionHeader GridSkeleton Onboarding useLibrary.ts
        ├── features/series/*.tsx           [WP-10] SeriesDetailPage SeriesHeader VolumeGrid VolumeTile VolumeList VolumeRow
        ├── features/overlays/*.tsx         [WP-10] CommandPalette SettingsDialog ShortcutsDialog RootsPanel CachePanel ScanLogPanel ReadDefaultsPanel
        └── features/viewer/*.tsx|ts        [WP-11] ViewerPage PageStage PageFrame ViewerTopBar ViewerBottomBar PageSlider ThumbnailStrip NextVolumeCard fit.ts useViewerKeys.ts usePrefetch.ts useTouchZones.ts useProgressSync.ts
```

---

## 3. Work packages

14 packages in 5 waves. **Wave N may only depend on waves < N.** Inside a wave, packages are independent
and may run fully in parallel.

### Wave 0

#### WP-00 — Bootstrap & fixtures
*Blocks everything. Keep it to one hour.*

| | |
|---|---|
| **Owns** | `go.mod` `go.sum` `Makefile` `shelf.example.yaml` `.gitignore` `README.md` `internal/buildinfo/*` `internal/testutil/*` `web/embed.go` `web/package.json` `web/pnpm-lock.yaml` `web/vite.config.ts` `web/tsconfig*.json` `web/eslint.config.js` `web/vitest.config.ts` `web/playwright.config.ts` `web/index.html` `web/dist/.gitkeep` |
| **Depends on** | — |
| **FRs** | NFR-OPS-001 (embed skeleton), CON-001 (build flags) |

**Acceptance**
1. `GOPROXY=… GOTOOLCHAIN=auto CGO_ENABLED=0 go build ./...` succeeds on a clean checkout with `web/dist/` containing only `.gitkeep`.
2. `go.mod` pins **exactly** the arch §1.1 versions: `modernc.org/sqlite v1.54.0`, `golang.org/x/text v0.40.0`, `golang.org/x/image v0.44.0`, `github.com/disintegration/imaging v1.6.2`, `github.com/gen2brain/avif v0.6.0`, `github.com/klippa-app/go-pdfium v1.19.6`, `github.com/tetratelabs/wazero v1.12.0`, `gopkg.in/yaml.v3 v3.0.1`, `golang.org/x/crypto v0.54.0`. No router library. `go 1.25.0`.
3. `make` targets exist and are wired exactly as arch §11 (`web build dev test test-int bench lint fmt tidy release clean` + `e2e`), all `go` calls carrying `GOPROXY=https://proxy.golang.org,direct GOSUMDB=sum.golang.org GOTOOLCHAIN=auto CGO_ENABLED=0`.
4. `cd web && pnpm install && pnpm build && pnpm typecheck` succeeds against an empty `src/` stub (a single `main.tsx` printing nothing is fine and is then replaced by WP-05 — **WP-00 must not create `src/main.tsx`; it creates `src/.gitkeep`** and `vite.config.ts` tolerates it).
5. `testutil.BuildZIP` can produce: stored + deflate entries, GP bit 0 (encrypted), GP bit 11 set/clear, arbitrary **raw name bytes** (so CP949 golden vectors are byte-exact), a 40 KiB archive comment, a truncated tail, a 0-byte entry, a directory entry, `__MACOSX/…` and `Thumbs.db` entries. `BuildZIP64` forces the ZIP64 EOCD + `0x0001` extra field on a tiny archive. Round-trip test: every generated archive opens in `archive/zip` with the expected entry set (except the deliberately broken ones).
6. `shelf.example.yaml` reproduces arch §3.2 **plus** A-1, A-2, A-3, and carries the warning comment that renaming a root orphans its progress.

---

### Wave 1 — six independent packages

#### WP-01 — Config

| | |
|---|---|
| **Owns** | `internal/config/*` |
| **Depends on** | WP-00 |
| **FRs** | FR-CFG-001, -002, -003; NFR-SEC-003 (base_path normalisation); NFR-OPS-002 |

**Acceptance**
1. Lookup order of arch §3.1 exactly; nothing merged; `--config` missing ⇒ fatal.
2. Every default from arch §3.2 with A-1 (`widths [120,240,400,640]`), A-2 (`fit_mode height`), A-3 (`include_globs []`). `workers: 0` resolves to `min(8, max(2, NumCPU/2))`; `thumbnails.workers: 0` → `min(4, NumCPU)`.
3. Every validation rejection listed in arch §3.2 exits 2 with a message naming the offending key **and** its value. Table-driven test with one case per rejection.
4. `data_dir`/`cache_dir` resolve per-OS (XDG / `~/Library/…` / `%LOCALAPPDATA%`) and are created 0700 if missing; unwritable ⇒ validation error.
5. `base_path` normalises `reader` → `/reader`, `/reader/` → `/reader`, `""` → `""`; `..` rejected.
6. `shelf.example.yaml` parses and — with every commented key uncommented — round-trips to the documented defaults (test reads the real file).

#### WP-02 — Text primitives (ids · natsort · hangul · kenc)

| | |
|---|---|
| **Owns** | `internal/ids/*` `internal/natsort/*` `internal/hangul/*` `internal/kenc/*` |
| **Depends on** | WP-00 |
| **FRs** | FR-CFG-004, FR-IDX-007, FR-IDX-008, FR-LIB-006, FR-STT-003, AC-002, AC-006 |

**Acceptance**
1. `ids`: the exact hash input string of arch §3.4 — `IDVersion ‖ 0x00 ‖ domain ‖ 0x00 ‖ root name ‖ 0x00 ‖ NormalizeRel(rel)`, with `IDVersion = "shelf-id/1"` and `domain ∈ {"series","book"}`. Golden vectors **must** reproduce `SeriesID("mangga","[만화] 군계 1~25") == "ruzwlotzngls2ua5"` and `BookID("mangga","[만화] 군계 1~25/군계(軍鷄) 01권.zip") == "yvtfrny77ehkt2we"`. Backslash and slash forms of the same rel path produce the same id. `series` and `book` domains never collide for the same rel path. A second test reconstructs the hash input from literals so the *construction* is pinned, not just the two strings. **Landed in wave 1 — `internal/ids` is now authoritative for this scheme; do not re-derive it from prose.** (The pre-2026-07-28 literals `gzj75n6x7rir6but` / `ox74tfcrwwnfopch` were the untagged spike values; see the erratum in arch §3.4.)
2. `natsort`: every row of arch §4.7's verified output table passes verbatim. Property test over ≥100 000 generated strings (digits, zero-padding, ASCII case, Hangul, Hanja, fullwidth): `sign(Compare(a,b)) == sign(bytes.Compare(Key(a),Key(b)))`. Total-order laws (antisymmetry, transitivity) asserted. 22-digit numbers do not overflow. `Key` output contains no invalid-for-BLOB constraint (any bytes allowed).
3. `kenc.DecodeEntryName` implements the four-step rule of arch §4.4 **with step 2 (valid-UTF-8 probe) before CP949**. Golden vectors: `"\xbd\xb4\xc6\xdb\xb8\xb8\xc8\xad\xb5\xa5\xbb\xfd"` → `"슈퍼만화데생"`; `"\xc7\xd1\xb1\xdb.jpg"` → `"한글.jpg"`; a UTF-8 Korean name **without** the flag returns the UTF-8 string, not mojibake. A regression test asserts `korean.EUCKR.NewDecoder()` still returns a `nil` error on garbage bytes (so the U+FFFD content check cannot be deleted as "dead code").
4. `hangul.Choseong`: `강철의 연금술사`→`ㄱㅊㅇ ㅇㄱㅅㅅ`, `군계`→`ㄱㄱ`, `20세기소년`→`20ㅅㄱㅅㄴ`, `Attack on Titan`→`attack on titan`. Jamo passthrough; non-Hangul lowercased.
5. Zero non-stdlib imports except `golang.org/x/text`.

#### WP-03 — Storage layer (index.db + user.db)

| | |
|---|---|
| **Owns** | `internal/index/*` `internal/userdata/*` |
| **Depends on** | WP-00 |
| **FRs** | FR-IDX-005, FR-STT-001..004, NFR-DAT-001..004, FR-LIB-003/004/005, A-4, A-5 |

**Acceptance**
1. Both schemas apply exactly as arch §3.5/§3.6 (column names, types, `WITHOUT ROWID` on `pages`, every index). `PRAGMA journal_mode` returns `wal` on both.
2. `ATTACH` of `user.db` onto every index connection via `sqlite.RegisterConnectionHook`; a test runs 64 goroutines against an 8-connection pool doing cross-DB joins with 0 errors.
3. **No transaction spans both databases** — enforced by code review and by a `make lint` grep asserting no SQL literal containing `ud.` also contains `INSERT|UPDATE|DELETE`.
4. `(*index.DB).ListSeries(ctx, index.SeriesFilter) (index.SeriesList, error)` supports: `root[]`, `q` (search_key OR choseong_key when the query is jamo/ASCII), `status`, **`progress` (A-4)**, `sort` ∈ {name,mtime,recent,size,books,added}, `order`, `offset`, `limit`; returns `total` before paging. `sort=name` orders by the stored `sort_key` BLOB under BINARY collation (no Go-side re-sort). Benchmark: 1 000 series with the progress join < 20 ms.
5. `index.Writer` exposes a single-writer transactional API: `UpsertSeries`, `UpsertBook`, `ReplacePages(bookID, []Page)`, `StampGen`, `SweepRoot(rootName, gen)`, committing every 200 books or 2 s.
6. `userdata` survives deleting `index.db*`: test writes progress + prefs, deletes the index files, reopens, reads them back identically (AC-006 unit half).
7. `ProgressExport` round-trips; import merges by `book_id` keeping the newer `updated_at`; `?strategy=replace` overwrites.

#### WP-04 — Archive engine & book sources

| | |
|---|---|
| **Owns** | `internal/archive/*` `internal/openpool/*` `internal/source/*` `internal/pdfium/*` |
| **Depends on** | WP-00, WP-01 (config types), WP-02 (`kenc`, `natsort`) |
| **FRs** | FR-IDX-002, -009, -010, -011; FR-SRV-001, -002, -003, -004, -005, -006; CON-004; NFR-PRF-006; NFR-SEC-001 (layer 3); AC-001 |

**Acceptance**
1. `zipidx.ReadCentralDirectory` uses the two-step tail scan (1 KiB then 65 557 B) and averages **≤ 2 `ReadAt` calls and ≤ 16 KB read per archive** on the fixture corpus. Never reads an entry payload.
2. ZIP64: locator + record parsed; the `0x0001` extra field is consumed **in fixed order** (uncompressed → compressed → local-header-offset → disk) and only for slots that held `0xffffffff`. Verified against `testutil.BuildZIP64`.
3. Differential test vs `archive/zip`: identical entry count, names (after `kenc` decode of raw bytes vs stdlib's own decode where flags agree), method, CRC, sizes; `DataOffset()` parity; **and the error verdict must agree** on every fixture. When `SHELF_TEST_ROOT` is set, the same test runs over every archive under it.
4. `OpenEntry` reads exactly `comp_size + 30` bytes for a page (test asserts via a counting `ReaderAt`); CRC-32 of the inflated stream equals the central-directory CRC for the first, middle and last page of a real archive. `method 0` returns the section reader unwrapped (FR-SRV-003).
5. `openpool`: hit moves to MRU and increments refs; eviction never closes an fd with `refs > 0`; `Invalidate(path)` drops the entry; a test runs 8 goroutines × 300 page reads through one handle with zero errors and no `Seek` usage.
6. `dirsource` opens **only** through `os.Root`; tests assert `../secret`, an absolute path, a symlink pointing outside, and a symlink to `..` are all refused, while `sub/../ok.txt` is allowed.
7. `pdfsource` (non-`nopdf`) opens a document via `FileReader` without slurping (assert peak RSS growth < 16 MiB for a 36 MB PDF), returns the page count, renders at a requested width, and **always** calls `res.Cleanup()`. With `-tags nopdf` the package compiles and every entrypoint returns `ErrUnsupported`.
8. `make lint`'s `check-readonly` passes: no `os.Create/Remove/Rename/Mkdir/WriteFile/OpenFile/Chtimes/Chmod/Truncate` anywhere in `internal/{archive,openpool,source}`.

#### WP-05 — Frontend foundation (tokens · DS components · shell · routing · theme)

| | |
|---|---|
| **Owns** | `web/tailwind.config.ts` `web/postcss.config.js` `web/src/main.tsx` `web/src/App.tsx` `web/src/router.tsx` `web/src/styles/*` `web/src/lib/*` `web/src/store/*` `web/src/components/ds/*` `web/src/components/shell/*` `web/src/features/auth/LoginScreen.tsx` |
| **Depends on** | WP-00 |
| **FRs** | NFR-CMP-002, NFR-CMP-003, NFR-SEC-002 (login screen), NFR-SEC-003 (base path), FR-LIB-008 (FallbackCover), FR-VWR-007 (global hotkeys) |

**Acceptance**
1. `tokens.css` reproduces ui-spec §1.2 **and** §1.4 exactly, including the semantic tokens that flip (`--ink*`, `--rule*`, `--fill-*`, `--nav-active`, `--accent-hover/press/text`) while the raw ramps stay constant. `--grid-min` is set from **one** media-query block (152/150/224/150 per ui-spec §7).
2. `tailwind.config.ts` **overrides** `borderRadius` to `{none,DEFAULT,full}` — an ESLint/stylelint rule or a build-time grep fails the build if `rounded-sm|md|lg|xl|2xl|3xl` appears anywhere in `src/`.
3. Theme: `data-theme` on `<html>` from the user setting with `system` following `prefers-color-scheme`. A `<div data-theme="dark">` wrapper re-scopes tokens (used by the viewer) and is verified by a test asserting `getComputedStyle` inside it resolves `--color-bg` to `#201e1d` **in both app themes**.
4. Every DS component matches the class contracts in ui-spec §2.3 geometry-for-geometry. `Button` with `block` sets `justify-content:flex-start` (the flush-left rule). Zero hardcoded hex in any component — enforced by a grep test.
5. `:focus-visible` is a 2px accent ring at 2px offset; `:focus` has no outline; disabled is `opacity:.45; cursor:not-allowed`.
6. Sidebar and TopBar implement the ≥1440 / 1024 / 768 / <768 behaviours of ui-spec §7 (240px fixed → 56px icon rail → off-canvas drawer). At every breakpoint from 320 px to 1920 px, `document.body.scrollWidth <= clientWidth` (no horizontal page scroll) — asserted in the E2E suite.
7. Routing: `createBrowserRouter` with `basename` read **synchronously** from `<base href>` before the router is created. Routes `/`, `/series/:sid`, `/series/:sid/books/:bid`. Palette/settings/shortcuts are state, not routes.
8. Fonts are vendored (`@fontsource/archivo` 400/600/800, latin + latin-ext). A build assertion fails if `dist/` contains any `https://fonts.g` string.
9. Touch targets ≥ 44×44 CSS px below 768 px.

#### WP-06 — Typed API client (the contract boundary)

| | |
|---|---|
| **Owns** | `web/src/api/*` |
| **Depends on** | WP-00 |
| **FRs** | all frontend FRs, transitively. This package is what lets frontend and backend be built simultaneously. |

**Acceptance**
1. `types.ts` mirrors arch §7.3–§7.12 **verbatim**, with §0.3 amendments applied and C-1..C-4 enum values. No optional-vs-null drift: a field the server sends as `null` is typed `T | null`, never `T | undefined`.
2. `client.ts` is the **only** module in the app that calls `fetch`. An ESLint rule (`no-restricted-globals: fetch`) enforces it outside `src/api/`. It: prefixes `basePath`, sets `Accept: application/json`, sends `credentials: 'same-origin'`, parses the §7.2 error envelope into a typed `ApiError {status, code, message, detail}`, and rejects with it on any non-2xx.
3. `urls.ts` builds page/thumb/cover URLs and **always** appends `?v={cv}` and an explicit `w` from `[120,240,400,640]` (§0.4). A unit test asserts the produced URL for each consumer.
4. `queries.ts` exposes typed TanStack Query hooks: `useRoots`, `useSeriesList`, `useSeries`, `useBook`, `useContinue`, `useSettings`, `useScanStatus`, `useScanLog`, `useCacheUsage`, `useAuthStatus`, and mutations `useSaveProgress` (debounced 1 s, idempotent), `useSetPrefs`, `useSaveSettings`, `useStartScan`, `useRescanSeries`, `useCancelScan`, `usePurgeCache`. **As landed (wave 1) the module also exports** `useHealth`, `useSeriesListInfinite` (the paged source for the virtualised library grid, FR-LIB-007), `useBookPrefs`, `useDeleteProgress`, `useLogin`, `useLogout`, and the two image hooks `useCoverImage(sid, {w, v})` / `usePageThumbImage(bid, n, {w, v})`, which return an `ImageState` of `loading | queued | ready | unavailable` and encapsulate the whole `202 + Retry-After` dance. **WP-09/10/11 must use these rather than hand-rolling `<img>` retry logic.** Policies: `useScanStatus` polls **1 000 ms while `state !== "idle"`**, stops otherwise; a `202` on an image endpoint retries after `Retry-After` (max 10 tries, then falls back to the placeholder); a `409 stale_version` invalidates the book query and retries once; a `401` flips the app into the login screen.
5. Tests use MSW against **the golden JSON files produced by WP-12** once available; until then, hand-written fixtures matching arch §7 shapes. `pnpm typecheck` is clean under `strict` + `noUncheckedIndexedAccess` + `exactOptionalPropertyTypes`.

---

### Wave 2 — five independent packages

#### WP-07 — Thumbnails, cache and page dimensions

| | |
|---|---|
| **Owns** | `internal/thumbs/*` |
| **Depends on** | WP-01, WP-03, WP-04 |
| **FRs** | FR-THM-001..008, FR-IDX-011 (decode side), FR-VWR-004 (dims), CON-003 |

**Acceptance**
1. Cache path is exactly `<cache_dir>/thumbs/<h[0:2]>/<h[2:4]>/<h>.jpg` where `h` is the 16-char base32 of SHA-256's first 10 bytes over the arch §5.6 input string including `content_version`, format and quality. Rendered PDF pages use `<cache_dir>/pdf/` with domain `shelf-pdfpage/1`.
2. **FR-THM-006 is structural**: a changed `content_version` yields a different path. Test asserts no invalidation code exists — changing `cv` alone changes the path.
3. Publish is `write .tmp` + `os.Rename`. A concurrency test with 16 readers racing a writer never observes a truncated JPEG.
4. Single-flight: N concurrent misses for the same key perform exactly **one** decode (counter assertion).
5. Two queues: `coverQ` unbounded and drained first (FR-THM-003), `pageQ` bounded drop-oldest (FR-THM-004). `thumbnails.workers` bounds concurrency; AVIF decode holds a **1-permit** semaphore regardless.
6. Format policy of arch §5.5: still WebP/BMP/TIFF/AVIF decode; **animated WebP returns a typed `ErrUndecodable{reason:"animated_webp"}`** which WP-12 maps to `422 thumb_unavailable`; the negative result is memo-cached 10 min.
7. `dims.go` fills `pages.width/height` for free during thumbnail generation, and a low-priority background pass uses `image.DecodeConfig` with an aborting reader (must **not** read the whole entry — assert bytes read < 64 KiB per page for JPEG). `books.dims_state` transitions `none → partial → done`.
8. `usage.go`: 60 s-cached walk reporting per-kind files/bytes; purge by `thumbs|pdf|wazero|all` returns `{deleted_files, freed_bytes}`. Deleting the whole cache dir mid-run costs latency only (test does exactly that).
9. Benchmark `BenchmarkThumbnail` present; regression >20 % fails.

#### WP-08 — Scanner

| | |
|---|---|
| **Owns** | `internal/scanner/*` |
| **Depends on** | WP-01, WP-02, WP-03, WP-04 |
| **FRs** | FR-IDX-001, -003, -004, -006, -010; FR-CFG-002, -005; FR-THM-003 (cover enqueue); A-3 |

**Acceptance**
1. Pipeline exactly as arch §4.1: one walker per root, `scan.workers` archive readers, **exactly one writer goroutine** owning the write connection, results channel bounded at 512, `context.Context` honoured and `POST /api/scan/cancel` commits what it has.
2. `classify.go` + `collect.go` reproduce **every row of prd §2.2** and every real shape from data-survey. Table test in `t.TempDir()` covering: N zips; N subdirs of images; images directly inside; single zip; single pdf; **N zips + 1 cover image → the image is a cover, not a book** (D-5); N zips + 5 loose images → a `(loose pages)` book; two-level nesting to `max_depth`; a directory holding both `01권/` and `01권.zip` (both become books, D-6); a `.txt`-only directory → `status='empty'` (D-7).
3. Exclusions (FR-IDX-006) applied to the **decoded** name: directory entries, `__MACOSX/`, `._*`, dot-files, `.DS_Store`/`Thumbs.db`/`desktop.ini`, 0-byte, non-image extensions, `exclude_globs`. **`include_globs` (A-3)** filters root direct children before classification; empty means "everything".
4. Incremental (FR-IDX-003): archive/PDF books skip on `(size, mtime)` equality without reading the central directory; directory books skip on an unchanged FNV-1a-64 fingerprint over natural-sorted `(name,size,mtime,isDir)` of direct children; `full:true` never skips. Test: touch one file ⇒ exactly one book re-indexed.
5. Error isolation (FR-IDX-010): every failure in arch §4.11's table maps to the stated `books.status` + a `scan_log` warn row, and **the scan completes**. A panic inside a per-book unit is recovered and converted to `status='error'`.
6. Generation sweep: rows for the scanned root with `scan_gen < current` are deleted in one transaction; **an unreachable or disabled root is never swept** and sets `roots.last_scan_error` instead.
7. Cover ladder of arch §4.10 (named cover file → the single loose-image candidate → page 1 of the first `ok` book → NULL). Covers are enqueued to `coverQ` as each series completes so they appear during the scan.
8. Status snapshot updated ≤ every 200 ms via `atomic.Pointer`, with the phases and fields of arch §7.10.
9. `check-readonly` passes for `internal/scanner`.
10. **Ruling E-14 — `seriesStatus` is the three-row fold of arch §3.5**, including the row that is not yet implemented: a series with **≥ 1 book and none of them `ok`** is `error`, *even when every one of those books is specifically `empty`*. `empty` means "no books at all" and nothing else. Book statuses and the scan-error count are unaffected — an `empty` book is still not a scan failure. See §0.3 "E-14 follow-up work"; it names the three `internal/scanner` files and is **blocking** for the E2E acceptance run.

#### WP-09 — Frontend: Home / Library

| | |
|---|---|
| **Owns** | `web/src/features/library/*` |
| **Depends on** | WP-05, WP-06 |
| **FRs** | FR-LIB-001, -002, -003, -004, -005, -007, -008, -010; FR-IDX-004 (progress display); NFR-PRF-003 |

**Acceptance**
1. Grid and list reproduce ui-spec §4.5 pixel contracts (column template `32px minmax(0,1fr) 66px 64px 78px 100px 148px`; card cover `aspect-ratio:2/3` with the four absolute layers). Verified against `library-grid-1440.png` / `library-list-1440.png`.
2. **List is co-equal with grid** (design.md principle 1) — same data, same virtualisation, same sort affordances. View mode is sticky via `settings.library_view`.
3. Virtualised with `@tanstack/react-virtual` in **both** modes; the sticky section header sits outside the scroller. 1 000-series first paint < 1.5 s measured by a Playwright performance assertion (NFR-PRF-003).
4. Fallback cover (FR-LIB-008) is **always rendered beneath** the image so a cover load never shifts layout; `202` from `/cover` shows the fallback and retries.
5. 이어보기 row hides entirely when `/api/continue` returns `items: []`, and during the skeleton state.
6. Sidebar scopes drive `progress=` (A-4) and `root=` params; the section header shows the scope name and result count.
7. Sort header behaviour: first click on 시리즈명 = asc, on 권/용량/수정일 = desc, clicking the active column flips; the active cell renders `--ink` plus `↑`/`↓`.
8. Onboarding (no roots) renders per ui-spec §4.6 with the **C-5 wording** (`설정 파일 위치 보기`, path shown, no add button).
9. Skeleton has zero CLS: a Playwright layout-shift assertion of `< 0.01` across the load.

#### WP-10 — Frontend: Series detail + overlays

| | |
|---|---|
| **Owns** | `web/src/features/series/*` `web/src/features/overlays/*` |
| **Depends on** | WP-05, WP-06 |
| **FRs** | FR-LIB-006, -009, -011; FR-THM-008; FR-VWR-012 (manual mark); FR-IDX-004 (scan log); NFR-CMP-003 (theme switch) |

**Acceptance**
1. Series detail matches ui-spec §5.1–§5.4: 176×264 hero, path as the **only** filesystem path in the product, four stats (권/용량/형식/진행률), action row, volume grid **and** list.
2. Broken volumes render the accent-900@82 % scrim with `암호화`/`손상` badge and reason, and are **not clickable** (FR-IDX-010 surfacing).
3. Volume list/grid uses `books[]` from `GET /api/series/{sid}` in `ord` order — no client re-sorting.
4. `이 시리즈 재스캔` calls `POST /api/series/{sid}/rescan`, shows the scan indicator, and handles `409 conflict` with a non-blocking notice.
5. Command palette (FR-LIB-011): `Ctrl/Cmd+K` from anywhere including the viewer; queries the **server** (`/api/series?q=&limit=8`, 150 ms debounce, C-10); empty query shows `/api/continue`-derived recents; `↑/↓/↵/Esc`; row 0 preselected; 초성 highlighting via `lib/chosung.ts`.
6. Settings dialog implements ui-spec §8.6 **as amended by C-5**: roots are read-only with per-root `재스캔` and the config path; cache usage + `전체 삭제` (FR-THM-008); reading defaults writing `PUT /api/settings`; **a real 라이트/다크/시스템 `.seg`** (NFR-CMP-003) plus the note that the viewer stays dark; scan log from `/api/scan/log` with INFO/WARN/ERROR colouring.
7. Manual read/unread (FR-VWR-012): each volume row exposes a `읽음 표시` / `안읽음` action calling `PUT`/`DELETE /api/books/{bid}/progress`.
8. Shortcuts dialog matches ui-spec §8.5 entry-for-entry.
9. All dialogs: focus trap, `Esc` ladder (palette/settings/shortcuts before viewer), restore focus on close, `aria-modal`.

#### WP-11 — Frontend: Viewer

| | |
|---|---|
| **Owns** | `web/src/features/viewer/*` |
| **Depends on** | WP-05, WP-06 |
| **FRs** | FR-VWR-001..012; FR-SRV-007 (client cache discipline); NFR-PRF-002; AC-008 (client half) |

**Acceptance**
1. Chromeless base state on `#201e1d` with `cursor:none`; overlays fade via **opacity, never `display:none`**; auto-hide **2 200 ms** after the last wake.
2. Rendered inside `<div data-theme="dark">` so it is dark in both app themes (NFR-CMP-003).
3. Display modes `single | spread | vertical` (**C-1 wire values**) and fits `width|height|original|contain` (**C-2**), with `height` default (C-13). Stage rules per ui-spec §6.2.
4. **RTL spread**: `flex-direction: row-reverse` puts page *n* right and *n+1* left, and `←`/`→` invert. Asserted by a DOM-order test **and** an E2E screenshot compared to `viewer-mode-double-spread-1440.png`.
5. Landscape auto-split (FR-VWR-004): a page with `w > h` (from `PageInfo.w/h`, or from natural size once loaded when dims are `null`) renders single even in spread mode.
6. Prefetch (FR-VWR-006): `settings.prefetch` pages ahead (default 4) + 1 behind, via `new Image()` against the same versioned URLs so the browser cache is shared with the `<img>`.
7. Keyboard: `←/→` (direction-aware), `Space` (+1, prevented default), `T`, `F` (**real `requestFullscreen`**), `Esc`, `1/2/3`. Step is **+2 in spread**, clamped to `[1, page_count]`.
8. Touch (FR-VWR-011): left 30 % / right 30 % tap zones in reading order, centre 40 % toggles chrome, horizontal swipe turns pages (disabled in vertical).
9. Loading never blanks the stage: the previous page stays, the spinner appears only after ~240 ms.
10. Page error renders the `이미지 로드 실패` badge + entry name + cause + `다시 시도`, flush-left.
11. End of volume raises the next-volume card (light surface over a 92 % scrim) with `다음 권 읽기` → `next_book_id`, hidden in vertical mode.
12. Thumbnail strip is **virtualised**, auto-scrolls the current thumb into view, and handles `202`/`422` per thumb without breaking the row (AC-008 with 1 071 pages).
13. Progress: `PUT /api/books/{bid}/progress` debounced 1 s and on unmount/`visibilitychange`; resume opens at `last_page`; a `stale: true` progress shows a one-line "파일이 변경되었습니다" hint.
14. Slider drag shows the 68×102 preview thumbnail at the dragged page; commit on release only.

---

### Wave 3

#### WP-12 — HTTP API (the whole of arch §7) + auth

| | |
|---|---|
| **Owns** | `internal/httpapi/*` `internal/auth/*` |
| **Depends on** | WP-01, WP-02, WP-03, WP-04, WP-07, WP-08 |
| **FRs** | every FR with an endpoint; FR-SRV-007/008; NFR-SEC-001/002/003; NFR-OPS-005 (request logging) |
| **Note** | Largest package. It may be split across **two agents on disjoint file sets** — *lane A*: `deps.go router.go middleware.go errors.go dto.go spa.go health.go roots.go settings.go cache.go scan.go authhandlers.go internal/auth/*`; *lane B*: `series.go books.go pages.go thumbs.go progress.go continueread.go golden_test.go testdata/golden/*`. Lane A defines `dto.go`; lane B consumes it. |

**Acceptance**
1. Every route, method, status code, parameter and field name of arch §7 **as amended by §0.3**, implemented on `net/http.ServeMux` with no router dependency. Wrong method ⇒ 405. Unknown `/api/*` ⇒ JSON 404 in the envelope, **never** the SPA fallback.
2. Strict JSON decoding (`DisallowUnknownFields`) on every request body ⇒ `400 bad_request`. Ids validated against `^[a-z2-7]{16}$` ⇒ `400`; well-formed-but-unknown ⇒ `404`.
3. Page hot path: `Content-Type` from a fixed extension table (never sniffed), `Content-Length`, strong ETag in the arch §5.3 forms, `Cache-Control: public, max-age=31536000, immutable` **only** when `?v=` matches `cv`, `max-age=60, must-revalidate` when absent, `409 stale_version` with `detail.cv` when stale. `304` on matching `If-None-Match`. `Accept-Ranges: bytes` and `206` **only** for stored entries and `dir` pages; deflate entries answer `200` to a `Range`.
4. **FR-SRV-008 byte-identity**: a test hashes the served body and the archive member's inflated bytes — they must be equal.
5. `202 + Retry-After: 1` for a queued cover/thumb; `422 thumb_unavailable` with `detail.reason`; `501 unsupported` for PDF under `nopdf` or `pdf.enabled:false`; `503 unavailable` when a root is unreachable.
6. Auth: `bcrypt` (cost 12) verify, HMAC-SHA256 session cookie (`HttpOnly; SameSite=Lax; Path={base}/`; `Secure` over TLS or trusted `X-Forwarded-Proto`), per-IP token bucket 5/min burst 5 then `429 + Retry-After`, failures ≥250 ms. When enabled, everything except `/api/health` and `/api/auth/*` — **including static assets** — requires the cookie.
7. Base path: the whole app mounts under `http.StripPrefix(base, mux)`; `{base}` without a trailing slash `308`s to `{base}/`; `index.html` is served with `<base href="{base}/">` injected.
8. Security headers on every response: `X-Content-Type-Options: nosniff`, `Referrer-Policy: same-origin`, the arch §8.4 CSP. Bodies capped (1 MiB JSON, 32 MiB import).
9. Path-traversal layer 4 assertion before every open; failure ⇒ `500` + an `error`-level log.
10. **Golden JSON files** under `internal/httpapi/testdata/golden/` for at least: `series_list`, `series_detail`, `book_detail`, `continue`, `settings`, `scan_status`, `scan_log`, `roots`, `cache_usage`, `error_not_found`, `error_stale_version`. WP-06's MSW fixtures are switched to these files once they land.
11. `slog` one line per request with `req_id method path status bytes dur_ms remote`; image endpoints demoted to `debug`.

---

### Wave 4

#### WP-13 — Assembly, integration tests and E2E

| | |
|---|---|
| **Owns** | `cmd/shelf/*` `internal/app/app.go` `integration/*` `scripts/*` `test/*` |
| **Depends on** | all |
| **FRs** | FR-IDX-005, FR-CFG-005, NFR-OPS-001, -003, -006; AC-001..AC-008 |

**Acceptance**
1. Startup sequence of arch §6.3 in order, including: **the library is served from the existing index before the scan starts** (NFR-OPS-006), refusing to start on an unknown/newer `schema_version`, and `SIGINT/SIGTERM` → cancel scan → `Server.Shutdown` → `wal_checkpoint(TRUNCATE)` on both DBs.
2. `--rebuild-index` deletes **only** `index.db`, `index.db-wal`, `index.db-shm` from a hard-coded allowlist (never a glob), then runs a full scan (FR-IDX-005).
3. `--init-config` writes the commented starter file and exits 0; missing config without it exits non-zero printing the path it looked for (AC-007).
4. `shelf hash-password` prints a bcrypt hash.
5. `make release` cross-compiles all seven targets of arch §11 with `CGO_ENABLED=0`, emitting a **static** default artefact and an `-avif` variant for each (14 files) plus `SHA256SUMS` and `dist/ARTIFACTS.txt` (NFR-OPS-003, ruling **E-21**). Default linux/amd64 ≤ **32 MiB = 33 554 432 bytes** (ruling **E-19**; see §7.3).
6. `make e2e` runs §6.3 end to end and exits non-zero on any failed assertion.
7. Integration suite (`-tags integration`, gated on `SHELF_TEST_ROOT`) implements every test in §6.2.
8. **Ruling E-14** — `integration/scan_test.go` and `scripts/e2e-assert.py` expect `[만화] 엔젤하트 전32권 완결.zip` to be a **series** with `status:"error"` and a non-empty `error`, over a single **book** with `status:"empty"`. See §0.3 "E-14 follow-up work". This assertion cannot pass until WP-08 lands its half of that table, so the two move together.

---

### Wave / dependency summary

```
wave 0:  WP-00
wave 1:  WP-01  WP-02  WP-03  WP-04*  WP-05  WP-06          (*WP-04 needs WP-02's kenc/natsort;
                                                              start WP-02 first inside the wave or
                                                              stub the two function signatures)
wave 2:  WP-07(01,03,04)  WP-08(01,02,03,04)  WP-09(05,06)  WP-10(05,06)  WP-11(05,06)
wave 3:  WP-12(01,02,03,04,07,08)
wave 4:  WP-13(all)
```

> **The one intra-wave edge.** WP-04 imports `internal/kenc` and `internal/natsort` from WP-02. Both are
> pure functions with signatures frozen here:
> `kenc.DecodeEntryName(raw []byte, utf8Flag bool) (name string, enc string)` and
> `natsort.Compare(a, b string) int` / `natsort.Key(s string) []byte`.
> WP-04 may code against those signatures immediately; if WP-02 has not landed, add a temporary
> `//go:build ignore` local stub and delete it before opening the PR. Do **not** create files in
> `internal/kenc` or `internal/natsort`.

---

## 4. The frozen API contract

**`docs/arch-backend.md` §7 (7.1 through 7.13) is the normative HTTP contract**, as amended by §0.3 of this
document (**A-1 … A-14**) and the enum resolutions C-1 … C-4. Read it there; it is not duplicated here so
there can only ever be one copy.

> **A-8 landed after wave 2** (ruling E-9, 2026-07-28), and carries the follow-up work listed under the
> A-8 row in §0.3 — see also arch §7.5. Every amendment older than it is already encoded on both sides.
>
> **A-9** (ruling E-13) and **A-10** (ruling E-25, 2026-07-30) also postdate wave 2, so the sentence that
> once stood here — *"A-8 is the only amendment newer than `web/src/api/types.ts`"* — is no longer true.
> Both are encoded in `types.ts`, and `scripts/contractcheck` compares that file against the server's
> golden JSON on every `make lint`, so the two sides cannot silently disagree about either one.
>
> **A-11 … A-14 postdate all of those** (rulings E-26, E-40, E-41, E-45). **`contractcheck` covers them
> unevenly, and that is worth knowing before trusting it:** it compares golden **response** JSON against
> `types.ts`, and **deliberately does not check request bodies** (`scripts/contractcheck/main.go:48-53`).
> So A-14's `stale_seen` — a *request* field — is invisible to it. **Do not read a green `make lint` as
> "both sides agree about A-14".**
>
> **And do not read a green Go handler test that way either.** It fixes one half — *the server accepts
> `stale_seen`* — and the vitest suite fixes the other — *the client sends `stale_seen`*. Neither compares
> the two spellings, so renaming the Go struct tag to `staleSeen` and updating `api_test.go` with it leaves
> **all five gates green** while every real acknowledgement PUT is rejected as `400 bad_request` by
> `DisallowUnknownFields` (`params.go:108`) and the warning never clears. The reverse — TypeScript silently
> dropping the field — returns 200 and is quieter still. **The seam is guarded by the e2e, and by nothing
> else** (ruling E-45 §4-3, which says so normatively). **That e2e does not exist yet**: as of this round
> `web/e2e/` asserts nothing about this hint or this field, so A-14's seam is currently **unguarded** and
> the round is not finished until it is written.

What "frozen" means operationally:

* **Backend and frontend never talk to each other during this build.** Both implement §7 + §0.3.
* The reconciliation artefact is `internal/httpapi/testdata/golden/*.json` (WP-12) diffed against
  `web/src/api/types.ts` (WP-06). WP-13 runs that diff as a gate.
* Anything ambiguous in §7 is resolved **in favour of the literal field names and status codes written
  there**. If a genuine gap is found, it is escalated to the tech lead and recorded as a new `A-` row in
  §0.3 before any code changes — not patched locally.
* The five things most often gotten wrong, restated:
  1. **All page numbers are 1-based.** There is no page 0.
  2. Every page/thumb/cover URL carries `?v={cv}`; without it the response is only cacheable for 60 s.
  3. `202` is a normal, expected response for covers and thumbs — it means "queued", retry after `Retry-After`.
  4. Books with `status != "ok"` return **200** with `pages: []` and a populated `error`, not an HTTP error.
  5. Unknown JSON body fields are rejected with `400`; unknown **query** params are ignored.

---

## 5. Shared conventions (binding)

### 5.1 Go

* **Package layout**: everything under `internal/`, one responsibility per package, no `util`/`common`.
  Package names are lower-case single words matching their directory.
* **Interfaces are declared by the consumer.** `internal/httpapi/deps.go` declares the narrow interfaces it
  needs; `internal/index`, `internal/scanner`, `internal/thumbs` return **concrete** types. This is what
  lets wave-2 packages compile without wave-3 existing.
* **Context first**: every function that does I/O takes `ctx context.Context` as its first parameter and
  honours cancellation. No `context.TODO()` in committed code.
* **Errors**: wrap with `fmt.Errorf("reading central directory: %w", err)` — lower-case, no trailing
  punctuation, no "failed to". Sentinel errors are exported vars named `ErrX` in the package that owns the
  concept (`zipidx.ErrNoEOCD`, `source.ErrUnsupported`, `thumbs.ErrUndecodable`). Compare with
  `errors.Is`/`errors.As`, never string matching.
* **Logging**: `log/slog` only, obtained from the struct it is attached to (no package-level logger, no
  `log.Printf`). Standard attribute keys: `req_id, method, path, status, bytes, dur_ms, remote,
  run_id, root, rel_path, book_id, series_id`. Level policy per arch §9. **Never log passwords, the session
  key, or absolute paths outside `roots[].path`.**
* **Concurrency**: no naked `go` statements — every goroutine is owned by a struct with a `Close()`/context
  and is waited on. Bounded channels only.
* **No global mutable state.** `buildinfo` vars set by ldflags are the sole exception.
* **Tests**: table-driven, `t.Parallel()` where safe, `t.TempDir()` for anything on disk, golden files under
  `testdata/`. `make test` runs with `-race -count=1` and must be green.
* **Formatting/lint**: `gofmt` + `go vet` + `staticcheck` clean. `make lint` also runs `check-readonly`.
* **Build**: `CGO_ENABLED=0` always. Build tags: `nopdf`, `noavif`, `integration`.

### 5.2 TypeScript / React

* `strict: true`, `noUncheckedIndexedAccess: true`, `exactOptionalPropertyTypes: true`,
  `verbatimModuleSyntax: true`, `noUnusedLocals/Parameters: true`. **`any` is banned** (ESLint error);
  `unknown` + a narrowing guard instead.
* **Named exports only** (no `export default`), one component per file, file name = component name.
* Components are function components with an explicit `Props` interface. No `React.FC`.
* **No inline hex colours, no `rounded-*` utilities, no arbitrary Tailwind values for colour or radius.**
  Everything comes from the tokens (ui-spec §1.2/§1.4). Enforced by ESLint + a build grep.
* **Every string shown to a user comes from the ko catalogue in ui-spec §9.** Keep the Korean copy verbatim.
  A single `src/lib/strings.ts`-style catalogue is *not* required (no i18n in scope) but literals must match
  the catalogue exactly.
* Numeric cells carry `font-variant-numeric: tabular-nums`.
* **Data fetching**: TanStack Query v5 only, through the hooks in `src/api/queries.ts`. No `useEffect` +
  `fetch`. Query keys are arrays defined in `queries.ts` and exported.
* **Client state**: **Zustand** (`src/store/ui.ts`, `src/store/viewer.ts`). Server data never lives in
  Zustand; UI state never lives in Query.
* **Routing**: **React Router v7** `createBrowserRouter`, `basename` from `<base href>`.
  `/`, `/series/:sid`, `/series/:sid/books/:bid` (`?page=n`). Overlays are state, not routes.
* **Virtualisation**: `@tanstack/react-virtual`.
* **Icons**: `lucide-react`, per-icon imports only.
* Accessibility: every interactive element is a real `button`/`a`/`input`; `:focus-visible` ring per §2.2;
  dialogs trap focus and restore it.

### 5.3 Naming

| Thing | Convention | Example |
|---|---|---|
| Go package | lower, single word | `zipidx`, `userdata` |
| Go exported type | noun | `BookSource`, `ScanStatus` |
| JSON field | `snake_case` | `page_count`, `last_scan_end` |
| TS type | `PascalCase`, mirrors the Go DTO name | `BookSummary` |
| TS variable/field | `camelCase` **except** fields typed straight off the wire, which keep `snake_case` | `book.page_count` |
| React component file | `PascalCase.tsx` | `SeriesCard.tsx` |
| CSS custom property | `--kebab-case` | `--fill-track` |
| Query key | `['series', {params}]` | — |
| Test name | `TestThing_condition_expectation` | `TestDecodeEntryName_noFlagValidUTF8_returnsUTF8` |

### 5.4 How the frontend calls the API

**One module: `web/src/api/client.ts`.** It is the only place `fetch` appears. Everything else uses the
typed hooks in `web/src/api/queries.ts`, which are built on it. Types live in `web/src/api/types.ts` and
mirror the frozen contract. URL construction (base path, `?v=`, `?w=`) lives in `web/src/api/urls.ts`.
An ESLint `no-restricted-globals` rule fails the build if any file outside `src/api/` calls `fetch`.

### 5.5 Build & run commands

```bash
# ---- every go command in this project carries this prefix ----
export GOPROXY=https://proxy.golang.org,direct
export GOSUMDB=sum.golang.org
export GOTOOLCHAIN=auto
export CGO_ENABLED=0

# backend
make build          # pnpm build, then the static binary into dist/shelf
make dev            # go run ./cmd/shelf --config ./shelf.yaml --log-level debug   (port 8790)
make test           # go test ./... -race -count=1
make lint           # vet + staticcheck + check-readonly
make test-int       # SHELF_TEST_ROOT=... go test -tags integration ./... -timeout 30m
make bench          # -bench . -benchmem
make release        # 7 targets × 2 variants (static default, -avif) + SHA256SUMS
make e2e            # scripts/e2e.sh — full curated-subset run, see §6.3

# frontend (from web/)
pnpm install
pnpm dev            # vite on :5173, proxying /api -> http://localhost:8790
pnpm build          # -> web/dist, which go:embed swallows
pnpm typecheck      # tsc --noEmit
pnpm lint
pnpm test           # vitest
pnpm e2e            # playwright, needs a running server (scripts/e2e.sh does this for you)
```

Ports: **8790** backend dev · **5173** Vite dev · **8791** E2E server (verified free).

---

## 6. Test plan

### 6.1 Unit tests (hermetic, on every commit, `make test`)

| WP | Package | Must assert |
|---|---|---|
| 00 | `testutil` | every generated fixture opens in `archive/zip` with the expected entry set (except the deliberately broken ones) |
| 01 | `config` | lookup order · every default · every validation rejection · per-OS dirs · base_path normalisation · `shelf.example.yaml` round-trip |
| 02 | `ids` | the two golden ids verbatim (`ruzwlotzngls2ua5` / `yvtfrny77ehkt2we`) · the hash input rebuilt from literals · `IDVersion == "shelf-id/1"` · determinism · domain separation on the same rel path · backslash≡slash · `Valid` accepts exactly `[a-z2-7]{16}` |
| 02 | `natsort` | arch §4.7 table verbatim (incl. the Korean rows) · 100k-case `Compare`↔`Key` property · total-order laws · 22-digit no-overflow |
| 02 | `kenc` | the 6-branch decision table · CP949 golden vectors · **flagless-but-UTF-8 returns UTF-8** · decoder-returns-nil regression guard |
| 02 | `hangul` | the four verified vectors · jamo passthrough |
| 03 | `index`,`userdata` | schema + WAL · ATTACH under a hammered pool · list filters/sorts incl. A-4 · single-writer API · **progress survives index deletion** · export/import merge |
| 04 | `zipidx` | fixtures (stored/deflate, CP949 + UTF-8 names, nested dirs, `__MACOSX`, `Thumbs.db`, 0-byte, encrypted, 40 KiB comment, truncated, bogus EOCD, **ZIP64**) · **differential vs `archive/zip` incl. error verdict** · `comp_size+30` byte accounting · CRC match |
| 04 | `openpool` | LRU order · no fd closed under a live reader · invalidate · 8×300 concurrent reads |
| 04 | `source` | byte identity per kind · `os.Root` refuses the four escape shapes · `nopdf` returns `ErrUnsupported` |
| 07 | `thumbs` | exact cache path · cv changes the path · atomic publish under readers · single-flight coalescing · animated-WebP → `ErrUndecodable` · dims pass reads <64 KiB/page |
| 08 | `scanner` | every prd §2.2 row + every data-survey shape · exclusions · incremental skip/rescan · error isolation incl. recovered panic · gen sweep never touching an unreachable root · cover ladder |
| 12 | `httpapi`,`auth` | every endpoint via `httptest`: status codes, envelope, strict decoding, id validation, base_path mounting, 405, auth gating, ETag/304/206, `?v=` matrix, 202/422/409/501 · golden JSON files |
| 05 | frontend DS | token resolution in both themes · viewer wrapper stays dark in light theme · no `rounded-*` · no hex in components · flush-left block buttons |
| 06 | `api` | envelope→`ApiError` · 202 retry · 409 invalidate-and-retry · 401 → login · URL builders per §0.4 |
| 09/10/11 | features | Vitest + Testing Library: RTL spread DOM order · spread step=+2 · direction-aware arrows · palette keyboard · dialog focus trap · progress debounce |

### 6.2 Integration tests (`-tags integration`, gated on `SHELF_TEST_ROOT`, `make test-int`)

Run against the curated root of §6.3, **not** the whole 414 GB tree.

| # | Test | Assertion |
|---|---|---|
| I-1 | Full scan | completes without panic; series count == 10; `books.status='error'` == exactly the known-broken set; a `scan_log` warn row exists per error |
| I-2 | **AC-002** | sample every indexed page name in the curated set: **zero** contain U+FFFD; names from `Clover 클로버 1.zip` match golden strings |
| I-3 | **AC-003** | `SeriesDetail → BookDetail → pages/1` is shape-identical for `[만화] Clover 클로버 (총4권)` (folder-of-zips) and `[만화] 바퀴.zip` (single zip) |
| I-4 | **AC-004** | the same flow for `[만화] 미생 1~9 (완결 pdf)` returns `image/jpeg` and a plausible page count |
| I-5 | **AC-001 / NFR-PRF-006** | read all 1 540 pages of `[만화] 배틀로얄 1~15 [완결].zip`: peak RSS growth < 64 MiB, and `find $TMPDIR $PWD -newer <marker>` outside `cache_dir` is empty |
| I-6 | **AC-005 / AC-006** | write progress → delete `index.db*` + the whole cache → restart → rescan → progress, prefs and settings intact; covers regenerate |
| I-7 | **NFR-PRF-004** | a second scan immediately after the first finishes in < 30 s |
| I-8 | **AC-008** | 50 random page jumps in the 1 540-page book: p95 TTFB < 100 ms warm |
| I-9 | **FR-CFG-005** | `find "$ROOT" -newermt "$START"` is empty after a full scan **and** a full read |
| I-10 | Classification | `[만화] 군계 1~25` yields books for both `01권/` and `01권.zip` (D-6) and uses `[cover].jpg` as the cover; `[만화] 강철의 연금술사 …` uses `강철의 연금술사 00 Cover.jpg` and produces **no** one-page book (D-5); `[만화] 엔젤하트 전32권 완결.zip` yields one **book** with `status='empty'` and a **series** with `status='error'` carrying that book's reason (D-10 as narrowed by ruling **E-14**) |

### 6.3 End-to-end: the curated real-collection subset

#### Why not symlinks — decided

A directory of symlinks pointing into the real collection **will not work**, and this is not a judgement
call: `os.Root` (path-traversal layer 3, arch §8.1) refuses to open any symlink that escapes its root —
verified — and `scan.follow_symlinks` defaults to `false`. A symlink farm would index as an empty library.
Copying is also rejected: 5 GB of duplication, and it destroys the 2012–2018 mtimes that
`content_version`, incremental scan and FR-THM-006 all key off. Hard links cannot cross the
`/mnt/big-data` → `/mnt/data` filesystem boundary.

**Decision: point a root directly at the real collection and constrain it with `scan.include_globs`
(amendment A-3).** Nothing is copied, the media volume is read-only in practice (I-9 proves it), and the
test exercises genuine CP949 bytes and genuine truncated archives.

#### The curated set — 10 series, ~5.1 GB, zero bytes copied

Root: `/mnt/big-data/pds/taison-data/02. books/01. mangga`

| # | `include_globs` entry (exact) | Shape | Covers |
|---|---|---|---|
| 1 | `[만화] Clover 클로버 (총4권)` | folder + 4 ZIPs, 31 MB | prd §2.2 row 1; CP949 entry names; 4-book series |
| 2 | `[만화] 상처를 쫓는자 1-11 (완) 이케가미 료이치` | folder + 11 image sub-folders, 183 MB | prd §2.2 row 2; `kind:"dir"` books; dir fingerprint incremental path |
| 3 | `[만화] 자살도114-122` | folder with 181 loose images, 48 MB | prd §2.2 row 3 (the **only** instance in the collection); natural-sort stress (`1.jpg`,`10.jpg`,`100.jpg`) |
| 4 | `[만화] 바퀴.zip` | single top-level ZIP, 4.5 MB | prd §2.2 row 4; series == its own book |
| 5 | `[만화] 강철의 연금술사 1~27권 완결` | 27 ZIPs + `강철의 연금술사 00 Cover.jpg`, 876 MB | prd §2.2 row 6 "mixed" as it actually occurs (D-5); cover-file ladder step 1 |
| 6 | `[만화] 군계 1~25` | `[cover].jpg` + `01권/` **and** `01권.zip` + `07권.zip`/`07권.repair.zip`/`07권 (2).repair.zip` + **2 truncated archives**, 622 MB | duplicate books (D-6); FR-IDX-010 truncated-CD isolation; cover-file rule |
| 7 | `[만화] 디엔엔젤 1-13권 연재중` | 13 ZIPs, one of them **0 bytes**, 250 MB | FR-IDX-010 unopenable container → `status:"error"`, scan continues |
| 8 | `[만화] 미생 1~9 (완결 pdf)` | 9 PDFs, 509 MB | prd §2.2 row 5; **AC-004**; FR-SRV-006; pdfium lazy init + render cache |
| 9 | `[만화] 배틀로얄 1~15 [완결].zip` | one 1.34 GB ZIP, **1 540 pages** | **AC-008**; NFR-PRF-006; deflate streaming at scale |
| 10 | `[만화] 엔젤하트 전32권 완결.zip` | 1.44 GB ZIP containing 33 sub-ZIPs, 0 images | **series `status:"ok"` with one `kind:"nestedzip"` book per inner archive, each with pages (D-70, superseding D-10's first clause)**; proves a container of volumes is read without extracting anything |
| 10b | `비둘기.zip` | opens cleanly, one directory entry, 128 bytes | book `status:"empty"`, series **`status:"error"`** with that reason (**E-14**); the shape row 10 used to demonstrate, now that row 10 is readable — a series with nothing readable in it is visibly broken rather than greyed out |

#### The config (`scripts/e2e-config.sh` emits this)

```yaml
server: { listen: "127.0.0.1", port: 8791 }
roots:
  - name: "mangga"
    label: "만화 (E2E subset)"
    path: "/mnt/big-data/pds/taison-data/02. books/01. mangga"
storage:
  data_dir:  "<repo>/.e2e/data"
  cache_dir: "<repo>/.e2e/cache"
scan:
  on_start: false          # the script triggers the scan explicitly so it can time it
  workers: 8
  include_globs:
    - "[만화] Clover 클로버 (총4권)"
    - "[만화] 상처를 쫓는자 1-11 (완) 이케가미 료이치"
    - "[만화] 자살도114-122"
    - "[만화] 바퀴.zip"
    - "[만화] 강철의 연금술사 1~27권 완결"
    - "[만화] 군계 1~25"
    - "[만화] 디엔엔젤 1-13권 연재중"
    - "[만화] 미생 1~9 (완결 pdf)"
    - "[만화] 배틀로얄 1~15 [완결].zip"
    - "[만화] 엔젤하트 전32권 완결.zip"
thumbnails: { widths: [120, 240, 400, 640], workers: 4 }
pdf: { enabled: true, workers: 1 }
log: { level: "debug", format: "text" }
```

`[` and `]` are `path.Match` character-class metacharacters — the config loader must therefore accept
these patterns literally when they contain no unmatched class, and the E2E script escapes them as
`[[]만화]` if `path.Match` rejects the raw form. **WP-01 must add a test for a glob containing `[만화]`.**

#### The script (`scripts/e2e.sh`, run by `make e2e`)

1. `make build`.
2. `mkdir -p .e2e/{data,cache}`; emit `test/shelf.e2e.yaml`; record `START=$(date)` and a marker file in `$TMPDIR`.
3. Start `dist/shelf --config test/shelf.e2e.yaml`; wait for `GET /api/health` `{ok:true}`.
4. `POST /api/scan {"full":true}`; poll `/api/scan/status` at 1 s until `state=="idle"`; **fail if it exceeds 180 s**.
5. **curl assertions** (no browser yet): `GET /api/series` returns `total == 10`; kinds are `folder×6, zip×3, pdf×1`; `[만화] 디엔엔젤…` has exactly one book with `status:"error"`; `[만화] 엔젤하트…` is series `status:"error"` with a non-empty `error` (its single book is `status:"empty"` — D-10 as narrowed by **E-14**); `[만화] 군계 1~25` has ≥ 27 books including two with `status:"error"` and both `01권` variants; **no page name in a 500-name sample contains `�`**; `GET /api/books/{battle_royale}/pages/900` returns `200 image/jpeg` in < 200 ms.
6. **Playwright** (`channel: 'chrome'` → `/usr/bin/google-chrome`, `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1`), four viewport projects (1440, 1024, 768, 400):
   1. Library grid renders 10 cards, covers appear (retrying `202`), no layout shift > 0.01.
   2. Toggle to list; sort by 용량 desc; the 1.44 GB series is first.
   3. Type `ㄱㄱ` in the search box → `[만화] 군계 1~25` appears (초성, FR-LIB-006).
   4. `Ctrl+K` → palette → type `ㅁㅅ` → `↵` opens the 미생 series (FR-LIB-011).
   5. Series detail for 군계: path shown, broken volumes carry the 손상 badge and are not clickable.
   6. Open volume 1 → viewer: `→` five times, `T` opens the strip, `2` switches to 양면, `R→L` puts page *n* on the right, `1` back to 단면, `Esc` exits.
   7. Reopen the same volume → it resumes at the saved page (FR-VWR-009).
   8. Open the 미생 PDF series → a page renders as `image/jpeg` in the same viewer (AC-004).
   9. Open 배틀로얄 → drag the slider to page 1 400 → the page loads (AC-008); the strip is virtualised (DOM node count < 120).
   10. Settings dialog: cache usage non-zero; theme `.seg` flips `<html data-theme>`; the viewer stays dark; roots are read-only with no add/remove buttons (C-5); scan log shows the WARN rows.
   11. At 400 px: sidebar is an off-canvas drawer, `document.body.scrollWidth <= clientWidth`, tap zones turn pages.
   12. Screenshots of every step into `docs/e2e-shots/` for visual diff against `docs/ui-shots/`.
7. **AC-001 / FR-CFG-005**: `find "$ROOT" -newermt "$START"` is empty; `find "$TMPDIR" -newer marker` contains nothing belonging to shelf.
8. **AC-005 / AC-006**: stop the server; `rm -rf .e2e/data/index.db* .e2e/cache`; restart; rescan; assert the reading progress recorded in step 6.7 is still there and covers regenerate.
9. Stop the server, print a pass/fail summary, exit non-zero on any failure.

#### Hermetic fallback

`scripts/e2e.sh --synthetic` builds a ~12 MB tree with `testutil.BuildTree` covering the same ten shapes
(including a synthetic **encrypted** ZIP and a synthetic **ZIP64** archive, which the real collection does
not contain) and runs the identical assertion set. This is the version that can run without the media
volume mounted.

### 6.4 Benchmarks

`BenchmarkCentralDir`, `BenchmarkOpenEntry`, `BenchmarkThumbnail`, `BenchmarkNatsortKey`,
`BenchmarkSeriesList`. Run with `-benchmem`; a >20 % regression against the checked-in baseline fails.

---

## 7. Definition of done

The build is done when **every** box below is ticked. Each is verifiable by a command, not by opinion.

### 7.1 Acceptance criteria (prd §8)

- [ ] **AC-001** — During a full read of a 1 540-page volume, no file is created outside `cache_dir` and the media volume is untouched. *Verified by:* I-5, I-9, `scripts/e2e.sh` step 7.
- [ ] **AC-002** — Korean filenames inside 2014–2018 ZIPs display correctly. *Verified by:* WP-02 golden vectors, I-2, E2E step 5 (zero U+FFFD in 500 names).
- [ ] **AC-003** — Folder-type and ZIP-type series are read through an identical UI flow. *Verified by:* I-3, E2E steps 6.1–6.6 run against both `[만화] Clover…` and `[만화] 바퀴.zip`.
- [ ] **AC-004** — A PDF series opens in the same viewer as a ZIP series. *Verified by:* I-4, E2E step 6.8.
- [ ] **AC-005** — Deleting the cache and `index.db` and restarting recovers with no data loss. *Verified by:* I-6, E2E step 8.
- [ ] **AC-006** — Reading progress survives an index rebuild. *Verified by:* WP-03 unit test, I-6, E2E step 8.
- [ ] **AC-007** — The server starts from a single binary plus a config file. *Verified by:* `dist/shelf --config test/shelf.e2e.yaml` in E2E step 3; `--init-config` in WP-13 acceptance 3.
- [ ] **AC-008** — Arbitrary page jumps in a 500+-page volume are not slow. *Verified by:* I-8 (p95 TTFB < 100 ms over 50 random jumps in a 1 540-page book), E2E step 6.9.

### 7.2 Requirement coverage

- [ ] Every FR in §1.1 has at least one passing test naming its id in the test name or a comment.
- [ ] `docs/impl-plan.md` §1.1 and `arch-backend.md` Appendix A agree — no FR is claimed by zero packages.

### 7.3 Non-functional gates

- [ ] `CGO_ENABLED=0 make release` produces seven **statically linked** default binaries plus seven `-avif` variants and `SHA256SUMS`; the default linux/amd64 build is ≤ **32 MiB = 33 554 432 bytes** (NFR-OPS-001, -003, CON-001). *Amended by ruling **E-19** and confirmed by **E-21** §4: the original 20 MiB figure was this section rounding up arch §1.2's "18 MB is fine for a NAS", an estimate whose non-pdfium/non-AVIF base term was ~7 MB low; prd NFR-OPS-001 itself states no size. What occupies the space: pdfium WASM ≈8.34 MB, AVIF WASM ≈1.58 MB, pure-Go `modernc.org/sqlite` (CON-001 forces it), and the embedded SPA. The only lever left below the default is `-tags nopdf` (FR-SRV-006/AC-004), which is 필수. The number lives in three files — `Makefile`'s `SIZE_BUDGET`, this plan (here and in WP-13 acceptance 5) and `README.md` — and `internal/buildinfo/release_budget_test.go` reads all three and fails if any of them drift apart, if a line states the same budget twice in two different figures, or if the gate stops being fatal.*
- [ ] The default artefact is **statically linked** on every linux target: no `PT_INTERP`, no `DT_NEEDED` (CON-001's stated purpose, NFR-OPS-003's NAS hosts). Asserted on the produced binary's ELF headers by `internal/buildinfo/staticlink_test.go`, not on the build flags — ruling **E-21**. The default carries `-tags noavif`; the dynamic AVIF build ships as the documented `-avif` variant, and `avif_enabled` on `/api/health` and `/api/settings` reports the build's capability, not the config key.
- [ ] Idle RSS after a scan of the curated set, with no PDF or AVIF touched, is ≤ 200 MB (NFR-PRF-005).
- [ ] A no-change rescan of the curated set finishes in < 30 s (NFR-PRF-004).
- [ ] Library first paint with 1 000 synthetic series < 1.5 s (NFR-PRF-003).
- [ ] Cached page TTFB < 100 ms on localhost (NFR-PRF-001).
- [ ] Restart after `SIGKILL` serves the library from the existing index before any scan runs (NFR-OPS-006).
- [ ] Structured logs at every level with the standard attribute set; `--log-level` works (NFR-OPS-005).
- [ ] Both DBs run in WAL; `user.db` is a physically separate file that `--rebuild-index` never touches (NFR-DAT-002/003/004).
- [ ] Chrome, Edge, Safari and Firefox load the SPA without console errors (NFR-CMP-001; Safari/Firefox may be verified manually).
- [ ] No horizontal page scroll and no clipped control from 320 px to 1920 px (NFR-CMP-002).
- [ ] Light/dark/system theme switch works; the viewer is dark in both themes (NFR-CMP-003).
- [ ] `NFR-SEC-001`: no user-supplied path reaches the filesystem; the four traversal layers are all present and tested.
- [ ] `NFR-SEC-002`: with `auth.password_hash` set, every route except `/api/health` and `/api/auth/*` returns 401 without the cookie, **including static assets**; the rate limiter trips at 6 attempts/min.
- [ ] `NFR-SEC-003`: the whole app works mounted under `base_path: "/reader"`, including the SPA fallback and every image URL.

### 7.4 Hygiene

- [ ] `make lint` clean (vet + staticcheck + `check-readonly`).
- [ ] `make test` green with `-race`.
- [ ] `pnpm typecheck && pnpm lint && pnpm test` green.
- [ ] `make test-int` green with `SHELF_TEST_ROOT` set to the curated root.
- [ ] `make e2e` green; screenshots in `docs/e2e-shots/` reviewed against `docs/ui-shots/`.
- [ ] `dist/` contains no `https://fonts.g` string and no `rounded-lg` class (C-14, ui-spec §0.1).
- [ ] `docs/decisions.md` has no unresolved escalation older than this build.
