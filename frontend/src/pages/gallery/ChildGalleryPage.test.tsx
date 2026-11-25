/**
 * Dedicated child gallery (`/sessions/:sid/gallery/children/:cid`): the
 * consent gate locks this workspace (and the gallery offers no consent-sending
 * action — send/resend lives on the consent page), the honest-file upload flow
 * (multipart contract, transient preview only, item appears solely via
 * `photo.changed` → refetch), PHOTO_LIMIT inline, the client pre-check that
 * blocks oversize (>50 MB) and wrong-format photos before send, candidate
 * toggling and deletion, plus the `copy.completed` photo refetch.
 *
 * Topic selection lives here now; it is hidden when the session has one topic.
 */
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import StreamGate from '../../components/StreamGate'
import ChildGalleryPage from './ChildGalleryPage'
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
import type { PhotoItem, SessionDetail } from '../../lib/session-contracts'
import type { RouteObject } from 'react-router'

const CHILD_ROUTES = (gate = false): RouteObject[] => [
  {
    path: '/sessions/:sid/gallery',
    element: <div>GALLERY INDEX</div>,
  },
  {
    path: '/sessions/:sid/gallery/children/:cid',
    element: gate ? (
      <>
        <StreamGate />
        <ChildGalleryPage />
      </>
    ) : (
      <ChildGalleryPage />
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

function renderChild(
  cid: 2 | 3,
  options: { gate?: boolean; session?: ReturnType<typeof ADMIN_SESSION>; route?: string } = {},
) {
  const { gate = false, session = ADMIN_SESSION(), route } = options
  renderApp(null, {
    routes: CHILD_ROUTES(gate),
    route: route ?? `/sessions/1/gallery/children/${cid}`,
    session,
  })
}

describe('child gallery consent gate', () => {
  it('PENDING consent locks the gallery; the send action is gone', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))))
    renderChild(2)

    await screen.findByText('Galeri terkunci sampai orang tua menyetujui.')
    screen.getByText('Persetujuan: Menunggu')
    // No upload affordance while locked.
    expect(document.querySelector('input[type="file"]')).toBeNull()
    expect(screen.queryByRole('button', { name: /Ambil foto/ })).toBeNull()
    // The gallery offers no consent sending; the consent-page link stays.
    expect(screen.queryByRole('button', { name: /Kirim persetujuan/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Kirim ulang/ })).toBeNull()
    screen.getByRole('button', { name: 'Buka persetujuan' })
  })

  it('DITOLAK locks the gallery; no resend is offered', async () => {
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
    )
    renderChild(2)

    await screen.findByText('Galeri terkunci sampai orang tua menyetujui.')
    screen.getByText('Persetujuan: Ditolak')
    expect(screen.queryByRole('button', { name: /Kirim ulang/ })).toBeNull()
  })

  it('a member with no consent row reads "not yet submitted", not "pending"', async () => {
    server.use(
      http.get('/api/sessions/1', () =>
        ok(
          (() => {
            const detail = detailFixture({ status: 'OPENED' })
            detail.groups[0].members = [
              { child_id: 2, name: 'Aisyah Putri', age: 8, school: 'SD Cendekia', consent: null },
            ]
            detail.groups[0].member_count = 1
            return detail
          })(),
        ),
      ),
    )
    renderChild(2)

    await screen.findByText('Galeri terkunci sampai orang tua menyetujui.')
    screen.getByText('Persetujuan: Belum diserahkan')
    expect(screen.queryByText('Persetujuan: Menunggu')).toBeNull()
  })

  it('FAS never sees the consent send action', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))))
    renderChild(2, { session: FAS_SESSION() })

    await screen.findByText('Galeri terkunci sampai orang tua menyetujui.')
    expect(screen.queryByRole('button', { name: /Kirim persetujuan/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Kirim ulang/ })).toBeNull()
  })

  it('an unknown child shows not-found with a way back to the directory', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))))
    renderApp(null, {
      routes: CHILD_ROUTES(),
      route: '/sessions/1/gallery/children/99',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Anak ini tidak ada di sesi ini.')
    expect(screen.getByRole('button', { name: 'Kembali ke galeri' })).toBeInTheDocument()
  })
})

