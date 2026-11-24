/**
 * `/sessions/:sid/reports/topics` (REQ9): topic picker — the reports flow is
 * session → topics → participants (`ReportsParticipantsPage`) → the focused
 * report page. The source is `useSessionDetail(sid)` → `detail.topics`; each
 * topic row opens its participant table.
 *
 * The header carries the session context as plain text plus an explicit back
 * action to the session picker (no bare title link).
 */
import { useEffect } from 'react'
import { Alert, Button, Space, Table } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { ArrowLeftOutlined } from '@ant-design/icons'
import { Link, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import EmptyState from '../../components/EmptyState'
import StatusChip from '../../components/StatusChip'
import { errorCode } from '../../lib/api'
import { useCrumbName } from '../../lib/crumbs'
import { useSessionDetail } from '../sessions/shared'
import type { SessionTopic } from '../../lib/session-contracts'

export default function ReportsTopicsPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { sid } = useParams()
  const navigate = useNavigate()
  const { detail, error, deleted } = useSessionDetail(sid)

  useCrumbName('sid', detail?.session.name)

  useEffect(() => {
    if (deleted) navigate('/reports', { replace: true })
  }, [deleted, navigate])

  if (error) {
    return (
      <>
        <PageHeader title={t('reports.topicsTitle')} />
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
        description={
          <Space wrap>
            <span>{detail.session.name}</span>
            <StatusChip status={detail.session.status} />
            <span>{detail.session.date}</span>
          </Space>
        }
        actions={
          <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/reports')}>
            {tc('actions.back')}
          </Button>
        }
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
