/**
 * Session list (F-08): server-paged, server-side status/q filters, SSE
 * same-params refetch (`session.changed`), `session.deleted` drops the row
 * (soft same-params refetch). No client slicing, no polling.
 */
import { useState } from 'react'
import { Alert, Button, Input, Select, Space } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { EyeOutlined, PlusOutlined } from '@ant-design/icons'
import { Link, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import FilterBar, { FilterField } from '../../components/FilterBar'
import PageHeader from '../../components/PageHeader'
import Pager from '../../components/Pager'
import RefreshButton from '../../components/RefreshButton'
import { RowAction, RowActions } from '../../components/RowActions'
import StatusChip from '../../components/StatusChip'
import { errorCode } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { useListLoader } from '../../lib/list'
import type { SessionListItem } from '../../lib/session-contracts'

export default function SessionsPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { user } = useAuth()
  const navigate = useNavigate()
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const [status, setStatus] = useState('')
  const [q, setQ] = useState('')

  const { rows, meta, error, loading, reload } = useListLoader<SessionListItem>(
    '/api/sessions',
    { page, per_page: perPage, status: status || undefined, q: q || undefined },
    ['session.changed', 'session.deleted'],
  )

  const canManage = user?.role === 'SA' || user?.role === 'ADMIN'

  const columns: ColumnsType<SessionListItem> = [
    {
      title: t('list.name'),
      dataIndex: 'name',
      width: 220,
      ellipsis: true,
      render: (name: string, row) => <Link to={`/sessions/${row.id}`}>{name}</Link>,
    },
    { title: t('list.program'), dataIndex: 'program_name', width: 180, ellipsis: true },
    { title: t('list.date'), dataIndex: 'date', width: 110 },
    { title: t('list.location'), dataIndex: 'location', width: 180, ellipsis: true },
    {
      title: t('list.status'),
      dataIndex: 'status',
      width: 110,
      render: (value: SessionListItem['status']) => <StatusChip status={value} />,
    },
    {
      title: t('list.groups'),
      dataIndex: 'group_count',
      width: 90,
      align: 'right',
      className: 'kv-num',
    },
    {
      title: t('list.members'),
      dataIndex: 'member_count',
      width: 100,
      align: 'right',
      className: 'kv-num',
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
            icon={<EyeOutlined />}
            onClick={() => navigate(`/sessions/${row.id}`)}
          />
        </RowActions>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title={t('list.title')}
        actions={
          <Space>
            <RefreshButton onRefresh={reload} loading={loading} />
            {canManage ? (
              <Button type="primary" icon={<PlusOutlined />} onClick={() => navigate('/sessions/new')}>
                {t('list.new')}
              </Button>
            ) : null}
          </Space>
        }
      />
      <FilterBar
        activeCount={[status, q].filter(Boolean).length}
        onClear={() => {
          setStatus('')
          setQ('')
          setPage(1)
        }}
        hint={t('list.filterHint')}
      >
        <FilterField label={t('list.statusFilter')} htmlFor="session-status-filter" width={180}>
          <Select
            id="session-status-filter"
            value={status}
            aria-label={t('list.statusFilter')}
            onChange={(next) => {
              setStatus(next)
              setPage(1)
            }}
            options={[
              { value: '', label: tc('filters.allStatuses') },
              { value: 'DRAFT', label: tc('status.draft') },
              { value: 'OPENED', label: tc('status.opened') },
              { value: 'FINISHED', label: tc('status.finished') },
            ]}
          />
        </FilterField>
        <FilterField label={t('list.searchLabel')} htmlFor="session-search" width={280}>
          <Input.Search
            id="session-search"
            allowClear
            aria-label={t('list.search')}
            placeholder={t('list.search')}
            onSearch={(value) => {
              setQ(value.trim())
              setPage(1)
            }}
          />
        </FilterField>
      </FilterBar>
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
            scroll={{ x: 1092 }}
            empty={
              <EmptyState
                title={t('list.empty')}
                description={tc('emptyState.hint')}
                action={
                  canManage ? (
                    <Button
                      type="primary"
                      icon={<PlusOutlined />}
                      onClick={() => navigate('/sessions/new')}
                    >
                      {t('common:emptyState.first')}
                    </Button>
                  ) : undefined
                }
              />
            }
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
