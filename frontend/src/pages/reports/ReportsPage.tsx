/**
 * Focused single-child report page
 * `/sessions/:sid/reports/topics/:stid/children/:cid` (F-12 / FL-13).
 *
 * The reports flow is session → topic → participant table
 * (`ReportsParticipantsPage`) → this page. Submission lives on the
 * participant table (batch send); this page only MODIFIES one report:
 * readiness gate via `GET P/readiness` (missing list; compose = preview/
 * export/links hidden until complete), narrative + mission editors (ungated
 * inputs), ReportView preview from `GET P/report` with client-only PNG/PDF
 * export, and the public links manager.
 *
 * ReportView/export come from the parallel F5 module (`src/report/*`).
 * SSE: assessment/copy → readiness+report; narrative/mission-pick/photo →
 * report; report-link → links; readiness.changed renders the gate directly.
 */
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { Alert, Button, Card, Flex, List, Space, Spin, Typography } from 'antd'
import { ArrowLeftOutlined, DownloadOutlined } from '@ant-design/icons'
import { Link, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import StatusChip from '../../components/StatusChip'
import { api, errorCode } from '../../lib/api'
import { useCrumbName } from '../../lib/crumbs'
import { useErrorToast } from '../../lib/errors'
import { useEventRefetch } from '../../lib/list'
import { registerSignal } from '../../lib/stream'
import ReportView from '../../report/ReportView'
import { exportReport } from '../../report/export'
import type { ReportData } from '../../report/types'
import { pPrefix, type PublicLink, type Readiness } from '../../lib/session-contracts'
import { useSessionDetail } from '../sessions/shared'
import LinksPanel from './LinksPanel'
import MissionPanel from './MissionPanel'
import NarrativePanel from './NarrativePanel'

/** CSS 1mm = 96/25.4px — matches the fixed 210×297mm page box exactly. */
const MM = 96 / 25.4
const PAGE_W = 210 * MM
const PAGE_H = 297 * MM

export default function ReportsPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const toast = useErrorToast()
  const navigate = useNavigate()
  const { sid, stid: stidParam, cid: cidParam } = useParams()
  const { detail, error: detailError, deleted } = useSessionDetail(sid)

  // The breadcrumb bar shows the session's name instead of "Detail sesi".
  useCrumbName('sid', detail?.session.name)

  const stid = Number(stidParam)
  const cid = Number(cidParam)
  const validScope =
    Number.isInteger(stid) && stid > 0 && Number.isInteger(cid) && cid > 0

  const [ready, setReady] = useState<Readiness | null>(null)
  const [readyError, setReadyError] = useState<unknown>(null)
  const [report, setReport] = useState<ReportData | null>(null)
  const [reportError, setReportError] = useState<unknown>(null)
  const [links, setLinks] = useState<PublicLink[]>([])
  const [linksError, setLinksError] = useState<unknown>(null)

  // Wait for the detail to confirm the topic before fetching the scoped P
  // prefix — an unknown /topics/:stid URL must not issue stray requests.
  const topicKnown = detail?.topics.some((entry) => entry.stid === stid) ?? false
  const prefix =
    sid !== undefined && validScope && topicKnown ? pPrefix(sid, stid, cid) : null

  const loadReady = useCallback(() => {
    if (!prefix) return
    api.get<Readiness>(`${prefix}/readiness`).then(
      (data) => {
        setReady(data)
        setReadyError(null)
      },
      (failure: unknown) => setReadyError(failure),
    )
  }, [prefix])

  const loadReport = useCallback(() => {
    if (!prefix) return
    api.get<ReportData>(`${prefix}/report`).then(
      (data) => {
        setReport(data)
        setReportError(null)
      },
      (failure: unknown) => setReportError(failure),
    )
  }, [prefix])

  const loadLinks = useCallback(() => {
    if (!prefix) return
    api.get<PublicLink[]>(`${prefix}/public-links`).then(
      (data) => {
        setLinks(data)
        setLinksError(null)
      },
      (failure: unknown) => setLinksError(failure),
    )
  }, [prefix])

  useEffect(() => {
    if (!prefix) return
    setReady(null)
    setReport(null)
    loadReady()
    loadReport()
    loadLinks()
  }, [prefix, loadReady, loadReport, loadLinks])

  useEventRefetch(['assessment.changed', 'copy.completed'], () => {
    loadReady()
    loadReport()
  })
  useEventRefetch(['narrative.changed', 'mission-pick.changed', 'photo.changed'], loadReport)
  useEventRefetch(['report-link.changed'], loadLinks)

  useEffect(() => {
    const off = registerSignal<{ child_id: number; stid: number; complete: boolean }>(
      'readiness.changed',
      (data) => {
        if (data.child_id === cid && data.stid === stid) {
          setReady((current) => (current ? { ...current, complete: data.complete } : current))
        }
      },
    )
    return off
  }, [cid, stid])

  useEffect(() => {
    if (deleted) navigate('/reports', { replace: true })
  }, [deleted, navigate])

  // Screen preview: scale the fixed A4 node to the available width (aspect kept).
  const boxRef = useRef<HTMLDivElement>(null)
  const [scale, setScale] = useState(1)
  useLayoutEffect(() => {
    const box = boxRef.current
    if (!box) return
    const update = () => setScale(Math.min(1, box.clientWidth / PAGE_W))
    update()
    const observer = new ResizeObserver(update)
    observer.observe(box)
    return () => observer.disconnect()
  }, [report])

  if (detailError) {
    return (
      <>
        <PageHeader title={t('reports.title')} />
        <Alert
          type="error"
          showIcon
          message={tc(`errors.${errorCode(detailError)}`)}
          action={<Link to="/reports">{tc('actions.back')}</Link>}
        />
      </>
    )
  }

  if (!detail) {
    return <PageSkeleton />
  }

  const topic = detail.topics.find((entry) => entry.stid === stid) ?? null
  const members = detail.groups.flatMap((group) => group.members ?? [])
  const childName = members.find((member) => member.child_id === cid)?.name ?? ''
  const errorText = (value: unknown) => tc(`errors.${errorCode(value)}`)

  const backToTopics = () => navigate(`/sessions/${sid}/reports/topics`)
  const backToParticipants = () => navigate(`/sessions/${sid}/reports/topics/${stid}`)

  if (!topic || !prefix) {
    return (
      <>
        <PageHeader title={t('reports.title')} />
        <Alert
          type="error"
          showIcon
          message={tc('errors.NOT_FOUND')}
          action={<Button onClick={backToTopics}>{t('reports.backToTopics')}</Button>}
        />
      </>
    )
  }

  const runExport = (kind: 'png' | 'pdf') => {
    exportReport(kind, {
      childName,
      topicName: topic.name,
      sessionDate: detail.session.date,
    }).catch(toast)
  }

  return (
    <>
      <PageHeader
        title={childName || t('reports.title')}
        description={
          <Space wrap>
            <span>{detail.session.name}</span>
            <span aria-hidden="true">·</span>
            <span>{topic.name}</span>
            <StatusChip status={detail.session.status} />
            <span>{detail.session.date}</span>
          </Space>
        }
        actions={
          <Button icon={<ArrowLeftOutlined />} onClick={backToParticipants}>
            {t('reports.backToParticipants')}
          </Button>
        }
      />

      {ready === null && readyError === null ? <PageSkeleton variant="list" rows={3} /> : null}
      {readyError ? (
        <Alert
          style={{ marginBottom: 16 }}
          type="error"
          showIcon
          message={errorText(readyError)}
          action={<Button onClick={loadReady}>{tc('actions.retry')}</Button>}
        />
      ) : null}
      {ready && !ready.complete ? (
        <Alert
          style={{ marginBottom: 16 }}
          type="warning"
          showIcon
          message={t('readiness.incomplete', { count: ready.missing.length })}
          description={
            <div>
              <Typography.Text type="secondary" style={{ display: 'block' }}>
                {t('reports.missing')}
              </Typography.Text>
              <List
                size="small"
                dataSource={ready.missing}
                split={false}
                renderItem={(item) => (
                  <List.Item style={{ padding: '2px 0' }}>
                    <Typography.Text style={{ fontSize: 12 }}>{item.name}</Typography.Text>
                  </List.Item>
                )}
              />
              <Link to={`/assessment/${sid}/${cid}/${stid}`}>{t('readiness.open')}</Link>
            </div>
          }
        />
      ) : null}

      <NarrativePanel prefix={prefix} />
      <MissionPanel prefix={prefix} masterTopicId={topic.topic_id} />

      {ready?.complete ? (
        <>
          <Card style={{ marginBottom: 16 }}>
            <Flex
              justify="space-between"
              align="flex-start"
              gap="middle"
              wrap
              style={{ marginBottom: 8 }}
            >
              <Typography.Title level={2} style={{ margin: 0 }}>
                {t('reports.preview')}
              </Typography.Title>
              <Space wrap>
                <Button
                  size="small"
                  icon={<DownloadOutlined />}
                  disabled={report === null}
                  onClick={() => runExport('png')}
                >
                  {t('reports.exportPng')}
                </Button>
                <Button
                  size="small"
                  icon={<DownloadOutlined />}
                  disabled={report === null}
                  onClick={() => runExport('pdf')}
                >
                  {t('reports.exportPdf')}
                </Button>
              </Space>
            </Flex>
            {reportError ? (
              <Alert
                type="error"
                showIcon
                message={errorText(reportError)}
                action={<Button onClick={loadReport}>{tc('actions.retry')}</Button>}
              />
            ) : report ? (
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
                  <div
                    style={{
                      width: PAGE_W,
                      transform: `scale(${scale})`,
                      transformOrigin: 'top left',
                    }}
                  >
                    <ReportView data={report} />
                  </div>
                </div>
              </div>
            ) : (
              <Spin size="small" />
            )}
          </Card>

          {linksError ? (
            <Alert
              style={{ marginBottom: 16 }}
              type="error"
              showIcon
              message={errorText(linksError)}
              action={<Button onClick={loadLinks}>{tc('actions.retry')}</Button>}
            />
          ) : null}
          <LinksPanel prefix={prefix} links={links} onChanged={loadLinks} />
        </>
      ) : null}
    </>
  )
}
