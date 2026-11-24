/**
 * Topics master (spec F-04 / wave F3) — behavior tests mirroring the green
 * Programs reference suite: server-paged list cursor (default
 * `sort_order asc`, search/program-filter/sort/pager keep the query and any
 * change rewinds to page=1), `topic.changed` SSE refetch of the SAME query
 * through the real StreamGate, multipart create/edit payloads (program_id is
 * create-only, sort_order only when entered, badge both-or-neither + image
 * pre-check), the typed-text delete gate and the partial `{is_active}` toggle.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http } from 'msw'
import { server } from '../../test/setup'
import { fail, ok, sseChannel } from '../../test/handlers'
import { expectToast } from '../../test/toast'
import StreamGate from '../../components/StreamGate'
import TopicsPage from './TopicsPage'
import {
  failFirstThenList,
  fieldCount,
  findOption,
  imageFile,
  multipartText,
  pagedList,
  programRow,
  queryOf,
  recordCall,
  renderAs,
  topicRow,
  uploadFiles,
} from '../../test/helpers.cov-master'

const ACTIVE = topicRow(1, 'Batik Nusantara', {
  badge_name: 'Badge Emas',
  badge_image_url: '/uploads/badges/emas.png',
})
const INACTIVE = topicRow(2, 'Tanah Nusantara', {
  description: null,
  is_active: false,
  badge_name: null,
})

/** Fresh program-options recorder (every TopicsPage mount fetches it). */
function programs() {
  return pagedList('/api/programs', [
    programRow(1, 'Eksplorasi Kota'),
    programRow(2, 'Petualangan Alam'),
  ])
}

/** The table row whose cell contains `name` (row-scoped button queries). */
function rowOf(name: string): HTMLElement {
  const cell = screen.getByText(name)
  const row = cell.closest('tr')
  if (!row) throw new Error(`no table row containing "${name}"`)
  return row
}

async function openCreateDialog(user: UserEvent): Promise<HTMLElement> {
  await user.click(screen.getByRole('button', { name: /Buat topik/ }))
  return screen.findByRole('dialog')
}

/** Inline validation marker on a Form.Item label. */
function fieldItem(dialog: HTMLElement, label: string): Element | null {
  return within(dialog)
    .getByLabelText(label)
    .closest('.ant-form-item')
}

/**
 * The clickable trigger box of the program Select inside a dialog. The page
 * filter and the form picker now carry different placeholders ("Semua
 * program" vs "Pilih program"), so this helper is dialog-scoped only.
 */
function programSelect(root: ParentNode): HTMLElement {
  const spans = Array.from(root.querySelectorAll('.ant-select-selection-placeholder')).filter(
    (span) => span.textContent === 'Pilih program',
  )
  if (spans.length !== 1) {
    throw new Error(`expected one program select, found ${spans.length}`)
  }
  const trigger = spans[0]?.closest('.ant-select-selector')
  if (!(trigger instanceof HTMLElement)) throw new Error('no .ant-select-selector found')
  return trigger
}

