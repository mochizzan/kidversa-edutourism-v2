/**
 * Activities master (spec F-04 / 02§3 decision 25) — behavior tests:
 * server-paged list cursor contract (default sort_order asc; search/sort/
 * program+topic filters keep the query and rewind to page=1), the program
 * filter clearing the topic filter and re-requesting topic options with
 * `program_id`, the `activity.changed` SSE refetch of the SAME query through
 * the real StreamGate, the JSON create/edit wire payloads (sort_order key
 * only when a number was typed, program_id/topic_id never on PATCH, both
 * Selects locked on edit) and the typed-text delete gate with
 * `errors.<CODE>` toasts.
 */
import { describe, expect, it } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { server } from '../../test/setup'
import { fail, sseChannel } from '../../test/handlers'
import { expectToast } from '../../test/toast'
import StreamGate from '../../components/StreamGate'
import ActivitiesPage from './ActivitiesPage'
import {
  activityRow,
  failFirstThenList,
  findOption,
  pagedList,
  programRow,
  queryOf,
  recordCall,
  renderAs,
  topicRow,
} from '../../test/helpers.cov-master'

const PROG1 = programRow(1, 'Eksplorasi Kota')
const PROG2 = programRow(2, 'Petualangan Alam')
const TOPIC = topicRow(2, 'Batik Nusantara')
const TOPIC2 = topicRow(3, 'Angklung', { program_id: 2, program_name: 'Petualangan Alam' })
const ACT = activityRow(1, 'Kegiatan Baru')
const ACT2 = activityRow(2, 'Membatik Bersama', { indicator: null })

type Json = Record<string, unknown>

/** The table row whose cell contains `name` (row-scoped button queries). */
function rowOf(name: string): HTMLElement {
  const cell = screen.getByText(name)
  const row = cell.closest('tr')
  if (!row) throw new Error(`no table row containing "${name}"`)
  return row
}

async function openCreateDialog(user: UserEvent): Promise<HTMLElement> {
  await user.click(screen.getByRole('button', { name: /Buat kegiatan/ }))
  return screen.findByRole('dialog')
}

/** Inline validation marker on a Form.Item label. */
function fieldItem(dialog: HTMLElement, label: string): Element | null {
  return within(dialog)
    .getByLabelText(label)
    .closest('.ant-form-item')
}

/**
 * Clickable `.ant-select-selector` box of a page filter Select, found via
 * its placeholder span (rc-select renders the placeholder with
 * `pointer-events: none`). Scoped by class: the table headers share the same
 * label texts, so a screen-level getByText would hit both.
 */
function filterSelect(placeholder: string): HTMLElement {
  for (const span of document.querySelectorAll<HTMLElement>('.ant-select-selection-placeholder')) {
    if (span.textContent === placeholder) {
      const box = span.closest('.ant-select-selector')
      if (box instanceof HTMLElement) return box
    }
  }
  throw new Error(`no filter select with placeholder "${placeholder}"`)
}

/** Clickable `.ant-select-selector` box of the labelled Select in `dialog`. */
function formSelect(dialog: HTMLElement, label: string): HTMLElement {
  const box = within(dialog)
    .getByLabelText(label)
    .closest('.ant-select-selector')
  if (!(box instanceof HTMLElement)) throw new Error(`no form select "${label}"`)
  return box
}

/** Option handlers for the two pickers every masters page fetches. */
function optionHandlers() {
  return [
    pagedList('/api/programs', [PROG1, PROG2]).handler,
    pagedList('/api/topics', [TOPIC, TOPIC2]).handler,
  ]
}

