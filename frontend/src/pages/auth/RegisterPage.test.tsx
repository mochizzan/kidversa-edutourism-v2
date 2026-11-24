/**
 * Register form (FL-01): client validation mirrors the server (email
 * format, password rule, WA digits) and blocks submission; a valid submit
 * posts the contract payload and surfaces `errors.<CODE>` on failure.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import RegisterPage from './RegisterPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, schoolsHandler } from '../../test/handlers'

function captureRegister(onBody: (body: unknown) => void) {
  return http.post('/api/auth/register', async ({ request }) => {
    onBody(await request.json())
    return HttpResponse.json({ data: { id: 9, status: 'pending' } })
  })
}

async function fillValidCore(
  user: UserEvent,
  overrides: { email?: string; password?: string; wa?: string } = {},
) {
  await user.type(screen.getByLabelText('Nama'), 'Budi Santoso')
  await user.type(screen.getByLabelText('Email'), overrides.email ?? 'budi@contoh.id')
  await user.type(screen.getByLabelText('Nomor WhatsApp'), overrides.wa ?? '081234567890')
  await user.type(screen.getByLabelText('Kata sandi'), overrides.password ?? 'abc12345')
}

describe('form register validation', () => {
  it('blocks a malformed email without posting', async () => {
    let registerCalls = 0
    server.use(schoolsHandler(), captureRegister(() => (registerCalls += 1)))
    const user = userEvent.setup()
    renderApp(<RegisterPage />)

    await user.type(screen.getByLabelText('Email'), 'budi@salah')
    await user.click(screen.getByRole('button', { name: 'Buat akun' }))

    await screen.findByText('Format email tidak valid.')
    expect(registerCalls).toBe(0)
  })

  it('enforces the password letter+digit minimum', async () => {
    let registerCalls = 0
    server.use(schoolsHandler(), captureRegister(() => (registerCalls += 1)))
    const user = userEvent.setup()
    renderApp(<RegisterPage />)

    await fillValidCore(user, { password: 'abc' })
    await user.click(screen.getByRole('button', { name: 'Buat akun' }))

    // The field hint carries the same sentence; the error makes it ≥2 matches.
    await screen.findAllByText('Minimal 8 karakter dan harus berisi huruf serta angka.')
    expect(registerCalls).toBe(0)
  })

  it('rejects non-digit WhatsApp input', async () => {
    let registerCalls = 0
    server.use(schoolsHandler(), captureRegister(() => (registerCalls += 1)))
    const user = userEvent.setup()
    renderApp(<RegisterPage />)

    await fillValidCore(user, { wa: '0812abc' })
    await user.click(screen.getByRole('button', { name: 'Buat akun' }))

    await screen.findByText('Nomor WhatsApp hanya boleh berisi angka.')
    expect(registerCalls).toBe(0)
  })
})

describe('form register submission', () => {
  it('posts the contract payload on a valid form and renders errors.EMAIL_TAKEN', async () => {
    let payload: unknown = null
    server.use(
      schoolsHandler(),
      http.post('/api/auth/register', async ({ request }) => {
        payload = await request.json()
        return fail('EMAIL_TAKEN')
      }),
    )
    const user = userEvent.setup()
    renderApp(<RegisterPage />)

    await fillValidCore(user)
    // Pick the school (antd Select) so the required rule passes.
    await user.click(screen.getByRole('combobox'))
    await user.click(await screen.findByText('Bandung'))
    await user.click(screen.getByRole('button', { name: 'Buat akun' }))

    await screen.findByText('Email sudah digunakan.')
    expect(payload).toEqual({
      name: 'Budi Santoso',
      email: 'budi@contoh.id',
      wa_number: '081234567890',
      password: 'abc12345',
      role: 'FASILITATOR',
      school_id: 1,
    })
  })
})
