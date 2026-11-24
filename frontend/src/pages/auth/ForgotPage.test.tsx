/**
 * Forgot-password form (FL-03): client validation blocks any request; a
 * valid submit posts the normalized email and always shows the same
 * "check email / ask admin" answer; failures render through `errors.<CODE>`
 * without wedging the form.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import type { RouteObject } from 'react-router'
import ForgotPage from './ForgotPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok } from '../../test/handlers'

const FORGOT_ROUTES: RouteObject[] = [
  { path: '/', element: <ForgotPage /> },
  { path: '/reset', element: <div>halaman-atur-ulang</div> },
]

describe('forgot form validation', () => {
  it('blocks a malformed email without posting', async () => {
    let forgotCalls = 0
    server.use(
      http.post('/api/auth/forgot', () => {
        forgotCalls += 1
        return ok({ sent: true })
      }),
    )
    const user = userEvent.setup()
    renderApp(<ForgotPage />)

    await user.type(await screen.findByLabelText('Email'), 'budi@salah')
    await user.click(screen.getByRole('button', { name: 'Kirim kode' }))

    await screen.findByText('Format email tidak valid.')
    expect(forgotCalls).toBe(0)
  })
})

describe('forgot form submission', () => {
  it('posts the normalized email, shows the neutral answer and links to the reset page', async () => {
    let payload: unknown = null
    server.use(
      http.post('/api/auth/forgot', async ({ request }) => {
        payload = await request.json()
        return ok({ sent: true })
      }),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: FORGOT_ROUTES, route: '/' })

    await user.type(await screen.findByLabelText('Email'), '  Admin@Kidversa.ID ')
    await user.click(screen.getByRole('button', { name: 'Kirim kode' }))

    await screen.findByText(
      'Jika email terdaftar, kode reset akan dikirim. Jika tidak menerima email, minta kode ke admin.',
    )
    expect(payload).toEqual({ email: 'admin@kidversa.id' })

    await user.click(screen.getByRole('link', { name: 'Buka halaman atur ulang' }))
    await screen.findByText('halaman-atur-ulang')
  })

  it('renders errors.RATE_LIMITED with Retry-After and stays usable for a retry', async () => {
    let forgotCalls = 0
    server.use(
      http.post('/api/auth/forgot', () => {
        forgotCalls += 1
        // `fail()` cannot attach headers; same envelope plus the Retry-After header.
        if (forgotCalls === 1) {
          return HttpResponse.json(
            { error: { code: 'RATE_LIMITED', message: 'RATE_LIMITED message' } },
            { headers: { 'Retry-After': '30' } },
          )
        }
        return ok({ sent: true })
      }),
    )
    const user = userEvent.setup()
    renderApp(<ForgotPage />)

    await user.type(await screen.findByLabelText('Email'), 'admin@kidversa.id')
    await user.click(screen.getByRole('button', { name: 'Kirim kode' }))

    await screen.findByText('Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.')

    // The form keeps its value; resubmitting clears the alert and succeeds.
    await user.click(screen.getByRole('button', { name: 'Kirim kode' }))

    await screen.findByText(
      'Jika email terdaftar, kode reset akan dikirim. Jika tidak menerima email, minta kode ke admin.',
    )
    expect(forgotCalls).toBe(2)
    expect(screen.queryByText('Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.')).toBeNull()
  })
})
