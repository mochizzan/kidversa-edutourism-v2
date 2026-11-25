/**
 * Inline session lifecycle actions for the list. Every row renders Edit, Open,
 * Close and Delete as icon buttons, matching every other table in the app; the
 * tooltip names the action and a status/race gate turns a button off with a
 * reason instead of removing it.
 *
 * - Edit: draft only (the PATCH is locked after opening).
 * - Open: draft or finished; confirms first because opening locks the session
 *   surface. A finished session may be opened again through the same gates.
 * - Close: opened only; confirms first because closing makes it read-only.
 * - Delete: draft/finished; an open session must be closed first.
 * - Delete sits behind a hairline because its consequence is a wipe.
 */
import {
  DeleteOutlined,
  EditOutlined,
  PlayCircleOutlined,
  StopOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { RowAction, RowActions, RowActionsDivider } from '../../components/RowActions'
import type { SessionStatus } from '../../components/StatusChip'

export interface SessionRowActionsProps {
  status: SessionStatus
  opening: boolean
  closing: boolean
  deleting: boolean
  onEdit: () => void
  onOpen: () => void
  onClose: () => void
  onDelete: () => void
}

export default function SessionRowActions({
  status,
  opening,
  closing,
  deleting,
  onEdit,
  onOpen,
  onClose,
  onDelete,
}: SessionRowActionsProps) {
  const { t } = useTranslation('session')
  const busy = opening || closing || deleting

  const editReason =
    status === 'OPENED'
      ? t('list.actionHints.editOpened')
      : status === 'FINISHED'
        ? t('list.actionHints.editFinished')
        : busy
          ? t('list.actionHints.busy')
          : undefined
  const openReason =
    status === 'OPENED'
      ? t('list.actionHints.openOpened')
      : busy && !opening
        ? t('list.actionHints.busy')
        : undefined
  const closeReason =
    status === 'DRAFT'
      ? t('list.actionHints.closeDraft')
      : status === 'FINISHED'
        ? t('list.actionHints.closeFinished')
        : busy && !closing
          ? t('list.actionHints.busy')
          : undefined
  const deleteReason =
    status === 'OPENED'
      ? t('list.actionHints.deleteOpened')
      : busy && !deleting
        ? t('list.actionHints.busy')
        : undefined

  return (
    <RowActions>
      <RowAction
        label={t('list.actions.edit')}
        icon={<EditOutlined />}
        disabled={editReason !== undefined}
        disabledReason={editReason}
        onClick={onEdit}
      />
      <RowAction
        accent={status !== 'OPENED'}
        label={t('list.actions.open')}
        icon={<PlayCircleOutlined />}
        disabled={openReason !== undefined}
        disabledReason={openReason}
        loading={opening}
        confirm={t('list.actions.openConfirmTitle')}
        confirmDescription={t('list.actions.openConfirmBody')}
        onClick={onOpen}
      />
      <RowAction
        accent={status === 'OPENED'}
        label={t('list.actions.close')}
        icon={<StopOutlined />}
        disabled={closeReason !== undefined}
        disabledReason={closeReason}
        loading={closing}
        confirm={t('list.actions.closeConfirmTitle')}
        confirmDescription={t('list.actions.closeConfirmBody')}
        onClick={onClose}
      />
      <RowActionsDivider />
      <RowAction
        danger
        label={t('list.actions.delete')}
        icon={<DeleteOutlined />}
        disabled={deleteReason !== undefined}
        disabledReason={deleteReason}
        loading={deleting}
        onClick={onDelete}
      />
    </RowActions>
  )
}
