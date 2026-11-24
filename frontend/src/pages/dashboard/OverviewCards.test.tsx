/**
 * OverviewCards (F-13 anatomy): KPI row (antd Card + Statistic) with the
 * pending attention hint and per-session progress rows (link, assessed/total,
 * percent text, antd Progress value) — rendered directly, no network
 * involved.
 */
import { describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import OverviewCards, { type OverviewData, type ProgressRow } from './OverviewCards'
import { renderApp } from '../../test/render'

const ROWS: ProgressRow[] = [
  { session_id: 5, name: 'Kelas Batik', date: '2026-10-09', total: 10, assessed: 3, percent: 30 },
  { session_id: 6, name: 'Kelas Caping', date: '2026-10-10', total: 8, assessed: 8, percent: 100 },
]

function overviewOf(overrides: Partial<OverviewData>): OverviewData {
  return {
    children: 42,
    sessions_open: 3,
    sessions_finished: 7,
    reports_issued: 11,
    pending: 3,
    progress: ROWS,
    ...overrides,
  }
}

/** Progress row element containing the session link of the given name. */
function progressRow(name: string): HTMLElement {
  const row = screen.getByRole('link', { name }).closest('.kv-progress-row')
  expect(row).not.toBeNull()
  return row as HTMLElement
}

/** KPI card element containing the given label (cards have no a11y grouping). */
function kpiCard(label: string): HTMLElement {
  const card = screen.getByText(label).closest('.ant-card')
  expect(card).not.toBeNull()
  return card as HTMLElement
}

describe('kpi row', () => {
  it('renders every KPI label with its value', () => {
    renderApp(<OverviewCards data={overviewOf({})} />)

    expect(within(kpiCard('Anak terdaftar')).getByText('42')).toBeInTheDocument()
    expect(within(kpiCard('Sesi dibuka')).getByText('3')).toBeInTheDocument()
    expect(within(kpiCard('Sesi selesai')).getByText('7')).toBeInTheDocument()
    expect(within(kpiCard('Rapor terbit')).getByText('11')).toBeInTheDocument()
    expect(within(kpiCard('Menunggu persetujuan')).getByText('3')).toBeInTheDocument()
  })

  it('flags the pending KPI with the attention hint when pending > 0', () => {
    renderApp(<OverviewCards data={overviewOf({ pending: 3 })} />)

    expect(
      within(kpiCard('Menunggu persetujuan')).getByText('perlu ditinjau'),
    ).toBeInTheDocument()
  })

  it('omits the attention hint when pending is 0', () => {
    renderApp(<OverviewCards data={overviewOf({ pending: 0 })} />)

    expect(within(kpiCard('Menunggu persetujuan')).getByText('0')).toBeInTheDocument()
    expect(screen.queryByText('perlu ditinjau')).toBeNull()
  })
})

describe('progress list', () => {
  it('renders rows with session link, counts, percent and progress value', () => {
    renderApp(<OverviewCards data={overviewOf({})} />)

    const first = progressRow('Kelas Batik')
    expect(screen.getByRole('link', { name: 'Kelas Batik' })).toHaveAttribute(
      'href',
      '/sessions/5',
    )
    expect(within(first).getByText('3/10')).toBeInTheDocument()
    expect(within(first).getByText('30%')).toBeInTheDocument()
    expect(within(first).getByText('2026-10-09')).toBeInTheDocument()
    expect(within(first).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '30')

    const second = progressRow('Kelas Caping')
    expect(screen.getByRole('link', { name: 'Kelas Caping' })).toHaveAttribute(
      'href',
      '/sessions/6',
    )
    expect(within(second).getByText('8/8')).toBeInTheDocument()
    expect(within(second).getByText('100%')).toBeInTheDocument()
    expect(within(second).getByRole('progressbar')).toHaveAttribute('aria-valuenow', '100')
  })

  it('shows the empty placeholder when there are no progress rows', () => {
    renderApp(<OverviewCards data={overviewOf({ progress: [] })} />)

    expect(screen.getByText('Tidak ada data')).toBeInTheDocument()
  })
})
