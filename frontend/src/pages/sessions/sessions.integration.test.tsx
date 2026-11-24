/**
 * Sessions list integration (FL-03 server paging + 02§2c SSE refetch):
 * page navigation keeps the whole query contract (only `page` changes), and
 * `session.changed` frames arriving through the REAL StreamGate refetch
 * EXACTLY the current page+filters once (300ms debounce + rev gate).
 */
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import StreamGate from '../../components/StreamGate'
import SessionsPage from './SessionsPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, sseChannel } from '../../test/handlers'

function sessionRow(id: number, name: string) {
  return {
    id,
    program_id: 1,
    program_name: 'Eksplorasi Kota',
    name,
    description: '',
    date: '2026-10-10',
    start_time: null,
    end_time: null,
    location: 'Museum Tekstil',
    status: 'DRAFT',
    group_count: 0,
    member_count: 0,
    created_at: '2026-10-01T00:00:00Z',
  }
}

/** Page-aware list endpoint that records every request URL it served. */
function listHandler(seen: URL[]) {
  return http.get('/api/sessions', ({ request }) => {
    const url = new URL(request.url)
    seen.push(url)
    const page = Number(url.searchParams.get('page') ?? '1')
    const perPage = Number(url.searchParams.get('per_page') ?? '20')
    const rows =
      page === 2
        ? [sessionRow(3, 'Kelas Batik Rabu')]
        : [sessionRow(1, 'Kelas Batik Senin'), sessionRow(2, 'Kelas Batik Selasa')]
    return HttpResponse.json({ data: rows, meta: { page, per_page: perPage, total: 45 } })
  })
}

/** Everything except the page cursor — proves filters/sort survive paging. */
function withoutPage(url: URL): string {
  return [...url.searchParams.entries()]
    .filter(([key]) => key !== 'page')
    .map(([key, value]) => `${key}=${value}`)
    .sort()
    .join('&')
}

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

const ADMIN_SESSION = () =>
  makeLoginData({ user: makeUser({ role: 'ADMIN' }) })

describe('session list paging', () => {
  it('changes only the page cursor when navigating pages', async () => {
    const seen: URL[] = []
    server.use(listHandler(seen))
    const user = userEvent.setup()
    renderApp(<SessionsPage />, { route: '/sessions', session: ADMIN_SESSION() })

    await screen.findByText('Kelas Batik Senin')
    expect(seen).toHaveLength(1)
    expect(seen[0].searchParams.get('page')).toBe('1')
    expect(seen[0].searchParams.get('per_page')).toBe('20')

    await user.click(screen.getByTitle('2'))
    await screen.findByText('Kelas Batik Rabu')

    const after = seen.at(-1)
    expect(after?.searchParams.get('page')).toBe('2')
    expect(after?.searchParams.get('per_page')).toBe('20')
    expect(withoutPage(after as URL)).toBe(withoutPage(seen[0]))
  })
})

describe('sse signal refetch', () => {
  it('one debounced refetch with the SAME params; stale rev dropped', async () => {
    // Only the debounce/backoff clock may be faked: MSW + undici need their
    // real setImmediate/MessageChannel macrotasks to settle responses.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    const seen: URL[] = []
    const channel = sseChannel('/api/stream')
    server.use(channel.handler, listHandler(seen))
    renderApp(
      <>
        <StreamGate />
        <SessionsPage />
      </>,
      { route: '/sessions', session: ADMIN_SESSION() },
    )

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Kelas Batik Senin') !== null)

    // Move to page 2 first — the refetch must preserve that cursor.
    // (fireEvent: userEvent's own delay clock deadlocks under fake timers.)
    fireEvent.click(screen.getByTitle('2'))
    await until(() => screen.queryByText('Kelas Batik Rabu') !== null)
    const before = seen.at(-1)?.search
    const count = seen.length
    expect(before).toContain('page=2')

    // A burst of two frames → exactly ONE refetch with the identical query.
    channel.push('event: session.changed\ndata: {"rev":1}\n\n')
    channel.push('event: session.changed\ndata: {"rev":2}\n\n')
    await advance(300)
    await advance(0)
    expect(seen).toHaveLength(count + 1)
    expect(seen.at(-1)?.search).toBe(before)

    // rev 1 again — already seen, the gate drops it without a refetch.
    channel.push('event: session.changed\ndata: {"rev":1}\n\n')
    await advance(300)
    await advance(0)
    expect(seen).toHaveLength(count + 1)
  })
})
