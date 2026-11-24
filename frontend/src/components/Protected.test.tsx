/**
 * Protected (spec 02§2s): renders the RequireRole decision through a REAL
 * router — the boot spinner holds the current route while `ready` is false
 * (no premature redirect), `anon` redirects to /login with the deep-link
 * origin in `location.state.from` (path + query), `forbidden` redirects to
 * `decision.redirectTo`, `allow` renders the guarded children.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { useLocation, type RouteObject } from 'react-router'
import Protected from './Protected'
import type { AuthDecision } from '../lib/auth'
import { renderApp } from '../test/render'

/** Stands in for LoginPage: exposes the redirect state as JSON text. */
function LoginPageProbe() {
  const location = useLocation()
  return <div>{JSON.stringify(location.state)}</div>
}

function renderProtected(ready: boolean, decision: AuthDecision, route = '/dashboard') {
  const routes: RouteObject[] = [
    { path: '/login', element: <LoginPageProbe /> },
    {
      path: '/dashboard',
      element: (
        <Protected decision={decision} ready={ready}>
          <div>konten-terlindungi</div>
        </Protected>
      ),
    },
    { path: '/fasilitator', element: <div>halaman-fas</div> },
  ]
  return renderApp(null, { routes, route })
}

describe('Protected decision rendering', () => {
  it('holds on the boot spinner without redirecting while the restore has not settled', () => {
    const { container } = renderProtected(false, { status: 'anon' })

    expect(container.querySelector('.ant-flex')).toBeInTheDocument()
    expect(container.querySelector('.ant-spin-spinning')).toBeInTheDocument()
    // Still on /dashboard: no Navigate ran (neither /login nor children).
    expect(screen.queryByText('{"from":"/dashboard"}')).toBeNull()
    expect(screen.queryByText('konten-terlindungi')).toBeNull()
  })

  it('redirects an anonymous decision to /login carrying the origin path', async () => {
    renderProtected(true, { status: 'anon' })

    expect(await screen.findByText('{"from":"/dashboard"}')).toBeInTheDocument()
    expect(screen.queryByText('konten-terlindungi')).toBeNull()
    expect(screen.queryByText('halaman-fas')).toBeNull()
  })

  it('preserves the query string in the deep link', async () => {
    renderProtected(true, { status: 'anon' }, '/dashboard?tab=umum')

    expect(await screen.findByText('{"from":"/dashboard?tab=umum"}')).toBeInTheDocument()
  })

  it('sends a forbidden decision to its redirectTo', async () => {
    renderProtected(true, { status: 'forbidden', redirectTo: '/fasilitator' })

    expect(await screen.findByText('halaman-fas')).toBeInTheDocument()
    expect(screen.queryByText('konten-terlindungi')).toBeNull()
    expect(screen.queryByText('{"from":"/dashboard"}')).toBeNull()
  })

  it('renders the guarded children for an allowed decision', () => {
    renderProtected(true, { status: 'allow' })

    expect(screen.getByText('konten-terlindungi')).toBeInTheDocument()
    expect(screen.queryByText('{"from":"/dashboard"}')).toBeNull()
    expect(screen.queryByText('halaman-fas')).toBeNull()
  })
})
