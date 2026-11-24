/**
 * Mission bank master (spec F-05 / 02§3 decision 25) — behavior tests:
 * server-paged list cursor contract (default id asc; search/sort/program+
 * topic filters keep the query and rewind to page=1), the program filter
 * clearing the topic filter, the `mission.changed` SSE refetch of the SAME
 * query through the real StreamGate, the cross-program topic picker (all
 * school topics labelled "<topic> · <program>", ≥1 entry required with the
 * `Pilih minimal satu topik.` hint), the JSON create/edit wire payloads
 * (program_id IS editable on PATCH, topic_ids replaces the link set) and the
 * typed-text delete gate with `errors.<CODE>` toasts.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http } from 'msw'
import { server } from '../../test/setup'
import { fail, sseChannel } from '../../test/handlers'
import { expectToast } from '../../test/toast'
import StreamGate from '../../components/StreamGate'
import MissionsPage from './MissionsPage'
import {
  failFirstThenList,
  findOption,
  missionRow,
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
const MISSION = missionRow(1, 'Misi Baru')
const MISSION2 = missionRow(2, 'Misi Kosong', { body: null, topics: [] })

type Json = Record<string, unknown>

/** The table row whose cell contains `name` (row-scoped button queries). */
function rowOf(name: string): HTMLElement {
  const cell = screen.getByText(name)
  const row = cell.closest('tr')
  if (!row) throw new Error(`no table row containing "${name}"`)
  return row
}

