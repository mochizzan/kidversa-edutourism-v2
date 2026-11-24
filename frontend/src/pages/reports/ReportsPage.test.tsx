/**
 * Focused single-child report page (F-12 / FL-13): the readiness gate hides
 * compose (preview / links) until complete, `readiness.changed` flips the gate
 * in place, the header carries the child/topic context and an explicit back
 * action, readiness errors retry, and export routes through the client-only
 * exportReport module. Submission lives on the participant table, so this
 * page has no send panel.
 */
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import StreamGate from '../../components/StreamGate'
import ReportsPage from './ReportsPage'
import { emitSignal } from '../../lib/stream'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, ok, reportData, sseChannel } from '../../test/handlers'
import { detailFixture, until, useSseFakeTimers } from '../../test/helpers.cov-session'
import type { SessionDetail } from '../../lib/session-contracts'
import type { RouteObject } from 'react-router'

const mocks = vi.hoisted(() => ({ exportReport: vi.fn(async () => undefined) }))
vi.mock('../../report/export', () => ({ exportReport: mocks.exportReport }))

const ROUTE = '/sessions/1/reports/topics/3/children/2'

const ROUTES: RouteObject[] = [
  { path: '/sessions/:sid/reports/topics/:stid/children/:cid', element: <ReportsPage /> },
  { path: '/sessions/:sid/reports/topics/:stid', element: <div>Peserta topik</div> },
  { path: '/sessions/:sid/reports/topics', element: <div>Daftar topik</div> },
  { path: '/reports', element: <div>Daftar rapor</div> },
]

const P2 = '/api/sessions/1/topics/3/children/2'

/** Everything the page fetches EXCEPT readiness (each test supplies it). */
function baseHandlers(detail?: () => SessionDetail) {
  return [
    http.get('/api/sessions/1', () => ok(detail ? detail() : detailFixture({ status: 'OPENED' }))),
    http.get(`${P2}/report`, () => ok(reportData())),
    http.get(`${P2}/public-links`, () => ok([])),
    http.get(`${P2}/narrative`, () => ok(null)),
    http.get(`${P2}/missions`, () => ok([])),
  ]
}

function readinessHandler(complete: boolean, readyUrls?: URL[]) {
  return http.get(`${P2}/readiness`, ({ request }) => {
    readyUrls?.push(new URL(request.url))
    return ok(
      complete
        ? { complete: true, missing: [] }
        : { complete: false, missing: [{ said: 10, name: 'Mengamati motif' }] },
    )
  })
}

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })

describe('reports readiness gate', () => {
  it('blocks compose while incomplete and lists the missing outcomes', async () => {
    server.use(readinessHandler(false), ...baseHandlers())
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    // Warning banner carries the count, the missing list and the jump link.
    await screen.findByText('1 kegiatan belum dinilai.', {}, { timeout: 5000 })
    screen.getByText('Mengamati motif')
    const jump = screen.getByRole('link', { name: 'Buka penilaian' })
    expect(jump).toHaveAttribute('href', '/assessment/1/2/3')

    // Compose surfaces stay hidden; the ungated editors stay visible.
    expect(screen.queryByText('Pratinjau rapor')).toBeNull()
    expect(screen.queryByText('Tautan publik')).toBeNull()
    screen.getByText('Narasi AI')
    screen.getByRole('heading', { name: 'Misi' })
  })

  it('renders compose surfaces when readiness is complete', async () => {
    server.use(readinessHandler(true), ...baseHandlers())
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    await screen.findByText('Pratinjau rapor', {}, { timeout: 5000 })
    screen.getByRole('button', { name: /Unduh PNG/ })
    screen.getByRole('button', { name: /Unduh PDF/ })
    screen.getByText('Tautan publik')
    screen.getByText('Belum ada tautan aktif.')
  })

  it('readiness.changed flips the gate in place through the stream gate', async () => {
    useSseFakeTimers()
    const channel = sseChannel('/api/stream')
    server.use(channel.handler, readinessHandler(false), ...baseHandlers())
    renderApp(null, {
      routes: [
        {
          path: '/sessions/:sid/reports/topics/:stid/children/:cid',
          element: (
            <>
              <StreamGate />
              <ReportsPage />
            </>
          ),
        },
      ],
      route: ROUTE,
      session: ADMIN_SESSION(),
    })

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('1 kegiatan belum dinilai.') !== null)

    // Same child+topic completes → the preview appears without a refetch.
    channel.push(
      'event: readiness.changed\ndata: {"rev":1,"child_id":2,"stid":3,"complete":true}\n\n',
    )
    await until(() => screen.queryByText('Pratinjau rapor') !== null)
  })
})

