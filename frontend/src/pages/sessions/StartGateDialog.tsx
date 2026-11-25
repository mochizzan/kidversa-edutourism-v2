/**
 * The list's Start action can fail the server's open gates. Instead of a
 * transient toast, this dialog keeps the offending groups on screen and sends
 * the user to the one place where they can be fixed (the session detail page).
 *
 * FINISHED sessions are special: their roster is frozen, so a zero/empty-group
 * failure cannot be repaired — the dialog says the only recovery is delete.
 */
import { Button, Modal } from 'antd'
import { useTranslation } from 'react-i18next'
import { errorCode } from '../../lib/api'
import type { SessionStatus } from '../../lib/session-contracts'
import { gateGroupsOf } from './gates'

export interface StartGateDialogProps {
  sessionName: string
  status: SessionStatus
  failure: unknown
  onClose: () => void
  onReview: () => void
}

export default function StartGateDialog({
  sessionName,
  status,
  failure,
  onClose,
  onReview,
}: StartGateDialogProps) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const code = errorCode(failure)
  const groups = gateGroupsOf(failure)
  const names = groups.map((group) => group.name).join(', ')
  const finished = status === 'FINISHED'

  let reason: string
  if (code === 'VALIDATION') {
    reason =
      groups.length === 0
        ? t(finished ? 'list.gate.noGroupsFinished' : 'list.gate.noGroups')
        : t(finished ? 'list.gate.emptyGroupsFinished' : 'list.gate.emptyGroups')
  } else if (code === 'GROUP_WITHOUT_FACILITATOR') {
    reason =
      groups.length === 0
        ? tc('errors.GROUP_WITHOUT_FACILITATOR')
        : t('list.gate.ownerMissing')
  } else {
    reason = names || tc(`errors.${code}`)
  }

  const unrecoverable = finished && code === 'VALIDATION'

  return (
    <Modal
      open
      width={440}
      title={
        unrecoverable
          ? t('list.gate.titleFinished', { name: sessionName })
          : t('list.gate.title', { name: sessionName })
      }
      onCancel={onClose}
      footer={[
        <Button key="close" onClick={onClose}>
          {tc('actions.close')}
        </Button>,
        <Button key="review" type="primary" onClick={onReview}>
          {t('list.gate.review')}
        </Button>,
      ]}
    >
      <div className="kv-session-gate">
        <p className="kv-session-gate__reason">{reason}</p>
        {groups.length > 0 ? (
          <ul className="kv-session-gate__groups" aria-label={t('list.gate.groupsLabel')}>
            {groups.map((group, index) => (
              <li className="kv-session-gate__group" key={`${group.name}-${index}`}>
                {group.name}
              </li>
            ))}
          </ul>
        ) : null}
        <p className="kv-session-gate__hint">
          {unrecoverable ? t('list.gate.hintFinished') : t('list.gate.hint')}
        </p>
      </div>
    </Modal>
  )
}
