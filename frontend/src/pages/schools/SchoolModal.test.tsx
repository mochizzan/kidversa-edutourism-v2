/**
 * SchoolModal (F-02 create/edit): create defaults (Country Indonesia, blank
 * City, active), the required-name mirror blocking any POST, edit prefill +
 * PATCH by id, the pending state (loading OK / disabled cancel), the generic
 * inline alert for failures, and a clean re-open after cancel (no leftover
 * error, no leftover value).
 */
import { useState } from 'react'
import { describe, expect, it, vi, type Mock } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http } from 'msw'
import SchoolModal from './SchoolModal'
import type { SchoolRow } from './SchoolsPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, ok } from '../../test/handlers'

const SCHOOL: SchoolRow = {
  id: 7,
  school_name: 'Semarang',
  country: 'Indonesia',
  city: null,
  is_active: false,
  created_at: '2026-01-15T00:00:00Z',
}

/** Spies the modal reports its lifecycle through (types owned by vitest). */
interface ModalSpies {
  onClose: Mock
  onSaved: Mock
}

function renderCreate(): ModalSpies {
  const onClose = vi.fn()
  const onSaved = vi.fn()
  renderApp(<SchoolModal open school={null} onClose={onClose} onSaved={onSaved} />)
  return { onClose, onSaved }
}

function renderEdit(school: SchoolRow): ModalSpies {
  const onClose = vi.fn()
  const onSaved = vi.fn()
  renderApp(<SchoolModal open school={school} onClose={onClose} onSaved={onSaved} />)
  return { onClose, onSaved }
}

/** Drives `open` from outside so the test can cancel and reopen the modal. */
function ReopenHarness({ onClose, onSaved }: ModalSpies) {
  const [open, setOpen] = useState(true)
  return (
    <>
      <button type="button" onClick={() => setOpen(true)}>
        Buka lagi
      </button>
      <SchoolModal
        open={open}
        school={null}
        onClose={() => {
          onClose()
          setOpen(false)
        }}
        onSaved={onSaved}
      />
    </>
  )
}

async function submitEmptyName(user: UserEvent): Promise<HTMLElement> {
  const dialog = await screen.findByRole('dialog')
  await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
  await within(dialog).findByText('Wajib diisi.')
  return dialog
}

describe('school modal create mode', () => {
  it('opens with the create title, Country Indonesia, blank City and active status defaults', async () => {
    renderCreate()
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Sekolah baru')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Nama sekolah')).toHaveValue('')
    expect(within(dialog).getByLabelText('Negara')).toHaveValue('Indonesia')
    expect(within(dialog).getByLabelText('Kota')).toHaveValue('')
    expect(within(dialog).getByRole('switch')).toBeChecked() // status default on
  })

  it('blocks an empty name with no POST', async () => {
    let posts = 0
    server.use(
      http.post('/api/schools', () => {
        posts += 1
        return ok({ school: SCHOOL })
      }),
    )
    const user = userEvent.setup()
    const { onSaved } = renderCreate()

    const dialog = await submitEmptyName(user)
    expect(posts).toBe(0)
    expect(onSaved).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Nama sekolah')).toHaveValue('')
  })

  it('posts the form values and reports onSaved', async () => {
    let posts = 0
    let payload: unknown = null
    server.use(
      http.post('/api/schools', async ({ request }) => {
        payload = await request.json()
        posts += 1
        return ok({ school: { ...SCHOOL, id: 9, school_name: 'Depok' } })
      }),
    )
    const user = userEvent.setup()
    const { onClose, onSaved } = renderCreate()

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Nama sekolah'), 'Depok')
    await user.type(within(dialog).getByLabelText('Kota'), 'Jawa Barat')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(posts).toBe(1))
    expect(payload).toEqual({
      school_name: 'Depok',
      country: 'Indonesia',
      city: 'Jawa Barat',
      is_active: true,
    })
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('cancel calls onClose without any request', async () => {
    let posts = 0
    server.use(
      http.post('/api/schools', () => {
        posts += 1
        return ok({ school: SCHOOL })
      }),
    )
    const user = userEvent.setup()
    const { onClose, onSaved } = renderCreate()

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Nama sekolah'), 'Depok')
    await user.click(within(dialog).getByRole('button', { name: 'Batal' }))

    expect(onClose).toHaveBeenCalledTimes(1)
    expect(onSaved).not.toHaveBeenCalled()
    expect(posts).toBe(0)
  })
})

