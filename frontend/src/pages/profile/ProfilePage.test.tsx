/**
 * Profile (F-14): identity line in the header, own-name save (trim mirror,
 * toast, errors.<CODE> section alert) and change-password (the client mirror
 * blocks any POST; INVALID_CREDENTIALS lands on the current-password field
 * while other codes surface as the section alert; success toasts + resets the
 * three fields).
 */
import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http } from 'msw'
import ProfilePage from './ProfilePage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, makeUser, ok } from '../../test/handlers'
import { avatarUrl } from '../../lib/avatar'

/**
 * The editor's internals are covered exhaustively by PhotoEditorModal.test;
 * here the page integration is pinned through a probe component that exposes
 * the parent handlers (confirm/delete/close) and the live uploading/progress
 * props.
 */
vi.mock('../../components/profile/PhotoEditorModal', () => ({
  default: function FakeEditor(props: {
    open: boolean
    uploading: boolean
    progress: number
    onClose: () => void
    onConfirm: (file: File) => void
    onDelete: () => void
  }) {
    if (!props.open) return null
    return (
      <div data-testid="fake-editor">
        <button onClick={() => props.onConfirm(new File(['x'], 'avatar.jpg', { type: 'image/jpeg' }))}>
          FAKE-CONFIRM
        </button>
        <button onClick={() => props.onDelete()}>FAKE-DELETE</button>
        <button onClick={() => props.onClose()}>FAKE-CLOSE</button>
        <span data-testid="fake-editor-upload">{props.uploading ? 'up' : 'idle'}</span>
        <span data-testid="fake-editor-progress">{props.progress}</span>
      </div>
    )
  },
}))

function nameSection(): HTMLElement {
  const card = screen.getByRole('heading', { name: 'Nama' }).closest('.ant-card')
  if (!card) throw new Error('name card not found')
  return card as HTMLElement
}

function passwordSection(): HTMLElement {
  const card = screen.getByRole('heading', { name: 'Kata sandi' }).closest('.ant-card')
  if (!card) throw new Error('password card not found')
  return card as HTMLElement
}

/** Render and wait for the boot refresh to restore the session identity. */
async function renderProfile(): Promise<void> {
  renderApp(<ProfilePage />, { session: makeLoginData() })
  await screen.findByText('admin@kidversa.id · Admin · Bandung')
}

async function fillPassword(
  user: UserEvent,
  values: { current?: string; fresh: string; confirm: string },
): Promise<HTMLElement> {
  const section = passwordSection()
  if (values.current) {
    await user.type(within(section).getByLabelText('Kata sandi saat ini'), values.current)
  }
  await user.type(within(section).getByLabelText('Kata sandi baru'), values.fresh)
  await user.type(within(section).getByLabelText('Ulangi kata sandi baru'), values.confirm)
  return section
}

describe('profile header', () => {
  it('shows the identity line email · role · school in the page description', async () => {
    renderApp(<ProfilePage />, { session: makeLoginData() })
    expect(
      await screen.findByText('admin@kidversa.id · Admin · Bandung'),
    ).toBeInTheDocument()
  })
})

describe('profile name form', () => {
  it('requires the name and never PATCHes an empty value', async () => {
    let patches = 0
    server.use(
      http.patch('/api/auth/profile', () => {
        patches += 1
        return ok({ user: makeUser() })
      }),
    )
    const user = userEvent.setup()
    await renderProfile()

    const section = nameSection()
    await user.clear(within(section).getByLabelText('Nama'))
    await user.click(within(section).getByRole('button', { name: 'Simpan' }))

    await within(section).findByText('Wajib diisi.')
    expect(patches).toBe(0)
  })

  it('sends the trimmed name and toasts the success', async () => {
    let patches = 0
    let payload: unknown = null
    server.use(
      http.patch('/api/auth/profile', async ({ request }) => {
        payload = await request.json()
        patches += 1
        return ok({ user: makeUser({ name: 'Rina Aditya' }) })
      }),
    )
    const user = userEvent.setup()
    await renderProfile()

    const section = nameSection()
    await user.clear(within(section).getByLabelText('Nama'))
    await user.type(within(section).getByLabelText('Nama'), '  Rina Aditya  ')
    await user.click(within(section).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(patches).toBe(1))
    expect(payload).toEqual({ name: 'Rina Aditya' })
    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Nama berhasil disimpan.',
      ),
    )
  })

  it('shows the translated INTERNAL alert inside the name section on failure', async () => {
    server.use(http.patch('/api/auth/profile', () => fail('INTERNAL')))
    const user = userEvent.setup()
    await renderProfile()

    const section = nameSection()
    await user.clear(within(section).getByLabelText('Nama'))
    await user.type(within(section).getByLabelText('Nama'), 'Rina')
    await user.click(within(section).getByRole('button', { name: 'Simpan' }))

    await within(section).findByText('Terjadi kesalahan tak terduga. Coba lagi.')
    expect(within(section).getByRole('alert')).toBeInTheDocument()
    expect(within(passwordSection()).queryByRole('alert')).toBeNull()
  })
})

