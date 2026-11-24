/**
 * Shared confirm modal (spec 02§3): antd Modal with an optional TYPED-TEXT
 * variant used for destructive wipes (school hard-delete, child hard-delete,
 * session/group cascade wipe) — the caller passes the exact text the user
 * must type (e.g. the entity name or "HAPUS") to enable the confirm button.
 */
import { useState, type ReactNode } from 'react'
import { Input, Modal, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'

export interface ConfirmDialogProps {
  open: boolean
  title: string
  /** Body content — counts, consequences, links that will die with the wipe. */
  children?: ReactNode
  confirmLabel?: string
  cancelLabel?: string
  /** When set, the user must type this exact string to enable confirmation. */
  typedText?: string
  danger?: boolean
  loading?: boolean
  onConfirm: () => void
  onCancel: () => void
}

export default function ConfirmDialog({
  open,
  title,
  children,
  confirmLabel,
  cancelLabel,
  typedText,
  danger = false,
  loading = false,
  onConfirm,
  onCancel,
}: ConfirmDialogProps) {
  const { t } = useTranslation('common')
  const [typed, setTyped] = useState('')
  const [wasOpen, setWasOpen] = useState(open)
  if (wasOpen !== open) {
    // Adjust state during render (React: "you might not need an Effect"):
    // a freshly opened dialog must never carry the previous typed confirmation.
    setWasOpen(open)
    setTyped('')
  }

  const confirmDisabled = typedText !== undefined && typed !== typedText
  return (
    <Modal
      open={open}
      title={title}
      okText={confirmLabel ?? t('actions.confirm')}
      cancelText={cancelLabel ?? t('actions.cancel')}
      okButtonProps={{ danger, disabled: confirmDisabled, loading }}
      cancelButtonProps={{ disabled: loading }}
      maskClosable={!loading}
      onOk={onConfirm}
      onCancel={onCancel}
    >
      {children}
      {typedText !== undefined ? (
        <Space direction="vertical" size={8} style={{ width: '100%' }}>
          <Typography.Text>{t('confirm.typeToConfirm', { text: typedText })}</Typography.Text>
          <Input
            autoFocus
            value={typed}
            onChange={(event) => setTyped(event.target.value)}
            placeholder={typedText}
            disabled={loading}
          />
        </Space>
      ) : null}
    </Modal>
  )
}
