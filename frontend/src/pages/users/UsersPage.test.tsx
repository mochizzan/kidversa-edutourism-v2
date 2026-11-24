/**
 * Users list (F-03 / FL-01): paged rows with decision/status tags, ADMIN vs SA
 * filter scopes (role filter + school column for SA), pending pill + tab badge,
 * row decisions through antd Popconfirm, typed-gate delete surfacing
 * HAS_OPEN_GROUPS inline, error retry, empty state, pager cursor contract and
 * the StreamGate SSE refetch (user.changed debounced, pending.changed as a
 * full-value signal).
 *
 * Popconfirm assertions pin the DOM-verified accessible names of the default
 * antd en_US buttons — ConfigProvider carries no locale, so they read "OK" /
 * "Cancel".
 */
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http } from 'msw'
import StreamGate from '../../components/StreamGate'
import UsersPage from './UsersPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, makeUser, ok, paged, sseChannel } from '../../test/handlers'
import { expectToast } from '../../test/toast'

/* ---------- fixtures ---------- */

function userRow(overrides: Record<string, unknown> = {}) {
  return {
    id: 1,
    name: 'Budi Santoso',
    email: 'budi@kidversa.id',
    role: 'FASILITATOR',
    decision: 'PENDING',
    is_active: true,
    school_id: 1,
    wa_number: '081234567890',
    school_name: 'Bandung',
    ...overrides,
  }
}

const PENDING_ROW = () => userRow()
const APPROVED_ACTIVE_ROW = () =>
  userRow({ id: 2, name: 'Siti Aminah', email: 'siti@kidversa.id', decision: 'APPROVED' })
const APPROVED_INACTIVE_ROW = userRow({
  id: 4,
  name: 'Rina Amelia',
  email: 'rina@kidversa.id',
  decision: 'APPROVED',
  is_active: false,
})

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })

function listUsers(seen: URL[], rows: unknown[], total = rows.length) {
  return http.get('/api/users', ({ request }) => {
    const url = new URL(request.url)
    seen.push(url)
    return paged(rows, {
      page: Number(url.searchParams.get('page') ?? '1'),
      per_page: Number(url.searchParams.get('per_page') ?? '20'),
      total,
    })
  })
}

function pendingCount(value: number) {
  return http.get('/api/users/pending-count', () => ok({ pending: value }))
}

/* ---------- DOM helpers ---------- */

function rowEl(name: string): HTMLElement {
  const el = screen.getByText(name).closest('tr')
  if (!el) throw new Error(`no table row contains ${name}`)
  return el as HTMLElement
}

function filtersEl(): HTMLElement {
  const el = document.querySelector('.kv-list-filters')
  if (!el) throw new Error('filter bar not rendered')
  return el as HTMLElement
}

/** Open a trigger and pin the full option list of the dropdown it controls. */
async function readOptions(user: UserEvent, trigger: HTMLElement, expected: string[]): Promise<void> {
  await user.click(trigger)
  const texts = await waitFor(() => {
    const dropdowns = document.querySelectorAll<HTMLElement>(
      '.ant-select-dropdown:not(.ant-select-dropdown-hidden)',
    )
    if (dropdowns.length === 0) throw new Error('no select dropdown is open')
    const current = Array.from(
      dropdowns[dropdowns.length - 1].querySelectorAll('.ant-select-item-option-content'),
    ).map((node) => node.textContent ?? '')
    expect(current.length).toBeGreaterThan(0)
    return current
  })
  expect(texts).toEqual(expected)
}

/** Confirm an open Popconfirm — its accessible button names come from the DOM. */
async function confirmPopconfirm(user: UserEvent, title: string): Promise<void> {
  const titleEl = await screen.findByText(title)
  const popover = titleEl.closest('.ant-popover')
  if (!popover) throw new Error('popconfirm was not rendered')
  const pop = within(popover as HTMLElement)
  expect(pop.getByRole('button', { name: 'OK' })).toBeInTheDocument()
  expect(pop.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
  await user.click(pop.getByRole('button', { name: 'OK' }))
}

/** Everything except the page cursor — proves filters survive paging. */
function withoutPage(url: URL): string {
  return [...url.searchParams.entries()]
    .filter(([key]) => key !== 'page')
    .map(([key, value]) => `${key}=${value}`)
    .sort()
    .join('&')
}

async function until(condition: () => boolean, step = 5, limit = 4000): Promise<void> {
  for (let i = 0; i < limit && !condition(); i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(step)
    })
  }
  expect(condition()).toBe(true)
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

