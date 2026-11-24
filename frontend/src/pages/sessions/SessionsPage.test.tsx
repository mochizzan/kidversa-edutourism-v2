/**
 * Session list (F-08): server filters always reset page=1 and combine with
 * the search query; the StreamGate-routed SSE refetch re-runs EXACTLY the
 * current params; `session.deleted` drops the row via the same soft refetch;
 * FAS never sees the create entry point; envelope errors render the
 * translated code with a working retry.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import StreamGate from '../../components/StreamGate'
import SessionsPage from './SessionsPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, paged, sseChannel } from '../../test/handlers'
import {
  advance,
  pushFrame,
  sessionRow,
  until,
  useSseFakeTimers,
} from '../../test/helpers.cov-session'
import type { SessionListItem } from '../../lib/session-contracts'

/** Page-aware list endpoint: records every request URL, rows depend on filters. */
function listHandler(seen: URL[], rowsRef: { rows: SessionListItem[] }) {
  return http.get('/api/sessions', ({ request }) => {
    const url = new URL(request.url)
    seen.push(url)
    const page = Number(url.searchParams.get('page') ?? '1')
    const perPage = Number(url.searchParams.get('per_page') ?? '20')
    const rows =
      page === 2 ? [sessionRow(3, 'Kelas Batik Rabu')] : rowsRef.rows
    return HttpResponse.json({
      data: rows,
      meta: { page, per_page: perPage, total: 45 },
    })
  })
}

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })
const FAS_SESSION = () => makeLoginData({ user: makeUser({ role: 'FASILITATOR' }) })

describe('session list filters', () => {
  it('status filter and search reset page=1 and combine with the query', async () => {
    const seen: URL[] = []
    const rowsRef = { rows: [sessionRow(1, 'Kelas Batik Senin'), sessionRow(2, 'Kelas Batik Selasa')] }
    server.use(listHandler(seen, rowsRef))
    const user = userEvent.setup()
    renderApp(<SessionsPage />, { route: '/sessions', session: ADMIN_SESSION() })

    await screen.findByText('Kelas Batik Senin')
    expect(seen).toHaveLength(1)
    expect(seen[0].searchParams.get('page')).toBe('1')

    // Move to page 2 first — a filter change must snap the cursor back.
    await user.click(screen.getByTitle('2'))
    await screen.findByText('Kelas Batik Rabu')
    expect(seen.at(-1)?.searchParams.get('page')).toBe('2')

    // Status filter → page=1 + status param. (index 0: index 1 is the pager's
    // page-size select, which renders only once meta exists.)
    await user.click(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('Dibuka'))
    await screen.findByText('Kelas Batik Senin')
    let last = seen.at(-1) as URL
    expect(last.searchParams.get('page')).toBe('1')
    expect(last.searchParams.get('status')).toBe('OPENED')

    // Search keeps the status filter and again resets the cursor.
    await user.type(screen.getByRole('searchbox'), 'batik{Enter}')
    await screen.findByText('Kelas Batik Senin')
    last = seen.at(-1) as URL
    expect(last.searchParams.get('page')).toBe('1')
    expect(last.searchParams.get('q')).toBe('batik')
    expect(last.searchParams.get('status')).toBe('OPENED')
  })

  it('renders the translated envelope error with a working retry', async () => {
    let failNext = true
    server.use(
      http.get('/api/sessions', () => {
        if (failNext) {
          failNext = false
          return HttpResponse.json({ error: { code: 'RATE_LIMITED', message: 'slow' } })
        }
        return paged([sessionRow(1, 'Kelas Batik Senin')], { page: 1, per_page: 20, total: 1 })
      }),
    )
    const user = userEvent.setup()
    renderApp(<SessionsPage />, { route: '/sessions', session: ADMIN_SESSION() })

    await screen.findByText('Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.')
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    await screen.findByText('Kelas Batik Senin')
  })
})

describe('session list role gating', () => {
  it('FAS never sees the create entry point', async () => {
    server.use(http.get('/api/sessions', () => paged([], { page: 1, per_page: 20, total: 0 })))
    renderApp(<SessionsPage />, { route: '/sessions', session: FAS_SESSION() })

    await screen.findByText('Belum ada sesi.')
    expect(screen.queryByRole('button', { name: 'Buat sesi' })).toBeNull()
    expect(screen.getByRole('button', { name: /Muat ulang/ })).toBeInTheDocument()
  })

  it('ADMIN create button navigates to the wizard route', async () => {
    const seen: URL[] = []
    const rowsRef = { rows: [sessionRow(1, 'Kelas Batik Senin')] }
    server.use(listHandler(seen, rowsRef))
    const user = userEvent.setup()
    renderApp(null, {
      routes: [
        { path: '/sessions', element: <SessionsPage /> },
        { path: '/sessions/new', element: <div>CREATE WIZARD</div> },
      ],
      route: '/sessions',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Kelas Batik Senin')
    // The leading icon's aria-label is part of the accessible name.
    await user.click(screen.getByRole('button', { name: /Buat sesi/ }))
    await screen.findByText('CREATE WIZARD')
  })

  it('changing the page size resets to page 1 with the new per_page', async () => {
    const seen: URL[] = []
    const rowsRef = { rows: [sessionRow(1, 'Kelas Batik Senin')] }
    server.use(listHandler(seen, rowsRef))
    const user = userEvent.setup()
    renderApp(<SessionsPage />, { route: '/sessions', session: ADMIN_SESSION() })

    await screen.findByText('Kelas Batik Senin')

    // The pager's size changer is a Select of "<size> / page" options.
    await user.click(screen.getByRole('combobox', { name: 'Page Size' }))
    await user.click(await screen.findByText('50 / page', { selector: '.ant-select-item-option-content' }))

    const last = seen.at(-1) as URL
    await waitFor(() => expect(last.searchParams.get('per_page')).toBe('50'))
    expect(last.searchParams.get('page')).toBe('1')
  })
})

describe('session list SSE refetch', () => {
  it('session.changed refetches the SAME params; session.deleted drops the row', async () => {
    useSseFakeTimers()
    const seen: URL[] = []
    const rowsRef = { rows: [sessionRow(1, 'Kelas Batik Senin'), sessionRow(2, 'Kelas Batik Selasa')] }
    const channel = sseChannel('/api/stream')
    server.use(channel.handler, listHandler(seen, rowsRef))
    renderApp(
      <>
        <StreamGate />
        <SessionsPage />
      </>,
      { route: '/sessions', session: ADMIN_SESSION() },
    )

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Kelas Batik Senin') !== null)
    const before = seen.at(-1)?.search
    const count = seen.length

    // Same-params soft refetch.
    pushFrame(channel, 'session.changed', { rev: 1 })
    await advance(300)
    await advance(0)
    expect(seen).toHaveLength(count + 1)
    expect(seen.at(-1)?.search).toBe(before)

    // The server drops the row → the next refetch removes it from the table.
    rowsRef.rows = [rowsRef.rows[0]]
    pushFrame(channel, 'session.deleted', { rev: 2, entity_id: 2 })
    await until(() => screen.queryByText('Kelas Batik Selasa') === null)
    expect(screen.getByText('Kelas Batik Senin')).toBeInTheDocument()
  })
})
