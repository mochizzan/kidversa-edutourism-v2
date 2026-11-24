/** `/reports` index (menu matrix target): pick a session → its reports matrix. */
import { useTranslation } from 'react-i18next'
import SessionPickTable from '../sessions/SessionPickTable'

export default function ReportsIndexPage() {
  const { t } = useTranslation('session')
  return (
    <SessionPickTable
      title={t('reports.title')}
      description={t('reports.indexTitle')}
      to={(sessionId) => `/sessions/${sessionId}/reports`}
    />
  )
}