/* ---------- tests ---------- */

describe('users list rendering (ADMIN)', () => {
  it('renders rows, fetches list + pending count and offers only decision/is_active filters', async () => {
    const seen: URL[] = []
    let pendingGets = 0
    let schoolGets = 0
    server.use(
      listUsers(seen, [PENDING_ROW()]),
      http.get('/api/users/pending-count', () => {
        pendingGets += 1
        return ok({ pending: 0 })
      }),
      http.get('/api/schools', () => {
        schoolGets += 1
        return ok([])
      }),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })

    await screen.findByText('Budi Santoso')
    expect(seen).toHaveLength(1)
    expect(pendingGets).toBe(1)
    expect(schoolGets).toBe(0) // ADMIN never asks for schools

    const row = rowEl('Budi Santoso')
    expect(within(row).getByText('budi@kidversa.id')).toBeInTheDocument()
    expect(within(row).getByText('Fasilitator')).toBeInTheDocument() // role label
    expect(within(row).getByText('Menunggu')).toBeInTheDocument() // decision tag
    expect(within(row).getByText('Aktif')).toBeInTheDocument() // status tag

    const headers = screen
      .getAllByRole('columnheader')
      .map((cell) => cell.textContent)
    expect(headers).toEqual(
      expect.arrayContaining(['Nama', 'Email', 'Peran', 'Keputusan', 'Status', 'Tindakan']),
    )
    expect(headers).not.toContain('Sekolah') // facilitator-scope: no school column

    // Exactly two filter selects — decision + is_active, no role filter.
    const combos = within(filtersEl()).getAllByRole('combobox')
    expect(combos).toHaveLength(2)
    await readOptions(user, combos[0], ['Semua keputusan', 'Menunggu', 'Disetujui', 'Ditolak'])
    await readOptions(user, combos[1], ['Semua status', 'Aktif', 'Nonaktif'])
  })

  it('seeds decision=PENDING from the route query string', async () => {
    const seen: URL[] = []
    server.use(listUsers(seen, [PENDING_ROW()]), pendingCount(0))
    renderApp(<UsersPage />, { route: '/users?decision=PENDING', session: ADMIN_SESSION() })

    await screen.findByText('Budi Santoso')
    expect(seen[0].searchParams.get('decision')).toBe('PENDING')
    expect(seen[0].searchParams.get('page')).toBe('1')
  })

  it('drives decision and is_active query params from the filter selects', async () => {
    const seen: URL[] = []
    server.use(listUsers(seen, [APPROVED_ACTIVE_ROW()]), pendingCount(0))
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Siti Aminah')

    const combos = within(filtersEl()).getAllByRole('combobox')
    await user.click(combos[0])
    await user.click(
      await screen.findByText('Disetujui', { selector: '.ant-select-item-option-content' }),
    )
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('decision')).toBe('APPROVED'))

    await user.click(combos[1])
    await user.click(
      await screen.findByText('Nonaktif', { selector: '.ant-select-item-option-content' }),
    )
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('is_active')).toBe('false'))
    expect(seen.at(-1)?.searchParams.get('decision')).toBe('APPROVED') // filters combine
    expect(seen.at(-1)?.searchParams.get('page')).toBe('1')
  })

  it('renders the empty state when the server returns no rows', async () => {
    server.use(listUsers([], []), pendingCount(0))
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })

    await screen.findByText('Belum ada pengguna.')
  })

  it('renders the translated list error and recovers through Coba lagi', async () => {
    const seen: URL[] = []
    let broken = true
    server.use(
      http.get('/api/users', ({ request }) => {
        seen.push(new URL(request.url))
        if (broken) return fail('DB_UNAVAILABLE')
        return paged([PENDING_ROW()], { page: 1, per_page: 20, total: 1 })
      }),
      pendingCount(0),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Layanan sedang sibuk. Coba lagi.')

    broken = false
    await user.click(within(alert).getByRole('button', { name: 'Coba lagi' }))
    await screen.findByText('Budi Santoso')
    expect(seen).toHaveLength(2)
    expect(screen.queryByRole('alert')).toBeNull()
  })
})

