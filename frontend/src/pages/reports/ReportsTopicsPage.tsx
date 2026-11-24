/**
 * `/sessions/:sid/reports/topics` (REQ9): topic picker — the reports flow is
 * session → topics → participants (`ReportsParticipantsPage`) → the reports
 * matrix (`ReportsPage`). The source is `useSessionDetail(sid)` →
 * `detail.topics`; each topic row opens its participant page.
 */
import { useEffect } from 'react'
import { Alert, Button, Table } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { Link, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import EmptyState from '../../components/EmptyState'
import { errorCode } from '../../lib/api'
import { useSessionDetail } from '../sessions/shared'
import type { SessionTopic } from '../../lib/session-contracts'

export default function ReportsTopicsPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { sid } = useParams()
  const navigate = useNavigate()
  const { detail, error, deleted } = useSessionDetail(sid)

  useEffect(() => {
    if (deleted) navigate('/reports', { replace: true })
  }, [deleted, navigate])

  if (error) {
    return (
      <>
        <PageHeader title={t('reports.title')} />
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

  const columns: ColumnsType<SessionTopic> = [
    { title: t('reports.topic'), dataIndex: 'name' },
    {
      title: tc('columns.actions'),
      key: 'actions',
      width: 92,
      fixed: 'right',
      render: (_, topic) => (
        <Button
          type="link"
          onClick={() => navigate(`/sessions/${sid}/reports/topics/${topic.stid}`)}
        >
          {t('reports.openTopic')}
        </Button>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title={t('reports.topicsTitle')}
        description={<Link to={`/sessions/${sid}`}>{detail.session.name}</Link>}
      />
      {detail.topics.length === 0 ? (
        <EmptyState variant="card" title={t('reports.noTopicTitle')} />
      ) : (
        <Table<SessionTopic>
          rowKey="stid"
          size="small"
          columns={columns}
          dataSource={detail.topics}
          locale={{ emptyText: tc('empty') }}
        />
      )}
    </>
  )
}
