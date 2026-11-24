/**
 * `/sessions/:sid/reports/topics` (REQ9): topic picker — the reports flow is
 * session → topics → participants (`ReportsParticipantsPage`) → the focused
 * report page. The source is `useSessionDetail(sid)` → `detail.topics`; each
 * topic row opens its participant table.
 *
 * House list anatomy: the header carries the session context as plain text, a
 * manual refresh and an explicit back action; the table is the shared
 * DataTable (sticky header, house empty state) and the row action is the
 * shared icon-only RowActions cluster.
 */
import { useEffect } from 'react'
import { Alert, Button, Space } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { ArrowLeftOutlined, RightOutlined } from '@ant-design/icons'
import { useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import RefreshButton from '../../components/RefreshButton'
import { RowAction, RowActions } from '../../components/RowActions'
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
  const { detail, error, deleted, reload } = useSessionDetail(sid)

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
          action={<Button onClick={() => navigate('/reports')}>{tc('actions.back')}</Button>}
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
        <RowActions>
          <RowAction
            label={t('reports.openTopic')}
            icon={<RightOutlined />}
            onClick={() => navigate(`/sessions/${sid}/reports/topics/${topic.stid}`)}
          />
        </RowActions>
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
          <Space>
            <RefreshButton onRefresh={reload} />
            <Button icon={<ArrowLeftOutlined />} onClick={() => navigate('/reports')}>
              {tc('actions.back')}
            </Button>
          </Space>
        }
      />
      <DataTable<SessionTopic>
        rowKey="stid"
        columns={columns}
        dataSource={detail.topics}
        pagination={false}
        empty={
          <EmptyState
            title={t('reports.noTopicTitle')}
            description={t('reports.noTopicDesc')}
          />
        }
      />
    </>
  )
}
