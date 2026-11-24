/**
 * Login form (FL-02 / 08 form acceptance): client validation mirrors the
 * server and blocks any request; API failures render through the
 * `errors.<CODE>` i18n key (06§1 rule — never the raw server message).
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import LoginPage from './LoginPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData } from '../../test/handlers'

describe('form login validation', () => {
  it('rejects a malformed email before any request is sent', async () => {
    let loginCalls = 0
    server.use(
      http.post('/api/auth/login', () => {
        loginCalls += 1
        return HttpResponse.json({ data: makeLoginData() })
      }),
    )
    const user = userEvent.setup()
    renderApp(<LoginPage />)

    await user.type(screen.getByLabelText('Email'), 'budi@salah')
    await user.click(screen.getByRole('button', { name: 'Masuk' }))

    await screen.findByText('Format email tidak valid.')
    expect(loginCalls).toBe(0)
  })

  it('requires the password', async () => {
    let loginCalls = 0
    server.use(
      http.post('/api/auth/login', () => {
        loginCalls += 1
        return HttpResponse.json({ data: makeLoginData() })
      }),
    )
    const user = userEvent.setup()
    renderApp(<LoginPage />)

    await user.type(screen.getByLabelText('Email'), 'admin@kidversa.id')
    await user.click(screen.getByRole('button', { name: 'Masuk' }))

    await screen.findByText('Wajib diisi.')
    expect(loginCalls).toBe(0)
  })
})

describe('form login error handling', () => {
  it('renders a 200 error envelope through errors.<CODE> and normalizes the email', async () => {
    let payload: unknown = null
    server.use(
      http.post('/api/auth/login', async ({ request }) => {
        payload = await request.json()
        return fail('INVALID_CREDENTIALS')
      }),
    )
    const user = userEvent.setup()
    renderApp(<LoginPage />)

    await user.type(screen.getByLabelText('Email'), '  Admin@Kidversa.ID ')
    await user.type(screen.getByLabelText('Kata sandi'), 'rahasia1')
    await user.click(screen.getByRole('button', { name: 'Masuk' }))

    await screen.findByRole('alert') // antd Alert with translated errors.INVALID_CREDENTIALS
    await screen.findByText('Email atau kata sandi salah.')
    expect(payload).toEqual({ email: 'admin@kidversa.id', password: 'rahasia1' })
  })
})