describe('users list scope (SA)', () => {
  it('adds the role filter and school column, and drives the role query param', async () => {
    const seen: URL[] = []
    server.use(listUsers(seen, [PENDING_ROW()]), pendingCount(0))
    const user = userEvent.setup()
    renderApp(<UsersPage />, {
      route: '/users',
      session: makeLoginData({ user: makeUser({ role: 'SA' }) }),
    })
    await screen.findByText('Budi Santoso')

    const bar = filtersEl()
    // The role select appears only once the SA session has restored.
    await waitFor(() => expect(within(bar).getAllByRole('combobox')).toHaveLength(3))

    const headers = screen
      .getAllByRole('columnheader')
      .map((cell) => cell.textContent)
    expect(headers).toContain('Sekolah')
    expect(within(rowEl('Budi Santoso')).getByText('Bandung')).toBeInTheDocument() // school_name

    const combos = within(bar).getAllByRole('combobox')
    await readOptions(user, combos[0], ['Semua peran', 'Superadmin', 'Admin', 'Fasilitator'])
    await user.click(
      await screen.findByText('Superadmin', { selector: '.ant-select-item-option-content' }),
    )
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('role')).toBe('SA'))
    expect(seen.at(-1)?.searchParams.get('page')).toBe('1')
  })
})

describe('pending pill and tab badge', () => {
  it('shows count pill + tab badge and jumps to decision=PENDING on click', async () => {
    const seen: URL[] = []
    server.use(listUsers(seen, [PENDING_ROW()]), pendingCount(3))
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Budi Santoso')

    const pill = screen.getByRole('button', { name: '3 menunggu persetujuan' })
    const usersTab = screen.getAllByRole('tab')[0]
    expect(within(usersTab).getByText('3')).toBeInTheDocument() // Badge inside the tab label

    await user.click(pill)
    await waitFor(() => expect(seen).toHaveLength(2))
    const last = seen.at(-1) as URL
    expect(last.searchParams.get('decision')).toBe('PENDING')
    expect(last.searchParams.get('page')).toBe('1')
  })

  it('hides the pill and the badge when nothing is pending', async () => {
    server.use(listUsers([], []), pendingCount(0))
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Belum ada pengguna.')

    expect(screen.queryByRole('button', { name: '0 menunggu persetujuan' })).toBeNull()
    expect(within(screen.getAllByRole('tab')[0]).queryByText('0')).toBeNull()
  })
})

