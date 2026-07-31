# Oh My Manhwa-bbang

A web reader for a comic and book collection that already exists on disk as ZIP
archives, folders of images and PDFs — **read without unpacking anything**.

Point it at one or more directories. It walks them, reads only each ZIP's
central directory (never the entry payloads), and builds a catalogue of
series → volumes → pages. Opening a page seeks straight to that entry's stored
offset and streams the original bytes: no temporary directory, no extraction,
no re-encoding, and nothing is ever written to the media volume. It ships as a
single static binary with the frontend compiled in; the only things it needs at
runtime are a config file and a writable cache directory.

- **Library** — cover grid and an information-dense list view, sorting, per-root
  filters, substring and Korean 초성 search, a `Ctrl/Cmd+K` command palette, and
  a 이어보기 row for whatever you were part-way through.
- **Reader** — single page, two-page spread, and vertical webtoon scroll; left-to-right
  or right-to-left; four fit modes; prefetch; a thumbnail strip; keyboard,
  mouse and touch. Reading position is saved per volume and survives an index
  rebuild.
- **Formats** — ZIP (stored and deflate, ZIP64, CP949 filenames), folders of
  images (jpg, jpeg, png, gif, webp, bmp, avif) and PDF, rasterised server-side.

Documentation lives in [`docs/`](./docs): [`prd.md`](./docs/prd.md) is the
requirements document and wins every conflict; [`arch-backend.md`](./docs/arch-backend.md)
is the backend architecture and the frozen HTTP contract; [`ui-spec.md`](./docs/ui-spec.md)
is the interface specification; [`impl-plan.md`](./docs/impl-plan.md) is the
build plan; [`decisions.md`](./docs/decisions.md) is the decision log.

### Layout

```
cmd/shelf/          the binary: flags, exit codes, signals — and nothing else
internal/app/       the composition root: what is built, in what order, and how it shuts down
internal/…          seventeen single-responsibility packages; see docs/arch-backend.md §2
web/                simultaneously the Vite project and the Go package go:embed reads
integration/        -tags integration, gated on SHELF_TEST_ROOT (impl-plan §6.2)
scripts/            the E2E runner, the static guards, the contract gate, the fixture builder
test/               the E2E configuration template
```

---

## Build

Requires **Go 1.25+** (or any Go with `GOTOOLCHAIN=auto`, which fetches it),
**Node 20+** and **pnpm**.

```bash
make build          # pnpm build, then the static binary into dist/shelf
```

Two files can come out. `make build` produces `dist/shelf`, **25.8 MB** on
linux/amd64 (25 833 656 B) with the SPA, SQLite, pdfium and every image codec
but AVIF compiled in — and, on linux, **statically linked**: no libc, no dynamic
loader, so it starts on a musl or old-glibc NAS. That is the whole point of
`CGO_ENABLED=0` (CON-001) and it is not something the flag gives you for free:
the AVIF decoder reaches `libc.so.6` through `ebitengine/purego` whatever cgo is
set to, which is why the default build carries `-tags noavif` (ruling **E-21**).
Every sample taken of the reference collection contains zero `.avif` files — two
independent passes, 500 ZIPs and ~56k entries (`docs/data-survey.md`,
`docs/arch-backend.md` §1.1) — so the default loses nothing real; an `.avif` page
still streams its original bytes and every target browser decodes it, only the
server-side thumbnail degrades to `422 thumb_unavailable`.

`make release` also emits an `-avif` variant per platform — **27.4 MB**
(27 418 916 B), AVIF thumbnails working, **dynamically linked on linux**. Take
it only on a glibc host. Both variants are in `dist/SHA256SUMS`, and
`dist/ARTIFACTS.txt` says which is which.

Adding `nopdf` drops PDF support entirely and takes the default down to 15.3 MB
(15 286 456 B) — **~10.5 MB** below the 25.8 MB above, and the floor a
`CGO_ENABLED=0` build with pure-Go SQLite can reach. Spell it
`make build TAGS="noavif nopdf"`: tags are space-separated here and the Makefile
rejects a comma, because the `-avif` release variant is derived from `TAGS` by
splitting on whitespace. (The ~7.7 MB figure quoted elsewhere for PDF is measured
against the *AVIF-enabled* build, not against this one.)

`make release` reports every artefact's size and fails when the default
linux/amd64 build exceeds `SIZE_BUDGET`, which is ≤ **32 MiB = 33 554 432 bytes**
(ruling **E-19**). Pass `SIZE_BUDGET=0` to waive the gate for one run.

### The GOPROXY prefix — read this before running any `go` command by hand

Every `go` invocation in this project must carry:

```bash
export GOPROXY=https://proxy.golang.org,direct
export GOSUMDB=sum.golang.org
export GOTOOLCHAIN=auto
export CGO_ENABLED=0
```

Without it, a bare `go build` on the development machine fails with:

```
go: GOPROXY list is not the empty string, but contains no entries
```

