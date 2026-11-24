/**
 * Extra login acceptance (FL-02): a successful submit adopts the session and
 * honours the deep-link `from` target, an `errors.<CODE>` alert clears when a
 * retry succeeds, a non-envelope 200 surfaces INTERNAL without navigating,
 * and the footer links reach the sibling auth pages.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { Navigate, type RouteObject } from 'react-router'
import LoginPage from './LoginPage'
import ForgotPage from './ForgotPage'
import RegisterPage from './RegisterPage'
import { getAccessToken } from '../../lib/api'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, ok, schoolsHandler } from '../../test/handlers'

/** Anonymous deep link to /login carrying `from: /panel` — the login success path. */
const DEEP_LINK_ROUTES: RouteObject[] = [
  { path: '/', element: <Navigate to="/login" state={{ from: '/panel' }} replace /> },
  { path: '/login', element: <LoginPage /> },
  { path: '/panel', element: <div>panel-utama</div> },
]

describe('login success navigation', () => {
  it('posts the normalized credentials, adopts the session and returns to the deep-link target', async () => {
    let payload: unknown = null
    server.use(
      http.post('/api/auth/login', async ({ request }) => {
        payload = await request.json()
        return ok(makeLoginData())
      }),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: DEEP_LINK_ROUTES, route: '/' })

    const email = await screen.findByLabelText('Email')
    await user.type(email, '  Admin@Kidversa.ID ')
    await user.type(screen.getByLabelText('Kata sandi'), 'rahasia1')
    await user.click(screen.getByRole('button', { name: 'Masuk' }))

    // Deep-link honored: after login the app lands on /panel, not on '/'.
    await screen.findByText('panel-utama')
    expect(payload).toEqual({ email: 'admin@kidversa.id', password: 'rahasia1' })
    expect(getAccessToken()).toBe(makeLoginData().access) // setSession ran
    expect(screen.queryByLabelText('Email')).toBeNull() // login form unmounted
    expect(screen.queryByRole('button', { name: 'Masuk' })).toBeNull()
  })

  it('clears the error alert when a retry succeeds and navigates', async () => {
    let loginCalls = 0
    server.use(
      http.post('/api/auth/login', () => {
        loginCalls += 1
        return loginCalls === 1 ? fail('INVALID_CREDENTIALS') : ok(makeLoginData())
      }),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: DEEP_LINK_ROUTES, route: '/' })

    await user.type(await screen.findByLabelText('Email'), 'admin@kidversa.id')
    await user.type(screen.getByLabelText('Kata sandi'), 'rahasia1')
    await user.click(screen.getByRole('button', { name: 'Masuk' }))

    await screen.findByText('Email atau kata sandi salah.')

    // Fields keep their values; resubmitting goes through and the alert is gone.
    await user.click(screen.getByRole('button', { name: 'Masuk' }))

    await screen.findByText('panel-utama')
    expect(screen.queryByText('Email atau kata sandi salah.')).toBeNull()
    expect(loginCalls).toBe(2)
  })
})

describe('login error handling', () => {
  it('maps a non-envelope 200 response to INTERNAL and stays on the login page', async () => {
    server.use(http.post('/api/auth/login', () => HttpResponse.json({ foo: 1 })))
    const user = userEvent.setup()
    renderApp(null, { routes: DEEP_LINK_ROUTES, route: '/' })

    await user.type(await screen.findByLabelText('Email'), 'admin@kidversa.id')
    await user.type(screen.getByLabelText('Kata sandi'), 'rahasia1')
    await user.click(screen.getByRole('button', { name: 'Masuk' }))

    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
    expect(screen.queryByText('panel-utama')).toBeNull() // no navigation
    expect(screen.getByLabelText('Email')).toBeInTheDocument() // form still up
  })
})

describe('login footer links', () => {
  it('routes "Lupa kata sandi?" to the forgot-password page', async () => {
    server.use() // this path fetches nothing
    const routes: RouteObject[] = [
      { path: '/', element: <LoginPage /> },
      { path: '/forgot', element: <ForgotPage /> },
    ]
    const user = userEvent.setup()
    renderApp(null, { routes, route: '/' })

    await user.click(await screen.findByRole('link', { name: 'Lupa kata sandi?' }))

    await screen.findByRole('heading', { name: 'Lupa kata sandi' })
    expect(screen.queryByRole('button', { name: 'Masuk' })).toBeNull() // login form gone
  })

  it('routes "Daftar" to the register page', async () => {
    server.use(schoolsHandler()) // RegisterPage fetches the school list on mount
    const routes: RouteObject[] = [
      { path: '/', element: <LoginPage /> },
      { path: '/register', element: <RegisterPage /> },
    ]
    const user = userEvent.setup()
    renderApp(null, { routes, route: '/' })

    await user.click(await screen.findByRole('link', { name: 'Daftar' }))

    await screen.findByRole('heading', { name: 'Daftar akun' })
    expect(await screen.findByRole('button', { name: 'Buat akun' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Masuk' })).toBeNull() // login form gone
  })
})

describe('remember-me and the boot-restore marker', () => {
  async function submitLogin(user: ReturnType<typeof userEvent.setup>): Promise<void> {
    await user.type(await screen.findByLabelText('Email'), 'admin@kidversa.id')
    await user.type(screen.getByLabelText('Kata sandi'), 'rahasia1')
    await user.click(screen.getByRole('button', { name: 'Masuk' }))
    await screen.findByText('panel-utama') // session adopted, logged in
  }

  it('login with the Remember toggle OFF sets no kv-session (return = anonymous)', async () => {
    server.use(http.post('/api/auth/login', () => ok(makeLoginData())))
    const user = userEvent.setup()
    renderApp(null, { routes: DEEP_LINK_ROUTES, route: '/' })

    await submitLogin(user)

    expect(getAccessToken()).toBe(makeLoginData().access) // logged in now…
    expect(localStorage.getItem('kv-session')).toBeNull() // …but the return does not auto-restore
  })

  it('login with the Remember toggle ON sets kv-session (auto-login on return)', async () => {
    server.use(http.post('/api/auth/login', () => ok(makeLoginData())))
    const user = userEvent.setup()
    renderApp(null, { routes: DEEP_LINK_ROUTES, route: '/' })

    await user.type(await screen.findByLabelText('Email'), 'admin@kidversa.id')
    await user.type(screen.getByLabelText('Kata sandi'), 'rahasia1')
    await user.click(screen.getByRole('switch', { name: 'Ingat saya' })) // toggle ON
    await user.click(screen.getByRole('button', { name: 'Masuk' }))
    await screen.findByText('panel-utama')

    expect(localStorage.getItem('kv-session')).toBe('1')
  })
})