describe('activity list cursor', () => {
  it('renders cells under the default sort_order asc cursor', async () => {
    const list = pagedList('/api/activities', [ACT, ACT2])
    server.use(list.handler, ...optionHandlers())
    renderAs(<ActivitiesPage />)

    expect(await screen.findByText('Kegiatan Baru')).toBeInTheDocument()
    expect(screen.getByText('Membatik Bersama')).toBeInTheDocument()
    expect(within(rowOf('Kegiatan Baru')).getByText('Eksplorasi Kota')).toBeInTheDocument()
    expect(within(rowOf('Kegiatan Baru')).getByText('Batik Nusantara')).toBeInTheDocument()
    expect(within(rowOf('Kegiatan Baru')).getByText('Menyebutkan 3 motif')).toBeInTheDocument()
    expect(within(rowOf('Membatik Bersama')).getByText('—')).toBeInTheDocument() // null indicator

    expect(list.seen[0].searchParams.get('page')).toBe('1')
    expect(list.seen[0].searchParams.get('per_page')).toBe('20')
    expect(list.seen[0].searchParams.get('sort')).toBe('sort_order')
    expect(list.seen[0].searchParams.get('dir')).toBe('asc')
    expect(list.seen[0].searchParams.has('program_id')).toBe(false)
    expect(list.seen[0].searchParams.has('topic_id')).toBe(false)
    expect(list.seen[0].searchParams.has('q')).toBe(false)
  })

  it('shows the section empty text when the server returns no rows', async () => {
    server.use(pagedList('/api/activities', []).handler, ...optionHandlers())
    renderAs(<ActivitiesPage />)

    expect(await screen.findByText('Belum ada kegiatan untuk topik ini.')).toBeInTheDocument()
  })

  it('search rewinds to page=1 keeping sort, and clearing drops q again', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/activities', [ACT], 40)
    server.use(list.handler, ...optionHandlers())
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    await user.type(screen.getByPlaceholderText('Cari nama kegiatan'), 'batik{Enter}')
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('q')).toBe('batik'))
    const searched = list.seen.at(-1) as URL
    expect(searched.searchParams.get('page')).toBe('1') // any filter change rewinds (02§3)
    expect(searched.searchParams.get('sort')).toBe('sort_order')
    expect(searched.searchParams.get('dir')).toBe('asc')

    await user.clear(screen.getByPlaceholderText('Cari nama kegiatan'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.has('q')).toBe(false))
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')
  })

  it('Urutan and Nama kegiatan headers cycle asc → desc → default, rewinding the page', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/activities', [ACT], 40)
    server.use(list.handler, ...optionHandlers())
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    const orderHeader = screen.getByRole('columnheader', { name: 'Urutan' })
    const nameHeader = screen.getByRole('columnheader', { name: 'Nama kegiatan' })

    // default sort_order asc → first click on Urutan flips it to desc
    await user.click(orderHeader)
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('desc'))
    expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('sort_order')
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')
    expect(orderHeader).toHaveAttribute('aria-sort', 'descending')

    // second click cancels → DEFAULT_SORT (sort_order asc)
    await user.click(orderHeader)
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('dir')).toBe('asc'))
    expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('sort_order')

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
    expect(orderHeader).toHaveAttribute('aria-sort', 'ascending')
  })

  it('paging changes only the page cursor', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/activities', [ACT], 40)
    server.use(list.handler, ...optionHandlers())
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen).toHaveLength(2))
    expect(list.seen[1].searchParams.get('page')).toBe('2')
    expect(queryOf(list.seen[1])).toBe(queryOf(list.seen[0]).replace('page=1', 'page=2'))
  })

  it('program filter sets program_id, clears topic_id and refetches topic options', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/activities', [ACT], 40)
    const topics = pagedList('/api/topics', [TOPIC, TOPIC2])
    server.use(
      list.handler,
      pagedList('/api/programs', [PROG1, PROG2]).handler,
      topics.handler,
    )
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    await user.click(filterSelect('Semua topik'))
    await user.click(await findOption('Batik Nusantara'))
    await waitFor(() => {
      const last = list.seen.at(-1) as URL
      expect(last.searchParams.get('topic_id')).toBe('2')
      expect(last.searchParams.get('page')).toBe('1') // filter change rewinds (02§3)
    })

    await user.click(filterSelect('Semua program'))
    await user.click(await findOption('Eksplorasi Kota'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('program_id')).toBe('1'))
    const last = list.seen.at(-1) as URL
    expect(last.searchParams.get('topic_id')).toBeNull() // program pick clears the topic filter
    expect(last.searchParams.get('page')).toBe('1')

    // the page-level topic option list re-requests with the chosen program
    await waitFor(() =>
      expect(topics.seen.some((url) => url.searchParams.get('program_id') === '1')).toBe(true),
    )

    // the create dialog prefills the active program filter
    const dialog = await openCreateDialog(user)
    await within(dialog).findByText('Eksplorasi Kota')
    await user.click(within(dialog).getByRole('button', { name: 'Batal' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('renders the errors.<CODE> alert on a failed fetch and recovers on retry', async () => {
    const user = userEvent.setup()
    const list = failFirstThenList('/api/activities', 'DB_UNAVAILABLE', [ACT])
    server.use(list.handler, ...optionHandlers())
    renderAs(<ActivitiesPage />)

    expect(await screen.findByText('Layanan sedang sibuk. Coba lagi.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    expect(await screen.findByText('Kegiatan Baru')).toBeInTheDocument()
    expect(screen.queryByText('Layanan sedang sibuk. Coba lagi.')).toBeNull()
    expect(list.seen).toHaveLength(2)
  })
})

describe('activity signal refetch', () => {
  it('an activity.changed burst refetches the identical query once; stale rev dropped', async () => {
    const user = userEvent.setup()
    const channel = sseChannel('/api/stream')
    const list = pagedList('/api/activities', [ACT], 40)
    server.use(channel.handler, list.handler, ...optionHandlers())
    renderAs(
      <>
        <StreamGate />
        <ActivitiesPage />
      </>,
      { route: '/activities' },
    )

    await waitFor(() => expect(channel.handshakes).toBe(1))
    await screen.findByText('Kegiatan Baru')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))
    const before = (list.seen.at(-1) as URL).search
    const count = list.seen.length

    channel.push('event: activity.changed\ndata: {"rev":1}\n\n')
    channel.push('event: activity.changed\ndata: {"rev":2}\n\n')
    await waitFor(() => expect(list.seen).toHaveLength(count + 1), { timeout: 3000 })
    expect((list.seen.at(-1) as URL).search).toBe(before) // same page + filters

    channel.push('event: activity.changed\ndata: {"rev":1}\n\n') // rev ≤ last_seen
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 500)
    await promise
    expect(list.seen).toHaveLength(count + 1)
  })
})

describe('activity create form', () => {
  it('blocks missing fields with no POST, then creates JSON without sort_order', async () => {
    const create = recordCall<Json>('post', '/api/activities', (request) => request.json())
    const list = pagedList('/api/activities', [ACT])
    server.use(list.handler, ...optionHandlers(), create.handler)
    const user = userEvent.setup()
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')
    const initialFetches = list.seen.length

    const dialog = await openCreateDialog(user)
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(fieldItem(dialog, 'Program')).toHaveClass('ant-form-item-has-error'))
    expect(fieldItem(dialog, 'Topik')).toHaveClass('ant-form-item-has-error')
    expect(fieldItem(dialog, 'Nama kegiatan')).toHaveClass('ant-form-item-has-error')
    expect(create.bodies).toHaveLength(0) // required rules fired client-side

    await user.click(formSelect(dialog, 'Program'))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.click(formSelect(dialog, 'Topik'))
    await user.click(await findOption('Batik Nusantara'))
    await user.type(within(dialog).getByLabelText('Nama kegiatan'), 'Kegiatan Baru Baru')
    await user.type(within(dialog).getByLabelText('Indikator'), 'Menampilkan 6 karya')

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(create.bodies).toHaveLength(1))
    expect(create.bodies[0]).toMatchObject({
      program_id: 1,
      topic_id: 2,
      name: 'Kegiatan Baru Baru',
      indicator: 'Menampilkan 6 karya',
    })
    expect('sort_order' in create.bodies[0]).toBe(false) // left empty → key omitted

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Kegiatan “Kegiatan Baru Baru” dibuat')
  })

  it('sends sort_order as a number when the Urutan field is filled', async () => {
    const create = recordCall<Json>('post', '/api/activities', (request) => request.json())
    server.use(pagedList('/api/activities', [ACT]).handler, ...optionHandlers(), create.handler)
    const user = userEvent.setup()
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')

    const dialog = await openCreateDialog(user)
    await user.click(formSelect(dialog, 'Program'))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.click(formSelect(dialog, 'Topik'))
    await user.click(await findOption('Batik Nusantara'))
    await user.type(within(dialog).getByLabelText('Nama kegiatan'), 'Kegiatan Urutan')
    await user.type(within(dialog).getByLabelText('Urutan'), '3')

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(create.bodies).toHaveLength(1))
    expect('sort_order' in create.bodies[0]).toBe(true)
    expect(create.bodies[0].sort_order).toBe(3)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await expectToast('Kegiatan “Kegiatan Urutan” dibuat')
  })

  it('shows the inline errors.<CODE> alert for a rejected save without closing', async () => {
    const create = recordCall<Json>('post', '/api/activities', (request) => request.json(), () =>
      fail('VALIDATION'),
    )
    server.use(pagedList('/api/activities', [ACT]).handler, ...optionHandlers(), create.handler)
    const user = userEvent.setup()
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')

    const dialog = await openCreateDialog(user)
    await user.click(formSelect(dialog, 'Program'))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.click(formSelect(dialog, 'Topik'))
    await user.click(await findOption('Batik Nusantara'))
    await user.type(within(dialog).getByLabelText('Nama kegiatan'), 'Kegiatan Tolak')

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await within(dialog).findByText('Data tidak valid. Periksa kembali isian Anda.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(create.bodies).toHaveLength(1)
  })
})

