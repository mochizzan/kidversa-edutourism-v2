/**
 * Camera studio for the child gallery (F-10 / FL-10).
 *
 * The viewfinder is a strict-ratio stage: the live feed and the captured
 * review image are centre-cropped to fill it (`object-fit: cover`), while a
 * selected frame is drawn with `contain` so it never stretches. Frames can be
 * changed in live mode or in review; the save step fuses image + frame into
 * ONE JPEG, so the server only ever stores a combined photo.
 *
 * Composition guides (3×3, 1×1 square, golden ratio) are preview-only and are
 * never baked into the file. Mirror toggles are applied to both the preview
 * and the saved pixels; frames stay unmirrored.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import type { CSSProperties } from 'react'
import {
  App as AntApp,
  Button,
  Segmented,
  Switch,
  Typography,
  Upload,
} from 'antd'
import { CameraOutlined, PictureOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { IMAGE_ACCEPT, isAcceptedImage } from '../../lib/validation'
import type { FrameOption } from './shared'

export type CaptureRatio = 'portrait' | 'landscape'
export type CaptureGrid = 'off' | 'thirds' | 'square' | 'golden'
type Mode = 'live' | 'review'
type FrameStatus = 'none' | 'loading' | 'ready' | 'error'
/** Why the viewfinder is unavailable. Only an explicit permission/security
 * refusal is a denial; everything else is retryable. */
type CameraFailure = 'denied' | 'unavailable' | 'unsupported' | 'insecure'

/**
 * Map a getUserMedia rejection to the panel it deserves. The old catch-all
 * treated every failure — a busy device, an aborted start, a missing camera —
 * as a permanent permission denial, which is exactly the false
 * CAMERA_DENIED the user saw while the camera indicator was still on.
 */
function classifyCameraFailure(failure: unknown): CameraFailure {
  const name =
    typeof failure === 'object' && failure !== null && 'name' in failure
      ? String((failure as { name?: unknown }).name ?? '')
      : ''
  switch (name) {
    case 'NotAllowedError':
    case 'PermissionDeniedError':
    case 'SecurityError':
      return 'denied'
    default:
      // NotReadableError (device busy), AbortError (start aborted),
      // NotFoundError / OverconstrainedError (no matching camera) and
      // unknown failures are transient or environmental — never a denial.
      return 'unavailable'
  }
}

export interface CameraStudioProps {
  frames: FrameOption[]
  framesLoading: boolean
  framesError: unknown
  onReloadFrames: () => void
  frameId: number | null
  onFrameChange: (frameId: number | null) => void
  uploading: boolean
  onSave: (file: File, frameId: number | null) => void
}

const RATIOS: Record<CaptureRatio, { value: number; outputLabel: string }> = {
  portrait: { value: 9 / 16, outputLabel: '9:16' },
  landscape: { value: 16 / 9, outputLabel: '16:9' },
}

/** Longest edge of the stored JPEG. Camera crops stay at hardware resolution
 * below this cap; oversized uploaded files are scaled down before merging. */
const MAX_OUTPUT_EDGE = 4096

/** Centre-crop rectangle (source pixels) that fills a target ratio. */
export function coverCropRect(
  sourceWidth: number,
  sourceHeight: number,
  targetRatio: number,
): { x: number; y: number; width: number; height: number } {
  let width = sourceWidth
  let height = Math.round(sourceWidth / targetRatio)
  if (height > sourceHeight) {
    height = sourceHeight
    width = Math.round(sourceHeight * targetRatio)
  }
  return {
    x: Math.round((sourceWidth - width) / 2),
    y: Math.round((sourceHeight - height) / 2),
    width,
    height,
  }
}

/** Contain rectangle (destination pixels) that fits an overlay without stretch. */
export function containRect(
  sourceWidth: number,
  sourceHeight: number,
  targetWidth: number,
  targetHeight: number,
): { x: number; y: number; width: number; height: number } {
  const scale = Math.min(targetWidth / sourceWidth, targetHeight / sourceHeight)
  const width = sourceWidth * scale
  const height = sourceHeight * scale
  return {
    x: (targetWidth - width) / 2,
    y: (targetHeight - height) / 2,
    width,
    height,
  }
}

