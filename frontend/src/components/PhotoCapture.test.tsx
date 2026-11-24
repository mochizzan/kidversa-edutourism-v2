/**
 * PhotoCapture (spec F-10 / FL-10 / 06 E28): camera lifecycle (start with the
 * component, stop on unmount or denial) → centre-crop 9:16 canvas capture →
 * optional frame overlay fused on top → transient "mengunggah…" preview that
 * exists only while the caller reports `uploading`, plus the CAMERA_DENIED
 * upload-fallback picker. jsdom ships none of the media/canvas-paint/blob-URL
 * APIs — the getUserMedia, canvas, Image and URL stacks are stubbed per test.
 *
 * The CAMERA_DENIED upload-fallback picker also carries the ONE upload
 * pre-check (jpg/jpeg/png ≤50 MB shared gate, spec 09 Pembaruan
 * 2026-11-10):
 * oversize or wrong-format picks are blocked with an i18n toast and never
 * reach `onCapture` (no request is issued).
 *
 * Labels are pinned to the ID translations of `session:gallery.capture.*`,
 * so a missing key (i18next falling back to the raw key text) fails the
 * assertions instead of passing through unnoticed.
 */
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import PhotoCapture from './PhotoCapture'
import { IMAGE_ACCEPT } from '../pages/masters/shared'
import { renderApp } from '../test/render'

const DENIED_TEXT = 'Izinkan akses kamera atau unggah gambar dari perangkat.'

/** PhotoCapture's UI strings: session:gallery.capture.* in the default ID language. */
const CAMERA_LABEL = 'Kamera'
const TAKE_TEXT = 'Ambil foto'
const UPLOADING_TEXT = 'mengunggah…'
const FALLBACK_TEXT = 'Unggah gambar'

function cameraVideo(): HTMLVideoElement {
  return screen.getByLabelText(CAMERA_LABEL) as HTMLVideoElement
}

/** The button whose label text is `Ambil foto` (icon pollutes aria names). */
function takeButton(): HTMLButtonElement {
  const button = screen.getByText(TAKE_TEXT).closest('button')
  if (!button) throw new Error('take-photo button not found')
  return button as HTMLButtonElement
}

function fallbackButton(): HTMLButtonElement {
  const button = screen.getByText(FALLBACK_TEXT).closest('button')
  if (!button) throw new Error('upload-fallback button not found')
  return button as HTMLButtonElement
}

/* ---------- stubs for the media / canvas / blob-URL stacks ---------- */

function installMedia(outcome: 'resolve' | 'reject') {
  const track = { stop: vi.fn() }
  const getUserMedia = vi.fn(() =>
    outcome === 'resolve'
      ? Promise.resolve({ getTracks: () => [track] })
      : Promise.reject(new Error('PermissionDenied')),
  )
  Object.defineProperty(navigator, 'mediaDevices', {
    value: { getUserMedia },
    configurable: true,
  })
  return { getUserMedia, track }
}

function removeMediaDevices(): void {
  Reflect.deleteProperty(navigator, 'mediaDevices')
}

function stubObjectUrls() {
  let seq = 0
  const createObjectURL = vi.fn(() => `blob:preview-${(seq += 1)}`)
  const revokeObjectURL = vi.fn()
  Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true })
  Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true })
  return { createObjectURL, revokeObjectURL }
}

function stubCanvas(options: { ctx?: unknown; blob?: Blob | null } = {}) {
  const drawImage = vi.fn()
  const getContext = vi.fn(() => ('ctx' in options ? options.ctx : { drawImage }))
  const toBlob = vi.fn((callback: (blob: Blob | null) => void) => {
    const result = 'blob' in options ? options.blob : new Blob(['jpeg'], { type: 'image/jpeg' })
    callback(result ?? null)
  })
  Object.defineProperty(HTMLCanvasElement.prototype, 'getContext', {
    value: getContext,
    configurable: true,
    writable: true,
  })
  Object.defineProperty(HTMLCanvasElement.prototype, 'toBlob', {
    value: toBlob,
    configurable: true,
    writable: true,
  })
  return { drawImage, getContext, toBlob }
}

/** jsdom reports videoWidth/videoHeight 0 — shadow the instance to "have a frame". */
function makeVideoReady(video: HTMLVideoElement, width = 640, height = 480): void {
  Object.defineProperty(video, 'videoWidth', { value: width, configurable: true })
  Object.defineProperty(video, 'videoHeight', { value: height, configurable: true })
}

/** Lets the getUserMedia promise (resolve/reject) settle inside act. */
async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

/** Test-side handle for the `uploading` prop of the captured preview. */
let setUpload: ((uploading: boolean) => void) | null = null

function CaptureHarness({ onCapture }: { onCapture: (file: File) => void }) {
  const [uploading, setUploading] = useState(false)
  setUpload = setUploading
  return <PhotoCapture onCapture={onCapture} uploading={uploading} />
}

