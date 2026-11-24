/**
 * `/sessions/:sid/reports/topics/:stid` (REQ9): the participant table for one
 * topic. This is where reports are submitted: the matrix carries the
 * per-child readiness/links columns, the group filter, the row checkboxes and
 * the batch send action. Each row's action opens the focused report page
 * (`/sessions/:sid/reports/topics/:stid/children/:cid`) for viewing/editing.
 *
 * The matrix itself is `BatchSendPanel` scoped with `stid`; the header gives
 * the session/topic context and an explicit back action (no bare title link).
 */
import { useEffect } from 'react'
import { Alert, Button, Space } from 'antd'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { Link, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import StatusChip from '../../components/StatusChip'
import { errorCode } from '../../lib/api'
import { useCrumbName } from '../../lib/crumbs'
import { useSessionDetail } from '../sessions/shared'
import BatchSendPanel from './BatchSendPanel'

export default function ReportsParticipantsPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { sid, stid: stidParam } = useParams()
  const navigate = useNavigate()
  const { detail, error, deleted } = useSessionDetail(sid)

  useCrumbName('sid', detail?.session.name)

  useEffect(() => {
    if (deleted) navigate('/reports', { replace: true })
  }, [deleted, navigate])

  if (error) {
    return (
      <>
        <PageHeader title={t('reports.participantsTitle')} />
        <Alert
          type="error"
          showIcon
          message={tc(`errors.${errorCode(error)}`)}
          action={<Link to="/reports">{tc('actions.back')}</Link>}
        />
      </>
    )
  }

  if (!detail) return <PageSkeleton />

  const stid = Number(stidParam)
  const topic = detail.topics.find((entry) => entry.stid === stid) ?? null
  const back = () => navigate(`/sessions/${sid}/reports/topics`)

  if (!topic) {
    return (
      <>
        <PageHeader title={t('reports.participantsTitle')} />
        <Alert
          type="error"
          showIcon
          message={tc('errors.NOT_FOUND')}
          action={<Button onClick={back}>{t('reports.backToTopics')}</Button>}
        />
      </>
    )
  }

  return (
    <>
      <PageHeader
        title={t('reports.participantsTitle')}
        description={
          <Space wrap>
            <span>{detail.session.name}</span>
            <span aria-hidden="true">·</span>
            <span>{topic.name}</span>
            <StatusChip status={detail.session.status} />
          </Space>
        }
        actions={
          <Button icon={<ArrowLeftOutlined />} onClick={back}>
            {t('reports.backToTopics')}
          </Button>
        }
      />
      <BatchSendPanel sid={String(sid)} stid={stid} />
    </>
  )
}
