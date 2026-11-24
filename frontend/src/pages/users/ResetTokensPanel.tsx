/**
 * Reset-token dashboard (F-01 / FL-03): paged masked codes with a local
 * 1s TokenCountdown (no polling), audited one-time reveal in a modal with
 * copy, and revoke. Refetches on `reset.changed` signals only — there is no
 * 5 s poll anywhere.
 */
import { useState } from 'react'
import { App as AntApp, Alert, Button, Input, Modal, Space } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { EyeOutlined, StopOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { useErrorToast } from '../../lib/errors'
import { useFeedback } from '../../lib/feedback'
import { useListLoader } from '../../lib/list'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import Pager from '../../components/Pager'
import { RowAction, RowActions, RowActionsDivider } from '../../components/RowActions'
import StatusPill from '../../components/StatusPill'
import TokenCountdown from '../../components/TokenCountdown'
import '../admin/admin.css'

interface ResetRow {
  id: number
  code_masked: string
  expires_at: string
  seconds_left: number
  revoked: boolean
  mail_failed: boolean
  user: { id: number; name: string; email: string }
}

export default function ResetTokensPanel() {
  const { t } = useTranslation(['auth', 'common'])
  const { message } = AntApp.useApp()
  const toast = useErrorToast()
  const feedback = useFeedback()
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)
  const [revealed, setRevealed] = useState<string | null>(null)

  const { rows, meta, error, loading, reload } = useListLoader<ResetRow>(
    '/api/reset-tokens',
    { page, per_page: perPage },
    ['reset.changed'],
  )
  const listError = error === null ? null : errorCode(error)

  const reveal = (row: ResetRow) => {
    api.post<{ code_plain: string }>(`/api/reset-tokens/${row.id}/reveal`).then(
      (data) => setRevealed(data.code_plain),
      toast,
    )
  }

  const revoke = (row: ResetRow) => {
    api.post(`/api/reset-tokens/${row.id}/revoke`).then(
      () => {
        feedback.changed('reset', row.user.name, 'revoked')
        reload()
      },
      toast,
    )
  }

  const columns: ColumnsType<ResetRow> = [
    {
      title: t('auth:users.reset.user'),
      key: 'user',
      render: (_value, row) => (
        <>
          <div>{row.user.name}</div>
          <div className="kv-meta">{row.user.email}</div>
        </>
      ),
    },
    {
      title: t('auth:users.reset.code'),
      dataIndex: 'code_masked',
      key: 'code_masked',
      render: (value: string, row) => (
        <Space size={4}>
          <span className="kv-num">{value}</span>
          {row.mail_failed ? (
            <StatusPill tone="attention">{t('auth:users.reset.mailFailed')}</StatusPill>
          ) : null}
        </Space>
      ),
    },
    {
      title: t('auth:users.reset.expires'),
      dataIndex: 'expires_at',
      key: 'expires_at',
      render: (value: string) => <TokenCountdown expiresAt={value} />,
    },
    {
      title: t('auth:users.reset.status'),
      key: 'status',
      render: (_value, row) => (
        <StatusPill tone={row.revoked ? 'neutral' : 'positive'}>
          {t(row.revoked ? 'auth:users.reset.revoked' : 'auth:users.reset.valid')}
        </StatusPill>
      ),
    },
    {
      title: t('common:columns.actions'),
      key: 'actions',
      width: 92,
      render: (_value, row) => (
        <RowActions>
          <RowAction
            label={t('auth:users.reset.reveal')}
            icon={<EyeOutlined />}
            disabled={row.revoked}
            onClick={() => reveal(row)}
          />
          <RowActionsDivider />
          <RowAction
            danger
            label={t('auth:users.reset.revoke')}
            icon={<StopOutlined />}
            disabled={row.revoked}
            confirm={t('auth:users.reset.revokeConfirm', { name: row.user.name })}
            onClick={() => revoke(row)}
          />
        </RowActions>
      ),
    },
  ]

  return (
    <>
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
      ) : null}
      <DataTable<ResetRow>
        rowKey="id"
        columns={columns}
        dataSource={rows}
        loading={loading}
        pagination={false}
        scroll={{ x: 760 }}
        empty={<EmptyState title={t('common:empty')} />}
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

      <Modal
        open={revealed !== null}
        title={t('auth:users.reset.revealedTitle')}
        footer={
          <Button onClick={() => setRevealed(null)}>{t('common:actions.close')}</Button>
        }
        onCancel={() => setRevealed(null)}
      >
        <p>{t('auth:users.reset.revealedNote')}</p>
        <Input
          className="kv-num"
          readOnly
          value={revealed ?? ''}
          aria-label={t('auth:users.reset.revealedTitle')}
        />
        <Button
          style={{ marginTop: 12 }}
          onClick={() => {
            void navigator.clipboard
              .writeText(revealed ?? '')
              .then(() => message.success(t('common:actions.copied')))
          }}
        >
          {t('common:actions.copy')}
        </Button>
      </Modal>
    </>
  )
}
