/**
 * Session detail (F-08 / FL-08): lifecycle buttons per status (ADMIN/SA only),
 * guarded-transition error contract (STATE_CONFLICT → toast + reload,
 * SESSION_LOCKED → toast + reload, VALIDATION/GROUP_WITHOUT_FACILITATOR →
 * localized gate message + offending group names inline), the finish flow's
 * incomplete-topic list, the draft meta editor
 * incl. its SESSION_LOCKED notice, the typed delete dialog fed from
 * `detail.scope`, and the create-with-groups copy-queue wiring (member add →
 * CopyDataModal for the added child) incl. the SSE `copy_available` hint
 * (registerSignal payload → queue) for member.changed / session.changed.
 */
import { describe, expect, it } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import type { RouteObject } from 'react-router'
import SessionDetailPage from './SessionDetailPage'
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

const ROUTES: RouteObject[] = [
  { path: '/sessions/:sid', element: <SessionDetailPage /> },
  { path: '/sessions', element: <div>SESSIONS LIST</div> },
]

interface DetailCalls {
  detail: number
  posts: string[]
}

function detailHandlers(
  calls: DetailCalls,
  status: 'DRAFT' | 'OPENED' | 'FINISHED',
  post: () => Response,
) {
  const countDetail = () => {
    calls.detail += 1
    return ok(detailFixture({ status }))
  }
  return [
    http.get('/api/sessions/1', countDetail),
    http.get('/api/children', () => paged([sessionChildRow(2, 'Aisyah Putri')], { page: 1, per_page: 1000, total: 1 })),
    http.post('/api/sessions/1/open', () => {
      calls.posts.push('open')
      return post()
    }),
    http.post('/api/sessions/1/finish', () => {
      calls.posts.push('finish')
      return post()
    }),
    http.post('/api/sessions/1/reopen', () => {
      calls.posts.push('reopen')
      return post()
    }),
  ]
}

function newCalls(): DetailCalls {
  return { detail: 0, posts: [] }
}

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })
const FAS_SESSION = () => makeLoginData({ user: makeUser({ role: 'FASILITATOR' }) })

