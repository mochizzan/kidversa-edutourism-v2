/**
 * Children form (FL-06 / F-07): age 1–10 saves directly, 11–15 goes
 * through the confirm modal and posts `age_confirmed: true`, invalid ages
 * and unusable WA numbers are blocked client-side with the mirrored
 * messages and no POST.
 *
 * Field labels are queried INSIDE the form dialog — the list table's column
 * headers carry the same texts.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import ChildrenPage from './ChildrenPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeSchool, makeUser, paged } from '../../test/handlers'
import { expectToast } from '../../test/toast'

interface PostCapture {
  calls: number
  payload: unknown
}

function childrenHandlers(capture: PostCapture) {
  return [
    http.get('/api/children', () => paged([], { page: 1, per_page: 20, total: 0 })),
    http.post('/api/children', async ({ request }) => {
      capture.payload = await request.json()
      capture.calls += 1
      return HttpResponse.json({ data: { child: {} } })
    }),
  ]
}

async function openFilledForm(user: UserEvent, age: string): Promise<void> {
  // The PlusOutlined icon carries its own aria-label, so the accessible
  // name is "plus Tambah anak".
  await user.click(await screen.findByRole('button', { name: /Tambah anak/ }))
  const form = within(await screen.findByRole('dialog'))
  await user.type(form.getByLabelText('Nama anak'), 'Budi Santoso')
  await user.type(form.getByLabelText('Usia'), age)
  // New-child forms prefill the active school's name — replace it explicitly.
  const schoolInput = form.getByLabelText('Sekolah anak')
  await user.clear(schoolInput)
  await user.type(schoolInput, 'SD Ceria')
  await user.type(form.getByLabelText('Nama ibu'), 'Sari')
  await user.type(form.getByLabelText('WhatsApp ibu'), '081234567890')
}

function renderChildrenPage() {
  return renderApp(<ChildrenPage />, {
    session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
  })
}

describe('new-child school prefill', () => {
  it('prefills the active school name and keeps the field editable', async () => {
    const capture: PostCapture = { calls: 0, payload: null }
    server.use(...childrenHandlers(capture))
    const user = userEvent.setup()
    renderApp(<ChildrenPage />, {
      session: makeLoginData({
        user: makeUser({ role: 'ADMIN' }),
        school: makeSchool({ school_name: 'Sekolah Bandung' }),
      }),
    })

    await user.click(await screen.findByRole('button', { name: /Tambah anak/ }))
    const form = within(await screen.findByRole('dialog'))
    const schoolInput = form.getByLabelText('Sekolah anak')
    expect(schoolInput).toHaveValue('Sekolah Bandung')

    // Still an editable free-text field, not a locked select.
    await user.clear(schoolInput)
    await user.type(schoolInput, 'SD Ceria')
    await user.type(form.getByLabelText('Nama anak'), 'Budi Santoso')
    await user.type(form.getByLabelText('Usia'), '8')
    await user.type(form.getByLabelText('Nama ibu'), 'Sari')
    await user.type(form.getByLabelText('WhatsApp ibu'), '081234567890')
    await user.click(form.getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(capture.calls).toBe(1))
    expect(capture.payload).toMatchObject({ school: 'SD Ceria' })
  })
})

describe('form children age rules', () => {
  it('age 11–15 opens the confirm flow and posts age_confirmed', async () => {
    const capture: PostCapture = { calls: 0, payload: null }
    server.use(...childrenHandlers(capture))
    const user = userEvent.setup()
    renderChildrenPage()

    await openFilledForm(user, '12')
    await user.click(screen.getByRole('button', { name: 'Simpan' }))

    // Confirm modal only appears for the 11–15 band.
    await screen.findByText('Konfirmasi usia')
    await screen.findByText('Anak berusia 11–15 tahun perlu dikonfirmasi. Simpan sebagai 12 tahun?')
    expect(capture.calls).toBe(0)

    await user.click(screen.getByRole('button', { name: 'Ya' }))
    await waitFor(() => expect(capture.calls).toBe(1))
    expect(capture.payload).toMatchObject({
      name: 'Budi Santoso',
      age: 12,
      age_confirmed: true,
      school: 'SD Ceria',
      mother_name: 'Sari',
      mother_wa: '+6281234567890',
    })
    // Both modals close after the successful save.
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await expectToast('Anak “Budi Santoso” dibuat')
  })

  it('declining the age confirm closes the prompt without a POST', async () => {
    const capture: PostCapture = { calls: 0, payload: null }
    server.use(...childrenHandlers(capture))
    const user = userEvent.setup()
    renderChildrenPage()

    await openFilledForm(user, '13')
    await user.click(screen.getByRole('button', { name: 'Simpan' }))
    await screen.findByText('Konfirmasi usia')

    // 'Tidak' cancels the prompt: no POST, form still open for editing.
    await user.click(screen.getByRole('button', { name: 'Tidak' }))
    // antd keeps a closed modal mounted (display:none after the exit motion,
    // which is rAF-driven and slow under coverage load) → generous timeout.
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Ya' })).toBeNull(), {
      timeout: 10_000,
    })
    expect(capture.calls).toBe(0)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('blocks invalid ages (0, 16, non-integer) without a POST or confirm prompt', async () => {
    const capture: PostCapture = { calls: 0, payload: null }
    server.use(...childrenHandlers(capture))
    const user = userEvent.setup()
    renderChildrenPage()

    await openFilledForm(user, '99')
    await user.click(screen.getByRole('button', { name: 'Simpan' }))
    await screen.findByText('Usia harus bilangan bulat 1–15.')

    const form = within(await screen.findByRole('dialog'))
    for (const age of ['0', '16', '4.5', 'abc']) {
      const ageInput = form.getByLabelText('Usia')
      await user.clear(ageInput)
      await user.type(ageInput, age)
      await user.click(screen.getByRole('button', { name: 'Simpan' }))
    }
    await screen.findByText('Usia harus bilangan bulat 1–15.')
    expect(capture.calls).toBe(0)
    expect(screen.queryByText('Konfirmasi usia')).toBeNull() // no confirm flow for invalid ages
  })

  it('blocks a non-normalisable WhatsApp number without a POST', async () => {
    const capture: PostCapture = { calls: 0, payload: null }
    server.use(...childrenHandlers(capture))
    const user = userEvent.setup()
    renderChildrenPage()

    await openFilledForm(user, '8')
    const form = within(await screen.findByRole('dialog'))
    const waInput = form.getByLabelText('WhatsApp ibu')
    await user.clear(waInput)
    await user.type(waInput, '12345')
    await user.click(screen.getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Nomor WhatsApp tidak valid.')
    expect(capture.calls).toBe(0)
  })
})
