/**
 * The ONE render helper: AuthProvider + I18nextProvider + ConfigProvider +
 * antd App (pages use `App.useApp()` for toasts) + MemoryRouter.
 *
 * - `session` given → seeds the browser-session marker + memory access token
 *   + `kv-school` and registers the boot-refresh handler, so AuthProvider
 *   restores the session through the REAL refresh round-trip (same path as a
 *   page reload). Custom restore behaviour passes `refresh` explicitly.
 * - `routes` renders a route table (guards/redirects) instead of `ui`.
 * - Omit `session` for anonymous context (ready immediately, no refresh).
 */
import { render, type RenderResult } from '@testing-library/react'
import type { ReactElement } from 'react'
import type { HttpHandler } from 'msw'
import { App as AntApp, ConfigProvider } from 'antd'
import { I18nextProvider } from 'react-i18next'
import { MemoryRouter, useRoutes, type InitialEntry, type RouteObject } from 'react-router'
import { setAccessToken, setSessionSchool } from '../lib/api'
import { AuthProvider, type LoginData } from '../lib/auth'
import i18n from '../lib/i18n'
import { server } from './setup'
import { refreshHandler } from './handlers'

export interface RenderAppOptions {
  /** Initial MemoryRouter entry (path, may carry a query string and state). */
  route?: InitialEntry
  /** Route table (guards/redirects) — rendered instead of the `ui` element. */
  routes?: RouteObject[]
  /** Authenticated context: seeded before mount, restored via refresh. */
  session?: LoginData
  /** Boot-refresh handler override (defaults to a success with `session`). */
  refresh?: HttpHandler
}
function RoutesBody({ routes }: { routes: RouteObject[] }) {
  return useRoutes(routes)
}

export function renderApp(ui: ReactElement | null, options: RenderAppOptions = {}): RenderResult {
  const { route = '/', routes, session, refresh } = options
  if (session) {
    localStorage.setItem('kv-session', '1')
    setAccessToken(session.access)
    if (session.school) {
      localStorage.setItem('kv-school', String(session.school.id))
      setSessionSchool(session.school.id)
    }
    server.use(refresh ?? refreshHandler(session))
  }
  return render(
    <AuthProvider>
      <I18nextProvider i18n={i18n}>
        <ConfigProvider>
          <AntApp>
            <MemoryRouter initialEntries={[route]}>
              {routes ? <RoutesBody routes={routes} /> : ui}
            </MemoryRouter>
          </AntApp>
        </ConfigProvider>
      </I18nextProvider>
    </AuthProvider>,
  )
}
