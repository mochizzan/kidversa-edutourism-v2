/**
 * Schools list page (F-02) [SA]: server-paged cursor contract (default params,
 * every filter rewinds page=1, clearing the search drops q again), the
 * errors.<CODE> alert with a working retry, the REAL StreamGate
 * `school.changed` refetch of the SAME query, create/edit modal payloads,
 * popconfirm activate/deactivate (+ error toast without a refetch), and the
 * typed-text delete gate.
 */
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import StreamGate from '../../components/StreamGate'
import SchoolsPage, { type SchoolRow } from './SchoolsPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, ok, paged, sseChannel } from '../../test/handlers'
import { expectToast } from '../../test/toast'

const BANDUNG: SchoolRow = {
  id: 1,
  school_name: 'Bandung',
  country: 'Indonesia',
  city: null,
  is_active: true,
  created_at: '2026-10-01T00:00:00Z',
}
const BEKASI: SchoolRow = {
  id: 2,
  school_name: 'Bekasi',
  country: 'Indonesia',
  city: 'Jawa Barat',
  is_active: false,
  created_at: '',
}
const CIREBON: SchoolRow = {
  id: 3,
  school_name: 'Cirebon',
  country: 'Indonesia',
  city: 'Jawa Barat',
  is_active: true,
  created_at: '2026-03-05T00:00:00Z',
}

/** Page-aware list endpoint: records every request URL; page 2 serves its own row. */
function listHandler(seen: URL[]) {
  return http.get('/api/schools', ({ request }) => {
    const url = new URL(request.url)
    seen.push(url)
    const page = Number(url.searchParams.get('page') ?? '1')
    const perPage = Number(url.searchParams.get('per_page') ?? '20')
    return paged(page === 2 ? [CIREBON] : [BANDUNG, BEKASI], {
      page,
      per_page: perPage,
      total: 45,
    })
  })
}

/** The table row whose cell contains `school_name` (row-scoped action buttons). */
function rowOf(schoolName: string): HTMLElement {
  const cell = screen.getByText(schoolName)
  const row = cell.closest('tr')
  if (!row) throw new Error(`no table row containing "${schoolName}"`)
  return row
}

/* ---------- fake-timer steppers copied from the sessions SSE pattern ---------- */

async function until(condition: () => boolean, step = 5, limit = 4000): Promise<void> {
  for (let i = 0; i < limit && !condition(); i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(step)
    })
  }
  expect(condition()).toBe(true)
}

describe('school list rendering', () => {
  it('renders rows with country/city, status tags and created date under the default params', async () => {
    const seen: URL[] = []
    server.use(listHandler(seen))
    renderApp(<SchoolsPage />)

    expect(await screen.findByText('Bandung')).toBeInTheDocument()
    expect(screen.getByText('Bekasi')).toBeInTheDocument()
    expect(within(rowOf('Bandung')).getByText('Indonesia')).toBeInTheDocument()
    expect(within(rowOf('Bandung')).getByText('—')).toBeInTheDocument() // null city
    expect(within(rowOf('Bekasi')).getByText('Jawa Barat')).toBeInTheDocument()
    expect(screen.getByText('Aktif')).toBeInTheDocument()
    expect(screen.getByText('Nonaktif')).toBeInTheDocument()
    expect(
      screen.getByText(new Date(BANDUNG.created_at).toLocaleDateString()),
    ).toBeInTheDocument()

    expect(seen).toHaveLength(1)
    expect(seen[0].search).toBe('?page=1&per_page=20') // empty filters omitted
    expect([...seen[0].searchParams.keys()]).toEqual(['page', 'per_page'])
  })
})

