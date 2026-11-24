/**
 * Public links manager (F-12 / FL-13): list `GET P/public-links` (owned by
 * the page — SSE `report-link.changed` refetches), create `POST
 * P/public-links {kind}` which returns BOTH urls, per-row copy, revoke
 * (confirm) and pair-wide regenerate (confirm; old tokens die in one Tx).
 */
import { useState } from 'react'
import { Alert, App as AntApp, Button, Card, Empty, Flex, List, Space, Typography } from 'antd'
import { CopyOutlined, LinkOutlined, ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import ConfirmDialog from '../../components/ConfirmDialog'
import StatusPill from '../../components/StatusPill'
import { api, errorCode } from '../../lib/api'
import { copyText, type PublicLink } from '../../lib/session-contracts'

export interface LinksPanelProps {
  prefix: string
  links: PublicLink[]
  onChanged: () => void
}

export default function LinksPanel({ prefix, links, onChanged }: LinksPanelProps) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [confirm, setConfirm] = useState<{ mode: 'revoke' | 'regenerate'; token: string } | null>(
    null,
  )

  const run = async (action: () => Promise<unknown>, doneMessage?: string) => {
    setBusy(true)
    setError(null)
    try {
      await action()
      onChanged()
      if (doneMessage) message.success(doneMessage)
      setConfirm(null)
    } catch (failure: unknown) {
      setError(failure)
    } finally {
      setBusy(false)
    }
  }

  const copyUrl = (url: string) => {
    void copyText(url).then((ok) =>
      ok ? message.success(tc('actions.copied')) : message.error(tc('errors.INTERNAL')),
    )
  }

  const firstToken = links[0]?.token ?? ''

  return (
    <Card style={{ marginBottom: 16 }}>
      <Flex justify="space-between" align="flex-start" gap="middle" wrap style={{ marginBottom: 8 }}>
        <Typography.Title level={2} style={{ margin: 0 }}>
          {t('reports.links')}
        </Typography.Title>
        <Space size={4}>
          {links.length === 0 ? (
            <Button
              size="small"
              type="primary"
              icon={<LinkOutlined />}
              loading={busy}
              onClick={() => void run(() => api.post(`${prefix}/public-links`, { kind: 'REPORT' }), t('reports.linkCreated'))}
            >
              {t('reports.linksCreate')}
            </Button>
          ) : (
            <Button
              size="small"
              icon={<ReloadOutlined />}
              loading={busy}
              onClick={() => setConfirm({ mode: 'regenerate', token: firstToken })}
            >
              {t('reports.regenerate')}
            </Button>
          )}
        </Space>
      </Flex>

      {links.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t('reports.linksEmpty')} style={{ margin: '8px 0' }} />
      ) : (
        <List
          size="small"
          dataSource={links}
          split
          renderItem={(link) => (
            <List.Item
              key={link.token}
              actions={[
                <Button
                  key="copy"
                  size="small"
                  icon={<CopyOutlined />}
                  onClick={() => copyUrl(link.url)}
                >
                  {tc('actions.copy')}
                </Button>,
                <Button
                  key="revoke"
                  size="small"
                  danger
                  onClick={() => setConfirm({ mode: 'revoke', token: link.token })}
                >
                  {t('reports.revoke')}
                </Button>,
              ]}
            >
              <Space wrap>
                <StatusPill tone={link.kind === 'REPORT' ? 'info' : 'neutral'}>
                  {link.kind}
                </StatusPill>
                <Typography.Text type="secondary" style={{ minWidth: 200, wordBreak: 'break-all' }}>
                  {link.url}
                </Typography.Text>
              </Space>
            </List.Item>
          )}
        />
      )}

      {error ? (
        <Alert
          style={{ marginTop: 8 }}
          type="error"
          showIcon
          message={tc(`errors.${errorCode(error)}`)}
        />
      ) : null}

      <ConfirmDialog
        open={confirm?.mode === 'revoke'}
        title={t('reports.revokeTitle')}
        danger
        loading={busy}
        confirmLabel={t('reports.revoke')}
        cancelLabel={tc('actions.cancel')}
        onCancel={() => setConfirm(null)}
        onConfirm={() =>
          confirm
            ? void run(
                () => api.post(`/api/public-links/${confirm.token}/revoke`),
                t('reports.revokeDone'),
              )
            : undefined
        }
      >
        <p>{t('reports.revokeWarn')}</p>
        {error ? (
          <Alert
            type="error"
            showIcon
            message={tc(`errors.${errorCode(error)}`)}
          />
        ) : null}
      </ConfirmDialog>

      <ConfirmDialog
        open={confirm?.mode === 'regenerate'}
        title={t('reports.regenerateTitle')}
        loading={busy}
        confirmLabel={t('reports.regenerate')}
        cancelLabel={tc('actions.cancel')}
        onCancel={() => setConfirm(null)}
        onConfirm={() =>
          confirm
            ? void run(
                () => api.post(`/api/public-links/${confirm.token}/regenerate`),
                t('reports.linkCreated'),
              )
            : undefined
        }
      >
        <p>{t('reports.regenerateWarn')}</p>
        {error ? (
          <Alert
            type="error"
            showIcon
            message={tc(`errors.${errorCode(error)}`)}
          />
        ) : null}
      </ConfirmDialog>
    </Card>
  )
}
