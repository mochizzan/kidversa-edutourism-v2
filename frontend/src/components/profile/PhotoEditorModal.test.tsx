/**
 * PhotoEditorModal (spec 09 decision 6/7): the crop editor behind ProfilePage.
 *
 * - File picker + drag-drop pick the source; the source loads into the
 *   viewport; pan by drag; zoom by wheel + control; rotate 0–360°; standard
 *   grid overlay.
 * - Confirm composes a 512×512 square JPEG (the browser crops; the server
 *   never sees crop parameters) and hands the File to the parent.
 * - Real-time 0–100% upload progress + honest "processing" state once the
 *   bytes are fully away; Delete is visible ONLY when a photo exists.
 *
 * jsdom has no canvas/image decode: the context/toBlob/object-URL surfaces
 * are stubbed here (PhotoCapture.test.tsx pattern) and the decode seam
 * (loadImage) is mocked wholesale.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import PhotoEditorModal from './PhotoEditorModal'
import { clampPan, cropTransform } from './cropMath'

vi.mock('./loadImage', () => ({ default: vi.fn() }))

/** The wholesale mock standing in for the decode seam. */
type MockLoad = ReturnType<typeof vi.fn>

/* ---------- canvas/url stubs ---------- */

interface CtxCall {
  method: string
  args: unknown[]
}

let ctxCalls: CtxCall[]

function stubCanvasEnvironment(options: { blob?: Blob | null } = {}) {
  ctxCalls = []
  const record = (method: string) => (...args: unknown[]) => {
    ctxCalls.push({ method, args })
  }
  const ctx = {
    save: record('save'),
    restore: record('restore'),
    clearRect: record('clearRect'),
    translate: record('translate'),
    rotate: record('rotate'),
    scale: record('scale'),
    drawImage: record('drawImage'),
    fillRect: record('fillRect'),
  }
  const doToBlob = vi.fn(
    (callback: (blob: Blob | null) => void, type?: string, quality?: number) => {
      ctxCalls.push({ method: 'toBlob', args: [type, quality] })
      // `=== undefined` (not ??): an explicit null means "encode yields null".
      callback(options.blob === undefined ? new Blob(['jpeg-bytes'], { type: 'image/jpeg' }) : options.blob)
    },
  )
  Object.defineProperty(HTMLCanvasElement.prototype, 'getContext', {
    value: vi.fn(() => ctx),
    configurable: true,
    writable: true,
  })
  Object.defineProperty(
    HTMLCanvasElement.prototype,
    'toBlob',
    {
      value: doToBlob,
      configurable: true,
      writable: true,
    },
  )
  Object.defineProperty(HTMLCanvasElement.prototype, 'width', {
    get: () => 512,
    configurable: true,
  })
  Object.defineProperty(HTMLCanvasElement.prototype, 'height', {
    get: () => 512,
    configurable: true,
  })
  const createObjectURL = vi.fn(() => 'blob:editor-src-1')
  const revokeObjectURL = vi.fn()
  Object.defineProperty(URL, 'createObjectURL', { value: createObjectURL, configurable: true })
  Object.defineProperty(URL, 'revokeObjectURL', { value: revokeObjectURL, configurable: true })
  return { createObjectURL, revokeObjectURL, toBlob: doToBlob }
}

/* ---------- decode/picker harness ---------- */

const VIEW = 320 // the modal's square viewport, mirrored by the impl

function fakeImage(w = 1600, h = 900) {
  return { naturalWidth: w, naturalHeight: h } as HTMLImageElement
}

function pickFile(): File {
  return new File(['jpeg-bytes'], 'me.jpg', { type: 'image/jpeg' })
}

async function mockDecode(
  options: { img?: HTMLImageElement; loadImageError?: boolean } = {},
): Promise<void> {
  const mod = (await import('./loadImage')) as unknown as { default: MockLoad }
  mod.default.mockReset()
  if (options.loadImageError) mod.default.mockRejectedValue(new Error('bad image'))
  else mod.default.mockResolvedValue(options.img ?? fakeImage())
}

