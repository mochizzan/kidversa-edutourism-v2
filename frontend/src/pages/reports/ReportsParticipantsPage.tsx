/**
 * `/sessions/:sid/reports/topics/:stid` (REQ9): the participant/children
 * table for one topic — each row links to the reports edit page
 * (`ReportsPage`); the header links back to the topic picker.
 *
 * Members come from `useSessionDetail(sid)` → `detail.groups.flatMap(g =>
 * g.members ?? [])` — the wire `SessionMember` type carries no per-member
 * topic id, so the list is the session's reachable members (all groups).
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
import type { SessionMember } from '../../lib/session-contracts'

export default function ReportsParticipantsPage() {
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

  const members = detail.groups.flatMap((group) => group.members ?? [])

  const columns: ColumnsType<SessionMember> = [
    { title: t('reports.participantName'), dataIndex: 'name' },
    { title: t('reports.participantSchool'), dataIndex: 'school' },
    {
      title: tc('columns.actions'),
      key: 'actions',
      width: 92,
      fixed: 'right',
      render: () => (
        <Button type="link" onClick={() => navigate(`/sessions/${sid}/reports`)}>
          {t('reports.openReport')}
        </Button>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title={t('reports.participantsTitle')}
        description={
          <Link to={`/sessions/${sid}/reports/topics`}>{t('reports.backToTopics')}</Link>
        }
      />
      {members.length === 0 ? (
        <EmptyState variant="card" title={t('reports.participantsEmpty')} />
      ) : (
        <Table<SessionMember>
          rowKey="child_id"
          size="small"
          columns={columns}
          dataSource={members}
          locale={{ emptyText: tc('empty') }}
        />
      )}
    </>
  )
}
