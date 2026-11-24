/**
 * Paged session picker table shared by the `/reports` and `/consent` index
 * routes (menu matrix targets both): server-paged rows, one action per row →
 * the per-session page. Same SSE refetch rules and the same house table,
 * header and refresh affordance as every other list (DataTable, not a raw
 * antd Table), so the index pages no longer look like a third kind of list.
 */
import { useState } from 'react'
import { Alert, Button } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { FileSearchOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import PageHeader from '../../components/PageHeader'
import Pager from '../../components/Pager'
import RefreshButton from '../../components/RefreshButton'
import { RowAction, RowActions } from '../../components/RowActions'
import StatusChip from '../../components/StatusChip'
import { errorCode } from '../../lib/api'
import { useListLoader } from '../../lib/list'
import type { SessionListItem } from '../../lib/session-contracts'

export interface SessionPickTableProps {
  title: string
  description?: string
  /** Builds the destination the row action opens. */
  to: (sessionId: number) => string
}

export default function SessionPickTable({ title, description, to }: SessionPickTableProps) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const navigate = useNavigate()
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const { rows, meta, error, loading, reload } = useListLoader<SessionListItem>(
    '/api/sessions',
    { page, per_page: perPage },
    ['session.changed', 'session.deleted'],
  )

  const columns: ColumnsType<SessionListItem> = [
    {
      title: t('list.name'),
      dataIndex: 'name',
    },
    { title: t('list.program'), dataIndex: 'program_name' },
    { title: t('list.date'), dataIndex: 'date', width: 110 },
    { title: t('list.location'), dataIndex: 'location' },
    {
      title: t('list.status'),
      dataIndex: 'status',
      width: 110,
      render: (value: SessionListItem['status']) => <StatusChip status={value} />,
    },
    {
      title: tc('columns.actions'),
      key: 'actions',
      width: 92,
      fixed: 'right',
      render: (_, row) => (
        <RowActions>
          <RowAction
            label={tc('actions.view')}
            icon={<FileSearchOutlined />}
            onClick={() => navigate(to(row.id))}
          />
        </RowActions>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title={title}
        description={description}
        actions={<RefreshButton onRefresh={reload} loading={loading} />}
      />
      {error ? (
        <Alert
          className="kv-list-error"
          type="error"
          showIcon
          message={tc(`errors.${errorCode(error)}`)}
          action={<Button onClick={reload}>{tc('actions.retry')}</Button>}
        />
      ) : (
        <>
          <DataTable<SessionListItem>
            rowKey="id"
            loading={loading}
            columns={columns}
            dataSource={rows}
            pagination={false}
            scroll={{ x: 920 }}
            empty={<EmptyState title={t('list.empty')} description={tc('emptyState.hint')} />}
          />
          {meta ? (
            <Pager
              page={meta.page}
              perPage={meta.per_page}
              total={meta.total}
              onPage={setPage}
              onPerPage={(size) => {
                setPerPage(size)
                setPage(1)
              }}
            />
          ) : null}
        </>
      )}
    </>
  )
}