describe('school list filters', () => {
  it('search and status filters rewind page=1, combine with paging, and clearing drops q', async () => {
    const seen: URL[] = []
    server.use(listHandler(seen))
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')
    expect(seen).toHaveLength(1)

    // Move to page 2 first — a filter change must snap the cursor back.
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(seen).toHaveLength(2))
    expect(seen[1].searchParams.get('page')).toBe('2')
    await screen.findByText('Cirebon')

    // Search → page=1 + q.
    await user.type(screen.getByPlaceholderText('Cari nama sekolah'), 'depok{Enter}')
    await waitFor(() => expect(seen).toHaveLength(3))
    expect(seen[2].searchParams.get('page')).toBe('1')
    expect(seen[2].searchParams.get('q')).toBe('depok')
    await screen.findByText('Bandung')

    // Paging keeps the search query in the contract.
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(seen).toHaveLength(4))
    expect(seen[3].searchParams.get('page')).toBe('2')
    expect(seen[3].searchParams.get('q')).toBe('depok')
    await screen.findByText('Cirebon')

    // is_active filter → is_active=true + page rewound to 1.
    await user.click(screen.getAllByRole('combobox')[0])
    const matches = await screen.findAllByText('Aktif')
    const activeOption = matches.find(
      (el) => el.closest('.ant-select-item-option') !== null,
    ) as HTMLElement
    await user.click(activeOption)
    await waitFor(() => expect(seen).toHaveLength(5))
    const filtered = seen[4]
    expect(filtered.searchParams.get('page')).toBe('1')
    expect(filtered.searchParams.get('q')).toBe('depok')
    expect(filtered.searchParams.get('is_active')).toBe('true')
    await screen.findByText('Bandung')

    // Clearing the search drops q but keeps the status filter and the cursor.
    await user.clear(screen.getByPlaceholderText('Cari nama sekolah'))
    await waitFor(() => expect(seen).toHaveLength(6))
    const cleared = seen[5]
    expect(cleared.searchParams.has('q')).toBe(false)
    expect(cleared.searchParams.get('is_active')).toBe('true')
    expect(cleared.searchParams.get('page')).toBe('1')
  })

  it('a page-size change rewinds to page 1 with the new per_page', async () => {
    const seen: URL[] = []
    server.use(listHandler(seen))
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(seen).toHaveLength(2))
    expect(seen[1].searchParams.get('page')).toBe('2')
    await screen.findByText('Cirebon')

    await user.click(screen.getByRole('combobox', { name: 'Page Size' }))
    await user.click(await screen.findByText('50 / page'))
    await waitFor(() => expect(seen).toHaveLength(3))
    expect(seen[2].searchParams.get('per_page')).toBe('50')
    expect(seen[2].searchParams.get('page')).toBe('1')
  })
})