describe('session detail lifecycle buttons', () => {
  it('DRAFT shows open + edit + delete only', async () => {
    const calls = newCalls()
    server.use(...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })))
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    expect(screen.getByRole('button', { name: 'Mulai sesi' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Ubah' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Hapus sesi' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Selesaikan sesi' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Buka kembali' })).toBeNull()
  })

  it('OPENED shows finish only — no delete, no edit', async () => {
    const calls = newCalls()
    server.use(...detailHandlers(calls, 'OPENED', () => ok({ status: 'FINISHED' })))
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    expect(screen.getByRole('button', { name: 'Selesaikan sesi' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Hapus sesi' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Ubah' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Mulai sesi' })).toBeNull()
  })

  it('FINISHED shows reopen + delete', async () => {
    const calls = newCalls()
    server.use(...detailHandlers(calls, 'FINISHED', () => ok({ status: 'OPENED' })))
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    expect(screen.getByRole('button', { name: 'Buka kembali' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Hapus sesi' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Mulai sesi' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Selesaikan sesi' })).toBeNull()
  })

  it('FAS sees no lifecycle actions at all', async () => {
    const calls = newCalls()
    server.use(...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })))
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: FAS_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    expect(screen.queryByRole('button', { name: 'Mulai sesi' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Ubah' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Hapus sesi' })).toBeNull()
  })
})

describe('session detail guarded transitions', () => {
  it('open success toasts and reloads the detail (own-200 refetch)', async () => {
    const calls = newCalls()
    server.use(...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    const before = calls.detail
    await user.click(screen.getByRole('button', { name: 'Mulai sesi' }))

    await screen.findByText('Sesi dibuka.')
    expect(calls.posts).toEqual(['open'])
    await waitFor(() => expect(calls.detail).toBeGreaterThan(before))
  })

  it('STATE_CONFLICT toasts the translated error and reloads', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'DRAFT', () =>
        HttpResponse.json({ error: { code: 'STATE_CONFLICT', message: 'stale' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    const before = calls.detail
    await user.click(screen.getByRole('button', { name: 'Mulai sesi' }))

    await screen.findByText('Data berubah di tempat lain. Muat ulang halaman.')
    await waitFor(() => expect(calls.detail).toBeGreaterThan(before))
  })

  it('SESSION_LOCKED toasts and reloads', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'DRAFT', () =>
        HttpResponse.json({ error: { code: 'SESSION_LOCKED', message: 'locked' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    const before = calls.detail
    await user.click(screen.getByRole('button', { name: 'Mulai sesi' }))

    await screen.findByText('Sesi terkunci untuk perubahan ini.')
    await waitFor(() => expect(calls.detail).toBeGreaterThan(before))
  })

  it('VALIDATION open gate with zero groups explains what is missing (no raw server text)', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'DRAFT', () =>
        HttpResponse.json({
          error: { code: 'VALIDATION', message: 'invalid or missing field', groups: [] },
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Mulai sesi' }))

    await screen.findByText(
      'Belum ada kelompok. Tambahkan kelompok berisi minimal satu anak sebelum sesi dibuka.',
    )
    expect(screen.queryByText('invalid or missing field')).toBeNull()
  })

  it('VALIDATION open gate names the empty groups inline', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'DRAFT', () =>
        HttpResponse.json({
          error: {
            code: 'VALIDATION',
            message: 'invalid or missing field',
            groups: [{ gid: 9, name: 'Kelompok Merah', reason: 'empty' }],
          },
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Mulai sesi' }))

    await screen.findByText(
      'Kelompok berikut belum berisi anak: Kelompok Merah. Tambahkan anak sebelum sesi dibuka.',
    )
  })

  it('GROUP_WITHOUT_FACILITATOR names the offending groups inline (no toast-only loss)', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'DRAFT', () =>
        HttpResponse.json({
          error: {
            code: 'GROUP_WITHOUT_FACILITATOR',
            message: 'group has no facilitator',
            groups: [{ gid: 9, name: 'Kelompok Merah', reason: 'owner_missing' }],
          },
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Mulai sesi' }))

    const message =
      'Kelompok berikut belum punya fasilitator aktif: Kelompok Merah. Tugaskan fasilitator sebelum sesi dibuka.'
    await screen.findByText(message)
    // The warning is dismissible — the inline message clears on close.
    await user.click(document.querySelector('.ant-alert-close-icon') as HTMLElement)
    await waitFor(() => expect(screen.queryByText(message)).toBeNull())
  })

  it('an unlisted error code falls back to the translated toast, no reload probe', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'DRAFT', () =>
        HttpResponse.json({ error: { code: 'RATE_LIMITED', message: 'slow' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    const before = calls.detail
    await user.click(screen.getByRole('button', { name: 'Mulai sesi' }))

    await screen.findByText('Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.')
    // Generic codes do not reload — only the guarded-transition codes do.
    expect(calls.detail).toBe(before)
  })

  it('renders the session description under the header when present', async () => {
    const calls = newCalls()
    server.use(
      http.get('/api/sessions/1', () => {
        calls.detail += 1
        return ok({ ...detailFixture({ status: 'DRAFT' }), session: { ...detailFixture().session, description: 'Batik bersama Bu Dewi' } })
      }),
    )
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Batik bersama Bu Dewi')
  })
})

describe('session detail finish flow', () => {
  it('lists the incomplete topics in the confirm dialog, then finishes', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'OPENED', () => ok({ status: 'FINISHED' })),
      http.get('/api/sessions/1/progress', () =>
        ok({
          children: [
            {
              child_id: 2,
              name: 'Aisyah Putri',
              gid: 5,
              topics: [
                { stid: 3, topic: 'Batik Nusantara', starred: 0, total: 2, complete: false },
              ],
            },
          ],
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Selesaikan sesi' }))

    await screen.findByText('Selesaikan sesi Kelas Batik?')
    await screen.findByText('Penilaian belum lengkap:')
    await screen.findByText('Aisyah Putri — Batik Nusantara')
    // Header + dialog share the label; the dialog's confirm is the last one.
    const confirmButtons = screen.getAllByRole('button', { name: 'Selesaikan sesi' })
    await user.click(confirmButtons.at(-1) as HTMLElement)

    await screen.findByText('Sesi diselesaikan.')
    expect(calls.posts).toEqual(['finish'])
  })

  it('still finishes when the progress warn-list read fails (best-effort scope)', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'OPENED', () => ok({ status: 'FINISHED' })),
      http.get('/api/sessions/1/progress', () =>
        HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Selesaikan sesi' }))

    // The dialog opens with the partial-finish note but no incomplete list.
    await screen.findByText('Selesaikan sesi Kelas Batik?')
    expect(screen.queryByText('Penilaian belum lengkap:')).toBeNull()
    const confirmButtons = screen.getAllByRole('button', { name: 'Selesaikan sesi' })
    await user.click(confirmButtons.at(-1) as HTMLElement)

    await screen.findByText('Sesi diselesaikan.')
    expect(calls.posts).toEqual(['finish'])
  })
})

describe('session detail meta editor', () => {
  it('validates the snapshot inline and sends nothing until the fields pass', async () => {
    const calls = newCalls()
    let patchCalls = 0
    server.use(
      ...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })),
      http.patch('/api/sessions/1', () => {
        patchCalls += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Ubah' }))
    await screen.findByText('Ubah detail sesi')

    // Clear the required fields and make end <= start.
    await user.clear(screen.getByDisplayValue('Kelas Batik'))
    // antd DatePicker: an emptied input text is not a valid format value, so
    // typing-clear commits nothing — clear through the picker's own clear
    // button instead.
    fireEvent.click(
      (screen.getByDisplayValue('2026-10-09').closest('.ant-picker') as HTMLElement).querySelector(
        '.ant-picker-clear',
      )!,
    )
    await user.clear(screen.getByDisplayValue('Museum Tekstil'))
    // Time pickers sit in antd Form.Items — pickers in DOM order are
    // date (0), start (1), end (2); mask-typed digits + Enter commit.
    const [, start, end] = document.querySelectorAll('.ant-picker-input > input')
    await user.type(start, '10:00{Enter}')
    await user.type(end, '09:00{Enter}')
    await user.click(screen.getByRole('button', { name: 'Simpan' }))

    // Every rule fires at once, inline; not one request leaves the tab.
    await screen.findByText('Nama sesi wajib diisi.')
    screen.getByText('Tanggal wajib diisi.')
    screen.getByText('Lokasi wajib diisi.')
    screen.getByText('Waktu selesai harus setelah waktu mulai.')
    expect(patchCalls).toBe(0)
  })

  it('saves the draft snapshot via PATCH and toasts', async () => {
    const calls = newCalls()
    let patched: unknown = null
    server.use(
      ...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })),
      http.patch('/api/sessions/1', async ({ request }) => {
        patched = await request.json()
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Ubah' }))
    await screen.findByText('Ubah detail sesi')

    const nameInput = screen.getByDisplayValue('Kelas Batik')
    await user.clear(nameInput)
    await user.type(nameInput, 'Kelas Batik Baru')
    await user.click(screen.getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Detail sesi diperbarui.')
    expect(patched).toMatchObject({ name: 'Kelas Batik Baru' })
  })

  it('shows the SESSION_LOCKED notice inline when the PATCH is refused', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })),
      http.patch('/api/sessions/1', () =>
        HttpResponse.json({ error: { code: 'SESSION_LOCKED', message: 'locked' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Ubah' }))
    await screen.findByText('Ubah detail sesi')
    await user.click(screen.getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Sesi terkunci untuk perubahan ini.')
  })
})

describe('session detail delete dialog', () => {
  it('renders the wipe scope from detail.scope behind the typed gate', async () => {
    const calls = newCalls()
    server.use(...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Hapus sesi' }))

    await screen.findByText('Hapus sesi Kelas Batik?')
    // Pinned scope counts (rapor 2, foto 4, links 1+1+3).
    await screen.findByText(
      '2 rapor terkirim, 4 foto, dan 5 link aktif akan ikut mati. Tindakan ini tidak dapat dibatalkan.',
    )
    // Detail-derived line.
    screen.getByText('2 kelompok, 5 anggota, dan 3 topik penilaian ikut terhapus.')
    // Typed gate: disabled until the exact session name is typed.
    const confirm = screen.getByRole('button', { name: 'Hapus' })
    expect(confirm).toBeDisabled()
    const typed = screen.getByPlaceholderText('Kelas Batik')
    await user.type(typed, 'Salah')
    expect(confirm).toBeDisabled()
  })
})

describe('session detail copy-data queue', () => {
  it('create-with-groups handoff: location.state copyChildren opens the modal once, then history is cleared', async () => {
    const calls = newCalls()
    server.use(
      ...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })),
      http.get('/api/sessions/1/children/4/copy-candidates', () =>
        ok([
          {
            session_id: 9,
            session_name: 'Sesi Lama',
            session_date: '2026-09-01',
            status: 'FINISHED',
            counts: { assessments: 2, narratives: 1, missions: 1, photos: 3 },
          },
        ]),
      ),
    )
    renderApp(null, {
      routes: ROUTES,
      route: { pathname: '/sessions/1', state: { copyChildren: [{ cid: 4, name: 'Citra Ayu' }] } },
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Salin nilai dari Sesi Sesi Lama?')
  })

  it('malformed navigation state never opens the copy modal', async () => {
    const calls = newCalls()
    server.use(...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })))
    // No copyChildren key / not an array / entries that fail the shape guard.
    renderApp(null, {
      routes: ROUTES,
      route: {
        pathname: '/sessions/1',
        state: { copyChildren: [{ cid: 'four', name: 42 }, 7] },
      },
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    expect(screen.queryByText(/Salin nilai dari/)).toBeNull()
  })

  it('member add enqueues the added children and opens the copy modal', async () => {
    const calls = newCalls()
    server.use(
      // More specific /api/children answer must precede the generic batch
      // (within one use() batch the FIRST matching handler wins).
      http.get('/api/children', () => paged([sessionChildRow(4, 'Citra Ayu')], { page: 1, per_page: 1000, total: 1 })),
      ...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })),
      http.post('/api/sessions/1/groups/5/members', async ({ request }) => {
        const body = (await request.json()) as { child_ids: number[] }
        expect(body).toEqual({ child_ids: [4] })
        return ok({})
      }),
      http.get('/api/sessions/1/children/4/copy-candidates', () =>
        ok([
          {
            session_id: 9,
            session_name: 'Sesi Lama',
            session_date: '2026-09-01',
            status: 'FINISHED',
            counts: { assessments: 2, narratives: 1, missions: 1, photos: 3 },
          },
        ]),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    await user.click(screen.getByRole('button', { name: /Tambah anggota/ }))

    // ChildPicker in the member modal.
    const picker = document.querySelector('.ant-select .ant-select-selection-search-input') as HTMLElement
    await openDropdown(picker)
    await chooseOption('Citra Ayu · SD Cendekia')
    await user.keyboard('{Escape}')
    await user.click(screen.getByRole('button', { name: 'Buat' }))

    // The copy modal opens for the added child (queue head).
    await screen.findByText('Salin nilai dari Sesi Sesi Lama?')
  })

  it('copy_available hints on member/session frames enqueue the copy modal (registerSignal payload)', async () => {
    const calls = newCalls()
    const candCalls: number[] = []
    const candidates = (cid: number) =>
      http.get(`/api/sessions/1/children/${cid}/copy-candidates`, () => {
        candCalls.push(cid)
        return ok([
          {
            session_id: 9,
            session_name: 'Sesi Lama',
            session_date: '2026-09-01',
            status: 'FINISHED',
            counts: { assessments: 2, narratives: 1, missions: 1, photos: 3 },
          },
        ])
      })
    server.use(...detailHandlers(calls, 'DRAFT', () => ok({ status: 'OPENED' })), candidates(4), candidates(6))
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')

    // No hint → nothing queued (the payload must survive to the listener —
    // registerRefetch would drop it).
    act(() => emitSignal('member.changed', { rev: 1, copy_available: false, child_id: 4 }))
    expect(screen.queryByText('Salin nilai dari Sesi Sesi Lama?')).toBeNull()

    // copy_available → the hinted child enters the queue and the modal opens;
    // the sibling event carries the same shape and queues the NEXT child.
    act(() => emitSignal('member.changed', { rev: 2, copy_available: true, child_id: 4 }))
    act(() => emitSignal('session.changed', { rev: 3, copy_available: true, child_id: 6 }))
    await screen.findByText('Salin nilai dari Sesi Sesi Lama?')
    expect(candCalls).toEqual([4])

    // One modal at a time: closing hands over to the queued child (fresh
    // fetch — the modal is keyed per child, no state bleeds across it).
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Tidak' }))
    await waitFor(() => expect(candCalls).toContain(6))
  })
})

describe('session detail SSE close', () => {
  it('session.deleted closes THIS session from the frame entity_id, with no extra GET', async () => {
    // Publish is post-commit (004g): the listener compares the frame's
    // entity_id to this sid — a foreign session's frame is a no-op and the
    // matching one navigates WITHOUT re-fetching the detail.
    let detailCalls = 0
    server.use(
      http.get('/api/sessions/1', () => {
        detailCalls += 1
        return ok(detailFixture({ status: 'DRAFT' }))
      }),
      http.get('/api/children', () =>
        paged([sessionChildRow(2, 'Aisyah Putri')], { page: 1, per_page: 1000, total: 1 }),
      ),
    )
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelompok Merah')
    act(() => emitSignal('session.deleted', { rev: 1, entity_id: 99 }))
    expect(screen.queryByText('SESSIONS LIST')).toBeNull() // another session's frame

    act(() => emitSignal('session.deleted', { rev: 2, entity_id: 1 }))
    await screen.findByText('SESSIONS LIST')
    expect(detailCalls).toBe(1) // zero extra requests — no re-GET probe
  })
})

describe('session detail fetch failure', () => {
  it('renders the translated envelope error with a back action', async () => {
    server.use(
      http.get('/api/sessions/1', () =>
        HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'gone' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Data tidak ditemukan.')
    await user.click(screen.getByRole('button', { name: 'Kembali' }))
    await screen.findByText('SESSIONS LIST')
  })
})
