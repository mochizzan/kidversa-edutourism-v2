/**
 * Children master list + lifecycle (spec F-07 / 02§3) — complements
 * ChildrenPage.test.tsx (age 11–15 confirm flow): FAS read-only rendering,
 * the is_active filter/search/sort/pager cursor contract, age 1–10 direct
 * save with the WA normalisation previews, blocked father numbers, the
 * standing `age_confirmed` acknowledgement on edit, the counts-gated typed
 * delete (`GET /api/children/:id` → `{child, counts}`), activate/deactivate
 * POSTs with `errors.<CODE>` toasts and the `child.changed` SSE refetch.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http } from 'msw'
import StreamGate from '../../components/StreamGate'
import ChildrenPage from './ChildrenPage'
import { server } from '../../test/setup'
import { fail, ok, sseChannel } from '../../test/handlers'
import { expectToast } from '../../test/toast'
import {
  childRow,
  failFirstThenList,
  findOption,
  pagedList,
  recordCall,
  renderAs,
  selectTrigger,
} from '../../test/helpers.cov-master'

const CHILD = childRow(7, 'Aisyah Putri')

/** The table row whose cell contains `name` (row-scoped button queries). */
function rowOf(name: string): HTMLElement {
  const cell = screen.getByText(name)
  const row = cell.closest('tr')
  if (!row) throw new Error(`no table row containing "${name}"`)
  return row
}

/** Open the create dialog and fill every required field for a direct save. */
async function openNewChild(user: UserEvent, age: string): Promise<HTMLElement> {
  await user.click(screen.getByRole('button', { name: /Tambah anak/ }))
  const dialog = await screen.findByRole('dialog')
  await user.type(within(dialog).getByLabelText('Nama anak'), 'Budi Santoso')
  await user.type(within(dialog).getByLabelText('Usia'), age)
  // New-child forms prefill the active school's name — replace it explicitly.
  const schoolInput = within(dialog).getByLabelText('Sekolah anak')
  await user.clear(schoolInput)
  await user.type(schoolInput, 'SD Ceria')
  await user.type(within(dialog).getByLabelText('Nama ibu'), 'Sari')
  await user.type(within(dialog).getByLabelText('WhatsApp ibu'), '081234567890')
  return dialog
}