describe('topic list cursor', () => {
  it('renders rows with badge, status and dash cells under sort_order asc', async () => {
    const list = pagedList('/api/topics', [ACTIVE, INACTIVE])
    server.use(list.handler, programs().handler)
    renderAs(<TopicsPage />)

    expect(await screen.findByText('Batik Nusantara')).toBeInTheDocument()
    expect(screen.getByText('Tanah Nusantara')).toBeInTheDocument()
    expect(screen.getByText('Badge Emas')).toBeInTheDocument()
    expect(screen.getByText('Aktif')).toBeInTheDocument()
    expect(screen.getByText('Nonaktif')).toBeInTheDocument()
    expect(within(rowOf('Tanah Nusantara')).getAllByText('—')).toHaveLength(2) // desc + badge
    expect(screen.getByRole('columnheader', { name: 'Urutan' })).toHaveAttribute(
      'aria-sort',
      'ascending',
    )

    expect(list.seen[0].searchParams.get('page')).toBe('1')
    expect(list.seen[0].searchParams.get('per_page')).toBe('20')
    expect(list.seen[0].searchParams.get('sort')).toBe('sort_order')
    expect(list.seen[0].searchParams.get('dir')).toBe('asc')
  })

  it('shows the section empty text when the server returns no rows', async () => {
    server.use(pagedList('/api/topics', []).handler, programs().handler)
    renderAs(<TopicsPage />)

    expect(await screen.findByText('Belum ada topik untuk program ini.')).toBeInTheDocument()
  })

  it('search and the program filter rewind to page=1; clearing drops q', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/topics', [ACTIVE], 40)
    server.use(list.handler, programs().handler)
    renderAs(<TopicsPage />)
    await screen.findByText('Batik Nusantara')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen).toHaveLength(2))
    expect(list.seen[1].searchParams.get('page')).toBe('2')
    expect(queryOf(list.seen[1])).toBe(queryOf(list.seen[0]).replace('page=1', 'page=2'))

    await user.type(screen.getByPlaceholderText('Cari nama topik'), 'batik{Enter}')
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('q')).toBe('batik'))
    const searched = list.seen.at(-1) as URL
    expect(searched.searchParams.get('page')).toBe('1') // any filter change rewinds (02§3)
    expect(searched.searchParams.get('sort')).toBe('sort_order')
    expect(searched.searchParams.get('dir')).toBe('asc')

    await user.clear(screen.getByPlaceholderText('Cari nama topik'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.has('q')).toBe(false))
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))
    await user.click(screen.getByRole('combobox', { name: 'Semua program' }))
    await user.click(await findOption('Eksplorasi Kota'))
    await waitFor(() =>
      expect(list.seen.at(-1)?.searchParams.get('program_id')).toBe('1'),
    )
    const filtered = list.seen.at(-1) as URL
    expect(filtered.searchParams.get('page')).toBe('1') // filter rewinds

    // the create dialog prefills the active program filter into its Select
    const dialog = await openCreateDialog(user)
    await within(dialog).findByText('Eksplorasi Kota')
  })

  it('column sort cycles asc → desc → default; Urutan sorts too', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/topics', [ACTIVE], 40)
    server.use(list.handler, programs().handler)
    renderAs(<TopicsPage />)
    await screen.findByText('Batik Nusantara')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    const nameHeader = screen.getByRole('columnheader', { name: 'Nama topik' })
    await user.click(nameHeader)
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('name'))
    expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('asc')
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')
    expect(nameHeader).toHaveAttribute('aria-sort', 'ascending')

    await user.click(nameHeader)
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('desc'))

    await user.click(nameHeader) // third click cancels sorting → DEFAULT_SORT
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('sort_order'))
    expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('asc')
    expect(nameHeader).not.toHaveAttribute('aria-sort')
    const orderHeader = screen.getByRole('columnheader', { name: 'Urutan' })
    expect(orderHeader).toHaveAttribute('aria-sort', 'ascending')

    await user.click(orderHeader) // currently ascend → click toggles to descend
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('desc'))
    expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('sort_order')
  })

  it('renders the errors.<CODE> alert on a failed fetch and recovers on retry', async () => {
    const user = userEvent.setup()
    const list = failFirstThenList('/api/topics', 'DB_UNAVAILABLE', [ACTIVE])
    server.use(list.handler, programs().handler)
    renderAs(<TopicsPage />)

    expect(await screen.findByText('Layanan sedang sibuk. Coba lagi.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    expect(await screen.findByText('Batik Nusantara')).toBeInTheDocument()
    expect(screen.queryByText('Layanan sedang sibuk. Coba lagi.')).toBeNull()
    expect(list.seen).toHaveLength(2)
  })

  it('a failed program-options picker fetch stays silent; the list still renders', async () => {
    // useProgramOptions is best-effort: its rejection must not surface as an
    // alert — only the page's own /api/topics fetch may do that — and the
    // program filter is left with no options.
    server.use(
      pagedList('/api/topics', [ACTIVE]).handler,
      http.get('/api/programs', () => fail('DB_UNAVAILABLE')),
    )
    renderAs(<TopicsPage />)

    expect(await screen.findByText('Batik Nusantara')).toBeInTheDocument()
    expect(screen.queryByText('Layanan sedang sibuk. Coba lagi.')).toBeNull()
    const user = userEvent.setup()
    await user.click(screen.getAllByRole('combobox')[0])
    // The dropdown itself has no program rows to offer (the table's own
    // Program column still shows the topic's program — that's list data).
    await waitFor(() =>
      expect(document.querySelectorAll('.ant-select-dropdown .ant-select-item-option')).toHaveLength(0),
    )
  })
})

