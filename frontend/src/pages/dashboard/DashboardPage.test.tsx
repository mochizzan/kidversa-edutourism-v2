/**
 * Dashboard page (F-13 / FL-14): boot load fires overview + analytics with the
 * requested period, pending-link gating, error alerts with retry, period
 * switching (layout skeleton while the new period is in flight), and the SSE
 * `stats.changed` debounced refetch including the warning branch (refetch
 * failed while data is still current). No focus/staleness refetch exists
 * anymore — refresh is signal-only (B5).
 *
 * SA-only per-school summary (keputusan #29): `GET /api/dashboard/school-summary`
 * mounts once for SA and refetches on the same `stats.changed`; ADMIN never
 * fetches nor renders it.
 *
 * SSE test pattern copied from pages/sessions/sessions.integration.test.tsx:
 * only the debounce clock is faked (MSW/undici need their real macrotasks).
 */
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import StreamGate from '../../components/StreamGate'
import DashboardPage from './DashboardPage'
import type { OverviewData } from './OverviewCards'
import type { AnalyticsData } from './AnalyticsCharts'
import type { SchoolSummaryData } from './SchoolSummaryTable'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, makeUser, ok, sseChannel } from '../../test/handlers'

/** antd mirrors the header for sticky tables; assert against the real table. */
const mainTable = () =>
  screen.getAllByRole('table').find((table) => !table.closest('.ant-table-sticky-holder'))!

/* G2 canvas charts do not run under jsdom; the stub records the rows passed. */
vi.mock('@ant-design/plots', () => ({
  Column: (props: { data: unknown }) => (
    <div data-testid="star-chart" data-rows={JSON.stringify(props.data)} />
  ),
}))

/* ---------- fixtures ---------- */

const OVERVIEW: OverviewData = {
  children: 42,
  sessions_open: 3,
  sessions_finished: 7,
  reports_issued: 11,
  pending: 3,
  progress: [
    { session_id: 5, name: 'Kelas Batik', date: '2026-10-09', total: 10, assessed: 3, percent: 30 },
  ],
}

const EMPTY_OVERVIEW: OverviewData = {
  children: 0,
  sessions_open: 0,
  sessions_finished: 0,
  reports_issued: 0,
  pending: 0,
  progress: [],
}

const ANALYTICS: AnalyticsData = {
  period: '7d',
  star_distribution: [
    { stars: 1, count: 1 },
    { stars: 2, count: 2 },
    { stars: 3, count: 5 },
    { stars: 4, count: 8 },
  ],
  topics: [{ topic_id: 3, name: 'Batik Nusantara', assessed: 4, total: 8, avg_stars: 3.5 }],
  sessions: [{ session_id: 7, name: 'Kelas Caping', date: '2026-10-05', percent: 75 }],
  facilitators: [{ id: 1, name: 'Dewi Lestari', assessments: 6 }],
}

const EMPTY_ANALYTICS: AnalyticsData = {
  period: '7d',
  star_distribution: [],
  topics: [],
  sessions: [],
  facilitators: [],
}

const ANALYTICS_30D: AnalyticsData = {
  period: '30d',
  star_distribution: [{ stars: 4, count: 12 }],
  topics: [{ topic_id: 3, name: 'Topik 30 Hari', assessed: 6, total: 10, avg_stars: 3.2 }],
  sessions: [],
  facilitators: [],
}

const ANALYTICS_ALL: AnalyticsData = {
  period: 'all',
  star_distribution: [{ stars: 4, count: 20 }],
  topics: [{ topic_id: 3, name: 'Sepanjang Masa', assessed: 20, total: 20, avg_stars: 4 }],
  sessions: [],
  facilitators: [],
}

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })
const SA_SESSION = () => makeLoginData({ user: makeUser({ role: 'SA' }) })

const SCHOOL_SUMMARY: SchoolSummaryData = {
  schools: [
    {
      school_id: 1,
      school_name: 'Bandung',
      is_active: true,
      children: 120,
      sessions_open: 3,
      sessions_finished: 15,
      reports_issued: 21,
    },
    {
      school_id: 2,
      school_name: 'Jakarta',
      is_active: false,
      children: 40,
      sessions_open: 0,
      sessions_finished: 6,
      reports_issued: 9,
    },
  ],
}

