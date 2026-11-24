/**
 * Single report send (F-12 / FL-13): `POST P/send-report` — default is the
 * mother's registered WA number; a supplied `to` must match one of the
 * child's registered numbers (server → VALIDATION, shown inline). The 200
 * envelope carries `{status: SENT|FAILED, code?}`; WA_FAILED shows the
 * manual copy-URL fallback from the active public links.
 */
import { useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, Card, Select, Space, Typography } from 'antd'
import { SendOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { copyText, type PublicLink } from '../../lib/session-contracts'

interface ChildWa {
  mother_wa: string | null
  father_wa: string | null
}

export default function SendReportPanel({
  prefix,
  cid,
  links,
}: {
  prefix: string
  cid: number
  links: PublicLink[]
}) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const [child, setChild] = useState<ChildWa | null>(null)
  const [to, setTo] = useState('')
  const [busy, setBusy] = useState(false)
  const [failureCode, setFailureCode] = useState<string | null>(null)
  const [failed, setFailed] = useState(false)

  useEffect(() => {
    let alive = true
    api
      .get<ChildWa>(`/api/children/${cid}`)
      .then(
        (data) => {
          if (alive) setChild(data)
        },
        () => undefined, // fallback: default (mother) send works without the numbers
      )
    return () => {
      alive = false
    }
  }, [cid])

  const send = async () => {
    setBusy(true)
    setFailureCode(null)
    setFailed(false)
    try {
      const result = await api.post<{ status: string; code?: string }>(`${prefix}/send-report`, to
        ? { to }
        : undefined)
      if (result.status === 'SENT') {
        message.success(t('reports.sent'))
      } else {
        setFailed(true)
        setFailureCode(result.code ?? 'WA_FAILED')
      }
    } catch (failure: unknown) {
      setFailed(true)
      setFailureCode(errorCode(failure))
    } finally {
      setBusy(false)
    }
  }

  const copyUrl = (url: string) => {
    void copyText(url).then((ok) =>
      ok ? message.success(tc('actions.copied')) : message.error(tc('errors.INTERNAL')),
    )
  }
  const fallbackLinks = links.filter((link) => link.url)

  return (
    <Card style={{ marginBottom: 16 }}>
      <Typography.Title level={2} style={{ marginBottom: 8 }}>
        {t('reports.send')}
      </Typography.Title>
      <Space wrap>
        <Typography.Text type="secondary">{t('reports.sendTo')}</Typography.Text>
        <Select
          style={{ width: 220 }}
          value={to}
          onChange={setTo}
          options={[
            { value: '', label: t('reports.sendDefault') },
            ...(child?.father_wa
              ? [{ value: child.father_wa, label: `${t('reports.sendFather')} · ${child.father_wa}` }]
              : []),
          ]}
        />
        <Button type="primary" icon={<SendOutlined />} loading={busy} onClick={() => void send()}>
          {t('reports.send')}
        </Button>
      </Space>
      {failed && failureCode ? (
        <Alert
          style={{ marginTop: 8 }}
          type="error"
          showIcon
          message={tc(`errors.${failureCode}`)}
        />
      ) : null}
      {failed && fallbackLinks.length > 0 ? (
        <div style={{ marginTop: 8 }}>
          <Typography.Text type="secondary" style={{ display: 'block' }}>{t('reports.copyFallback')}</Typography.Text>
          <Space wrap>
            {fallbackLinks.map((link) => (
              <Button key={link.token} size="small" onClick={() => copyUrl(link.url)}>
                {link.kind}
              </Button>
            ))}
          </Space>
        </div>
      ) : null}
    </Card>
  )
}
