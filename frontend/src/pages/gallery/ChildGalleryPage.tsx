/**
 * Dedicated child gallery `/sessions/:sid/gallery/children/:cid` (F-10 /
 * FL-10): photos only — topic selection, the parental-consent gate, the photo
 * grid, and the two ways to add a photo (open the camera studio or upload a
 * file). Capture itself lives on the separate studio route
 * (`…/capture`), reached from this page's primary action.
 *
 * Consent gate: member consent !== SETUJU → locked panel with the status and a
 * link to `/consent/:sid` (the send/resend action lives on the consent page;
 * FL-11's `resent` vs already-decided envelope is handled there).
 *
 * Upload: the ONE shared client pre-check `isAcceptedImage` (jpg/jpeg/png
 * ≤50 MB, spec 09 2026-11-10) → `api.upload` multipart field `file` → the
 * item appears ONLY via `photo.changed` → refetch (honest-file rule — no
 * optimistic gallery row, no success toast before 200). 11th photo →
 * PHOTO_LIMIT shown inline.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, App as AntApp, Button, Select, Space, Typography, Upload } from 'antd'
import {
  ArrowLeftOutlined,
  CameraOutlined,
  UploadOutlined,
} from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import FilterBar, { FilterField } from '../../components/FilterBar'
import { api, errorCode } from '../../lib/api'
import { useErrorToast } from '../../lib/errors'
import { useEventRefetch } from '../../lib/list'
import { IMAGE_ACCEPT, isAcceptedImage } from '../../lib/validation'
import {
  consentStatusLabel,
  type PhotoItem,
} from '../../lib/session-contracts'
import { ConsentTag } from '../sessions/controls'
import { childCapturePath, useChildGallery } from './shared'
import PhotoGrid from './PhotoGrid'
import './gallery.css'

/** Photo list stamped with the scope it belongs to, so a topic switch can never
 * paint the previous topic's photos for a frame while the new list loads. */
interface PhotoState {
  prefix: string
  photos: PhotoItem[] | null
  error: unknown
}

