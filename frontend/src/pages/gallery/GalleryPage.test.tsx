/**
 * Admin gallery (F-10 / FL-10): the consent gate blocks uploads and gates the
 * send action per role, FL-11 resend envelopes, the honest-file upload flow
 * (multipart contract, transient preview only, item appears solely via
 * `photo.changed` → refetch), PHOTO_LIMIT inline, the client pre-check that
 * blocks oversize (>50 MB) and wrong-format photos before send,
 * candidate toggling and deletion, plus the `copy.completed` photo refetch
 * (the open gallery tab follows copy jobs like `photo.changed`).
 */
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import StreamGate from '../../components/StreamGate'
import GalleryPage from './GalleryPage'
import { emitSignal } from '../../lib/stream'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, ok, paged, sseChannel } from '../../test/handlers'
import {
  detailFixture,
  photoItem,
  pushFrame,
  until,
  useSseFakeTimers,
} from '../../test/helpers.cov-session'
import type { PhotoItem } from '../../lib/session-contracts'
import type { RouteObject } from 'react-router'

const GALLERY_ROUTE = (gate = false): RouteObject[] => [
  {
    path: '/sessions/:sid/gallery',
    element: gate ? (
      <>
        <StreamGate />
        <GalleryPage />
      </>
    ) : (
      <GalleryPage />
    ),
  },
]

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })
const FAS_SESSION = () => makeLoginData({ user: makeUser({ role: 'FASILITATOR' }) })

function framesHandler() {
  return http.get('/api/frames', () =>
    paged([{ id: 11, name: 'Bingkai Cantik', image_url: '/uploads/frames/11.png' }], {
      page: 1,
      per_page: 1000,
      total: 1,
    }),
  )
}

/** Child 2 (Aisyah) is PENDING, child 3 (Budi) is SETUJU — see detailFixture. */
function photosHandler(prefix: '2' | '3', photosRef: { photos: PhotoItem[] }) {
  return http.get(`/api/sessions/1/topics/3/children/${prefix}/photos`, () =>
    ok(photosRef.photos),
  )
}

function renderGallery(
  session = ADMIN_SESSION(),
  gate = false,
  route = '/sessions/1/gallery',
) {
  renderApp(null, { routes: GALLERY_ROUTE(gate), route, session })
}

/** Real-timer picker interaction (userEvent drives antd's open reliably). */
async function switchToBudi(user: UserEvent): Promise<void> {
  // Group + child pickers: [0]=group, [1]=child (single topic → no topic select).
  await screen.findAllByRole('combobox')
  const [, childPicker] = screen.getAllByRole('combobox')
  await user.click(childPicker)
  await user.click(await screen.findByText('Budi Santoso · SD Melati'))
}

describe('admin gallery consent gate', () => {
  it('PENDING consent locks the gallery; ADMIN can send the consent', async () => {
    let sendCalls = 0
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('2', { photos: [] }),
      http.post('/api/sessions/1/children/2/send-consent', () => {
        sendCalls += 1
        return ok({ token: 'tok', decision: 'PENDING', resent: true })
      }),
    )
    const user = userEvent.setup()
    renderGallery()

    await screen.findByText('Galeri terkunci sampai orang tua menyetujui.')
    screen.getByText('Persetujuan: Menunggu')
    // No upload affordance while locked.
    expect(document.querySelector('input[type="file"]')).toBeNull()
    expect(screen.queryByRole('button', { name: /Ambil foto/ })).toBeNull()

    await user.click(screen.getByRole('button', { name: /Kirim persetujuan/ }))
    await screen.findByText('Persetujuan terkirim.')
    await waitFor(() => expect(sendCalls).toBe(1))
  })

  it('DITOLAK shows the resend label and the already-decided envelope as info', async () => {
    server.use(
      http.get('/api/sessions/1', () =>
        ok(
          (() => {
            const detail = detailFixture({ status: 'OPENED' })
            detail.groups[0].members = [
              { child_id: 2, name: 'Aisyah Putri', age: 8, school: 'SD Cendekia', consent: 'DITOLAK' },
            ]
            detail.groups[0].member_count = 1
            return detail
          })(),
        ),
      ),
      framesHandler(),
      photosHandler('2', { photos: [] }),
      http.post('/api/sessions/1/children/2/send-consent', () =>
        ok({ token: 'tok', decision: 'SETUJU', resent: false }),
      ),
    )
    const user = userEvent.setup()
    renderGallery()

    await screen.findByText('Galeri terkunci sampai orang tua menyetujui.')
    screen.getByText('Persetujuan: Ditolak')
    await user.click(screen.getByRole('button', { name: /Kirim ulang/ }))

    await screen.findByText(
      'Sudah Disetujui — tautan tidak berubah dan WhatsApp tidak dikirim ulang.',
    )
  })

  it('FAS never sees the consent send action', async () => {
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('2', { photos: [] }),
    )
    renderGallery(FAS_SESSION())

    await screen.findByText('Galeri terkunci sampai orang tua menyetujui.')
    expect(screen.queryByRole('button', { name: /Kirim persetujuan/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Kirim ulang/ })).toBeNull()
  })
})

