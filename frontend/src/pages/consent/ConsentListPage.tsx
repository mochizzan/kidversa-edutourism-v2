/**
 * Consent list `/consent/:sid` (F-10 / FL-11): paged
 * `GET /api/sessions/:sid/consents?status=&group_id=`, status + group
 * filters, copyable `consent_url` while PENDING, SEND/RESEND **ADMIN/SA
 * only** (hidden for FAS — menu matrix "read-only status"; server also
 * enforces FORBIDDEN_ROLE).
 *
 * The list is driven by SESSION MEMBERSHIP: a child with no consent row is
 * shown as NOT_SENT ("Not yet submitted"). Pending means a request was
 * actually sent and is awaiting the parent's answer.
 *
 * Bulk send: one `POST /api/sessions/:sid/send-consent {child_ids}` request
 * (new endpoint — the per-child route stays rate-limited 5/min/IP), with
 * per-child SENT/SKIPPED/FAILED outcomes. Row selection only enables rows
 * that can still be sent.
 *
 * Resend semantics (FL-11): still-PENDING → same token re-sent (`resent`);
 * already decided → `{decision, resent:false}` info (link unchanged, no WA);
 * FINISHED → button hidden, SESSION_LOCKED still handled as a toast.
 * SSE `consent.changed` → same-params refetch.
 */
import { useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, Flex, Select, Space, Typography } from 'antd'
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

interface BulkItemResult {
  child_id: number
  status: 'SENT' | 'SKIPPED' | 'FAILED'
  code?: string
  decision?: string
}

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
  const [groupId, setGroupId] = useState<number | null>(null)
  const [selectedKeys, setSelectedKeys] = useState<number[]>([])
  const [sendTarget, setSendTarget] = useState<ConsentItem | null>(null)
  const [bulkOpen, setBulkOpen] = useState(false)
  const [sending, setSending] = useState(false)
  const [sendError, setSendError] = useState<unknown>(null)

  const { rows, meta, error, loading, reload } = useListLoader<ConsentItem>(
    `/api/sessions/${sid}/consents`,
    {
      page,
      per_page: perPage,
      status: status || undefined,
      group_id: groupId ?? undefined,
    },
    ['consent.changed'],
  )

  const canSend =
    (user?.role === 'SA' || user?.role === 'ADMIN') && detail?.session.status !== 'FINISHED'

  // A page/filter change means a different set of checkboxes.
  useEffect(() => {
    setSelectedKeys([])
  }, [page, perPage, status, groupId])

  const selectable = (row: ConsentItem) =>
    canSend && (row.status === 'NOT_SENT' || row.status === 'PENDING')

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
      setSelectedKeys((keys) => keys.filter((key) => key !== sendTarget.child_id))
      setSendTarget(null)
      reload()
    } catch (failure: unknown) {
      setSendError(failure)
    } finally {
      setSending(false)
    }
  }

  const sendBulk = async () => {
    if (!sid || selectedKeys.length === 0) return
    setSending(true)
    setSendError(null)
    try {
      const data = await api.post<{ items: BulkItemResult[] }>(
        `/api/sessions/${sid}/send-consent`,
        { child_ids: selectedKeys },
        { idempotencyKey: crypto.randomUUID() },
      )
      const sent = data.items.filter((item) => item.status === 'SENT').length
      const failed = data.items.filter((item) => item.status === 'FAILED').length
      const skipped = data.items.filter((item) => item.status === 'SKIPPED').length
      if (failed > 0) {
        message.warning(t('consent.bulkDone', { sent, failed, skipped }))
      } else if (sent > 0) {
        message.success(t('consent.bulkDone', { sent, failed, skipped }))
      } else {
        message.info(t('consent.bulkSkipped', { skipped }))
      }
      setSelectedKeys([])
      setBulkOpen(false)
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
    {
      title: t('consent.group'),
      dataIndex: 'group_name',
      width: 160,
      ellipsis: true,
      render: (value: string | null) => value ?? '—',
    },
    {
      title: t('consent.title'),
      dataIndex: 'status',
      width: 150,
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
          {selectable(row) ? (
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
            {detail?.session.name ?? `#${sid}`}
            {detail ? <StatusChip status={detail.session.status} /> : null}
            {!canSend && detail ? <span className="kv-meta">{t('consent.readOnly')}</span> : null}
          </Space>
        }
        actions={<RefreshButton onRefresh={reload} loading={loading} />}
      />

      <FilterBar
        activeCount={(status ? 1 : 0) + (groupId !== null ? 1 : 0)}
        onClear={() => {
          setStatus('')
          setGroupId(null)
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
              { value: 'NOT_SENT', label: t('consent.notYetSubmitted') },
              { value: 'PENDING', label: t('consent.pending') },
              { value: 'SETUJU', label: t('consent.approved') },
              { value: 'DITOLAK', label: t('consent.rejected') },
            ]}
          />
        </FilterField>
        <FilterField label={t('consent.group')} htmlFor="consent-group-filter" width={220}>
          <Select
            id="consent-group-filter"
            value={groupId ?? undefined}
            aria-label={t('consent.group')}
            placeholder={t('consent.allGroups')}
            allowClear
            onChange={(next?: number) => {
              setGroupId(next ?? null)
              setPage(1)
            }}
            options={(detail?.groups ?? []).map((group) => ({
              value: group.gid,
              label: group.name,
            }))}
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
          {canSend && rows.length > 0 ? (
            <Flex
              justify="space-between"
              align="center"
              gap="middle"
              wrap
              style={{ marginBottom: 12 }}
            >
              <Typography.Text type="secondary">
                {selectedKeys.length > 0
                  ? t('consent.selectedCount', { count: selectedKeys.length })
                  : t('consent.selectHint')}
              </Typography.Text>
              <Button
                type="primary"
                icon={<SendOutlined />}
                disabled={selectedKeys.length === 0}
                onClick={() => {
                  setSendError(null)
                  setBulkOpen(true)
                }}
              >
                {t('consent.sendSelected')}
              </Button>
            </Flex>
          ) : null}
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
            rowSelection={
              canSend
                ? {
                    selectedRowKeys: selectedKeys,
                    onChange: (keys) => setSelectedKeys(keys.map(Number)),
                    getCheckboxProps: (row) => ({ disabled: !selectable(row) }),
                    columnWidth: 44,
                  }
                : undefined
            }
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
            {sendTarget.child_name} · {sendTarget.group_name ?? '—'}
          </p>
        ) : null}
        {sendError ? <SendErrorAlert error={sendError} /> : null}
      </ConfirmDialog>

      <ConfirmDialog
        open={bulkOpen}
        title={t('consent.sendSelectedTitle', { count: selectedKeys.length })}
        confirmLabel={t('consent.sendSelected')}
        cancelLabel={tc('actions.cancel')}
        loading={sending}
        onCancel={() => setBulkOpen(false)}
        onConfirm={sendBulk}
      >
        <p>{t('consent.sendSelectedBody')}</p>
        {sendError ? <SendErrorAlert error={sendError} /> : null}
      </ConfirmDialog>
    </>
  )
}

function SendErrorAlert({ error }: { error: unknown }) {
  const { t: tc } = useTranslation('common')
  return (
    <Alert
      type="error"
      showIcon
      message={
        error instanceof ApiError && error.message !== error.code
          ? error.message
          : tc(`errors.${errorCode(error)}`)
      }
    />
  )
}