describe('row decisions', () => {
  it('approves a pending row through the popconfirm and refetches', async () => {
    const seen: URL[] = []
    let approvals = 0
    server.use(
      listUsers(seen, [PENDING_ROW()]),
      pendingCount(0),
      http.post('/api/users/1/approve', () => {
        approvals += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Budi Santoso')

    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Setujui' }))
    await confirmPopconfirm(user, 'Setujui Budi Santoso?')
    await waitFor(() => expect(approvals).toBe(1))
    await waitFor(() => expect(seen).toHaveLength(2))
    await expectToast('Pengguna “Budi Santoso” disetujui')
  })

  it('rejects a pending row through the popconfirm', async () => {
    const seen: URL[] = []
    let rejections = 0
    server.use(
      listUsers(seen, [PENDING_ROW()]),
      pendingCount(0),
      http.post('/api/users/1/reject', () => {
        rejections += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Budi Santoso')

    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Tolak' }))
    await confirmPopconfirm(user, 'Tolak Budi Santoso? Keputusan ini permanen.')
    await waitFor(() => expect(rejections).toBe(1))
    await waitFor(() => expect(seen).toHaveLength(2))
    await expectToast('Pengguna “Budi Santoso” ditolak')
  })

  it('deactivates an active row and activates an inactive one', async () => {
    const seen: URL[] = []
    let deactivations = 0
    let activations = 0
    server.use(
      listUsers(seen, [APPROVED_ACTIVE_ROW(), APPROVED_INACTIVE_ROW]),
      pendingCount(0),
      http.post('/api/users/2/deactivate', () => {
        deactivations += 1
        return ok({})
      }),
      http.post('/api/users/4/activate', () => {
        activations += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Siti Aminah')
    await screen.findByText('Rina Amelia')

    await user.click(within(rowEl('Siti Aminah')).getByRole('button', { name: 'Nonaktifkan' }))
    // The deactivate Popconfirm title interpolates the row's actual name.
    await confirmPopconfirm(user, 'Nonaktifkan Siti Aminah? Sesi login lainnya langsung dicabut.')
    await waitFor(() => expect(deactivations).toBe(1))
    await waitFor(() => expect(seen).toHaveLength(2))
    await expectToast('Pengguna “Siti Aminah” dinonaktifkan')

    await user.click(within(rowEl('Rina Amelia')).getByRole('button', { name: 'Aktifkan' }))
    await confirmPopconfirm(user, 'Aktifkan pengguna ini?')
    await waitFor(() => expect(activations).toBe(1))
    await waitFor(() => expect(seen).toHaveLength(3))
    await expectToast('Pengguna “Rina Amelia” diaktifkan')
  })

  it('toasts the translated errors.SESSION_LOCKED and skips the refetch on failure', async () => {
    const seen: URL[] = []
    let deactivations = 0
    server.use(
      listUsers(seen, [APPROVED_ACTIVE_ROW()]),
      pendingCount(0),
      http.post('/api/users/2/deactivate', () => {
        deactivations += 1
        return fail('SESSION_LOCKED')
      }),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Siti Aminah')

    await user.click(within(rowEl('Siti Aminah')).getByRole('button', { name: 'Nonaktifkan' }))
    await confirmPopconfirm(user, 'Nonaktifkan Siti Aminah? Sesi login lainnya langsung dicabut.')

    await screen.findByText('Sesi terkunci untuk perubahan ini.')
    expect(deactivations).toBe(1)
    expect(seen).toHaveLength(1) // a failed action never refetches
  })
})

describe('search filter', () => {
  it('applies q, rewinds to page 1 and drops q when cleared', async () => {
    const seen: URL[] = []
    server.use(listUsers(seen, [PENDING_ROW()], 45), pendingCount(0))
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Budi Santoso')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(seen).toHaveLength(2))
    expect(seen.at(-1)?.searchParams.get('page')).toBe('2')

    const search = screen.getByPlaceholderText('Cari nama atau email')
    await user.type(search, 'budi{Enter}')
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('q')).toBe('budi'))
    expect(seen.at(-1)?.searchParams.get('page')).toBe('1')

    await user.clear(search)
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('q')).toBeNull())
    expect(seen.at(-1)?.searchParams.get('page')).toBe('1')
  })
})

describe('delete flow', () => {
  it('locks confirmation behind the typed name, then deletes and refetches', async () => {
    const seen: URL[] = []
    let deletions = 0
    server.use(
      listUsers(seen, [PENDING_ROW()]),
      pendingCount(0),
      http.delete('/api/users/1', () => {
        deletions += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Budi Santoso')

    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Hapus' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Hapus pengguna Budi Santoso?')).toBeInTheDocument()
    expect(within(dialog).getByText('Ketik Budi Santoso untuk mengonfirmasi.')).toBeInTheDocument()

    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeDisabled()
    const typed = within(dialog).getByRole('textbox')
    await user.type(typed, 'Budi')
    expect(confirm).toBeDisabled() // partial text stays locked
    await user.type(typed, ' Santoso')
    expect(confirm).toBeEnabled()

    await user.click(confirm)
    await waitFor(() => expect(deletions).toBe(1))
    await waitFor(() => expect(seen).toHaveLength(2))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), { timeout: 10_000 })
    await expectToast('Pengguna “Budi Santoso” dihapus')
  })

  it('keeps the dialog open on HAS_OPEN_GROUPS and lists the open groups', async () => {
    const seen: URL[] = []
    let deletions = 0
    server.use(
      listUsers(seen, [PENDING_ROW()]),
      pendingCount(0),
      http.delete('/api/users/1', () => {
        deletions += 1
        return fail('HAS_OPEN_GROUPS', {
          groups: [{ gid: 9, session_id: 5, session_name: 'Kelas Batik Rabu' }],
        })
      }),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Budi Santoso')

    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Hapus' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Budi Santoso')
    await user.click(within(dialog).getByRole('button', { name: 'Konfirmasi' }))

    await waitFor(() => expect(deletions).toBe(1))
    const alert = await within(screen.getByRole('dialog')).findByRole('alert')
    expect(alert).toHaveTextContent('User masih memimpin kelompok aktif. Alihkan kelompok dulu.')
    const openDialog = screen.getByRole('dialog') // still open
    expect(
      within(openDialog).getByText('Pengguna masih memimpin kelompok berikut — alihkan dulu:'),
    ).toBeInTheDocument()
    expect(within(openDialog).getByText('Kelas Batik Rabu')).toBeInTheDocument()
    expect(seen).toHaveLength(1) // failed delete never refetches
  })

  it('shows a translated alert without the group list for other delete errors', async () => {
    const seen: URL[] = []
    server.use(
      listUsers(seen, [PENDING_ROW()]),
      pendingCount(0),
      http.delete('/api/users/1', () => fail('NOT_FOUND')),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Budi Santoso')

    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Hapus' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Budi Santoso')
    await user.click(within(dialog).getByRole('button', { name: 'Konfirmasi' }))

    const alert = await within(screen.getByRole('dialog')).findByRole('alert')
    expect(alert).toHaveTextContent('Data tidak ditemukan.')
    const openDialog = screen.getByRole('dialog') // stays open for a retry
    expect(within(openDialog).queryByText('Kelas Batik Rabu')).toBeNull()
    expect(
      within(openDialog).queryByText('Pengguna masih memimpin kelompok berikut — alihkan dulu:'),
    ).toBeNull()
    expect(seen).toHaveLength(1)
  })
})

describe('pager', () => {
  it('keeps the whole query on page 2 and rewinds the cursor on a size change', async () => {
    const seen: URL[] = []
    server.use(listUsers(seen, [PENDING_ROW()], 45), pendingCount(0))
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Budi Santoso')
    const first = seen.at(-1) as URL

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(seen).toHaveLength(2))
    const second = seen.at(-1) as URL
    expect(second.searchParams.get('page')).toBe('2')
    expect(withoutPage(second)).toBe(withoutPage(first))

    await user.click(screen.getByRole('combobox', { name: 'Page Size' }))
    await user.click(await screen.findByText('50 / page', { selector: '.ant-select-item-option-content' }))
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('per_page')).toBe('50'))
    expect(seen.at(-1)?.searchParams.get('page')).toBe('1')
  })
})

describe('sse refetch', () => {
  it('one debounced user.changed refetch with the SAME query; pending.changed applies at once', async () => {
    // Only the debounce clock may be faked: MSW + undici need their real
    // macrotasks to settle responses (sessions integration pattern).
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    const seen: URL[] = []
    const channel = sseChannel('/api/stream')
    server.use(channel.handler, listUsers(seen, [PENDING_ROW()]), pendingCount(3))
    renderApp(
      <>
        <StreamGate />
        <UsersPage />
      </>,
      { route: '/users', session: ADMIN_SESSION() },
    )

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Budi Santoso') !== null)

    const before = seen.at(-1)?.search
    const count = seen.length
    // A burst of two frames → exactly ONE refetch with the identical query.
    channel.push('event: user.changed\ndata: {"rev":1}\n\n')
    channel.push('event: user.changed\ndata: {"rev":2}\n\n')
    await advance(300)
    await advance(0)
    expect(seen).toHaveLength(count + 1)
    expect(seen.at(-1)?.search).toBe(before)

    // rev 1 again — already seen, the gate drops it without a refetch.
    channel.push('event: user.changed\ndata: {"rev":1}\n\n')
    await advance(300)
    await advance(0)
    expect(seen).toHaveLength(count + 1)

    // Full-value signal: renders immediately, no debounce wait.
    channel.push('event: pending.changed\ndata: {"pending":7,"rev":2}\n\n')
    await until(() => screen.queryByText('7 menunggu persetujuan') !== null)
    expect(within(screen.getAllByRole('tab')[0]).getByText('7')).toBeInTheDocument()
  })
})

describe('create and edit dialogs from the page', () => {
  it('saves a new user (POST + refetch) and opens then cancels the edit dialog', async () => {
    const seen: URL[] = []
    let posts = 0
    let payload: Record<string, unknown> | null = null
    server.use(
      listUsers(seen, [PENDING_ROW()]),
      pendingCount(0),
      http.post('/api/users', async ({ request }) => {
        payload = (await request.json()) as Record<string, unknown>
        posts += 1
        return ok({ user: PENDING_ROW() })
      }),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Budi Santoso')

    await user.click(screen.getByRole('button', { name: /Pengguna baru/ }))
    let dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Nama'), 'Rina Amelia')
    await user.type(within(dialog).getByLabelText('Email'), 'Rina@Kidversa.ID')
    await user.type(within(dialog).getByLabelText('Nomor WhatsApp'), '081234567890')
    await user.type(within(dialog).getByLabelText('Kata sandi'), 'rahasia1')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(posts).toBe(1))
    expect(payload).toEqual({
      name: 'Rina Amelia',
      email: 'rina@kidversa.id',
      wa_number: '081234567890',
      password: 'rahasia1',
      role: 'FASILITATOR',
    })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), { timeout: 10_000 })
    await waitFor(() => expect(seen).toHaveLength(2)) // saved → list refetched

    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Ubah' }))
    dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Ubah pengguna')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Nama')).toHaveValue('Budi Santoso')
    await user.click(within(dialog).getByRole('button', { name: 'Batal' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), { timeout: 10_000 })
  })
})

