/**
 * Overview section: KPI row (icon → label → big number) + per-session
 * assessment progress list (DESIGN §2 anatomy, palette-mapped).
 */
import {
  CheckCircleOutlined,
  FileTextOutlined,
  PlayCircleOutlined,
  UserAddOutlined,
  UsergroupAddOutlined,
} from '@ant-design/icons'
import { Card, Flex, Progress, Statistic, Typography } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import EmptyState from '../../components/EmptyState'

export interface ProgressRow {
  session_id: number
  name: string
  date: string
  total: number
  assessed: number
  percent: number
}

/** `GET /api/dashboard/overview` payload (contracts-shapes, pinned). */
export interface OverviewData {
  children: number
  sessions_open: number
  sessions_finished: number
  reports_issued: number
  pending: number
  progress: ProgressRow[]
}

export default function OverviewCards({ data }: { data: OverviewData }) {
  const { t } = useTranslation(['auth', 'common'])
  const kpis = [
    { key: 'children', icon: <UsergroupAddOutlined />, value: data.children },
    { key: 'open', icon: <PlayCircleOutlined />, value: data.sessions_open },
    { key: 'finished', icon: <CheckCircleOutlined />, value: data.sessions_finished },
    { key: 'reports', icon: <FileTextOutlined />, value: data.reports_issued },
    { key: 'pending', icon: <UserAddOutlined />, value: data.pending, attention: data.pending > 0 },
  ]

  return (
    <>
      <section className="kv-dash__kpis">
        {kpis.map((kpi) => (
          <Card size="small" key={kpi.key}>
            <Flex align="center" gap={6} style={{ marginBottom: 6 }}>
              <Typography.Text type="secondary">{kpi.icon}</Typography.Text>
              <Typography.Text type="secondary">{t(`auth:dash.kpi.${kpi.key}`)}</Typography.Text>
            </Flex>
            <Flex align="baseline" gap={8}>
              <Statistic value={kpi.value} valueStyle={{ fontWeight: 700 }} />
              {'attention' in kpi && kpi.attention ? (
                <span className="kv-dash__attention">{t('auth:dash.needsReview')}</span>
              ) : null}
            </Flex>
          </Card>
        ))}
      </section>

      <Card
        className="kv-dash__progress"
        title={
          <Typography.Title level={2} className="kv-card-title">
            {t('auth:dash.progressTitle')}
          </Typography.Title>
        }
      >
        {data.progress.length === 0 ? (
          <EmptyState
            title={t('common:empty')}
            description={t('auth:dash.emptyProgress')}
            variant="card"
          />
        ) : (
          data.progress.map((row) => (
            <div className="kv-progress-row" key={row.session_id}>
              <div className="kv-progress-row__head">
                <Link to={`/sessions/${row.session_id}`}>{row.name}</Link>
                <Typography.Text type="secondary" className="kv-meta">
                  {row.date}
                </Typography.Text>
                <span className="kv-num">
                  {row.assessed}/{row.total}
                </span>
                <span className="kv-num kv-progress-row__percent">{row.percent}%</span>
              </div>
              <Progress percent={row.percent} size="small" showInfo={false} />
            </div>
          ))
        )}
      </Card>
    </>
  )
}