async function openCreateDialog(user: UserEvent): Promise<HTMLElement> {
  await user.click(screen.getByRole('button', { name: /Buat misi/ }))
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

/**
 * Collapse an open picker popup by re-clicking its trigger — rc-select
 * closes multi-selects on Escape via `event.which`, which user-event and
 * jsdom never set, so the toggle path is the reliable one in tests.
 */
async function collapsePicker(user: UserEvent, dialog: HTMLElement, label: string): Promise<void> {
  await user.click(formSelect(dialog, label))
  await waitFor(() =>
    expect(document.querySelectorAll('.ant-select-dropdown:not(.ant-select-dropdown-hidden)')).toHaveLength(0),
  )
}

/** Clickable remove icon of the selected topic tag inside `dialog`. */
function tagRemove(dialog: HTMLElement, label: string): HTMLElement {
  const content = [...dialog.querySelectorAll<HTMLElement>('.ant-select-selection-item-content')].find(
    (element) => element.textContent === label,
  )
  if (!content) throw new Error(`no selected tag "${label}"`)
  const remove = content
    .closest('.ant-select-selection-item')
    ?.querySelector<HTMLElement>('.ant-select-selection-item-remove')
  if (!remove) throw new Error(`no remove icon for tag "${label}"`)
  return remove
}

/** Option handlers for the two pickers every masters page fetches. */
function optionHandlers() {
  return [
    pagedList('/api/programs', [PROG1, PROG2]).handler,
    pagedList('/api/topics', [TOPIC, TOPIC2]).handler,
  ]
}

describe('mission list cursor', () => {
  it('renders cells under the default id asc cursor', async () => {
    const list = pagedList('/api/missions', [MISSION, MISSION2])
    server.use(list.handler, ...optionHandlers())
    renderAs(<MissionsPage />)

    expect(await screen.findByText('Misi Baru')).toBeInTheDocument()
    expect(screen.getByText('Misi Baru — isi misi')).toBeInTheDocument()
    expect(within(rowOf('Misi Baru')).getByText('Eksplorasi Kota')).toBeInTheDocument() // program cell
    expect(within(rowOf('Misi Baru')).getByText('Batik Nusantara')).toBeInTheDocument() // topic tag
    expect(screen.getByText('Misi Kosong')).toBeInTheDocument()
    // null body + no topic links both render the dash
    expect(within(rowOf('Misi Kosong')).getAllByText('—')).toHaveLength(2)

    expect(list.seen[0].searchParams.get('page')).toBe('1')
    expect(list.seen[0].searchParams.get('per_page')).toBe('20')
    expect(list.seen[0].searchParams.get('sort')).toBe('id')
    expect(list.seen[0].searchParams.get('dir')).toBe('asc')
    expect(list.seen[0].searchParams.has('program_id')).toBe(false)
    expect(list.seen[0].searchParams.has('topic_id')).toBe(false)
    expect(list.seen[0].searchParams.has('q')).toBe(false)
  })

  it('shows the section empty text when the server returns no rows', async () => {
    server.use(pagedList('/api/missions', []).handler, ...optionHandlers())
    renderAs(<MissionsPage />)

    expect(await screen.findByText('Belum ada misi di bank misi.')).toBeInTheDocument()
  })

  it('a failed topic-options picker fetch stays silent; the list still renders', async () => {
    // useTopicOptions is best-effort — its rejection must not raise the
    // errors.<CODE> alert (only the page's own /api/missions fetch may).
    server.use(
      pagedList('/api/missions', [MISSION]).handler,
      pagedList('/api/programs', [PROG1, PROG2]).handler,
      http.get('/api/topics', () => fail('DB_UNAVAILABLE')),
    )
    renderAs(<MissionsPage />)

    expect(await screen.findByText('Misi Baru')).toBeInTheDocument()
    expect(screen.queryByText('Layanan sedang sibuk. Coba lagi.')).toBeNull()
    // The create dialog's cross-program topic picker has nothing to offer.
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Buat misi/ }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getAllByRole('combobox').at(-1) as HTMLElement)
    await waitFor(() =>
      expect(
        document.querySelectorAll('.ant-select-dropdown .ant-select-item-option'),
      ).toHaveLength(0),
    )
  })

  it('search rewinds to page=1 keeping sort, and clearing drops q again', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/missions', [MISSION], 40)
    server.use(list.handler, ...optionHandlers())
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    await user.type(screen.getByPlaceholderText('Cari nama misi'), 'batik{Enter}')
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('q')).toBe('batik'))
    const searched = list.seen.at(-1) as URL
    expect(searched.searchParams.get('page')).toBe('1') // any filter change rewinds (02§3)
    expect(searched.searchParams.get('sort')).toBe('id')
    expect(searched.searchParams.get('dir')).toBe('asc')

    await user.clear(screen.getByPlaceholderText('Cari nama misi'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.has('q')).toBe(false))
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')
  })

  it('Nama misi header cycles asc → desc → default, rewinding the page', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/missions', [MISSION], 40)
    server.use(list.handler, ...optionHandlers())
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    const nameHeader = screen.getByRole('columnheader', { name: 'Nama misi' })
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
    const list = pagedList('/api/missions', [MISSION], 40)
    server.use(list.handler, ...optionHandlers())
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen).toHaveLength(2))
    expect(list.seen[1].searchParams.get('page')).toBe('2')
    expect(queryOf(list.seen[1])).toBe(queryOf(list.seen[0]).replace('page=1', 'page=2'))
  })

  it('program filter sets program_id, clears topic_id and refetches topic options', async () => {
    const user = userEvent.setup()
    const list = pagedList('/api/missions', [MISSION], 40)
    const topics = pagedList('/api/topics', [TOPIC, TOPIC2])
    server.use(
      list.handler,
      pagedList('/api/programs', [PROG1, PROG2]).handler,
      topics.handler,
    )
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')

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
    const list = failFirstThenList('/api/missions', 'DB_UNAVAILABLE', [MISSION])
    server.use(list.handler, ...optionHandlers())
    renderAs(<MissionsPage />)

    expect(await screen.findByText('Layanan sedang sibuk. Coba lagi.')).toBeInTheDocument()
    expect(screen.queryByRole('table')).toBeNull()

    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    expect(await screen.findByText('Misi Baru')).toBeInTheDocument()
    expect(screen.queryByText('Layanan sedang sibuk. Coba lagi.')).toBeNull()
    expect(list.seen).toHaveLength(2)
  })
})

