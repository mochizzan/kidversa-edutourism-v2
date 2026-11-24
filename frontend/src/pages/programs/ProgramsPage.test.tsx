/**
 * Programs master (spec F-04 / 02§3 decision 25) — behavior tests:
 * server-paged list cursor contract (search/sort/pager keep the query, any
 * change rewinds to page=1), `program.changed` SSE refetch of the SAME
 * query through the real StreamGate, the badge both-or-neither + image
 * pre-check mirrors, multipart create/edit payloads, the typed-text delete
 * gate and the partial `{is_active}` toggle with `errors.<CODE>` toasts.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { server } from '../../test/setup'
import { fail, ok, sseChannel } from '../../test/handlers'
import { expectToast } from '../../test/toast'
import StreamGate from '../../components/StreamGate'
import ProgramsPage from './ProgramsPage'
import {
  failFirstThenList,
  fieldCount,
  imageFile,
  multipartText,
  pagedList,
  programRow,
  queryOf,
  recordCall,
  renderAs,
  uploadFiles,
} from '../../test/helpers.cov-master'

const ACTIVE = programRow(1, 'Eksplorasi Kota', {
  badge_name: 'Badge Emas',
  badge_image_url: '/uploads/badges/emas.png',
  topic_count: 2,
  activity_count: 5,
})
const INACTIVE = programRow(2, 'Petualangan Alam', {
  description: null,
  is_active: false,
  eligible: false,
  topic_count: 0,
  activity_count: 0,
})

/** The table row whose cell contains `name` (row-scoped button queries). */
function rowOf(name: string): HTMLElement {
  const cell = screen.getByText(name)
  const row = cell.closest('tr')
  if (!row) throw new Error(`no table row containing "${name}"`)
  return row
}

async function openCreateDialog(user: UserEvent): Promise<HTMLElement> {
  await user.click(screen.getByRole('button', { name: /Buat program/ }))
  return screen.findByRole('dialog')
}

/** Inline validation marker on a Form.Item label. */
function fieldItem(dialog: HTMLElement, label: string): Element | null {
  return within(dialog)
    .getByLabelText(label)
    .closest('.ant-form-item')
}

describe('program list cursor', () => {
  it('renders rows with status, eligibility and badge cells under the default sort', async () => {
    const list = pagedList('/api/programs', [ACTIVE, INACTIVE])
    server.use(list.handler)
    renderAs(<ProgramsPage />)

    expect(await screen.findByText('Eksplorasi Kota')).toBeInTheDocument()
    expect(screen.getByText('Petualangan Alam')).toBeInTheDocument()
    expect(screen.getByText('Badge Emas')).toBeInTheDocument()
    expect(screen.getByText('Aktif')).toBeInTheDocument()
    expect(screen.getByText('Nonaktif')).toBeInTheDocument()
    expect(screen.getByText('Siap dipakai di sesi')).toBeInTheDocument()
    expect(screen.getByText('Belum siap')).toBeInTheDocument()

    expect(list.seen[0].searchParams.get('page')).toBe('1')
    expect(list.seen[0].searchParams.get('per_page')).toBe('20')
    expect(list.seen[0].searchParams.get('sort')).toBe('id')
    expect(list.seen[0].searchParams.get('dir')).toBe('asc')
  })

  it('shows the section empty text when the server returns no rows', async () => {
    server.use(pagedList('/api/programs', []).handler)
    renderAs(<ProgramsPage />)

    expect(await screen.findByText('Belum ada program. Buat program pertama.')).toBeInTheDocument()
  })

  it('search rewinds to page=1 keeping sort, and clearing drops q again', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/programs', [ACTIVE], 40)
    server.use(list.handler)
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    await user.type(screen.getByPlaceholderText('Cari nama program'), 'batik{Enter}')
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('q')).toBe('batik'))
    const searched = list.seen.at(-1) as URL
    expect(searched.searchParams.get('page')).toBe('1') // any filter change rewinds (02§3)
    expect(searched.searchParams.get('sort')).toBe('id')
    expect(searched.searchParams.get('dir')).toBe('asc')

    await user.clear(screen.getByPlaceholderText('Cari nama program'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.has('q')).toBe(false))
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')
  })

  it('column sort cycles asc → desc → default and rewinds the page each time', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/programs', [ACTIVE], 40)
    server.use(list.handler)
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    const nameHeader = screen.getByRole('columnheader', { name: 'Nama program' })
    await user.click(nameHeader)
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('name'))
    expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('asc')
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')
    expect(nameHeader).toHaveAttribute('aria-sort', 'ascending')

    await user.click(nameHeader)
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('desc'))

    await user.click(nameHeader) // third click cancels sorting → DEFAULT_SORT
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('id'))
    expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('asc')
    expect(nameHeader).not.toHaveAttribute('aria-sort')
  })

  it('paging changes only the page cursor', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/programs', [ACTIVE], 40)
    server.use(list.handler)
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen).toHaveLength(2))
    expect(list.seen[1].searchParams.get('page')).toBe('2')
    expect(queryOf(list.seen[1])).toBe(queryOf(list.seen[0]).replace('page=1', 'page=2'))
  })

  it('renders the errors.<CODE> alert on a failed fetch and recovers on retry', async () => {
    const user = userEvent.setup()
    const list = failFirstThenList('/api/programs', 'DB_UNAVAILABLE', [ACTIVE])
    server.use(list.handler)
    renderAs(<ProgramsPage />)

    expect(await screen.findByText('Layanan sedang sibuk. Coba lagi.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    expect(await screen.findByText('Eksplorasi Kota')).toBeInTheDocument()
    expect(screen.queryByText('Layanan sedang sibuk. Coba lagi.')).toBeNull()
    expect(list.seen).toHaveLength(2)
  })
})

