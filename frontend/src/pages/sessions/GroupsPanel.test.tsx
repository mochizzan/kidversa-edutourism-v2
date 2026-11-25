/**
 * Groups panel (F-08 / FL-08): full render of an own group (owner, consent
 * tallies, member rows with assess/gallery links), FAS reduced foreign groups
 * and the Finished read-only state, group create/rename contracts incl. the
 * VALIDATION owner notice, member remove/add with error passthrough, and the
 * typed group-wipe dialog whose counts derive from the progress + reports
 * matrices.
 */
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import GroupsPanel from './GroupsPanel'
import { emitSignal } from '../../lib/stream'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, ok, paged } from '../../test/handlers'
import {
  chooseOption,
  detailFixture,
  openDropdown,
  sessionChildRow,
} from '../../test/helpers.cov-session'
import type { SessionDetail } from '../../lib/session-contracts'

function renderPanel(
  detail: SessionDetail,
  canManage = true,
  onChanged = vi.fn(),
  onMembersAdded = vi.fn(),
) {
  renderApp(
    <GroupsPanel
      detail={detail}
      canManage={canManage}
      onChanged={onChanged}
      onMembersAdded={onMembersAdded}
    />,
    { route: '/sessions/1', session: makeLoginData({ user: makeUser() }) },
  )
  return { onChanged, onMembersAdded }
}

function childrenHandler() {
  return http.get('/api/children', () =>
    paged([sessionChildRow(4, 'Citra Ayu')], { page: 1, per_page: 1000, total: 1 }),
  )
}

function usersHandler() {
  return http.get('/api/users', () =>
    paged(
      [
        {
          id: 7,
          name: 'Dewi Lestari',
          email: 'dewi@kidversa.id',
          role: 'FASILITATOR',
          decision: 'APPROVED',
          is_active: true,
          school_id: 1,
        },
      ],
      { page: 1, per_page: 1000, total: 1 },
    ),
  )
}