async function openEditor(
  options: {
    hasPhoto?: boolean
    img?: HTMLImageElement
    loadImageError?: boolean
  } = {},
) {
  await mockDecode(options)
  const props = {
    hasPhoto: options.hasPhoto ?? false,
    uploading: false,
    progress: 0,
    onClose: vi.fn(),
    onConfirm: vi.fn(),
    onDelete: vi.fn(),
  }
  const user = userEvent.setup()
  render(
    <PhotoEditorModal
      open
      hasPhoto={props.hasPhoto}
      uploading={props.uploading}
      progress={props.progress}
      onClose={props.onClose}
      onConfirm={props.onConfirm}
      onDelete={props.onDelete}
    />,
  )
  // The picker drives the source: feed one file through the modal's input.
  const input = (await screen.findByLabelText('Pilih foto')) as HTMLInputElement
  await user.upload(input, pickFile())
  // Determinism: the decode has finished (or failed) before any interaction —
  // the marker text only renders in that settled state.
  if (options.loadImageError) {
    await screen.findByText('Foto tidak dapat dimuat. Coba berkas lain.')
  } else {
    await screen.findByText('Seret untuk memindah · Rolanya untuk memperbesar')
  }
  return { props }
}

const confirmButton = (): HTMLButtonElement => screen.getByRole('button', { name: 'Gunakan foto' })

const viewport = (): HTMLElement =>
  document.querySelector<HTMLDivElement>('.kv-crop-vp') as HTMLElement

const preview = (): HTMLElement =>
  document.querySelector<HTMLImageElement>('.kv-crop-img') as HTMLElement

beforeEach(() => {
  stubCanvasEnvironment()
})

/* ---------- scenarios ---------- */

