/**
 * Session status chip — Draft / Opened / Finished ONLY (spec F-08; there is no
 * CANCELED state). Always renders the status text next to the colour
 * (DESIGN rule: never colour alone); pill radius per the pattern language.
 */
import { Tag } from 'antd'
import { useTranslation } from 'react-i18next'

export type SessionStatus = 'DRAFT' | 'OPENED' | 'FINISHED'

export interface StatusChipProps {
  status: SessionStatus
}

const STATUS_CLASS: Record<SessionStatus, string> = {
  DRAFT: 'kv-status kv-status--draft',
  OPENED: 'kv-status kv-status--opened',
  FINISHED: 'kv-status kv-status--finished',
}

export default function StatusChip({ status }: StatusChipProps) {
  const { t } = useTranslation('common')
  return <Tag className={STATUS_CLASS[status]}>{t(`status.${status.toLowerCase()}`)}</Tag>
}
