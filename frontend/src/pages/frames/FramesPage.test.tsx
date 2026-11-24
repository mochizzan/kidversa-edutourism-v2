/**
 * Frames master (spec F-06 / wave F3) — behavior tests mirroring the green
 * Programs reference suite: server-paged list cursor (default `id asc`,
 * search/program-filter/sort/pager keep the query and any change rewinds to
 * page=1), `frame.changed` SSE refetch of the SAME query through the real
 * StreamGate, the multipart create payload (`program_ids` as a REPEATED
 * plain key, required `image`, filename-stem name auto-fill), the PATCH that
 * always carries `program_ids` but never a stale `image`, and the typed-text
 * delete gate.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { server } from '../../test/setup'
import { fail, ok, sseChannel } from '../../test/handlers'
import { expectToast } from '../../test/toast'
import StreamGate from '../../components/StreamGate'
import FramesPage from './FramesPage'
import {
  failFirstThenList,
  fieldCount,
  findOption,
  frameRow,
  imageFile,
  multipartText,
  pagedList,
  programRow,
  queryOf,
  recordCall,
  renderAs,
  uploadFiles,
} from '../../test/helpers.cov-master'

const SUDUT = frameRow(1, 'Sudut Kota')
const ROMBAK = frameRow(2, 'Rombak Diri', { programs: [] })

/** Fresh program-options recorder (every FramesPage mount fetches it). */
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
  await user.click(screen.getByRole('button', { name: /Unggah frame/ }))
  return screen.findByRole('dialog')
}

/** Inline validation marker on a Form.Item label. */
function fieldItem(dialog: HTMLElement, label: string): Element | null {
  return within(dialog)
    .getByLabelText(label)
    .closest('.ant-form-item')
}

/** Clickable trigger box of a Select identified by its placeholder SPAN. */
function selectByPlaceholder(root: ParentNode, placeholder: string): HTMLElement {
  const spans = Array.from(root.querySelectorAll('.ant-select-selection-placeholder')).filter(
    (span) => span.textContent === placeholder,
  )
  if (spans.length !== 1) {
    throw new Error(`expected one select with placeholder "${placeholder}", found ${spans.length}`)
  }
  const trigger = spans[0]?.closest('.ant-select-selector')
  if (!(trigger instanceof HTMLElement)) throw new Error('no .ant-select-selector found')
  return trigger
}

// NOTE: the shared `selectTrigger` helpers are ambiguous on this page — the
// table column header and the Form.Item extra hint repeat the Select
// placeholder texts verbatim — so triggers are resolved via the placeholder
// SPAN inside `.ant-select-selector` instead.

