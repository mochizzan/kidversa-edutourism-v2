/**
 * User create / edit modal (F-03 / FL-01): ADMIN creates FASILITATOR only
 * (no school field, no schools fetch), SA picks an ACTIVE school and a role,
 * client validation mirrors the server (email / WhatsApp), EMAIL_TAKEN lands
 * on the email field while other codes surface as a modal alert, and edit
 * PATCHes only {name, email, wa_number}.
 */
import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http } from 'msw'
import UserModal, { type UserRow } from './UserModal'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, makeUser, ok, paged } from '../../test/handlers'
import { expectToast } from '../../test/toast'

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })
/** SA with no ACTIVE school: the required school rule must be reachable. */
const SA_NO_SCHOOL_SESSION = () =>
  makeLoginData({ user: makeUser({ role: 'SA' }), school: null })

const EDIT_USER: UserRow = {
  id: 3,
  name: 'Siti Aminah',
  email: 'siti@Kidversa.ID',
  role: 'FASILITATOR',
  decision: 'APPROVED',
  is_active: true,
  school_id: 1,
  wa_number: '081234567890',
  school_name: null,
}

interface PostCapture {
  count: number
  payload: Record<string, unknown> | null
}

interface CreateValues {
  name?: string
  email?: string
  wa?: string
  password?: string
}

function createPost(capture: PostCapture, result: () => Response = () => ok({ user: {} })) {
  return http.post('/api/users', async ({ request }) => {
    capture.payload = (await request.json()) as Record<string, unknown>
    capture.count += 1
    return result()
  })
}

function editPatch(capture: PostCapture) {
  return http.patch('/api/users/3', async ({ request }) => {
    capture.payload = (await request.json()) as Record<string, unknown>
    capture.count += 1
    return ok({ user: EDIT_USER })
  })
}

function schoolsOk(rows: Array<{ id: number; school_name: string; is_active: boolean }>) {
  return http.get('/api/schools', () =>
    paged(rows, { page: 1, per_page: 1000, total: rows.length }),
  )
}

/**
 * Sessions that restore a school during boot replace their form-content node
 * once the modal enter motion settles (~300ms after open): interactions aimed
 * at the first node hit a stale subtree — selects never expand, focus never
 * moves. Wait for that swap and hand back the stable dialog. School-less
 * (`school: null` SA fixture) sessions never swap, so they use a plain
 * `findByRole('dialog')` instead.
 */
async function stableDialog(): Promise<HTMLElement> {
  const dialog = await screen.findByRole('dialog')
  const initialName = within(dialog).getByLabelText('Nama')
  await waitFor(
    () => expect(within(screen.getByRole('dialog')).getByLabelText('Nama')).not.toBe(initialName),
    { timeout: 5000, interval: 20 },
  )
  return screen.getByRole('dialog')
}

/** Type the four create-mode fields with valid defaults; override to trigger a rule. */
async function fillCreate(user: UserEvent, values: CreateValues = {}): Promise<void> {
  const dialog = within(screen.getByRole('dialog'))
  await user.type(dialog.getByLabelText('Nama'), values.name ?? 'Siti Aminah')
  await user.type(dialog.getByLabelText('Email'), values.email ?? 'siti@kidversa.id')
  await user.type(dialog.getByLabelText('Nomor WhatsApp'), values.wa ?? '081234567890')
  await user.type(dialog.getByLabelText('Kata sandi'), values.password ?? 'rahasia1')
}