describe('topic signal refetch', () => {
  it('a topic.changed burst refetches the identical query once; stale rev dropped', async () => {
    const user = userEvent.setup()
    const channel = sseChannel('/api/stream')
    const list = pagedList('/api/topics', [ACTIVE], 40)
    server.use(channel.handler, list.handler, programs().handler)
    renderAs(
      <>
        <StreamGate />
        <TopicsPage />
      </>,
      { route: '/topics' },
    )

    await waitFor(() => expect(channel.handshakes).toBe(1))
    await screen.findByText('Batik Nusantara')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))
    const before = (list.seen.at(-1) as URL).search
    const count = list.seen.length

    channel.push('event: topic.changed\ndata: {"rev":1}\n\n')
    channel.push('event: topic.changed\ndata: {"rev":2}\n\n')
    await waitFor(() => expect(list.seen).toHaveLength(count + 1), { timeout: 3000 })
    expect((list.seen.at(-1) as URL).search).toBe(before) // same page + filters

    channel.push('event: topic.changed\ndata: {"rev":1}\n\n') // rev ≤ last_seen
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 500)
    await promise
    expect(list.seen).toHaveLength(count + 1)
  })
})

describe('topic create form', () => {
  it('blocks a missing name and a half-filled badge pair with no POST', async () => {
    const create = recordCall('post', '/api/topics', (request) => request.text())
    server.use(pagedList('/api/topics', [ACTIVE]).handler, programs().handler, create.handler)
    const user = userEvent.setup()
    renderAs(<TopicsPage />)
    await screen.findByText('Batik Nusantara')

    const dialog = await openCreateDialog(user)
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() =>
      expect(fieldItem(dialog, 'Nama topik')).toHaveClass('ant-form-item-has-error'),
    )
    expect(create.bodies).toHaveLength(0) // required rule fired client-side

    await user.click(programSelect(dialog))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.type(within(dialog).getByLabelText('Nama topik'), 'Batik Tulis')
    await user.type(within(dialog).getByLabelText('Nama badge'), 'Badge Saja')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    const mirrored = await within(dialog).findAllByText(
      'Nama dan gambar badge harus diisi bersamaan atau dikosongkan bersamaan.',
    )
    expect(mirrored.length).toBeGreaterThanOrEqual(2) // name + image fields
    expect(create.bodies).toHaveLength(0)
    expect(screen.getByRole('dialog')).toBeInTheDocument() // stays open
  })

  it('rejects wrong-format and oversize images through the pre-check toast', async () => {
    const create = recordCall('post', '/api/topics', (request) => request.text())
    server.use(pagedList('/api/topics', []).handler, programs().handler, create.handler)
    const user = userEvent.setup()
    renderAs(<TopicsPage />)
    await screen.findByText('Belum ada topik untuk program ini.')
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

  it('creates with multipart program_id, sort_order and badge upload, then refetches', async () => {
    const create = recordCall('post', '/api/topics', (request) => request.text())
    const list = pagedList('/api/topics', [ACTIVE])
    server.use(list.handler, programs().handler, create.handler)
    const user = userEvent.setup()
    renderAs(<TopicsPage />)
    await screen.findByText('Batik Nusantara')
    const initialFetches = list.seen.length

    const dialog = await openCreateDialog(user)
    await user.click(programSelect(dialog))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.type(within(dialog).getByLabelText('Nama topik'), 'Batik Tulis')
    await user.type(within(dialog).getByLabelText('Deskripsi'), 'Batik tulis Pekalongan')
    await user.type(within(dialog).getByLabelText('Urutan'), '5')
    await user.type(within(dialog).getByLabelText('Nama badge'), 'Badge Baru')
    uploadFiles([imageFile('badge.png', 'image/png')])
    await within(dialog).findByText('badge.png') // accepted file lands in the list

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(create.bodies).toHaveLength(1))
    const body = multipartText(create.bodies[0])
    expect(body).toContain('name="program_id"\n\n1')
    expect(body).toContain('name="name"\n\nBatik Tulis')
    expect(body).toContain('name="description"\n\nBatik tulis Pekalongan')
    expect(body).toContain('name="sort_order"\n\n5')
    expect(body).toContain('name="badge_name"\n\nBadge Baru')
    expect(fieldCount(body, 'badge_image')).toBe(1) // file part attached
    expect(body).toContain('Content-Type: image/png')

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Topik “Batik Tulis” dibuat')
  })

  it('shows the inline errors.<CODE> alert on a rejected save, omitting empty keys', async () => {
    const create = recordCall('post', '/api/topics', (request) => request.text(), () =>
      fail('VALIDATION'),
    )
    server.use(pagedList('/api/topics', []).handler, programs().handler, create.handler)
    const user = userEvent.setup()
    renderAs(<TopicsPage />)
    await screen.findByText('Belum ada topik untuk program ini.')
    const dialog = await openCreateDialog(user)

    await user.click(programSelect(dialog))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.type(within(dialog).getByLabelText('Nama topik'), 'Tanpa Urutan')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await within(dialog).findByText('Data tidak valid. Periksa kembali isian Anda.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(create.bodies).toHaveLength(1)
    const body = multipartText(create.bodies[0])
    expect(fieldCount(body, 'sort_order')).toBe(0) // never entered → key omitted
    expect(fieldCount(body, 'badge_image')).toBe(0) // no file chosen
    expect(fieldCount(body, 'badge_name')).toBe(1) // always sent (empty clears)
  })
})

describe('topic edit and delete', () => {
  it('edits via PATCH multipart: never program_id, program Select disabled', async () => {
    const update = recordCall('patch', '/api/topics/1', (request) => request.text())
    server.use(pagedList('/api/topics', [ACTIVE]).handler, programs().handler, update.handler)
    const user = userEvent.setup()
    renderAs(<TopicsPage />)
    await screen.findByText('Batik Nusantara')

    await user.click(within(rowOf('Batik Nusantara')).getByRole('button', { name: /Ubah/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('Nama topik')).toHaveValue('Batik Nusantara')
    expect(within(dialog).getByLabelText('Program')).toBeDisabled() // create-only field

    await user.clear(within(dialog).getByLabelText('Nama topik'))
    await user.type(within(dialog).getByLabelText('Nama topik'), 'Batik Nusantara 2')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(update.bodies).toHaveLength(1))
    const body = multipartText(update.bodies[0])
    expect(body).toContain('name="name"\n\nBatik Nusantara 2')
    expect(fieldCount(body, 'program_id')).toBe(0) // PATCH never carries program_id
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await expectToast('Topik “Batik Nusantara 2” disimpan')
  })

  it('delete is gated on the typed name and only then DELETEs + refetches', async () => {
    const remove = recordCall('delete', '/api/topics/1', async () => null, () =>
      ok({ deleted: true }),
    )
    const list = pagedList('/api/topics', [ACTIVE, INACTIVE])
    server.use(list.handler, programs().handler, remove.handler)
    const user = userEvent.setup()
    renderAs(<TopicsPage />)
    await screen.findByText('Batik Nusantara')
    const initialFetches = list.seen.length

    await user.click(within(rowOf('Batik Nusantara')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('Hapus Batik Nusantara?')
    await within(dialog).findByText('Ketik Batik Nusantara untuk mengonfirmasi.')

    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeDisabled()
    const typed = within(dialog).getByRole('textbox')
    await user.type(typed, 'Batik Nusantara lain')
    expect(confirm).toBeDisabled()
    expect(remove.bodies).toHaveLength(0)

    await user.clear(typed)
    await user.type(typed, 'Batik Nusantara')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(remove.bodies).toHaveLength(1))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Topik “Batik Nusantara” dihapus')
  })

  it('a failed DELETE toasts errors.<CODE> and keeps the dialog open', async () => {
    const remove = recordCall('delete', '/api/topics/1', async () => null, () =>
      fail('NOT_FOUND'),
    )
    server.use(pagedList('/api/topics', [ACTIVE]).handler, programs().handler, remove.handler)
    const user = userEvent.setup()
    renderAs(<TopicsPage />)
    await screen.findByText('Batik Nusantara')

    await user.click(within(rowOf('Batik Nusantara')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Batik Nusantara')
    await user.click(within(dialog).getByRole('button', { name: 'Konfirmasi' }))

    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Data tidak ditemukan.',
      ),
    )
    expect(remove.bodies).toHaveLength(1)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

describe('topic activation', () => {
  it('toggles is_active as partial PATCH, toasts a failure, succeeds on retry', async () => {
    const toggle = recordCall(
      'patch',
      '/api/topics/1',
      (request) => request.text(),
      (call) => (call === 1 ? fail('VALIDATION') : ok({})),
    )
    const list = pagedList('/api/topics', [ACTIVE])
    server.use(list.handler, programs().handler, toggle.handler)
    const user = userEvent.setup()
    renderAs(<TopicsPage />)
    await screen.findByText('Batik Nusantara')
    const initialFetches = list.seen.length

    await user.click(
      within(rowOf('Batik Nusantara')).getByRole('button', { name: /Nonaktifkan/ }),
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
      within(rowOf('Batik Nusantara')).getByRole('button', { name: /Nonaktifkan/ }),
    )
    await waitFor(() => expect(toggle.bodies).toHaveLength(2))
    await waitFor(() => expect(list.seen.length).toBe(initialFetches + 1))
    await expectToast('Topik “Batik Nusantara” dinonaktifkan')
  })
})
