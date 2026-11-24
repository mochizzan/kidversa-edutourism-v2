/** `/consent` index (menu matrix target): pick a session → its consent list. */
import { useTranslation } from 'react-i18next'
import SessionPickTable from '../sessions/SessionPickTable'

export default function ConsentIndexPage() {
  const { t } = useTranslation('session')
  return (
    <SessionPickTable
      title={t('consent.title')}
      description={t('consent.indexTitle')}
      to={(sessionId) => `/consent/${sessionId}`}
    />
  )
}