/** Output canvas size for a crop, capped on its longest edge. */
function outputSize(cropWidth: number, cropHeight: number): { width: number; height: number } {
  const scale = Math.min(1, MAX_OUTPUT_EDGE / Math.max(cropWidth, cropHeight))
  return {
    width: Math.max(1, Math.round(cropWidth * scale)),
    height: Math.max(1, Math.round(cropHeight * scale)),
  }
}

/** Draw a source (video or image) centre-cropped into a raw canvas. */
function cropToCanvas(
  source: CanvasImageSource,
  sourceWidth: number,
  sourceHeight: number,
  ratio: CaptureRatio,
): HTMLCanvasElement | null {
  if (sourceWidth <= 0 || sourceHeight <= 0) return null
  const crop = coverCropRect(sourceWidth, sourceHeight, RATIOS[ratio].value)
  const size = outputSize(crop.width, crop.height)
  const canvas = document.createElement('canvas')
  canvas.width = size.width
  canvas.height = size.height
  const context = canvas.getContext('2d')
  if (!context) return null
  context.drawImage(
    source,
    crop.x,
    crop.y,
    crop.width,
    crop.height,
    0,
    0,
    size.width,
    size.height,
  )
  return canvas
}

function GridOverlay({ grid }: { grid: CaptureGrid }) {
  if (grid === 'off') return null
  if (grid === 'square') {
    return (
      <div className="kv-camera__grid" aria-hidden="true">
        <div className="kv-camera__square" />
      </div>
    )
  }
  const positions = grid === 'thirds' ? [100 / 3, 200 / 3] : [38.2, 61.8]
  return (
    <div className="kv-camera__grid" aria-hidden="true">
      {positions.map((position) => (
        <div
          key={`v-${position}`}
          className="kv-camera__line kv-camera__line--v"
          style={{ left: `${position}%` }}
        />
      ))}
      {positions.map((position) => (
        <div
          key={`h-${position}`}
          className="kv-camera__line kv-camera__line--h"
          style={{ top: `${position}%` }}
        />
      ))}
    </div>
  )
}