/* ---------- scenarios ---------- */

describe('PhotoCapture denied fallback', () => {
  it('renders the camera-denied alert with the upload picker when getUserMedia is unavailable', () => {
    removeMediaDevices()
    const { container } = renderApp(<PhotoCapture onCapture={vi.fn()} />)

    const alert = screen.getByRole('alert')
    expect(alert).toHaveTextContent(DENIED_TEXT)
    expect(fallbackButton()).toBeInTheDocument()
    // The picker advertises the ONE shared accept list (MIMEs + extensions).
    expect(container.querySelector('input[type="file"]')).toHaveAttribute('accept', IMAGE_ACCEPT)
  })

  it('flips into the same denied UI when permission is rejected', async () => {
    const { getUserMedia } = installMedia('reject')
    renderApp(<PhotoCapture onCapture={vi.fn()} />)

    const alert = await screen.findByRole('alert') // the pending promise settled
    expect(getUserMedia).toHaveBeenCalledTimes(1)
    expect(alert).toHaveTextContent(DENIED_TEXT)
    expect(fallbackButton()).toBeInTheDocument()
  })

  it('emits the picked file through the fallback picker without any upload request', async () => {
    removeMediaDevices()
    stubObjectUrls()
    const fetchSpy = vi.spyOn(globalThis, 'fetch')
    const xhrOpen = vi.spyOn(XMLHttpRequest.prototype, 'open')
    const onCapture = vi.fn()
    const { container } = renderApp(<PhotoCapture onCapture={onCapture} />)

    const input = container.querySelector('input[type="file"]')
    expect(input).not.toBeNull()
    const picked = new File(['isi-gambar'], 'foto-galeri.jpg', { type: 'image/jpeg' })
    fireEvent.change(input as Element, { target: { files: [picked] } })

    await waitFor(() => expect(onCapture).toHaveBeenCalledTimes(1))
    expect(onCapture.mock.calls[0][0]).toBe(picked)
    expect(fetchSpy).not.toHaveBeenCalled() // beforeUpload returned false → no antd request
    expect(xhrOpen).not.toHaveBeenCalled()
  })

  it('blocks an oversize fallback pick before it reaches the caller', async () => {
    removeMediaDevices()
    stubObjectUrls()
    const onCapture = vi.fn()
    const { container } = renderApp(<PhotoCapture onCapture={onCapture} />)

    const input = container.querySelector('input[type="file"]')
    const big = new File(['isi-gambar'], 'besar.jpg', { type: 'image/jpeg' })
    Object.defineProperty(big, 'size', { value: 51 * 1024 * 1024 })
    fireEvent.change(input as Element, { target: { files: [big] } })

    // The pre-check surfaces the gate message and never hands the file over.
    await screen.findByText('Foto harus berformat JPG, JPEG, atau PNG dan maksimal 50 MB.')
    expect(onCapture).not.toHaveBeenCalled()
    expect(document.querySelector('.kv-capture__preview')).toBeNull()
  })

  it('blocks a wrong-format fallback pick before it reaches the caller', async () => {
    removeMediaDevices()
    stubObjectUrls()
    const onCapture = vi.fn()
    const { container } = renderApp(<PhotoCapture onCapture={onCapture} />)

    const input = container.querySelector('input[type="file"]')
    const ico = new File(['isi-gambar'], 'ikon.ico', { type: 'image/vnd.microsoft.icon' })
    fireEvent.change(input as Element, { target: { files: [ico] } })

    await screen.findByText('Foto harus berformat JPG, JPEG, atau PNG dan maksimal 50 MB.')
    expect(onCapture).not.toHaveBeenCalled()
    expect(document.querySelector('.kv-capture__preview')).toBeNull()
  })
})

