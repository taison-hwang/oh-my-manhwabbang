import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from 'react-router-dom'

// Order matters: the tokens must be defined before Tailwind's layers reference
// them, and the font faces before the first paint that uses them.
import './styles/fonts.css'
import './styles/tokens.css'
import './styles/base.css'

import { applyTheme } from './lib/theme'
import { router } from './router'
import { useUiStore } from './store/ui'

/**
 * Boot.
 *
 * `applyTheme` runs *before* the first render, from the value zustand's persist
 * middleware has already rehydrated out of `localStorage`. Doing it in an effect
 * instead would paint one frame of the wrong theme on every load — which on a
 * dark-theme user's machine is a white flash.
 *
 * The base path is resolved at import time inside `lib/basePath.ts`, which
 * `router.tsx` consumes; there is nothing async between here and the router.
 */
applyTheme(useUiStore.getState().theme)

const container = document.getElementById('root')
if (container === null) {
  throw new Error('mounting 석교만화방: #root is missing from index.html')
}

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // The index is a local SQLite file behind a localhost server: refetching
      // is cheap, but a 30 s window still collapses the storm of duplicate
      // requests a grid of cards would otherwise make.
      staleTime: 30_000,
      // A single-user LAN tool; window focus is not a signal that anything
      // changed, and the viewer refetching on every alt-tab is user-hostile.
      refetchOnWindowFocus: false,
      retry: 1,
    },
  },
})

createRoot(container).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
