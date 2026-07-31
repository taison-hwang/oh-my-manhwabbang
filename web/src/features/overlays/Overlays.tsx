import { useUiStore } from '../../store/ui'
import { CommandPalette } from './CommandPalette'
import { SettingsDialog } from './SettingsDialog'
import { ShortcutsDialog } from './ShortcutsDialog'

/**
 * The three overlays, mounted from `store/ui.ts`.
 *
 * Overlays are **state, not routes** (impl-plan §5.2), which is why the router
 * has no entry for any of them and why this component takes no props: the open
 * set is a store slice, and the `Esc` ladder in `lib/useHotkeys.ts` pops it.
 *
 * One component so the shell mounts all three with a single line — WP-05 left
 * exactly that hole in `App.tsx` ("Dialogs (z-80) arrive with WP-10").
 */
export function Overlays() {
  const overlays = useUiStore((s) => s.overlays)
  const closeOverlay = useUiStore((s) => s.closeOverlay)
  const paletteQuery = useUiStore((s) => s.paletteQuery)
  const setPaletteQuery = useUiStore((s) => s.setPaletteQuery)

  return (
    <>
      <CommandPalette
        open={overlays.includes('palette')}
        query={paletteQuery}
        onQueryChange={setPaletteQuery}
        onClose={() => {
          closeOverlay('palette')
        }}
      />
      <SettingsDialog
        open={overlays.includes('settings')}
        onClose={() => {
          closeOverlay('settings')
        }}
      />
      <ShortcutsDialog
        open={overlays.includes('shortcuts')}
        onClose={() => {
          closeOverlay('shortcuts')
        }}
      />
    </>
  )
}
