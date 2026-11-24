/**
 * Login integration (FL-02 + 08 navigation acceptance): the anonymous deep
 * link redirects to /login and returns after a successful login; role guards
 * send FAS to /sessions while SA/ADMIN reach the admin home.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import type { RouteObject } from 'react-router'
import { getAccessToken } from '../../lib/api'
import { RequireRole } from '../../router'
import LoginPage from './LoginPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser } from '../../test/handlers'

const AUTH_ROUTES: RouteObject[] = [
  { path: '/sessions', element: <RequireRole><div>SESSIONS PAGE</div></RequireRole> },
  { path: '/login', element: <LoginPage /> },
  {
    path: '/',
    element: (
      <RequireRole roles={['SA', 'ADMIN']}>
        <div>HOME PAGE</div>
      </RequireRole>
    ),
  },
]

describe('auth navigation login flow', () => {
  it('redirects an anonymous deep link to /login and back after login', async () => {
    let payload: unknown = null
    server.use(
      http.post('/api/auth/login', async ({ request }) => {
        payload = await request.json()
        return HttpResponse.json({ data: makeLoginData() })
      }),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: AUTH_ROUTES, route: '/sessions' })

    // Guard: anonymous visitors bounce to /login before any content renders.
    const email = await screen.findByLabelText('Email')
    await user.type(email, ' Admin@Kidversa.ID ')
    await user.type(screen.getByLabelText('Kata sandi'), 'rahasia1')
    await user.click(screen.getByRole('button', { name: 'Masuk' }))

    await screen.findByText('SESSIONS PAGE') // returned to the deep link
    expect(payload).toEqual({ email: 'admin@kidversa.id', password: 'rahasia1' })
    expect(getAccessToken()).toBe(makeLoginData().access) // memory token adopted
    expect(localStorage.getItem('kv-school')).toBe('1')
  })

  it('sends a FASILITATOR away from the admin home to /sessions', async () => {
    renderApp(null, {
      routes: AUTH_ROUTES,
      route: '/',
      session: makeLoginData({ user: makeUser({ role: 'FASILITATOR' }) }),
    })

    await screen.findByText('SESSIONS PAGE')
  })

  it('lets SA/ADMIN render the admin home', async () => {
    renderApp(null, { routes: AUTH_ROUTES, route: '/', session: makeLoginData() })

    await screen.findByText('HOME PAGE')
  })
})