describe('PhotoEditorModal open', () => {
  it('shows the editor with Cancel/Confirm and the grid overlay once a source is picked', async () => {
    await openEditor()

    expect(screen.getByRole('dialog')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Batal' })).toBeInTheDocument()
    expect(
      screen.getByText('Seret untuk memindah · Rolanya untuk memperbesar'),
    ).toBeInTheDocument()
    expect(document.querySelector('.kv-crop-grid')).not.toBeNull()
    await waitFor(() => {
      expect(preview()).toBeInstanceOf(HTMLImageElement)
      expect(preview().style.transform).not.toBe('')
    })
  })

  it('accepts a drag-drop source as well as the picker', async () => {
    await mockDecode()
    render(
      <PhotoEditorModal
        open
        hasPhoto={false}
        uploading={false}
        progress={0}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        onDelete={vi.fn()}
      />,
    )
    const drop = await screen.findByText('Pilih foto')
    fireEvent.drop(drop, {
      dataTransfer: { files: [pickFile()] },
    })

    await waitFor(() => expect(preview()).toBeInstanceOf(HTMLImageElement))
  })

  it('shows Delete only when an avatar already exists (and Delete does not close)', async () => {
    await openEditor({ hasPhoto: false })
    await screen.findByRole('dialog')
    expect(screen.queryByRole('button', { name: 'Hapus foto' })).toBeNull()
    cleanup()

    const { props } = await openEditor({ hasPhoto: true })
    await screen.findByRole('dialog')
    const del = screen.getByRole('button', { name: 'Hapus foto' })
    const user = userEvent.setup()
    await user.click(del)
    expect(props.onDelete).toHaveBeenCalledTimes(1)
    expect(props.onClose).not.toHaveBeenCalled() // close follows the server answer
  })

  it('shows a friendly error and blocks Confirm when the source cannot decode', async () => {
    await openEditor({ loadImageError: true })

    await screen.findByText('Foto tidak dapat dimuat. Coba berkas lain.')
    expect(confirmButton()).toBeDisabled()
  })
})

describe('PhotoEditorModal crop interaction', () => {
  it('zooms by wheel from cover towards the 3× cap and never past it', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')

    // Start: cover. One wheel out (deltaY < 0 zooms in) changes the transform.
    expect(preview().style.transform).toBe(cropTransform(1600, 900, VIEW, VIEW, 0.1, 0, 0, 0).cssTransform)
    fireEvent.wheel(viewport(), { deltaY: -100 })
    expect(preview().style.transform).not.toBe(
      cropTransform(1600, 900, VIEW, VIEW, 0.1, 0, 0, 0).cssTransform,
    )

    // Wheel in far beyond the cap → clamped to exactly 3× cover.
    for (let i = 0; i < 30; i += 1) fireEvent.wheel(viewport(), { deltaY: -100 })
    expect(preview().style.transform).toBe(cropTransform(1600, 900, VIEW, VIEW, 99, 0, 0, 0).cssTransform)
  })

  it('zooms via the zoom control as well (native range input)', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    const dialog = await screen.findByRole('dialog')
    const control = within(dialog).getByLabelText('Perbesar tampilan') as HTMLInputElement
    fireEvent.change(control, { target: { value: '100' } })

    expect(preview().style.transform).toBe(cropTransform(1600, 900, VIEW, VIEW, 99, 0, 0, 0).cssTransform)
  })

  it('sets the rotation on the preview via the 0–360 slider', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    const dialog = await screen.findByRole('dialog')
    const slider = within(dialog).getByLabelText('Putar') as HTMLInputElement
    fireEvent.change(slider, { target: { value: '120' } })

    expect(preview().style.transform).toContain('rotate(120deg)')
  })

  it('zooms by the − / + buttons across the fit → 3× band', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    const dialog = await screen.findByRole('dialog')
    const user = userEvent.setup()
    const cover = cropTransform(1600, 900, VIEW, VIEW, 0.1, 0, 0, 0).cssTransform
    const cap = cropTransform(1600, 900, VIEW, VIEW, 99, 0, 0, 0).cssTransform

    // Zoom out at cover stays pinned at cover.
    await user.click(within(dialog).getByRole('button', { name: 'Perkecil' }))
    expect(preview().style.transform).toBe(cover)

    // Zoom in past the cap clamps at exactly 3× cover.
    for (let i = 0; i < 15; i += 1) {
      await user.click(within(dialog).getByRole('button', { name: 'Perbesar' }))
    }
    expect(preview().style.transform).toBe(cap)
  })

  it('pans by dragging the viewport inside the zoom band', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')
    const before = preview().style.transform

    fireEvent.pointerDown(viewport(), { pointerId: 1, clientX: 10, clientY: 10, button: 0 })
    fireEvent.pointerMove(viewport(), { pointerId: 1, clientX: -30, clientY: -40 })
    fireEvent.pointerUp(viewport(), { pointerId: 1 })

    expect(preview().style.transform).not.toBe(before)
    expect(preview().style.transform).toContain('translate(-')
  })

  it('pans right after zooming in (both directions have room)', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')

    for (let i = 0; i < 3; i += 1) fireEvent.wheel(viewport(), { deltaY: -100 })

    // The old top-left clamp capped pan at 0, so a rightward drag was dead.
    fireEvent.pointerDown(viewport(), { pointerId: 1, clientX: 100, clientY: 100, button: 0 })
    fireEvent.pointerMove(viewport(), { pointerId: 1, clientX: 160, clientY: 100 })
    fireEvent.pointerUp(viewport(), { pointerId: 1 })

    expect(preview().style.transform).toMatch(/^translate\((?!-)\d/)
  })

  it('the preview never becomes a native drag source (pan, not a ghost copy)', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')

    expect(preview()).toHaveAttribute('draggable', 'false')
    // fireEvent returns false when the default action was prevented.
    expect(fireEvent.dragStart(viewport())).toBe(false)
    // A stray drop must not navigate to / open the blob URL either.
    expect(fireEvent.drop(viewport())).toBe(false)
    // No native image callout/context menu on the crop surface.
    expect(fireEvent.contextMenu(viewport())).toBe(false)
  })

  it('a drag after zooming starts from the effective clamped pan (no stuck lag)', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')

    // Zoom to the 3× cap: at 1600×900 the band is ±693 × ±320.
    for (let i = 0; i < 30; i += 1) fireEvent.wheel(viewport(), { deltaY: -100 })
    const cap = cropTransform(1600, 900, VIEW, VIEW, 99, 0, 0, 0)
    const s = cap.scale
    const band = clampPan(-2000, 0, 1600 * s, 900 * s, VIEW, VIEW)

    // Drag far past the left edge — the preview clamps at the band.
    fireEvent.pointerDown(viewport(), { pointerId: 1, clientX: 0, clientY: 0, button: 0 })
    fireEvent.pointerMove(viewport(), { pointerId: 1, clientX: -2000, clientY: 0 })
    fireEvent.pointerUp(viewport(), { pointerId: 1 })
    expect(preview().style.transform).toBe(
      cropTransform(1600, 900, VIEW, VIEW, 99, band.x, 0, 0).cssTransform,
    )

    // The NEXT drag responds immediately: 50px right = 50px along the band.
    fireEvent.pointerDown(viewport(), { pointerId: 2, clientX: 0, clientY: 0, button: 0 })
    fireEvent.pointerMove(viewport(), { pointerId: 2, clientX: 50, clientY: 0 })
    fireEvent.pointerUp(viewport(), { pointerId: 2 })
    expect(preview().style.transform).toBe(
      cropTransform(1600, 900, VIEW, VIEW, 99, band.x + 50, 0, 0).cssTransform,
    )
  })
})