describe('reports page interactions', () => {
  it('shows the focused child header and returns to the participant table', async () => {
    server.use(readinessHandler(true), ...baseHandlers())
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    await screen.findByText('Pratinjau rapor')
    const header = document.querySelector('.kv-page-header') as HTMLElement
    within(header).getByRole('heading', { level: 1, name: 'Aisyah Putri' })
    within(header).getByText('Kelas Batik')
    within(header).getByText('Batik Nusantara')

    await user.click(screen.getByRole('button', { name: /Kembali ke peserta/ }))
    expect(await screen.findByText('Peserta topik')).toBeInTheDocument()
  })

  it('an unknown topic renders NOT_FOUND with a back-to-topics action', async () => {
    server.use(...baseHandlers())
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/1/reports/topics/99/children/2',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Data tidak ditemukan.')
    await user.click(screen.getByRole('button', { name: /Kembali ke topik/ }))
    expect(await screen.findByText('Daftar topik')).toBeInTheDocument()
  })

  it('a readiness signal arriving before the first load cannot patch a null state', async () => {
    const gate = Promise.withResolvers<void>()
    server.use(
      http.get(`${P2}/readiness`, async () => {
        await gate.promise
        return ok({ complete: false, missing: [{ said: 10, name: 'Mengamati motif' }] })
      }),
      ...baseHandlers(),
    )
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    // Settled page while readiness is still pending.
    await screen.findByText('Narasi AI')
    act(() => emitSignal('readiness.changed', { rev: 1, child_id: 2, stid: 3, complete: true }))
    gate.resolve()

    // The load wins: the stale signal could not create the state it wanted to patch.
    await screen.findByText('1 kegiatan belum dinilai.')
    expect(screen.queryByText('Pratinjau rapor')).toBeNull()
  })

  it('session.changed refetches the detail and keeps the page scoped', async () => {
    let detailCalls = 0
    const detail = () => {
      detailCalls += 1
      return detailFixture({ status: 'OPENED' })
    }
    server.use(readinessHandler(true), ...baseHandlers(detail))
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    await screen.findByText('Pratinjau rapor')
    expect(detailCalls).toBe(1)

    act(() => emitSignal('session.changed', { rev: 2 }))
    await waitFor(() => expect(detailCalls).toBe(2))

    screen.getByRole('heading', { level: 1, name: 'Aisyah Putri' })
    expect(screen.getByText('Pratinjau rapor')).toBeInTheDocument()
  })

  it('session.deleted redirects the open report to the list', async () => {
    server.use(readinessHandler(true), ...baseHandlers())
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    await screen.findByText('Pratinjau rapor')
    act(() => emitSignal('session.deleted', { rev: 3, entity_id: 1 }))

    expect(await screen.findByText('Daftar rapor')).toBeInTheDocument()
  })

  it('retries the readiness error from the inline action', async () => {
    let readyCalls = 0
    server.use(
      http.get(`${P2}/readiness`, () => {
        readyCalls += 1
        if (readyCalls === 1) {
          return HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } })
        }
        return ok({ complete: true, missing: [] })
      }),
      ...baseHandlers(),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))

    await screen.findByText('Pratinjau rapor')
    expect(readyCalls).toBe(2)
  })

  it('runs the PNG export through the export module', async () => {
    server.use(readinessHandler(true), ...baseHandlers())
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    await screen.findByText('Pratinjau rapor')
    await user.click(screen.getByRole('button', { name: /Unduh PNG/ }))

    await waitFor(() => expect(mocks.exportReport).toHaveBeenCalledTimes(1))
    expect(mocks.exportReport).toHaveBeenCalledWith(
      'png',
      expect.objectContaining({
        childName: 'Aisyah Putri',
        topicName: 'Batik Nusantara',
        sessionDate: expect.stringMatching(/^\d{4}-\d{2}-\d{2}$/),
      }),
    )
  })

  it('runs the PDF export and toasts when the export module rejects', async () => {
    let failNext = false
    mocks.exportReport.mockImplementation(async () => {
      if (failNext) throw new Error('pdf engine down')
    })
    server.use(readinessHandler(true), ...baseHandlers())
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    await screen.findByText('Pratinjau rapor')
    await user.click(screen.getByRole('button', { name: /Unduh PDF/ }))
    await waitFor(() => expect(mocks.exportReport).toHaveBeenCalledWith('pdf', expect.anything()))

    // A rejecting export surfaces as the INTERNAL toast, never a silent drop.
    failNext = true
    await user.click(screen.getByRole('button', { name: /Unduh PNG/ }))
    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
    mocks.exportReport.mockImplementation(async () => undefined)
  })

  it('readiness.changed patches the gate in place; report-link.changed refetches links', async () => {
    const readyUrls: URL[] = []
    let linksCalls = 0
    server.use(
      readinessHandler(false, readyUrls),
      http.get(`${P2}/report`, () => ok(reportData())),
      http.get(`${P2}/public-links`, () => {
        linksCalls += 1
        return ok([])
      }),
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get(`${P2}/narrative`, () => ok(null)),
      http.get(`${P2}/missions`, () => ok([])),
    )
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    // Incomplete → the gate shows the missing list.
    await screen.findByText('1 kegiatan belum dinilai.')
    const beforeReady = readyUrls.length

    // The signal patches completeness IN PLACE — no extra readiness GET.
    act(() => emitSignal('readiness.changed', { rev: 1, child_id: 2, stid: 3, complete: true }))
    await waitFor(() => expect(screen.queryByText('1 kegiatan belum dinilai.')).toBeNull())
    expect(readyUrls.length).toBe(beforeReady)

    const beforeLinks = linksCalls
    act(() => emitSignal('report-link.changed', { rev: 1 }))
    await waitFor(() => expect(linksCalls).toBeGreaterThan(beforeLinks))
  })

  it('renders the detail envelope error translated with a back link', async () => {
    server.use(
      http.get('/api/sessions/1', () =>
        HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'gone' } }),
      ),
    )
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    await screen.findByText('Data tidak ditemukan.')
    expect(screen.getByRole('link', { name: 'Kembali' })).toHaveAttribute('href', '/reports')
  })

  it('report and links failures render their retry alerts', async () => {
    let reportCalls = 0
    let linksCalls = 0
    server.use(
      readinessHandler(true),
      http.get(`${P2}/report`, () => {
        reportCalls += 1
        return HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } })
      }),
      http.get(`${P2}/public-links`, () => {
        linksCalls += 1
        if (linksCalls === 1) {
          return HttpResponse.json({ error: { code: 'INTERNAL', message: 'boom' } })
        }
        return ok([])
      }),
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get(`${P2}/narrative`, () => ok(null)),
      http.get(`${P2}/missions`, () => ok([])),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: ROUTE, session: ADMIN_SESSION() })

    // Both panels surface their failures with dedicated retry actions.
    const reportAlert = (await screen.findAllByText('Layanan sedang sibuk. Coba lagi.'))[0]
    screen.getByText('Terjadi kesalahan tak terduga. Coba lagi.')
    await user.click(
      within(reportAlert.closest('.ant-alert') as HTMLElement).getByRole('button', {
        name: 'Coba lagi',
      }),
    )
    await waitFor(() => expect(reportCalls).toBe(2))

    // The links alert retries through loadLinks.
    const linksAlert = (await screen.findAllByText('Terjadi kesalahan tak terduga. Coba lagi.')).at(-1)
    await user.click(
      within(linksAlert?.closest('.ant-alert') as HTMLElement).getByRole('button', {
        name: 'Coba lagi',
      }),
    )
    await waitFor(() => expect(linksCalls).toBe(2))
  })
})
