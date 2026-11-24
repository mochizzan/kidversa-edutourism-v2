/**
 * Server-side paging controls (spec 02§3 decision 25): the list page holds
 * only the active server page; this component renders the 20/50/100/1000
 * page-size selector and the pager driven by `meta.total`. Changing filters
 * resets the page to 1 in the owning page; changing page size resets to 1 here.
 */
import { Pagination } from 'antd'
import { useTranslation } from 'react-i18next'

const PAGE_SIZES = [20, 50, 100, 1000]

export interface PagerProps {
  page: number
  perPage: number
  /** meta.total from the paged envelope. */
  total: number
  onPage: (page: number) => void
  onPerPage: (perPage: number) => void
}

export default function Pager({ page, perPage, total, onPage, onPerPage }: PagerProps) {
  const { t } = useTranslation('common')
  return (
    <Pagination
      className="kv-pager"
      current={page}
      pageSize={perPage}
      total={total}
      showSizeChanger
      pageSizeOptions={PAGE_SIZES.map(String)}
      showTotal={(count, range) =>
        t('pager.showing', { from: range[0], to: range[1], total: count })
      }
      onChange={(nextPage, nextSize) => {
        if (nextSize !== perPage) {
          onPerPage(nextSize)
          onPage(1)
        } else {
          onPage(nextPage)
        }
      }}
    />
  )
}