describe('PhotoEditorModal confirm', () => {
  it('composes a 512×512 JPEG and hands the File to the caller', async () => {
    const { props } = await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')

    // Interact first: some rotation so the composed transform is non-trivial.
    const slider = within(screen.getByRole('dialog')).getByLabelText('Putar') as HTMLInputElement
    fireEvent.change(slider, { target: { value: '90' } })

    const user = userEvent.setup()
    await user.click(confirmButton())

    await waitFor(() => expect(props.onConfirm).toHaveBeenCalledTimes(1))
    const file = props.onConfirm.mock.calls[0][0] as File
    expect(file.name).toBe('avatar.jpg')
    expect(file.type).toBe('image/jpeg')

    // The canvas mirror: translate(center + pan*k), rotate(rad), scale(s*k),
    // draw the image centered, then encode JPEG at the pinned quality.
    const t = cropTransform(1600, 900, VIEW, VIEW, 0.1, 0, 0, 90)
    const k = 512 / VIEW
    const translate = ctxCalls.find((c) => c.method === 'translate')
    const rotate = ctxCalls.find((c) => c.method === 'rotate')
    const scale = ctxCalls.find((c) => c.method === 'scale')
    const draw = ctxCalls.find((c) => c.method === 'drawImage')
    expect(translate?.args[0]).toBeCloseTo(256 + t.pan.x * k)
    expect(translate?.args[1]).toBeCloseTo(256 + t.pan.y * k)
    expect(rotate?.args[0]).toBeCloseTo((t.deg * Math.PI) / 180)
    expect(scale?.args[0]).toBeCloseTo(t.scale * k)
    expect(scale?.args[1]).toBeCloseTo(t.scale * k)
    expect(draw?.args[1]).toBe(-800) // image center offset: -naturalWidth/2
    expect(draw?.args[2]).toBe(-450) // image center offset: -naturalHeight/2
    expect(ctxCalls.find((c) => c.method === 'toBlob')?.args).toEqual(['image/jpeg', 0.92])
  })

  it('reports the live upload progress and honestly marks full-away as processing', async () => {
    await openEditor({ hasPhoto: true })
    await screen.findByRole('dialog')
    cleanup()

    // 47%: the bar shows the live number; actions stay disabled.
    render(
      <PhotoEditorModal
        open
        hasPhoto
        uploading
        progress={47}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        onDelete={vi.fn()}
      />,
    )
    expect(document.querySelector('.ant-progress')).not.toBeNull()
    expect(document.querySelector('.ant-progress')?.textContent).toContain('47')
    expect(confirmButton()).toBeDisabled()

    // 100% while still uploading: the honest "processing" state.
    cleanup()
    render(
      <PhotoEditorModal
        open
        hasPhoto
        uploading
        progress={100}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        onDelete={vi.fn()}
      />,
    )
    expect(screen.getByText('Memproses…')).toBeInTheDocument()
    expect(screen.queryByText('47')).not.toBeInTheDocument()
  })
})

describe('PhotoEditorModal dismiss', () => {
  it('Cancel closes without emitting a file', async () => {
    const { props } = await openEditor({ hasPhoto: true })
    await screen.findByRole('dialog')

    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Batal' }))
    expect(props.onClose).toHaveBeenCalledTimes(1)
    expect(props.onConfirm).not.toHaveBeenCalled()
  })
})