export default function ChildGalleryPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const toast = useErrorToast()
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
    changeTopic,
  } = useChildGallery()

  const [photoState, setPhotoState] = useState<PhotoState | null>(null)
  const photoRequest = useRef(0)
  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState<string | null>(null)

  const loadPhotos = useCallback(() => {
    // Bump first so a request started under an older scope is invalidated even
    // when the new scope has nothing to fetch (locked / no topic).
    const request = ++photoRequest.current
    if (!prefix) return
    // Only the newest request may land: a topic switch can leave an older
    // response in flight, and it must not repaint the new topic's grid.
    api.get<PhotoItem[]>(`${prefix}/photos`).then(
      (data) => {
        if (request !== photoRequest.current) return
        setPhotoState({ prefix, photos: data, error: null })
      },
      (failure: unknown) => {
        if (request !== photoRequest.current) return
        setPhotoState({ prefix, photos: null, error: failure })
      },
    )
  }, [prefix])

  useEffect(() => {
    loadPhotos()
  }, [loadPhotos])

  // States from another scope read as "still loading", so the grid never
  // flashes the previous topic while the fresh list is on its way.
  const scopedPhotos =
    photoState !== null && photoState.prefix === prefix ? photoState : null
  const photos = scopedPhotos?.photos ?? null
  const photoError = scopedPhotos?.error ?? null

  useEventRefetch(['photo.changed', 'copy.completed'], loadPhotos)

  const onUpload = async (file: File) => {
    if (!prefix || uploading) return
    // Client-side pre-check — the ONE shared upload gate (spec 09, 2026-11-10):
    // jpg/jpeg/png only, ≤50 MB, blocked BEFORE the FormData is built.
    if (!isAcceptedImage(file)) {
      setUploadError(t('gallery.badFormat'))
      return
    }
    setUploading(true)
    setUploadError(null)
    try {
      const form = new FormData()
      form.append('file', file)
      await api.upload(`${prefix}/photos`, form, crypto.randomUUID())
      // Server 200: the item appears only after `photo.changed` → refetch
      // (honest-file rule — no success toast).
    } catch (failure: unknown) {
      const code = errorCode(failure)
      setUploadError(code === 'PHOTO_LIMIT' ? t('gallery.max') : tc(`errors.${code}`))
    } finally {
      setUploading(false)
    }
  }

  const setCandidate = async (photoId: number) => {
    if (!prefix) return
    try {
      await api.patch(`${prefix}/photos/${photoId}`, { is_candidate: true })
      loadPhotos() // own-200 refetch; other tabs follow `photo.changed`
      message.success(t('gallery.candidateSet'))
    } catch (failure: unknown) {
      toast(failure)
    }
  }

  const deletePhoto = async (photoId: number) => {
    if (!prefix) return
    try {
      await api.del(`${prefix}/photos/${photoId}`)
      loadPhotos()
      message.success(t('gallery.deleted'))
    } catch (failure: unknown) {
      toast(failure)
    }
  }

  if (detailError) {
    return (
      <>
        <PageHeader title={t('gallery.title')} />
        <Alert
          type="error"
          showIcon
          message={tc(`errors.${errorCode(detailError)}`)}
          action={
            <Button onClick={() => navigate(`/sessions/${sid}/gallery`)}>
              {t('gallery.backToGallery')}
            </Button>
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
        <PageHeader title={t('gallery.title')} />
        <Alert
          type="error"
          showIcon
          message={t('gallery.childNotFound')}
          action={
            <Button onClick={() => navigate(`/sessions/${sid}/gallery`)}>
              {t('gallery.backToGallery')}
            </Button>
          }
        />
      </>
    )
  }

  const consentLabel = consentStatusLabel(t, consent)
  const topicName = detail.topics.find((topic) => topic.stid === activeStid)?.name
  const canAdd = unlocked && activeStid !== null

  return (
    <>
      <PageHeader
        title={member.name}
        description={
          <Space wrap size={8}>
            <span>{group?.name}</span>
            <span aria-hidden="true">·</span>
            <span>{member.school}</span>
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
        actions={
          <Space wrap>
            <Button
              icon={<ArrowLeftOutlined />}
              onClick={() => navigate(`/sessions/${sid}/gallery`)}
            >
              {t('gallery.backToGallery')}
            </Button>
            {canAdd ? (
              <Button
                type="primary"
                icon={<CameraOutlined />}
                onClick={() => navigate(childCapturePath(sid!, cid, activeStid))}
              >
                {t('gallery.takePhoto')}
              </Button>
            ) : null}
            {canAdd ? (
              <Upload
                accept={IMAGE_ACCEPT}
                showUploadList={false}
                disabled={uploading}
                beforeUpload={(file) => {
                  void onUpload(file)
                  return Upload.LIST_IGNORE
                }}
              >
                <Button icon={<UploadOutlined />} loading={uploading}>
                  {t('gallery.upload')}
                </Button>
              </Upload>
            ) : null}
            <Button type="text" onClick={() => navigate(`/consent/${sid}`)}>
              {t('consent.title')}
            </Button>
          </Space>
        }
      />

      {!unlocked ? (
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
      ) : (
        <>
          {detail.topics.length > 1 ? (
            <FilterBar>
              <FilterField label={t('gallery.topic')} htmlFor="child-gallery-topic" width={280}>
                <Select
                  id="child-gallery-topic"
                  value={activeStid ?? undefined}
                  onChange={changeTopic}
                  options={detail.topics.map((topic) => ({
                    value: topic.stid,
                    label: topic.name,
                  }))}
                  aria-label={t('gallery.topic')}
                  placeholder={t('gallery.pickTopic')}
                />
              </FilterField>
            </FilterBar>
          ) : null}

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

          {activeStid === null ? (
            <Alert type="info" showIcon message={t('gallery.noTopic')} />
          ) : (
            <section className="kv-gallery-photos">
              <div className="kv-gallery-photos__head">
                <Typography.Title level={2} className="kv-card-title">
                  {t('gallery.photosTitle')}
                </Typography.Title>
                {photos !== null ? (
                  <span className="kv-gallery-photos__count">
                    {t('gallery.photoCount', { count: photos.length })}
                  </span>
                ) : null}
              </div>
              <PhotoGrid
                photos={photos}
                loading={photos === null && photoError === null}
                error={photoError}
                onRetry={loadPhotos}
                onCandidate={(photoId) => void setCandidate(photoId)}
                onDelete={(photoId) => void deletePhoto(photoId)}
              />
            </section>
          )}
        </>
      )}
    </>
  )
}