because `GOPROXY` resolves to an empty string in that shell. `GOTOOLCHAIN=auto`
is what lets an installed Go 1.21 download the Go 1.25 toolchain `go.mod` asks
for. Every Makefile target already carries the prefix, so `make build`,
`make test` and friends work from a clean shell — you only need the exports when
you invoke `go` directly.

`CGO_ENABLED=0` is a hard requirement of the product (a single static binary,
easy cross-compilation): SQLite is the pure-Go `modernc.org/sqlite`, and PDF and
AVIF decoding run as WebAssembly under wazero. The **one** exception is
`make test`, which sets `CGO_ENABLED=1` because Go's race detector is written in
C and `go test -race` refuses to run without cgo. Nothing shipped is built that
way.

### Other targets

```bash
make help           # this list, from the Makefile itself
make web            # build the SPA only
make build-go       # relink the binary against whatever is already in web/dist
make dev            # run the server on :8790 with debug logging
make test           # unit suite, -race
make lint           # vet + staticcheck + the static guards + the contract gate
make check-readonly # the three static guards on their own
make contract       # the frozen-contract gate on its own
make test-int       # integration suite; needs SHELF_TEST_ROOT
make bench          # benchmarks with -benchmem
make release        # 7 targets × 2 variants (default static, -avif) + SHA256SUMS
make e2e            # end-to-end against the real collection
make e2e-synthetic  # the same assertions against a ~2 MB fixture tree
make fmt            # gofmt
make tidy           # go mod tidy, guarded — see below
make clean
```

`make tidy` is not a bare `go mod tidy`. `go.mod` requires the whole frozen
dependency set (arch §1.1) ahead of the packages that import it, so every work
package builds against one immutable module graph; a plain tidy would delete
each dependency nothing imports *yet* — seven of the nine, at the time of
writing. The target therefore snapshots `go.mod`/`go.sum`, tidies, checks all
nine pins survived at their exact versions, and rolls back and exits non-zero if
any did not. Once the importing package lands, tidy passes normally.

Frontend-only loop, from `web/`: `pnpm dev` (Vite on :5173, proxying `/api` to
:8790), `pnpm build`, `pnpm typecheck`, `pnpm lint`, `pnpm test`, `pnpm e2e`.

There are three TypeScript projects, and the split is load-bearing rather than
cosmetic. `tsconfig.json` is the app (`src/` + the root configs);
`tsconfig.node.json` gives the build configs a Node environment for editors;
`tsconfig.e2e.json` owns `playwright.config.ts` and everything under the `e2e/`
directory it declares as `testDir`. ESLint runs type-aware rules through the
TypeScript project service, so a file belonging to no project fails to *parse* —
without the third project the first Playwright spec committed would break
`pnpm lint`. `pnpm typecheck` runs the app project and the E2E project in turn.

---

## Testing — four tiers

Each tier is a `make` target, each is independently runnable, and each answers a
different question.

### 1. Unit — `make test`

Hermetic. Every fixture is generated in a `t.TempDir()`, nothing touches the
network or a media volume, and it runs with `-race -count=1` across all
packages. This is the tier that has to be green on every commit.

```bash
make test                       # ~1–2 minutes
make test RACE=                 # drop -race on a machine with no C compiler
cd web && pnpm test             # the frontend half (vitest)
```

`make test` also inspects `dist/shelf`'s ELF headers if a build is lying around,
and fails if it is dynamically linked (ruling **E-21**); `make build` and
`make release` run that same check on the artefact they just produced, where it
cannot skip.

### 2. Static guards and the frozen contract — `make lint`

```bash
make lint
```

Runs `go vet`, `staticcheck`, and then two things that are specific to this
product:

- **`make check-readonly`** — three greps, each protecting an invariant that
  six parallel work packages could otherwise erode one convenient line at a
  time. No filesystem *mutation* primitive (`os.Create`, `os.Remove`, `os.Rename`,
  `os.Chtimes`, …) may appear anywhere in `internal/{scanner,source,archive,openpool}`;
  no SQL statement may write to the ATTACHed `ud.` schema, because no
  transaction is allowed to span `index.db` and `user.db`; and nothing may
  `UPDATE` or `DELETE` from `series_seen`, which is write-once so that
  "recently added" survives a rebuild.
- **`make contract`** — the reconciliation gate. Backend and frontend were built
  in parallel from the same written contract with no contact between them, so
  the two are diffed mechanically: every response in
  `internal/httpapi/testdata/golden/*.json` is checked against
  `web/src/api/types.ts` field by field, null by null and enum member by enum
  member, plus the Go error-code constants against `ERROR_CODES`. It exits
  non-zero on any disagreement and names the exact JSON path.

### 3. Integration — `make test-int`

`-tags integration`, gated on `SHELF_TEST_ROOT`. Brings the whole product up
exactly as `cmd/shelf` does, against ten real series (~5.1 GB) selected out of
the collection with `scan.include_globs`. **Nothing is copied**: the tests read
the genuine 2012–2018 mtimes, the genuine CP949 entry names and the genuine
truncated archives.

