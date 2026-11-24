/**
 * The one manual refresh affordance for list pages.
 *
 * SSE keeps every list current, but a dropped stream must never leave the user
 * with no way to pull fresh rows. Sessions had this button and the other lists
 * did not; here it is once, with the same label, icon and position (left of the
 * page's primary action) everywhere.
 */
import { Button } from 'antd'
import { ReloadOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'

export interface RefreshButtonProps {
  onRefresh: () => void
  /** Reuses the button's spinner while the list is refetching. */
  loading?: boolean
}

export default function RefreshButton({ onRefresh, loading = false }: RefreshButtonProps) {
  const { t } = useTranslation('common')
  return (
    <Button icon={<ReloadOutlined />} loading={loading} onClick={onRefresh}>
      {t('actions.refresh')}
    </Button>
  )
}