describe('groups panel render', () => {
  it('renders an own group fully: owner, counts, consent tallies, member rows', () => {
    server.use(childrenHandler())
    renderPanel(detailFixture())

    const card = screen.getByText('Kelompok Merah').closest('.ant-card') as HTMLElement
    expect(within(card).getByText(/Fasilitator: Dewi Lestari/)).toBeInTheDocument()
    // Consent tallies (PENDING 1, SETUJU 1) with labels, never colour alone.
    expect(within(card).getAllByText('Menunggu').length).toBeGreaterThan(0)
    expect(within(card).getAllByText('Disetujui').length).toBeGreaterThan(0)
    expect(within(card).getAllByText('×1')).toHaveLength(2)
    // Member rows: assess link (first stid) + gallery link per member.
    const assessLinks = within(card).getAllByRole('link', { name: 'Nilai' })
    expect(assessLinks[0]).toHaveAttribute('href', '/assessment/1/2/3')
    expect(assessLinks[1]).toHaveAttribute('href', '/assessment/1/3/3')
    const galleryLinks = within(card).getAllByRole('link', { name: 'Galeri' })
    expect(galleryLinks[0]).toHaveAttribute('href', '/sessions/1/gallery/children/2')
    expect(within(card).getByText('8 · SD Cendekia')).toBeInTheDocument()
  })

  it('flags a group without an active facilitator and an empty group body', () => {
    server.use(childrenHandler())
    renderPanel(
      detailFixture({
        groups: [
          {
            gid: 6,
            name: 'Kelompok Biru',
            own: true,
            owner: null,
            owner_missing: true,
            member_count: 0,
            consent: {},
            members: [],
          },
        ],
      }),
    )

    expect(screen.getByText(/Kelompok tanpa fasilitator aktif\./)).toBeInTheDocument()
    expect(screen.getByText('Belum ada anggota.')).toBeInTheDocument()
  })

  it('shows the no-groups notice', () => {
    server.use(childrenHandler())
    renderPanel(detailFixture({ groups: [] }))

    expect(
      screen.getByText('Belum ada kelompok. Tambahkan kelompok sebelum sesi dibuka.'),
    ).toBeInTheDocument()
  })

  it('renders a FAS reduced foreign group without owner/consent/members/actions', () => {
    server.use(childrenHandler())
    renderPanel(
      detailFixture({
        groups: [{ gid: 9, name: 'Kelompok Luar', own: false, member_count: 4, status: 'DRAFT' }],
      }),
      false,
    )

    const card = screen.getByText('Kelompok Luar').closest('.ant-card') as HTMLElement
    expect(within(card).getByText('Anggota: 4')).toBeInTheDocument()
    expect(within(card).queryByText('Fasilitator: Dewi Lestari')).toBeNull()
    expect(within(card).queryByRole('button', { name: /Ubah kelompok/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Tambah kelompok/ })).toBeNull()
  })

  it('Finished sessions hide every mutate action (server would answer SESSION_LOCKED)', () => {
    server.use(childrenHandler())
    renderPanel(detailFixture({ status: 'FINISHED' }))

    expect(screen.queryByRole('button', { name: /Tambah kelompok/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Ubah kelompok/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Hapus kelompok/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Tambah anggota/ })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Keluarkan dari sesi' })).toBeNull()
    // Read-only data still renders.
    expect(screen.getByText('Kelompok Merah')).toBeInTheDocument()
  })
})

describe('groups panel mutations', () => {
  it('creates a group with the contract payload', async () => {
    let created: unknown = null
    server.use(
      childrenHandler(),
      usersHandler(),
      http.post('/api/sessions/1/groups', async ({ request }) => {
        created = await request.json()
        return ok({ gid: 10 })
      }),
    )
    const { onChanged } = renderPanel(detailFixture())
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    const modal = await screen.findByRole('dialog')
    // Empty name is blocked client-side.
    await user.click(within(modal).getByRole('button', { name: 'Simpan' }))
    await within(modal).findByText('Nama kelompok wajib diisi.')

    await user.type(within(modal).getByPlaceholderText('mis. Kelompok Merah'), 'Kelompok Hijau')
    const picker = within(modal).getByRole('combobox')
    await openDropdown(picker)
    await chooseOption('Dewi Lestari · dewi@kidversa.id')
    await user.keyboard('{Escape}')
    await user.click(within(modal).getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Kelompok ditambahkan.')
    expect(created).toEqual({ name: 'Kelompok Hijau', facilitator_id: 7 })
    expect(onChanged).toHaveBeenCalled()
  })

  it('shows the owner-school VALIDATION notice inline', async () => {
    server.use(
      childrenHandler(),
      usersHandler(),
      http.post('/api/sessions/1/groups', () =>
        HttpResponse.json({ error: { code: 'VALIDATION', message: 'bad owner' } }),
      ),
    )
    renderPanel(detailFixture())
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    const modal = await screen.findByRole('dialog')
    await user.type(within(modal).getByPlaceholderText('mis. Kelompok Merah'), 'Kelompok Hijau')
    await user.click(within(modal).getByRole('button', { name: 'Simpan' }))

    await within(modal).findByText('Fasilitator harus aktif di sekolah ini.')
  })

  it('renames a group via PATCH', async () => {
    let patched: unknown = null
    server.use(
      childrenHandler(),
      usersHandler(),
      http.patch('/api/sessions/1/groups/5', async ({ request }) => {
        patched = await request.json()
        return ok({})
      }),
    )
    const { onChanged } = renderPanel(detailFixture())
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Ubah kelompok/ }))
    const modal = await screen.findByRole('dialog')
    const nameInput = within(modal).getByDisplayValue('Kelompok Merah')
    await user.clear(nameInput)
    await user.type(nameInput, 'Kelompok Baru')
    await user.click(within(modal).getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Kelompok diperbarui.')
    expect(patched).toMatchObject({ name: 'Kelompok Baru' })
    expect(onChanged).toHaveBeenCalled()
  })

  it('removes a member after the confirm and surfaces the server message on failure', async () => {
    let deleted = 0
    let failNext = false
    server.use(
      childrenHandler(),
      http.delete('/api/sessions/1/groups/5/members/2', () => {
        deleted += 1
        if (failNext) {
          return HttpResponse.json({ error: { code: 'VALIDATION', message: 'cannot remove' } })
        }
        return ok({})
      }),
    )
    const { onChanged } = renderPanel(detailFixture())
    const user = userEvent.setup()

    failNext = true
    const removeButtons = screen.getAllByRole('button', { name: 'Keluarkan dari sesi' })
    await user.click(removeButtons[0])
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText(
      'Penilaian, foto, narasi, misi, dan persetujuan anak ini di sesi ini ikut terhapus.',
    )
    // antd keeps the loading icon in the OK button for a beat after a failed
    // run — wait for the settled, enabled "Keluarkan dari sesi" before re-confirming.
    const confirmInDialog = async (): Promise<HTMLElement> => {
      let button: HTMLElement | undefined
      await waitFor(() => {
        button = within(dialog).getByRole('button', { name: 'Keluarkan dari sesi' })
        expect(button).toBeEnabled()
      })
      return button as HTMLElement
    }
    await user.click(await confirmInDialog())
    // Inline passthrough of the server message.
    await within(dialog).findByText('cannot remove')

    failNext = false
    await user.click(await confirmInDialog())
    await screen.findByText('Anggota dikeluarkan.')
    expect(deleted).toBe(2)
    expect(onChanged).toHaveBeenCalled()
  })

  it('adds members with the contract payload and enqueues the copy flow', async () => {
    let posted: unknown = null
    server.use(
      childrenHandler(),
      usersHandler(),
      http.post('/api/sessions/1/groups/5/members', async ({ request }) => {
        posted = await request.json()
        return ok({})
      }),
    )
    const { onChanged, onMembersAdded } = renderPanel(detailFixture())
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Tambah anggota/ }))
    const modal = await screen.findByRole('dialog')
    const picker = within(modal).getByRole('combobox')
    await openDropdown(picker)
    // The existing member is excluded (disabled); only Citra is selectable.
    await chooseOption('Citra Ayu · SD Cendekia')
    await user.keyboard('{Escape}')
    await user.click(within(modal).getByRole('button', { name: 'Buat' }))

    await screen.findByText('Anggota ditambahkan.')
    expect(posted).toEqual({ child_ids: [4] })
    expect(onChanged).toHaveBeenCalled()
    expect(onMembersAdded).toHaveBeenCalledWith([{ cid: 4, name: 'Citra Ayu' }])
  })

  it('passes the CHILD_DUPLICATE server message through inline', async () => {
    server.use(
      childrenHandler(),
      usersHandler(),
      http.post('/api/sessions/1/groups/5/members', () =>
        HttpResponse.json({
          error: { code: 'CHILD_DUPLICATE', message: 'Anak sudah berada di kelompok lain pada sesi ini.' },
        }),
      ),
    )
    renderPanel(detailFixture())
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Tambah anggota/ }))
    const modal = await screen.findByRole('dialog')
    const picker = within(modal).getByRole('combobox')
    await openDropdown(picker)
    await chooseOption('Citra Ayu · SD Cendekia')
    await user.keyboard('{Escape}')
    await user.click(within(modal).getByRole('button', { name: 'Buat' }))

    await within(modal).findByText('Anak sudah berada di kelompok lain pada sesi ini.')
  })
})