describe('school list error handling', () => {
  it('renders the translated errors.<CODE> alert with a working retry', async () => {
    const seen: URL[] = []
    let failFirst = true
    server.use(
      http.get('/api/schools', ({ request }) => {
        seen.push(new URL(request.url))
        if (failFirst) {
          failFirst = false
          return fail('FORBIDDEN_ROLE')
        }
        return paged([BANDUNG, BEKASI], { page: 1, per_page: 20, total: 45 })
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Anda tidak punya akses untuk tindakan ini.')
    expect(screen.queryByText('Bandung')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    expect(await screen.findByText('Bandung')).toBeInTheDocument()
    expect(screen.queryByText('Anda tidak punya akses untuk tindakan ini.')).toBeNull()
    expect(seen).toHaveLength(2)
  })
})

describe('school signal refetch', () => {
  it('school.changed refetches the SAME query exactly once; a stale rev is dropped', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    const seen: URL[] = []
    const channel = sseChannel('/api/stream')
    server.use(channel.handler, listHandler(seen))
    renderApp(
      <>
        <StreamGate />
        <SchoolsPage />
      </>,
      { session: makeLoginData() },
    )

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Bandung') !== null)
    const before = (seen.at(-1) as URL).search
    const count = seen.length
    expect(before).toContain('page=1')

    channel.push('event: school.changed\ndata: {"rev":1}\n\n')
    await until(() => seen.length === count + 1)
    expect(seen.at(-1)?.search).toBe(before) // identical query

    channel.push('event: school.changed\ndata: {"rev":1}\n\n')
    await act(async () => {
      await vi.advanceTimersByTimeAsync(400)
    })
    expect(seen).toHaveLength(count + 1)
  })
})

describe('school create', () => {
  it('blocks an empty name with no POST, then posts the full payload and refetches', async () => {
    const seen: URL[] = []
    let posts = 0
    let payload: unknown = null
    server.use(
      listHandler(seen),
      http.post('/api/schools', async ({ request }) => {
        payload = await request.json()
        posts += 1
        return ok({ school: { ...BANDUNG, id: 9, school_name: 'Depok' } })
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(screen.getByRole('button', { name: /Sekolah baru/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Sekolah baru')).toBeInTheDocument()

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await within(dialog).findByText('Wajib diisi.')
    expect(posts).toBe(0)

    await user.type(within(dialog).getByLabelText('Nama sekolah'), 'Depok')
    expect(within(dialog).getByLabelText('Negara')).toHaveValue('Indonesia')
    await user.type(within(dialog).getByLabelText('Kota'), 'Jawa Barat')
    await user.click(within(dialog).getByRole('switch')) // is_active: on → off
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(posts).toBe(1))
    expect(payload).toEqual({
      school_name: 'Depok',
      country: 'Indonesia',
      city: 'Jawa Barat',
      is_active: false,
    })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), { timeout: 10_000 })
    await waitFor(() => expect(seen).toHaveLength(2)) // list refetched after save
    await expectToast('Sekolah “Depok” dibuat')
  })

  it('shows the generic translated alert for a VALIDATION failure', async () => {
    const seen: URL[] = []
    let posts = 0
    server.use(
      listHandler(seen),
      http.post('/api/schools', () => {
        posts += 1
        return fail('VALIDATION')
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(screen.getByRole('button', { name: /Sekolah baru/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Nama sekolah'), 'Bandung')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent('Data tidak valid. Periksa kembali isian Anda.')
    expect(posts).toBe(1)
    expect(screen.getByRole('dialog')).toBeInTheDocument() // modal stays open
    expect(seen).toHaveLength(1) // no refetch on failure
  })

  it('shows the translated inline alert for a non-VALIDATION failure', async () => {
    const seen: URL[] = []
    let posts = 0
    server.use(
      listHandler(seen),
      http.post('/api/schools', () => {
        posts += 1
        return fail('STATE_CONFLICT')
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(screen.getByRole('button', { name: /Sekolah baru/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Nama sekolah'), 'Depok')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await within(dialog).findByText('Data berubah di tempat lain. Muat ulang halaman.')
    expect(posts).toBe(1)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

describe('school edit', () => {
  it('prefills the row, PATCHes by id, closes and refetches', async () => {
    const seen: URL[] = []
    let patches = 0
    let payload: unknown = null
    server.use(
      listHandler(seen),
      http.patch('/api/schools/1', async ({ request }) => {
        payload = await request.json()
        patches += 1
        return ok({ school: { ...BANDUNG, school_name: 'Bandung Raya' } })
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(within(rowOf('Bandung')).getByRole('button', { name: 'Ubah' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Ubah sekolah')).toBeInTheDocument()
    const nameInput = within(dialog).getByLabelText('Nama sekolah')
    expect(nameInput).toHaveValue('Bandung')
    expect(within(dialog).getByLabelText('Kota')).toHaveValue('') // null city → empty

    await user.clear(nameInput)
    await user.type(nameInput, 'Bandung Raya')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(patches).toBe(1))
    expect(payload).toEqual({
      school_name: 'Bandung Raya',
      country: 'Indonesia',
      city: '',
      is_active: true,
    })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), { timeout: 10_000 })
    await waitFor(() => expect(seen).toHaveLength(2)) // refetched after save
    await expectToast('Sekolah “Bandung Raya” disimpan')
  })
})

describe('school activation', () => {
  it('activates an inactive row through the popconfirm and refetches', async () => {
    const seen: URL[] = []
    let activates = 0
    server.use(
      listHandler(seen),
      http.post('/api/schools/2/activate', () => {
        activates += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(within(rowOf('Bekasi')).getByRole('button', { name: 'Aktifkan' }))
    await screen.findByText('Aktifkan sekolah ini?')
    await user.click(await screen.findByRole('button', { name: 'OK' }))

    await waitFor(() => expect(activates).toBe(1))
    await waitFor(() => expect(seen).toHaveLength(2))
    await expectToast('Sekolah “Bekasi” diaktifkan')
  })

  it('deactivates an active row through the popconfirm and refetches', async () => {
    const seen: URL[] = []
    let deactivates = 0
    server.use(
      listHandler(seen),
      http.post('/api/schools/1/deactivate', () => {
        deactivates += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(within(rowOf('Bandung')).getByRole('button', { name: 'Nonaktifkan' }))
    await screen.findByText('Nonaktifkan sekolah ini? Semua anggota tidak akan bisa masuk.')
    await user.click(await screen.findByRole('button', { name: 'OK' }))

    await waitFor(() => expect(deactivates).toBe(1))
    await waitFor(() => expect(seen).toHaveLength(2))
    await expectToast('Sekolah “Bandung” dinonaktifkan')
  })

  it('toasts the translated error without refetching when activation fails', async () => {
    const seen: URL[] = []
    let activates = 0
    server.use(
      listHandler(seen),
      http.post('/api/schools/2/activate', () => {
        activates += 1
        return fail('FORBIDDEN_ROLE')
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(within(rowOf('Bekasi')).getByRole('button', { name: 'Aktifkan' }))
    await user.click(await screen.findByRole('button', { name: 'OK' }))

    await waitFor(() => expect(activates).toBe(1))
    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Anda tidak punya akses untuk tindakan ini.',
      ),
    )
    expect(seen).toHaveLength(1) // failure → list NOT refetched
  })
})

describe('school delete', () => {
  it('gates delete on the typed name, then DELETEs, closes and refetches', async () => {
    const seen: URL[] = []
    let deletes = 0
    server.use(
      listHandler(seen),
      http.delete('/api/schools/1', () => {
        deletes += 1
        return ok({ deleted: true })
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(within(rowOf('Bandung')).getByRole('button', { name: 'Hapus' }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('Hapus sekolah Bandung?')
    await within(dialog).findByText('Ketik Bandung untuk mengonfirmasi.')

    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeDisabled()
    const typed = within(dialog).getByRole('textbox')
    await user.type(typed, 'Bandun') // partial text keeps the gate locked
    expect(confirm).toBeDisabled()
    expect(deletes).toBe(0)

    await user.type(typed, 'g')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(deletes).toBe(1))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), { timeout: 10_000 })
    await waitFor(() => expect(seen).toHaveLength(2))
    await expectToast('Sekolah “Bandung” dihapus')
  })

  it('cancel closes the dialog without a DELETE', async () => {
    const seen: URL[] = []
    let deletes = 0
    server.use(
      listHandler(seen),
      http.delete('/api/schools/1', () => {
        deletes += 1
        return ok({ deleted: true })
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(within(rowOf('Bandung')).getByRole('button', { name: 'Hapus' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Bandung')
    expect(within(dialog).getByRole('button', { name: 'Konfirmasi' })).toBeEnabled()

    await user.click(within(dialog).getByRole('button', { name: 'Batal' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), { timeout: 10_000 })
    expect(deletes).toBe(0)
    expect(seen).toHaveLength(1)
  })

  it('a failed DELETE keeps the dialog open with the translated alert', async () => {
    const seen: URL[] = []
    let deletes = 0
    server.use(
      listHandler(seen),
      http.delete('/api/schools/1', () => {
        deletes += 1
        return fail('STATE_CONFLICT')
      }),
    )
    const user = userEvent.setup()
    renderApp(<SchoolsPage />)
    await screen.findByText('Bandung')

    await user.click(within(rowOf('Bandung')).getByRole('button', { name: 'Hapus' }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Bandung')
    await user.click(within(dialog).getByRole('button', { name: 'Konfirmasi' }))

    await within(dialog).findByText('Data berubah di tempat lain. Muat ulang halaman.')
    expect(deletes).toBe(1)
    expect(screen.getByRole('dialog')).toBeInTheDocument() // stays open
    expect(seen).toHaveLength(1) // no refetch on failure
  })
})
