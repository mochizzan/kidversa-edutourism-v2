/**
 * House data table (table rules): one place that decides how every list
 * behaves, so alignment, borders, sticky headers and system states never
 * drift page to page.
 *
 * - First load → skeleton rows shaped like the real columns (no centre spinner).
 * - Refetch with rows on screen → rows stay, a 2px indeterminate bar rides the
 *   top edge. Nothing jumps.
 * - Zero rows → `EmptyState` with an explanation and, where the user can act,
 *   the action that creates the first row.
 * - Header sticks to the viewport while the page scrolls; on the compact shell
 *   it offsets below the 56px mobile bar.
 * - Long cells truncate with a native title tooltip (`ellipsis` on the column).
 */
import { useEffect, useState, type CSSProperties, type ReactNode } from 'react'
import { Table } from 'antd'
import type { TableProps } from 'antd'
import { useTranslation } from 'react-i18next'
import EmptyState from './EmptyState'
import './data-table.css'

export interface DataTableProps<Row extends object> extends Omit<TableProps<Row>, 'loading'> {
  /** First load is a skeleton; later loads keep the rows and show a top bar. */
  loading?: boolean
  /** Shown when the load finished with zero rows. */
  empty?: ReactNode
  /** Skeleton row count on first load. */
  skeletonRows?: number
}

const COMPACT_QUERY = '(max-width: 1024px)'
const MOBILE_BAR_HEIGHT = 56

/** Sticky header offset: clear the mobile bar when the rail becomes a drawer. */
function useStickyOffset(): number {
  const [compact, setCompact] = useState(false)

  useEffect(() => {
    const query = window.matchMedia(COMPACT_QUERY)
    const sync = () => setCompact(query.matches)
    sync()
    query.addEventListener('change', sync)
    return () => query.removeEventListener('change', sync)
  }, [])

  return compact ? MOBILE_BAR_HEIGHT : 0
}

function SkeletonTable<Row extends object>({
  columns,
  rows,
  label,
}: {
  columns?: TableProps<Row>['columns']
  rows: number
  label: string
}) {
  const headings = columns && columns.length > 0 ? columns : null
  const count = headings?.length ?? 5
  // Cells copy the real column's width and alignment so the placeholder reads
  // as the same table, not a generic bar stack.
  const cellStyle = (index: number): CSSProperties | undefined => {
    const column = headings?.[index]
    if (!column) return undefined
    const style: CSSProperties = {}
    if (typeof column.width === 'number') style.flex = `0 0 ${column.width}px`
    if (column.align === 'right') {
      style.display = 'flex'
      style.justifyContent = 'flex-end'
    }
    return style
  }
  return (
    <div className="kv-table-skeleton" role="status" aria-live="polite" aria-busy="true">
      <span className="kv-sr-only">{label}</span>
      <div className="kv-table-skeleton__row kv-table-skeleton__row--head" aria-hidden="true">
        {Array.from({ length: count }, (_, index) => (
          <span className="kv-table-skeleton__cell" key={`head-${index}`} style={cellStyle(index)}>
            {headings?.[index]?.title as ReactNode}
          </span>
        ))}
      </div>
      {Array.from({ length: rows }, (_, rowIndex) => (
        <div className="kv-table-skeleton__row" key={`row-${rowIndex}`} aria-hidden="true">
          {Array.from({ length: count }, (_, index) => (
            <span
              className="kv-table-skeleton__cell"
              key={`cell-${rowIndex}-${index}`}
              style={cellStyle(index)}
            >
              <span
                className="kv-skel"
                style={{ width: `${[92, 66, 78, 54, 84, 72][(rowIndex + index) % 6]}%` }}
              />
            </span>
          ))}
        </div>
      ))}
    </div>
  )
}

export default function DataTable<Row extends object>({
  loading = false,
  empty,
  skeletonRows = 6,
  columns,
  dataSource,
  locale,
  sticky = true,
  size = 'small',
  ...rest
}: DataTableProps<Row>) {
  const { t } = useTranslation('common')
  const offsetHeader = useStickyOffset()
  const rows = dataSource ?? []
  const booting = loading && rows.length === 0

  if (booting) {
    return <SkeletonTable<Row> columns={columns} rows={skeletonRows} label={t('actions.loading')} />
  }

  return (
    <div className="kv-table">
      {loading ? (
        <span className="kv-table__refetch" role="status" aria-label={t('actions.loading')} />
      ) : null}
      <Table<Row>
        size={size}
        columns={columns}
        dataSource={rows}
        sticky={sticky === true ? { offsetHeader } : sticky}
        locale={{
          emptyText: empty ?? <EmptyState title={t('empty')} />,
          ...locale,
        }}
        {...rest}
      />
    </div>
  )
}