describe('frame list cursor', () => {
  it('renders rows with avatar, program tags and dash cells under id asc', async () => {
    const list = pagedList('/api/frames', [SUDUT, ROMBAK])
    server.use(list.handler, programs().handler)
    renderAs(<FramesPage />)

    expect(await screen.findByText('Sudut Kota')).toBeInTheDocument()
    expect(screen.getByText('Rombak Diri')).toBeInTheDocument()
    expect(within(rowOf('Sudut Kota')).getByText('Eksplorasi Kota')).toBeInTheDocument()
    expect(within(rowOf('Rombak Diri')).getByText('—')).toBeInTheDocument() // no programs
    expect(screen.getByRole('columnheader', { name: 'Nama frame' })).not.toHaveAttribute(
      'aria-sort',
    )

    expect(list.seen[0].searchParams.get('page')).toBe('1')
    expect(list.seen[0].searchParams.get('per_page')).toBe('20')
    expect(list.seen[0].searchParams.get('sort')).toBe('id')
    expect(list.seen[0].searchParams.get('dir')).toBe('asc')
  })

  it('shows the section empty text when the server returns no rows', async () => {
    server.use(pagedList('/api/frames', []).handler, programs().handler)
    renderAs(<FramesPage />)

    expect(await screen.findByText('Belum ada frame.')).toBeInTheDocument()
  })

  it('search and the program filter rewind to page=1; clearing drops q', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/frames', [SUDUT], 40)
    server.use(list.handler, programs().handler)
    renderAs(<FramesPage />)
    await screen.findByText('Sudut Kota')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen).toHaveLength(2))
    expect(list.seen[1].searchParams.get('page')).toBe('2')
    expect(queryOf(list.seen[1])).toBe(queryOf(list.seen[0]).replace('page=1', 'page=2'))

    await user.type(screen.getByPlaceholderText('Cari nama frame'), 'sudut{Enter}')
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('q')).toBe('sudut'))
    const searched = list.seen.at(-1) as URL
    expect(searched.searchParams.get('page')).toBe('1') // any filter change rewinds (02§3)
    expect(searched.searchParams.get('sort')).toBe('id')
    expect(searched.searchParams.get('dir')).toBe('asc')

    await user.clear(screen.getByPlaceholderText('Cari nama frame'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.has('q')).toBe(false))
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))
    await user.click(selectByPlaceholder(document.body, 'Semua program'))
    await user.click(await findOption('Eksplorasi Kota'))
    await waitFor(() =>
      expect(list.seen.at(-1)?.searchParams.get('program_id')).toBe('1'),
    )
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1') // filter rewinds
  })

  it('column sort cycles asc → desc → default and rewinds the page each time', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/frames', [SUDUT], 40)
    server.use(list.handler, programs().handler)
    renderAs(<FramesPage />)
    await screen.findByText('Sudut Kota')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    const nameHeader = screen.getByRole('columnheader', { name: 'Nama frame' })
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

  it('renders the errors.<CODE> alert on a failed fetch and recovers on retry', async () => {
    const user = userEvent.setup()
    const list = failFirstThenList('/api/frames', 'DB_UNAVAILABLE', [SUDUT])
    server.use(list.handler, programs().handler)
    renderAs(<FramesPage />)

    expect(await screen.findByText('Layanan sedang sibuk. Coba lagi.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    expect(await screen.findByText('Sudut Kota')).toBeInTheDocument()
    expect(screen.queryByText('Layanan sedang sibuk. Coba lagi.')).toBeNull()
    expect(list.seen).toHaveLength(2)
  })
})

describe('frame signal refetch', () => {
  it('a frame.changed burst refetches the identical query once; stale rev dropped', async () => {
    const user = userEvent.setup()
    const channel = sseChannel('/api/stream')
    const list = pagedList('/api/frames', [SUDUT], 40)
    server.use(channel.handler, list.handler, programs().handler)
    renderAs(
      <>
        <StreamGate />
        <FramesPage />
      </>,
      { route: '/frames' },
    )

    await waitFor(() => expect(channel.handshakes).toBe(1))
    await screen.findByText('Sudut Kota')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))
    const before = (list.seen.at(-1) as URL).search
    const count = list.seen.length

    channel.push('event: frame.changed\ndata: {"rev":1}\n\n')
    channel.push('event: frame.changed\ndata: {"rev":2}\n\n')
    await waitFor(() => expect(list.seen).toHaveLength(count + 1), { timeout: 3000 })
    expect((list.seen.at(-1) as URL).search).toBe(before) // same page + filters

    channel.push('event: frame.changed\ndata: {"rev":1}\n\n') // rev ≤ last_seen
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 500)
    await promise
    expect(list.seen).toHaveLength(count + 1)
  })
})

describe('frame create form', () => {
  it('blocks a save without name/programs/image and posts nothing', async () => {
    const create = recordCall('post', '/api/frames', (request) => request.text())
    server.use(pagedList('/api/frames', [SUDUT]).handler, programs().handler, create.handler)
    const user = userEvent.setup()
    renderAs(<FramesPage />)
    await screen.findByText('Sudut Kota')

    const dialog = await openCreateDialog(user)
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() =>
      expect(fieldItem(dialog, 'Nama frame')).toHaveClass('ant-form-item-has-error'),
    )
    expect(fieldItem(dialog, 'Tautan program')).toHaveClass('ant-form-item-has-error')
    expect(create.bodies).toHaveLength(0)

    // rules pass now → the modal's own image guard fires instead of a POST
    await user.type(within(dialog).getByLabelText('Nama frame'), 'Raket Santai')
    await user.click(selectByPlaceholder(dialog, 'Pilih minimal satu program.'))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() =>
      expect(fieldItem(dialog, 'Gambar frame')).toHaveClass('ant-form-item-has-error'),
    )
    expect(create.bodies).toHaveLength(0)
    expect(screen.getByRole('dialog')).toBeInTheDocument() // stays open
  })

  it('rejects wrong-format and oversize images through the pre-check toast', async () => {
    const create = recordCall('post', '/api/frames', (request) => request.text())
    server.use(pagedList('/api/frames', []).handler, programs().handler, create.handler)
    const user = userEvent.setup()
    renderAs(<FramesPage />)
    await screen.findByText('Belum ada frame.')
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

  it('uploads multipart: repeated program_ids, image part, name from filename stem', async () => {
    const create = recordCall('post', '/api/frames', (request) => request.text())
    const list = pagedList('/api/frames', [SUDUT])
    server.use(list.handler, programs().handler, create.handler)
    const user = userEvent.setup()
    renderAs(<FramesPage />)
    await screen.findByText('Sudut Kota')
    const initialFetches = list.seen.length

    const dialog = await openCreateDialog(user)
    uploadFiles([imageFile('raketsantai.png', 'image/png')])
    await within(dialog).findByText('raketsantai.png') // accepted file lands in the list
    expect(within(dialog).getByLabelText('Nama frame')).toHaveValue('raketsantai') // auto-filled

    await user.click(selectByPlaceholder(dialog, 'Pilih minimal satu program.'))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.click(await findOption('Petualangan Alam')) // dropdown stays open in multiple mode

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(create.bodies).toHaveLength(1))
    const body = multipartText(create.bodies[0])
    expect(fieldCount(body, 'program_ids')).toBe(2) // repeated plain key
    expect(body).toContain('name="program_ids"\n\n1')
    expect(body).toContain('name="program_ids"\n\n2')
    expect(body).toContain('name="name"\n\nraketsantai')
    expect(fieldCount(body, 'image')).toBe(1) // required file part attached
    expect(body).toContain('Content-Type: image/png')

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Frame “raketsantai” dibuat')
  })

  it('shows the inline errors.<CODE> alert on a rejected save without closing', async () => {
    const create = recordCall('post', '/api/frames', (request) => request.text(), () =>
      fail('VALIDATION'),
    )
    server.use(pagedList('/api/frames', []).handler, programs().handler, create.handler)
    const user = userEvent.setup()
    renderAs(<FramesPage />)
    await screen.findByText('Belum ada frame.')
    const dialog = await openCreateDialog(user)

    uploadFiles([imageFile('piala.png', 'image/png')])
    await within(dialog).findByText('piala.png')
    await user.click(selectByPlaceholder(dialog, 'Pilih minimal satu program.'))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await within(dialog).findByText('Data tidak valid. Periksa kembali isian Anda.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(create.bodies).toHaveLength(1)
  })
})

