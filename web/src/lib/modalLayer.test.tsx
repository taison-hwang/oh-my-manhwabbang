import '@testing-library/jest-dom/vitest'

import { render, screen, waitFor } from '@testing-library/react'
import { StrictMode, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { afterEach, describe, expect, it } from 'vitest'

import { openLayerCount, trapTab, useModalLayer, type ModalLayerOptions } from './modalLayer'

/**
 * The modal layer stack (10-23 accessibility audit #8).
 *
 * `aria-modal` alone does not take the page behind a dialog out of Chrome's
 * accessibility tree — measured — so the layer marks it `inert`. Two facts about
 * jsdom 26 shape every assertion here:
 *
 *  - it has no `HTMLElement.prototype.inert`, and testing-library's `getByRole`
 *    does not skip an inert subtree, so "is this out of the tree" is asked as
 *    `closest('[inert]')`, the way the attribute is inherited;
 *  - it lets an element inside an inert subtree take focus, so `activeElement`
 *    cannot show that focus went back *after* the page was released. The order
 *    test reads the attribute from inside the opener's own `focus` listener.
 */

function isInert(el: Element): boolean {
  return el.closest('[inert]') !== null
}

interface LayerProps {
  name: string
  open: boolean
  /** Rendered in the tree like the drawer, rather than portalled like a dialog. */
  inline?: boolean
  initialFocus?: ModalLayerOptions['initialFocus']
  children?: ReactNode
}

function Layer({ name, open, inline = false, initialFocus, children }: LayerProps) {
  const ref = useRef<HTMLDivElement>(null)
  useModalLayer(ref, open, initialFocus === undefined ? undefined : { initialFocus })
  if (!open) return null
  const panel = (
    <div ref={ref} role="dialog" aria-label={name} tabIndex={-1}>
      {children}
    </div>
  )
  return inline ? panel : createPortal(panel, document.body)
}

interface SceneProps {
  a?: boolean
  b?: boolean
  aInline?: boolean
}

function Scene({ a = false, b = false, aInline = false }: SceneProps) {
  return (
    <>
      <button type="button">background</button>
      <div data-testid="beside-a">beside A</div>
      <Layer name="A" open={a} inline={aInline}>
        <button type="button">a-button</button>
      </Layer>
      <Layer name="B" open={b}>
        <button type="button">b-button</button>
      </Layer>
    </>
  )
}

/** Three layers, each mounted only while open — B holds a second control to open C from. */
function Three({ a = false, b = false, c = false }: { a?: boolean; b?: boolean; c?: boolean }) {
  return (
    <>
      <button type="button">background</button>
      <Layer name="A" open={a}>
        <button type="button">a-button</button>
        <button type="button">a-rescan</button>
      </Layer>
      <Layer name="B" open={b}>
        <button type="button">b-button</button>
        <button type="button">b-result</button>
      </Layer>
      <Layer name="C" open={c}>
        <button type="button">c-button</button>
        <button type="button">c-other</button>
      </Layer>
    </>
  )
}

/** A stays open; B is a *new* component when it appears, so StrictMode replays its effect. */
function LateB({ b = false }: { b?: boolean }) {
  return (
    <>
      <Layer name="A" open>
        <button type="button">a-button</button>
      </Layer>
      {b && (
        <Layer name="B" open>
          <button type="button">b-button</button>
        </Layer>
      )}
    </>
  )
}

const button = (name: string): HTMLElement => screen.getByRole('button', { name })

const foreign: Element[] = []
const teardown: (() => void)[] = []
afterEach(() => {
  for (const node of foreign.splice(0)) node.remove()
  for (const undo of teardown.splice(0)) undo()
})

interface FocusRecord {
  name: string
  inert: boolean
}

/**
 * Every focus that lands from now on, with whether its target sat in an inert
 * subtree *at that moment*. jsdom lets focus into inert succeed, so this is how
 * a test sees the focus a browser would have refused.
 */
function recordFocus(): FocusRecord[] {
  const log: FocusRecord[] = []
  const onFocus = (e: FocusEvent): void => {
    if (!(e.target instanceof Element)) return
    const name = e.target.getAttribute('aria-label') ?? e.target.textContent ?? ''
    log.push({ name, inert: isInert(e.target) })
  }
  document.addEventListener('focus', onFocus, true)
  teardown.push(() => {
    document.removeEventListener('focus', onFocus, true)
  })
  return log
}

const intoInert = (log: FocusRecord[]): string[] => log.filter((f) => f.inert).map((f) => f.name)

/** A page control outside anything React renders, to open layers from. */
function outsideButton(label: string): HTMLButtonElement {
  const el = document.createElement('button')
  el.type = 'button'
  el.textContent = label
  document.body.appendChild(el)
  foreign.push(el)
  return el
}

function appendForeign(parent: Element, inert = false): HTMLDivElement {
  const node = document.createElement('div')
  if (inert) node.setAttribute('inert', '')
  parent.appendChild(node)
  foreign.push(node)
  return node
}

describe('useModalLayer — inert background', () => {
  it('makes everything outside an open layer inert, and nothing once it closes', () => {
    const { rerender } = render(<Scene />)
    expect(isInert(button('background'))).toBe(false)

    rerender(<Scene a />)
    expect(isInert(button('background'))).toBe(true)
    expect(isInert(screen.getByTestId('beside-a'))).toBe(true)
    expect(isInert(button('a-button'))).toBe(false)
    expect(isInert(screen.getByRole('dialog', { name: 'A' }))).toBe(false)

    rerender(<Scene />)
    expect(document.querySelector('[inert]')).toBeNull()
  })

  it.each([
    ['portalled', false],
    ['in the tree, like the drawer', true],
  ])('closing the top of two layers hands the page back to the one below (A %s)', (_, aInline) => {
    const { rerender } = render(<Scene a aInline={aInline} />)
    expect(isInert(button('a-button'))).toBe(false)
    expect(isInert(button('background'))).toBe(true)

    rerender(<Scene a b aInline={aInline} />)
    expect(isInert(button('a-button'))).toBe(true)
    expect(isInert(button('background'))).toBe(true)
    expect(isInert(button('b-button'))).toBe(false)

    rerender(<Scene a aInline={aInline} />)
    expect(isInert(button('a-button'))).toBe(false)
    expect(isInert(screen.getByRole('dialog', { name: 'A' }))).toBe(false)
    expect(isInert(button('background'))).toBe(true)
    expect(isInert(screen.getByTestId('beside-a'))).toBe(true)

    rerender(<Scene aInline={aInline} />)
    expect(document.querySelector('[inert]')).toBeNull()
  })

  it('survives a close that is not last-in first-out', () => {
    const { rerender } = render(<Scene a b />)
    rerender(<Scene b />)
    expect(isInert(button('b-button'))).toBe(false)
    expect(isInert(button('background'))).toBe(true)

    rerender(<Scene />)
    expect(document.querySelector('[inert]')).toBeNull()
  })

  it('never releases an inert it did not set', () => {
    const alreadyInert = appendForeign(document.body, true)
    const { rerender } = render(<Scene a />)
    rerender(<Scene a b />)
    rerender(<Scene a />)
    rerender(<Scene />)
    expect(alreadyInert).toHaveAttribute('inert')
  })

  it('covers a portal appended to <body> while a layer is open', async () => {
    const { rerender } = render(<Scene a />)
    const late = appendForeign(document.body)
    await waitFor(() => {
      expect(late).toHaveAttribute('inert')
    })

    rerender(<Scene />)
    expect(late).not.toHaveAttribute('inert')
  })

  it('covers a sibling that appears beside an in-tree layer', async () => {
    const { container, rerender } = render(<Scene a aInline />)
    const late = appendForeign(container)
    await waitFor(() => {
      expect(late).toHaveAttribute('inert')
    })

    rerender(<Scene aInline />)
    expect(late).not.toHaveAttribute('inert')
  })

  it('leaves exactly one layer after StrictMode mounts, unmounts and remounts', () => {
    const { rerender } = render(
      <StrictMode>
        <Scene a />
      </StrictMode>,
    )
    expect(openLayerCount()).toBe(1)
    expect(isInert(button('background'))).toBe(true)

    rerender(
      <StrictMode>
        <Scene />
      </StrictMode>,
    )
    expect(openLayerCount()).toBe(0)
    expect(document.querySelector('[inert]')).toBeNull()
  })
})

describe('useModalLayer — focus', () => {
  it('moves focus to the first focusable, and back to the opener only after un-inerting it', () => {
    const { rerender } = render(<Scene />)
    const opener = button('background')
    opener.focus()

    rerender(<Scene a />)
    expect(button('a-button')).toHaveFocus()

    let inertWhenFocused: Element | null | undefined
    opener.addEventListener('focus', () => {
      inertWhenFocused = opener.closest('[inert]')
    })
    rerender(<Scene />)
    expect(opener).toHaveFocus()
    expect(inertWhenFocused).toBeNull()
  })

  it('returns focus into the lower layer, already un-inert, when the top one closes', () => {
    const { rerender } = render(<Scene a aInline />)
    const opener = button('a-button')
    expect(opener).toHaveFocus()

    rerender(<Scene a b aInline />)
    expect(button('b-button')).toHaveFocus()

    let inertWhenFocused: Element | null | undefined
    opener.addEventListener('focus', () => {
      inertWhenFocused = opener.closest('[inert]')
    })
    rerender(<Scene a aInline />)
    expect(opener).toHaveFocus()
    expect(inertWhenFocused).toBeNull()
  })

  it('restores the opener through StrictMode’s double mount', () => {
    const { rerender } = render(
      <StrictMode>
        <Scene />
      </StrictMode>,
    )
    const opener = button('background')
    opener.focus()
    rerender(
      <StrictMode>
        <Scene a />
      </StrictMode>,
    )
    expect(button('a-button')).toHaveFocus()
    rerender(
      <StrictMode>
        <Scene />
      </StrictMode>,
    )
    expect(opener).toHaveFocus()
  })

  it('replays an effect that mounts open under StrictMode, and an unmount leaves nothing behind', () => {
    const opener = document.createElement('button')
    opener.type = 'button'
    opener.textContent = 'outside opener'
    document.body.appendChild(opener)
    foreign.push(opener)
    opener.focus()
    const focused = recordFocus()

    const { unmount } = render(
      <StrictMode>
        <Layer name="A" open>
          <button type="button">a-button</button>
        </Layer>
      </StrictMode>,
    )
    expect(button('a-button')).toHaveFocus()
    expect(openLayerCount()).toBe(1)
    expect(isInert(opener)).toBe(true)

    unmount()
    expect(openLayerCount()).toBe(0)
    expect(document.querySelector('[inert]')).toBeNull()
    expect(opener).toHaveFocus()
    expect(intoInert(focused)).toEqual([])
  })

  // Review #1: the initial focus must wait until this layer's own path is out
  // of the inert set — a layer below may have inerted it in the same commit.
  it('focuses into a layer mounted in the same commit as the one below only once it is live', () => {
    const focused = recordFocus()
    render(<Scene a b />)
    expect(button('b-button')).toHaveFocus()
    expect(focused).toContainEqual({ name: 'b-button', inert: false })
    expect(intoInert(focused)).toEqual([])
  })

  it('focuses into a layer StrictMode replays over an open one only once it is live', () => {
    const { rerender } = render(
      <StrictMode>
        <LateB />
      </StrictMode>,
    )
    expect(button('a-button')).toHaveFocus()

    const focused = recordFocus()
    rerender(
      <StrictMode>
        <LateB b />
      </StrictMode>,
    )
    expect(button('b-button')).toHaveFocus()
    expect(openLayerCount()).toBe(2)
    expect(intoInert(focused)).toEqual([])

    rerender(
      <StrictMode>
        <LateB />
      </StrictMode>,
    )
    expect(openLayerCount()).toBe(1)
    expect(button('a-button')).toHaveFocus()
  })

  // Review #2: settings → palette → (a result) `?` → shortcuts, the palette
  // closes underneath, then the shortcuts dialog closes — its opener is gone.
  it('hands focus to the new top layer when the opener left with a layer closed underneath', () => {
    const { rerender } = render(<Three />)
    button('background').focus()
    rerender(<Three a />)
    rerender(<Three a b />)
    button('b-result').focus()
    rerender(<Three a b c />)
    expect(button('c-button')).toHaveFocus()

    button('c-other').focus()
    const focused = recordFocus()
    rerender(<Three a c />)
    // A layer closing under the top one leaves focus where it is in the top one.
    expect(button('c-other')).toHaveFocus()
    expect(focused).toEqual([])

    rerender(<Three a />)
    expect(button('a-button')).toHaveFocus()
    expect(focused).toContainEqual({ name: 'a-button', inert: false })

    rerender(<Three />)
    expect(button('background')).toHaveFocus()
    expect(intoInert(focused)).toEqual([])
  })

  it('treats <body> as no opener while a layer below is still open', () => {
    const { rerender } = render(<Three a />)
    expect(button('a-button')).toHaveFocus()
    // The focused row unmounted under the drawer: focus fell to <body>.
    button('a-button').blur()
    expect(document.activeElement).toBe(document.body)

    rerender(<Three a c />)
    rerender(<Three a />)
    expect(button('a-button')).toHaveFocus()
  })

  // Cycle 2 #1: settings' rescan opened the palette, then a scan-status
  // response disabled it — connected, not inert, and still unable to take focus.
  it('falls back to the top layer when the opener was disabled while covered', () => {
    const { rerender } = render(<Three a />)
    button('a-rescan').focus()
    rerender(<Three a b />)
    expect(button('b-button')).toHaveFocus()

    button('a-rescan').setAttribute('disabled', '')
    const focused = recordFocus()
    rerender(<Three a />)
    expect(button('a-button')).toHaveFocus()
    expect(focused).toContainEqual({ name: 'a-button', inert: false })
    expect(intoInert(focused)).toEqual([])
  })

  // Cycle 2 #2: StrictMode replays both setups after both cleanups, so A's
  // second setup would see its own control as the opener.
  it('keeps the page opener through StrictMode replaying two layers opened together', () => {
    const opener = outsideButton('outside opener')
    opener.focus()
    const { rerender } = render(
      <StrictMode>
        <Scene a b />
      </StrictMode>,
    )
    expect(button('b-button')).toHaveFocus()
    expect(openLayerCount()).toBe(2)

    rerender(
      <StrictMode>
        <Scene a />
      </StrictMode>,
    )
    expect(button('a-button')).toHaveFocus()
    rerender(
      <StrictMode>
        <Scene />
      </StrictMode>,
    )
    expect(opener).toHaveFocus()
  })

  it('captures a fresh opener each time the same layer opens again', () => {
    const { rerender } = render(<Scene />)
    button('background').focus()
    rerender(<Scene a />)
    rerender(<Scene />)
    expect(button('background')).toHaveFocus()

    const second = outsideButton('second opener')
    second.focus()
    rerender(<Scene a />)
    rerender(<Scene />)
    expect(second).toHaveFocus()
  })

  it('leaves focus alone when the last layer closes and its opener is gone', () => {
    const opener = document.createElement('button')
    opener.type = 'button'
    opener.textContent = 'gone'
    document.body.appendChild(opener)
    foreign.push(opener)
    opener.focus()

    const { rerender } = render(<Scene a />)
    opener.remove()
    rerender(<Scene />)
    expect(openLayerCount()).toBe(0)
    expect(document.activeElement).toBe(document.body)
  })

  it('focuses the container when nothing inside can take focus', () => {
    function Empty() {
      return <Layer name="empty" open />
    }
    render(<Empty />)
    expect(screen.getByRole('dialog', { name: 'empty' })).toHaveFocus()
  })

  it("focuses the container under initialFocus: 'container' even with controls inside", () => {
    render(
      <Layer name="drawer" open initialFocus="container">
        <button type="button">inside</button>
      </Layer>,
    )
    expect(screen.getByRole('dialog', { name: 'drawer' })).toHaveFocus()
  })
})

describe('trapTab', () => {
  function tab(shiftKey = false): KeyboardEvent {
    return new KeyboardEvent('keydown', { key: 'Tab', shiftKey, cancelable: true })
  }

  function box(...labels: string[]): HTMLDivElement {
    const el = document.createElement('div')
    el.tabIndex = -1
    for (const label of labels) {
      const b = document.createElement('button')
      b.textContent = label
      el.appendChild(b)
    }
    document.body.appendChild(el)
    foreign.push(el)
    return el
  }

  it('wraps Tab from the last to the first, and Shift+Tab from the first to the last', () => {
    const el = box('first', 'last')
    const [first, last] = [...el.querySelectorAll('button')]
    if (!first || !last) throw new Error('fixture')

    last.focus()
    const forward = tab()
    trapTab(forward, el)
    expect(first).toHaveFocus()
    expect(forward.defaultPrevented).toBe(true)

    const back = tab(true)
    trapTab(back, el)
    expect(last).toHaveFocus()
    expect(back.defaultPrevented).toBe(true)
  })

  it('keeps focus where it is when the container has nothing focusable', () => {
    const el = box()
    el.focus()
    const ev = tab()
    trapTab(ev, el)
    expect(ev.defaultPrevented).toBe(true)
    expect(el).toHaveFocus()
  })

  /**
   * Cycle 2 #3, measured in Chrome: the scan log's scroll area has nothing
   * focusable inside, so Chrome makes the area itself a Tab stop — one that
   * `FOCUSABLE` does not list. `tabIndex = -1` stands in for it here: jsdom
   * focuses no plain `<div>`, and -1 keeps it out of `FOCUSABLE` the same way.
   */
  function scrollArea(el: HTMLElement, before: Element | null = null): HTMLDivElement {
    const area = document.createElement('div')
    area.tabIndex = -1
    el.insertBefore(area, before)
    return area
  }

  it('wraps Tab from a focused scroll area after the last control, and leaves Shift+Tab to the browser', () => {
    const el = box('first', 'last')
    const area = scrollArea(el)
    const first = el.querySelector('button')

    area.focus()
    const back = tab(true)
    trapTab(back, el)
    expect(back.defaultPrevented).toBe(false)
    expect(area).toHaveFocus()

    const forward = tab()
    trapTab(forward, el)
    expect(forward.defaultPrevented).toBe(true)
    expect(first).toHaveFocus()
  })

  it('wraps Shift+Tab from a focused scroll area before the first control, and leaves Tab to the browser', () => {
    const el = box('first', 'last')
    const area = scrollArea(el, el.firstElementChild)
    const last = el.querySelectorAll('button')[1]

    area.focus()
    const forward = tab()
    trapTab(forward, el)
    expect(forward.defaultPrevented).toBe(false)
    expect(area).toHaveFocus()

    const back = tab(true)
    trapTab(back, el)
    expect(back.defaultPrevented).toBe(true)
    expect(last).toHaveFocus()
  })

  it('leaves both directions to the browser from a scroll area between two controls', () => {
    const el = box('first', 'last')
    const area = scrollArea(el, el.querySelectorAll('button')[1] ?? null)
    area.focus()
    for (const shiftKey of [false, true]) {
      const ev = tab(shiftKey)
      trapTab(ev, el)
      expect(ev.defaultPrevented).toBe(false)
    }
    expect(area).toHaveFocus()
  })

  /** One native radio group — one Tab stop — appended to `parent`, or put before `before`. */
  function radioGroup(
    parent: Element,
    name: string,
    values: readonly string[],
    checked?: string,
    before: Node | null = null,
  ): HTMLInputElement[] {
    return values.map((value) => {
      const radio = document.createElement('input')
      radio.type = 'radio'
      radio.name = name
      radio.value = value
      radio.setAttribute('aria-label', value)
      parent.insertBefore(radio, before)
      if (value === checked) radio.checked = true
      return radio
    })
  }

  function at<T>(xs: ArrayLike<T>, i: number): T {
    const x = xs[i]
    if (x === undefined) throw new Error('fixture')
    return x
  }

  /**
   * Cycle 2 #3b, measured in Chrome with theme = light: the theme `Seg` is a
   * native radio group, whose one Tab stop is the *checked* radio, while the
   * DOM-last radio is `system`. Tab from `light` with nothing tabbable after it
   * left the dialog for `<body>`.
   */
  it('wraps Tab from the checked radio of a trailing group to the first stop', () => {
    const el = box('first', 'second')
    const light = at(radioGroup(el, 'theme', ['dark', 'light', 'system'], 'light'), 1)

    light.focus()
    const ev = tab()
    trapTab(ev, el)
    expect(ev.defaultPrevented).toBe(true)
    expect(at(el.querySelectorAll('button'), 0)).toHaveFocus()
  })

  it('wraps Shift+Tab from the first stop to the checked radio of a trailing group', () => {
    const el = box('first', 'second')
    const light = at(radioGroup(el, 'theme', ['dark', 'light', 'system'], 'light'), 1)

    at(el.querySelectorAll('button'), 0).focus()
    const ev = tab(true)
    trapTab(ev, el)
    expect(ev.defaultPrevented).toBe(true)
    expect(light).toHaveFocus()
  })

  it('wraps onto an unchecked group at its first radio going forward, its last going back', () => {
    const trailing = box('only')
    const after = radioGroup(trailing, 'g', ['a', 'b', 'c'])
    at(trailing.querySelectorAll('button'), 0).focus()
    const back = tab(true)
    trapTab(back, trailing)
    expect(back.defaultPrevented).toBe(true)
    expect(at(after, 2)).toHaveFocus()

    const leading = box('only')
    const before = radioGroup(leading, 'h', ['a', 'b', 'c'], undefined, leading.firstChild)
    at(leading.querySelectorAll('button'), 0).focus()
    const forward = tab()
    trapTab(forward, leading)
    expect(forward.defaultPrevented).toBe(true)
    expect(at(before, 0)).toHaveFocus()
  })

  it('counts radios with another name, or in another form, as another stop', () => {
    const named = box()
    const one = radioGroup(named, 'one', ['a', 'b'], 'a')
    radioGroup(named, 'two', ['a', 'b'], 'a')
    at(one, 0).focus()
    const ev = tab()
    trapTab(ev, named)
    expect(ev.defaultPrevented).toBe(false)

    const forms = box()
    const [formA, formB] = [document.createElement('form'), document.createElement('form')]
    forms.append(formA, formB)
    const inA = radioGroup(formA, 'same', ['a', 'b'], 'a')
    radioGroup(formB, 'same', ['a', 'b'], 'a')
    at(inA, 0).focus()
    const ev2 = tab()
    trapTab(ev2, forms)
    expect(ev2.defaultPrevented).toBe(false)
  })

  it('leaves Tab between two inner controls to the browser', () => {
    const el = box('first', 'middle', 'last')
    el.querySelector('button')?.focus()
    const ev = tab()
    trapTab(ev, el)
    expect(ev.defaultPrevented).toBe(false)
  })
})
