/**
 * Gallery `/sessions/:sid/gallery` (F-10 / FL-10): nav Session → Topic →
 * Group → Child (prefillable from the detail page via ?gid=&cid=&stid=).
 *
 * Consent gate: member consent !== SETUJU → locked panel with the status and
 * a link to `/consent/:sid` (the send/resend action lives on the consent
 * page; FL-11's `resent` vs already-decided envelope is handled there).
 *
 * Upload/capture: `PhotoCapture` (camera or file fallback) → the ONE shared
 * client pre-check `isAcceptedImage` (jpg/jpeg/png ≤50 MB, spec 09
 * 2026-11-10) → `api.upload` multipart field `file` (+ optional `frame_id`) → the
 * transient preview exists only while `uploading`; after the server 200 the
 * item appears ONLY via `photo.changed` → refetch (honest-file rule — no
 * optimistic gallery row, no success toast before 200). 11th photo →
 * PHOTO_LIMIT shown inline. Frames: program-linked frame list (SSE
 * `frame.changed`), optional overlay fused at capture.
 */
import { useCallback, useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, Select, Space, Typography } from 'antd'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
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
import { consentStatusLabel, pPrefix, type PhotoItem, type SessionMember } from '../../lib/session-contracts'
import { ConsentTag } from '../sessions/controls'
import { useSessionDetail } from '../sessions/shared'
import PhotoGrid from './PhotoGrid'

interface FrameRow {
  id: number
  name: string
  image_url: string
}