export default function CameraStudio({
  frames,
  framesLoading,
  framesError,
  onReloadFrames,
  frameId,
  onFrameChange,
  uploading,
  onSave,
}: CameraStudioProps) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const streamRef = useRef<MediaStream | null>(null)
  const acquisitionRef = useRef<Promise<MediaStream> | null>(null)
  const consumersRef = useRef(0)
  const streamCleanupRef = useRef<{
    stream: MediaStream
    off: () => void
  } | null>(null)
  const rawCanvasRef = useRef<HTMLCanvasElement | null>(null)
  const frameImageRef = useRef<HTMLImageElement | null>(null)

  const [mode, setMode] = useState<Mode>('live')
  const [ratio, setRatio] = useState<CaptureRatio>('portrait')
  const [grid, setGrid] = useState<CaptureGrid>('thirds')
  const [mirrorX, setMirrorX] = useState(false)
  const [mirrorY, setMirrorY] = useState(false)
  const [cameraError, setCameraError] = useState<CameraFailure | null>(null)
  const [needsGesture, setNeedsGesture] = useState(false)
  const [ready, setReady] = useState(false)
  const [previewUrl, setPreviewUrl] = useState<string | null>(null)
  const [hasCapture, setHasCapture] = useState(false)
  // Status is keyed by URL so a frame switch can never be mistaken for the
  // previous frame still being ready (that would fuse the wrong pixels).
  const [frameState, setFrameState] = useState<{
    url: string
    status: FrameStatus
  } | null>(null)
  const [preparing, setPreparing] = useState(false)

  const selectedFrame = frames.find((frame) => frame.id === frameId) ?? null
  const frameUrl = selectedFrame?.image_url ?? null
  const frameStatus: FrameStatus = frameUrl
    ? frameState?.url === frameUrl
      ? frameState.status
      : 'loading'
    : 'none'

  /** Start playback. A rejected or synchronously throwing `play()` is an
   * autoplay/gesture problem, not a permission problem: ask for a tap instead
   * of rendering CAMERA_DENIED over a working camera. */
  const playVideo = useCallback((video: HTMLVideoElement) => {
    try {
      const playing = video.play() as Promise<void> | undefined
      if (playing && typeof playing.then === 'function') {
        void playing.then(
          () => {
            if (consumersRef.current > 0 && videoRef.current === video) {
              setNeedsGesture(false)
            }
          },
          () => {
            if (consumersRef.current > 0 && videoRef.current === video) {
              setNeedsGesture(true)
            }
          },
        )
      }
    } catch {
      if (consumersRef.current > 0) setNeedsGesture(true)
    }
  }, [])

  // The stream outlives the <video> element (review unmounts it), so attach it
  // whenever a live element mounts again — e.g. after Retake.
  const attachVideo = useCallback(
    (video: HTMLVideoElement | null) => {
      videoRef.current = video
      const stream = streamRef.current
      if (!video || !stream) return
      if (video.srcObject !== stream) video.srcObject = stream
      playVideo(video)
    },
    [playVideo],
  )

  /** Detach the track listeners and stop every track of one stream. */
  const stopStream = useCallback((stream: MediaStream | null) => {
    if (!stream) return
    if (streamCleanupRef.current?.stream === stream) {
      streamCleanupRef.current.off()
      streamCleanupRef.current = null
    }
    if (streamRef.current === stream) streamRef.current = null
    stream.getTracks().forEach((track) => track.stop())
  }, [])

  /** Adopt a fresh stream and watch its video track: a device that vanishes
   * mid-session (unplugged, or taken by another app) must lock the shutter. */
  const adoptStream = useCallback(
    (stream: MediaStream) => {
      const previous = streamRef.current
      if (previous && previous !== stream) stopStream(previous)
      streamRef.current = stream
      setReady(false)
      setNeedsGesture(false)
      const videoTracks = stream.getVideoTracks()
      const onEnded = () => {
        if (streamRef.current !== stream) return
        stopStream(stream)
        setReady(false)
        setCameraError('unavailable')
      }
      videoTracks.forEach((track) => track.addEventListener('ended', onEnded))
      streamCleanupRef.current = {
        stream,
        off: () => {
          videoTracks.forEach((track) => track.removeEventListener('ended', onEnded))
        },
      }
      attachVideo(videoRef.current)
    },
    [attachVideo, stopStream],
  )

  /** Acquire the camera once per component instance. StrictMode's second
   * effect run joins the first request instead of racing a second
   * getUserMedia — the race that could turn a transient NotReadableError into
   * a permanent denial panel. */
  const startCamera = useCallback(() => {
    if (typeof navigator === 'undefined') {
      setCameraError('unsupported')
      return
    }
    // `undefined` in jsdom/older engines means unknown, not insecure.
    if (typeof window !== 'undefined' && window.isSecureContext === false) {
      setCameraError('insecure')
      return
    }
    const media = navigator.mediaDevices
    if (!media?.getUserMedia) {
      setCameraError('unsupported')
      return
    }
    if (acquisitionRef.current) return

    setCameraError(null)
    setNeedsGesture(false)
    setReady(false)

    let pending: Promise<MediaStream>
    try {
      pending = media.getUserMedia({ video: { facingMode: 'environment' }, audio: false })
    } catch (failure) {
      setCameraError(classifyCameraFailure(failure))
      return
    }
    acquisitionRef.current = pending
    void pending.then(
      (stream) => {
        if (acquisitionRef.current === pending) acquisitionRef.current = null
        // A real unmount while the permission prompt was open: nobody can
        // consume this stream, so do not leave the camera light on.
        if (consumersRef.current === 0) {
          stream.getTracks().forEach((track) => track.stop())
          return
        }
        adoptStream(stream)
      },
      (failure: unknown) => {
        if (acquisitionRef.current === pending) acquisitionRef.current = null
        if (consumersRef.current === 0) return
        setCameraError(classifyCameraFailure(failure))
      },
    )
  }, [adoptStream])

  // Camera lifecycle: start with the component, stop on a real unmount. The
  // deferred teardown lets StrictMode's synchronous cleanup → setup pair keep
  // the one shared acquisition alive.
  useEffect(() => {
    consumersRef.current += 1
    startCamera()
    return () => {
      consumersRef.current -= 1
      queueMicrotask(() => {
        if (consumersRef.current > 0) return
        stopStream(streamRef.current)
      })
    }
  }, [startCamera, stopStream])

  const retryCamera = useCallback(() => {
    stopStream(streamRef.current)
    setCameraError(null)
    setNeedsGesture(false)
    setReady(false)
    startCamera()
  }, [startCamera, stopStream])

  // Load the frame bytes once for the canvas merge. The DOM overlay can render
  // immediately; the save button waits for this exact image to be drawable.
  useEffect(() => {
    if (!frameUrl) {
      frameImageRef.current = null
      setFrameState(null)
      return
    }
    let alive = true
    setFrameState({ url: frameUrl, status: 'loading' })
    const image = new globalThis.Image()
    image.crossOrigin = 'anonymous'
    image.onload = () => {
      if (!alive) return
      frameImageRef.current = image
      setFrameState({ url: frameUrl, status: 'ready' })
    }
    image.onerror = () => {
      if (!alive) return
      frameImageRef.current = null
      setFrameState({ url: frameUrl, status: 'error' })
    }
    image.src = frameUrl
    return () => {
      alive = false
    }
  }, [frameUrl])

  const markReady = () => {
    const video = videoRef.current
    // `loadedmetadata` alone is not enough: the frame must be drawable
    // (HAVE_CURRENT_DATA) before the shutter may open.
    if (
      video &&
      video.videoWidth > 0 &&
      video.videoHeight > 0 &&
      video.readyState >= 2
    ) {
      setReady(true)
    }
  }

  const takePhoto = () => {
    const video = videoRef.current
    if (
      !video ||
      !ready ||
      needsGesture ||
      video.readyState < 2 ||
      video.videoWidth <= 0 ||
      video.videoHeight <= 0
    ) {
      return
    }
    try {
      const raw = cropToCanvas(video, video.videoWidth, video.videoHeight, ratio)
      if (!raw) {
        message.error(t('gallery.capture.failed'))
        return
      }
      rawCanvasRef.current = raw
      setPreviewUrl(raw.toDataURL('image/jpeg', 0.92))
      setHasCapture(true)
      setMode('review')
    } catch {
      // drawImage/toDataURL can throw on a frame that is no longer drawable.
      message.error(t('gallery.capture.failed'))
    }
  }

  const retake = () => {
    rawCanvasRef.current = null
    setPreviewUrl(null)
    setHasCapture(false)
    // The remounted <video> needs fresh metadata before the shutter reopens.
    setReady(false)
    setMode('live')
  }

  /** Retry playback after an autoplay/gesture block. */
  const resumePlayback = () => {
    const video = videoRef.current
    if (video) playVideo(video)
  }

  const pickFile = (file: File) => {
    // The ONE shared upload gate (jpg/jpeg/png ≤50 MB, spec 09 2026-11-10).
    if (!isAcceptedImage(file)) {
      message.error(t('gallery.badFormat'))
      return
    }
    const url = URL.createObjectURL(file)
    const image = new globalThis.Image()
    image.onload = () => {
      try {
        const raw = cropToCanvas(
          image,
          image.naturalWidth,
          image.naturalHeight,
          ratio,
        )
        if (!raw) {
          message.error(t('gallery.capture.failed'))
          return
        }
        rawCanvasRef.current = raw
        setPreviewUrl(raw.toDataURL('image/jpeg', 0.92))
        setHasCapture(true)
        setMode('review')
      } catch {
        message.error(t('gallery.capture.failed'))
      } finally {
        URL.revokeObjectURL(url)
      }
    }
    image.onerror = () => {
      URL.revokeObjectURL(url)
      message.error(t('gallery.capture.failed'))
    }
    image.src = url
  }

  const save = () => {
    const raw = rawCanvasRef.current
    if (!raw || uploading || preparing) return
    // A selected frame must be decoded before it can be fused; an errored
    // frame saves the photo without a frame rather than pretending. `none`
    // with a URL is the one render between selection and the load effect.
    if (frameUrl && frameStatus !== 'ready' && frameStatus !== 'error') return
    const output = document.createElement('canvas')
    output.width = raw.width
    output.height = raw.height
    const context = output.getContext('2d')
    if (!context) {
      message.error(t('gallery.capture.failed'))
      return
    }
    setPreparing(true)
    context.fillStyle = '#171614'
    context.fillRect(0, 0, output.width, output.height)
    // Mirror around the centre, then draw the already cover-cropped raw frame.
    context.save()
    context.translate(output.width / 2, output.height / 2)
    context.scale(mirrorX ? -1 : 1, mirrorY ? -1 : 1)
    context.drawImage(raw, -output.width / 2, -output.height / 2, output.width, output.height)
    context.restore()

    // Provenance follows the pixels: only a frame that was actually drawn is
    // sent as `frame_id`, so the preview and the stored photo can never
    // disagree about whether a frame is present.
    let fusedFrameId: number | null = null
    const frame = frameImageRef.current
    if (
      frameUrl &&
      frameStatus === 'ready' &&
      frame &&
      frame.naturalWidth > 0 &&
      frame.naturalHeight > 0
    ) {
      const fit = containRect(
        frame.naturalWidth,
        frame.naturalHeight,
        output.width,
        output.height,
      )
      context.drawImage(frame, fit.x, fit.y, fit.width, fit.height)
      fusedFrameId = frameId
    }

    try {
      output.toBlob(
        (blob) => {
          setPreparing(false)
          if (!blob) {
            message.error(t('gallery.capture.failed'))
            return
          }
          onSave(
            new File([blob], `photo-${Date.now()}.jpg`, {
              type: 'image/jpeg',
              lastModified: Date.now(),
            }),
            fusedFrameId,
          )
        },
        'image/jpeg',
        0.92,
      )
    } catch {
      setPreparing(false)
      message.error(t('gallery.capture.failed'))
    }
  }

  const mirrorStyle = {
    '--kv-mirror-x': mirrorX ? '-1' : '1',
    '--kv-mirror-y': mirrorY ? '-1' : '1',
  } as CSSProperties

  const canSave =
    mode === 'review' &&
    hasCapture &&
    !uploading &&
    !preparing &&
    (!frameUrl || frameStatus === 'ready' || frameStatus === 'error')

  const uploadButton = (
    <Upload
      accept={IMAGE_ACCEPT}
      showUploadList={false}
      beforeUpload={(file) => {
        pickFile(file)
        return Upload.LIST_IGNORE
      }}
    >
      <Button icon={<PictureOutlined />}>{t('gallery.capture.uploadFallback')}</Button>
    </Upload>
  )

  const gridOptions: { value: CaptureGrid; label: string }[] = [
    { value: 'off', label: t('gallery.capture.gridOff') },
    { value: 'thirds', label: '3×3' },
    { value: 'square', label: '1×1' },
    { value: 'golden', label: t('gallery.capture.goldenShort') },
  ]

  const frameHint =
    frameStatus === 'error'
      ? t('gallery.capture.frameError')
      : t('gallery.capture.frameHint')

  const failureCopy: Record<CameraFailure, { title: string; hint: string }> = {
    denied: {
      title: tc('errors.CAMERA_DENIED'),
      hint: t('gallery.capture.deniedHint'),
    },
    unavailable: {
      title: t('gallery.capture.unavailableTitle'),
      hint: t('gallery.capture.unavailableHint'),
    },
    insecure: {
      title: t('gallery.capture.insecureTitle'),
      hint: t('gallery.capture.insecureHint'),
    },
    unsupported: {
      title: t('gallery.capture.unsupportedTitle'),
      hint: t('gallery.capture.unsupportedHint'),
    },
  }
  const failure = cameraError ? failureCopy[cameraError] : null

  return (
    <div className="kv-camera">
      <div className="kv-camera__stagewrap">
        <div className={`kv-camera__stage kv-camera__stage--${ratio}`}>
          {!cameraError && mode === 'live' ? (
            <video
              ref={attachVideo}
              className="kv-camera__video"
              style={mirrorStyle}
              aria-label={t('gallery.capture.title')}
              autoPlay
              playsInline
              muted
              onLoadedMetadata={markReady}
              onCanPlay={markReady}
              onPlaying={markReady}
            />
          ) : null}

          {mode === 'review' && previewUrl ? (
            <img
              className="kv-camera__preview"
              src={previewUrl}
              alt={t('gallery.capture.review')}
              style={mirrorStyle}
            />
          ) : null}

          {failure && mode === 'live' ? (
            <div className="kv-camera__denied">
              <div className="kv-camera__denied-body">
                <CameraOutlined className="kv-camera__denied-icon" aria-hidden="true" />
                <Typography.Text className="kv-camera__denied-title">
                  {failure.title}
                </Typography.Text>
                <Typography.Text className="kv-camera__denied-hint">
                  {failure.hint}
                </Typography.Text>
                <div className="kv-camera__denied-actions">
                  {cameraError === 'denied' || cameraError === 'unavailable' ? (
                    <Button size="small" onClick={retryCamera}>
                      {t('gallery.capture.retry')}
                    </Button>
                  ) : null}
                  {uploadButton}
                </div>
              </div>
            </div>
          ) : null}

          {!cameraError && mode === 'live' && needsGesture ? (
            <button
              type="button"
              className="kv-camera__resume"
              onClick={resumePlayback}
            >
              {t('gallery.capture.tapToStart')}
            </button>
          ) : null}

          {!cameraError && mode === 'live' && !ready && !needsGesture ? (
            <span className="kv-camera__starting">{t('gallery.capture.starting')}</span>
          ) : null}

          {/* The review image must preview the frame that save() will fuse,
              including the upload fallback shown after a camera failure. */}
          {(!cameraError || mode === 'review') && frameUrl && frameStatus !== 'error' ? (
            <img
              className="kv-camera__frame"
              src={frameUrl}
              alt=""
              aria-hidden="true"
            />
          ) : null}

          {!cameraError && mode === 'live' ? <GridOverlay grid={grid} /> : null}

          {mode === 'review' ? (
            <span className="kv-camera__badge">{t('gallery.capture.review')}</span>
          ) : null}
        </div>
      </div>

      <aside className="kv-camera__rail" aria-label={t('gallery.capture.controls')}>
        <section className="kv-camera__panel">
          <Typography.Title level={2} className="kv-card-title">
            {t('gallery.frame')}
          </Typography.Title>
          {framesLoading ? (
            <Typography.Text type="secondary" className="kv-camera__note">
              {t('gallery.capture.framesLoading')}
            </Typography.Text>
          ) : framesError ? (
            <div className="kv-camera__note">
              <Typography.Text type="secondary">
                {t('gallery.capture.framesError')}
              </Typography.Text>{' '}
              <Button type="link" size="small" onClick={onReloadFrames}>
                {t('gallery.capture.retry')}
              </Button>
            </div>
          ) : frames.length === 0 ? (
            <Typography.Text type="secondary" className="kv-camera__note">
              {t('gallery.capture.noFrames')}
            </Typography.Text>
          ) : (
            <div className="kv-camera__frames">
              <button
                type="button"
                className={`kv-camera__frame-chip${frameId === null ? ' is-selected' : ''}`}
                aria-pressed={frameId === null}
                onClick={() => onFrameChange(null)}
              >
                <span className="kv-camera__frame-none" aria-hidden="true">
                  ×
                </span>
                <span className="kv-camera__frame-name">{t('gallery.noFrame')}</span>
              </button>
              {frames.map((frame) => (
                <button
                  key={frame.id}
                  type="button"
                  className={`kv-camera__frame-chip${frame.id === frameId ? ' is-selected' : ''}`}
                  aria-pressed={frame.id === frameId}
                  title={frame.name}
                  onClick={() => onFrameChange(frame.id)}
                >
                  <img
                    className="kv-camera__frame-thumb"
                    src={frame.image_url}
                    alt=""
                    loading="lazy"
                  />
                  <span className="kv-camera__frame-name">{frame.name}</span>
                </button>
              ))}
            </div>
          )}
          <Typography.Text type="secondary" className="kv-camera__hint">
            {frameHint}
          </Typography.Text>
        </section>

        <section className="kv-camera__panel">
          <Typography.Title level={2} className="kv-card-title">
            {t('gallery.capture.guide')}
          </Typography.Title>

          <div className="kv-camera__control">
            <span className="kv-camera__label">{t('gallery.capture.grid')}</span>
            <Segmented
              block
              value={grid}
              options={gridOptions}
              onChange={(value) => setGrid(value as CaptureGrid)}
              disabled={mode === 'review'}
              aria-label={t('gallery.capture.grid')}
            />
          </div>

          <div className="kv-camera__control">
            <span className="kv-camera__label">{t('gallery.capture.ratio')}</span>
            <Segmented
              block
              value={ratio}
              options={[
                { value: 'portrait', label: '9:16' },
                { value: 'landscape', label: '16:9' },
              ]}
              onChange={(value) => setRatio(value as CaptureRatio)}
              disabled={mode === 'review'}
              aria-label={t('gallery.capture.ratio')}
            />
            <Typography.Text type="secondary" className="kv-camera__hint">
              {t('gallery.capture.ratioHint', {
                ratio: RATIOS[ratio].outputLabel,
              })}
            </Typography.Text>
          </div>

          <div className="kv-camera__mirrors">
            <label className="kv-camera__switch">
              <span>{t('gallery.capture.mirrorHorizontal')}</span>
              <Switch
                checked={mirrorX}
                onChange={setMirrorX}
                aria-label={t('gallery.capture.mirrorHorizontal')}
              />
            </label>
            <label className="kv-camera__switch">
              <span>{t('gallery.capture.mirrorVertical')}</span>
              <Switch
                checked={mirrorY}
                onChange={setMirrorY}
                aria-label={t('gallery.capture.mirrorVertical')}
              />
            </label>
          </div>
        </section>

        <div className="kv-camera__actions">
          {mode === 'live' ? (
            cameraError ? null : (
              <div className="kv-camera__shutter-wrap">
                <button
                  type="button"
                  className="kv-camera__shutter"
                  onClick={takePhoto}
                  disabled={!ready || needsGesture || uploading}
                  aria-label={t('gallery.capture.take')}
                />
                <span className="kv-camera__shutter-label">
                  {ready && !needsGesture
                    ? t('gallery.capture.take')
                    : t('gallery.capture.starting')}
                </span>
              </div>
            )
          ) : (
            <div className="kv-camera__review-actions">
              <Button onClick={retake} disabled={uploading || preparing}>
                {t('gallery.capture.retake')}
              </Button>
              <Button
                type="primary"
                onClick={save}
                loading={uploading || preparing}
                disabled={!canSave}
              >
                {t('gallery.capture.save')}
              </Button>
              <Typography.Text type="secondary" className="kv-camera__hint">
                {t('gallery.capture.reviewHint')}
              </Typography.Text>
            </div>
          )}
        </div>
      </aside>
    </div>
  )
}