describe('child gallery upload (honest-file rule)', () => {
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
    renderChild(3, { gate: true })
    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Belum ada foto.') !== null)
    // The default frame is selected before the capture, so frame_id rides along.
    await until(() => screen.queryByTitle('Bingkai Cantik') !== null)

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
      photosHandler('3', { photos: [] }),
      http.post('/api/sessions/1/topics/3/children/3/photos', () => {
        uploadCalls += 1
        return HttpResponse.json({ error: { code: 'PHOTO_LIMIT', message: 'full' } })
      }),
    )
    renderChild(3)

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
      photosHandler('3', { photos: [] }),
      http.post('/api/sessions/1/topics/3/children/3/photos', () => {
        uploadCalls += 1
        return HttpResponse.json({ error: { code: 'PHOTO_LIMIT', message: 'full' } })
      }),
    )
    renderChild(3)

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
      photosHandler('3', { photos: [] }),
      http.post('/api/sessions/1/topics/3/children/3/photos', () => {
        uploadCalls += 1
        return HttpResponse.json({ error: { code: 'PHOTO_LIMIT', message: 'full' } })
      }),
    )
    const user = userEvent.setup()
    renderChild(3)

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
})

describe('child gallery topic selection', () => {
  it('shows no topic picker for a single-topic session', async () => {
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('3', { photos: [] }),
    )
    renderChild(3)

    await screen.findByText('Belum ada foto.')
    expect(screen.queryByRole('combobox', { name: 'Topik' })).toBeNull()
    screen.getByRole('combobox', { name: 'Bingkai' })
  })

  it('switches the photo scope when another topic is picked', async () => {
    let lastPath = ''
    const detail: SessionDetail = detailFixture({ status: 'OPENED' })
    detail.topics.push({
      stid: 4,
      topic_id: 10,
      name: 'Tenun Nusantara',
      sort_order: 2,
      activities: [],
    })
    server.use(
      http.get('/api/sessions/1', () => ok(detail)),
      framesHandler(),
      http.get('/api/sessions/1/topics/:stid/children/3/photos', ({ request }) => {
        lastPath = new URL(request.url).pathname
        return ok([])
      }),
    )
    const user = userEvent.setup()
    renderChild(3)

    await screen.findByText('Belum ada foto.')
    expect(lastPath).toBe('/api/sessions/1/topics/3/children/3/photos')

    await user.click(screen.getByRole('combobox', { name: 'Topik' }))
    await user.click(await screen.findByText('Tenun Nusantara'))

    await waitFor(() =>
      expect(lastPath).toBe('/api/sessions/1/topics/4/children/3/photos'),
    )
  })
})

describe('child gallery photo actions', () => {
  it('candidate toggle PATCHes and refetches through the own-200 path', async () => {
    let patched: unknown = null
    const photosRef: { photos: PhotoItem[] } = { photos: [photoItem(7)] }
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      photosHandler('3', photosRef),
      http.patch('/api/sessions/1/topics/3/children/3/photos/7', async ({ request }) => {
        patched = await request.json()
        photosRef.photos = [photoItem(7, { is_candidate: true })]
        return ok({ photo: photoItem(7, { is_candidate: true }) })
      }),
    )
    const user = userEvent.setup()
    renderChild(3)

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
      photosHandler('3', photosRef),
      http.delete('/api/sessions/1/topics/3/children/3/photos/7', () => {
        deleted += 1
        photosRef.photos = []
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderChild(3)

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
      http.get('/api/sessions/1/topics/3/children/3/photos', () => {
        photoCalls += 1
        return ok(photosRef.photos)
      }),
    )
    renderChild(3)

    await screen.findByAltText('/uploads/photos/7.jpg')
    expect(photoCalls).toBe(1)

    // The copy lands new photos server-side; the refetch pulls them in.
    photosRef.photos = [photoItem(7), photoItem(8)]
    act(() => emitSignal('copy.completed', { rev: 1 }))
    await waitFor(() => expect(photoCalls).toBe(2))
    await screen.findByAltText('/uploads/photos/8.jpg')
  })
})
