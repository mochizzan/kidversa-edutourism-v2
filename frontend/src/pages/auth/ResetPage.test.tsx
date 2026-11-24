/**
 * Reset form (FL-03): client validation mirrors the server and blocks any
 * request; a valid submit posts `{email, code, new_password}` and swaps in
 * the done screen; RESET_EXPIRED / RESET_USED / VALIDATION envelopes render
 * through `errors.<CODE>` and clear on the next submit. RESET_USED first
 * auto-logs-in with the new password (06§3 re-login disambiguation): a
 * session means the reset worked (done screen), only a refused login
 * surfaces the RESET_USED alert.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http, type HttpHandler } from 'msw'
import type { RouteObject } from 'react-router'
import ResetPage from './ResetPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, ok } from '../../test/handlers'

const RESET_ROUTES: RouteObject[] = [
  { path: '/', element: <ResetPage /> },
  { path: '/login', element: <div>halaman-masuk</div> },
]

/** Counts POST /api/auth/reset; defaults to a `{}` success envelope. */
function countingReset(state: { calls: number }, respond?: () => Response): HttpHandler {
  return http.post('/api/auth/reset', () => {
    state.calls += 1
    return respond ? respond() : ok({})
  })
}

interface ResetOverrides {
  /** `null` leaves the field empty (untouched). */
  email?: string | null
  code?: string | null
  password?: string | null
  confirm?: string | null
}

async function fillReset(user: UserEvent, overrides: ResetOverrides = {}) {
  const email = overrides.email === undefined ? 'admin@kidversa.id' : overrides.email
  const code = overrides.code === undefined ? 'AB12CD34' : overrides.code
  const password = overrides.password === undefined ? 'abc12345' : overrides.password
  const confirm =
    overrides.confirm === undefined
      ? password ?? 'abc12345'
      : overrides.confirm

  if (email !== null) await user.type(screen.getByLabelText('Email'), email)
  if (code !== null) await user.type(screen.getByLabelText('Kode reset'), code)
  if (password !== null) await user.type(screen.getByLabelText('Kata sandi baru'), password)
  if (confirm !== null) await user.type(screen.getByLabelText('Ulangi kata sandi baru'), confirm)
}

describe('reset form validation', () => {
  it('blocks a malformed email without posting', async () => {
    const state = { calls: 0 }
    server.use(countingReset(state))
    const user = userEvent.setup()
    renderApp(<ResetPage />)

    await fillReset(user, { email: 'budi@salah' })
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))

    await screen.findByText('Format email tidak valid.')
    expect(state.calls).toBe(0)
  })

  it('blocks an empty reset code without posting', async () => {
    const state = { calls: 0 }
    server.use(countingReset(state))
    const user = userEvent.setup()
    renderApp(<ResetPage />)

    await fillReset(user, { code: null })
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))

    await screen.findByText('Wajib diisi.')
    expect(state.calls).toBe(0)
  })

  it('enforces the password letter+digit minimum without posting', async () => {
    const state = { calls: 0 }
    server.use(countingReset(state))
    const user = userEvent.setup()
    renderApp(<ResetPage />)

    await fillReset(user, { password: 'abcdefgh', confirm: 'abcdefgh' })
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))

    // The field hint carries the same sentence; the error makes it ≥2 matches.
    const matches = await screen.findAllByText(
      'Minimal 8 karakter dan harus berisi huruf serta angka.',
    )
    expect(matches.length).toBeGreaterThanOrEqual(2)
    expect(state.calls).toBe(0)
  })

  it('blocks a mismatched confirmation without posting', async () => {
    const state = { calls: 0 }
    server.use(countingReset(state))
    const user = userEvent.setup()
    renderApp(<ResetPage />)

    await fillReset(user, { confirm: 'xyz98765' })
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))

    // The confirm field surfaces its own mismatch sentence (the required rule
    // and the validator are separate rules, so the validator's text wins) and
    // the POST stays blocked.
    const confirmItem = screen.getByLabelText('Ulangi kata sandi baru').closest('.ant-form-item')
    if (!confirmItem) throw new Error('confirm form item not found')
    await waitFor(() =>
      expect(confirmItem.querySelector('.ant-form-item-explain-error')).toHaveTextContent(
        'Kata sandi tidak sama.',
      ),
    )
    expect(state.calls).toBe(0)
  })

  it('blocks an empty confirmation without posting', async () => {
    const state = { calls: 0 }
    server.use(countingReset(state))
    const user = userEvent.setup()
    renderApp(<ResetPage />)

    await fillReset(user, { confirm: null })
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))

    // The dedicated required rule fires on the empty field ("Wajib diisi.") —
    // async-validator skips `required` only when it shares a rule object with
    // a custom validator — so the submit never reaches the request.
    const confirmItem = screen.getByLabelText('Ulangi kata sandi baru').closest('.ant-form-item')
    if (!confirmItem) throw new Error('confirm form item not found')
    await waitFor(() =>
      expect(confirmItem.querySelector('.ant-form-item-explain-error')).toHaveTextContent(
        'Wajib diisi.',
      ),
    )
    expect(screen.queryByText('Kata sandi berhasil diganti. Silakan masuk.')).toBeNull()
    expect(state.calls).toBe(0)
  })
})

