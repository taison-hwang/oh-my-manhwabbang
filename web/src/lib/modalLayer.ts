import {
  useLayoutEffect,
  useRef,
  type KeyboardEvent as ReactKeyboardEvent,
  type RefObject,
} from 'react'

/**
 * The modal layer stack: what makes a dialog (or the drawer) *modal* to
 * assistive tech, not only to the eye (10-23 accessibility audit #8).
 *
 * `aria-modal="true"` alone does not take the page behind a dialog out of
 * Chrome's accessibility tree — measured — so the top layer marks everything
 * outside itself `inert`: for every node on the path from the layer up to
 * `<body>`, each sibling that is not already inert. In practice that is every
 * other child of `<body>` (the app root, other portals), plus the siblings along
 * the way when the layer lives inside the tree rather than in a portal (the
 * drawer).
 *
 * Rules the stack keeps:
 *
 *  - **Only the top layer is live.** A dialog opened over the drawer makes the
 *    drawer inert; closing the dialog hands the drawer back and leaves the rest
 *    of the page inert, because the drawer is still open.
 *  - **It never removes an `inert` it did not set.** Every node it marks goes
 *    into `owned`; a node that was inert already is left to whoever made it so.
 *  - **Layers are tokens, not positions,** so a close that is not last-in
 *    first-out, and StrictMode's mount → unmount → mount, leave exactly the
 *    layers that are open.
 *  - **Focus never goes into an inert subtree.** A layer takes focus only once
 *    it is on top; on close focus returns to the opener or, when the opener has
 *    gone or is still behind the top layer, into the top layer that remains.
 *  - **A late portal is covered.** While any layer is open, a MutationObserver
 *    watches the child lists along the top layer's path (`<body>` included) and
 *    recomputes, so a toast or a second portal mounted afterwards is inert too.
 *
 * The attribute is set through the DOM, not as a prop: React 18.3 drops
 * `inert={true}` with a warning and `@types/react` 18 has no such prop.
 */

/** What can take focus inside a layer — the trap and the initial focus share it. */
export const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

export interface ModalLayerOptions {
  /**
   * `'first'` (default) focuses the first focusable inside, else the container.
   * `'container'` focuses the container itself — the drawer's choice, so that a
   * screen reader starts at the dialog rather than at its first link.
   */
  initialFocus?: 'first' | 'container'
}

interface Layer {
  readonly token: symbol
  readonly el: HTMLElement
  readonly initialFocus: NonNullable<ModalLayerOptions['initialFocus']>
}

const stack: Layer[] = []
const owned = new Set<Element>()
let observer: MutationObserver | null = null

/** Every sibling along the path from `el` up to `<body>`. */
function outside(el: HTMLElement): Set<Element> {
  const out = new Set<Element>()
  let node: Element = el
  while (node !== document.body) {
    const parent = node.parentElement
    if (parent === null) break
    for (const sibling of parent.children) {
      if (sibling !== node) out.add(sibling)
    }
    node = parent
  }
  return out
}

/** The parents whose child lists decide what `outside()` returns. */
function pathParents(el: HTMLElement): Element[] {
  const parents: Element[] = []
  let node: Element = el
  while (node !== document.body) {
    const parent = node.parentElement
    if (parent === null) break
    parents.push(parent)
    node = parent
  }
  return parents
}

/** Brings the inert set into line with the current top of the stack. */
function recompute(): void {
  const top = stack.at(-1)
  const want = top === undefined ? new Set<Element>() : outside(top.el)

  for (const node of [...owned]) {
    if (!want.has(node) || !node.isConnected) {
      node.removeAttribute('inert')
      owned.delete(node)
    }
  }
  for (const node of want) {
    if (owned.has(node) || node.hasAttribute('inert')) continue
    node.setAttribute('inert', '')
    owned.add(node)
  }

  observer?.disconnect()
  if (top === undefined) {
    observer = null
    return
  }
  observer ??= new MutationObserver(recompute)
  for (const parent of pathParents(top.el)) {
    observer.observe(parent, { childList: true })
  }
}

/** Moves focus into a layer: its first focusable, else the container itself. */
function focusInto(layer: Layer): void {
  const first =
    layer.initialFocus === 'first' ? layer.el.querySelector<HTMLElement>(FOCUSABLE) : null
  ;(first ?? layer.el).focus()
}

/**
 * Where focus goes once a layer has closed and the inert set has followed.
 *
 * The opener, if it can still take focus: connected, not `<body>` (focus that
 * had already fallen out of everything), and not inside an inert subtree — the
 * last is a layer that closed *under* the top one, whose opener is still behind
 * it — and only if it actually took the focus: an opener can be disabled while
 * covered (settings' rescan, once a scan starts). Otherwise the top layer that
 * remains, unless focus is already in it; with no layer left, focus is not this
 * stack's to move.
 */
