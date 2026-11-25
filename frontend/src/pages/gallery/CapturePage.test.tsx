/**
 * Capture page (`/sessions/:sid/gallery/children/:cid/capture`): the camera
 * studio mounted beside the real session/member context. It loads only the
 * session program's frames, fuses the selected frame into the saved JPEG, and
 * uploads it with `frame_id` — then returns to the child gallery with the
 * topic intact. The consent gate blocks a direct URL exactly like the gallery.
 */
import { describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { delay, http, HttpResponse } from 'msw'
import { useSearchParams } from 'react-router'
import CapturePage from './CapturePage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, ok } from '../../test/handlers'
import { detailFixture, photoItem } from '../../test/helpers.cov-session'
import type { RouteObject } from 'react-router'

function ChildStub() {
  const [params] = useSearchParams()
  return <div>CHILD GALLERY {params.get('stid')}</div>
}

const CAPTURE_ROUTES: RouteObject[] = [
  { path: '/sessions/:sid/gallery', element: <div>GALLERY INDEX</div> },
  { path: '/sessions/:sid/gallery/children/:cid', element: <ChildStub /> },
  { path: '/sessions/:sid/gallery/children/:cid/capture', element: <CapturePage /> },
]

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })

function framesHandler(frameCalls: string[] = []) {
  return http.get('/api/frames/options', ({ request }) => {
    frameCalls.push(new URL(request.url).searchParams.get('program_id') ?? '')
    return ok([
      { id: 11, name: 'Bingkai Hutan', image_url: '/uploads/frames/11.png' },
      { id: 12, name: 'Bingkai Kota', image_url: '/uploads/frames/12.png' },
    ])
  })
}

function renderCapture(cid: 2 | 3 = 3) {
  return renderApp(null, {
    routes: CAPTURE_ROUTES,
    route: `/sessions/1/gallery/children/${cid}/capture?stid=3`,
    session: ADMIN_SESSION(),
  })
}

/* ---------- media / canvas stubs (jsdom gaps) ---------- */

function installMedia(): void {
  const track = {
    stop: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  }
  Object.defineProperty(navigator, 'mediaDevices', {
    value: {
      getUserMedia: vi.fn(async () => ({
        getTracks: () => [track],
        getVideoTracks: () => [track],
      })),
    },
    configurable: true,
  })
}

function removeMediaDevices(): void {
  Reflect.deleteProperty(navigator, 'mediaDevices')
}

function stubPlay(): void {
  Object.defineProperty(HTMLMediaElement.prototype, 'play', {
    value: vi.fn(() => Promise.resolve()),
    configurable: true,
    writable: true,
  })
}

function stubObjectUrls(): void {
  Object.defineProperty(URL, 'createObjectURL', {
    value: vi.fn(() => 'blob:fallback-1'),
    configurable: true,
  })
  Object.defineProperty(URL, 'revokeObjectURL', {
    value: vi.fn(),
    configurable: true,
  })
}

function stubCanvas(): void {
  Object.defineProperty(HTMLCanvasElement.prototype, 'getContext', {
    value: vi.fn(() => ({
      drawImage: vi.fn(),
      scale: vi.fn(),
      save: vi.fn(),
      restore: vi.fn(),
      translate: vi.fn(),
      fillRect: vi.fn(),
      fillStyle: '',
    })),
    configurable: true,
    writable: true,
  })
  Object.defineProperty(HTMLCanvasElement.prototype, 'toDataURL', {
    value: vi.fn(() => 'data:image/jpeg;base64,preview'),
    configurable: true,
    writable: true,
  })
  Object.defineProperty(HTMLCanvasElement.prototype, 'toBlob', {
    value: vi.fn((callback: (blob: Blob | null) => void) => {
      callback(new Blob(['jpeg'], { type: 'image/jpeg' }))
    }),
    configurable: true,
    writable: true,
  })
}

function stubFrameImages(): void {
  class FakeImage {
    crossOrigin: string | null = null
    onload: (() => void) | null = null
    onerror: (() => void) | null = null
    naturalWidth = 200
    naturalHeight = 400
    private value = ''
    get src(): string {
      return this.value
    }
    set src(next: string) {
      this.value = next
      Promise.resolve().then(() => this.onload?.())
    }
  }
  vi.stubGlobal('Image', FakeImage)
}

function makeVideoReady(video: HTMLVideoElement): void {
  Object.defineProperty(video, 'videoWidth', { value: 640, configurable: true })
  Object.defineProperty(video, 'videoHeight', { value: 480, configurable: true })
  // Shutter readiness is event-driven and needs a drawable frame, not just
  // metadata: announce HAVE_CURRENT_DATA with the dimensions.
  Object.defineProperty(video, 'readyState', { value: 2, configurable: true })
  fireEvent(video, new Event('loadedmetadata'))
  fireEvent(video, new Event('canplay'))
}