```bash
make test-int SHELF_TEST_ROOT="/mnt/big-data/pds/taison-data/02. books/01. mangga"
```

With `SHELF_TEST_ROOT` unset every test skips, so the target is a no-op on a
machine with no media volume rather than a failure.

### 4. End to end — `make e2e`

Builds the real binary, writes a config, starts the server on :8791, scans,
runs ~50 HTTP assertions, proves the media volume was not written to, then
deletes `index.db` and the whole cache, restarts, and checks that reading
progress survived and covers regenerated.

```bash
make e2e                        # against the real collection
make e2e-synthetic              # against a ~2 MB generated fixture tree
make e2e E2E_ARGS=--keep        # leave the scratch directory behind
```

`make e2e-synthetic` needs no media volume: `scripts/mkfixture` generates a tree
carrying the same ten directory names and the same ten shapes, plus an
encrypted archive and a ZIP64 archive that the real collection has no sample
of.

> **Known gap.** The browser half of the E2E plan (Playwright, four viewports)
> has no specs: no work package owns `web/e2e/`. `scripts/e2e.sh` runs
> `pnpm e2e` when specs appear there and prints a `skip` line until then.
> Everything else in the E2E script is HTTP-level and does run.

---

## Run

```bash
./dist/shelf --init-config          # writes a commented shelf.yaml and exits
$EDITOR ~/.config/shelf/shelf.yaml  # set roots[].path
./dist/shelf --config ./shelf.yaml  # http://localhost:8080
```

[`shelf.example.yaml`](./shelf.example.yaml) documents every key with its
default. The minimum viable config is one root:

```yaml
roots:
  - name: "manga"
    path: "/mnt/media/manga"
```

Useful flags: `--port`, `--log-level debug`, `--rebuild-index` (throws the
derived index away and rescans; never touches your reading progress),
`--version`. `shelf hash-password` prints a bcrypt hash for the optional
`auth:` block.

There is **no password by default**. That matches the single-user LAN tool this
is; if the server is reachable from anywhere else, uncomment `auth:` in the
config *and* put it behind a reverse proxy that terminates TLS. The server has no
built-in TLS, by design — set `server.base_path` and let the proxy handle
certificates.

### Where things live

| | |
|---|---|
| `index.db` | Derived catalogue. Disposable — delete it and it rebuilds. |
| `user.db` | **Your data**: reading progress, per-book preferences, settings. Never touched by a rebuild. Back this up. |
| `<cache_dir>/thumbs`, `pdf`, `wazero` | Regenerated on demand. Safe to delete at any time, even while the server is running. |
| your media volume | Read-only. The server opens, stats and reads; it never creates, renames, deletes or re-timestamps anything. |

---

## Two things to know before you commit to a layout

### 1. `roots[].name` and directory names are identity

Every `series_id` and `book_id` is a hash of `(root name, path relative to that
root)`. Nothing is keyed by a database row, so rebuilding the index reproduces
byte-identical ids and your progress reattaches itself. That is the whole
mechanism behind "reading progress survives a rebuild".

The cost is that those two inputs are load-bearing:

- **Renaming a root's `name` orphans that root's reading progress.** The rows
  are still in `user.db`, but nothing points at them any more. Use `label` for
  the display name; change `name` only deliberately. Moving a root to a
  different `path` is completely safe and is exactly what the scheme is for.
- **Renaming a series or volume directory orphans that item's progress**, for
  the same reason. Reorganising your collection is a migration, not a no-op.
- **A series is exactly one direct child of a root.** A directory two levels
  deep — say `건담 시리즈/기동전사 건담/…` — is flattened into a single series
  holding all of its volumes, with the sub-path shown in each volume's name.
  If a future version promotes those sub-directories to series of their own,
  their `series_id` changes and their progress orphans. That change would be a
  deliberate migration, not a silent upgrade.

### 2. Korean text renders in a system font

Only Archivo (Latin) is bundled. Hangul falls back to whatever the viewing
machine has, in this order:

```
Pretendard → Apple SD Gothic Neo → Malgun Gothic → Noto Sans KR → system-ui
```

So the same page looks slightly different on Linux, macOS and Windows: line
lengths, weights and vertical rhythm shift with the fallback. This is a
deliberate trade — a subsetted Korean webfont costs 1.5–4 MB against an ~18 MB
binary, and NFR-OPS-001/002 forbid fetching one from a font CDN at runtime. If
you would rather have consistent Korean typography, it is a two-file change:
drop a `.woff2` into `web/src/styles/fonts/` and add one `@font-face` block to
`web/src/styles/fonts.css`.

---

## Not in scope

Editing the collection (upload, rename, delete), adding or removing roots from
the web UI, external metadata lookup, multiple user accounts, OPDS, image
upscaling, and RAR/CBR/7z or nested archives. The archive reader is an
interface, so another container format is an additive change.