describe('mission signal refetch', () => {
  it('a mission.changed burst refetches the identical query once; stale rev dropped', async () => {
    const user = userEvent.setup()
    const channel = sseChannel('/api/stream')
    const list = pagedList('/api/missions', [MISSION], 40)
    server.use(channel.handler, list.handler, ...optionHandlers())
    renderAs(
      <>
        <StreamGate />
        <MissionsPage />
      </>,
      { route: '/missions' },
    )

    await waitFor(() => expect(channel.handshakes).toBe(1))
    await screen.findByText('Misi Baru')
    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))
    const before = (list.seen.at(-1) as URL).search
    const count = list.seen.length

    channel.push('event: mission.changed\ndata: {"rev":1}\n\n')
    channel.push('event: mission.changed\ndata: {"rev":2}\n\n')
    await waitFor(() => expect(list.seen).toHaveLength(count + 1), { timeout: 3000 })
    expect((list.seen.at(-1) as URL).search).toBe(before) // same page + filters

    channel.push('event: mission.changed\ndata: {"rev":1}\n\n') // rev ≤ last_seen
    const { promise, resolve } = Promise.withResolvers<void>()
    setTimeout(resolve, 500)
    await promise
    expect(list.seen).toHaveLength(count + 1)
  })
})

describe('mission create form', () => {
  it('blocks missing fields with no POST, then creates with topic_ids', async () => {
    const create = recordCall<Json>('post', '/api/missions', (request) => request.json())
    const list = pagedList('/api/missions', [MISSION])
    server.use(list.handler, ...optionHandlers(), create.handler)
    const user = userEvent.setup()
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')
    const initialFetches = list.seen.length

    const dialog = await openCreateDialog(user)
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(fieldItem(dialog, 'Program')).toHaveClass('ant-form-item-has-error'))
    expect(fieldItem(dialog, 'Nama misi')).toHaveClass('ant-form-item-has-error')
    expect(fieldItem(dialog, 'Isi misi')).toHaveClass('ant-form-item-has-error')
    expect(fieldItem(dialog, 'Tautan topik')).toHaveClass('ant-form-item-has-error')
    expect(create.bodies).toHaveLength(0) // required rules fired client-side

    await user.click(formSelect(dialog, 'Program'))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.type(within(dialog).getByLabelText('Nama misi'), 'Misi Baru Baru')
    await user.type(within(dialog).getByLabelText('Isi misi'), 'Kumpulkan 5 karya')

    // the picker carries ALL school topics labelled "<topic> · <program>"
    await user.click(formSelect(dialog, 'Tautan topik'))
    expect(await findOption('Batik Nusantara · Eksplorasi Kota')).toBeInTheDocument()
    expect(await findOption('Angklung · Petualangan Alam')).toBeInTheDocument()
    await user.click(await findOption('Batik Nusantara · Eksplorasi Kota'))
    await collapsePicker(user, dialog, 'Tautan topik')

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(create.bodies).toHaveLength(1))
    expect(create.bodies[0]).toEqual({
      program_id: 1,
      name: 'Misi Baru Baru',
      body: 'Kumpulkan 5 karya',
      topic_ids: [2],
    })

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Misi “Misi Baru Baru” dibuat')
  })

  it('blocks an emptied topic picker with Pilih minimal satu topik. and no POST', async () => {
    const create = recordCall<Json>('post', '/api/missions', (request) => request.json())
    server.use(pagedList('/api/missions', [MISSION]).handler, ...optionHandlers(), create.handler)
    const user = userEvent.setup()
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')

    const dialog = await openCreateDialog(user)
    await user.click(formSelect(dialog, 'Program'))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.type(within(dialog).getByLabelText('Nama misi'), 'Misi Tanpa Topik')
    await user.type(within(dialog).getByLabelText('Isi misi'), 'Isi misi tanpa tautan')

    // select a topic, then remove it again → the link set is empty
    await user.click(formSelect(dialog, 'Tautan topik'))
    await user.click(await findOption('Batik Nusantara · Eksplorasi Kota'))
    await collapsePicker(user, dialog, 'Tautan topik')
    await user.click(tagRemove(dialog, 'Batik Nusantara · Eksplorasi Kota'))
    await waitFor(() =>
      expect(
        [...dialog.querySelectorAll('.ant-select-selection-item-content')].some(
          (element) => element.textContent === 'Batik Nusantara · Eksplorasi Kota',
        ),
      ).toBe(false),
    )

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => {
      const explain = fieldItem(dialog, 'Tautan topik')?.querySelector('.ant-form-item-explain')
      expect(explain?.textContent).toContain('Pilih minimal satu topik.')
    })
    expect(create.bodies).toHaveLength(0)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('shows the inline errors.<CODE> alert for a rejected save without closing', async () => {
    const create = recordCall<Json>('post', '/api/missions', (request) => request.json(), () =>
      fail('VALIDATION'),
    )
    server.use(pagedList('/api/missions', [MISSION]).handler, ...optionHandlers(), create.handler)
    const user = userEvent.setup()
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')

    const dialog = await openCreateDialog(user)
    await user.click(formSelect(dialog, 'Program'))
    await user.click(await findOption('Eksplorasi Kota'))
    await user.type(within(dialog).getByLabelText('Nama misi'), 'Misi Tolak')
    await user.type(within(dialog).getByLabelText('Isi misi'), 'Isi misi tolak')
    await user.click(formSelect(dialog, 'Tautan topik'))
    await user.click(await findOption('Batik Nusantara · Eksplorasi Kota'))
    await collapsePicker(user, dialog, 'Tautan topik')

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await within(dialog).findByText('Data tidak valid. Periksa kembali isian Anda.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(create.bodies).toHaveLength(1)
  })
})

