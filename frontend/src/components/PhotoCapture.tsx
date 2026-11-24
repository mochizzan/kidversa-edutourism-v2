/**
 * Camera capture for gallery photos (spec F-10 / FL-10, 02§3).
 *
 * - getUserMedia preview → centre-crop to 9:16 on canvas → optional frame
 *   overlay fused in → emits a JPEG `File` (the caller uploads it:
 *   POST P/photos + X-Idempotency-Key).
 * - TRANSIENT local preview only: shown while the caller reports
 *   `uploading` and labelled "mengunggah…" — never a gallery item, never
 *   optimistic (honest-file rule: the gallery refetches server /uploads/…
 *   bytes after the 200 + `photo.changed`).
 * - Camera denied / unavailable → CAMERA_DENIED state with an
 *   upload-fallback picker (spec 06 E28) that pre-checks the ONE upload
 *   rule client-side (jpg/jpeg/png ≤50 MB, shared gate in lib/validation,
 *   spec 09 2026-11-10) before handing the file to the caller.
 */
import { useEffect, useRef, useState } from 'react'
import { Alert, App as AntApp, Button, Image, Typography, Upload } from 'antd'
import type { UploadProps } from 'antd'
import { CameraOutlined, PictureOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { IMAGE_ACCEPT, isAcceptedImage } from '../pages/masters/shared'

export interface PhotoCaptureProps {
  /** Receives the captured or fallback-picked file; the caller performs the upload. */
  onCapture: (file: File) => void
  /** True while the caller's upload is in flight — drives the "mengunggah…" preview. */
  uploading?: boolean
  /** Frame image URL (/uploads/…) drawn over the capture, when provided. */
  frameUrl?: string | null
}

const OUTPUT_WIDTH = 1080
const OUTPUT_HEIGHT = 1920 // 9:16

export default function PhotoCapture({ onCapture, uploading = false, frameUrl = null }: PhotoCaptureProps) {
  const { t } = useTranslation('session')
  const { t: tCommon } = useTranslation('common')
  const { message } = AntApp.useApp()
  const videoRef = useRef<HTMLVideoElement | null>(null)
  const frameRef = useRef<HTMLImageElement | null>(null)
  const previewRef = useRef<string | null>(null)
  const [preview, setPreview] = useState<string | null>(null)
  // Browsers without getUserMedia (or insecure context) start in CAMERA_DENIED.
  const [denied, setDenied] = useState(() => !navigator.mediaDevices?.getUserMedia)

  // Transient local preview: exactly one blob URL at a time, revoked on
  // replace and on unmount (visibility stays derived from `uploading`).
  const holdPreview = (url: string | null) => {
    if (previewRef.current) URL.revokeObjectURL(previewRef.current)
    previewRef.current = url
    setPreview(url)
  }

  // Camera lifecycle: start with the component, stop on unmount or denial.
  useEffect(() => {
    if (denied) return
    let cancelled = false
    let media: MediaStream | null = null
    navigator.mediaDevices!
      .getUserMedia({ video: { facingMode: 'environment' } })
      .then((stream) => {
        if (cancelled) {
          stream.getTracks().forEach((track) => track.stop())
          return
        }
        media = stream
        if (videoRef.current) videoRef.current.srcObject = stream
      })
      .catch(() => {
        if (!cancelled) setDenied(true)
      })
    return () => {
      cancelled = true
      media?.getTracks().forEach((track) => track.stop())
    }
  }, [denied])

  useEffect(
    () => () => {
      if (previewRef.current) URL.revokeObjectURL(previewRef.current)
    },
    [],
  )

  // Frame overlay image (same-origin /uploads/…; capture skips it if not loaded).
  useEffect(() => {
    if (!frameUrl) {
      frameRef.current = null
      return
    }
    const image = new globalThis.Image() // window.Image — antd Image shadows the global below
    image.crossOrigin = 'anonymous'
    image.src = frameUrl
    frameRef.current = image
  }, [frameUrl])

  const capture = () => {
    const video = videoRef.current
    if (!video || video.videoWidth === 0 || video.videoHeight === 0) return
    const { videoWidth: vw, videoHeight: vh } = video
    const target = OUTPUT_WIDTH / OUTPUT_HEIGHT
    let cropWidth = vw
    let cropHeight = Math.round(vw / target)
    if (cropHeight > vh) {
      cropHeight = vh
      cropWidth = Math.round(vh * target)
    }
    const canvas = document.createElement('canvas')
    canvas.width = OUTPUT_WIDTH
    canvas.height = OUTPUT_HEIGHT
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.drawImage(
      video,
      Math.round((vw - cropWidth) / 2),
      Math.round((vh - cropHeight) / 2),
      cropWidth,
      cropHeight,
      0,
      0,
      OUTPUT_WIDTH,
      OUTPUT_HEIGHT,
    )
    const frame = frameRef.current
    if (frame?.complete && frame.naturalWidth > 0) {
      ctx.drawImage(frame, 0, 0, OUTPUT_WIDTH, OUTPUT_HEIGHT)
    }
    canvas.toBlob((blob) => {
      if (!blob) return
      holdPreview(URL.createObjectURL(blob))
      onCapture(new File([blob], 'capture.jpg', { type: 'image/jpeg', lastModified: Date.now() }))
    }, 'image/jpeg', 0.92)
  }

  const beforeUpload: UploadProps['beforeUpload'] = (file) => {
    // Client-side pre-check — the ONE shared upload gate (spec 09,
    // 2026-11-10): block oversize or wrong format before the caller ever
    // sees the file (no request is issued).
    if (!isAcceptedImage(file)) {
      message.error(t('gallery.badFormat'))
      return Upload.LIST_IGNORE
    }
    holdPreview(URL.createObjectURL(file))
    onCapture(file)
    return false // local picker only — the caller uploads
  }

  const uploadButton = (
    <Upload accept={IMAGE_ACCEPT} showUploadList={false} beforeUpload={beforeUpload}>
      <Button icon={<PictureOutlined />}>{t('gallery.capture.uploadFallback')}</Button>
    </Upload>
  )

  if (denied) {
    return (
      <Alert
        type="warning"
        showIcon
        message={tCommon('errors.CAMERA_DENIED')}
        description={uploadButton}
      />
    )
  }

  return (
    <div className="kv-capture">
      <video
        ref={videoRef}
        className="kv-capture__video"
        aria-label={t('gallery.capture.title')}
        autoPlay
        playsInline
        muted
      />
      {preview && uploading ? (
        <div className="kv-capture__preview">
          <Image preview={false} src={preview} alt={t('gallery.capture.uploading')} />
          <Typography.Text className="kv-capture__hint">
            {t('gallery.capture.uploading')}
          </Typography.Text>
        </div>
      ) : null}
      <div className="kv-capture__bar">
        <Button type="primary" icon={<CameraOutlined />} onClick={capture}>
          {t('gallery.capture.take')}
        </Button>
        {uploadButton}
      </div>
    </div>
  )
}
