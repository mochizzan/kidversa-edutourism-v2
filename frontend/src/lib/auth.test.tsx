/**
 * Auth context (spec F-01 / FL-02, 02§3): login keeps the access token in
 * memory with user/school + `kv-school` persistence, logout revokes and
 * clears, school switch moves the X-School-ID source, boot refresh rotates
 * the session, and `requireAuth` drives the router decisions.
 */
import { describe, expect, it } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { api, getAccessToken } from './api'
import { requireAuth, useAuth, type LoginData } from './auth'
import { emitSignal } from './stream'
import { renderApp } from '../test/render'
import { server } from '../test/setup'
import {
  makeLoginData,
  makeSchool,
  makeUser,
  refreshHandler,
  switchSchoolHandler,
} from '../test/handlers'

const SESSION: LoginData = makeLoginData()

function AuthProbe() {
  const auth = useAuth()
  return (
    <div>
      <span data-testid="user">{auth.user ? `${auth.user.email}/${auth.user.role}` : 'anon'}</span>
      <span data-testid="school">{auth.school ? `${auth.school.id}/${auth.school.school_name}` : 'none'}</span>
      <span data-testid="ready">{String(auth.ready)}</span>
      <button onClick={() => auth.setSession(SESSION)}>login</button>
      <button onClick={() => void auth.logout()}>logout</button>
      <button onClick={() => void auth.switchSchool(2)}>switch</button>
    </div>
  )
}

