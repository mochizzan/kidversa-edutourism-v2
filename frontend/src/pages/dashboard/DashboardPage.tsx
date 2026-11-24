/**
 * Dashboard (F-13 / FL-14) — SA/ADMIN only (RequireRole in admin routes).
 *
 * Purely event-driven + mount: `stats.changed` → refetch both halves (the
 * gate debounces/rev-gates it). Overview is period-independent (mount + shared
 * signals only); switching the period refetches analytics alone. There is NO
 * interval polling and NO focus/staleness refetch anywhere on this page —
 * refresh happens only on signal (05§6).
 *
 * SA extras (keputusan #29): the per-school summary table
 * (`GET /api/dashboard/school-summary`) — SA-only, mounted once and refetched
 * on the same `stats.changed` signal; ADMIN never fetches nor sees it.
 */
import { useCallback, useEffect, useState } from 'react'
import { Alert, Badge, Button } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { registerRefetch } from '../../lib/stream'
import { useAuth } from '../../lib/auth'
import PageHeader from '../../components/PageHeader'
import WaStatusBadge from '../../components/WaStatusBadge'
import OverviewCards, { type OverviewData } from './OverviewCards'
import AnalyticsCharts, { type AnalyticsData } from './AnalyticsCharts'
import DashboardSkeleton from './DashboardSkeleton'
import SchoolSummaryTable, { type SchoolSummaryData } from './SchoolSummaryTable'
import './dashboard.css'

export type Period = '7d' | '30d' | 'all'

export default function DashboardPage() {
  const { t } = useTranslation(['auth', 'common'])
  const { user } = useAuth()
  const isSA = user?.role === 'SA'
  const [period, setPeriod] = useState<Period>('7d')
  const [overview, setOverview] = useState<OverviewData | null>(null)
  const [analytics, setAnalytics] = useState<AnalyticsData | null>(null)
  const [schoolSummary, setSchoolSummary] = useState<SchoolSummaryData | null>(null)
  // Independent failures: one fetch may succeed after its sibling failed, so
  // each fetch owns its error — success clears only its own.
  const [overviewError, setOverviewError] = useState<string | null>(null)
  const [analyticsError, setAnalyticsError] = useState<string | null>(null)
  const [schoolSummaryError, setSchoolSummaryError] = useState<string | null>(null)

  const loadOverview = useCallback(() => {
    api.get<OverviewData>('/api/dashboard/overview').then(
      (data) => {
        setOverview(data)
        setOverviewError(null)
      },
      (cause: unknown) => setOverviewError(errorCode(cause)),
    )
  }, [])

  const loadAnalytics = useCallback(() => {
    api.get<AnalyticsData>('/api/dashboard/analytics', { period }).then(
      (data) => {
        setAnalytics(data)
        setAnalyticsError(null)
      },
      (cause: unknown) => setAnalyticsError(errorCode(cause)),
    )
  }, [period])

  const loadSchoolSummary = useCallback(() => {
    api.get<SchoolSummaryData>('/api/dashboard/school-summary').then(
      (data) => {
        setSchoolSummary(data)
        setSchoolSummaryError(null)
      },
      (cause: unknown) => setSchoolSummaryError(errorCode(cause)),
    )
  }, [])

  // Overview is period-independent: mount only (loadOverview is stable).
  useEffect(() => {
    loadOverview()
  }, [loadOverview])

  // Analytics refetches per period; the listeners rebind with it so every
  // refetch carries the ACTIVE period.
  useEffect(() => {
    loadAnalytics()
    const unregister = registerRefetch('stats.changed', () => {
      loadOverview()
      loadAnalytics()
    })
    return () => {
      unregister()
    }
  }, [loadOverview, loadAnalytics])

  // SA-only per-school summary (keputusan #29): mount once auth is ready +
  // the SAME `stats.changed` signal (no new event, no polling); ADMIN never
  // fetches the endpoint (non-SA would only get FORBIDDEN_ROLE).
  useEffect(() => {
    if (!isSA) return
    loadSchoolSummary()
    return registerRefetch('stats.changed', loadSchoolSummary)
  }, [isSA, loadSchoolSummary])

  // Renderable only when both halves are present for the ACTIVE period; a
  // period switch refetches analytics alone and falls back to Spin meanwhile.
  const current =
    overview !== null && analytics !== null && analytics.period === period
      ? { overview, analytics }
      : null
  const error = overviewError ?? analyticsError ?? schoolSummaryError
  const pending = current?.overview.pending ?? 0
  const retry = (
    <Button
      size="small"
      onClick={() => {
        setOverviewError(null)
        setAnalyticsError(null)
        setSchoolSummaryError(null)
        loadOverview()
        loadAnalytics()
        if (isSA) loadSchoolSummary()
      }}
    >
      {t('common:actions.retry')}
    </Button>
  )

  return (
    <>
      <PageHeader
        title={t('auth:dash.title')}
        description={t('auth:dash.subtitle')}
        actions={
          <>
            {pending > 0 ? (
              <Link className="kv-dash__pending" to="/users?decision=PENDING">
                <Badge count={pending} size="small" />
                <span>{t('auth:dash.seePending')}</span>
              </Link>
            ) : null}
            <WaStatusBadge />
          </>
        }
      />
      <div className="kv-dash">
        {error && current ? (
          <Alert
            className="kv-dash__error"
            type="warning"
            showIcon
            message={t(`common:errors.${error}`)}
            action={retry}
          />
        ) : null}
        {current ? (
          <>
            <OverviewCards data={current.overview} />
            <AnalyticsCharts
              data={current.analytics}
              period={period}
              onPeriodChange={(next) => {
                setAnalyticsError(null)
                setPeriod(next)
              }}
            />
            {isSA && schoolSummary !== null ? (
              <SchoolSummaryTable data={schoolSummary} />
            ) : null}
          </>
        ) : error ? (
          <Alert type="error" showIcon message={t(`common:errors.${error}`)} action={retry} />
        ) : (
          <DashboardSkeleton />
        )}
      </div>
    </>
  )
}
