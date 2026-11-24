/**
 * Administrative password reset (design 2026-11-11 §10.6). Two ways to get a
 * value — generate (shown once, never persisted) or type one passing the shared
 * client policy — then one apply call. The password lives ONLY in component
 * state and is cleared whenever the modal closes or the target changes.
 */
import { useEffect, useRef, useState } from 'react'
import { Alert, App as AntApp, Button, Form, Input, Modal, Space, Tabs, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { checkPassword } from '../auth/validation'
import type { UserRow } from './UserModal'

export interface ResetPasswordModalProps {
  open: boolean
  target: UserRow | null
  onClose: () => void
  onDone: () => void
}

type TabKey = 'generate' | 'manual'

export default function ResetPasswordModal({
  open,
  target,
  onClose,
  onDone,
}: ResetPasswordModalProps) {
  const { t } = useTranslation(['auth', 'common'])
  const { notification } = AntApp.useApp()
  const [tab, setTab] = useState<TabKey>('generate')
  const [generated, setGenerated] = useState<string | null>(null)
  const [generating, setGenerating] = useState(false)
  const [copied, setCopied] = useState(false)
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [applying, setApplying] = useState(false)
  const copyTimer = useRef<number | null>(null)

  const name = target?.name ?? ''

  // A generated password is single-use: nothing survives a close or a new target.
  useEffect(() => {
    setTab('generate')
    setGenerated(null)
    setGenerating(false)
    setCopied(false)
    setPassword('')
    setConfirm('')
    setApplying(false)
    if (copyTimer.current !== null) {
      window.clearTimeout(copyTimer.current)
      copyTimer.current = null
    }
  }, [open, target])

  useEffect(
    () => () => {
      if (copyTimer.current !== null) window.clearTimeout(copyTimer.current)
    },
    [],
  )

  const generate = () => {
    if (target === null) return
    setGenerating(true)
    api.post<{ password: string }>(`/api/users/${target.id}/password/generate`).then(
      (data) => {
        setGenerating(false)
        setGenerated(data.password)
      },
      (cause: unknown) => {
        setGenerating(false)
        notification.error({ message: t(`common:errors.${errorCode(cause)}`) })
      },
    )
  }

  const copy = () => {
    // Clipboard needs a secure context; its absence is not worth an error toast.
    if (generated === null || !navigator.clipboard) return
    navigator.clipboard.writeText(generated).then(
      () => {
        setCopied(true)
        if (copyTimer.current !== null) window.clearTimeout(copyTimer.current)
        copyTimer.current = window.setTimeout(() => setCopied(false), 1500)
      },
      () => undefined,
    )
  }

  const apply = (value: string) => {
    if (target === null) return
    setApplying(true)
    api.post<{ reset: true }>(`/api/users/${target.id}/password`, { password: value }).then(
      () => {
        setApplying(false)
        notification.success({ message: t('auth:users.resetSuccess') })
        onDone()
        onClose()
      },
      (cause: unknown) => {
        setApplying(false)
        notification.error({ message: t(`common:errors.${errorCode(cause)}`) })
      },
    )
  }

  const passwordKey = checkPassword(password)
  const passwordError = password !== '' && passwordKey !== null ? t(`auth:${passwordKey}`) : null
  const mismatch = confirm !== '' && confirm !== password
  const manualValid = passwordKey === null && password === confirm

  const generatePanel = (
    <Space direction="vertical" size="middle" style={{ width: '100%' }}>
      {generated === null ? (
        <Button type="primary" loading={generating} onClick={generate}>
          {t('auth:users.resetGenerateButton')}
        </Button>
      ) : (
        <>
          <div>
            <Typography.Text type="secondary">
              {t('auth:users.resetGeneratedLabel')}
            </Typography.Text>
            <Space.Compact style={{ width: '100%', marginTop: 4 }}>
              <Input
                readOnly
                value={generated}
                aria-label={t('auth:users.resetGeneratedLabel')}
              />
              <Button onClick={copy}>
                {copied ? t('auth:users.resetCopied') : t('auth:users.resetCopy')}
              </Button>
            </Space.Compact>
          </div>
          <Alert
            type="warning"
            showIcon
            closable={false}
            message={t('auth:users.resetGeneratedWarning')}
          />
          <Button type="primary" loading={applying} onClick={() => apply(generated)}>
            {t('auth:users.resetUseGenerated')}
          </Button>
        </>
      )}
    </Space>
  )

  const manualPanel = (
    <Form layout="vertical" requiredMark={false} component="div">
      <Form.Item
        label={t('auth:users.resetManualPassword')}
        htmlFor="reset-manual-password"
        validateStatus={passwordError ? 'error' : undefined}
        help={passwordError ?? t('auth:users.resetPasswordHint')}
      >
        <Input.Password
          id="reset-manual-password"
          autoComplete="new-password"
          value={password}
          onChange={(event) => setPassword(event.target.value)}
        />
      </Form.Item>
      <Form.Item
        label={t('auth:users.resetManualConfirm')}
        htmlFor="reset-manual-confirm"
        validateStatus={mismatch ? 'error' : undefined}
        help={mismatch ? t('common:errors.VALIDATION') : undefined}
      >
        <Input.Password
          id="reset-manual-confirm"
          autoComplete="new-password"
          value={confirm}
          onChange={(event) => setConfirm(event.target.value)}
        />
      </Form.Item>
      <Button
        type="primary"
        disabled={!manualValid}
        loading={applying}
        onClick={() => apply(password)}
      >
        {t('common:actions.save')}
      </Button>
    </Form>
  )

  return (
    <Modal
      open={open}
      title={t('auth:users.resetPasswordTitle', { name })}
      onCancel={onClose}
      width={520}
      destroyOnHidden
      footer={<Button onClick={onClose}>{t('common:actions.cancel')}</Button>}
    >
      <Tabs
        activeKey={tab}
        onChange={(key) => setTab(key as TabKey)}
        items={[
          { key: 'generate', label: t('auth:users.resetGenerateTab'), children: generatePanel },
          { key: 'manual', label: t('auth:users.resetManualTab'), children: manualPanel },
        ]}
      />
    </Modal>
  )
}
