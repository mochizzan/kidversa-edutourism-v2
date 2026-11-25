/**
 * CameraStudio (child gallery capture page): the strict-ratio viewfinder
 * (9:16 / 16:9, never overflowing), cover-cropped live/captured image,
 * contain-fitted frame overlay, preview-only composition grids, independent
 * mirror switches, and the review step where the frame can still change before
 * the image + frame are fused into ONE JPEG.
 *
 * jsdom ships none of the media/canvas-paint APIs — getUserMedia, canvas,
 * Image and blob URLs are stubbed per test.
 */
import { StrictMode, useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import CameraStudio, { containRect, coverCropRect } from './CameraStudio'
import { renderApp } from '../../test/render'
import type { FrameOption } from './shared'

const DENIED_TEXT = 'Izinkan akses kamera atau unggah gambar dari perangkat.'
const UNSUPPORTED_TEXT = 'Peramban ini tidak dapat membuka kamera. Unggah gambar sebagai gantinya.'
const FRAMES: FrameOption[] = [
  { id: 11, name: 'Bingkai Hutan', image_url: '/uploads/frames/11.png' },
  { id: 12, name: 'Bingkai Kota', image_url: '/uploads/frames/12.png' },
]

/* ---------- stubs ---------- */

interface FakeTrack {
  stop: ReturnType<typeof vi.fn>
  addEventListener: ReturnType<typeof vi.fn>
  removeEventListener: ReturnType<typeof vi.fn>
  /** Simulate the camera device disappearing mid-session. */
  end: () => void
}

interface FakeStream {
  getTracks: () => FakeTrack[]
  getVideoTracks: () => FakeTrack[]
}

function makeTrack(): FakeTrack {
  const listeners = new Set<() => void>()
  return {
    stop: vi.fn(),
    addEventListener: vi.fn((type: string, listener: () => void) => {
      if (type === 'ended') listeners.add(listener)
    }),
    removeEventListener: vi.fn((type: string, listener: () => void) => {
      if (type === 'ended') listeners.delete(listener)
    }),
    end: () => {
      for (const listener of [...listeners]) listener()
    },
  }
}

function mediaError(name: string): Error {
  const error = new Error(name)
  error.name = name
  return error
}

/**
 * Install a fake `navigator.mediaDevices`. Each call consumes one step:
 * `'resolve'` yields a fresh stream, an Error rejects with that name. The last
 * step repeats, so a one-step list means "always this outcome".
 */
function installMedia(...steps: Array<'resolve' | Error>) {
  const tracks: FakeTrack[] = []
  const streams: FakeStream[] = []
  let call = 0
  const getUserMedia = vi.fn(() => {
    const step = steps[call] ?? steps[steps.length - 1] ?? 'resolve'
    call += 1
    if (step instanceof Error) return Promise.reject(step)
    const track = makeTrack()
    const stream: FakeStream = {
      getTracks: () => [track],
      getVideoTracks: () => [track],
    }
    tracks.push(track)
    streams.push(stream)
    return Promise.resolve(stream)
  })
  Object.defineProperty(navigator, 'mediaDevices', {
    value: { getUserMedia },
    configurable: true,
  })
  return { getUserMedia, tracks, streams }
}

function removeMediaDevices(): void {
  Reflect.deleteProperty(navigator, 'mediaDevices')
}

/** jsdom's HTMLMediaElement.play is not implemented — stub per test. */
function stubPlay(
  impl: () => Promise<void> | void = () => Promise.resolve(),
): ReturnType<typeof vi.fn> {
  const play = vi.fn(impl)
  Object.defineProperty(HTMLMediaElement.prototype, 'play', {
    value: play,
    configurable: true,
    writable: true,
  })
  return play
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

function stubCanvas() {
  const drawImage = vi.fn()
  const scale = vi.fn()
  const context = {
    drawImage,
    scale,
    save: vi.fn(),
    restore: vi.fn(),
    translate: vi.fn(),
    fillRect: vi.fn(),
    fillStyle: '',
  }
  const getContext = vi.fn(() => context)
  Object.defineProperty(HTMLCanvasElement.prototype, 'getContext', {
    value: getContext,
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
  return { context, drawImage, scale, getContext }
}

interface FakeImageInstance {
  src: string
  crossOrigin: string | null
  onload: (() => void) | null
  onerror: (() => void) | null
  naturalWidth: number
  naturalHeight: number
}

function stubFrameImages(): FakeImageInstance[] {
  const created: FakeImageInstance[] = []
  class FakeImage implements FakeImageInstance {
    crossOrigin: string | null = null
    onload: (() => void) | null = null
    onerror: (() => void) | null = null
    naturalWidth = 200
    naturalHeight = 400
    private value = ''
    constructor() {
      created.push(this)
    }
    get src(): string {
      return this.value
    }
    set src(next: string) {
      this.value = next
      Promise.resolve().then(() => {
        if (next.includes('broken')) {
          this.onerror?.()
          return
        }
        // A decoded-but-empty image: onload fired, nothing can be drawn.
        if (next.includes('zero')) {
          this.naturalWidth = 0
          this.naturalHeight = 0
        }
        this.onload?.()
      })
    }
  }
  vi.stubGlobal('Image', FakeImage)
  return created
}

function makeVideoReady(video: HTMLVideoElement, width = 640, height = 480): void {
  Object.defineProperty(video, 'videoWidth', { value: width, configurable: true })
  Object.defineProperty(video, 'videoHeight', { value: height, configurable: true })
  // HAVE_CURRENT_DATA: the studio only unlocks the shutter once a frame is
  // actually drawable, so tests must announce both dimensions and data.
  Object.defineProperty(video, 'readyState', { value: 2, configurable: true })
  fireEvent(video, new Event('loadedmetadata'))
  fireEvent(video, new Event('canplay'))
}

async function flush(): Promise<void> {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

function studio(overrides: Partial<Parameters<typeof CameraStudio>[0]> = {}) {
  return (
    <CameraStudio
      frames={FRAMES}
      framesLoading={false}
      framesError={null}
      onReloadFrames={vi.fn()}
      frameId={11}
      onFrameChange={vi.fn()}
      uploading={false}
      onSave={vi.fn()}
      {...overrides}
    />
  )
}

function renderStudio(
  overrides: Partial<Parameters<typeof CameraStudio>[0]> = {},
): ReturnType<typeof renderApp> {
  return renderApp(studio(overrides))
}

/** Production mounts under StrictMode (main.tsx); effects run twice there. */
function renderStrictStudio(
  overrides: Partial<Parameters<typeof CameraStudio>[0]> = {},
): ReturnType<typeof renderApp> {
  return renderApp(<StrictMode>{studio(overrides)}</StrictMode>)
}

/** Controlled harness for tests that change the frame: the page owns the
 * selection, so the studio alone cannot move `frameId`. */
function renderStatefulStudio(
  onSave: (file: File, frameId: number | null) => void,
): ReturnType<typeof renderApp> {
  function Harness() {
    const [frameId, setFrameId] = useState<number | null>(11)
    return (
      <CameraStudio
        frames={FRAMES}
        framesLoading={false}
        framesError={null}
        onReloadFrames={vi.fn()}
        frameId={frameId}
        onFrameChange={setFrameId}
        uploading={false}
        onSave={onSave}
      />
    )
  }
  return renderApp(<Harness />)
}

/* ---------- geometry ---------- */

describe('capture geometry', () => {
  it('centre-crops a landscape source to 9:16', () => {
    expect(coverCropRect(1920, 1080, 9 / 16)).toEqual({
      x: 656,
      y: 0,
      width: 608,
      height: 1080,
    })
  })

  it('keeps a source that already matches the target ratio', () => {
    expect(coverCropRect(1080, 1920, 9 / 16)).toEqual({
      x: 0,
      y: 0,
      width: 1080,
      height: 1920,
    })
  })

  it('fits an overlay without stretching it', () => {
    expect(containRect(100, 200, 400, 400)).toEqual({
      x: 100,
      y: 0,
      width: 200,
      height: 400,
    })
  })
})

/* ---------- viewfinder controls ---------- */

describe('camera studio controls', () => {
  it('switches between strict 9:16 and 16:9 stages and preview-only grids', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    const { container } = renderStudio()
    await flush()

    const stage = container.querySelector('.kv-camera__stage') as HTMLElement
    expect(stage).toHaveClass('kv-camera__stage--portrait')

    const user = userEvent.setup()
    await user.click(screen.getByText('16:9'))
    expect(stage).toHaveClass('kv-camera__stage--landscape')

    // Default: rule of thirds (two vertical + two horizontal lines).
    expect(container.querySelectorAll('.kv-camera__line')).toHaveLength(4)

    await user.click(screen.getByText('1×1'))
    expect(container.querySelectorAll('.kv-camera__line')).toHaveLength(0)
    expect(container.querySelector('.kv-camera__square')).not.toBeNull()

    await user.click(screen.getByText('Rasio emas'))
    expect(container.querySelector('.kv-camera__square')).toBeNull()
    expect(container.querySelectorAll('.kv-camera__line')).toHaveLength(4)

    await user.click(screen.getByText('Tanpa'))
    expect(container.querySelectorAll('.kv-camera__line')).toHaveLength(0)
    expect(container.querySelector('.kv-camera__square')).toBeNull()
  })

  it('loads the program frames and selects the first by default', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    renderStudio()
    await flush()

    expect(screen.getByRole('button', { name: /Bingkai Hutan/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    )
    expect(screen.getByRole('button', { name: /Bingkai Kota/ })).toHaveAttribute(
      'aria-pressed',
      'false',
    )
    expect(screen.getByRole('button', { name: /Tanpa bingkai/ })).toHaveAttribute(
      'aria-pressed',
      'false',
    )
  })

  it('mirrors the preview with independent horizontal and vertical switches', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    renderStudio()
    await flush()

    const user = userEvent.setup()
    const horizontal = screen.getByRole('switch', { name: 'Cermin horizontal' })
    const vertical = screen.getByRole('switch', { name: 'Cermin vertikal' })
    await user.click(horizontal)

    const video = screen.getByLabelText('Kamera') as HTMLElement
    expect(video.style.getPropertyValue('--kv-mirror-x')).toBe('-1')
    expect(video.style.getPropertyValue('--kv-mirror-y')).toBe('1')

    await user.click(vertical)
    expect(video.style.getPropertyValue('--kv-mirror-y')).toBe('-1')
  })
})

/* ---------- capture + review ---------- */

describe('camera studio capture', () => {
  it('captures, changes the frame in review, mirrors, and saves one fused JPEG', async () => {
    installMedia()
    stubPlay()
    const { drawImage, scale } = stubCanvas()
    const frameImages = stubFrameImages()
    const onSave = vi.fn()
    const { container } = renderStatefulStudio(onSave)
    await flush()

    const video = screen.getByLabelText('Kamera') as HTMLVideoElement
    makeVideoReady(video)
    const shutter = screen.getByRole('button', { name: 'Ambil foto' })
    await waitFor(() => expect(shutter).not.toBeDisabled())

    fireEvent.click(shutter)
    expect(screen.getByAltText('Pratinjau')).toBeInTheDocument()

    // Ratio and grid are composition controls; they lock after capture.
    expect(screen.getByText('16:9').closest('label')).toHaveClass(
      'ant-segmented-item-disabled',
    )

    const user = userEvent.setup()
    await user.click(screen.getByRole('switch', { name: 'Cermin horizontal' }))
    await user.click(screen.getByRole('button', { name: /Bingkai Kota/ }))

    const save = screen.getByRole('button', { name: 'Simpan foto' })
    await waitFor(() => expect(save).not.toBeDisabled())
    await user.click(save)

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    const [file, frameId] = onSave.mock.calls[0] as [File, number | null]
    expect(file).toBeInstanceOf(File)
    expect(file.type).toBe('image/jpeg')
    expect(frameId).toBe(12)

    // The frame bytes are fused on top of the mirrored capture: one draw for
    // the video crop, one for the frame selected during review (not the
    // initial default).
    expect(drawImage).toHaveBeenCalledWith(video, expect.any(Number), expect.any(Number), expect.any(Number), expect.any(Number), 0, 0, expect.any(Number), expect.any(Number))
    const selectedFrameImage = frameImages.find(
      (image) => image.src === '/uploads/frames/12.png',
    )
    expect(selectedFrameImage).toBeDefined()
    expect(drawImage.mock.calls.some(([source]) => source === selectedFrameImage)).toBe(true)
    expect(scale).toHaveBeenCalledWith(-1, 1)

    // The preview stage holds the review image, not the live video.
    expect(container.querySelector('video')).toBeNull()
  })

  it('saves without a frame when the frame image cannot be decoded', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    const onSave = vi.fn()
    renderStudio({
      onSave,
      frames: [{ id: 99, name: 'Bingkai Rusak', image_url: '/uploads/frames/broken.png' }],
      frameId: 99,
    })
    await flush()

    await screen.findByText('Bingkai tidak dapat dimuat — foto akan disimpan tanpa bingkai.')

    const video = screen.getByLabelText('Kamera') as HTMLVideoElement
    makeVideoReady(video)
    const shutter = screen.getByRole('button', { name: 'Ambil foto' })
    await waitFor(() => expect(shutter).not.toBeDisabled())
    fireEvent.click(shutter)

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Simpan foto' }))
    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(onSave.mock.calls[0][1]).toBeNull()
  })

  it('retake returns to the live view without emitting a file', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    const onSave = vi.fn()
    renderStudio({ onSave })
    await flush()

    makeVideoReady(screen.getByLabelText('Kamera') as HTMLVideoElement)
    const shutter = screen.getByRole('button', { name: 'Ambil foto' })
    await waitFor(() => expect(shutter).not.toBeDisabled())
    fireEvent.click(shutter)

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Ambil ulang' }))

    expect(screen.getByLabelText('Kamera')).toBeInTheDocument()
    expect(screen.queryByAltText('Pratinjau')).toBeNull()
    expect(onSave).not.toHaveBeenCalled()
  })

  it('reattaches the live stream to the remounted video after retake', async () => {
    const { streams } = installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    renderStudio()
    await flush()

    const firstVideo = screen.getByLabelText('Kamera') as HTMLVideoElement
    makeVideoReady(firstVideo)
    const shutter = screen.getByRole('button', { name: 'Ambil foto' })
    await waitFor(() => expect(shutter).not.toBeDisabled())
    fireEvent.click(shutter)

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Ambil ulang' }))

    const secondVideo = screen.getByLabelText('Kamera') as HTMLVideoElement
    expect(secondVideo).not.toBe(firstVideo)
    expect(secondVideo.srcObject).toBe(streams[0])
    // The remounted feed must prove dimensions again before the shutter opens.
    expect(screen.getByRole('button', { name: 'Ambil foto' })).toBeDisabled()
  })
})

/* ---------- denied fallback ---------- */

describe('camera studio denied fallback', () => {
  it('uploads a picked image through the same crop → review → save flow', async () => {
    removeMediaDevices()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    stubObjectUrls()
    const onSave = vi.fn()
    const { container } = renderStudio({ onSave, frameId: null })
    await flush()

    screen.getByText(UNSUPPORTED_TEXT)
    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, {
      target: { files: [new File([new Uint8Array(16)], 'foto.jpg', { type: 'image/jpeg' })] },
    })

    await screen.findByAltText('Pratinjau')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Simpan foto' }))

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(onSave.mock.calls[0][0]).toBeInstanceOf(File)
    expect(onSave.mock.calls[0][1]).toBeNull()
  })

  it('blocks an oversize fallback pick before it reaches review', async () => {
    removeMediaDevices()
    stubCanvas()
    stubFrameImages()
    stubObjectUrls()
    const onSave = vi.fn()
    const { container } = renderStudio({ onSave, frameId: null })
    await flush()

    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    const big = new File([new Uint8Array(16)], 'besar.jpg', { type: 'image/jpeg' })
    Object.defineProperty(big, 'size', { value: 51 * 1024 * 1024 })
    fireEvent.change(input, { target: { files: [big] } })

    await screen.findByText('Foto harus berformat JPG, JPEG, atau PNG dan maksimal 50 MB.')
    expect(screen.queryByAltText('Pratinjau')).toBeNull()
    expect(onSave).not.toHaveBeenCalled()
  })

  it('previews and fuses the selected frame in the denied fallback', async () => {
    installMedia(mediaError('NotAllowedError'))
    stubPlay()
    const { drawImage } = stubCanvas()
    const frameImages = stubFrameImages()
    stubObjectUrls()
    const onSave = vi.fn()
    const { container } = renderStudio({ onSave })
    await screen.findByText(DENIED_TEXT)

    const input = container.querySelector('input[type="file"]') as HTMLInputElement
    fireEvent.change(input, {
      target: { files: [new File([new Uint8Array(16)], 'foto.jpg', { type: 'image/jpeg' })] },
    })
    await screen.findByAltText('Pratinjau')

    // The review must show the same frame that save() will fuse; the old
    // `!denied` gate hid it while still drawing it into the JPEG.
    const overlay = container.querySelector('.kv-camera__frame') as HTMLImageElement
    expect(overlay).not.toBeNull()
    expect(overlay.getAttribute('src')).toBe('/uploads/frames/11.png')

    const user = userEvent.setup()
    const save = screen.getByRole('button', { name: 'Simpan foto' })
    await waitFor(() => expect(save).not.toBeDisabled())
    await user.click(save)

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(onSave.mock.calls[0][1]).toBe(11)
    const selected = frameImages.find((image) => image.src === '/uploads/frames/11.png')
    expect(drawImage.mock.calls.some(([source]) => source === selected)).toBe(true)
  })

  it('does not report a frame id when the frame had no drawable pixels', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    const onSave = vi.fn()
    renderStudio({
      onSave,
      frames: [{ id: 99, name: 'Bingkai Kosong', image_url: '/uploads/frames/zero.png' }],
      frameId: 99,
    })
    await flush()

    makeVideoReady(screen.getByLabelText('Kamera') as HTMLVideoElement)
    const shutter = screen.getByRole('button', { name: 'Ambil foto' })
    await waitFor(() => expect(shutter).not.toBeDisabled())
    fireEvent.click(shutter)

    const user = userEvent.setup()
    const save = screen.getByRole('button', { name: 'Simpan foto' })
    await waitFor(() => expect(save).not.toBeDisabled())
    await user.click(save)

    await waitFor(() => expect(onSave).toHaveBeenCalledTimes(1))
    expect(onSave.mock.calls[0][1]).toBeNull()
  })
})