/** Open a trigger and pin the full option list of the dropdown it controls. */
async function readOptions(
  user: UserEvent,
  trigger: HTMLElement,
  expected: string[],
): Promise<void> {
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

describe('create mode (ADMIN)', () => {
  it('offers only the Fasilitator role and renders no school field', async () => {
    let schoolGets = 0
    const capture: PostCapture = { count: 0, payload: null }
    server.use(
      createPost(capture),
      http.get('/api/schools', () => {
        schoolGets += 1
        return paged([], { page: 1, per_page: 1000, total: 0 })
      }),
    )
    const user = userEvent.setup()
    renderApp(<UserModal open user={null} onClose={vi.fn()} onSaved={vi.fn()} />, {
      session: ADMIN_SESSION(),
    })

    const dialog = await stableDialog()
    expect(within(dialog).getByText('Pengguna baru')).toBeInTheDocument()
    await readOptions(user, within(dialog).getByRole('combobox'), ['Fasilitator'])

    expect(within(dialog).queryByText('Sekolah')).toBeNull()
    expect(within(dialog).queryByPlaceholderText('Pilih sekolah')).toBeNull()
    expect(schoolGets).toBe(0) // ADMIN never asks for schools
  })

  it('blocks a malformed email before any POST', async () => {
    const capture: PostCapture = { count: 0, payload: null }
    server.use(createPost(capture))
    const user = userEvent.setup()
    renderApp(<UserModal open user={null} onClose={vi.fn()} onSaved={vi.fn()} />, {
      session: ADMIN_SESSION(),
    })
    const dialog = await stableDialog()

    await fillCreate(user, { email: 'budi@salah' })
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Format email tidak valid.')
    expect(capture.count).toBe(0)
  })

  it('rejects a non-digit WhatsApp number before any POST', async () => {
    const capture: PostCapture = { count: 0, payload: null }
    server.use(createPost(capture))
    const user = userEvent.setup()
    renderApp(<UserModal open user={null} onClose={vi.fn()} onSaved={vi.fn()} />, {
      session: ADMIN_SESSION(),
    })
    const dialog = await stableDialog()

    await fillCreate(user, { wa: '08x12345' })
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Nomor WhatsApp hanya boleh berisi angka.')
    expect(capture.count).toBe(0)
  })

  it('posts a normalized payload without school_id and calls onSaved', async () => {
    const capture: PostCapture = { count: 0, payload: null }
    server.use(createPost(capture))
    const onSaved = vi.fn()
    const user = userEvent.setup()
    renderApp(<UserModal open user={null} onClose={vi.fn()} onSaved={onSaved} />, {
      session: ADMIN_SESSION(),
    })
    const dialog = await stableDialog()

    await fillCreate(user, { name: '  Siti Aminah  ', email: 'Siti@Kidversa.ID' })
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(capture.count).toBe(1))
    expect(capture.payload).toEqual({
      name: 'Siti Aminah',
      email: 'siti@kidversa.id',
      wa_number: '081234567890',
      password: 'rahasia1',
      role: 'FASILITATOR',
    })
    expect(onSaved).toHaveBeenCalledTimes(1)
    await expectToast('Pengguna “Siti Aminah” dibuat')
  })

  it('maps EMAIL_TAKEN onto the email field instead of the modal alert', async () => {
    const capture: PostCapture = { count: 0, payload: null }
    server.use(createPost(capture, () => fail('EMAIL_TAKEN')))
    const user = userEvent.setup()
    renderApp(<UserModal open user={null} onClose={vi.fn()} onSaved={vi.fn()} />, {
      session: ADMIN_SESSION(),
    })
    const dialog = await stableDialog()

    await fillCreate(user)
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    const fieldError = await screen.findByText('Email sudah digunakan.')
    expect(fieldError.closest('.ant-form-item')).not.toBeNull() // field-level explain
    expect(screen.queryByRole('alert')).toBeNull() // no modal Alert
    expect(capture.count).toBe(1)
  })

  it('renders other server failures as the translated modal alert', async () => {
    const capture: PostCapture = { count: 0, payload: null }
    server.use(createPost(capture, () => fail('INTERNAL')))
    const user = userEvent.setup()
    renderApp(<UserModal open user={null} onClose={vi.fn()} onSaved={vi.fn()} />, {
      session: ADMIN_SESSION(),
    })
    const dialog = await stableDialog()

    await fillCreate(user)
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Terjadi kesalahan tak terduga. Coba lagi.')
  })
})

