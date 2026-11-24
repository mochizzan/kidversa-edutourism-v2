/**
 * Schools (F-02) [SA]: paged list (server-side only, Pager), create/edit
 * modal, activate/deactivate, and a typed-text ConfirmDialog hard delete.
 * List refetches on `school.changed` signals.
 */
import { useState } from 'react'
import { Alert, Button, Input, Select } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import {
  CheckCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  StopOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { useListLoader } from '../../lib/list'
import { useErrorToast } from '../../lib/errors'
import { useFeedback } from '../../lib/feedback'
import ConfirmDialog from '../../components/ConfirmDialog'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import FilterBar, { FilterField } from '../../components/FilterBar'
import PageHeader from '../../components/PageHeader'
import Pager from '../../components/Pager'
import { RowAction, RowActions, RowActionsDivider } from '../../components/RowActions'
import RefreshButton from '../../components/RefreshButton'
import StatusPill from '../../components/StatusPill'
import SchoolModal from './SchoolModal'
import '../admin/admin.css'

export interface SchoolRow {
  id: number
  school_name: string
  country: string
  city: string | null
  is_active: boolean
  created_at: string
}

export default function SchoolsPage() {
  const { t } = useTranslation(['auth', 'common'])
  const toast = useErrorToast()
  const feedback = useFeedback()
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const [q, setQ] = useState('')
  const [active, setActive] = useState('')
  const [modalOpen, setModalOpen] = useState(false)
  const [editing, setEditing] = useState<SchoolRow | null>(null)
  const [deleting, setDeleting] = useState<SchoolRow | null>(null)
  const [deleteError, setDeleteError] = useState<string | null>(null)
  const [deleteLoading, setDeleteLoading] = useState(false)

  const { rows, meta, error, loading, reload } = useListLoader<SchoolRow>(
    '/api/schools',
    { page, per_page: perPage, q, is_active: active },
    ['school.changed'],
  )
  const listError = error === null ? null : errorCode(error)

  const toggleActive = (row: SchoolRow) => {
    api
      .post(`/api/schools/${row.id}/${row.is_active ? 'deactivate' : 'activate'}`)
      .then(
        () => {
          feedback.changed('school', row.school_name, row.is_active ? 'deactivated' : 'activated')
          reload()
        },
        toast,
      )
  }

  const confirmDelete = () => {
    if (!deleting) return
    setDeleteLoading(true)
    api.del(`/api/schools/${deleting.id}`).then(
      () => {
        feedback.deleted('school', deleting.school_name)
        setDeleteLoading(false)
        setDeleting(null)
        reload()
      },
      (cause: unknown) => {
        setDeleteLoading(false)
        setDeleteError(errorCode(cause))
      },
    )
  }

  const columns: ColumnsType<SchoolRow> = [
    {
      title: t('auth:schools.schoolName'),
      dataIndex: 'school_name',
      key: 'school_name',
      width: 220,
      ellipsis: true,
    },
    {
      title: t('auth:schools.country'),
      dataIndex: 'country',
      key: 'country',
      width: 140,
      ellipsis: true,
    },
    {
      title: t('auth:schools.city'),
      dataIndex: 'city',
      key: 'city',
      width: 140,
      ellipsis: true,
      render: (value: string | null) => value || '—',
    },
    {
      title: t('auth:schools.status'),
      key: 'is_active',
      width: 120,
      render: (_value, row) => (
        <StatusPill tone={row.is_active ? 'positive' : 'neutral'}>
          {t(`common:status.${row.is_active ? 'active' : 'inactive'}`)}
        </StatusPill>
      ),
    },
    {
      title: t('auth:schools.createdAt'),
      dataIndex: 'created_at',
      key: 'created_at',
      width: 140,
      render: (value: string) => (value ? new Date(value).toLocaleDateString() : '—'),
    },
    {
      title: t('common:columns.actions'),
      key: 'actions',
      width: 122,
      fixed: 'right',
      render: (_value, row) => (
        <RowActions>
          <RowAction
            label={t('common:actions.edit')}
            icon={<EditOutlined />}
            onClick={() => {
              setEditing(row)
              setModalOpen(true)
            }}
          />
          <RowAction
            accent={!row.is_active}
            label={t(row.is_active ? 'auth:schools.deactivate' : 'auth:schools.activate')}
            icon={row.is_active ? <StopOutlined /> : <CheckCircleOutlined />}
            confirm={t(
              row.is_active ? 'auth:schools.deactivateConfirm' : 'auth:schools.activateConfirm',
            )}
            onClick={() => toggleActive(row)}
          />
          <RowActionsDivider />
          <RowAction
            danger
            label={t('common:actions.delete')}
            icon={<DeleteOutlined />}
            onClick={() => {
              setDeleteError(null)
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

  return (
    <>
      <PageHeader
        title={t('auth:schools.title')}
        description={t('auth:schools.subtitle')}
        actions={
          <>
            <RefreshButton onRefresh={reload} loading={loading} />
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              {t('auth:schools.create')}
            </Button>
          </>
        }
      />

      <FilterBar
        activeCount={[q, active].filter(Boolean).length}
        onClear={() => {
          setPage(1)
          setQ('')
          setActive('')
        }}
      >
        <FilterField label={t('auth:schools.searchLabel')} htmlFor="school-search" width={280}>
          <Input.Search
            id="school-search"
            allowClear
            aria-label={t('auth:schools.searchPlaceholder')}
            placeholder={t('auth:schools.searchPlaceholder')}
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
        <FilterField label={t('auth:schools.statusLabel')} htmlFor="school-status-filter" width={170}>
          <Select
            id="school-status-filter"
            value={active}
            aria-label={t('auth:schools.statusLabel')}
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
      </FilterBar>

      {listError ? (
        <Alert
          className="kv-list-error"
          type="error"
          showIcon
          message={t(`common:errors.${listError}`)}
          action={
            <Button size="small" onClick={reload}>
              {t('common:actions.retry')}
            </Button>
          }
        />
      ) : (
        <>
          <DataTable<SchoolRow>
            rowKey="id"
            columns={columns}
            dataSource={rows}
            loading={loading}
            pagination={false}
            scroll={{ x: 940 }}
            empty={
              <EmptyState
                title={t('auth:schools.emptyTitle')}
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

      <SchoolModal
        open={modalOpen}
        school={editing}
        onClose={() => setModalOpen(false)}
        onSaved={() => {
          setModalOpen(false)
          reload()
        }}
      />

      <ConfirmDialog
        open={deleting !== null}
        title={t('auth:schools.deleteTitle', { name: deleting?.school_name ?? '' })}
        typedText={deleting?.school_name}
        danger
        loading={deleteLoading}
        onConfirm={confirmDelete}
        onCancel={() => setDeleting(null)}
      >
        <p>{t('auth:schools.deleteBody')}</p>
        <p>{t('common:confirm.irreversible')}</p>
        {deleteError ? (
          <Alert type="error" showIcon message={t(`common:errors.${deleteError}`)} />
        ) : null}
      </ConfirmDialog>
    </>
  )
}
