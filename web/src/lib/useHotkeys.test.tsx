import '@testing-library/jest-dom/vitest'

import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { useUiStore } from '../store/ui'
import { cancelChromeAutoHide, useViewerStore } from '../store/viewer'
import { isTypingTarget, useGlobalHotkeys } from './useHotkeys'

/** ui-spec §8.1 — the only three keys that are global. */

function Harness({ onExitViewer }: { onExitViewer?: () => void }) {
  useGlobalHotkeys(onExitViewer === undefined ? {} : { onExitViewer })
  return <input aria-label="시리즈 검색 (초성 가능)" />
}

beforeEach(() => {
  localStorage.clear()
  useUiStore.setState({ overlays: [], paletteQuery: '' })
  useViewerStore.getState().close()
  cancelChromeAutoHide()
})

describe('Ctrl/Cmd + K', () => {
  it('toggles the palette and clears its query', async () => {
    useUiStore.setState({ paletteQuery: '환타' })
    render(<Harness />)
    await userEvent.keyboard('{Control>}k{/Control}')
    expect(useUiStore.getState().overlays).toEqual(['palette'])
    expect(useUiStore.getState().paletteQuery).toBe('')
    await userEvent.keyboard('{Control>}k{/Control}')
    expect(useUiStore.getState().overlays).toEqual([])
  })

  it('also answers to Meta, for Apple keyboards', async () => {
    render(<Harness />)
    await userEvent.keyboard('{Meta>}k{/Meta}')
    expect(useUiStore.getState().overlays).toEqual(['palette'])
  })

  it('works from inside the search field', async () => {
    render(<Harness />)
    await userEvent.click(screen.getByLabelText('시리즈 검색 (초성 가능)'))
    await userEvent.keyboard('{Control>}k{/Control}')
    expect(useUiStore.getState().overlays).toEqual(['palette'])
  })

  it('preventDefault()s so the browser keeps neither its search bar nor its own K', () => {
    // ui-spec §8.1 spells `preventDefault()` out for this key alone. userEvent
    // swallows the result, so the event is dispatched by hand.
    render(<Harness />)
    for (const init of [{ ctrlKey: true }, { metaKey: true }]) {
      const event = new KeyboardEvent('keydown', {
        key: 'k',
        bubbles: true,
        cancelable: true,
        ...init,
      })
      window.dispatchEvent(event)
      expect(event.defaultPrevented).toBe(true)
    }
  })

  it('works from inside the viewer', async () => {
    useViewerStore.getState().open('bk', { pageCount: 10 })
    render(<Harness />)
    await userEvent.keyboard('{Control>}k{/Control}')
    expect(useUiStore.getState().overlays).toEqual(['palette'])
    expect(useViewerStore.getState().bookId).toBe('bk')
  })
})

describe('the Esc ladder', () => {
  it('closes the topmost overlay before touching the viewer', async () => {
    const onExitViewer = vi.fn()
    useViewerStore.getState().open('bk', { pageCount: 10 })
    useUiStore.getState().openOverlay('settings')
    useUiStore.getState().openOverlay('palette')
    render(<Harness onExitViewer={onExitViewer} />)

    await userEvent.keyboard('{Escape}')
    expect(useUiStore.getState().overlays).toEqual(['settings'])
    expect(useViewerStore.getState().bookId).toBe('bk')

    await userEvent.keyboard('{Escape}')
    expect(useUiStore.getState().overlays).toEqual([])
    expect(useViewerStore.getState().bookId).toBe('bk')
    expect(onExitViewer).not.toHaveBeenCalled()

    await userEvent.keyboard('{Escape}')
    expect(useViewerStore.getState().bookId).toBeNull()
    expect(onExitViewer).toHaveBeenCalledOnce()
  })

  it('does nothing when there is neither an overlay nor a viewer', async () => {
    const onExitViewer = vi.fn()
    render(<Harness onExitViewer={onExitViewer} />)
    await userEvent.keyboard('{Escape}')
    expect(onExitViewer).not.toHaveBeenCalled()
  })
})

describe('?', () => {
  it('opens the shortcuts dialog', async () => {
    render(<Harness />)
    await userEvent.keyboard('?')
    expect(useUiStore.getState().overlays).toEqual(['shortcuts'])
  })

  it('is inert while typing — the search field must be able to contain "?"', async () => {
    render(<Harness />)
    const field = screen.getByLabelText('시리즈 검색 (초성 가능)')
    await userEvent.click(field)
    await userEvent.keyboard('?')
    expect(useUiStore.getState().overlays).toEqual([])
    expect(field).toHaveValue('?')
  })
})

describe('isTypingTarget', () => {
  it('recognises the elements where a printable key means "type"', () => {
    for (const tag of ['input', 'textarea', 'select']) {
      expect(isTypingTarget(document.createElement(tag))).toBe(true)
    }
    // jsdom implements neither the `contentEditable` reflection nor
    // `isContentEditable`, so the attribute is set the way markup would.
    const editable = document.createElement('div')
    editable.setAttribute('contenteditable', 'true')
    expect(isTypingTarget(editable)).toBe(true)
  })

  it('does not swallow keys aimed at ordinary elements', () => {
    expect(isTypingTarget(document.createElement('div'))).toBe(false)
    expect(isTypingTarget(document.createElement('button'))).toBe(false)
    expect(isTypingTarget(null)).toBe(false)
  })
})