describe('admin gallery upload (honest-file rule)', () => {
  it('uploads multipart, keeps the preview transient, then lands via photo.changed', async () => {
    useSseFakeTimers()
    // Grant a fake camera: without it PhotoCapture starts in CAMERA_DENIED
    // and the transient upload preview (a non-denied-mode branch) never renders.
    Object.defineProperty(navigator, 'mediaDevices', {
      configurable: true,
      value: { getUserMedia: vi.fn(async () => ({ getTracks: () => [] })) },
    })
    const captured: { raw: string | null } = { raw: null }
    const photosRef: { photos: PhotoItem[] } = { photos: [] }
    const channel = sseChannel('/api/stream')
    server.use(
      channel.handler,
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('3', photosRef),
      // NB: request.formData() hits a jsdom-File/undici webidl assertion here —
      // the multipart contract is asserted on the raw body instead. The
      // artificial delay keeps `uploading` observable while the response is
      // still in flight (fake-timer stepped by `until`).
      http.post('/api/sessions/1/topics/3/children/3/photos', async ({ request }) => {
        captured.raw = await request.text()
        await delay(500)
        return ok({ photo: photoItem(7) })
      }),
    )
    renderGallery(ADMIN_SESSION(), true, '/sessions/1/gallery?gid=5&cid=3&stid=3')
    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Belum ada foto.') !== null)

    // Camera is unavailable in jsdom → the fallback picker carries the file input.
    const input = document.querySelector('input[type="file"]') as HTMLInputElement
    const file = new File([new Uint8Array(16)], 'foto.jpg', { type: 'image/jpeg' })
    fireEvent.change(input, { target: { files: [file] } })

    // While the upload is in flight: the transient preview shows, but NO
    // gallery item exists yet (honest-file rule). NB: asserted via the preview
    // block's class — PhotoCapture's `capture.*` i18n keys miss (the locale
    // nests them at `gallery.capture.*`), so its label renders as the raw key.
    await until(() => captured.raw !== null)
    expect(document.querySelector('.kv-capture__preview')).not.toBeNull()
    expect(screen.queryByAltText('/uploads/photos/7.jpg')).toBeNull()

    // Multipart contract: file field name + JPEG type + the default frame
    // overlay value. (The serialized filename reads "blob" — the jsdom File
    // name does not survive the undici body bridge; not an app concern.)
    expect(captured.raw).toContain('name="file"')
    expect(captured.raw).toContain('Content-Type: image/jpeg')
    expect(captured.raw).toContain('name="frame_id"')
    expect(captured.raw).toContain('11')

    // The preview drops with the 200; the grid stays empty.
    await until(() => document.querySelector('.kv-capture__preview') === null)
    expect(screen.queryByText('Belum ada foto.')).not.toBeNull()

    // The honest-file rule: the item lands ONLY via photo.changed → refetch.
    photosRef.photos = [photoItem(7)]
    pushFrame(channel, 'photo.changed', { rev: 1 })
    await until(() => screen.queryByAltText('/uploads/photos/7.jpg') !== null)
  })

  it('the >50 MB client pre-check blocks the request', async () => {
    let uploadCalls = 0
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('2', { photos: [] }),
      photosHandler('3', { photos: [] }),
      http.post('/api/sessions/1/topics/3/children/3/photos', () => {
        uploadCalls += 1
        return HttpResponse.json({ error: { code: 'PHOTO_LIMIT', message: 'full' } })
      }),
    )
    const user = userEvent.setup()
    renderGallery()

    await switchToBudi(user)
    await screen.findByText('Belum ada foto.')
    const input = document.querySelector('input[type="file"]') as HTMLInputElement

    const big = new File([new Uint8Array(16)], 'besar.jpg', { type: 'image/jpeg' })
    Object.defineProperty(big, 'size', { value: 51 * 1024 * 1024 })
    fireEvent.change(input, { target: { files: [big] } })

    await screen.findByText('Foto harus berformat JPG, JPEG, atau PNG dan maksimal 50 MB.')
    expect(uploadCalls).toBe(0)
  })

  it('the wrong-format client pre-check blocks the request', async () => {
    let uploadCalls = 0
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('2', { photos: [] }),
      photosHandler('3', { photos: [] }),
      http.post('/api/sessions/1/topics/3/children/3/photos', () => {
        uploadCalls += 1
        return HttpResponse.json({ error: { code: 'PHOTO_LIMIT', message: 'full' } })
      }),
    )
    const user = userEvent.setup()
    renderGallery()

    await switchToBudi(user)
    await screen.findByText('Belum ada foto.')
    const input = document.querySelector('input[type="file"]') as HTMLInputElement

    // A GIF with a plausible size must still be blocked on format.
    const gif = new File([new Uint8Array(16)], 'animasi.gif', { type: 'image/gif' })
    fireEvent.change(input, { target: { files: [gif] } })

    await screen.findByText('Foto harus berformat JPG, JPEG, atau PNG dan maksimal 50 MB.')
    expect(uploadCalls).toBe(0)
  })

  it('PHOTO_LIMIT renders as the inline limit notice', async () => {
    let uploadCalls = 0
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('2', { photos: [] }),
      photosHandler('3', { photos: [] }),
      http.post('/api/sessions/1/topics/3/children/3/photos', () => {
        uploadCalls += 1
        return HttpResponse.json({ error: { code: 'PHOTO_LIMIT', message: 'full' } })
      }),
    )
    const user = userEvent.setup()
    renderGallery()

    await switchToBudi(user)
    await screen.findByText('Belum ada foto.')
    const input = document.querySelector('input[type="file"]') as HTMLInputElement

    fireEvent.change(input, {
      target: { files: [new File([new Uint8Array(16)], 'oke.jpg', { type: 'image/jpeg' })] },
    })
    await screen.findByText('Maksimal 10 foto per anak per topik.')
    expect(uploadCalls).toBe(1)

    // The limit alert is dismissible — the inline notice clears on close.
    await user.click(document.querySelector('.ant-alert-close-icon') as HTMLElement)
    await waitFor(() =>
      expect(screen.queryByText('Maksimal 10 foto per anak per topik.')).toBeNull(),
    )
  })

  it('picking a group resets the child picker (filters re-scope together)', async () => {
    // Two groups with members (so the second option changes gid) plus one
    // WITHOUT members — it must be filtered out of the option list.
    const groups = detailFixture({ status: 'OPENED' }).groups
    groups.push(
      {
        gid: 6,
        name: 'Kelompok Biru',
        own: true,
        owner: { id: 7, name: 'Dewi Lestari' },
        owner_missing: false,
        member_count: 0,
        consent: {},
        members: [],
      },
      { gid: 7, name: 'Tanpa Anggota', own: true, member_count: 0 },
    )
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED', groups }))),
      framesHandler(),
      photosHandler('2', { photos: [] }),
      photosHandler('3', { photos: [] }),
    )
    const user = userEvent.setup()
    renderGallery()

    await switchToBudi(user)
    await screen.findByText('Belum ada foto.')

    const groupPicker = screen.getAllByRole('combobox')[0]
    await user.click(groupPicker)
    // The memberless group is not offered; only Merah + Biru are.
    expect(screen.queryByText('Tanpa Anggota')).toBeNull()
    const options = screen.getAllByText('Kelompok Biru')
    await user.click(options[options.length - 1]) // the dropdown option, not a selection item

    // gid changed → setCid(null): the child selection item is gone, the
    // group now shows the new gid, and the photos unload (prefix null).
    await waitFor(() => {
      const items = Array.from(document.querySelectorAll('.ant-select-selection-item'))
      expect(items.map((item) => item.getAttribute('title'))).not.toContain(
        'Budi Santoso · SD Melati',
      )
      expect(items.map((item) => item.getAttribute('title'))).toContain('Kelompok Biru')
    })
  })
})

