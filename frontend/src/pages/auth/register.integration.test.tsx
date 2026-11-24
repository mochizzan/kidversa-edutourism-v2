/**
 * Register integration (FL-01): a valid submission posts the contract
 * payload with an X-Idempotency-Key and lands on the pending-approval
 * message — the account is NOT active yet.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import type { RouteObject } from 'react-router'
import RegisterPage from './RegisterPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { schoolsHandler } from '../../test/handlers'

const ROUTES: RouteObject[] = [{ path: '/register', element: <RegisterPage /> }]

describe('form register pending flow', () => {
  it('submits a valid form and shows the pending message', async () => {
    let payload: unknown = null
    let idempotencyKey: string | null = null
    let registerCalls = 0
    server.use(
      schoolsHandler(),
      http.post('/api/auth/register', async ({ request }) => {
        registerCalls += 1
        idempotencyKey = request.headers.get('X-Idempotency-Key')
        payload = await request.json()
        return HttpResponse.json({ data: { id: 9, status: 'pending' } })
      }),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/register' })

    await user.type(await screen.findByLabelText('Nama'), 'Budi Santoso')
    await user.type(screen.getByLabelText('Email'), 'budi@contoh.id')
    await user.type(screen.getByLabelText('Nomor WhatsApp'), '081234567890')
    await user.type(screen.getByLabelText('Kata sandi'), 'abc12345')
    await user.click(screen.getByRole('combobox'))
    await user.click(await screen.findByText('Bandung'))
    await user.click(screen.getByRole('button', { name: 'Buat akun' }))

    await screen.findByText('Pendaftaran dikirim. Menunggu persetujuan admin.')
    expect(registerCalls).toBe(1)
    expect(idempotencyKey).toMatch(/^[0-9a-f-]{36}$/) // auto-generated idem key
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
