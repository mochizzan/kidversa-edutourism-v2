/**
 * "Login as" confirmation (design 2026-11-11 §9.6). Impersonation is a HARD
 * session switch: the server revokes the actor's refresh row and mints the
 * target's, so there is no return path — the blocking warning is the only
 * mitigation. A failure keeps the modal open (HTTP-200 envelope → ApiError
 * code → `common:errors.<CODE>`).
 */
import { useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, Descriptions, Modal } from 'antd'
import { useTranslation } from 'react-i18next'
import { useNavigate } from 'react-router'
import { api, errorCode } from '../../lib/api'
import { useAuth, type LoginData } from '../../lib/auth'
import type { UserRow } from './UserModal'

export interface ImpersonationModalProps {
  open: boolean
  target: UserRow | null
  onClose: () => void
}

export default function ImpersonationModal({ open, target, onClose }: ImpersonationModalProps) {
  const { t } = useTranslation(['auth', 'common'])
  const { notification } = AntApp.useApp()
  const { setSession } = useAuth()
  const navigate = useNavigate()
  const [loading, setLoading] = useState(false)

  const name = target?.name ?? ''

  // A fresh dialog is always idle — no spinner left over from a previous row.
  useEffect(() => {
    setLoading(false)
  }, [open, target])

  const confirm = () => {
    if (target === null) return
    setLoading(true)
    api.post<LoginData>(`/api/users/${target.id}/impersonate`).then(
      (data) => {
        // The whole session is replaced: adopt the target identity and land on
        // the route their role can actually use. Deliberately NO list reload —
        // the page unmounts when navigation runs.
        setSession(data)
        navigate(data.user.role === 'FASILITATOR' ? '/sessions' : '/')
        setLoading(false)
        onClose()
      },
      (cause: unknown) => {
        setLoading(false)
        notification.error({ message: t(`common:errors.${errorCode(cause)}`) })
      },
    )
  }

  return (
    <Modal
      open={open}
      title={t('auth:users.loginAsTitle', { name })}
      onCancel={onClose}
      maskClosable={!loading}
      footer={[
        <Button key="cancel" disabled={loading} onClick={onClose}>
          {t('common:actions.cancel')}
        </Button>,
        <Button key="confirm" type="primary" loading={loading} onClick={confirm}>
          {t('auth:users.loginAsConfirm', { name })}
        </Button>,
      ]}
    >
      <Descriptions
        column={1}
        size="small"
        items={[
          { key: 'name', label: t('auth:users.name'), children: target?.name ?? '—' },
          { key: 'email', label: t('auth:users.email'), children: target?.email ?? '—' },
          {
            key: 'role',
            label: t('auth:users.role'),
            children: target ? t(`common:role.${target.role}`) : '—',
          },
          {
            key: 'school',
            label: t('auth:users.school'),
            children: target?.school_name ?? '—',
          },
        ]}
      />
      <Alert
        style={{ marginTop: 16 }}
        type="warning"
        showIcon
        closable={false}
        message={t('auth:users.loginAsWarning', { name })}
      />
    </Modal>
  )
}
