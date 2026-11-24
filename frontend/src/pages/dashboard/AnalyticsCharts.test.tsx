/**
 * AnalyticsCharts (F-13 split): the star distribution Column chart (mocked —
 * @ant-design/plots renders to canvas), topic rows (with the total=0 guard),
 * session completion and facilitator Progress rows, the Segmented period
 * switch and the per-list empty placeholders — rendered directly with a spy
 * onPeriodChange, no network involved.
 */
import { describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import AnalyticsCharts, { type AnalyticsData } from './AnalyticsCharts'
import { renderApp } from '../../test/render'

/* G2 canvas charts do not run under jsdom; the stub records the rows passed. */
vi.mock('@ant-design/plots', () => ({
  Column: (props: { data: unknown }) => (
    <div data-testid="star-chart" data-rows={JSON.stringify(props.data)} />
  ),
}))

const FULL: AnalyticsData = {
  period: '7d',
  star_distribution: [
    { stars: 1, count: 1 },
    { stars: 2, count: 2 },
    { stars: 3, count: 5 },
    { stars: 4, count: 8 },
  ],
  topics: [
    { topic_id: 3, name: 'Batik Nusantara', assessed: 4, total: 8, avg_stars: 3.5 },
    { topic_id: 9, name: 'Topik Kosong', assessed: 0, total: 0, avg_stars: 0 },
  ],
  sessions: [
    { session_id: 5, name: 'Kelas Batik', date: '2026-10-09', percent: 75 },
    { session_id: 6, name: 'Kelas Caping', date: '2026-10-10', percent: 100 },
  ],
  facilitators: [
    { id: 1, name: 'Dewi Lestari', assessments: 6 },
    { id: 2, name: 'Budi Santoso', assessments: 0 },
  ],
}

const EMPTY: AnalyticsData = {
  period: '7d',
  star_distribution: [],
  topics: [],
  sessions: [],
  facilitators: [],
}

/** Bar-row element containing the given label text. */
function barRow(label: string): HTMLElement {
  const row = screen.getByText(label).closest('.kv-bar-row')
  expect(row).not.toBeNull()
  return row as HTMLElement
}

describe('star distribution', () => {
  it('feeds every star row to the Column chart', () => {
    renderApp(<AnalyticsCharts data={FULL} period="7d" onPeriodChange={vi.fn()} />)

    expect(screen.getByTestId('star-chart')).toHaveAttribute(
      'data-rows',
      JSON.stringify(FULL.star_distribution),
    )
  })
})

describe('topic rows', () => {
  it('renders name, assessed/total and the average stars', () => {
    renderApp(<AnalyticsCharts data={FULL} period="7d" onPeriodChange={vi.fn()} />)

    const row = barRow('Batik Nusantara')
    expect(within(row).getByText('4/8')).toBeInTheDocument()
    expect(within(row).getByText('3.5')).toBeInTheDocument()
    expect(row.querySelector('.anticon-star')).not.toBeNull()
    expect(within(row).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '50')
  })

  it('renders 0/0 and a zero progress for a topic with total 0', () => {
    renderApp(<AnalyticsCharts data={FULL} period="7d" onPeriodChange={vi.fn()} />)

    const row = barRow('Topik Kosong')
    expect(within(row).getByText('0/0')).toBeInTheDocument()
    expect(within(row).getByText('0.0')).toBeInTheDocument()
    expect(within(row).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '0')
  })
})

describe('sessions and facilitators', () => {
  it('renders session percent rows including a 100% one', () => {
    renderApp(<AnalyticsCharts data={FULL} period="7d" onPeriodChange={vi.fn()} />)

    const partial = barRow('Kelas Batik')
    expect(within(partial).getByText('2026-10-09')).toBeInTheDocument()
    expect(within(partial).getByText('75%')).toBeInTheDocument()
    expect(within(partial).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '75')

    const complete = barRow('Kelas Caping')
    expect(within(complete).getByText('100%')).toBeInTheDocument()
    expect(within(complete).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '100')
  })

  it('renders facilitator activity counts with proportional progress', () => {
    renderApp(<AnalyticsCharts data={FULL} period="7d" onPeriodChange={vi.fn()} />)

    const active = barRow('Dewi Lestari')
    expect(within(active).getByText('6')).toBeInTheDocument()
    expect(within(active).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '100')

    const idle = barRow('Budi Santoso')
    expect(within(idle).getByText('0')).toBeInTheDocument()
    expect(within(idle).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '0')
  })
})

describe('period segmented control', () => {
  it('reports the clicked period through onPeriodChange', async () => {
    const onPeriodChange = vi.fn()
    renderApp(
      <AnalyticsCharts data={FULL} period="7d" onPeriodChange={onPeriodChange} />,
    )
    const user = userEvent.setup()

    await user.click(screen.getByText('Semua'))
    expect(onPeriodChange).toHaveBeenLastCalledWith('all')

    await user.click(screen.getByText('30 hari'))
    expect(onPeriodChange).toHaveBeenLastCalledWith('30d')
  })
})

describe('empty state', () => {
  it('renders an empty placeholder per empty list', () => {
    renderApp(<AnalyticsCharts data={EMPTY} period="7d" onPeriodChange={vi.fn()} />)

    // star distribution + topics + sessions + facilitators.
    expect(screen.getAllByText('Tidak ada data')).toHaveLength(4)
  })
})
