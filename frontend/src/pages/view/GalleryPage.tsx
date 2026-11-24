/**
 * Public gallery view (spec F-10, F-16, FL-15): `/view/gallery?token=` — no
 * auth. Fetches `GET /public/gallery?token=`; `locked`/`reason` render the
 * inactive / consent-pending / consent-denied notices with NO photos; unlocked
 * shows the responsive `/uploads/…` grid (antd Image placeholder-mask while
 * loading, error → cache-busted retry, `missing:true` → placeholder notice,
 * candidate badge. Page-level public stream refetches on `gallery.changed`;
 * `link.revoked` is terminal and closes the stream. No polling.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Alert, Button, Card, Col, Empty, Flex, Image, Result, Row, Spin, Tag, Typography } from 'antd'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import { api, ApiError } from '../../lib/api'
import { usePublicStream } from '../../lib/list'

interface GalleryPhoto {
  id: number
  url: string
  topic: { stid: number; name: string }
  created_at: string
  is_candidate: boolean
  missing?: boolean
}

interface GalleryData {
  child: { id: number; name: string; age: number; school: string }
  session: { id: number; name: string; date: string }
  photos: GalleryPhoto[]
  locked: boolean
  reason: string | null
}

function PhotoCard({ photo }: { photo: GalleryPhoto }) {
  const { t } = useTranslation()
  // The loading phase is handled by antd Image's own placeholder mask; only
  // the load-ERROR state stays page-owned (the retry bumps a cache-buster).
  const [errored, setErrored] = useState(false)
  const [nonce, setNonce] = useState(0)

  if (photo.missing) {
    return (
      <Card style={{ minHeight: 140, display: 'flex', alignItems: 'center', textAlign: 'center' }}>
        <Typography.Text type="secondary">{t('errors.PHOTO_MISSING')}</Typography.Text>
      </Card>
    )
  }

  const src = nonce === 0 ? photo.url : `${photo.url}?r=${nonce}`
  return (
    <Card style={{ padding: 8 }}>
      {errored ? (
        <Flex
          vertical
          align="center"
          gap={8}
          style={{ minHeight: 140, padding: '24px 8px', textAlign: 'center' }}
        >
          <Typography.Text type="secondary">{t('errors.INTERNAL')}</Typography.Text>
          <Button size="small" onClick={() => { setErrored(false); setNonce((n) => n + 1) }}>
            {t('actions.retry')}
          </Button>
        </Flex>
      ) : (
        <Image
          src={src}
          alt=""
          preview={false}
          placeholder={<Spin />}
          onError={() => setErrored(true)}
          style={{ display: 'block', width: '100%', minHeight: 140 }}
        />
      )}
      <div style={{ display: 'flex', justifyContent: 'space-between', alignItems: 'center', gap: 8, marginTop: 6 }}>
        <Typography.Text type="secondary">{photo.topic.name}</Typography.Text>
        {photo.is_candidate && (
          <Tag color="purple" style={{ borderRadius: 999, fontSize: 9, fontWeight: 600 }}>
            {t('report:public.candidatePhoto')}
          </Tag>
        )}
      </div>
    </Card>
  )
}

export default function GalleryPage() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const token = params.get('token')

  const [data, setData] = useState<GalleryData | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [invalid, setInvalid] = useState(false)
  const revokedRef = useRef(false)

  const load = useCallback(async () => {
    try {
      const gallery = await api.get<GalleryData>('/public/gallery', { token })
      if (revokedRef.current) return
      setData(gallery)
      setError(null)
    } catch (e) {
      if (revokedRef.current) return
      const err = e instanceof ApiError ? e : new ApiError('INTERNAL', String(e))
      if (err.code === 'TOKEN_INVALID') {
        revokedRef.current = true
        setInvalid(true)
      } else {
        setError(err)
      }
    }
  }, [token])

  useEffect(() => {
    if (token) void load()
  }, [token, load])

  usePublicStream(
    invalid ? null : token,
    { 'gallery.changed': () => void load() },
    () => {
      revokedRef.current = true
      setInvalid(true) // flips the token arg → the hook closes the stream
    },
  )

  if (!token || invalid) {
    return <Result status="404" title={t('report:public.invalidLink')} />
  }

  if (!data) {
    if (error) {
      return (
        <Result
          status="error"
          title={t(`errors.${error.code}`, { defaultValue: error.message })}
          extra={
            <Button onClick={() => { setError(null); void load() }}>{t('actions.retry')}</Button>
          }
        />
      )
    }
    return <PageSkeleton variant="grid" />
  }

  const lockNotice =
    data.reason === 'CHILD_INACTIVE'
      ? t('report:public.childInactive')
      : data.reason === 'CONSENT_DENIED'
        ? t('report:public.galleryDenied')
        : t('report:public.galleryLocked')

  return (
    <div className="kv-content" style={{ maxWidth: 960, margin: '0 auto' }}>
      <PageHeader
        title={t('report:public.galleryTitle', { child: data.child.name })}
        description={`${data.session.name} · ${data.session.date}`}
      />
      {data.locked ? (
        <Alert type="warning" showIcon message={lockNotice} />
      ) : data.photos.length === 0 ? (
        <Empty
          image={Empty.PRESENTED_IMAGE_SIMPLE}
          description={t('report:public.galleryEmpty')}
          style={{ marginTop: 24 }}
        />
      ) : (
        <Row gutter={[16, 16]}>
          {data.photos.map((photo) => (
            <Col key={photo.id} xs={12} sm={8} md={6}>
              <PhotoCard photo={photo} />
            </Col>
          ))}
        </Row>
      )}
    </div>
  )
}