function restoreFocus(opener: Element | null): void {
  if (
    opener instanceof HTMLElement &&
    opener !== document.body &&
    opener.isConnected &&
    opener.closest('[inert]') === null
  ) {
    opener.focus()
    if (document.activeElement === opener) return
  }
  const top = stack.at(-1)
  if (top === undefined || top.el.contains(document.activeElement)) return
  focusInto(top)
}

/** For tests: how many layers are open right now. */
export function openLayerCount(): number {
  return stack.length
}

/**
 * Makes `ref`'s element the top modal layer while `open`.
 *
 * One layout effect, on purpose: on open it captures the opener, recomputes the
 * inert set with this layer on top, *then* moves focus in — a layer below can
 * have inerted this one (two opened in one commit, or StrictMode replaying this
 * one over it), and focus into an inert subtree fails in a browser. On close it
 * releases the page *first* and only then focuses the opener, or the new top
 * layer when the opener is gone (`restoreFocus`), so focus never lands in an
 * inert subtree. Two effects cannot express "capture first, focus last" in both
 * directions — React runs both setups and both cleanups in declaration order.
 *
 * The opener is captured once per opening and kept across StrictMode's replay,
 * which runs every cleanup before any setup: for two layers opened together,
 * the lower one's second setup would otherwise capture its own control.
 */
export function useModalLayer(
  ref: RefObject<HTMLElement | null>,
  open: boolean,
  opts?: ModalLayerOptions,
): void {
  const initialFocus = opts?.initialFocus ?? 'first'
  /** `undefined` while closed; the opener, once captured, until it closes. */
  const openerRef = useRef<Element | null | undefined>(undefined)

  useLayoutEffect(() => {
    if (!open) {
      openerRef.current = undefined
      return undefined
    }
    const el = ref.current
    if (el === null) return undefined

    const opener =
      openerRef.current === undefined ? document.activeElement : openerRef.current
    openerRef.current = opener
    const layer: Layer = { token: Symbol('modal-layer'), el, initialFocus }
    stack.push(layer)
    recompute()
    focusInto(layer)

    return () => {
      const index = stack.findIndex((l) => l.token === layer.token)
      if (index !== -1) stack.splice(index, 1)
      recompute()
      restoreFocus(opener)
    }
  }, [open, ref, initialFocus])
}

/** True when `b` comes after `a` in document order — a descendant counts as after. */
function isAfter(a: Node, b: Node): boolean {
  return (a.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING) !== 0
}

/** True when `a` and `b` are radios of one native group: one name, one form owner. */
function sameGroup(a: Element, b: Element): boolean {
  return (
    a instanceof HTMLInputElement &&
    b instanceof HTMLInputElement &&
    a.type === 'radio' &&
    b.type === 'radio' &&
    a.name !== '' &&
    a.name === b.name &&
    a.form === b.form
  )
}

/**
 * The Tab stops among `items`, in order. A native radio group (`Seg`, `Radio`)
 * is one stop: its checked radio, else its first going forward, its last going
 * back.
 */
function tabStops(items: readonly HTMLElement[], backward: boolean): HTMLElement[] {
  return items.filter((item) => {
    const group = items.filter((other) => other === item || sameGroup(item, other))
    const stop = group.find((r) => r instanceof HTMLInputElement && r.checked)
    return item === (stop ?? (backward ? group.at(-1) : group[0]))
  })
}

/**
 * Keeps Tab inside `container`: Tab with no stop after the focus wraps to the
 * first stop, Shift+Tab with none before it (or from the container itself)
 * wraps to the last, and a container with nothing focusable keeps focus where
 * it is. Anything else is the browser's to move.
 *
 * "Is there another stop", not "is this the first or last item": two shapes in
 * the settings dialog, both measured escaping in Chrome, are not items at
 * either end. A radio group's stop is its checked radio, not its DOM-last one
 * (the theme `Seg` set to 라이트), and Chrome makes a scroll area with nothing
 * focusable inside a stop of its own that `FOCUSABLE` does not list (the scan
 * log) — so the focused element's own group is set aside and DOM order decides.
 */
export function trapTab(e: KeyboardEvent | ReactKeyboardEvent, container: HTMLElement): void {
  if (e.key !== 'Tab') return

  const items = [...container.querySelectorAll<HTMLElement>(FOCUSABLE)]
  if (items.length === 0) {
    e.preventDefault()
    return
  }

  const active = document.activeElement
  if (active === null || !container.contains(active)) return

  const backward = e.shiftKey
  const stops = tabStops(items, backward)
  const another = stops.some(
    (stop) =>
      stop !== active &&
      !sameGroup(active, stop) &&
      (backward ? isAfter(stop, active) : isAfter(active, stop)),
  )
  if (another) return

  const target = backward ? stops.at(-1) : stops[0]
  if (target === undefined) return
  e.preventDefault()
  target.focus()
}