const EMPTY_SCHOOL_SUMMARY: SchoolSummaryData = { schools: [] }

/** KPI card element containing the given label (cards have no a11y grouping). */
function kpiCard(label: string): HTMLElement {
  const card = screen.getByText(label).closest('.ant-card')
  expect(card).not.toBeNull()
  return card as HTMLElement
}

/* ---------- fake-timer helpers (sessions.integration.test.tsx pattern) ---------- */

async function until(condition: () => boolean, step = 5, limit = 4000): Promise<void> {
  for (let i = 0; i < limit && !condition(); i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(step)
    })
  }
  expect(condition()).toBe(true)
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

/* ---------- tests ---------- */

describe('dashboard load', () => {
  it('renders KPIs, progress, analytics and requests analytics with period=7d', async () => {
    const seenOverview: URL[] = []
    const seenAnalytics: string[] = []
    server.use(
      http.get('/api/dashboard/overview', ({ request }) => {
        seenOverview.push(new URL(request.url))
        return ok(OVERVIEW)
      }),
      http.get('/api/dashboard/analytics', ({ request }) => {
        const url = new URL(request.url)
        seenAnalytics.push(url.searchParams.get('period') ?? '')
        return ok(ANALYTICS)
      }),
    )
    renderApp(<DashboardPage />, { session: ADMIN_SESSION() })

    await screen.findByText('Kelas Batik')

    // Requests: both endpoints hit, analytics carries the default period.
    expect(seenOverview).toHaveLength(1)
    expect(seenOverview[0].pathname).toBe('/api/dashboard/overview')
    expect(seenOverview[0].searchParams.get('period')).toBeNull()
    expect(seenAnalytics).toEqual(['7d'])

    // Header.
    expect(screen.getByRole('heading', { level: 1, name: 'Dasbor' })).toBeInTheDocument()

    // KPI values, each inside the card bearing its label.
    expect(within(kpiCard('Anak terdaftar')).getByText('42')).toBeInTheDocument()
    expect(within(kpiCard('Sesi dibuka')).getByText('3')).toBeInTheDocument()
    expect(within(kpiCard('Sesi selesai')).getByText('7')).toBeInTheDocument()
    expect(within(kpiCard('Rapor terbit')).getByText('11')).toBeInTheDocument()
    expect(within(kpiCard('Menunggu persetujuan')).getByText('3')).toBeInTheDocument()

    // Pending link (pending > 0) with the count in the badge.
    const pendingLink = screen.getByRole('link', { name: /menunggu persetujuan/ })
    expect(pendingLink).toHaveAttribute('href', '/users?decision=PENDING')
    expect(pendingLink.querySelector('.ant-badge-count')).toHaveTextContent('3')

    // Progress rows: link + counts + percent.
    const progressLink = screen.getByRole('link', { name: 'Kelas Batik' })
    expect(progressLink).toHaveAttribute('href', '/sessions/5')
    expect(screen.getByText('3/10')).toBeInTheDocument()
    expect(screen.getByText('30%')).toBeInTheDocument()

    // Analytics sections: star distribution reaches the Column chart; topic
    // row, session and facilitator keep their numeric labels.
    expect(screen.getByTestId('star-chart')).toHaveAttribute(
      'data-rows',
      JSON.stringify(ANALYTICS.star_distribution),
    )
    expect(screen.getByText('Batik Nusantara')).toBeInTheDocument()
    expect(screen.getByText('4/8')).toBeInTheDocument()
    expect(screen.getByText('3.5')).toBeInTheDocument()
    expect(screen.getByText('Kelas Caping')).toBeInTheDocument()
    expect(screen.getByText('75%')).toBeInTheDocument()
    expect(screen.getByText('Dewi Lestari')).toBeInTheDocument()
    expect(screen.getByText('6')).toBeInTheDocument()

    // WA badge: no stream event yet → "checking".
    expect(screen.getByText('Memeriksa…')).toBeInTheDocument()
  })

  it('renders an empty placeholder per empty list', async () => {
    server.use(
      http.get('/api/dashboard/overview', () => ok(EMPTY_OVERVIEW)),
      http.get('/api/dashboard/analytics', () => ok(EMPTY_ANALYTICS)),
    )
    renderApp(<DashboardPage />, { session: ADMIN_SESSION() })

    // progress + star chart + topics + sessions + facilitators.
    expect(await screen.findAllByText('Tidak ada data')).toHaveLength(5)
  })

  it('shows a translated error alert and recovers through the retry button', async () => {
    let failing = true
    server.use(
      http.get('/api/dashboard/overview', () => (failing ? fail('DB_UNAVAILABLE') : ok(OVERVIEW))),
      http.get('/api/dashboard/analytics', () => (failing ? fail('DB_UNAVAILABLE') : ok(ANALYTICS))),
    )
    const user = userEvent.setup()
    renderApp(<DashboardPage />, { session: ADMIN_SESSION() })

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Layanan sedang sibuk. Coba lagi.')
    expect(screen.getByRole('button', { name: 'Coba lagi' })).toBeInTheDocument()
    expect(screen.queryByText('Sebaran bintang')).toBeNull()

    failing = false
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))

    await screen.findByText('Batik Nusantara')
    expect(screen.getByText('Kelas Batik')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('links pending accounts only when pending > 0', async () => {
    server.use(
      http.get('/api/dashboard/overview', () => ok({ ...OVERVIEW, pending: 3 })),
      http.get('/api/dashboard/analytics', () => ok(ANALYTICS)),
    )
    renderApp(<DashboardPage />, { session: ADMIN_SESSION() })

    await screen.findByText('Kelas Batik')
    expect(screen.getByRole('link', { name: /menunggu persetujuan/ })).toHaveAttribute(
      'href',
      '/users?decision=PENDING',
    )
  })

  it('omits the pending link when pending is 0', async () => {
    server.use(
      http.get('/api/dashboard/overview', () => ok({ ...OVERVIEW, pending: 0 })),
      http.get('/api/dashboard/analytics', () => ok(ANALYTICS)),
    )
    renderApp(<DashboardPage />, { session: ADMIN_SESSION() })

    await screen.findByText('Kelas Batik')
    expect(screen.queryByRole('link', { name: /menunggu persetujuan/i })).toBeNull()
    expect(within(kpiCard('Menunggu persetujuan')).getByText('0')).toBeInTheDocument()
    expect(screen.queryByText('perlu ditinjau')).toBeNull()
  })
})

describe('period switching', () => {
  it('refetches analytics per period with a spin while the new period loads', async () => {
    const overviewCalls: URL[] = []
    const periods: string[] = []
    // Hold the 30d response until the test releases it.
    const gate = Promise.withResolvers<AnalyticsData>()
    server.use(
      http.get('/api/dashboard/overview', ({ request }) => {
        overviewCalls.push(new URL(request.url))
        return ok(OVERVIEW)
      }),
      http.get('/api/dashboard/analytics', async ({ request }) => {
        const period = new URL(request.url).searchParams.get('period') ?? '7d'
        periods.push(period)
        if (period === '30d') {
          return ok(await gate.promise)
        }
        if (period === 'all') return ok(ANALYTICS_ALL)
        return ok(ANALYTICS)
      }),
    )
    const user = userEvent.setup()
    renderApp(<DashboardPage />, { session: ADMIN_SESSION() })

    await screen.findByText('Kelas Batik')
    expect(periods).toEqual(['7d'])

    await user.click(screen.getByText('30 hari'))
    await waitFor(() => expect(periods).toContain('30d'))

    // In flight: the layout skeleton holds the page, stale charts unmounted.
    expect(document.querySelector('.kv-dash-skeleton')).not.toBeNull()
    expect(screen.queryByText('Sebaran bintang')).toBeNull()

    gate.resolve(ANALYTICS_30D)
    await screen.findByText('Topik 30 Hari')
    expect(screen.getByText('Sebaran bintang')).toBeInTheDocument()

    // The overview is period-independent: switching refetches analytics only.
    expect(periods).toEqual(['7d', '30d'])
    expect(overviewCalls).toHaveLength(1)

    await user.click(screen.getByText('Semua'))
    await screen.findByText('Sepanjang Masa')
    expect(periods).toEqual(['7d', '30d', 'all'])
    expect(overviewCalls).toHaveLength(1)
  })
})

describe('sse signal refetch', () => {
  it('refetches once per stats.changed; a failing refetch warns over stale data and retry recovers', async () => {
    // Only the debounce/backoff clock may be faked: MSW + undici need their
    // real setImmediate/MessageChannel macrotasks to settle responses.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    const channel = sseChannel('/api/stream')
    let overviewCalls = 0
    let analyticsCalls = 0
    let children = 42
    let failNextOverview = false
    server.use(
      channel.handler,
      http.get('/api/dashboard/overview', () => {
        overviewCalls += 1
        if (failNextOverview) return fail('DB_UNAVAILABLE')
        const value = children
        children += 1
        return ok({ ...OVERVIEW, children: value })
      }),
      http.get('/api/dashboard/analytics', () => {
        analyticsCalls += 1
        return ok(ANALYTICS)
      }),
    )
    renderApp(
      <>
        <StreamGate />
        <DashboardPage />
      </>,
      { session: ADMIN_SESSION() },
    )

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Kelas Batik') !== null)
    expect(overviewCalls).toBe(1)
    expect(analyticsCalls).toBe(1)

    // Next refetch will fail on the overview endpoint.
    failNextOverview = true
    channel.push('event: stats.changed\ndata: {"rev":1}\n\n')
    await advance(300)
    await advance(0)
    await until(() => document.querySelector('.kv-dash__error') !== null)

    // Exactly one refetch (both endpoints once); stale data stays visible with
    // the warning alert + retry.
    expect(overviewCalls).toBe(2)
    expect(analyticsCalls).toBe(2)
    expect(screen.getByText('Sebaran bintang')).toBeInTheDocument()
    expect(screen.getByText('Kelas Batik')).toBeInTheDocument()
    await until(() => screen.queryByText('Layanan sedang sibuk. Coba lagi.') !== null)

    // Retry with healthy handlers → warning gone, refreshed data rendered.
    failNextOverview = false
    fireEvent.click(screen.getByRole('button', { name: 'Coba lagi' }))
    await until(() => screen.queryByText('43') !== null)
    expect(document.querySelector('.kv-dash__error')).toBeNull()
    expect(screen.queryByText('42')).toBeNull()
  })
})

describe('school summary (SA-only)', () => {
  it('SA: renders the per-school table and fetches school-summary once', async () => {
    const seen: URL[] = []
    server.use(
      http.get('/api/dashboard/overview', () => ok(OVERVIEW)),
      http.get('/api/dashboard/analytics', () => ok(ANALYTICS)),
      http.get('/api/dashboard/school-summary', ({ request }) => {
        seen.push(new URL(request.url))
        return ok(SCHOOL_SUMMARY)
      }),
    )
    renderApp(<DashboardPage />, { session: SA_SESSION() })

    await screen.findByText('Ringkasan per Sekolah')
    expect(seen).toHaveLength(1)
    expect(seen[0].pathname).toBe('/api/dashboard/school-summary')

    const table = mainTable()
    // Column headers reuse the schools/kpi labels (antd mirrors them for sticky).
    const headers = screen.getAllByRole('columnheader').map((cell) => cell.textContent)
    expect(headers).toEqual(
      expect.arrayContaining([
        'Nama sekolah',
        'Status',
        'Anak terdaftar',
        'Sesi dibuka',
        'Sesi selesai',
        'Rapor terbit',
      ]),
    )
    // Row values, each in its own school row.
    const bandungRow = within(table).getByText('Bandung').closest('tr')
    expect(bandungRow).not.toBeNull()
    const row = within(bandungRow as HTMLElement)
    expect(row.getByText('Aktif')).toBeInTheDocument()
    expect(row.getByText('120')).toBeInTheDocument()
    expect(row.getByText('3')).toBeInTheDocument()
    expect(row.getByText('15')).toBeInTheDocument()
    expect(row.getByText('21')).toBeInTheDocument()

    const jakartaRow = within(table).getByText('Jakarta').closest('tr')
    expect(jakartaRow).not.toBeNull()
    const row2 = within(jakartaRow as HTMLElement)
    expect(row2.getByText('Nonaktif')).toBeInTheDocument()
    expect(row2.getByText('40')).toBeInTheDocument()
    expect(row2.getByText('6')).toBeInTheDocument()
    expect(row2.getByText('9')).toBeInTheDocument()
  })

  it('ADMIN: never fetches nor renders the school summary', async () => {
    let summaryCalls = 0
    server.use(
      http.get('/api/dashboard/overview', () => ok(OVERVIEW)),
      http.get('/api/dashboard/analytics', () => ok(ANALYTICS)),
      http.get('/api/dashboard/school-summary', () => {
        summaryCalls += 1
        return ok(SCHOOL_SUMMARY)
      }),
    )
    renderApp(<DashboardPage />, { session: ADMIN_SESSION() })

    await screen.findByText('Kelas Batik')
    expect(summaryCalls).toBe(0)
    expect(screen.queryByText('Ringkasan per Sekolah')).toBeNull()
    expect(screen.queryByText('Bandung')).toBeNull()
  })

  it('SA: renders the empty placeholder for an empty school list', async () => {
    server.use(
      http.get('/api/dashboard/overview', () => ok(OVERVIEW)),
      http.get('/api/dashboard/analytics', () => ok(ANALYTICS)),
      http.get('/api/dashboard/school-summary', () => ok(EMPTY_SCHOOL_SUMMARY)),
    )
    renderApp(<DashboardPage />, { session: SA_SESSION() })

    await screen.findByText('Ringkasan per Sekolah')
    expect(within(mainTable()).getByText('Tidak ada data')).toBeInTheDocument()
  })

  it('SA: a failing school-summary warns over the rendered dashboard and retry recovers', async () => {
    let failing = true
    server.use(
      http.get('/api/dashboard/overview', () => ok(OVERVIEW)),
      http.get('/api/dashboard/analytics', () => ok(ANALYTICS)),
      http.get('/api/dashboard/school-summary', () =>
        failing ? fail('DB_UNAVAILABLE') : ok(SCHOOL_SUMMARY),
      ),
    )
    const user = userEvent.setup()
    renderApp(<DashboardPage />, { session: SA_SESSION() })

    // Overview + analytics render; the summary failure surfaces as the page's
    // warning alert (stale data stays visible).
    await screen.findByText('Kelas Batik')
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Layanan sedang sibuk. Coba lagi.')
    expect(screen.getByText('Sebaran bintang')).toBeInTheDocument()
    expect(screen.queryByText('Ringkasan per Sekolah')).toBeNull()

    failing = false
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    await screen.findByText('Ringkasan per Sekolah')
    expect(within(mainTable()).getByText('Bandung')).toBeInTheDocument()
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('SA: stats.changed refetches the school summary once per signal', async () => {
    // Only the debounce clock is faked (same pattern as the sse describe above).
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    const channel = sseChannel('/api/stream')
    let summaryCalls = 0
    let children = 120
    server.use(
      channel.handler,
      http.get('/api/dashboard/overview', () => ok(OVERVIEW)),
      http.get('/api/dashboard/analytics', () => ok(ANALYTICS)),
      http.get('/api/dashboard/school-summary', () => {
        summaryCalls += 1
        const value = children
        children += 1
        return ok({ schools: [{ ...SCHOOL_SUMMARY.schools[0], children: value }] })
      }),
    )
    renderApp(
      <>
        <StreamGate />
        <DashboardPage />
      </>,
      { session: SA_SESSION() },
    )

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Ringkasan per Sekolah') !== null)
    expect(summaryCalls).toBe(1)

    channel.push('event: stats.changed\ndata: {"rev":1}\n\n')
    await advance(300)
    await advance(0)
    // One debounced signal → exactly one summary refetch with the new value.
    await until(() => summaryCalls === 2)
    expect(within(mainTable()).getByText('121')).toBeInTheDocument()
  })
})