describe('groups panel typed group wipe', () => {
  it('derives the wipe counts from the progress + reports matrices behind the typed gate', async () => {
    let deleted = 0
    server.use(
      childrenHandler(),
      http.get('/api/sessions/1/progress', () =>
        ok({
          children: [
            { child_id: 2, name: 'Aisyah Putri', gid: 5, topics: [{ stid: 3, topic: 'Batik', starred: 3, total: 2, complete: true }] },
            { child_id: 3, name: 'Budi Santoso', gid: 5, topics: [{ stid: 3, topic: 'Batik', starred: 2, total: 2, complete: true }] },
            { child_id: 8, name: 'Lain', gid: 9, topics: [{ stid: 3, topic: 'Batik', starred: 9, total: 2, complete: true }] },
          ],
        }),
      ),
      http.get('/api/sessions/1/reports', () =>
        ok([
          { child_id: 2, session_topic_id: 3, report_url: '/view/report?token=a', gallery_url: null },
          { child_id: 3, session_topic_id: 3, report_url: null, gallery_url: null },
          { child_id: 8, session_topic_id: 3, report_url: '/view/report?token=b', gallery_url: null },
        ]),
      ),
      http.delete('/api/sessions/1/groups/5', () => {
        deleted += 1
        return ok({})
      }),
    )
    const { onChanged } = renderPanel(detailFixture())
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Hapus kelompok/ }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText(
      '2 anggota dan seluruh data sesi mereka ikut terhapus.',
    )
    // Starred sum for gid 5 only (3+2), consent links = tallies 1+1,
    // report links for MEMBER children only (child 2 → 1, child 8 ignored).
    await within(dialog).findByText(
      '5 penilaian, 2 link persetujuan, dan 1 link rapor/galeri milik anggota kelompok ini ikut mati.',
    )

    const confirm = within(dialog).getByRole('button', { name: 'Hapus kelompok' })
    expect(confirm).toBeDisabled()
    await user.type(within(dialog).getByPlaceholderText('Kelompok Merah'), 'Kelompok Merah')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await screen.findByText('Kelompok dihapus.')
    expect(deleted).toBe(1)
    expect(onChanged).toHaveBeenCalled()
  })

  it('stays usable when both count reads fail (best-effort scope)', async () => {
    server.use(
      childrenHandler(),
      http.get('/api/sessions/1/progress', () =>
        HttpResponse.json({ error: { code: 'INTERNAL', message: 'boom' } }),
      ),
      http.get('/api/sessions/1/reports', () =>
        HttpResponse.json({ error: { code: 'INTERNAL', message: 'boom' } }),
      ),
      http.delete('/api/sessions/1/groups/5', () => ok({})),
    )
    renderPanel(detailFixture())
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Hapus kelompok/ }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText(
      '2 anggota dan seluruh data sesi mereka ikut terhapus.',
    )
    // No derived-scope alert without the counts, but the dialog still works.
    expect(
      within(dialog).queryByText(/penilaian, .* link persetujuan/),
    ).toBeNull()
    expect(within(dialog).getByRole('button', { name: 'Hapus kelompok' })).toBeInTheDocument()
  })

  it('a DELETE failure renders the translated code inline and keeps the dialog open', async () => {
    server.use(
      childrenHandler(),
      http.get('/api/sessions/1/progress', () => ok({ children: [] })),
      http.get('/api/sessions/1/reports', () => ok([])),
      http.delete('/api/sessions/1/groups/5', () =>
        HttpResponse.json({ error: { code: 'STATE_CONFLICT', message: 'STATE_CONFLICT' } }),
      ),
    )
    renderPanel(detailFixture())
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: /Hapus kelompok/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByPlaceholderText('Kelompok Merah'), 'Kelompok Merah')
    await user.click(within(dialog).getByRole('button', { name: 'Hapus kelompok' }))

    // message === code → the TRANSLATED fallback renders, dialog stays open.
    await within(dialog).findByText('Data berubah di tempat lain. Muat ulang halaman.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

describe('groups panel member remove error fallback', () => {
  it('an error whose message equals its code renders the translated code inline', async () => {
    server.use(
      childrenHandler(),
      http.delete('/api/sessions/1/groups/5/members/2', () =>
        HttpResponse.json({ error: { code: 'STATE_CONFLICT', message: 'STATE_CONFLICT' } }),
      ),
    )
    renderPanel(detailFixture())
    const user = userEvent.setup()

    await user.click(screen.getAllByRole('button', { name: 'Keluarkan dari sesi' })[0])
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Keluarkan dari sesi' }))

    await within(dialog).findByText('Data berubah di tempat lain. Muat ulang halaman.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

describe('groups panel children SSE refetch', () => {
  it('child.changed refetches the children list; the modal options reflect it', async () => {
    const rowsRef = { rows: [sessionChildRow(4, 'Citra Ayu')] }
    let childCalls = 0
    server.use(
      http.get('/api/children', () => {
        childCalls += 1
        return paged(rowsRef.rows, { page: 1, per_page: 1000, total: rowsRef.rows.length })
      }),
    )
    renderPanel(detailFixture())

    await waitFor(() => expect(childCalls).toBe(1))

    // A child created elsewhere must reach the member picker without a reload.
    rowsRef.rows = [sessionChildRow(4, 'Citra Ayu'), sessionChildRow(9, 'Dara Ayu')]
    act(() => emitSignal('child.changed', { rev: 1 }))
    await waitFor(() => expect(childCalls).toBe(2))

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Tambah anggota/ }))
    const modal = await screen.findByRole('dialog')
    const picker = within(modal).getByRole('combobox')
    await openDropdown(picker)
    // The refreshed row is present in the dropdown (pre-refresh it did not
    // exist); picking it unblocks the submit (disabled while selection=[]).
    await chooseOption('Dara Ayu · SD Cendekia')
    await user.keyboard('{Escape}')
    expect(within(modal).getByRole('button', { name: 'Buat' })).toBeEnabled()
  })
})