describe('children list and filters', () => {
  it('renders rows with the ADMIN hints under the default sort cursor', async () => {
    const list = pagedList('/api/children', [CHILD])
    server.use(list.handler)
    renderAs(<ChildrenPage />)

    expect(await screen.findByText('Aisyah Putri')).toBeInTheDocument()
    expect(screen.getByText('SD Ceria')).toBeInTheDocument()
    expect(
      await screen.findByText(/Bedakan lewat sekolah, ibu, atau usia/),
    ).toBeInTheDocument()
    expect(list.seen[0].searchParams.get('page')).toBe('1')
    expect(list.seen[0].searchParams.get('per_page')).toBe('20')
    expect(list.seen[0].searchParams.get('sort')).toBe('id')
    expect(list.seen[0].searchParams.get('dir')).toBe('asc')
  })

  it('keeps FAS read-only: no create button, no action controls, readonly hint', async () => {
    server.use(pagedList('/api/children', [CHILD]).handler)
    renderAs(<ChildrenPage />, { role: 'FASILITATOR' })

    expect(await screen.findByText('Anda hanya dapat melihat data anak.')).toBeInTheDocument()
    expect(await screen.findByText('Aisyah Putri')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Tambah anak/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Ubah/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Nonaktifkan/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Hapus/ })).toBeNull()
  })

  it('search and the is_active filter both rewind to page=1 keeping the query', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/children', [CHILD], 40)
    server.use(list.handler)
    renderAs(<ChildrenPage />)
    await screen.findByText('Aisyah Putri')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    await user.type(screen.getByPlaceholderText('Cari nama anak'), 'aisyah{Enter}')
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('q')).toBe('aisyah'))
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')

    // rc-select renders the placeholder as a pointer-events:none span — the
    // interactive target is the selector box around it.
    await user.click(selectTrigger('Semua status'))
    await user.click(await findOption('Nonaktif'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('is_active')).toBe('false'))
    const filtered = list.seen.at(-1) as URL
    expect(filtered.searchParams.get('q')).toBe('aisyah') // filter merges with search
    expect(filtered.searchParams.get('page')).toBe('1')

    await user.clear(screen.getByPlaceholderText('Cari nama anak'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.has('q')).toBe(false))
  })

  it('name sort cycles asc → desc → default and rewinds each time', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/children', [CHILD], 40)
    server.use(list.handler)
    renderAs(<ChildrenPage />)
    await screen.findByText('Aisyah Putri')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    const nameHeader = screen.getByRole('columnheader', { name: 'Nama anak' })
    await user.click(nameHeader)
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('name'))
    expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('asc')
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')
    expect(nameHeader).toHaveAttribute('aria-sort', 'ascending')

    await user.click(nameHeader)
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('desc'))

    await user.click(nameHeader)
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('id'))
    expect(nameHeader).not.toHaveAttribute('aria-sort')
  })

  it('renders the errors.<CODE> alert and recovers through the retry button', async () => {
    const user = userEvent.setup()
    const list = failFirstThenList('/api/children', 'DB_UNAVAILABLE', [CHILD])
    server.use(list.handler)
    renderAs(<ChildrenPage />)

    expect(await screen.findByText('Layanan sedang sibuk. Coba lagi.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    expect(await screen.findByText('Aisyah Putri')).toBeInTheDocument()
    expect(list.seen).toHaveLength(2)
  })

  it('a child.changed burst refetches the identical query once; stale rev dropped', async () => {
    const user = userEvent.setup()
    const channel = sseChannel('/api/stream')
    const list = pagedList('/api/children', [CHILD], 40)
    server.use(channel.handler, list.handler)
    renderAs(
      <>
        <StreamGate />
        <ChildrenPage />
      </>,
      { route: '/children' },
    )

    await waitFor(() => expect(channel.handshakes).toBe(1))
    await screen.findByText('Aisyah Putri')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))
    const before = (list.seen.at(-1) as URL).search
    const count = list.seen.length

    channel.push('event: child.changed\ndata: {"rev":1}\n\n')
    channel.push('event: child.changed\ndata: {"rev":2}\n\n')
    await waitFor(() => expect(list.seen).toHaveLength(count + 1), { timeout: 3000 })
    expect((list.seen.at(-1) as URL).search).toBe(before)

    channel.push('event: child.changed\ndata: {"rev":1}\n\n') // rev ≤ last_seen
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 500)
    await promise
    expect(list.seen).toHaveLength(count + 1)
  })
})

