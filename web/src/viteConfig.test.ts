// @vitest-environment node
import { existsSync, mkdirSync, mkdtempSync, rmSync, statSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath } from 'node:url'

import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { keepDistPlaceholder } from '../vite.config'

/**
 * `keepDistPlaceholder` in `vite.config.ts` — here because vitest only collects
 * `src/`.
 *
 * It puts `.gitkeep` back after `emptyOutDir`, so `//go:embed all:dist` always
 * has something to match. It wrote `web/dist/.gitkeep` whatever `--outDir`
 * said, so a build into a scratch directory still rewrote a file in the
 * repository — and `make e2e` step 9, which fails on anything in the tree newer
 * than its marker, read that as the product writing outside its `data_dir`
 * (twice, 2026-10-22/23). The hooks are driven by hand with a temporary root:
 * a real default build would overwrite the shared `web/dist`.
 */

/** Calls a plugin hook the way Vite does, whichever of its two shapes it has. */
function callHook(hook: unknown, ...args: unknown[]): void {
  if (hook === undefined) return
  const fn: unknown =
    typeof hook === 'function' ? hook : (hook as { handler?: unknown } | null)?.handler
  if (typeof fn !== 'function') throw new Error('not a plugin hook')
  Reflect.apply(fn, undefined, args)
}

/** One build's worth of the plugin: the resolved config, then the bundle closing. */
function build(root: string, outDir: string): void {
  const plugin = keepDistPlaceholder()
  callHook(plugin.configResolved, { root, build: { outDir } })
  callHook(plugin.closeBundle)
}

/** This checkout's own placeholder, which a scratch build must not touch. */
const repoGitkeep = fileURLToPath(new URL('../dist/.gitkeep', import.meta.url))

function repoGitkeepMtime(): number | null {
  return existsSync(repoGitkeep) ? statSync(repoGitkeep).mtimeMs : null
}

describe('keepDistPlaceholder (vite.config.ts)', () => {
  let root: string

  beforeEach(() => {
    root = mkdtempSync(join(tmpdir(), 'shelf-vite-'))
  })

  afterEach(() => {
    rmSync(root, { recursive: true, force: true })
  })

  it('restores the placeholder in the directory the build wrote, and leaves web/dist alone', () => {
    // `vite build --outDir /some/scratch`: an absolute directory outside the root.
    const outDir = join(root, 'scratch-out')
    mkdirSync(outDir)
    const before = repoGitkeepMtime()

    build(join(root, 'web'), outDir)

    expect(existsSync(join(outDir, '.gitkeep'))).toBe(true)
    expect(existsSync(join(root, 'web', 'dist', '.gitkeep'))).toBe(false)
    expect(repoGitkeepMtime()).toBe(before)
  })

  it('keeps the default build placing it in <root>/dist', () => {
    // `outDir: 'dist'` as vite.config.ts sets it, resolved against the root.
    mkdirSync(join(root, 'dist'))

    build(root, 'dist')

    expect(existsSync(join(root, 'dist', '.gitkeep'))).toBe(true)
  })
})
