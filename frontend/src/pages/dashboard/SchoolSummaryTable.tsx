/**
 * SA-only per-school summary table (F-13, keputusan user #29): every row's
 * numbers belong to ONE school — NO cross-school mixed aggregate (08 AC).
 * Pure render: the dashboard owns the fetch, the `stats.changed` refetch and
 * the error/retry state, exactly like OverviewCards/AnalyticsCharts.
 */
import { Card, Typography } from 'antd'
import type { ColumnsType } from 'antd/es/table'
import { useTranslation } from 'react-i18next'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import StatusPill from '../../components/StatusPill'

/** `GET /api/dashboard/school-summary` row (contracts-shapes, pinned). */
export interface SchoolSummaryRow {
  school_id: number
  school_name: string
  is_active: boolean
  children: number
  sessions_open: number
  sessions_finished: number
  reports_issued: number
}

export interface SchoolSummaryData {
  schools: SchoolSummaryRow[]
}

export default function SchoolSummaryTable({ data }: { data: SchoolSummaryData }) {
  const { t } = useTranslation(['auth', 'common'])
  // antd treats a record's `children` field as nested rows (tree data); the
  // wire shape uses it as a numeric count — rename it for the table only.
  const rows = data.schools.map(({ children, ...rest }) => ({
    ...rest,
    children_count: children,
  }))
  const columns: ColumnsType<(typeof rows)[number]> = [
    {
      title: t('auth:schools.schoolName'),
      dataIndex: 'school_name',
      key: 'school_name',
      width: 220,
      ellipsis: true,
    },
    {
      title: t('auth:schools.status'),
      key: 'is_active',
      width: 96,
      render: (_value, row) => (
        <StatusPill tone={row.is_active ? 'positive' : 'neutral'}>
          {t(`common:status.${row.is_active ? 'active' : 'inactive'}`)}
        </StatusPill>
      ),
    },
    {
      title: t('auth:dash.kpi.children'),
      dataIndex: 'children_count',
      key: 'children_count',
      width: 110,
      align: 'right',
      className: 'kv-num',
    },
    {
      title: t('auth:dash.kpi.open'),
      dataIndex: 'sessions_open',
      key: 'sessions_open',
      width: 96,
      align: 'right',
      className: 'kv-num',
    },
    {
      title: t('auth:dash.kpi.finished'),
      dataIndex: 'sessions_finished',
      key: 'sessions_finished',
      width: 100,
      align: 'right',
      className: 'kv-num',
    },
    {
      title: t('auth:dash.kpi.reports'),
      dataIndex: 'reports_issued',
      key: 'reports_issued',
      width: 100,
      align: 'right',
      className: 'kv-num',
    },
  ]

  return (
    <Card
      className="kv-dash__schools"
      title={
        <Typography.Title level={2} className="kv-card-title">
          {t('auth:dash.schoolSummaryTitle', 'Ringkasan per Sekolah')}
        </Typography.Title>
      }
    >
      <DataTable<(typeof rows)[number]>
        rowKey="school_id"
        columns={columns}
        dataSource={rows}
        pagination={false}
        scroll={{ x: 722 }}
        empty={<EmptyState title={t('common:empty')} />}
      />
    </Card>
  )
}