describe('reset form submission', () => {
  it('posts the normalized payload, swaps in the done screen and links to login', async () => {
    const state = { calls: 0 }
    let payload: unknown = null
    server.use(
      http.post('/api/auth/reset', async ({ request }) => {
        state.calls += 1
        payload = await request.json()
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: RESET_ROUTES, route: '/' })

    await fillReset(user, { email: ' Admin@Kidversa.ID ', code: ' AB12CD3' })
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))

    await screen.findByText('Kata sandi berhasil diganti. Silakan masuk.')
    expect(payload).toEqual({
      email: 'admin@kidversa.id',
      code: 'AB12CD3',
      new_password: 'abc12345',
    })
    expect(state.calls).toBe(1)
    expect(screen.queryByRole('button', { name: 'Ganti kata sandi' })).toBeNull() // form replaced

    await user.click(screen.getAllByRole('link', { name: 'Masuk' })[0])
    await screen.findByText('halaman-masuk')
  })

  it('surfaces RESET_EXPIRED from the envelope', async () => {
    const state = { calls: 0 }
    server.use(countingReset(state, () => fail('RESET_EXPIRED')))
    const user = userEvent.setup()
    renderApp(<ResetPage />)

    await fillReset(user)
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))

    await screen.findByText('Kode kedaluwarsa. Minta kode baru.')
    expect(state.calls).toBe(1)
  })

  it('RESET_USED with a refused re-login keeps the RESET_USED alert, then the next attempt reports VALIDATION', async () => {
    const state = { calls: 0 }
    server.use(
      countingReset(state, () => (state.calls === 1 ? fail('RESET_USED') : fail('VALIDATION'))),
      // The auto-login disambiguation is refused → the RESET_USED alert stands.
      http.post('/api/auth/login', () => fail('INVALID_CREDENTIALS')),
    )
    const user = userEvent.setup()
    renderApp(<ResetPage />)

    await fillReset(user)
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))
    await screen.findByText('Kode sudah dipakai. Minta kode baru.')

    // The alert clears on retry and the new envelope's message takes over.
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))
    await screen.findByText('Data tidak valid. Periksa kembali isian Anda.')
    expect(screen.queryByText('Kode sudah dipakai. Minta kode baru.')).toBeNull()
    expect(state.calls).toBe(2)
  })

  it('RESET_USED auto-logs in with the new password and lands on the done screen', async () => {
    const state = { calls: 0 }
    let login: unknown = null
    server.use(
      countingReset(state, () => fail('RESET_USED')),
      http.post('/api/auth/login', async ({ request }) => {
        login = await request.json()
        return ok(makeLoginData())
      }),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: RESET_ROUTES, route: '/' })

    await fillReset(user, { email: ' Admin@Kidversa.ID ' })
    await user.click(screen.getByRole('button', { name: 'Ganti kata sandi' }))

    // The password DID change → the re-login succeeds → done, no RESET_USED alert.
    await screen.findByText('Kata sandi berhasil diganti. Silakan masuk.')
    expect(screen.queryByText('Kode sudah dipakai. Minta kode baru.')).toBeNull()
    expect(login).toEqual({ email: 'admin@kidversa.id', password: 'abc12345' })
    // The session was adopted (AuthProvider stores the marker on setSession).
    expect(localStorage.getItem('kv-session')).toBe('1')
    expect(state.calls).toBe(1)
  })
})