async function captureAndWaitForSave(): Promise<void> {
  const video = screen.getByLabelText('Kamera') as HTMLVideoElement
  makeVideoReady(video)
  await waitFor(() =>
    expect(screen.getByRole('button', { name: 'Ambil foto' })).not.toBeDisabled(),
  )
  fireEvent.click(screen.getByRole('button', { name: 'Ambil foto' }))
  await screen.findByRole('button', { name: 'Simpan foto' })
}

function frameIdField(raw: string, id: number): boolean {
  return new RegExp(`name="frame_id"\\r?\\n(?:Content-Type:[^\\n]*\\r?\\n)?\\r?\\n${id}\\r?\\n`).test(raw)
}

describe('capture page', () => {
  it('loads the program frames and saves the fused photo back to the gallery', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    const frameCalls: string[] = []
    const captured: { raw: string | null } = { raw: null }
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(frameCalls),
      http.post('/api/sessions/1/topics/3/children/3/photos', async ({ request }) => {
        captured.raw = await request.text()
        await delay(50)
        return ok({ photo: photoItem(7) })
      }),
    )
    renderCapture()

    // First program frame is the default selection.
    const first = await screen.findByRole(
      'button',
      { name: /Bingkai Hutan/ },
      { timeout: 5000 },
    )
    expect(first).toHaveAttribute('aria-pressed', 'true')
    expect(frameCalls).toEqual(['1'])

    await captureAndWaitForSave()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Simpan foto' }))

    await waitFor(() => expect(captured.raw).not.toBeNull())
    expect(captured.raw).toContain('name="file"')
    expect(captured.raw).toContain('Content-Type: image/jpeg')
    expect(frameIdField(captured.raw as string, 11)).toBe(true)

    // Back on the child gallery, topic preserved.
    await screen.findByText('CHILD GALLERY 3')
  })

  it('uses the frame picked during review, not the initial default', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    const captured: { raw: string | null } = { raw: null }
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      http.post('/api/sessions/1/topics/3/children/3/photos', async ({ request }) => {
        captured.raw = await request.text()
        return ok({ photo: photoItem(7) })
      }),
    )
    renderCapture()

    await screen.findByRole('button', { name: /Bingkai Hutan/ })
    await captureAndWaitForSave()

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: /Bingkai Kota/ }))
    const save = screen.getByRole('button', { name: 'Simpan foto' })
    await waitFor(() => expect(save).not.toBeDisabled())
    await user.click(save)

    await waitFor(() => expect(captured.raw).not.toBeNull())
    expect(frameIdField(captured.raw as string, 12)).toBe(true)
  })

  it('keeps the review page and shows PHOTO_LIMIT inline', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      http.post('/api/sessions/1/topics/3/children/3/photos', () =>
        HttpResponse.json({ error: { code: 'PHOTO_LIMIT', message: 'full' } }),
      ),
    )
    renderCapture()

    await screen.findByRole('button', { name: /Bingkai Hutan/ })
    await captureAndWaitForSave()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Simpan foto' }))

    await screen.findByText('Maksimal 10 foto per anak per topik.')
    // Still in the studio — nothing was saved, so there is nothing to go back to.
    expect(screen.queryByText(/CHILD GALLERY/)).toBeNull()
    // The save button is mid-spin while the failed request unwinds, so its
    // accessible name carries the loading icon; match the label itself.
    expect(screen.getByRole('button', { name: /Simpan foto/ })).toBeInTheDocument()
  })

  it('uses the upload fallback through the same crop → review → save flow', async () => {
    removeMediaDevices()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    stubObjectUrls()
    const captured: { raw: string | null } = { raw: null }
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      http.post('/api/sessions/1/topics/3/children/3/photos', async ({ request }) => {
        captured.raw = await request.text()
        return ok({ photo: photoItem(7) })
      }),
    )
    const { container } = renderCapture()

    await screen.findByRole('button', { name: /Bingkai Hutan/ })
    screen.getByText('Peramban ini tidak dapat membuka kamera. Unggah gambar sebagai gantinya.')

    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, {
      target: { files: [new File([new Uint8Array(16)], 'foto.jpg', { type: 'image/jpeg' })] },
    })

    await screen.findByAltText('Pratinjau')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Simpan foto' }))

    await waitFor(() => expect(captured.raw).not.toBeNull())
    expect(frameIdField(captured.raw as string, 11)).toBe(true)
    await screen.findByText('CHILD GALLERY 3')
  })

  it('PENDING consent blocks a direct capture URL before the camera mounts', async () => {
    const frameCalls: string[] = []
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(frameCalls),
    )
    const { container } = renderCapture(2)

    await screen.findByText('Galeri terkunci sampai orang tua menyetujui.')
    expect(screen.queryByLabelText('Kamera')).toBeNull()
    expect(container.querySelector('video')).toBeNull()
    expect(frameCalls).toEqual([])
  })

  it('an unknown child shows not-found with a way back to the directory', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))))
    renderApp(null, {
      routes: CAPTURE_ROUTES,
      route: '/sessions/1/gallery/children/99/capture?stid=3',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Anak ini tidak ada di sesi ini.')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Kembali ke galeri' }))
    // The child gallery would be a dead end; the directory is the way back.
    await screen.findByText('GALLERY INDEX')
  })
})