describe('admin gallery photo actions', () => {
  it('candidate toggle PATCHes and refetches through the own-200 path', async () => {
    let patched: unknown = null
    const photosRef: { photos: PhotoItem[] } = { photos: [photoItem(7)] }
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('2', { photos: [] }),
      photosHandler('3', photosRef),
      http.patch('/api/sessions/1/topics/3/children/3/photos/7', async ({ request }) => {
        patched = await request.json()
        photosRef.photos = [photoItem(7, { is_candidate: true })]
        return ok({ photo: photoItem(7, { is_candidate: true }) })
      }),
    )
    const user = userEvent.setup()
    renderGallery()

    await switchToBudi(user)
    const photo = await screen.findByAltText('/uploads/photos/7.jpg')
    const tile = photo.closest('.ant-card') as HTMLElement
    await user.click(within(tile).getByRole('button', { name: 'Jadikan foto utama' }))

    expect(patched).toEqual({ is_candidate: true })
    await waitFor(() =>
      expect(
        within(screen.getByAltText('/uploads/photos/7.jpg').closest('.ant-card') as HTMLElement)
          .getByRole('button', { name: 'Jadikan foto utama' }),
      ).toHaveClass('ant-btn-primary'),
    )
  })

  it('delete asks for confirmation, then DELETEs', async () => {
    let deleted = 0
    const photosRef: { photos: PhotoItem[] } = { photos: [photoItem(7)] }
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('2', { photos: [] }),
      photosHandler('3', photosRef),
      http.delete('/api/sessions/1/topics/3/children/3/photos/7', () => {
        deleted += 1
        photosRef.photos = []
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderGallery()

    await switchToBudi(user)
    const photo = await screen.findByAltText('/uploads/photos/7.jpg')
    const tile = photo.closest('.ant-card') as HTMLElement
    await user.click(within(tile).getByRole('button', { name: 'Hapus foto' }))

    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Hapus' }))

    await screen.findByText('Foto dihapus.')
    await waitFor(() => expect(deleted).toBe(1))
    await screen.findByText('Belum ada foto.')
  })

  it('copy.completed refetches the open gallery photos (F-F03)', async () => {
    // Backend publishes copy.completed (with session_id) after a copy job —
    // the open gallery tab must refresh its photos like photo.changed does.
    let photoCalls = 0
    const photosRef: { photos: PhotoItem[] } = { photos: [photoItem(7)] }
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('2', { photos: [] }),
      http.get('/api/sessions/1/topics/3/children/3/photos', () => {
        photoCalls += 1
        return ok(photosRef.photos)
      }),
    )
    const user = userEvent.setup()
    renderGallery()

    await switchToBudi(user)
    await screen.findByAltText('/uploads/photos/7.jpg')
    expect(photoCalls).toBe(1)

    // The copy lands new photos server-side; the refetch pulls them in.
    photosRef.photos = [photoItem(7), photoItem(8)]
    act(() => emitSignal('copy.completed', { rev: 1 }))
    await waitFor(() => expect(photoCalls).toBe(2))
    await screen.findByAltText('/uploads/photos/8.jpg')
  })
})
