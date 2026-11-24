/**
 * Session wipe dialog (F-08 / FL-08): typed-text ConfirmDialog (the user must
 * type the session name) with the scope counts. Counts come from the pinned
 * top-level `detail.scope` (contract amendment 2026-10-09); while B2 has not
 * landed it yet, the groups/members/topics line still derives from the detail
 * itself. Draft/Finished only (Opened never reaches here — the button is
 * hidden and the server answers SESSION_LOCKED).
 */
import { useState } from 'react'
import { Alert, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import ConfirmDialog from '../../components/ConfirmDialog'
import { api, errorCode } from '../../lib/api'
import type { SessionDetail } from '../../lib/session-contracts'

export interface DeleteSessionDialogProps {
  detail: SessionDetail
  open: boolean
  onCancel: () => void
  /** Session wiped — the owner navigates back to the list. */
  onDeleted: () => void
}

export default function DeleteSessionDialog({
  detail,
  open,
  onCancel,
  onDeleted,
}: DeleteSessionDialogProps) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const scope = detail.scope
  const groups = scope?.groups ?? detail.groups.length
  const members = scope?.members ?? detail.groups.reduce((sum, group) => sum + group.member_count, 0)
  const topics = scope?.topics ?? detail.topics.length

  const confirm = async () => {
    setBusy(true)
    setError(null)
    try {
      await api.del(`/api/sessions/${detail.session.id}`)
      onDeleted()
    } catch (failure: unknown) {
      setError(failure)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ConfirmDialog
      open={open}
      title={t('detail.deleteTitle', { name: detail.session.name })}
      typedText={detail.session.name}
      danger
      loading={busy}
      confirmLabel={tc('actions.delete')}
      onCancel={onCancel}
      onConfirm={confirm}
    >
      {scope ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="warning"
          showIcon
          message={t('detail.deleteCounts', {
            rapor: scope.reports_sent,
            foto: scope.photos,
            link: scope.links_report + scope.links_gallery + scope.links_consent,
          })}
        />
      ) : null}
      <p>{t('detail.deleteScope', { groups, members, topics })}</p>
      <Typography.Text type="secondary" style={{ display: 'block' }}>{tc('confirm.irreversible')}</Typography.Text>
      {error ? (
        <Alert
          type="error"
          showIcon
          message={tc(`errors.${errorCode(error)}`)}
        />
      ) : null}
    </ConfirmDialog>
  )
}
