/**
 * Dedicated child gallery `/sessions/:sid/gallery/children/:cid` (F-10 /
 * FL-10): everything about one child's photos lives here — topic selection,
 * the parental-consent gate, capture, frames and the photo grid.
 *
 * The gallery index is a directory (`GalleryPage`); this page is the workspace.
 * Splitting them means the index never fetches photos and the consent lock
 * protects exactly the surface that shows or captures them.
 *
 * Consent gate: member consent !== SETUJU → locked panel with the status and a
 * link to `/consent/:sid` (the send/resend action lives on the consent page;
 * FL-11's `resent` vs already-decided envelope is handled there).
 *
 * Upload/capture: `PhotoCapture` (camera or file fallback) → the ONE shared
 * client pre-check `isAcceptedImage` (jpg/jpeg/png ≤50 MB, spec 09
 * 2026-11-10) → `api.upload` multipart field `file` (+ optional `frame_id`) →
 * the transient preview exists only while `uploading`; after the server 200 the
 * item appears ONLY via `photo.changed` → refetch (honest-file rule — no
 * optimistic gallery row, no success toast before 200). 11th photo →
 * PHOTO_LIMIT shown inline. Frames: program-linked frame list (SSE
 * `frame.changed`), optional overlay fused at capture.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, App as AntApp, Button, Select, Space, Typography } from 'antd'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import PhotoCapture from '../../components/PhotoCapture'
import FilterBar, { FilterField } from '../../components/FilterBar'
import { api, errorCode } from '../../lib/api'
import { useCrumbName } from '../../lib/crumbs'
import { useErrorToast } from '../../lib/errors'
import { useEventRefetch, useListLoader } from '../../lib/list'
import { isAcceptedImage } from '../masters/shared'
import {
  consentStatusLabel,
  pPrefix,
  type PhotoItem,
} from '../../lib/session-contracts'
import { ConsentTag } from '../sessions/controls'
import { useSessionDetail } from '../sessions/shared'
import PhotoGrid from './PhotoGrid'
import './gallery.css'

interface FrameRow {
  id: number
  name: string
  image_url: string
}

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
  const { sid, cid: cidParam } = useParams()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { detail, error: detailError, deleted, reload: reloadDetail } = useSessionDetail(sid)

  const cid = Number(cidParam)

  // The breadcrumb bar shows the session's name and the child's name.
  useCrumbName('sid', detail?.session.name)

  const found =
    detail?.groups
      .flatMap((group) =>
        group.own
          ? (group.members ?? []).map((member) => ({ member, group }))
          : [],
      )
      .find((entry) => entry.member.child_id === cid) ?? null
  const member = found?.member ?? null
  const group = found?.group ?? null

  useCrumbName('cid', member?.name)

  const [stid, setStid] = useState<number | null>(
    () => Number(searchParams.get('stid')) || null,
  )
  const [photoState, setPhotoState] = useState<PhotoState | null>(null)
  const photoRequest = useRef(0)
  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState<string | null>(null)
  const [frameChoice, setFrameChoice] = useState<number | null>(null)

  useEffect(() => {
    if (deleted) navigate('/sessions', { replace: true })
  }, [deleted, navigate])

  // A stale `?stid=` falls back to the session's first topic; the raw state is
  // kept so the picker can still move to a real topic afterwards.
  const activeStid =
    stid !== null && detail?.topics.some((topic) => topic.stid === stid)
      ? stid
      : detail?.topics[0]?.stid ?? null

  // No consent row yet is NOT the same as a pending request: keep '' so the
  // tag and lock message say "Not yet submitted" instead of "Pending".
  const consent = member?.consent ?? ''
  const unlocked = consent === 'SETUJU'

  const prefix =
    sid !== undefined && activeStid !== null && member !== null && unlocked
      ? pPrefix(sid, activeStid, cid)
      : null

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
  useEventRefetch(['consent.changed'], reloadDetail)

  const programId = detail?.session.program_id ?? null
  // Frame overlay is decoration (capture works without it): errors are
  // simply never surfaced; the list refetches on `frame.changed`.
  const { rows: frames } = useListLoader<FrameRow>(
    unlocked && programId !== null ? '/api/frames' : null,
    { program_id: programId ?? undefined, page: 1, per_page: 1000 },
    ['frame.changed'],
  )

  // The effective frame is derived, never stored by an effect: the first
  // available frame is the default, `-1` is the explicit "no frame" choice,
  // and an id the latest `frame.changed` removed heals back to the default.
  const frameId =
    frameChoice !== null &&
    (frameChoice === -1 || frames.some((frame) => frame.id === frameChoice))
      ? frameChoice
      : frames[0]?.id ?? null
  const selectedFrame = frames.find((frame) => frame.id === frameId) ?? null

  const changeTopic = (next: number) => {
    setStid(next)
    setSearchParams({ stid: String(next) }, { replace: true })
  }

  const onCapture = async (file: File) => {
    if (!prefix) return
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
      if (selectedFrame) form.append('frame_id', String(selectedFrame.id))
      await api.upload(`${prefix}/photos`, form, crypto.randomUUID())
      // Server 200: drop the transient preview; the item appears only after
      // `photo.changed` → refetch (honest-file rule — no success toast).
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
            <ConsentTag status={consent} />
          </Space>
        }
        actions={
          <Space>
            <Button
              icon={<ArrowLeftOutlined />}
              onClick={() => navigate(`/sessions/${sid}/gallery`)}
            >
              {t('gallery.backToGallery')}
            </Button>
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

          {activeStid === null ? (
            <Alert type="info" showIcon message={t('gallery.noTopic')} />
          ) : (
            <div className="kv-gallery-layout">
              <section className="kv-gallery-capture" aria-label={t('gallery.capture.title')}>
                <PhotoCapture
                  onCapture={(file) => void onCapture(file)}
                  uploading={uploading}
                  frameUrl={selectedFrame?.image_url ?? null}
                />
                <div className="kv-gallery-frame">
                  <label className="kv-field-label" htmlFor="child-gallery-frame">
                    {t('gallery.frame')}
                  </label>
                  <Select
                    id="child-gallery-frame"
                    size="small"
                    value={frameId ?? undefined}
                    onChange={setFrameChoice}
                    options={[
                      { value: -1, label: t('gallery.noFrame') },
                      ...frames.map((frame) => ({ value: frame.id, label: frame.name })),
                    ]}
                    placeholder={t('gallery.noFrame')}
                    aria-label={t('gallery.frame')}
                  />
                </div>
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
              </section>

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
            </div>
          )}
        </>
      )}
    </>
  )
}