/* ---------- camera lifecycle regressions ---------- */

describe('camera lifecycle', () => {
  it('shares one acquisition across StrictMode double effects', async () => {
    const { getUserMedia, tracks, streams } = installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    renderStrictStudio()
    await flush()

    expect(getUserMedia).toHaveBeenCalledTimes(1)
    expect(streams).toHaveLength(1)
    expect(tracks[0].stop).not.toHaveBeenCalled()
    const video = screen.getByLabelText('Kamera') as HTMLVideoElement
    expect(video.srcObject).toBe(streams[0])
  })

  it('treats a busy camera as retryable, not as a permission denial', async () => {
    const { getUserMedia, tracks } = installMedia(
      mediaError('NotReadableError'),
      'resolve',
    )
    stubPlay()
    stubCanvas()
    stubFrameImages()
    renderStudio()

    await screen.findByText('Kamera tidak tersedia')
    expect(screen.queryByText(DENIED_TEXT)).toBeNull()
    expect(screen.queryByLabelText('Kamera')).toBeNull()

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    await waitFor(() => expect(getUserMedia).toHaveBeenCalledTimes(2))
    expect(tracks).toHaveLength(1)

    makeVideoReady(screen.getByLabelText('Kamera') as HTMLVideoElement)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Ambil foto' })).not.toBeDisabled(),
    )
  })

  it('shows permission guidance only for an explicit denial', async () => {
    installMedia(mediaError('NotAllowedError'))
    stubPlay()
    stubCanvas()
    stubFrameImages()
    renderStudio()

    await screen.findByText(DENIED_TEXT)
    expect(
      screen.getByText('Kamera tidak tersedia. Unggah gambar untuk memakai alur yang sama.'),
    ).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Coba lagi' })).toBeInTheDocument()
    expect(screen.queryByText('Kamera tidak tersedia')).toBeNull()
  })

  it('asks for a tap when play() throws instead of claiming denial', async () => {
    installMedia()
    let calls = 0
    const play = stubPlay(() => {
      calls += 1
      if (calls === 1) throw new Error('sync play failure')
      return Promise.resolve()
    })
    stubCanvas()
    stubFrameImages()
    renderStudio()

    const resume = await screen.findByRole('button', {
      name: 'Ketuk untuk menyalakan kamera',
    })
    expect(screen.queryByText(DENIED_TEXT)).toBeNull()
    const shutter = screen.getByRole('button', { name: 'Ambil foto' })
    expect(shutter).toBeDisabled()

    const user = userEvent.setup()
    await user.click(resume)
    await waitFor(() => expect(play).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Ketuk untuk menyalakan kamera' }),
      ).toBeNull(),
    )

    makeVideoReady(screen.getByLabelText('Kamera') as HTMLVideoElement)
    await waitFor(() => expect(shutter).not.toBeDisabled())
  })

  it('recovers from a rejected play() with a tap', async () => {
    installMedia()
    let calls = 0
    const play = stubPlay(() => {
      calls += 1
      return calls === 1
        ? Promise.reject(new Error('autoplay blocked'))
        : Promise.resolve()
    })
    stubCanvas()
    stubFrameImages()
    renderStudio()

    const resume = await screen.findByRole('button', {
      name: 'Ketuk untuk menyalakan kamera',
    })
    const user = userEvent.setup()
    await user.click(resume)
    await waitFor(() => expect(play).toHaveBeenCalledTimes(2))
    await waitFor(() =>
      expect(
        screen.queryByRole('button', { name: 'Ketuk untuk menyalakan kamera' }),
      ).toBeNull(),
    )
  })

  it('locks the viewfinder when the camera track ends mid-session', async () => {
    const { tracks } = installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    renderStudio()
    await flush()

    makeVideoReady(screen.getByLabelText('Kamera') as HTMLVideoElement)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Ambil foto' })).not.toBeDisabled(),
    )

    act(() => tracks[0].end())

    await screen.findByText('Kamera tidak tersedia')
    expect(tracks[0].stop).toHaveBeenCalled()
    expect(screen.queryByLabelText('Kamera')).toBeNull()
    expect(screen.getByRole('button', { name: 'Coba lagi' })).toBeInTheDocument()
  })

  it('keeps the shutter closed until a frame is actually drawable', async () => {
    installMedia()
    stubPlay()
    stubCanvas()
    stubFrameImages()
    renderStudio()
    await flush()

    const video = screen.getByLabelText('Kamera') as HTMLVideoElement
    Object.defineProperty(video, 'videoWidth', { value: 640, configurable: true })
    Object.defineProperty(video, 'videoHeight', { value: 480, configurable: true })
    Object.defineProperty(video, 'readyState', { value: 1, configurable: true })
    fireEvent(video, new Event('loadedmetadata'))

    const shutter = screen.getByRole('button', { name: 'Ambil foto' })
    expect(shutter).toBeDisabled()

    Object.defineProperty(video, 'readyState', { value: 2, configurable: true })
    fireEvent(video, new Event('canplay'))
    await waitFor(() => expect(shutter).not.toBeDisabled())
  })

  it('reports an insecure origin without asking for the camera', async () => {
    const { getUserMedia } = installMedia()
    Object.defineProperty(window, 'isSecureContext', {
      value: false,
      configurable: true,
    })
    stubPlay()
    stubCanvas()
    stubFrameImages()
    renderStudio()

    await screen.findByText('Kamera membutuhkan koneksi aman')
    expect(getUserMedia).not.toHaveBeenCalled()
    Reflect.deleteProperty(window, 'isSecureContext')
  })
})
