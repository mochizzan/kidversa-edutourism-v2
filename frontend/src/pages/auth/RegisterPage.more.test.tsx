/**
 * Extra register acceptance (FL-01): school-load failure recovers through
 * the retry action, a valid submit posts the contract payload and swaps in
 * the "menunggu persetujuan admin" screen, the school rule blocks posting,
 * the role radio reaches the payload, and non-EMAIL_TAKEN failures render
 * through `errors.<CODE>`.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import RegisterPage from './RegisterPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, ok, schoolsHandler } from '../../test/handlers'

function captureRegister(onBody: (body: unknown) => void) {
  return http.post('/api/auth/register', async ({ request }) => {
    onBody(await request.json())
    return HttpResponse.json({ data: { id: 9, status: 'pending' } })
  })
}

async function fillValidCore(user: UserEvent) {
  await user.type(screen.getByLabelText('Nama'), 'Budi Santoso')
  await user.type(screen.getByLabelText('Email'), 'budi@contoh.id')
  await user.type(screen.getByLabelText('Nomor WhatsApp'), '081234567890')
  await user.type(screen.getByLabelText('Kata sandi'), 'abc12345')
}

async function pickSchool(user: UserEvent) {
  await user.click(screen.getByRole('combobox'))
  await user.click(await screen.findByText('Bandung'))
}

describe('register school loading', () => {
  it('shows an INTERNAL alert when schools fail to load and recovers via "Coba lagi"', async () => {
    let schoolsCalls = 0
    server.use(
      http.get('/api/auth/schools', () => {
        schoolsCalls += 1
        return schoolsCalls === 1
          ? fail('INTERNAL')
          : ok([{ id: 1, school_name: 'Bandung', country: 'Indonesia', city: null }])
      }),
    )
    const user = userEvent.setup()
    renderApp(<RegisterPage />)

    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))

    await waitFor(() => expect(schoolsCalls).toBe(2))
    await waitFor(() =>
      expect(screen.queryByText('Terjadi kesalahan tak terduga. Coba lagi.')).toBeNull(),
    )

    // The retried list reached the Select: opening it shows the option.
    await user.click(screen.getByRole('combobox'))
    await screen.findByText('Bandung')
  })
})

describe('register submission', () => {
  it('posts the contract payload and swaps in the registered screen with a "Masuk" link', async () => {
    let payload: unknown = null
    server.use(schoolsHandler(), captureRegister((body) => (payload = body)))
    const user = userEvent.setup()
    renderApp(<RegisterPage />)

    await fillValidCore(user)
    await pickSchool(user)
    await user.click(screen.getByRole('button', { name: 'Buat akun' }))

    await screen.findByText('Pendaftaran dikirim. Menunggu persetujuan admin.')
    expect(payload).toEqual({
      name: 'Budi Santoso',
      email: 'budi@contoh.id',
      wa_number: '081234567890',
      password: 'abc12345',
      role: 'FASILITATOR',
      school_id: 1,
    })
    expect(screen.getAllByRole('link', { name: 'Masuk' }).length).toBeGreaterThan(0)
    expect(screen.queryByRole('button', { name: 'Buat akun' })).toBeNull() // form replaced
    expect(screen.queryByLabelText('Email')).toBeNull()
  })

  it('requires a school before posting', async () => {
    let registerCalls = 0
    server.use(
      schoolsHandler(),
      http.post('/api/auth/register', () => {
        registerCalls += 1
        return HttpResponse.json({ data: { id: 9, status: 'pending' } })
      }),
    )
    const user = userEvent.setup()
    renderApp(<RegisterPage />)

    await fillValidCore(user) // everything valid except the school
    await user.click(screen.getByRole('button', { name: 'Buat akun' }))

    await screen.findByText('Wajib diisi.')
    expect(registerCalls).toBe(0)
  })

  it('sends role ADMIN when the Admin radio is selected', async () => {
    let payload: unknown = null
    server.use(schoolsHandler(), captureRegister((body) => (payload = body)))
    const user = userEvent.setup()
    renderApp(<RegisterPage />)

    await fillValidCore(user)
    // The radio input itself sits behind `pointer-events: none`; click the wrapper label.
    const adminRadio = screen.getByRole('radio', { name: 'Admin' })
    const adminLabel = adminRadio.closest('label')
    if (!adminLabel) throw new Error('Admin radio wrapper label not found')
    await user.click(adminLabel)
    expect(adminRadio).toBeChecked()
    await pickSchool(user)
    await user.click(screen.getByRole('button', { name: 'Buat akun' }))

    await screen.findByText('Pendaftaran dikirim. Menunggu persetujuan admin.')
    expect(payload).toEqual({
      name: 'Budi Santoso',
      email: 'budi@contoh.id',
      wa_number: '081234567890',
      password: 'abc12345',
      role: 'ADMIN',
      school_id: 1,
    })
  })

  it('renders errors.RATE_LIMITED for a business failure other than EMAIL_TAKEN', async () => {
    server.use(schoolsHandler(), http.post('/api/auth/register', () => fail('RATE_LIMITED')))
    const user = userEvent.setup()
    renderApp(<RegisterPage />)

    await fillValidCore(user)
    await pickSchool(user)
    await user.click(screen.getByRole('button', { name: 'Buat akun' }))

    await screen.findByText('Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.')
  })
})
