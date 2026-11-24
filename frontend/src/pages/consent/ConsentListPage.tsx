/**
 * Consent list `/consent/:sid` (F-10 / FL-11): paged
 * `GET /api/sessions/:sid/consents?status=`, status filter, copyable
 * `consent_url` while PENDING, SEND/RESEND **ADMIN/SA only** (hidden for FAS —
 * menu matrix "read-only status"; server also enforces FORBIDDEN_ROLE).
 *
 * Resend semantics (FL-11): still-PENDING → same token re-sent (`resent`);
 * already decided → `{decision, resent:false}` info (link unchanged, no WA);
 * FINISHED → button hidden, SESSION_LOCKED still handled as a toast.
 * SSE `consent.changed` → same-params refetch.
 */
import { useState } from 'react'
import { Alert, App as AntApp, Button, Select, Space } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { CopyOutlined, SendOutlined } from '@ant-design/icons'
import { Link, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import ConfirmDialog from '../../components/ConfirmDialog'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import FilterBar, { FilterField } from '../../components/FilterBar'
import PageHeader from '../../components/PageHeader'
import Pager from '../../components/Pager'
import RefreshButton from '../../components/RefreshButton'
import { RowAction, RowActions } from '../../components/RowActions'
import StatusChip from '../../components/StatusChip'
import { api, errorCode, ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { useCrumbName } from '../../lib/crumbs'
import { useListLoader } from '../../lib/list'
import {
  consentStatusLabel,
  copyText,
  fmtDateTime,
  type ConsentItem,
} from '../../lib/session-contracts'
import { ConsentTag } from '../sessions/controls'
import { useSessionDetail } from '../sessions/shared'

export default function ConsentListPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const { user } = useAuth()
  const { sid } = useParams()
  const { detail, error: detailError } = useSessionDetail(sid)

  // The breadcrumb bar shows the session's name instead of "Detail sesi".
  useCrumbName('sid', detail?.session.name)

  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const [status, setStatus] = useState('')
  const [sendTarget, setSendTarget] = useState<ConsentItem | null>(null)
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<unknown>(null)

  const { rows, meta, error, loading, reload } = useListLoader<ConsentItem>(
    `/api/sessions/${sid}/consents`,
    { page, per_page: perPage, status: status || undefined },
    ['consent.changed'],
  )

  const canSend =
    (user?.role === 'SA' || user?.role === 'ADMIN') && detail?.session.status !== 'FINISHED'

  const send = async () => {
    if (!sendTarget || !sid) return
    setSending(true)
    setSendError(null)
    try {
      const data = await api.post<{ token: string; decision: string; resent: boolean }>(
        `/api/sessions/${sid}/children/${sendTarget.child_id}/send-consent`,
      )
      if (data.resent) message.success(t('consent.sent'))
      else message.info(t('consent.alreadyDecided', { status: consentStatusLabel(t, data.decision) }))
      setSendTarget(null)
      reload()
    } catch (failure: unknown) {
      setSendError(failure)
    } finally {
      setSending(false)
    }
  }

  if (detailError) {
    return (
      <>
        <PageHeader title={t('consent.title')} />
        <Alert
          type="error"
          showIcon
          message={tc(`errors.${errorCode(detailError)}`)}
          action={<Link to="/consent">{tc('actions.back')}</Link>}
        />
      </>
    )
  }

  const columns: ColumnsType<ConsentItem> = [
    { title: t('consent.child'), dataIndex: 'child_name', width: 180, ellipsis: true },
    { title: t('consent.group'), dataIndex: 'group_name', width: 160, ellipsis: true },
    {
      title: t('consent.title'),
      dataIndex: 'status',
      width: 130,
      render: (value: ConsentItem['status']) => <ConsentTag status={value} />,
    },
    {
      title: t('consent.respondent'),
      dataIndex: 'respondent_name',
      width: 180,
      ellipsis: true,
      render: (value: string | null) => value ?? '—',
    },
    {
      title: t('consent.time'),
      dataIndex: 'responded_at',
      width: 170,
      render: (value: string | null) => fmtDateTime(value),
    },
    {
      title: tc('columns.actions'),
      key: 'actions',
      width: 92,
      fixed: 'right',
      render: (_, row) => (
        <RowActions>
          {row.status === 'PENDING' && row.consent_url ? (
            <RowAction
              label={t('consent.copyUrl')}
              icon={<CopyOutlined />}
              onClick={() => {
                void copyText(row.consent_url ?? '').then((ok) =>
                  ok
                    ? message.success(tc('actions.copied'))
                    : message.error(tc('errors.INTERNAL')),
                )
              }}
            />
          ) : null}
          {canSend && row.status === 'PENDING' ? (
            <RowAction
              accent
              label={t('consent.send')}
              icon={<SendOutlined />}
              onClick={() => {
                setSendError(null)
                setSendTarget(row)
              }}
            />
          ) : null}
        </RowActions>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title={t('consent.title')}
        description={
          <Space wrap>
            <Link to={`/sessions/${sid}`}>{detail?.session.name ?? `#${sid}`}</Link>
            {detail ? <StatusChip status={detail.session.status} /> : null}
            {!canSend && detail ? <span className="kv-meta">{t('consent.readOnly')}</span> : null}
          </Space>
        }
        actions={<RefreshButton onRefresh={reload} loading={loading} />}
      />

      <FilterBar
        activeCount={status ? 1 : 0}
        onClear={() => {
          setStatus('')
          setPage(1)
        }}
      >
        <FilterField label={t('consent.statusLabel')} htmlFor="consent-status-filter" width={200}>
          <Select
            id="consent-status-filter"
            value={status}
            aria-label={t('consent.statusLabel')}
            onChange={(next) => {
              setStatus(next)
              setPage(1)
            }}
            options={[
              { value: '', label: tc('filters.allStatuses') },
              { value: 'PENDING', label: t('consent.pending') },
              { value: 'SETUJU', label: t('consent.approved') },
              { value: 'DITOLAK', label: t('consent.rejected') },
            ]}
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
          {rows.some((row) => row.status === 'PENDING') ? (
            <p className="kv-meta">{t('consent.urlWhilePending')}</p>
          ) : null}
          <DataTable<ConsentItem>
            rowKey="child_id"
            loading={loading}
            columns={columns}
            dataSource={rows}
            pagination={false}
            scroll={{ x: 930 }}
            empty={
              <EmptyState
                title={t('consent.empty')}
                description={tc('emptyState.hint')}
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

      <ConfirmDialog
        open={sendTarget !== null}
        title={t('consent.sendTitle', { child: sendTarget?.child_name ?? '' })}
        confirmLabel={t('consent.send')}
        cancelLabel={tc('actions.cancel')}
        loading={sending}
        onCancel={() => setSendTarget(null)}
        onConfirm={send}
      >
        {sendTarget ? (
          <p className="kv-meta">
            {sendTarget.child_name} · {sendTarget.group_name}
          </p>
        ) : null}
        {sendError ? (
          <Alert
            type="error"
            showIcon
            message={
              sendError instanceof ApiError && sendError.message !== sendError.code
                ? sendError.message
                : tc(`errors.${errorCode(sendError)}`)
            }
          />
        ) : null}
      </ConfirmDialog>
    </>
  )
}