describe('create mode (SA)', () => {
  it('lists only ACTIVE schools, offers both roles and requires a school', async () => {
    let schoolGets = 0
    const schoolQuery: { current: URLSearchParams | null } = { current: null }
    const capture: PostCapture = { count: 0, payload: null }
    server.use(
      http.get('/api/schools', ({ request }) => {
        schoolGets += 1
        schoolQuery.current = new URL(request.url).searchParams
        return paged(
          [
            { id: 1, school_name: 'Bandung', is_active: true },
            { id: 2, school_name: 'Jakarta', is_active: false },
          ],
          { page: 1, per_page: 1000, total: 2 },
        )
      }),
      createPost(capture),
    )
    const user = userEvent.setup()
    renderApp(<UserModal open user={null} onClose={vi.fn()} onSaved={vi.fn()} />, {
      session: SA_NO_SCHOOL_SESSION(),
    })

    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(schoolGets).toBe(1))
    expect(schoolQuery.current?.get('per_page')).toBe('1000')

    const combos = within(dialog).getAllByRole('combobox')
    await readOptions(user, combos[0], ['Fasilitator', 'Admin'])
    await readOptions(user, combos[1], ['Bandung']) // Jakarta (inactive) filtered out

    await fillCreate(user)
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Wajib diisi.') // school untouched
    expect(capture.count).toBe(0)
  })

  it('posts school_id once a school is picked', async () => {
    const capture: PostCapture = { count: 0, payload: null }
    const onSaved = vi.fn()
    server.use(
      schoolsOk([
        { id: 1, school_name: 'Bandung', is_active: true },
        { id: 2, school_name: 'Jakarta', is_active: false },
      ]),
      createPost(capture),
    )
    const user = userEvent.setup()
    renderApp(<UserModal open user={null} onClose={vi.fn()} onSaved={onSaved} />, {
      session: SA_NO_SCHOOL_SESSION(),
    })
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(within(dialog).getAllByRole('combobox')).toHaveLength(2))

    await fillCreate(user)
    await user.click(within(dialog).getAllByRole('combobox')[1])
    await user.click(
      await screen.findByText('Bandung', { selector: '.ant-select-item-option-content' }),
    )
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(capture.count).toBe(1))
    expect(capture.payload).toEqual({
      name: 'Siti Aminah',
      email: 'siti@kidversa.id',
      wa_number: '081234567890',
      password: 'rahasia1',
      role: 'FASILITATOR',
      school_id: 1,
    })
    expect(onSaved).toHaveBeenCalledTimes(1)
    await expectToast('Pengguna “Siti Aminah” dibuat')
  })

  it('keeps the modal usable when the school list fails to load', async () => {
    let schoolGets = 0
    const capture: PostCapture = { count: 0, payload: null }
    server.use(
      http.get('/api/schools', () => {
        schoolGets += 1
        return fail('INTERNAL')
      }),
      createPost(capture),
    )
    const user = userEvent.setup()
    renderApp(<UserModal open user={null} onClose={vi.fn()} onSaved={vi.fn()} />, {
      session: SA_NO_SCHOOL_SESSION(),
    })
    const dialog = await screen.findByRole('dialog')
    await waitFor(() => expect(schoolGets).toBe(1))

    const combos = within(dialog).getAllByRole('combobox')
    await user.click(combos[1])
    const optionCount = await waitFor(() => {
      const dropdowns = document.querySelectorAll<HTMLElement>(
        '.ant-select-dropdown:not(.ant-select-dropdown-hidden)',
      )
      expect(dropdowns.length).toBeGreaterThan(0)
      return dropdowns[dropdowns.length - 1].querySelectorAll('.ant-select-item-option-content')
        .length
    })
    expect(optionCount).toBe(0) // schools=[] branch: an empty select

    await fillCreate(user)
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))
    await screen.findByText('Wajib diisi.') // validation still runs
    expect(capture.count).toBe(0)
  })
})

describe('edit mode', () => {
  it('prefills the fields and PATCHes only {name, email, wa_number}', async () => {
    const capture: PostCapture = { count: 0, payload: null }
    server.use(editPatch(capture))
    const onSaved = vi.fn()
    const user = userEvent.setup()
    renderApp(<UserModal open user={EDIT_USER} onClose={vi.fn()} onSaved={onSaved} />, {
      session: ADMIN_SESSION(),
    })

    const dialog = await stableDialog()
    expect(within(dialog).getByText('Ubah pengguna')).toBeInTheDocument()
    expect(within(dialog).getByLabelText('Nama')).toHaveValue('Siti Aminah')
    expect(within(dialog).getByLabelText('Email')).toHaveValue('siti@Kidversa.ID')
    expect(within(dialog).getByLabelText('Nomor WhatsApp')).toHaveValue('081234567890')
    expect(within(dialog).queryByLabelText('Kata sandi')).toBeNull() // create-only
    expect(within(dialog).queryByRole('combobox')).toBeNull() // no role / school select

    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(capture.count).toBe(1))
    expect(capture.payload).toEqual({
      name: 'Siti Aminah',
      email: 'siti@kidversa.id',
      wa_number: '081234567890',
    })
    expect(onSaved).toHaveBeenCalledTimes(1)
    await expectToast('Pengguna “Siti Aminah” disimpan')
  })

  it('cancels through onClose without sending a request', async () => {
    const capture: PostCapture = { count: 0, payload: null }
    server.use(editPatch(capture))
    const onClose = vi.fn()
    const user = userEvent.setup()
    renderApp(<UserModal open user={EDIT_USER} onClose={onClose} onSaved={vi.fn()} />, {
      session: ADMIN_SESSION(),
    })
    const dialog = await stableDialog()

    await user.click(within(dialog).getByRole('button', { name: 'Batal' }))

    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    expect(capture.count).toBe(0)
  })

  it('blocks a malformed email on edit without a PATCH', async () => {
    const capture: PostCapture = { count: 0, payload: null }
    server.use(editPatch(capture))
    const user = userEvent.setup()
    renderApp(<UserModal open user={EDIT_USER} onClose={vi.fn()} onSaved={vi.fn()} />, {
      session: ADMIN_SESSION(),
    })
    const dialog = await stableDialog()

    const email = within(dialog).getByLabelText('Email')
    await user.clear(email)
    await user.type(email, 'salah')
    await user.click(within(dialog).getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Format email tidak valid.')
    expect(capture.count).toBe(0)
  })
})

describe('closed state', () => {
  it('renders no dialog while open is false', () => {
    renderApp(<UserModal open={false} user={null} onClose={vi.fn()} onSaved={vi.fn()} />, {
      session: ADMIN_SESSION(),
    })

    expect(screen.queryByRole('dialog')).toBeNull()
  })
})
