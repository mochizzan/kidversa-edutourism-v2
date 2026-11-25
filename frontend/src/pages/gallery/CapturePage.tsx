/**
 * Camera studio `/sessions/:sid/gallery/children/:cid/capture` (F-10 / FL-10):
 * the capture half of the child workspace. The gallery owns the photo grid;
 * this page owns the viewfinder, frame picker, composition guides, mirror
 * switches and the review step.
 *
 * The studio hands back ONE fused JPEG (`CameraStudio` draws the selected
 * frame into the pixels), so the upload is the same multipart contract as the
 * gallery's direct upload — plus `frame_id` for provenance. The saved photo
 * appears on the gallery only through `photo.changed` → refetch (honest-file
 * rule), then this page returns to the child gallery with the topic intact.
 *
 * The consent gate mirrors the gallery exactly: a direct URL can never bypass
 * it because the studio only mounts when consent is SETUJU.
 */
import { useState } from 'react'
import { Alert, App as AntApp, Button, Space, Typography } from 'antd'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import { api, errorCode } from '../../lib/api'
import { isAcceptedImage } from '../../lib/validation'
import { consentStatusLabel } from '../../lib/session-contracts'
import { ConsentTag } from '../sessions/controls'
import CameraStudio from './CameraStudio'
import {
  childGalleryPath,
  useChildGallery,
  useProgramFrames,
} from './shared'
import './gallery.css'
import './capture.css'

/** Gallery URL that keeps the camera's topic scope when coming back. */
function galleryHref(sid: string | undefined, cid: number, stid: number | null): string {
  const base = childGalleryPath(sid ?? '', cid)
  return stid === null ? base : `${base}?stid=${stid}`
}

export default function CapturePage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const navigate = useNavigate()
  const {
    sid,
    cid,
    detail,
    detailError,
    member,
    group,
    activeStid,
    consent,
    unlocked,
    prefix,
  } = useChildGallery()

  const programId = detail?.session.program_id ?? null
  const {
    frames,
    loading: framesLoading,
    error: framesError,
    reload: reloadFrames,
  } = useProgramFrames(programId, unlocked)

  // The effective frame is derived, never stored by an effect: the first
  // program frame is the default, -1 is the explicit "no frame" choice, and
  // an id a `frame.changed` refetch removed heals back to the default.
  const [frameChoice, setFrameChoice] = useState<number | null>(null)
  const frameId =
    frameChoice !== null &&
    (frameChoice === -1 || frames.some((frame) => frame.id === frameChoice))
      ? frameChoice === -1
        ? null
        : frameChoice
      : frames[0]?.id ?? null

  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState<string | null>(null)

  const backToGallery = () =>
    navigate(galleryHref(sid, cid, activeStid), { replace: true })

  // When the child or session cannot be resolved, the child gallery would be a
  // dead end — send the user to the gallery directory instead.
  const backToIndex = () => navigate(`/sessions/${sid}/gallery`)

  const onSave = async (file: File, fusedFrameId: number | null) => {
    if (!prefix || uploading) return
    // The ONE shared upload gate (jpg/jpeg/png ≤50 MB, spec 09 2026-11-10).
    if (!isAcceptedImage(file)) {
      setUploadError(t('gallery.badFormat'))
      return
    }
    setUploading(true)
    setUploadError(null)
    try {
      const form = new FormData()
      form.append('file', file)
      if (fusedFrameId !== null) form.append('frame_id', String(fusedFrameId))
      await api.upload(`${prefix}/photos`, form, crypto.randomUUID())
      // Server 200: the gallery refetches via `photo.changed`; returning there
      // now shows the new item once the signal lands (honest-file rule).
      message.success(t('gallery.capture.saved'))
      backToGallery()
    } catch (failure: unknown) {
      const code = errorCode(failure)
      setUploadError(code === 'PHOTO_LIMIT' ? t('gallery.max') : tc(`errors.${code}`))
    } finally {
      setUploading(false)
    }
  }

  const backButton = (
    <Button icon={<ArrowLeftOutlined />} onClick={backToGallery}>
      {t('gallery.backToGallery')}
    </Button>
  )

  if (detailError) {
    return (
      <>
        <PageHeader
          title={t('gallery.capture.title')}
          actions={
            <Button icon={<ArrowLeftOutlined />} onClick={backToIndex}>
              {t('gallery.backToGallery')}
            </Button>
          }
        />
        <Alert
          type="error"
          showIcon
          message={tc(`errors.${errorCode(detailError)}`)}
          action={
            <Button onClick={backToIndex}>{t('gallery.backToGallery')}</Button>
          }
        />
      </>
    )
  }

  if (!detail) {
    return <PageSkeleton />
  }

  if (!member) {
    return (
      <>
        <PageHeader
          title={t('gallery.capture.title')}
          actions={
            <Button icon={<ArrowLeftOutlined />} onClick={backToIndex}>
              {t('gallery.backToGallery')}
            </Button>
          }
        />
        <Alert
          type="error"
          showIcon
          message={t('gallery.childNotFound')}
          action={
            <Button onClick={backToIndex}>{t('gallery.backToGallery')}</Button>
          }
        />
      </>
    )
  }

  const consentLabel = consentStatusLabel(t, consent)
  const topicName = detail.topics.find((topic) => topic.stid === activeStid)?.name

  if (!unlocked) {
    return (
      <>
        <PageHeader title={t('gallery.capture.title')} actions={backButton} />
        <Alert
          type="warning"
          showIcon
          message={t('gallery.consentLocked')}
          description={t('gallery.consentLabel', { status: consentLabel })}
          action={
            <Button type="link" size="small" onClick={() => navigate(`/consent/${sid}`)}>
              {t('gallery.openConsent')}
            </Button>
          }
        />
      </>
    )
  }

  if (activeStid === null) {
    return (
      <>
        <PageHeader title={t('gallery.capture.title')} actions={backButton} />
        <Alert type="info" showIcon message={t('gallery.noTopic')} />
      </>
    )
  }

  return (
    <>
      <PageHeader
        title={t('gallery.capture.title')}
        description={
          <Space wrap size={8}>
            <span>{member.name}</span>
            <span aria-hidden="true">·</span>
            <span>{group?.name}</span>
            <span aria-hidden="true">·</span>
            <span>{detail.session.name}</span>
            {topicName ? (
              <>
                <span aria-hidden="true">·</span>
                <span>{topicName}</span>
              </>
            ) : null}
            <ConsentTag status={consent} />
          </Space>
        }
        actions={backButton}
      />

      {uploadError ? (
        <Alert
          className="kv-gallery-upload-error"
          type="error"
          showIcon
          closable
          message={uploadError}
          onClose={() => setUploadError(null)}
        />
      ) : null}

      <Typography.Text type="secondary" className="kv-camera__intro">
        {t('gallery.capture.intro')}
      </Typography.Text>

      <CameraStudio
        frames={frames}
        framesLoading={framesLoading}
        framesError={framesError}
        onReloadFrames={reloadFrames}
        frameId={frameId}
        onFrameChange={(next) => setFrameChoice(next === null ? -1 : next)}
        uploading={uploading}
        onSave={(file, fusedFrameId) => void onSave(file, fusedFrameId)}
      />
    </>
  )
}
