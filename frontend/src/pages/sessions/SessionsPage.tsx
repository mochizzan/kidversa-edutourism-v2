/**
 * Session list (F-08): server-paged, server-side status/q filters, SSE
 * same-params refetch (`session.changed` / `session.status`), `session.deleted`
 * drops the row (soft same-params refetch). No client slicing, no polling.
 *
 * Every row carries the same inline lifecycle actions for SA/ADMIN — Edit,
 * Open, Close, Delete — so managers never have to open a session just to act
 * on it. A status that forbids an action leaves its button visible but
 * disabled with a reason, keeping the column predictable from row to row.
 * Open-gate failures open a dialog that names the groups to fix and links to
 * the detail page; delete fetches the detail first so its typed wipe gate
 * keeps the real scope counts.
 */
import { useState } from 'react'
import { Alert, App as AntApp, Button, Input, Select, Space, Tooltip } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { PlusOutlined } from '@ant-design/icons'
import { Link, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import FilterBar, { FilterField } from '../../components/FilterBar'
import PageHeader from '../../components/PageHeader'
import Pager from '../../components/Pager'
import RefreshButton from '../../components/RefreshButton'
import StatusChip from '../../components/StatusChip'
import { api, errorCode } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { useErrorToast } from '../../lib/errors'
import { useListLoader } from '../../lib/list'
import type { SessionDetail, SessionListItem } from '../../lib/session-contracts'
import DeleteSessionDialog from './DeleteSessionDialog'
import SessionMetaModal from './SessionMetaModal'
import SessionRowActions from './SessionRowActions'
import StartGateDialog from './StartGateDialog'
import './sessions.css'

export default function SessionsPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const toastError = useErrorToast()
  const { user } = useAuth()
  const navigate = useNavigate()
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const [status, setStatus] = useState('')
  const [q, setQ] = useState('')
  const [editing, setEditing] = useState<SessionListItem | null>(null)
  const [openingId, setOpeningId] = useState<number | null>(null)
  const [closingId, setClosingId] = useState<number | null>(null)
  const [openGate, setOpenGate] = useState<{ session: SessionListItem; failure: unknown } | null>(
    null,
  )
  const [preparingDeleteId, setPreparingDeleteId] = useState<number | null>(null)
  const [deleteTarget, setDeleteTarget] = useState<SessionDetail | null>(null)

  const { rows, meta, error, loading, reload } = useListLoader<SessionListItem>(
    '/api/sessions',
    { page, per_page: perPage, status: status || undefined, q: q || undefined },
    ['session.changed', 'session.status', 'session.deleted'],
  )

  const canManage = user?.role === 'SA' || user?.role === 'ADMIN'

  const openSession = async (row: SessionListItem) => {
    setOpeningId(row.id)
    try {
      await api.post<{ status: string; already?: boolean }>(`/api/sessions/${row.id}/open`)
      message.success(t('detail.openDone'))
      reload()
    } catch (failure: unknown) {
      const code = errorCode(failure)
      if (code === 'VALIDATION' || code === 'GROUP_WITHOUT_FACILITATOR') {
        setOpenGate({ session: row, failure })
      } else if (code === 'STATE_CONFLICT' || code === 'SESSION_LOCKED') {
        toastError(failure)
        reload()
      } else {
        toastError(failure)
      }
    } finally {
      setOpeningId(null)
    }
  }

  const closeSession = async (row: SessionListItem) => {
    setClosingId(row.id)
    try {
      await api.post<{ status: string; already?: boolean }>(`/api/sessions/${row.id}/close`)
      message.success(t('detail.closeDone'))
      reload()
    } catch (failure: unknown) {
      toastError(failure)
      const code = errorCode(failure)
      if (code === 'STATE_CONFLICT' || code === 'SESSION_LOCKED') {
        reload()
      }
    } finally {
      setClosingId(null)
    }
  }

  const openDelete = async (row: SessionListItem) => {
    setPreparingDeleteId(row.id)
    try {
      const detail = await api.get<SessionDetail>(`/api/sessions/${row.id}`)
      if (detail.session.status === 'OPENED') {
        // The row was stale: an open session must be closed first.
        message.warning(t('list.actionHints.deleteOpened'))
        reload()
        return
      }
      setDeleteTarget(detail)
    } catch (failure: unknown) {
      toastError(failure)
      if (errorCode(failure) === 'NOT_FOUND') reload()
    } finally {
      setPreparingDeleteId(null)
    }
  }

  const columns: ColumnsType<SessionListItem> = [
    {
      title: t('list.name'),
      dataIndex: 'name',
      width: 200,
      ellipsis: true,
      render: (name: string, row) => (
        <span className="kv-sessions-name">
          <Link to={`/sessions/${row.id}`} title={name}>
            {name}
          </Link>
          {user?.role === 'FASILITATOR' && row.my_assignment ? (
            <Tooltip title={t('list.myAssignmentHint')}>
              <span className="kv-session-mine">
                <span className="kv-session-mine__dot" aria-hidden="true" />
                {t('list.myAssignment')}
              </span>
            </Tooltip>
          ) : null}
        </span>
      ),
    },
    { title: t('list.program'), dataIndex: 'program_name', width: 150, ellipsis: true },
    { title: t('list.date'), dataIndex: 'date', width: 110 },
    { title: t('list.location'), dataIndex: 'location', width: 150, ellipsis: true },
    {
      title: t('list.status'),
      dataIndex: 'status',
      width: 110,
      render: (value: SessionListItem['status']) => <StatusChip status={value} />,
    },
    {
      title: t('list.groups'),
      dataIndex: 'group_count',
      width: 80,
      align: 'right',
      className: 'kv-num',
    },
    {
      title: t('list.members'),
      dataIndex: 'member_count',
      width: 90,
      align: 'right',
      className: 'kv-num',
    },
  ]

  if (canManage) {
    columns.push({
      title: tc('columns.actions'),
      key: 'actions',
      width: 148,
      fixed: 'right',
      render: (_, row) => (
        <SessionRowActions
          status={row.status}
          opening={openingId === row.id}
          closing={closingId === row.id}
          deleting={preparingDeleteId === row.id}
          onEdit={() => setEditing(row)}
          onOpen={() => void openSession(row)}
          onClose={() => void closeSession(row)}
          onDelete={() => void openDelete(row)}
        />
      ),
    })
  }

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
            scroll={{ x: 1038 }}
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

      {editing ? (
        <SessionMetaModal
          session={editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            message.success(t('detail.metaSaved'))
            reload()
          }}
        />
      ) : null}

      {openGate ? (
        <StartGateDialog
          sessionName={openGate.session.name}
          status={openGate.session.status}
          failure={openGate.failure}
          onClose={() => setOpenGate(null)}
          onReview={() => {
            const sid = openGate.session.id
            setOpenGate(null)
            navigate(`/sessions/${sid}`)
          }}
        />
      ) : null}

      {deleteTarget ? (
        <DeleteSessionDialog
          detail={deleteTarget}
          open
          onCancel={() => setDeleteTarget(null)}
          onDeleted={() => {
            setDeleteTarget(null)
            message.success(t('detail.deleted'))
            reload()
          }}
        />
      ) : null}
    </>
  )
}