describe('frame edit and delete', () => {
  it('edits via PATCH multipart: name editable, program_ids always, no image part', async () => {
    const update = recordCall('patch', '/api/frames/1', (request) => request.text())
    server.use(pagedList('/api/frames', [SUDUT]).handler, programs().handler, update.handler)
    const user = userEvent.setup()
    renderAs(<FramesPage />)
    await screen.findByText('Sudut Kota')

    await user.click(within(rowOf('Sudut Kota')).getByRole('button', { name: /Ubah/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('Nama frame')).toHaveValue('Sudut Kota') // stored entry never re-prefills

    await user.clear(within(dialog).getByLabelText('Nama frame'))
    await user.type(within(dialog).getByLabelText('Nama frame'), 'Sudut Kota Baru')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(update.bodies).toHaveLength(1))
    const body = multipartText(update.bodies[0])
    expect(body).toContain('name="name"\n\nSudut Kota Baru')
    expect(fieldCount(body, 'program_ids')).toBe(1) // replaced wholesale → always sent
    expect(body).toContain('name="program_ids"\n\n1')
    expect(fieldCount(body, 'image')).toBe(0) // stored image untouched
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await expectToast('Frame “Sudut Kota Baru” disimpan')
  })

  it('delete is gated on the typed name and only then DELETEs + refetches', async () => {
    const remove = recordCall('delete', '/api/frames/1', async () => null, () =>
      ok({ deleted: true }),
    )
    const list = pagedList('/api/frames', [SUDUT, ROMBAK])
    server.use(list.handler, programs().handler, remove.handler)
    const user = userEvent.setup()
    renderAs(<FramesPage />)
    await screen.findByText('Sudut Kota')
    const initialFetches = list.seen.length

    await user.click(within(rowOf('Sudut Kota')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('Hapus Sudut Kota?')
    await within(dialog).findByText('Ketik Sudut Kota untuk mengonfirmasi.')

    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeDisabled()
    const typed = within(dialog).getByRole('textbox')
    await user.type(typed, 'Sudut Kota lain')
    expect(confirm).toBeDisabled()
    expect(remove.bodies).toHaveLength(0)

    await user.clear(typed)
    await user.type(typed, 'Sudut Kota')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(remove.bodies).toHaveLength(1))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Frame “Sudut Kota” dihapus')
  })

  it('a failed DELETE toasts errors.<CODE> and keeps the dialog open', async () => {
    const remove = recordCall('delete', '/api/frames/1', async () => null, () =>
      fail('NOT_FOUND'),
    )
    server.use(pagedList('/api/frames', [SUDUT]).handler, programs().handler, remove.handler)
    const user = userEvent.setup()
    renderAs(<FramesPage />)
    await screen.findByText('Sudut Kota')

    await user.click(within(rowOf('Sudut Kota')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Sudut Kota')
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
