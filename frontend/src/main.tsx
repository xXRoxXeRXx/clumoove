import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import './i18n'
import App from './App.tsx'
import { ErrorBoundary } from './components/ErrorBoundary.tsx'
import { isBoundaryError } from './utils/boundaryErrors.ts'
import { logger } from './utils/logger'

// Dev-only global error capture. These fire for errors that escape React's
// render tree (e.g. async callbacks, event handlers). In production they are
// intentionally silent — no telemetry is sent to the backend.
if (import.meta.env.DEV) {
  window.addEventListener('error', (event) => {
    if (event.error && !isBoundaryError(event.error)) {
      logger.error('Unhandled window error', event.error, { route: window.location.pathname })
    }
  })

  window.addEventListener('unhandledrejection', (event) => {
    const reason = event.reason instanceof Error ? event.reason : new Error(String(event.reason))
    if (!isBoundaryError(reason)) {
      logger.error('Unhandled promise rejection', reason, { route: window.location.pathname })
    }
  })
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
)
