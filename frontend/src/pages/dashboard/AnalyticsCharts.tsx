/**
 * Analytics split (period-scoped): star distribution + topic progress on the
 * left, session completion + facilitator activity on the right. The star
 * distribution is an @ant-design/plots Column chart (decision 39); the row
 * lists are antd Progress bars — every row keeps its numeric label (never
 * colour alone).
 */
import { StarFilled } from '@ant-design/icons'
import { Column } from '@ant-design/plots'
import { Card, Progress, Segmented, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import EmptyState from '../../components/EmptyState'
import type { Period } from './DashboardPage'

export interface StarRow {
  stars: number
  count: number
}

export interface TopicRow {
  topic_id: number
  name: string
  assessed: number
  total: number
  avg_stars: number
}

export interface PeriodSessionRow {
  session_id: number
  name: string
  date: string
  percent: number
}

export interface FacilitatorRow {
  id: number
  name: string
  assessments: number
}

/** `GET /api/dashboard/analytics?period=` payload (contracts-shapes, pinned). */
export interface AnalyticsData {
  period: string
  star_distribution: StarRow[]
  topics: TopicRow[]
  sessions: PeriodSessionRow[]
  facilitators: FacilitatorRow[]
}

export interface AnalyticsChartsProps {
  data: AnalyticsData
  period: Period
  onPeriodChange: (period: Period) => void
}

export default function AnalyticsCharts({ data, period, onPeriodChange }: AnalyticsChartsProps) {
  const { t } = useTranslation(['auth', 'common'])
  const maxFacilitator = Math.max(1, ...data.facilitators.map((row) => row.assessments))
  const empty = <EmptyState title={t('common:empty')} />

  return (
    <section className="kv-dash__split">
      <Card
        title={
          <Typography.Title level={2} className="kv-card-title">
            {t('auth:dash.analyticsTitle')}
          </Typography.Title>
        }
        extra={
          <Segmented
            value={period}
            onChange={(value) => onPeriodChange(value as Period)}
            options={[
              { label: t('auth:dash.period.7d'), value: '7d' },
              { label: t('auth:dash.period.30d'), value: '30d' },
              { label: t('auth:dash.period.all'), value: 'all' },
            ]}
          />
        }
      >
        <h3 className="kv-dash__subtitle">{t('auth:dash.starDistribution')}</h3>
        {data.star_distribution.every((row) => row.count === 0) ? (
          empty
        ) : (
          <div className="kv-dash__chart">
            <Column
              data={data.star_distribution}
              xField="stars"
              yField="count"
              axis={{
                x: { labelFormatter: (value: number) => `${value} ★`, tick: false, line: false },
                y: {
                  label: false,
                  tick: false,
                  line: false,
                  // hairline guides only (no full grid, no axis chrome); values live in the tooltip
                  gridStroke: 'rgba(32, 32, 30, 0.08)',
                  gridStrokeOpacity: 1,
                  gridLineWidth: 1,
                  gridLineDash: [0, 0],
                },
              }}
              legend={false}
              animate={false}
              style={{ fill: '#8140DC', radiusTopLeft: 4, radiusTopRight: 4, maxWidth: 96 }}
            />
          </div>
        )}

        <h3 className="kv-dash__subtitle">{t('auth:dash.topics')}</h3>
        {data.topics.length === 0 ? (
          empty
        ) : (
          data.topics.map((topic) => {
            const percent = topic.total > 0 ? Math.round((topic.assessed / topic.total) * 100) : 0
            return (
              <div className="kv-bar-row" key={topic.topic_id}>
                <span className="kv-bar-row__label">{topic.name}</span>
                <Progress
                  percent={percent}
                  size="small"
                  strokeColor="#8140DC"
                  format={() => `${topic.assessed}/${topic.total}`}
                />
                <span className="kv-num kv-bar-row__avg">
                  <StarFilled /> {topic.avg_stars.toFixed(1)}
                </span>
              </div>
            )
          })
        )}
      </Card>

      <div className="kv-dash__side">
        <Card
          title={
            <Typography.Title level={2} className="kv-card-title">
              {t('auth:dash.sessions')}
            </Typography.Title>
          }
        >
          {data.sessions.length === 0 ? (
            empty
          ) : (
            data.sessions.map((row) => (
              <div className="kv-bar-row" key={row.session_id}>
                <span className="kv-bar-row__label">
                  <span className="kv-bar-row__name">{row.name}</span>
                  <Typography.Text type="secondary" className="kv-meta">
                    {row.date}
                  </Typography.Text>
                </span>
                <Progress
                  percent={row.percent}
                  size="small"
                  strokeColor="#8140DC"
                  format={() => `${row.percent}%`}
                />
              </div>
            ))
          )}
        </Card>

        <Card
          title={
            <Typography.Title level={2} className="kv-card-title">
              {t('auth:dash.facilitators')}
            </Typography.Title>
          }
        >
          {data.facilitators.length === 0 ? (
            empty
          ) : (
            data.facilitators.map((row) => (
              <div className="kv-bar-row" key={row.id}>
                <span className="kv-bar-row__label">{row.name}</span>
                <Progress
                  percent={Math.round((row.assessments / maxFacilitator) * 100)}
                  size="small"
                  strokeColor="#8140DC"
                  format={() => String(row.assessments)}
                />
              </div>
            ))
          )}
        </Card>
      </div>
    </section>
  )
}