describe('PhotoCapture camera capture', () => {
  it('previews the stream, guards capture until a frame exists, then emits a JPEG with a transient preview', async () => {
    const { getUserMedia } = installMedia('resolve')
    stubObjectUrls()
    stubCanvas()
    const onCapture = vi.fn()
    renderApp(<CaptureHarness onCapture={onCapture} />)
    await flush()

    const video = cameraVideo()
    expect(video.tagName).toBe('VIDEO')
    expect(video).toHaveAttribute('aria-label', CAMERA_LABEL)
    expect(getUserMedia).toHaveBeenCalledWith({ video: { facingMode: 'environment' } })

    // jsdom videoWidth/videoHeight are 0 → not ready → capture is a no-op.
    fireEvent.click(takeButton())
    expect(onCapture).not.toHaveBeenCalled()

    makeVideoReady(video)
    fireEvent.click(takeButton())
    expect(onCapture).toHaveBeenCalledTimes(1)
    const [file] = onCapture.mock.calls[0] as [File]
    expect(file).toBeInstanceOf(File)
    expect(file.name).toBe('capture.jpg')
    expect(file.type).toBe('image/jpeg')

    // Transient local preview: shown exactly while the caller reports uploading.
    const uploadingAlt = UPLOADING_TEXT
    expect(screen.queryByAltText(uploadingAlt)).toBeNull()
    act(() => setUpload?.(true))
    const preview = screen.getByAltText(uploadingAlt)
    expect(preview.tagName).toBe('IMG')
    expect(preview).toHaveAttribute('alt', uploadingAlt)
    act(() => setUpload?.(false))
    expect(screen.queryByAltText(uploadingAlt)).toBeNull()
  })

  it('fuses a loaded frame overlay on top of the captured frame', async () => {
    installMedia('resolve')
    stubObjectUrls()
    const { drawImage } = stubCanvas()
    const frameImages: Array<{ complete: boolean; naturalWidth: number }> = []
    class FakeFrameImage {
      crossOrigin: string | null = null
      src = ''
      readonly complete = true
      readonly naturalWidth = 1080
      constructor() {
        frameImages.push(this)
      }
    }
    vi.stubGlobal('Image', FakeFrameImage)

    const onCapture = vi.fn()
    renderApp(<PhotoCapture onCapture={onCapture} frameUrl="/uploads/f.png" />)
    await flush()

    const video = cameraVideo()
    makeVideoReady(video)
    fireEvent.click(takeButton())

    expect(drawImage).toHaveBeenCalledTimes(2)
    expect(drawImage.mock.calls[0][0]).toBe(video) // crop pass draws the video frame
    expect(drawImage.mock.calls[1][0]).toBe(frameImages[0]) // overlay pass draws the frame
    expect(onCapture).toHaveBeenCalledTimes(1)
    expect((onCapture.mock.calls[0][0] as File).name).toBe('capture.jpg')
    expect((onCapture.mock.calls[0][0] as File).type).toBe('image/jpeg')
  })

  it('emits nothing when the canvas cannot provide a 2d context', async () => {
    installMedia('resolve')
    stubObjectUrls()
    const { getContext, toBlob } = stubCanvas({ ctx: null })
    const onCapture = vi.fn()
    renderApp(<PhotoCapture onCapture={onCapture} />)
    await flush()

    makeVideoReady(cameraVideo())
    fireEvent.click(takeButton())

    expect(getContext).toHaveBeenCalledTimes(1)
    expect(toBlob).not.toHaveBeenCalled()
    expect(onCapture).not.toHaveBeenCalled()
    expect(screen.queryByAltText(UPLOADING_TEXT)).toBeNull()
  })

  it('emits nothing when the canvas yields no blob', async () => {
    installMedia('resolve')
    stubObjectUrls()
    stubCanvas({ blob: null })
    const onCapture = vi.fn()
    renderApp(<PhotoCapture onCapture={onCapture} />)
    await flush()

    makeVideoReady(cameraVideo())
    fireEvent.click(takeButton())

    expect(onCapture).not.toHaveBeenCalled()
    expect(screen.queryByAltText(UPLOADING_TEXT)).toBeNull()
  })

  it('stops the camera tracks and revokes the held preview on unmount', async () => {
    const { track } = installMedia('resolve')
    const { revokeObjectURL } = stubObjectUrls()
    stubCanvas()
    const onCapture = vi.fn()
    const { unmount } = renderApp(<PhotoCapture onCapture={onCapture} />)
    await flush()

    makeVideoReady(cameraVideo())
    fireEvent.click(takeButton())
    expect(onCapture).toHaveBeenCalledTimes(1)
    expect(track.stop).not.toHaveBeenCalled()
    expect(revokeObjectURL).not.toHaveBeenCalled()

    unmount()

    expect(track.stop).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')
  })

  it('revokes the previous preview when a new capture replaces it', async () => {
    installMedia('resolve')
    const { revokeObjectURL } = stubObjectUrls()
    stubCanvas()
    const onCapture = vi.fn()
    renderApp(<PhotoCapture onCapture={onCapture} />)
    await flush()

    makeVideoReady(cameraVideo())
    fireEvent.click(takeButton())
    fireEvent.click(takeButton())

    expect(onCapture).toHaveBeenCalledTimes(2)
    expect(revokeObjectURL).toHaveBeenCalledTimes(1) // exactly one blob URL held at a time
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:preview-1')
  })

  it('stops a camera stream that resolves after unmount', async () => {
    const { track } = installMedia('resolve')
    const { unmount } = renderApp(<PhotoCapture onCapture={vi.fn()} />)

    unmount() // synchronous: the getUserMedia promise has not settled yet
    await flush()

    expect(track.stop).toHaveBeenCalledTimes(1)
  })
})
