/**
 * PhotoEditorModal (spec 09 decision 6/7): the crop editor behind ProfilePage.
 *
 * - Self-contained: the file picker + drag-drop live HERE (the parent only
 *   renders the modal and receives the finished File). The source decodes
 *   through the loadImage seam, shows in the square .kv-crop-vp viewport,
 *   and is composed by pan (drag), zoom (wheel + numeric control) and
 *   rotate (0–360° slider) — the preview grid overlay is what gets cropped.
 * - Confirm composes a 512×512 square JPEG at high quality WITH THE BROWSER:
 *   the server never sees crop parameters (spec 09 decision 7) — it only
 *   whitelists/compresses/stores. The canvas targets (translate/rotate/
 *   scale/draw) mirror cropTransform exactly (k = 512/VIEW), so the preview
 *   and the export can never disagree.
 * - While the parent uploads, the live 0–100% progress shows; once the bytes
 *   are fully away the honest "processing" state replaces the number
 *   (the server still compresses/stores). Delete shows ONLY when a photo
 *   already exists, and never closes the modal itself — the close follows
 *   the server answer through the parent.
 */
import { useEffect, useRef, useState } from 'react'
import { Alert, Button, Modal, Progress } from 'antd'
import { MinusOutlined, PictureOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { clampPan, clampZoom, coverScale, cropTransform } from './cropMath'
import loadImage from './loadImage'
import './PhotoEditorModal.css'

/** Square viewport px — mirrored by PhotoEditorModal.test.tsx. */
const VIEW = 320
/** Export square px (spec 09 decision 7). */
const EXPORT = 512
/** Wheel zoom step per notch (in when scrolling up). */
const ZOOM_STEP = 1.08
/** Zoom-button step in slider percentage points (band = fit → 3×fit). */
const ZOOM_BTN_PCT = 10
/** Export JPEG quality (high, spec 09). */
const JPEG_QUALITY = 0.92

interface PhotoEditorModalProps {
  open: boolean
  /** An avatar already exists (enables the Delete action). */
  hasPhoto: boolean
  /** The parent is POSTing the composed file (live progress). */
  uploading: boolean
  /** 0–100 upload progress. */
  progress: number
  onClose: () => void
  onConfirm: (file: File) => void
  /** Only fires for an existing photo; the parent closes afterwards. */
  onDelete: () => void
}

interface DragStart {
  id: number
  px: number
  py: number
  panX: number
  panY: number
}

export default function PhotoEditorModal({
  open,
  hasPhoto,
  uploading,
  progress,
  onClose,
  onConfirm,
  onDelete,
}: PhotoEditorModalProps) {
  const { t } = useTranslation('auth')
  const [file, setFile] = useState<Blob | null>(null)
  const [image, setImage] = useState<HTMLImageElement | null>(null)
  const [srcUrl, setSrcUrl] = useState('')
  const [loadError, setLoadError] = useState(false)
  // zoom stores the RAW scale; coverScale clamps it for display/compuse.
  const [zoom, setZoom] = useState(0.1)
  const [pan, setPan] = useState({ x: 0, y: 0 })
  const [deg, setDeg] = useState(0)
  const dragRef = useRef<DragStart | null>(null)

  const minZoom = image ? coverScale(image.naturalWidth, image.naturalHeight, VIEW, VIEW) : 0.1
  // Zoom as a 0–100 slider position across the [minZoom, 3×minZoom] band;
  // buttons, slider, and wheel all funnel through the same clamped setter.
  const zoomPct = Math.round(((zoom - minZoom) / (2 * minZoom)) * 100)
  const setZoomPct = (pct: number): void => {
    setZoom(clampZoom(minZoom * (1 + (2 * Math.min(Math.max(pct, 0), 100)) / 100), minZoom))
  }
  // The ONE transform: preview CSS and canvas export consume the same values.
  const tform = image
    ? cropTransform(image.naturalWidth, image.naturalHeight, VIEW, VIEW, zoom, pan.x, pan.y, deg)
    : null

  // Closed modal drops every edit state (a reopen starts clean).
  useEffect(() => {
    if (!open) {
      setFile(null)
      setImage(null)
      setSrcUrl('')
      setLoadError(false)
      setZoom(0.1)
      setPan({ x: 0, y: 0 })
      setDeg(0)
    }
  }, [open])

  // Source decode: object-URL lifecycle + the loadImage seam. A decode
  // failure is a friendly error and a disabled Confirm — never a crash.
  useEffect(() => {
    if (!open || !file) return undefined
    const url = URL.createObjectURL(file)
    let cancelled = false
    loadImage(url)
      .then((img) => {
        if (cancelled) return
        setImage(img)
        setSrcUrl(url)
        setZoom(coverScale(img.naturalWidth, img.naturalHeight, VIEW, VIEW))
        setPan({ x: 0, y: 0 })
        setDeg(0)
        setLoadError(false)
      })
      .catch(() => {
        if (cancelled) return
        setImage(null)
        setSrcUrl('')
        setLoadError(true)
      })
    return () => {
      cancelled = true
      URL.revokeObjectURL(url)
    }
  }, [open, file])

  const pickSource = (picked: Blob): void => setFile(picked)

  const compose = async (): Promise<void> => {
    if (!image) return
    const canvas = document.createElement('canvas')
    canvas.width = EXPORT
    canvas.height = EXPORT
    const ctx = canvas.getContext('2d')
    if (!ctx) return
    ctx.save()
    ctx.clearRect(0, 0, EXPORT, EXPORT)
    // Exactly cropTransform's numbers, scaled into the 512×512 export
    // (the same inputs as the preview's tform → the same clamped transform —
    // recomputed here as a non-null local value).
    const k = EXPORT / VIEW
    const drawn = cropTransform(
      image.naturalWidth, image.naturalHeight, VIEW, VIEW, zoom, pan.x, pan.y, deg,
    )
    ctx.translate(EXPORT / 2 + drawn.pan.x * k, EXPORT / 2 + drawn.pan.y * k)
    ctx.rotate((drawn.deg * Math.PI) / 180)
    ctx.scale(drawn.scale * k, drawn.scale * k)
    ctx.drawImage(image, -image.naturalWidth / 2, -image.naturalHeight / 2)
    ctx.restore()
    const blob = await new Promise<Blob | null>((resolve) => {
      canvas.toBlob(resolve, 'image/jpeg', JPEG_QUALITY)
    })
    if (blob) onConfirm(new File([blob], 'avatar.jpg', { type: 'image/jpeg' }))
  }

  if (!open) return null

  return (
    <Modal
      className="kv-photo-editor"
      open
      title={t('profile.editorTitle')}
      width={480}
      onCancel={onClose}
      footer={[
        ...(hasPhoto
          ? [
              <Button key="delete" danger disabled={uploading} onClick={onDelete}>
                {t('profile.deletePhoto')}
              </Button>,
            ]
          : []),
        <Button key="cancel" onClick={onClose}>
          {t('profile.cancelBtn')}
        </Button>,
        <Button
          key="confirm"
          type="primary"
          disabled={!image || uploading}
          onClick={() => void compose()}
        >
          {t('profile.confirmPhoto')}
        </Button>,
      ]}
    >
      {uploading &&
        (progress >= 100 ? (
          <p className="kv-crop-processing">{t('profile.processing')}</p>
        ) : (
          <Progress percent={progress} />
        ))}

      {!image && !loadError && (
        <div
          className="kv-crop-drop"
          onDragOver={(e) => e.preventDefault()}
          onDrop={(e) => {
            e.preventDefault()
            const dropped = e.dataTransfer?.files?.[0]
            if (dropped) pickSource(dropped)
          }}
        >
          <PictureOutlined className="kv-crop-drop__icon" />
          <span>{t('profile.pickPhoto')}</span>
          <input
            type="file"
            accept="image/jpeg,image/jpg,image/png"
            aria-label={t('profile.pickPhoto')}
            className="kv-crop-file"
            onChange={(e) => {
              const picked = e.target.files?.[0]
              if (picked) pickSource(picked)
              e.target.value = ''
            }}
          />
        </div>
      )}

      {loadError && <Alert type="error" showIcon message={t('profile.loadError')} />}

      {image && tform && (
        <>
          <div
            className="kv-crop-vp"
            style={{ width: VIEW, height: VIEW }}
            onDragStart={(e) => e.preventDefault()}
            onDrop={(e) => e.preventDefault()}
            onContextMenu={(e) => e.preventDefault()}
            onWheel={(e) => {
              e.preventDefault()
              const factor = e.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP
              setZoom(clampZoom(zoom * factor, minZoom))
            }}
            onPointerDown={(e) => {
              dragRef.current = {
                id: e.pointerId,
                px: e.clientX,
                py: e.clientY,
                // Start from the EFFECTIVE (clamped) pan: the raw state can
                // sit outside the cover band after zoom changes, and a drag
                // from it feels stuck until it burns through the excess.
                panX: tform.pan.x,
                panY: tform.pan.y,
              }
              const el = e.currentTarget as HTMLElement & {
                setPointerCapture?: (id: number) => void
              }
              try {
                el.setPointerCapture?.(e.pointerId)
              } catch {
                /* jsdom/browsers without capture: drag still works */
              }
            }}
            onPointerMove={(e) => {
              const start = dragRef.current
              if (!start || start.id !== e.pointerId) return
              // Clamp the stored pan into the cover band so the state never
              // drifts past what the viewport can show (rotation swaps the
              // extents, so the band must see the current angle).
              setPan(
                clampPan(
                  start.panX + (e.clientX - start.px),
                  start.panY + (e.clientY - start.py),
                  image.naturalWidth * tform.scale,
                  image.naturalHeight * tform.scale,
                  VIEW,
                  VIEW,
                  deg,
                ),
              )
            }}
            onPointerUp={() => {
              dragRef.current = null
            }}
            onPointerCancel={() => {
              dragRef.current = null
            }}
          >
            <div className="kv-crop-stage">
              <img
                className="kv-crop-img"
                src={srcUrl}
                alt=""
                draggable={false}
                style={{ transform: tform.cssTransform }}
              />
            </div>
            <div className="kv-crop-grid" />
          </div>
          <p className="kv-crop-hint">{t('profile.cropHint')}</p>
          <div className="kv-crop-controls">
            <div className="kv-crop-row">
              <span className="kv-crop-row__label">{t('profile.zoomLabel')}</span>
              <Button
                size="small"
                icon={<MinusOutlined />}
                aria-label={t('profile.zoomOut')}
                disabled={uploading}
                onClick={() => setZoomPct(zoomPct - ZOOM_BTN_PCT)}
              />
              <input
                type="range"
                aria-label={t('profile.zoomLabel')}
                min={0}
                max={100}
                value={zoomPct}
                onChange={(e) => {
                  const pct = Number(e.target.value)
                  if (!Number.isNaN(pct)) setZoomPct(pct)
                }}
              />
              <Button
                size="small"
                icon={<PlusOutlined />}
                aria-label={t('profile.zoomIn')}
                disabled={uploading}
                onClick={() => setZoomPct(zoomPct + ZOOM_BTN_PCT)}
              />
            </div>
            <div className="kv-crop-row">
              <span className="kv-crop-row__label">{t('profile.rotateLabel')}</span>
              <input
                type="range"
                aria-label={t('profile.rotateLabel')}
                min={0}
                max={360}
                value={deg}
                onChange={(e) => setDeg(Number(e.target.value))}
              />
            </div>
          </div>
        </>
      )}
    </Modal>
  )
}
