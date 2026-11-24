/**
 * Dashboard first-load skeleton: five KPI plates, the progress card and the
 * analytics split, at the real heights. The layout is already final when the
 * numbers land, so nothing shifts.
 */
import { useTranslation } from 'react-i18next'

export default function DashboardSkeleton() {
  const { t } = useTranslation('common')

  return (
    <div className="kv-dash-skeleton" role="status" aria-live="polite" aria-busy="true">
      <span className="kv-sr-only">{t('actions.loading')}</span>

      <div className="kv-dash-skeleton__kpis" aria-hidden="true">
        {Array.from({ length: 5 }, (_, index) => (
          <div className="kv-dash-skeleton__kpi" key={index}>
            <span className="kv-skel" style={{ width: '58%', height: 9 }} />
            <span className="kv-skel" style={{ width: '32%', height: 20 }} />
          </div>
        ))}
      </div>

      <div className="kv-dash-skeleton__card" aria-hidden="true">
        <span className="kv-skel" style={{ width: 120, height: 11 }} />
        <div className="kv-dash-skeleton__rows">
          {Array.from({ length: 3 }, (_, index) => (
            <div className="kv-dash-skeleton__row" key={index}>
              <span className="kv-skel" style={{ width: 180, height: 10 }} />
              <span className="kv-skel" style={{ flex: 1, height: 8 }} />
              <span className="kv-skel" style={{ width: 40, height: 10 }} />
            </div>
          ))}
        </div>
      </div>

      <div className="kv-dash-skeleton__split" aria-hidden="true">
        <div className="kv-dash-skeleton__card">
          <span className="kv-skel" style={{ width: 140, height: 11 }} />
          <div className="kv-dash-skeleton__chart">
            <span className="kv-skel" style={{ width: '12%', height: '38%' }} />
            <span className="kv-skel" style={{ width: '12%', height: '62%' }} />
            <span className="kv-skel" style={{ width: '12%', height: '86%' }} />
            <span className="kv-skel" style={{ width: '12%', height: '54%' }} />
            <span className="kv-skel" style={{ width: '12%', height: '28%' }} />
          </div>
        </div>
        <div className="kv-dash-skeleton__side" aria-hidden="true">
          <div className="kv-dash-skeleton__card">
            <span className="kv-skel" style={{ width: 96, height: 11 }} />
            <div className="kv-dash-skeleton__rows">
              {Array.from({ length: 3 }, (_, index) => (
                <span className="kv-skel" key={index} style={{ height: 10 }} />
              ))}
            </div>
          </div>
          <div className="kv-dash-skeleton__card">
            <span className="kv-skel" style={{ width: 84, height: 11 }} />
            <div className="kv-dash-skeleton__rows">
              {Array.from({ length: 3 }, (_, index) => (
                <span className="kv-skel" key={index} style={{ height: 10 }} />
              ))}
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}