describe('mission edit and delete', () => {
  it('edits via PATCH JSON: program_id editable, same four keys, prefilled form', async () => {
    const update = recordCall<Json>('patch', '/api/missions/1', (request) => request.json())
    const list = pagedList('/api/missions', [MISSION])
    server.use(list.handler, ...optionHandlers(), update.handler)
    const user = userEvent.setup()
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')
    const initialFetches = list.seen.length

    await user.click(within(rowOf('Misi Baru')).getByRole('button', { name: /Ubah/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByLabelText('Nama misi')).toHaveValue('Misi Baru')
    expect(within(dialog).getByLabelText('Isi misi')).toHaveValue('Misi Baru — isi misi')
    expect(within(dialog).getByText('Eksplorasi Kota')).toBeInTheDocument() // prefilled program
    expect(within(dialog).getByText('Batik Nusantara · Eksplorasi Kota')).toBeInTheDocument() // topic_ids tag

    // program_id IS editable here (unlike activities/topics)
    await user.click(formSelect(dialog, 'Program'))
    await user.click(await findOption('Petualangan Alam'))
    await user.clear(within(dialog).getByLabelText('Nama misi'))
    await user.type(within(dialog).getByLabelText('Nama misi'), 'Misi Baru 2')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(update.bodies).toHaveLength(1))
    expect(update.bodies[0]).toEqual({
      program_id: 2,
      name: 'Misi Baru 2',
      body: 'Misi Baru — isi misi',
      topic_ids: [2],
    })

    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Misi “Misi Baru 2” disimpan')
  })

  it('delete is gated on the typed name and only then DELETEs + refetches', async () => {
    const remove = recordCall('delete', '/api/missions/1', async () => null)
    const list = pagedList('/api/missions', [MISSION])
    server.use(list.handler, ...optionHandlers(), remove.handler)
    const user = userEvent.setup()
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')
    const initialFetches = list.seen.length

    await user.click(within(rowOf('Misi Baru')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await within(dialog).findByText('Hapus Misi Baru?')
    await within(dialog).findByText('Ketik Misi Baru untuk mengonfirmasi.')

    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeDisabled()
    const typed = within(dialog).getByRole('textbox')
    await user.type(typed, 'Misi Baru lain')
    expect(confirm).toBeDisabled()
    expect(remove.bodies).toHaveLength(0)

    await user.clear(typed)
    await user.type(typed, 'Misi Baru')
    expect(confirm).toBeEnabled()
    await user.click(confirm)

    await waitFor(() => expect(remove.bodies).toHaveLength(1))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
    await waitFor(() => expect(list.seen.length).toBeGreaterThan(initialFetches))
    await expectToast('Misi “Misi Baru” dihapus')
  })

  it('a failed DELETE toasts errors.<CODE> and keeps the dialog open', async () => {
    const remove = recordCall('delete', '/api/missions/1', async () => null, () =>
      fail('NOT_FOUND'),
    )
    server.use(pagedList('/api/missions', [MISSION]).handler, ...optionHandlers(), remove.handler)
    const user = userEvent.setup()
    renderAs(<MissionsPage />)
    await screen.findByText('Misi Baru')

    await user.click(within(rowOf('Misi Baru')).getByRole('button', { name: /Hapus/ }))
    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByRole('textbox'), 'Misi Baru')
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
