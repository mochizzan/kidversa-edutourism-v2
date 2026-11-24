/**
 * Session wipe dialog (F-08): the pinned `detail.scope` counts line, the
 * detail-derived fallback when scope is absent, the typed-text gate, the
 * DELETE contract, and the HAS_OPEN_GROUPS envelope rendered translated.
 */
import { describe, expect, it, vi } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import DeleteSessionDialog from './DeleteSessionDialog'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok } from '../../test/handlers'
import { detailFixture } from '../../test/helpers.cov-session'

function renderDialog(options: { withScope: boolean; onCancel?: () => void }) {
  const onDeleted = vi.fn()
  const onCancel = options.onCancel ?? vi.fn()
  renderApp(
    <DeleteSessionDialog
      detail={detailFixture({ withScope: options.withScope })}
      open
      onCancel={onCancel}
      onDeleted={onDeleted}
    />,
  )
  return { onDeleted, onCancel }
}

describe('delete session dialog', () => {
  it('renders the pinned scope counts and the detail-derived line', () => {
    const { onDeleted } = renderDialog({ withScope: true })

    screen.getByText(
      '2 rapor terkirim, 4 foto, dan 5 link aktif akan ikut mati. Tindakan ini tidak dapat dibatalkan.',
    )
    screen.getByText('2 kelompok, 5 anggota, dan 3 topik penilaian ikut terhapus.')
    screen.getByText('Ketik Kelas Batik untuk mengonfirmasi.')
    expect(onDeleted).not.toHaveBeenCalled()
  })

  it('falls back to detail-derived counts while scope is absent', () => {
    renderDialog({ withScope: false })

    // Derived from the detail itself: 1 group, its 2 members, 1 topic.
    screen.getByText('1 kelompok, 2 anggota, dan 1 topik penilaian ikut terhapus.')
    expect(screen.queryByText(/rapor terkirim/)).toBeNull()
  })

  it('gates the DELETE behind the exact typed session name', async () => {
    let deleted = 0
    server.use(http.delete('/api/sessions/1', () => {
      deleted += 1
      return ok({})
    }))
    const { onDeleted } = renderDialog({ withScope: true })
    const user = userEvent.setup()

    const confirm = screen.getByRole('button', { name: 'Hapus sesi' })
    expect(confirm).toBeDisabled()

    const typed = screen.getByPlaceholderText('Kelas Batik')
    await user.type(typed, 'Kelas Bat')
    expect(confirm).toBeDisabled()
    await user.type(typed, 'ik')
    expect(confirm).toBeEnabled()

    await user.click(confirm)
    await vi.waitFor(() => expect(onDeleted).toHaveBeenCalledTimes(1))
    expect(deleted).toBe(1)
  })

  it('renders the HAS_OPEN_GROUPS envelope translated, inline', async () => {
    server.use(
      http.delete('/api/sessions/1', () =>
        HttpResponse.json({
          error: {
            code: 'HAS_OPEN_GROUPS',
            message: 'User masih memimpin kelompok aktif. Alihkan kelompok dulu.',
            groups: [{ gid: 5, session_id: 1, session_name: 'Kelas Batik' }],
          },
        }),
      ),
    )
    const { onDeleted } = renderDialog({ withScope: true })
    const user = userEvent.setup()

    await user.type(screen.getByPlaceholderText('Kelas Batik'), 'Kelas Batik')
    await user.click(screen.getByRole('button', { name: 'Hapus sesi' }))

    await screen.findByText('User masih memimpin kelompok aktif. Alihkan kelompok dulu.')
    expect(onDeleted).not.toHaveBeenCalled()
  })
})
