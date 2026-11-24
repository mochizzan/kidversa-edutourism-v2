/**
 * Public report view (spec F-12, FL-15): `/view/report?token=` — no auth.
 * Fetches `GET /public/report?token=`, renders the A4 `ReportView` as a
 * scaled preview, and offers client-only PNG/PDF export. A page-level public
 * SSE stream refetches on `report.changed`; `link.revoked` (or any
 * `TOKEN_INVALID`) is terminal: the identical "link invalid" view renders and
 * the stream closes (cleanup on the `invalid` state flip). No polling.
 */
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Alert, Button, Result } from 'antd'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import { api, ApiError } from '../../lib/api'
import { usePublicStream } from '../../lib/list'
import { exportReport } from '../../report/export'
import ReportView from '../../report/ReportView'
import type { ReportData } from '../../report/types'

/** CSS 1mm = 96/25.4px — matches the fixed 210×297mm page box exactly. */
const MM = 96 / 25.4
const PAGE_W = 210 * MM
const PAGE_H = 297 * MM

export default function ReportPage() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const token = params.get('token')

  const [data, setData] = useState<ReportData | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [invalid, setInvalid] = useState(false)
  const [exporting, setExporting] = useState<'png' | 'pdf' | null>(null)
  const [exportError, setExportError] = useState<string | null>(null)
  const revokedRef = useRef(false)
  const boxRef = useRef<HTMLDivElement>(null)
  const [scale, setScale] = useState(1)

  const load = useCallback(async () => {
    try {
      const report = await api.get<ReportData>('/public/report', { token })
      if (revokedRef.current) return
      setData(report)
      setError(null)
    } catch (e) {
      if (revokedRef.current) return
      const err = e instanceof ApiError ? e : new ApiError('INTERNAL', String(e))
      if (err.code === 'TOKEN_INVALID') {
        revokedRef.current = true
        setInvalid(true)
      } else {
        setError(err) // keep stale data (if any) — a later signal retries
      }
    }
  }, [token])

  useEffect(() => {
    if (token) void load()
  }, [token, load])

  usePublicStream(
    invalid ? null : token,
    { 'report.changed': () => void load() },
    () => {
      revokedRef.current = true
      setInvalid(true) // flips the token arg → the hook closes the stream
    },
  )

  // Screen preview: scale the fixed A4 node to the available width (aspect kept).
  useLayoutEffect(() => {
    const box = boxRef.current
    if (!box) return
    const update = () => setScale(Math.min(1, box.clientWidth / PAGE_W))
    update()
    const observer = new ResizeObserver(update)
    observer.observe(box)
    return () => observer.disconnect()
  }, [data])

  const download = async (kind: 'png' | 'pdf') => {
    if (!data) return
    setExporting(kind)
    setExportError(null)
    try {
      await exportReport(kind, {
        childName: data.child.name,
        topicName: data.topic.name,
        sessionDate: data.session.date,
      })
    } catch (e) {
      setExportError(
        e instanceof ApiError ? t(`errors.${e.code}`, { defaultValue: e.message }) : t('errors.INTERNAL'),
      )
    } finally {
      setExporting(null)
    }
  }

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
    return <PageSkeleton variant="document" />
  }

  return (
    <div className="kv-content" style={{ maxWidth: 960, margin: '0 auto' }}>
      <PageHeader
        title={t('report:public.reportTitle', { child: data.child.name })}
        description={`${data.session.name} · ${data.session.date}`}
        actions={
          <>
            <Button disabled={exporting !== null} loading={exporting === 'png'} onClick={() => void download('png')}>
              {t('report:export.png')}
            </Button>
            <Button disabled={exporting !== null} loading={exporting === 'pdf'} onClick={() => void download('pdf')}>
              {t('report:export.pdf')}
            </Button>
          </>
        }
      />
      {exportError && (
        <Alert type="error" showIcon message={exportError} closable style={{ marginBottom: 16 }} />
      )}
      <div ref={boxRef}>
        <div
          style={{
            width: PAGE_W * scale,
            height: PAGE_H * scale,
            margin: '0 auto',
            overflow: 'hidden',
            border: '1px solid var(--kv-border)',
          }}
        >
          <div style={{ width: PAGE_W, transform: `scale(${scale})`, transformOrigin: 'top left' }}>
            <ReportView data={data} />
          </div>
        </div>
      </div>
    </div>
  )
}