describe('profile password validation', () => {
  it('blocks a mismatched confirmation without POSTing', async () => {
    let posts = 0
    server.use(
      http.post('/api/auth/change-password', () => {
        posts += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    await renderProfile()

    const section = await fillPassword(user, {
      current: 'rahasia1',
      fresh: 'rahasia12',
      confirm: 'rahasia13',
    })
    await user.click(within(section).getByRole('button', { name: 'Simpan' }))

    // The confirm field renders the mismatch sentence (dedicated required rule
    // + validator-only rule, so the validator's own text survives) and blocks
    // the POST.
    const field = within(section).getByLabelText('Ulangi kata sandi baru').closest('.ant-form-item')
    await waitFor(() =>
      expect(field?.querySelector('.ant-form-item-explain-error')).toHaveTextContent(
        'Kata sandi tidak sama.',
      ),
    )
    expect(posts).toBe(0)
  })

  it('blocks a weak new password against the mirrored rule without POSTing', async () => {
    let posts = 0
    server.use(
      http.post('/api/auth/change-password', () => {
        posts += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    await renderProfile()

    const section = await fillPassword(user, {
      current: 'rahasia1',
      fresh: 'abc123',
      confirm: 'abc123',
    })
    await user.click(within(section).getByRole('button', { name: 'Simpan' }))

    // The field renders the rule message as its validation error next to the
    // always-visible hint carrying the same wording.
    const field = within(section).getByLabelText('Kata sandi baru').closest('.ant-form-item')
    await waitFor(() =>
      expect(field?.querySelector('.ant-form-item-explain-error')).toHaveTextContent(
        'Minimal 8 karakter dan harus berisi huruf serta angka.',
      ),
    )
    expect(posts).toBe(0)
  })

  it('requires the current password without POSTing', async () => {
    let posts = 0
    server.use(
      http.post('/api/auth/change-password', () => {
        posts += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    await renderProfile()

    const section = await fillPassword(user, { fresh: 'rahasia12', confirm: 'rahasia12' })
    await user.click(within(section).getByRole('button', { name: 'Simpan' }))

    await within(section).findByText('Wajib diisi.')
    expect(posts).toBe(0)
  })
})

describe('profile password change', () => {
  it('posts the credentials, toasts and resets the three fields', async () => {
    let posts = 0
    let payload: unknown = null
    server.use(
      http.post('/api/auth/change-password', async ({ request }) => {
        payload = await request.json()
        posts += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    await renderProfile()

    const section = await fillPassword(user, {
      current: 'rahasia1',
      fresh: 'rahasia12',
      confirm: 'rahasia12',
    })
    await user.click(within(section).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(posts).toBe(1))
    expect(payload).toEqual({ current_password: 'rahasia1', new_password: 'rahasia12' })
    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Kata sandi berhasil diganti. Perangkat lain perlu masuk kembali.',
      ),
    )
    expect(within(section).getByLabelText('Kata sandi saat ini')).toHaveValue('')
    expect(within(section).getByLabelText('Kata sandi baru')).toHaveValue('')
    expect(within(section).getByLabelText('Ulangi kata sandi baru')).toHaveValue('')
  })

  it('maps INVALID_CREDENTIALS onto the current-password field without a generic alert', async () => {
    server.use(http.post('/api/auth/change-password', () => fail('INVALID_CREDENTIALS')))
    const user = userEvent.setup()
    await renderProfile()

    const section = await fillPassword(user, {
      current: 'rahasia1',
      fresh: 'rahasia12',
      confirm: 'rahasia12',
    })
    await user.click(within(section).getByRole('button', { name: 'Simpan' }))

    const field = within(section).getByLabelText('Kata sandi saat ini').closest('.ant-form-item')
    await waitFor(() =>
      expect(field?.querySelector('.ant-form-item-explain-error')).toHaveTextContent(
        'Email atau kata sandi salah.',
      ),
    )
    expect(within(section).queryByRole('alert')).toBeNull() // no section alert
  })

  it('shows the translated RATE_LIMITED alert inside the password section', async () => {
    server.use(http.post('/api/auth/change-password', () => fail('RATE_LIMITED')))
    const user = userEvent.setup()
    await renderProfile()

    const section = await fillPassword(user, {
      current: 'rahasia1',
      fresh: 'rahasia12',
      confirm: 'rahasia12',
    })
    await user.click(within(section).getByRole('button', { name: 'Simpan' }))

    await within(section).findByText('Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.')
    expect(within(section).getByRole('alert')).toBeInTheDocument()
    expect(within(nameSection()).queryByRole('alert')).toBeNull()
  })
})

// spec 09: the profile photo card — entry button opens the self-contained
// editor; confirm uploads via uploadWithProgress (multipart + idempotency
// header) and updates the session user; delete DELETEs and toasts; failures
// land on the card's section alert with the modal kept open for a retry.
describe('profile photo card', () => {
  function photoSection(): HTMLElement {
    const card = screen.getByRole('heading', { name: 'Foto profil' }).closest('.ant-card')
    if (!card) throw new Error('photo card not found')
    return card as HTMLElement
  }

  const editor = () => screen.getByTestId('fake-editor')

  it('shows the avatar fallback and the Ubah foto entry button', async () => {
    await renderProfile()

    const section = photoSection()
    const img = section.querySelector('.kv-profile-photo img')
    expect(img).toHaveAttribute('src', avatarUrl('Admin Bandung'))
    expect(within(section).getByRole('button', { name: 'Ubah foto' })).toBeInTheDocument()
    expect(screen.queryByTestId('fake-editor')).toBeNull() // closed by default
  })

  it('opens the editor from the entry button (and Close only closes)', async () => {
    const user = userEvent.setup()
    await renderProfile()

    await user.click(within(photoSection()).getByRole('button', { name: 'Ubah foto' }))
    expect(editor()).toBeInTheDocument()

    await user.click(within(editor()).getByText('FAKE-CLOSE'))
    expect(screen.queryByTestId('fake-editor')).toBeNull()
  })

  it('uploads the composed file with idempotency, updates the avatar and toasts', async () => {
    let release!: () => void
    const gate = new Promise<void>((resolve) => {
      release = resolve
    })
    let seenCt = ''
    let seenIdem = ''
    server.use(
      http.post('/api/auth/profile-photo', async ({ request }) => {
        seenCt = request.headers.get('content-type') ?? ''
        seenIdem = request.headers.get('x-idempotency-key') ?? ''
        await gate
        return ok({ user: makeUser({ photo_url: '/uploads/avatars/1/abc.png' }) })
      }),
    )
    const user = userEvent.setup()
    await renderProfile()

    await user.click(within(photoSection()).getByRole('button', { name: 'Ubah foto' }))
    await user.click(within(editor()).getByText('FAKE-CONFIRM'))

    // During the flight: live uploading state on the modal, card untouched.
    await waitFor(() => expect(editor().querySelector('[data-testid="fake-editor-upload"]')).toHaveTextContent('up'))
    expect(seenIdem).not.toBe('')
    expect(screen.queryByTestId('fake-editor-upload')).not.toBeNull()

    release()
    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Foto profil diperbarui.',
      ),
    )
    // Multipart POST (never a JSON body) + exactly one idempotency key.
    expect(seenCt).toContain('multipart/form-data')
    // The editor closes only after the server answer.
    expect(screen.queryByTestId('fake-editor')).toBeNull()
    // The card avatar now shows the stored photo.
    expect(photoSection().querySelector('.kv-profile-photo img')).toHaveAttribute(
      'src',
      '/uploads/avatars/1/abc.png',
    )
  })

  it('deletes the photo via DELETE, toasts, and closes after the server answer', async () => {
    let deletes = 0
    server.use(
      http.delete('/api/auth/profile-photo', () => {
        deletes += 1
        return ok({ user: makeUser() }) // photo_url absent again
      }),
    )
    const user = userEvent.setup()
    await renderProfile()

    await user.click(within(photoSection()).getByRole('button', { name: 'Ubah foto' }))
    await user.click(within(editor()).getByText('FAKE-DELETE'))

    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Foto profil dihapus.',
      ),
    )
    expect(deletes).toBe(1)
    expect(screen.queryByTestId('fake-editor')).toBeNull()
    expect(photoSection().querySelector('.kv-profile-photo img')).toHaveAttribute(
      'src',
      avatarUrl('Admin Bandung'),
    )
  })

  it('shows the section alert on an upload failure and keeps the editor open', async () => {
    server.use(http.post('/api/auth/profile-photo', () => fail('VALIDATION')))
    const user = userEvent.setup()
    await renderProfile()

    await user.click(within(photoSection()).getByRole('button', { name: 'Ubah foto' }))
    await user.click(within(editor()).getByText('FAKE-CONFIRM'))

    await within(photoSection()).findByText('Data tidak valid. Periksa kembali isian Anda.')
    expect(within(photoSection()).getByRole('alert')).toBeInTheDocument()
    // A failure is retriable: the modal stays open.
    expect(editor()).toBeInTheDocument()
  })
})