describe('program signal refetch', () => {
  it('a program.changed burst refetches the identical query once; stale rev dropped', async () => {
    const user = userEvent.setup()
    const channel = sseChannel('/api/stream')
    const list = pagedList('/api/programs', [ACTIVE], 40)
    server.use(channel.handler, list.handler)
    renderAs(
      <>
        <StreamGate />
        <ProgramsPage />
      </>,
      { route: '/programs' },
    )

    await waitFor(() => expect(channel.handshakes).toBe(1))
    await screen.findByText('Eksplorasi Kota')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))
    const before = (list.seen.at(-1) as URL).search
    const count = list.seen.length

    channel.push('event: program.changed\ndata: {"rev":1}\n\n')
    channel.push('event: program.changed\ndata: {"rev":2}\n\n')
    await waitFor(() => expect(list.seen).toHaveLength(count + 1), { timeout: 3000 })
    expect((list.seen.at(-1) as URL).search).toBe(before) // same page + filters

    channel.push('event: program.changed\ndata: {"rev":1}\n\n') // rev ≤ last_seen
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 500)
    await promise
    expect(list.seen).toHaveLength(count + 1)
  })
})

describe('program create form', () => {
  it('blocks a missing name and a half-filled badge pair with no POST', async () => {
    const create = recordCall('post', '/api/programs', (request) => request.text())
    server.use(pagedList('/api/programs', [ACTIVE]).handler, create.handler)
    const user = userEvent.setup()
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')

    const dialog = await openCreateDialog(user)
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() =>
      expect(fieldItem(dialog, 'Nama program')).toHaveClass('ant-form-item-has-error'),
    )
    expect(create.bodies).toHaveLength(0) // required rule fired client-side

    await user.type(within(dialog).getByLabelText('Nama program'), 'Program Baru')
    await user.type(within(dialog).getByLabelText('Nama badge'), 'Badge Emas')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    const mirrored = await within(dialog).findAllByText(
      'Nama dan gambar badge harus diisi bersamaan atau dikosongkan bersamaan.',
    )
    expect(mirrored.length).toBeGreaterThanOrEqual(2) // name + image fields
    expect(create.bodies).toHaveLength(0)
    expect(screen.getByRole('dialog')).toBeInTheDocument() // stays open
  })

  it('rejects wrong-format and oversize images through the pre-check toast', async () => {
    const create = recordCall('post', '/api/programs', (request) => request.text())
    server.use(pagedList('/api/programs', []).handler, create.handler)
    const user = userEvent.setup()
    renderAs(<ProgramsPage />)
    await screen.findByText('Belum ada program. Buat program pertama.')
    const dialog = await openCreateDialog(user)

    uploadFiles([imageFile('icon.ico', 'image/vnd.microsoft.icon')])
    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'JPG/JPEG/PNG, maksimal 50 MB.',
      ),
    )
    expect(within(dialog).queryByText('icon.ico')).toBeNull()

    uploadFiles([imageFile('big.png', 'image/png', 51 * 1024 * 1024)])
    await waitFor(() =>
      expect(document.querySelectorAll('.ant-message-notice').length).toBeGreaterThanOrEqual(2),
    )
    expect(within(dialog).queryByText('big.png')).toBeNull()
    expect(create.bodies).toHaveLength(0) // blocked client-side before any POST
  })

  it('creates with a multipart badge upload, then closes and refetches', async () => {
    const create = recordCall('post', '/api/programs', (request) => request.text())
    const list = pagedList('/api/programs', [ACTIVE])
    server.use(list.handler, create.handler)
    const user = userEvent.setup()
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')
    const initialFetches = list.seen.length

    const dialog = await openCreateDialog(user)
    await user.type(within(dialog).getByLabelText('Nama program'), 'Program Baru')
    await user.type(within(dialog).getByLabelText('Deskripsi'), 'Jelajah kota')
    await user.type(within(dialog).getByLabelText('Nama badge'), 'Badge Baru')
    uploadFiles([imageFile('badge.png', 'image/png')])
    await within(dialog).findByText('badge.png') // accepted file lands in the list

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(create.bodies).toHaveLength(1))
    const body = multipartText(create.bodies[0])
    expect(body).toContain('name="name"\n\nProgram Baru')
    expect(body).toContain('name="description"\n\nJelajah kota')
    expect(body).toContain('name="badge_name"\n\nBadge Baru')
    expect(fieldCount(body, 'badge_image')).toBe(1) // file part attached
    expect(body).toContain('Content-Type: image/png')

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Program “Program Baru” dibuat')
  })

  it('shows the inline errors.<CODE> alert for a rejected save without closing', async () => {
    const create = recordCall('post', '/api/programs', (request) => request.text(), () =>
      fail('VALIDATION'),
    )
    server.use(pagedList('/api/programs', []).handler, create.handler)
    const user = userEvent.setup()
    renderAs(<ProgramsPage />)
    await screen.findByText('Belum ada program. Buat program pertama.')
    const dialog = await openCreateDialog(user)

    await user.type(within(dialog).getByLabelText('Nama program'), 'Program Baru')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await within(dialog).findByText('Data tidak valid. Periksa kembali isian Anda.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(create.bodies).toHaveLength(1)
  })
})

