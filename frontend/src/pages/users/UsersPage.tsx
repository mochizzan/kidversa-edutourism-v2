/**
 * Users (F-03 / FL-01): paged list with role/decision/is_active/q filters
 * (any filter change resets page=1; SA sees role filter + school column, ADMIN
 * is facilitator-scope so the role filter is hidden). Pending badge renders
 * `pending.changed` full-value signals; rows refetch on `user.changed`.
 * Delete uses a typed-text ConfirmDialog and surfaces the server's
 * HAS_OPEN_GROUPS list inline. Reset codes live in the second tab.
 */
import { useCallback, useEffect, useState } from 'react'
import { Alert, Badge, Button, Input, List, Select, Tabs, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  CheckCircleOutlined,
  CheckOutlined,
  CloseOutlined,
  DeleteOutlined,
  EditOutlined,
  KeyOutlined,
  LoginOutlined,
  PlusOutlined,
  StopOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { useSearchParams } from 'react-router'
import { api, errorCode, ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { useErrorToast } from '../../lib/errors'
import { useFeedback } from '../../lib/feedback'
import { useListLoader } from '../../lib/list'
import { registerRefetch, registerSignal } from '../../lib/stream'
import FilterBar, { FilterField } from '../../components/FilterBar'
import ConfirmDialog from '../../components/ConfirmDialog'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import PageHeader from '../../components/PageHeader'
import Pager from '../../components/Pager'
import { RowAction, RowActionGroup, RowActions, RowActionsDivider } from '../../components/RowActions'
import RefreshButton from '../../components/RefreshButton'
import StatusPill from '../../components/StatusPill'
import ImpersonationModal from './ImpersonationModal'
import ResetPasswordModal from './ResetPasswordModal'
import ResetTokensPanel from './ResetTokensPanel'
import UserModal, { type UserRow } from './UserModal'
import '../admin/admin.css'

interface OpenGroup {
  gid: number
  session_id: number
  session_name: string
}

const DECISIONS = ['PENDING', 'APPROVED', 'REJECTED']

/** Row lifecycle actions and the confirmation verb each one earns. */
const ACTION_STATE = {
  approve: 'approved',
  reject: 'rejected',
  activate: 'activated',
  deactivate: 'deactivated',
} as const

export default function UsersPage() {
  const { t } = useTranslation(['auth', 'common'])
  const { user: me } = useAuth()
  const toast = useErrorToast()
  const feedback = useFeedback()
  const [searchParams] = useSearchParams()
  const isSa = me?.role === 'SA'

  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const [q, setQ] = useState('')
  const [role, setRole] = useState('')
  const [decision, setDecision] = useState(() => {
    const initial = searchParams.get('decision')
    return initial !== null && DECISIONS.includes(initial) ? initial : ''
  })
  const [active, setActive] = useState('')
  const [pending, setPending] = useState(0)
  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<UserRow | null>(null)
  const [deleting, setDeleting] = useState<UserRow | null>(null)
  const [impersonating, setImpersonating] = useState<UserRow | null>(null)
  const [resetting, setResetting] = useState<UserRow | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [openGroups, setOpenGroups] = useState<OpenGroup[]>([])
  const [deleteLoading, setDeleteLoading] = useState(false)

  const { rows, meta, error, loading, reload } = useListLoader<UserRow>(
    '/api/users',
    { page, per_page: perPage, q, role, decision, is_active: active },
    ['user.changed'],
  )
  const listError = error === null ? null : errorCode(error)

  // Pending badge fetch composes BESIDE the list (004a): best-effort, never
  // blocks the rows; refetches on user.changed like the list does.
  const loadPending = useCallback(() => {
    api.get<{ pending: number }>('/api/users/pending-count').then(
      (counts) => setPending(counts.pending),
      () => undefined,
    )
  }, [])
  useEffect(() => {
    loadPending()
  }, [loadPending])
  useEffect(() => registerRefetch('user.changed', loadPending), [loadPending])
  useEffect(
    () => registerSignal<{ pending: number }>('pending.changed', (data) => setPending(data.pending)),
    [],
  )

  /** Mutations refresh BOTH the list and the pending badge. */
  const load = useCallback(() => {
    reload()
    loadPending()
  }, [reload, loadPending])

  /**
   * Design 2026-11-11 §8.3 (defence in depth, never the enforcement):
   * SA manages ADMIN + FASILITATOR, ADMIN manages own-school FACILITATORs,
   * and nobody manages their own row. The server predicates still decide.
   */
  const canManage = (row: UserRow): boolean => {
    if (row.id === me?.id) return false
    if (me?.role === 'SA') return row.role === 'ADMIN' || row.role === 'FASILITATOR'
    if (me?.role === 'ADMIN') return row.role === 'FASILITATOR'
    return false
  }

  const act = (row: UserRow, action: keyof typeof ACTION_STATE) => {
    api.post(`/api/users/${row.id}/${action}`).then(
      () => {
        feedback.changed('user', row.name, ACTION_STATE[action])
        load()
      },
      toast,
    )
  }

  const confirmDelete = () => {
    if (!deleting) return
    setDeleteLoading(true)
    api.del(`/api/users/${deleting.id}`).then(
      () => {
        feedback.deleted('user', deleting.name)
        setDeleteLoading(false)
        setDeleting(null)
        load()
      },
      (cause: unknown) => {
        setDeleteLoading(false)
        if (cause instanceof ApiError && cause.code === 'HAS_OPEN_GROUPS') {
          const groups = cause.details?.groups
          setOpenGroups(Array.isArray(groups) ? (groups as OpenGroup[]) : [])
        } else {
          setOpenGroups([])
        }
        setDeleteError(errorCode(cause))
      },
    )
  }

  const columns: ColumnsType<UserRow> = [
    { title: t('auth:users.name'), dataIndex: 'name', key: 'name', width: 170, ellipsis: true },
    { title: t('auth:users.email'), dataIndex: 'email', key: 'email', width: 200, ellipsis: true },
    {
      title: t('auth:users.role'),
      dataIndex: 'role',
      key: 'role',
      width: 110,
      render: (value: UserRow['role']) => t(`common:role.${value}`),
    },
    {
      title: t('auth:users.decision'),
      dataIndex: 'decision',
      key: 'decision',
      width: 110,
      render: (value: UserRow['decision']) => (
        <StatusPill
          tone={
            value === 'APPROVED' ? 'positive' : value === 'REJECTED' ? 'attention' : 'neutral'
          }
        >
          {t(`common:status.${value.toLowerCase()}`)}
        </StatusPill>
      ),
    },
    ...(isSa
      ? [
          {
            title: t('auth:users.school'),
            dataIndex: 'school_name',
            key: 'school_name',
            width: 150,
            ellipsis: true,
            render: (value: string | null) => value ?? '—',
          },
        ]
      : []),
    {
      title: t('auth:schools.status'),
      key: 'is_active',
      width: 100,
      render: (_value, row) => (
        <StatusPill tone={row.is_active ? 'positive' : 'neutral'}>
          {t(`common:status.${row.is_active ? 'active' : 'inactive'}`)}
        </StatusPill>
      ),
    },
    {
      title: t('common:columns.actions'),
      key: 'actions',
      width: 176,
      fixed: 'right',
      render: (_value, row) => (
        <RowActions>
          {row.decision === 'PENDING' ? (
            <>
              <RowAction
                accent
                label={t('auth:users.approve')}
                icon={<CheckOutlined />}
                confirm={t('auth:users.approveConfirm', { name: row.name })}
                onClick={() => act(row, 'approve')}
              />
              <RowAction
                danger
                label={t('auth:users.reject')}
                icon={<CloseOutlined />}
                confirm={t('auth:users.rejectConfirm', { name: row.name })}
                onClick={() => act(row, 'reject')}
              />
            </>
          ) : (
            <RowAction
              accent={!row.is_active}
              label={t(row.is_active ? 'auth:users.deactivate' : 'auth:users.activate')}
              icon={row.is_active ? <StopOutlined /> : <CheckCircleOutlined />}
              confirm={t(
                row.is_active ? 'auth:users.deactivateConfirm' : 'auth:users.activateConfirm',
                { name: row.name },
              )}
              onClick={() => act(row, row.is_active ? 'deactivate' : 'activate')}
            />
          )}
          <RowAction
            label={t('common:actions.edit')}
            icon={<EditOutlined />}
            onClick={() => {
              setEditing(row)
              setModalOpen(true)
            }}
          />
          {canManage(row) ? (
            <RowActionGroup
              label={t('auth:users.moreActions')}
              items={[
                {
                  key: 'impersonate',
                  label: t('auth:users.loginAs'),
                  icon: <LoginOutlined />,
                  onClick: () => setImpersonating(row),
                },
                {
                  key: 'reset-password',
                  label: t('auth:users.resetPassword'),
                  icon: <KeyOutlined />,
                  onClick: () => setResetting(row),
                },
              ]}
            />
          ) : null}
          <RowActionsDivider />
          <RowAction
            danger
            label={t('common:actions.delete')}
            icon={<DeleteOutlined />}
            onClick={() => {
              setDeleteError(null)
              setOpenGroups([])
              setDeleting(row)
            }}
          />
        </RowActions>
      ),
    },
  ]

  const openCreate = () => {
    setEditing(null)
    setModalOpen(true)
  }

  const usersTab = (
    <>
      <FilterBar
        activeCount={[q, isSa ? role : '', decision, active].filter(Boolean).length}
        onClear={() => {
          setPage(1)
          setQ('')
          setRole('')
          setDecision('')
          setActive('')
        }}
      >
        <FilterField label={t('auth:users.searchLabel')} htmlFor="user-search" width={260}>
          <Input.Search
            id="user-search"
            allowClear
            aria-label={t('auth:users.searchPlaceholder')}
            placeholder={t('auth:users.searchPlaceholder')}
            onSearch={(value) => {
              setPage(1)
              setQ(value.trim())
            }}
            onChange={(event) => {
              if (event.target.value === '') {
                setPage(1)
                setQ('')
              }
            }}
          />
        </FilterField>
        {isSa ? (
          <FilterField label={t('auth:users.role')} htmlFor="user-role-filter" width={170}>
            <Select
              id="user-role-filter"
              value={role}
              aria-label={t('auth:users.role')}
              options={[
                { value: '', label: t('common:filters.allRoles') },
                { value: 'SA', label: t('common:role.SA') },
                { value: 'ADMIN', label: t('common:role.ADMIN') },
                { value: 'FASILITATOR', label: t('common:role.FASILITATOR') },
              ]}
              onChange={(value) => {
                setPage(1)
                setRole(value)
              }}
            />
          </FilterField>
        ) : null}
        <FilterField label={t('auth:users.decision')} htmlFor="user-decision-filter" width={170}>
          <Select
            id="user-decision-filter"
            value={decision}
            aria-label={t('auth:users.decision')}
            options={[
              { value: '', label: t('common:filters.allDecisions') },
              { value: 'PENDING', label: t('common:status.pending') },
              { value: 'APPROVED', label: t('common:status.approved') },
              { value: 'REJECTED', label: t('common:status.rejected') },
            ]}
            onChange={(value) => {
              setPage(1)
              setDecision(value)
            }}
          />
        </FilterField>
        <FilterField label={t('auth:users.statusLabel')} htmlFor="user-status-filter" width={170}>
          <Select
            id="user-status-filter"
            value={active}
            aria-label={t('auth:users.statusLabel')}
            options={[
              { value: '', label: t('common:filters.allStatuses') },
              { value: 'true', label: t('common:status.active') },
              { value: 'false', label: t('common:status.inactive') },
            ]}
            onChange={(value) => {
              setPage(1)
              setActive(value)
            }}
          />
        </FilterField>
        {pending > 0 ? (
          <Button
            type={decision === 'PENDING' ? 'primary' : 'default'}
            onClick={() => {
              setPage(1)
              setDecision('PENDING')
            }}
          >
            {t('auth:users.pendingPill', { count: pending })}
          </Button>
        ) : null}
      </FilterBar>

      {listError ? (
        <Alert
          className="kv-list-error"
          type="error"
          showIcon
          message={t(`common:errors.${listError}`)}
          action={
            <Button size="small" onClick={load}>
              {t('common:actions.retry')}
            </Button>
          }
        />
      ) : (
        <>
          <DataTable<UserRow>
            rowKey="id"
            columns={columns}
            dataSource={rows}
            loading={loading}
            pagination={false}
            scroll={{ x: 1020 }}
            empty={
              <EmptyState
                title={t('auth:users.emptyTitle')}
                description={t('common:emptyState.hint')}
                action={
                  <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
                    {t('common:emptyState.first')}
                  </Button>
                }
              />
            }
          />
          <Pager
            page={meta?.page ?? page}
            perPage={meta?.per_page ?? perPage}
            total={meta?.total ?? 0}
            onPage={setPage}
            onPerPage={(size) => {
              setPage(1)
              setPerPage(size)
            }}
          />
        </>
      )}
    </>
  )

  return (
    <>
      <PageHeader
        title={t('auth:users.title')}
        description={t('auth:users.subtitle')}
        actions={
          <>
            <RefreshButton onRefresh={load} loading={loading} />
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              {t('auth:users.create')}
            </Button>
          </>
        }
      />

      <Tabs
        items={[
          {
            key: 'users',
            label: (
              <span className="kv-nav-label">
                {t('auth:users.tabUsers')}
                {pending > 0 ? (
                  <Badge className="kv-nav-badge" count={pending} size="small" />
                ) : null}
              </span>
            ),
            children: usersTab,
          },
          {
            key: 'reset',
            label: t('auth:users.tabReset'),
            children: <ResetTokensPanel />,
          },
        ]}
      />

      <UserModal
        open={modalOpen}
        user={editing}
        onClose={() => setModalOpen(false)}
        onSaved={() => {
          setModalOpen(false)
          load()
        }}
      />

      {/* Impersonation replaces the whole session and navigates away, so its
          success path must NOT reload this list. */}
      <ImpersonationModal
        open={impersonating !== null}
        target={impersonating}
        onClose={() => setImpersonating(null)}
      />

      <ResetPasswordModal
        open={resetting !== null}
        target={resetting}
        onClose={() => setResetting(null)}
        onDone={load}
      />

      <ConfirmDialog
        open={deleting !== null}
        title={t('auth:users.deleteTitle', { name: deleting?.name ?? '' })}
        typedText={deleting?.name}
        danger
        loading={deleteLoading}
        onConfirm={confirmDelete}
        onCancel={() => setDeleting(null)}
      >
        <p>{t('auth:users.deleteBody')}</p>
        <p>{t('common:confirm.irreversible')}</p>
        {deleteError ? (
          <Alert type="error" showIcon message={t(`common:errors.${deleteError}`)} />
        ) : null}
        {openGroups.length > 0 ? (
          <>
            <p className="kv-meta">{t('auth:users.openGroups')}</p>
            <List
              size="small"
              style={{ marginTop: 8 }}
              dataSource={openGroups}
              renderItem={(group) => (
                <List.Item style={{ padding: '2px 0' }}>
                  <Typography.Text type="secondary" style={{ fontSize: 11 }}>
                    {group.session_name}
                  </Typography.Text>
                </List.Item>
              )}
            />
          </>
        ) : null}
      </ConfirmDialog>
    </>
  )
}