describe('children form', () => {
  it('age 1–10 saves directly with normalised WA previews and no confirm modal', async () => {
    const create = recordCall('post', '/api/children', (request) => request.json())
    server.use(pagedList('/api/children', []).handler, create.handler)
    const user = userEvent.setup()
    renderAs(<ChildrenPage />)
    await screen.findByText('Belum ada data anak.')

    const dialog = await openNewChild(user, '8')
    await within(dialog).findByText('Format tersimpan: +6281234567890')
    await user.type(within(dialog).getByLabelText('Nama ayah/wali'), 'Budi')
    await user.type(within(dialog).getByLabelText('WhatsApp ayah/wali'), '08111111111')
    await within(dialog).findByText('Format tersimpan: +628111111111')

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(create.bodies).toHaveLength(1))
    expect(create.bodies[0]).toMatchObject({
      name: 'Budi Santoso',
      age: 8,
      age_confirmed: false,
      school: 'SD Ceria',
      mother_name: 'Sari',
      mother_wa: '+6281234567890',
      father_name: 'Budi',
      father_wa: '+628111111111',
      guardian_name: '',
    })
    expect(screen.queryByText('Konfirmasi usia')).toBeNull() // direct save
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await expectToast('Anak “Budi Santoso” dibuat')
  })

  it('blocks an unusable father WhatsApp number with no POST', async () => {
    const create = recordCall('post', '/api/children', (request) => request.json())
    server.use(pagedList('/api/children', []).handler, create.handler)
    const user = userEvent.setup()
    renderAs(<ChildrenPage />)
    await screen.findByText('Belum ada data anak.')

    const dialog = await openNewChild(user, '8')
    await user.type(within(dialog).getByLabelText('WhatsApp ayah/wali'), '12345')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await within(dialog).findByText('Nomor WhatsApp tidak valid.')
    expect(create.bodies).toHaveLength(0)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('edits a previously confirmed 11–15 child without re-prompting', async () => {
    const confirmed = childRow(7, 'Aisyah Putri', { age: 12, age_confirmed: true })
    const update = recordCall('patch', '/api/children/7', (request) => request.json())
    server.use(pagedList('/api/children', [confirmed]).handler, update.handler)
    const user = userEvent.setup()
    renderAs(<ChildrenPage />)
    await screen.findByText('Aisyah Putri')

    await user.click(within(rowOf('Aisyah Putri')).getByRole('button', { name: /Ubah/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('Usia')).toHaveValue('12')

    await user.clear(within(dialog).getByLabelText('Usia'))
    await user.type(within(dialog).getByLabelText('Usia'), '13')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(update.bodies).toHaveLength(1))
    expect(update.bodies[0]).toMatchObject({ age: 13, age_confirmed: true, name: 'Aisyah Putri' })
    expect(screen.queryByText('Konfirmasi usia')).toBeNull() // standing acknowledgement
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await expectToast('Anak “Aisyah Putri” disimpan')
  })

  it('shows the inline errors.<CODE> alert when the save is rejected', async () => {
    const create = recordCall('post', '/api/children', (request) => request.json(), () =>
      fail('VALIDATION'),
    )
    server.use(pagedList('/api/children', []).handler, create.handler)
    const user = userEvent.setup()
    renderAs(<ChildrenPage />)
    await screen.findByText('Belum ada data anak.')

    const dialog = await openNewChild(user, '8')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await within(dialog).findByText('Data tidak valid. Periksa kembali isian Anda.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(create.bodies).toHaveLength(1)
  })
})

describe('children delete', () => {
  it('fetches counts first, gates on the typed name, then DELETEs + refetches', async () => {
    const remove = recordCall('delete', '/api/children/7', async () => null, () =>
      ok({ deleted: true }),
    )
    const list = pagedList('/api/children', [CHILD])
    server.use(
      list.handler,
      http.get('/api/children/7', () =>
        ok({
          child: CHILD,
          counts: { members: 2, assessments: 3, photos: 4, narratives: 1, missions: 0, links: 5 },
        }),
      ),
      remove.handler,
    )
    const user = userEvent.setup()
    renderAs(<ChildrenPage />)
    await screen.findByText('Aisyah Putri')
    const initialFetches = list.seen.length

    await user.click(within(rowOf('Aisyah Putri')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText(
      '2 keanggotaan, 3 penilaian, 4 foto, 1 narasi, 0 misi, 5 tautan rapor ikut terhapus permanen.',
    )
    await within(dialog).findByText('Ketik Aisyah Putri untuk mengonfirmasi.')

    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeDisabled()
    const typed = within(dialog).getByRole('textbox')
    await user.type(typed, 'Aisyah')
    expect(confirm).toBeDisabled()
    expect(remove.bodies).toHaveLength(0)

    await user.clear(typed)
    await user.type(typed, 'Aisyah Putri')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(remove.bodies).toHaveLength(1))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Anak “Aisyah Putri” dihapus')
  })

  it('a failed counts fetch toasts errors.<CODE> and opens no dialog', async () => {
    const list = pagedList('/api/children', [CHILD])
    server.use(
      list.handler,
      http.get('/api/children/7', () => fail('NOT_FOUND')),
    )
    const user = userEvent.setup()
    renderAs(<ChildrenPage />)
    await screen.findByText('Aisyah Putri')

    await user.click(within(rowOf('Aisyah Putri')).getByRole('button', { name: /Hapus/ }))

    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Data tidak ditemukan.',
      ),
    )
    expect(screen.queryByRole('dialog')).toBeNull()
    await waitFor(() => expect(list.seen.length).toBe(2)) // stale row → list refetch
  })

  it('a failed DELETE toasts errors.<CODE> and keeps the dialog open', async () => {
    const list = pagedList('/api/children', [CHILD])
    const remove = recordCall('delete', '/api/children/7', async () => null, () =>
      fail('VALIDATION'),
    )
    server.use(
      list.handler,
      http.get('/api/children/7', () =>
        ok({ child: CHILD, counts: { members: 1, assessments: 1, photos: 0, narratives: 0, missions: 0, links: 0 } }),
      ),
      remove.handler,
    )
    const user = userEvent.setup()
    renderAs(<ChildrenPage />)
    await screen.findByText('Aisyah Putri')

    await user.click(within(rowOf('Aisyah Putri')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Aisyah Putri')
    await user.click(within(dialog).getByRole('button', { name: 'Konfirmasi' }))

    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Data tidak valid. Periksa kembali isian Anda.',
      ),
    )
    expect(remove.bodies).toHaveLength(1)
    expect(screen.getByRole('dialog')).toBeInTheDocument() // catch → dialog stays
  })

  it('cancelling the delete dialog sends nothing and keeps the row', async () => {
    const list = pagedList('/api/children', [CHILD])
    const remove = recordCall('delete', '/api/children/7', async () => null)
    server.use(
      list.handler,
      http.get('/api/children/7', () =>
        ok({ child: CHILD, counts: { members: 1, assessments: 1, photos: 0, narratives: 0, missions: 0, links: 0 } }),
      ),
      remove.handler,
    )
    const user = userEvent.setup()
    renderAs(<ChildrenPage />)
    await screen.findByText('Aisyah Putri')

    await user.click(within(rowOf('Aisyah Putri')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Aisyah Putri')
    expect(within(dialog).getByRole('button', { name: 'Konfirmasi' })).toBeEnabled()

    await user.click(within(dialog).getByRole('button', { name: 'Batal' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(remove.bodies).toHaveLength(0)
    expect(screen.getByText('Aisyah Putri')).toBeInTheDocument()
  })
})

describe('children activation', () => {
  it('deactivate POSTs, toasts a failure, then succeeds on retry with a refetch', async () => {
    const toggle = recordCall(
      'post',
      '/api/children/7/deactivate',
      async () => null, // the toggle POST carries no body
      (call) => (call === 1 ? fail('CHILD_INACTIVE') : ok({ child: CHILD })),
    )
    const list = pagedList('/api/children', [CHILD])
    server.use(list.handler, toggle.handler)
    const user = userEvent.setup()
    renderAs(<ChildrenPage />)
    await screen.findByText('Aisyah Putri')
    const initialFetches = list.seen.length

    await user.click(
      within(rowOf('Aisyah Putri')).getByRole('button', { name: /Nonaktifkan/ }),
    )
    await waitFor(() => expect(toggle.bodies).toHaveLength(1))
    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Anak sudah dinonaktifkan.',
      ),
    )
    expect(list.seen).toHaveLength(initialFetches) // failure → no refetch

    await user.click(
      within(rowOf('Aisyah Putri')).getByRole('button', { name: /Nonaktifkan/ }),
    )
    await waitFor(() => expect(toggle.bodies).toHaveLength(2))
    await waitFor(() => expect(list.seen.length).toBe(initialFetches + 1))
    await expectToast('Anak “Aisyah Putri” dinonaktifkan')
  })
})