describe('program edit and delete', () => {
  it('edits via PATCH multipart: new name, badge kept, no image field', async () => {
    const update = recordCall('patch', '/api/programs/1', (request) => request.text())
    server.use(pagedList('/api/programs', [ACTIVE]).handler, update.handler)
    const user = userEvent.setup()
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')

    await user.click(within(rowOf('Eksplorasi Kota')).getByRole('button', { name: /Ubah/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('Nama program')).toHaveValue('Eksplorasi Kota')
    expect(within(dialog).getByLabelText('Nama badge')).toHaveValue('Badge Emas')

    await user.clear(within(dialog).getByLabelText('Nama program'))
    await user.type(within(dialog).getByLabelText('Nama program'), 'Eksplorasi Kota 2')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(update.bodies).toHaveLength(1))
    const body = multipartText(update.bodies[0])
    expect(body).toContain('name="name"\n\nEksplorasi Kota 2')
    expect(body).toContain('name="badge_name"\n\nBadge Emas')
    expect(fieldCount(body, 'badge_image')).toBe(0) // stored image untouched
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await expectToast('Program “Eksplorasi Kota 2” disimpan')
  })

  it('delete is gated on the typed name and only then DELETEs + refetches', async () => {
    const remove = recordCall('delete', '/api/programs/1', async () => null, () =>
      ok({ deleted: true }),
    )
    const list = pagedList('/api/programs', [ACTIVE, INACTIVE])
    server.use(list.handler, remove.handler)
    const user = userEvent.setup()
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')
    const initialFetches = list.seen.length

    await user.click(within(rowOf('Eksplorasi Kota')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('Hapus Eksplorasi Kota?')
    await within(dialog).findByText('Ketik Eksplorasi Kota untuk mengonfirmasi.')

    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeDisabled()
    const typed = within(dialog).getByRole('textbox')
    await user.type(typed, 'Eksplorasi Kota lain')
    expect(confirm).toBeDisabled()
    expect(remove.bodies).toHaveLength(0)

    await user.clear(typed)
    await user.type(typed, 'Eksplorasi Kota')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(remove.bodies).toHaveLength(1))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Program “Eksplorasi Kota” dihapus')
  })

  it('a failed DELETE toasts errors.<CODE> and keeps the dialog open', async () => {
    const remove = recordCall('delete', '/api/programs/1', async () => null, () =>
      fail('NOT_FOUND'),
    )
    server.use(pagedList('/api/programs', [ACTIVE]).handler, remove.handler)
    const user = userEvent.setup()
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')

    await user.click(within(rowOf('Eksplorasi Kota')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Eksplorasi Kota')
    await user.click(within(dialog).getByRole('button', { name: 'Konfirmasi' }))

    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Data tidak ditemukan.',
      ),
    )
    expect(remove.bodies).toHaveLength(1)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('cancelling the delete dialog sends nothing and restores the row', async () => {
    const remove = recordCall('delete', '/api/programs/1', async () => null)
    server.use(pagedList('/api/programs', [ACTIVE]).handler, remove.handler)
    const user = userEvent.setup()
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')

    await user.click(within(rowOf('Eksplorasi Kota')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Eksplorasi Kota')
    expect(within(dialog).getByRole('button', { name: 'Konfirmasi' })).toBeEnabled()

    await user.click(within(dialog).getByRole('button', { name: 'Batal' }))

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    expect(remove.bodies).toHaveLength(0)
    expect(screen.getByText('Eksplorasi Kota')).toBeInTheDocument()
  })
})

describe('program activation', () => {
  it('toggles is_active as partial PATCH, toasts a failure, succeeds on retry', async () => {
    const toggle = recordCall(
      'patch',
      '/api/programs/1',
      (request) => request.text(),
      (call) => (call === 1 ? fail('VALIDATION') : ok({})),
    )
    const list = pagedList('/api/programs', [ACTIVE])
    server.use(list.handler, toggle.handler)
    const user = userEvent.setup()
    renderAs(<ProgramsPage />)
    await screen.findByText('Eksplorasi Kota')
    const initialFetches = list.seen.length

    await user.click(
      within(rowOf('Eksplorasi Kota')).getByRole('button', { name: /Nonaktifkan/ }),
    )
    await waitFor(() => expect(toggle.bodies).toHaveLength(1))
    expect(multipartText(toggle.bodies[0])).toContain('name="is_active"\n\nfalse')
    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Data tidak valid. Periksa kembali isian Anda.',
      ),
    )
    expect(list.seen).toHaveLength(initialFetches) // failure → no refetch

    await user.click(
      within(rowOf('Eksplorasi Kota')).getByRole('button', { name: /Nonaktifkan/ }),
    )
    await waitFor(() => expect(toggle.bodies).toHaveLength(2))
    await waitFor(() => expect(list.seen.length).toBe(initialFetches + 1))
    await expectToast('Program “Eksplorasi Kota” dinonaktifkan')
  })
})