describe('activity edit and delete', () => {
  it('edits via PATCH JSON with locked Selects and never program_id/topic_id', async () => {
    const update = recordCall<Json>('patch', '/api/activities/1', (request) => request.json())
    const list = pagedList('/api/activities', [ACT])
    server.use(list.handler, ...optionHandlers(), update.handler)
    const user = userEvent.setup()
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')
    const initialFetches = list.seen.length

    // fireEvent: under coverage load a table refetch can briefly blur the row
    // (rc-spin sets pointer-events:none on the container), which trips
    // userEvent's pointer assertion — the label click itself is identical.
    fireEvent.click(within(rowOf('Kegiatan Baru')).getByRole('button', { name: /Ubah/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('Nama kegiatan')).toHaveValue('Kegiatan Baru')
    expect(within(dialog).getByLabelText('Indikator')).toHaveValue('Menyebutkan 3 motif')
    expect(within(dialog).getByLabelText('Program')).toBeDisabled() // immutable after create
    expect(within(dialog).getByLabelText('Topik')).toBeDisabled()

    await user.clear(within(dialog).getByLabelText('Nama kegiatan'))
    await user.type(within(dialog).getByLabelText('Nama kegiatan'), 'Kegiatan Baru 2')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(update.bodies).toHaveLength(1))
    expect(update.bodies[0]).toMatchObject({
      name: 'Kegiatan Baru 2',
      indicator: 'Menyebutkan 3 motif',
      sort_order: 1,
    })
    expect('program_id' in update.bodies[0]).toBe(false)
    expect('topic_id' in update.bodies[0]).toBe(false)

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Kegiatan “Kegiatan Baru 2” disimpan')
  })

  it('delete is gated on the typed name and only then DELETEs + refetches', async () => {
    const remove = recordCall('delete', '/api/activities/1', async () => null)
    const list = pagedList('/api/activities', [ACT])
    server.use(list.handler, ...optionHandlers(), remove.handler)
    const user = userEvent.setup()
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')
    const initialFetches = list.seen.length

    await user.click(within(rowOf('Kegiatan Baru')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('Hapus Kegiatan Baru?')
    await within(dialog).findByText('Ketik Kegiatan Baru untuk mengonfirmasi.')

    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeDisabled()
    const typed = within(dialog).getByRole('textbox')
    await user.type(typed, 'Kegiatan Baru lain')
    expect(confirm).toBeDisabled()
    expect(remove.bodies).toHaveLength(0)

    await user.clear(typed)
    await user.type(typed, 'Kegiatan Baru')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(remove.bodies).toHaveLength(1))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Kegiatan “Kegiatan Baru” dihapus')
  })

  it('a failed DELETE toasts errors.<CODE> and keeps the dialog open', async () => {
    const remove = recordCall('delete', '/api/activities/1', async () => null, () =>
      fail('NOT_FOUND'),
    )
    server.use(pagedList('/api/activities', [ACT]).handler, ...optionHandlers(), remove.handler)
    const user = userEvent.setup()
    renderAs(<ActivitiesPage />)
    await screen.findByText('Kegiatan Baru')

    await user.click(within(rowOf('Kegiatan Baru')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Kegiatan Baru')
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
