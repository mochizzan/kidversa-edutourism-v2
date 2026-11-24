/**
 * Photo grid (F-10 / FL-10, honest-file rule): items render ONLY from server
 * `/uploads/…` bytes fetched via `GET P/photos` — never optimistic, never
 * pushed. `missing:true` rows show a placeholder + notice (E29 item flag).
 * Candidate (PATCH clear+set, allowed even Finished) and delete (confirm) act
 * through the parent, which refetches on the server's `photo.changed`.
 */
import { useState } from 'react'
import { Alert, Button, Card, Empty, Image, Space } from 'antd'
import { PictureOutlined, StarFilled, StarOutlined, DeleteOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import ConfirmDialog from '../../components/ConfirmDialog'
import PageSkeleton from '../../components/PageSkeleton'
import { errorCode } from '../../lib/api'
import type { PhotoItem } from '../../lib/session-contracts'

export interface PhotoGridProps {
  photos: PhotoItem[] | null
  loading: boolean
  error: unknown
  onRetry: () => void
  onCandidate: (photoId: number) => void
  onDelete: (photoId: number) => void
}

export default function PhotoGrid({
  photos,
  loading,
  error,
  onRetry,
  onCandidate,
  onDelete,
}: PhotoGridProps) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const [deleteTarget, setDeleteTarget] = useState<PhotoItem | null>(null)

  if (loading && photos === null) {
    return <PageSkeleton variant="grid" />
  }

  if (error) {
    return (
      <Alert
        type="error"
        showIcon
        message={tc(`errors.${errorCode(error)}`)}
        action={<Button onClick={onRetry}>{tc('actions.retry')}</Button>}
      />
    )
  }

  if (!photos || photos.length === 0) {
    return <Empty description={t('gallery.empty')} />
  }

  return (
    <>
      {/* The fluid auto-fill CSS grid stays by design: antd Row/Col's fixed
          24-column spans cannot reproduce minmax(140px, 1fr) tiling with 9:16
          aspect tiles. The grid hosts Image.PreviewGroup so the lightbox
          navigates across all photos. */}
      <div style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fill, minmax(140px, 1fr))', gap: 12 }}>
        <Image.PreviewGroup>
          {photos.map((photo) => (
            <Card
              key={photo.id}
              style={{ margin: 0, position: 'relative', aspectRatio: '9 / 16' }}
              styles={{ body: { padding: 8, height: '100%', boxSizing: 'border-box' } }}
            >
              {photo.missing ? (
                <Empty
                  style={{
                    height: '100%',
                    margin: 0,
                    display: 'flex',
                    flexDirection: 'column',
                    justifyContent: 'center',
                  }}
                  image={<PictureOutlined style={{ fontSize: 28, color: '#777873' }} />}
                  styles={{ image: { height: 'auto' } }}
                  description={t('gallery.missing')}
                />
              ) : (
                <Image
                  src={photo.url}
                  alt={photo.url}
                  loading="lazy"
                  wrapperStyle={{ width: '100%', height: '100%' }}
                  style={{ width: '100%', height: '100%', objectFit: 'cover', borderRadius: 6 }}
                />
              )}
              <Space
                size={4}
                style={{
                  position: 'absolute',
                  top: 8,
                  right: 8,
                  background: 'rgba(255,255,255,.9)',
                  borderRadius: 6,
                  padding: 2,
                }}
              >
                <Button
                  size="small"
                  type={photo.is_candidate ? 'primary' : 'text'}
                  aria-label={t('gallery.candidate')}
                  title={t('gallery.candidate')}
                  icon={photo.is_candidate ? <StarFilled /> : <StarOutlined />}
                  onClick={() => onCandidate(photo.id)}
                />
                <Button
                  size="small"
                  danger
                  type="text"
                  aria-label={t('gallery.delete')}
                  title={t('gallery.delete')}
                  icon={<DeleteOutlined />}
                  onClick={() => setDeleteTarget(photo)}
                />
              </Space>
            </Card>
          ))}
        </Image.PreviewGroup>
      </div>

      <ConfirmDialog
        open={deleteTarget !== null}
        title={t('gallery.deleteTitle')}
        danger
        confirmLabel={tc('actions.delete')}
        cancelLabel={tc('actions.cancel')}
        onCancel={() => setDeleteTarget(null)}
        onConfirm={() => {
          if (deleteTarget) onDelete(deleteTarget.id)
          setDeleteTarget(null)
        }}
      />
    </>
  )
}
