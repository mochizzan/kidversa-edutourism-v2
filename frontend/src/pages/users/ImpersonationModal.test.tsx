/**
 * Impersonation modal (design 2026-11-11 §9): confirmation is a HARD session
 * switch — the success payload replaces the whole LoginData (so `kv-school`
 * moves to the target's school) and navigation lands on the role's home.
 * A failure keeps the dialog open with the mapped `common:errors.<CODE>` copy.
 */
import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import ImpersonationModal from './ImpersonationModal'
import type { UserRow } from './UserModal'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, makeSchool, makeUser, ok } from '../../test/handlers'
import { expectNotification } from '../../test/toast'

const ADMIN_SESSION = () =>
  makeLoginData({
    user: makeUser({ role: 'ADMIN' }),
    // Deliberately NOT the target's school: the header swap must be observable.
    school: makeSchool({ id: 5, school_name: 'Pusat' }),
  })

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

/** The modal mounted at /users, plus the two navigation destinations. */
function renderModal(onClose: () => void) {
  return renderApp(null, {
    route: '/users',
    session: ADMIN_SESSION(),
    routes: [
      {
        path: '/users',
        element: <ImpersonationModal open target={TARGET} onClose={onClose} />,
      },
      { path: '/sessions', element: <div>HALAMAN SESI</div> },
      { path: '/', element: <div>BERANDA</div> },
    ],
  })
}

describe('impersonation modal', () => {
  it('adopts the target session and lands on the facilitator route', async () => {
    server.use(
      http.post('/api/users/2/impersonate', () =>
        ok(
          makeLoginData({
            user: makeUser({ id: 2, name: 'Siti Aminah', role: 'FASILITATOR', school_id: 1 }),
            impersonation: { actor_id: 1, actor_name: 'Admin Bandung', actor_role: 'ADMIN' },
          }),
        ),
      ),
    )
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderModal(onClose)

    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText(
        'Anda akan keluar dari akun Anda dan masuk sebagai Siti Aminah. Anda harus masuk lagi untuk kembali.',
      ),
    ).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Masuk sebagai Siti Aminah' }))

    expect(await screen.findByText('HALAMAN SESI')).toBeInTheDocument()
    expect(localStorage.getItem('kv-school')).toBe('1') // target school replaced 5
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
  })

  it('keeps the dialog open and maps FORBIDDEN_ROLE', async () => {
    server.use(http.post('/api/users/2/impersonate', () => fail('FORBIDDEN_ROLE')))
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderModal(onClose)

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Masuk sebagai Siti Aminah' }))

    await expectNotification('Anda tidak punya akses untuk tindakan ini.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.queryByText('HALAMAN SESI')).toBeNull()
    expect(onClose).not.toHaveBeenCalled()
  })
})