describe('auth context', () => {
  it('useAuth() outside <AuthProvider> is a hard contract error', () => {
    // Rendering straight into the container (renderApp would add the provider)
    // exercises the missing-context guard.
    function Orphan() {
      useAuth()
      return null
    }
    expect(() => render(<Orphan />)).toThrow(
      'useAuth() must be used inside <AuthProvider>',
    )
  })

  it('login stores access in memory plus user/school and persists kv-school', async () => {
    const user = userEvent.setup()
    renderApp(<AuthProbe />)

    expect(screen.getByTestId('ready').textContent).toBe('true')
    expect(screen.getByTestId('user').textContent).toBe('anon')

    await user.click(screen.getByRole('button', { name: 'login' }))

    expect(screen.getByTestId('user').textContent).toBe('admin@kidversa.id/ADMIN')
    expect(screen.getByTestId('school').textContent).toBe('1/Bandung')
    expect(getAccessToken()).toBe(SESSION.access) // memory only
    expect(localStorage.getItem('kv-school')).toBe('1')
    expect(localStorage.getItem('kv-session')).toBe('1')
  })

  it('logout posts /api/auth/logout and clears memory, kv-school and kv-session', async () => {
    let logoutCalls = 0
    server.use(http.post('/api/auth/logout', () => {
      logoutCalls += 1
      return HttpResponse.json({ data: {} })
    }))
    const user = userEvent.setup()
    renderApp(<AuthProbe />, { session: SESSION })

    await screen.findByText('admin@kidversa.id/ADMIN') // boot refresh settled
    await user.click(screen.getByRole('button', { name: 'logout' }))

    await screen.findByText('anon')
    expect(logoutCalls).toBe(1)
    expect(getAccessToken()).toBeNull()
    expect(localStorage.getItem('kv-school')).toBeNull()
    expect(localStorage.getItem('kv-session')).toBeNull()
  })

  it('school switch updates the X-School-ID header source', async () => {
    const jakarta = makeSchool({ id: 2, school_name: 'Jakarta' })
    server.use(switchSchoolHandler(jakarta))
    let schoolHeader: string | null = null
    server.use(
      http.get('/api/dashboard/overview', ({ request }) => {
        schoolHeader = request.headers.get('X-School-ID')
        return HttpResponse.json({ data: { children: 0 } })
      }),
    )
    const user = userEvent.setup()
    renderApp(<AuthProbe />, { session: SESSION })

    await screen.findByText('admin@kidversa.id/ADMIN')
    await user.click(screen.getByRole('button', { name: 'switch' }))

    await screen.findByText('2/Jakarta')
    expect(localStorage.getItem('kv-school')).toBe('2')
    await api.get('/api/dashboard/overview')
    expect(schoolHeader).toBe('2')
  })

  it('boot refresh rotates the session (new access + school adopted from refresh)', async () => {
    const rotated = makeLoginData({
      access: 'rotated-access',
      user: makeUser({ email: 'restored@kidversa.id' }),
      school: makeSchool({ id: 7, school_name: 'Solo' }),
    })
    renderApp(<AuthProbe />, { session: SESSION, refresh: refreshHandler(rotated) })

    await screen.findByText('restored@kidversa.id/ADMIN')
    expect(screen.getByTestId('school').textContent).toBe('7/Solo')
    expect(getAccessToken()).toBe('rotated-access')
    expect(localStorage.getItem('kv-school')).toBe('7')
  })

  it('a dead refresh cookie logs out on boot without looping', async () => {
    let refreshCalls = 0
    const deadRefresh = http.post('/api/auth/refresh', () => {
      refreshCalls += 1
      return HttpResponse.json({ error: { code: 'INVALID_TOKEN', message: 'gone' } })
    })
    renderApp(<AuthProbe />, { session: SESSION, refresh: deadRefresh })

    // ready flips only after the boot refresh attempt settles.
    await waitFor(() => expect(screen.getByTestId('ready').textContent).toBe('true'))
    expect(screen.getByTestId('user').textContent).toBe('anon')
    // Two attempts, then stop (no loop): the boot restore POST, and the api
    // client's refresh-once it triggers (the boot path does not set
    // skipAuthRefresh). Both dead → local session cleared.
    expect(refreshCalls).toBe(2)
    expect(getAccessToken()).toBeNull()
    expect(localStorage.getItem('kv-session')).toBeNull()
    expect(localStorage.getItem('kv-school')).toBeNull()
  })

  it('own-school school.changed reloads the context; a foreign school does not', async () => {
    let refreshes = 0
    const countingRefresh = http.post('/api/auth/refresh', () => {
      refreshes += 1
      return HttpResponse.json({ data: SESSION })
    })
    renderApp(<AuthProbe />, { session: SESSION, refresh: countingRefresh })

    await screen.findByText('admin@kidversa.id/ADMIN') // boot refresh settled
    expect(refreshes).toBe(1)

    // Own school (id 1) → the context re-fetches through the SAME refresh path.
    act(() => {
      emitSignal('school.changed', { school_id: 1, entity_id: 1, rev: 1 })
    })
    await waitFor(() => expect(refreshes).toBe(2))

    // A foreign school id → the handler must not touch the network at all.
    act(() => {
      emitSignal('school.changed', { school_id: 999 })
    })
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 50)
    await act(async () => {
      await promise
    })
    expect(refreshes).toBe(2)
    expect(screen.getByTestId('school').textContent).toBe('1/Bandung')
  })
})

describe('requireAuth decisions', () => {
  it('anonymous visitors are always sent to login', () => {
    expect(requireAuth(null)).toEqual({ status: 'anon' })
    expect(requireAuth(null, ['SA', 'ADMIN'])).toEqual({ status: 'anon' })
  })

  it('role mismatch forbids with the role home (FAS → /sessions, others → /)', () => {
    expect(requireAuth(makeUser({ role: 'ADMIN' }), ['SA'])).toEqual({
      status: 'forbidden',
      redirectTo: '/',
    })
    expect(requireAuth(makeUser({ role: 'FASILITATOR' }), ['SA', 'ADMIN'])).toEqual({
      status: 'forbidden',
      redirectTo: '/sessions',
    })
  })

  it('matching roles and role-less guards allow', () => {
    expect(requireAuth(makeUser({ role: 'SA' }), ['SA', 'ADMIN'])).toEqual({ status: 'allow' })
    expect(requireAuth(makeUser({ role: 'FASILITATOR' }), ['SA', 'ADMIN', 'FASILITATOR'])).toEqual({
      status: 'allow',
    })
    expect(requireAuth(makeUser({ role: 'FASILITATOR' }))).toEqual({ status: 'allow' })
  })
})