describe('PhotoEditorModal edge interactions', () => {
  it('a reopen starts clean (the closed state drops every edit)', async () => {
    await mockDecode()
    const { rerender } = render(
      <PhotoEditorModal
        open={false}
        hasPhoto={false}
        uploading={false}
        progress={0}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        onDelete={vi.fn()}
      />,
    )
    expect(screen.queryByRole('dialog')).toBeNull()

    const user = userEvent.setup()
    rerender(
      <PhotoEditorModal
        open
        hasPhoto={false}
        uploading={false}
        progress={0}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        onDelete={vi.fn()}
      />,
    )
    const input = (await screen.findByLabelText('Pilih foto')) as HTMLInputElement
    await user.upload(input, pickFile())
    await screen.findByText('Seret untuk memindah · Rolanya untuk memperbesar')

    // Fresh editor: rotation slider back at 0 and the preview at cover.
    expect((within(screen.getByRole('dialog')).getByLabelText('Putar') as HTMLInputElement).value).toBe('0')
    expect(preview().style.transform).toBe(cropTransform(1600, 900, VIEW, VIEW, 0.1, 0, 0, 0).cssTransform)
  })

  it('dragOver is a no-op prevent and an empty drop changes nothing', async () => {
    await mockDecode()
    render(
      <PhotoEditorModal
        open
        hasPhoto={false}
        uploading={false}
        progress={0}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        onDelete={vi.fn()}
      />,
    )
    const dropEl = await screen.findByText('Pilih foto')
    fireEvent.dragOver(dropEl) // preventDefault only

    fireEvent.drop(dropEl, { dataTransfer: { files: [] } })
    expect(document.querySelector('.kv-crop-vp')).toBeNull() // still the picker
  })

  it('wheel-out steps the zoom back down (the 1/step branch)', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')

    const cover = cropTransform(1600, 900, VIEW, VIEW, 0.1, 0, 0, 0).cssTransform
    fireEvent.wheel(viewport(), { deltaY: -100 }) // one step in
    expect(preview().style.transform).not.toBe(cover)
    fireEvent.wheel(viewport(), { deltaY: 100 }) // one step out: back to cover
    expect(preview().style.transform).toBe(cover)
  })

  it('ignores pointer moves from a foreign pointer and stops cleanly on cancel', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')

    fireEvent.pointerDown(viewport(), { pointerId: 7, clientX: 0, clientY: 0, button: 0 })
    const before = preview().style.transform
    // Foreign pointer id: no drag follows.
    fireEvent.pointerMove(viewport(), { pointerId: 9, clientX: -50, clientY: -50 })
    expect(preview().style.transform).toBe(before)
    // Cancel ends the drag without a tail.
    fireEvent.pointerCancel(viewport(), { pointerId: 7 })
    fireEvent.pointerMove(viewport(), { pointerId: 7, clientX: -99, clientY: -99 })
    expect(preview().style.transform).toBe(before)
  })

  it('the zoom slider tracks the wheel zoom state', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    const dialog = await screen.findByRole('dialog')
    const control = within(dialog).getByLabelText('Perbesar tampilan') as HTMLInputElement

    expect(control.value).toBe('0') // cover fit = slider zero
    fireEvent.wheel(viewport(), { deltaY: -100 }) // one notch in
    expect(Number(control.value)).toBeGreaterThan(0)
  })

  it('compose without a 2d context never emits a file', async () => {
    await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')

    Object.defineProperty(HTMLCanvasElement.prototype, 'getContext', {
      value: vi.fn(() => null),
      configurable: true,
      writable: true,
    })
    const user = userEvent.setup()
    await user.click(confirmButton())
    expect(ctxCalls.find((c) => c.method === 'drawImage')).toBeUndefined()
  })

  it('clicking the disabled Confirm in the decode-error state never composes', async () => {
    const { props } = await openEditor({ loadImageError: true })
    await screen.findByText('Foto tidak dapat dimuat. Coba berkas lain.')

    // userEvent refuses disabled buttons; the raw DOM event still reaches the
    // handler — the `!image` compose guard must swallow it.
    fireEvent.click(confirmButton())
    expect(ctxCalls.find((c) => c.method === 'drawImage')).toBeUndefined()
    expect(props.onConfirm).not.toHaveBeenCalled()
  })

  it('a null toBlob result never emits a file (encode failure is silent)', async () => {
    stubCanvasEnvironment({ blob: null }) // re-stub on top of the beforeEach default
    const { props } = await openEditor({ img: fakeImage(1600, 900) })
    await screen.findByRole('dialog')

    const user = userEvent.setup()
    await user.click(confirmButton())

    // The compose ran (pixels drawn, encode attempted) but nothing is emitted.
    await waitFor(() => expect(ctxCalls.find((c) => c.method === 'toBlob')).toBeDefined())
    expect(props.onConfirm).not.toHaveBeenCalled()
  })

  it('an empty-file picker change leaves the picker untouched', async () => {
    await mockDecode()
    const decode = (await import('./loadImage')) as unknown as { default: MockLoad }
    render(
      <PhotoEditorModal
        open
        hasPhoto={false}
        uploading={false}
        progress={0}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        onDelete={vi.fn()}
      />,
    )
    const input = (await screen.findByLabelText('Pilih foto')) as HTMLInputElement
    fireEvent.change(input, { target: { files: [] } })

    // `files?.[0]` is undefined → no source picked, no decode attempt.
    expect(document.querySelector('.kv-crop-vp')).toBeNull()
    expect(decode.default).not.toHaveBeenCalled()
  })
})
