/**
 * Administrative password reset modal (design 2026-11-11 §10.6): generate →
 * show once (read-only) → apply, and the manual tab that stays disabled until
 * the shared client policy passes and both fields match. Failures map the
 * envelope code to `common:errors.<CODE>` and keep the dialog open.
 */
import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import ResetPasswordModal from './ResetPasswordModal'
import type { UserRow } from './UserModal'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, makeUser, ok } from '../../test/handlers'
import { expectNotification } from '../../test/toast'

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })

const TARGET: UserRow = {
  id: 2,
  name: 'Siti Aminah',
  email: 'siti@kidversa.id',
  role: 'FASILITATOR',
  decision: 'APPROVED',
  is_active: true,
  school_id: 1,
  wa_number: '081234567890',
  school_name: 'Bandung',
}

function renderModal(handlers: { onClose?: () => void; onDone?: () => void } = {}) {
  return renderApp(
    <ResetPasswordModal
      open
      target={TARGET}
      onClose={handlers.onClose ?? vi.fn()}
      onDone={handlers.onDone ?? vi.fn()}
    />,
    { session: ADMIN_SESSION() },
  )
}

/** Open the manual tab and fill both fields. */
async function fillManual(
  user: ReturnType<typeof userEvent.setup>,
  dialog: HTMLElement,
  password: string,
  confirmation = password,
): Promise<void> {
  await user.click(within(dialog).getByRole('tab', { name: 'Manual' }))
  await user.type(within(dialog).getByLabelText('Kata sandi baru'), password)
  await user.type(within(dialog).getByLabelText('Ulangi kata sandi baru'), confirmation)
}

describe('password reset modal', () => {
  it('generates a read-only password and applies exactly that value', async () => {
    let body: { password?: string } | null = null
    server.use(
      http.post('/api/users/2/password/generate', () => ok({ password: 'K7mQp2xR9tVw4nHs' })),
      http.post('/api/users/2/password', async ({ request }) => {
        body = (await request.json()) as { password?: string }
        return ok({ reset: true })
      }),
    )
    const onDone = vi.fn()
    const user = userEvent.setup()
    renderModal({ onDone })

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Buat kata sandi' }))

    const generated = await screen.findByDisplayValue('K7mQp2xR9tVw4nHs')
    expect(generated).toHaveAttribute('readonly')
    expect(
      screen.getByText('Kata sandi ini hanya ditampilkan sekali. Salin sebelum mengonfirmasi.'),
    ).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Gunakan kata sandi ini' }))

    await waitFor(() => expect(body?.password).toBe('K7mQp2xR9tVw4nHs'))
    await expectNotification('Kata sandi berhasil diatur ulang.')
    await waitFor(() => expect(onDone).toHaveBeenCalledTimes(1))
  })

  it('disables the manual submit until the policy passes and both fields match', async () => {
    const user = userEvent.setup()
    renderModal()

    const dialog = await screen.findByRole('dialog')
    await fillManual(user, dialog, 'short')

    const submit = within(dialog).getByRole('button', { name: 'Simpan' })
    expect(submit).toBeDisabled()

    const password = within(dialog).getByLabelText('Kata sandi baru')
    await user.clear(password)
    await user.type(password, 'ManualPass123')
    expect(submit).toBeDisabled() // confirmation still says "short"

    const confirmation = within(dialog).getByLabelText('Ulangi kata sandi baru')
    await user.clear(confirmation)
    await user.type(confirmation, 'ManualPass123')
    expect(submit).toBeEnabled()
  })

  it('maps a VALIDATION failure to a notification and keeps the dialog open', async () => {
    server.use(http.post('/api/users/2/password', () => fail('VALIDATION')))
    const onDone = vi.fn()
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderModal({ onDone, onClose })

    const dialog = await screen.findByRole('dialog')
    await fillManual(user, dialog, 'ManualPass123')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await expectNotification('Data tidak valid. Periksa kembali isian Anda.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(onDone).not.toHaveBeenCalled()
    expect(onClose).not.toHaveBeenCalled()
  })
})