export default function GalleryPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const toast = useErrorToast()
  const { sid } = useParams()
  const navigate = useNavigate()
  const [searchParams] = useSearchParams()
  const { detail, error: detailError, reload: reloadDetail } = useSessionDetail(sid)

  // The breadcrumb bar shows the session's name instead of "Detail sesi".
  useCrumbName('sid', detail?.session.name)

  const [stid, setStid] = useState<number | null>(Number(searchParams.get('stid')) || null)
  const [gid, setGid] = useState<number | null>(Number(searchParams.get('gid')) || null)
  const [cid, setCid] = useState<number | null>(Number(searchParams.get('cid')) || null)
  const [photos, setPhotos] = useState<PhotoItem[] | null>(null)
  const [photoError, setPhotoError] = useState<unknown>(null)
  const [uploading, setUploading] = useState(false)
  const [uploadError, setUploadError] = useState<string | null>(null)
  const [frameId, setFrameId] = useState<number | null>(null)

  // Defaults: first topic, first group with members, first member of the group.
  useEffect(() => {
    if (!detail) return
    const validStid = detail.topics.some((topic) => topic.stid === stid) ? stid : detail.topics[0]?.stid ?? null
    const groupWithMembers = detail.groups.find(
      (group) => group.members !== undefined && (gid === null || group.gid === gid),
    )
    const activeGid = groupWithMembers?.gid ?? gid
    const activeGroup = detail.groups.find((group) => group.gid === activeGid)
    const members = activeGroup?.members ?? []
    const validCid = members.some((member) => member.child_id === cid) ? cid : members[0]?.child_id ?? null
    setStid(validStid)
    setGid(activeGid)
    setCid(validCid)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [detail])

  const prefix = sid !== undefined && stid !== null && cid !== null ? pPrefix(sid, stid, cid) : null

  const loadPhotos = useCallback(() => {
    if (!prefix) return
    api.get<PhotoItem[]>(`${prefix}/photos`).then(
      (data) => {
        setPhotos(data)
        setPhotoError(null)
      },
      (failure: unknown) => setPhotoError(failure),
    )
  }, [prefix])

  useEffect(() => {
    if (!prefix) return
    setPhotos(null)
    setPhotoError(null)
    loadPhotos()
  }, [prefix, loadPhotos])

  useEventRefetch(['photo.changed', 'copy.completed'], loadPhotos)
  useEventRefetch(['consent.changed'], reloadDetail)

  const programId = detail?.session.program_id ?? null
  // Frame overlay is decoration (capture works without it): errors are
  // simply never surfaced; the list refetches on `frame.changed`.
  const { rows: frames } = useListLoader<FrameRow>(
    programId === null ? null : '/api/frames',
    { program_id: programId ?? undefined, page: 1, per_page: 1000 },
    ['frame.changed'],
  )
  useEffect(() => {
    if (frameId === null && frames.length > 0) setFrameId(frames[0].id)
  }, [frames, frameId])

  const group = detail?.groups.find((entry) => entry.gid === gid) ?? null
  const members: SessionMember[] = group?.members ?? []
  const member = members.find((entry) => entry.child_id === cid) ?? null
  // No consent row yet is NOT the same as a pending request: keep null so the
  // tag and lock message say "Not yet submitted" instead of "Pending".
  const consent = member?.consent ?? ''
  const unlocked = consent === 'SETUJU'

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
      const selectedFrame = frames.find((frame) => frame.id === frameId) ?? null
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
          action={<Link to="/sessions">{tc('actions.back')}</Link>}
        />
      </>
    )
  }

  if (!detail) {
    return <PageSkeleton />
  }

  const consentLabel = consentStatusLabel(t, consent)

  return (
    <>
      <PageHeader
        title={t('gallery.title')}
        description={`${detail.session.name} · ${detail.session.date}`}
      />

      <FilterBar>
        {detail.topics.length > 1 ? (
          <FilterField label={t('gallery.topic')} htmlFor="gallery-topic" width={240}>
            <Select
              id="gallery-topic"
              value={stid ?? undefined}
              onChange={setStid}
              options={detail.topics.map((topic) => ({ value: topic.stid, label: topic.name }))}
              aria-label={t('gallery.topic')}
              placeholder={t('gallery.pickTopic')}
            />
          </FilterField>
        ) : null}
        <FilterField label={t('gallery.group')} htmlFor="gallery-group" width={220}>
          <Select
            id="gallery-group"
            value={gid ?? undefined}
            onChange={(next) => {
              setGid(next)
              setCid(null)
            }}
            options={detail.groups
              .filter((entry) => entry.members !== undefined)
              .map((entry) => ({ value: entry.gid, label: entry.name }))}
            aria-label={t('gallery.group')}
            placeholder={t('gallery.pickGroup')}
          />
        </FilterField>
        <FilterField label={t('gallery.child')} htmlFor="gallery-child" width={260}>
          <Select
            id="gallery-child"
            value={cid ?? undefined}
            onChange={setCid}
            options={members.map((entry) => ({
              value: entry.child_id,
              label: `${entry.name} · ${entry.school}`,
            }))}
            aria-label={t('gallery.child')}
            placeholder={t('gallery.pickChild')}
          />
        </FilterField>
      </FilterBar>

      <Space style={{ marginBottom: 12 }} wrap>
        {member ? <ConsentTag status={consent} /> : null}
        <Button type="link" size="small" onClick={() => navigate(`/consent/${sid}`)}>
          {t('consent.title')}
        </Button>
      </Space>

      {!member || stid === null ? (
        <Alert type="info" showIcon message={t('gallery.pickPrompt')} />
      ) : !unlocked ? (
        <Alert
          type="warning"
          showIcon
          message={t('gallery.consentLocked')}
          description={t('gallery.consentLabel', { status: consentLabel })}
          action={
            <Button type="link" size="small" onClick={() => navigate(`/consent/${sid}`)}>
              {tc('actions.view')}
            </Button>
          }
        />
      ) : (
        <>
          <div style={{ maxWidth: 420, marginBottom: 16 }}>
            <PhotoCapture
              onCapture={(file) => void onCapture(file)}
              uploading={uploading}
              frameUrl={frames.find((frame) => frame.id === frameId)?.image_url ?? null}
            />
            <Space style={{ marginTop: 8 }} wrap>
              <Typography.Text type="secondary">{t('gallery.frame')}</Typography.Text>
              <Select
                size="small"
                style={{ width: 220 }}
                value={frameId ?? undefined}
                onChange={setFrameId}
                options={[
                  { value: -1, label: t('gallery.noFrame') },
                  ...frames.map((frame) => ({ value: frame.id, label: frame.name })),
                ]}
                placeholder={t('gallery.noFrame')}
              />
            </Space>
            {uploadError ? (
              <Alert
                style={{ marginTop: 8 }}
                type="error"
                showIcon
                closable
                message={uploadError}
                onClose={() => setUploadError(null)}
              />
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
        </>
      )}
    </>
  )
}