describe('school modal edit mode', () => {
  it('prefills the row and PATCHes by id', async () => {
    let patches = 0
    let payload: unknown = null
    server.use(
      http.patch('/api/schools/7', async ({ request }) => {
        payload = await request.json()
        patches += 1
        return ok({ school: SCHOOL })
      }),
    )
    const user = userEvent.setup()
    const { onClose, onSaved } = renderEdit(SCHOOL)

    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Ubah sekolah')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Nama sekolah')).toHaveValue('Semarang')
    expect(within(dialog).getByLabelText('Negara')).toHaveValue('Indonesia')
    expect(within(dialog).getByLabelText('Kota')).toHaveValue('') // null city → blank
    expect(within(dialog).getByRole('switch')).not.toBeChecked()

    await user.clear(within(dialog).getByLabelText('Nama sekolah'))
    await user.type(within(dialog).getByLabelText('Nama sekolah'), 'Semarang Baru')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(patches).toBe(1))
    expect(payload).toEqual({
      school_name: 'Semarang Baru',
      country: 'Indonesia',
      city: '',
      is_active: false,
    })
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
    expect(onClose).not.toHaveBeenCalled()
  })

  it('keeps the OK button loading and the cancel disabled while the save is pending', async () => {
    let started = false
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    server.use(
      http.post('/api/schools', async () => {
        started = true
        await gate
        return ok({ school: { ...SCHOOL, id: 9, school_name: 'Depok' } })
      }),
    )
    const user = userEvent.setup()
    const { onSaved } = renderCreate()

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Nama sekolah'), 'Depok')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(started).toBe(true))

    expect(within(dialog).getByRole('button', { name: /Simpan/ })).toHaveClass('ant-btn-loading')
    expect(within(dialog).getByRole('button', { name: 'Batal' })).toBeDisabled()
    expect(onSaved).not.toHaveBeenCalled()

    release()
    await waitFor(() => expect(onSaved).toHaveBeenCalledTimes(1))
  })

  it('shows the generic inline alert for a VALIDATION failure', async () => {
    let patches = 0
    server.use(
      http.patch('/api/schools/7', () => {
        patches += 1
        return fail('VALIDATION')
      }),
    )
    const user = userEvent.setup()
    const { onSaved } = renderEdit(SCHOOL)

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Nama sekolah'), '!')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent('Data tidak valid. Periksa kembali isian Anda.')
    expect(patches).toBe(1)
    expect(onSaved).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})

describe('school modal failure and reopen', () => {
  it('shows the translated inline alert for a non-VALIDATION failure', async () => {
    let patches = 0
    server.use(
      http.patch('/api/schools/7', () => {
        patches += 1
        return fail('DB_UNAVAILABLE')
      }),
    )
    const user = userEvent.setup()
    const { onSaved } = renderEdit(SCHOOL)

    const dialog = await screen.findByRole('dialog')
    await user.type(within(dialog).getByLabelText('Nama sekolah'), 'Semarang Baru')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await within(dialog).findByText('Layanan sedang sibuk. Coba lagi.')
    expect(patches).toBe(1)
    expect(onSaved).not.toHaveBeenCalled()
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('reopening after cancel starts from a clean field state', async () => {
    let posts = 0
    server.use(
      http.post('/api/schools', () => {
        posts += 1
        return ok({ school: SCHOOL })
      }),
    )
    const user = userEvent.setup()
    const onClose = vi.fn()
    const onSaved = vi.fn()
    renderApp(<ReopenHarness onClose={onClose} onSaved={onSaved} />)

    const dialog = await submitEmptyName(user)
    await user.click(within(dialog).getByRole('button', { name: 'Batal' }))
    expect(onClose).toHaveBeenCalledTimes(1)
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), { timeout: 10_000 })

    await user.click(screen.getByRole('button', { name: 'Buka lagi' }))
    const reopened = await screen.findByRole('dialog')
    expect(within(reopened).queryByText('Wajib diisi.')).toBeNull()
    expect(within(reopened).getByLabelText('Nama sekolah')).toHaveValue('')

    await user.type(within(reopened).getByLabelText('Nama sekolah'), 'Samarinda')
    await user.click(within(reopened).getByRole('button', { name: 'Batal' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull(), { timeout: 10_000 })

    await user.click(screen.getByRole('button', { name: 'Buka lagi' }))
    const reopened2 = await screen.findByRole('dialog')
    expect(within(reopened2).getByLabelText('Nama sekolah')).toHaveValue('')
    expect(within(reopened2).queryByText('Wajib diisi.')).toBeNull()
    expect(onClose).toHaveBeenCalledTimes(2)
    expect(posts).toBe(0)
  })
})