/* ---------- admin controls (design 2026-11-11 §8.3 / §9.6 / §10.6) ---------- */

const SA_SESSION = () =>
  makeLoginData({ user: makeUser({ id: 9, name: 'Super Admin', role: 'SA' }) })

const ADMIN_ROW = () =>
  userRow({
    id: 10,
    name: 'Admin Satu',
    email: 'admin.satu@kidversa.id',
    role: 'ADMIN',
    decision: 'APPROVED',
  })

const SA_SELF_ROW = () =>
  userRow({
    id: 9,
    name: 'Super Admin',
    email: 'sa@kidversa.id',
    role: 'SA',
    decision: 'APPROVED',
  })

describe('admin row actions', () => {
  /** The two account actions live behind the row's overflow button. */
  async function openGroup(user: UserEvent, rowName: string): Promise<void> {
    await user.click(within(rowEl(rowName)).getByRole('button', { name: 'Tindakan lain' }))
  }

  it('groups Login As + Reset Password for ADMIN/FASILITATOR rows but not the SA self row', async () => {
    server.use(
      listUsers([], [ADMIN_ROW(), APPROVED_ACTIVE_ROW(), SA_SELF_ROW()]),
      pendingCount(0),
    )
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: SA_SESSION() })

    await screen.findByText('Admin Satu')
    for (const name of ['Admin Satu', 'Siti Aminah']) {
      await openGroup(user, name)
      // antd keeps a closed row's portal menu in the DOM, so scope the
      // assertion to the most recently opened menu instead of the whole page.
      await waitFor(() => {
        const menus = screen
          .getAllByRole('menu')
          .filter((m) => within(m).queryByRole('menuitem', { name: 'Masuk sebagai' }))
        expect(menus.length).toBeGreaterThan(0)
        const menu = menus.at(-1)!
        expect(within(menu).getByRole('menuitem', { name: 'Masuk sebagai' })).toBeInTheDocument()
        expect(
          within(menu).getByRole('menuitem', { name: 'Atur ulang kata sandi' }),
        ).toBeInTheDocument()
      })
      await user.keyboard('{Escape}')
    }

    const self = rowEl('Super Admin')
    expect(within(self).queryByRole('button', { name: 'Tindakan lain' })).toBeNull()
    expect(within(self).queryByRole('button', { name: 'Masuk sebagai' })).toBeNull()
    expect(within(self).queryByRole('button', { name: 'Atur ulang kata sandi' })).toBeNull()
  })

  it('shows the group on a FASILITATOR row for an ADMIN session', async () => {
    server.use(listUsers([], [APPROVED_ACTIVE_ROW()]), pendingCount(0))
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })

    await screen.findByText('Siti Aminah')
    await openGroup(user, 'Siti Aminah')
    expect(await screen.findByRole('menuitem', { name: 'Masuk sebagai' })).toBeInTheDocument()
    expect(screen.getByRole('menuitem', { name: 'Atur ulang kata sandi' })).toBeInTheDocument()
  })

  it('opens the impersonation confirmation from the grouped action', async () => {
    server.use(listUsers([], [APPROVED_ACTIVE_ROW()]), pendingCount(0))
    const user = userEvent.setup()
    renderApp(<UsersPage />, { route: '/users', session: ADMIN_SESSION() })
    await screen.findByText('Siti Aminah')

    await openGroup(user, 'Siti Aminah')
    await user.click(await screen.findByRole('menuitem', { name: 'Masuk sebagai' }))

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Masuk sebagai Siti Aminah?')).toBeInTheDocument()
    expect(
      within(dialog).getByText(
        'Anda akan keluar dari akun Anda dan masuk sebagai Siti Aminah. Anda harus masuk lagi untuk kembali.',
      ),
    ).toBeInTheDocument()
  })
})
