/**
 * WaStatusBadge (spec F-13 / FL-14): hidden for FAS, event-driven from
 * `wa.changed` SSE frames (immediate value signal — no 300ms refetch
 * window), `GET /api/wa/status` only on popover open with fetch-once
 * caching, `errors.INTERNAL` on failure. Click behaviour is role-gated: an SA
 * click opens the connect modal, an ADMIN click is inert (badge + popover
 * still work), and FAS never sees the badge. Real timers everywhere except
 * the SSE test, which fakes only setTimeout/setInterval per the sessions
 * integration pattern (MSW + undici keep their real macrotasks).
 */
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, waitFor, within } from '@testing-library/react'
import { http } from 'msw'
import StreamGate from './StreamGate'
import WaStatusBadge from './WaStatusBadge'
import { renderApp } from '../test/render'
import { server } from '../test/setup'
import { fail, makeLoginData, makeUser, ok, sseChannel } from '../test/handlers'
import { useAuth } from '../lib/auth'

/** Renders the restored session role so a test can wait for auth readiness. */
function RoleProbe() {
  const { user } = useAuth()
  return <span data-testid="role-probe">{user?.role ?? 'none'}</span>
}

async function waitForRole(role: string): Promise<void> {
  await waitFor(() => expect(screen.getByTestId('role-probe')).toHaveTextContent(role))
}

const CHECKING_NAME = 'Status WhatsApp: Memeriksa…'

function adminSession() {
  return makeLoginData({ user: makeUser({ role: 'ADMIN' }) })
}

function saSession() {
  return makeLoginData({ user: makeUser({ role: 'SA' }) })
}

/** The detail DOM is "open" while mounted and not in the leave-hidden state. */
function detailOpen(): boolean {
  const detail = document.querySelector('.kv-wa-detail')
  return detail !== null && !detail.closest('.ant-popover-hidden')
}

async function hoverOpen(badge: HTMLElement): Promise<HTMLElement> {
  fireEvent.mouseOver(badge)
  await waitFor(() => expect(detailOpen()).toBe(true))
  return document.querySelector('.kv-wa-detail') as HTMLElement
}

async function hoverClose(badge: HTMLElement): Promise<void> {
  fireEvent.mouseOut(badge)
  await waitFor(() => expect(detailOpen()).toBe(false))
}

/** React state (session restore / stream frames) updates during these → wrap in act. */
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

describe('WaStatusBadge visibility and initial state', () => {
  it('renders nothing for a facilitator', async () => {
    const { container } = renderApp(<WaStatusBadge />, {
      session: makeLoginData({ user: makeUser({ role: 'FASILITATOR' }) }),
    })

    // antd App keeps its `.ant-app` wrapper — "renders nothing" means no badge.
    await waitFor(() => expect(container.querySelector('.kv-wa')).toBeNull())
    expect(container.textContent).toBe('')
    expect(screen.queryByRole('button', { name: /Status WhatsApp/ })).toBeNull()
  })

  it('renders the unknown state without touching the status endpoint', () => {
    let waCalls = 0
    server.use(
      http.get('/api/wa/status', () => {
        waCalls += 1
        return ok({ connected: false })
      }),
    )
    renderApp(<WaStatusBadge />, { session: adminSession() })

    const badge = screen.getByRole('button', { name: CHECKING_NAME })
    expect(badge).toHaveClass('kv-wa', 'is-unknown')
    expect(waCalls).toBe(0) // fetched only when the detail popover opens
  })
})

describe('WaStatusBadge stream-driven state', () => {
  it('renders wa.changed frames immediately, without the refetch debounce', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    const channel = sseChannel('/api/stream')
    server.use(channel.handler)
    renderApp(
      <>
        <StreamGate />
        <WaStatusBadge />
      </>,
      { session: makeLoginData() },
    )

    await until(() => channel.handshakes === 1)
    expect(screen.getByRole('button', { name: CHECKING_NAME })).toBeInTheDocument()

    channel.push('event: wa.changed\ndata: {"connected":true,"rev":1}\n\n')
    await advance(1) // a single tick — NOT the 300ms refetch window
    const connected = screen.getByRole('button', { name: 'Status WhatsApp: Terhubung' })
    expect(connected).toHaveClass('kv-wa', 'is-on')

    channel.push('event: wa.changed\ndata: {"connected":false,"rev":2}\n\n')
    await advance(1)
    const disconnected = screen.getByRole('button', { name: 'Status WhatsApp: Terputus' })
    expect(disconnected).toHaveClass('kv-wa', 'is-off')
  })
})

describe('WaStatusBadge detail popover', () => {
  it('fetches the detail once on hover and reuses it after close and reopen', async () => {
    let waCalls = 0
    server.use(
      http.get('/api/wa/status', () => {
        waCalls += 1
        return ok({ connected: true, session: 'Kantor Pusat' })
      }),
    )
    renderApp(<WaStatusBadge />, { session: adminSession() })
    const badge = screen.getByRole('button', { name: CHECKING_NAME })
    expect(waCalls).toBe(0)

    const detail = await hoverOpen(badge)
    expect(waCalls).toBe(1)
    expect(within(detail).getByText('Terhubung')).toBeInTheDocument()
    expect(within(detail).getByText('Kantor Pusat')).toBeInTheDocument()

    await hoverClose(badge)
    expect(waCalls).toBe(1)

    await hoverOpen(badge)
    expect(waCalls).toBe(1) // cached detail — no second GET
  })

  it('resolves the checking state from the on-demand status fetch', async () => {
    server.use(http.get('/api/wa/status', () => ok({ connected: true })))
    renderApp(<WaStatusBadge />, { session: adminSession() })
    const badge = screen.getByRole('button', { name: CHECKING_NAME })

    const detail = await hoverOpen(badge)

    // The badge must not keep saying "Checking…" once the detail call has
    // returned a real state — otherwise hovering shows a contradictory value.
    const resolved = screen.getByRole('button', { name: 'Status WhatsApp: Terhubung' })
    expect(resolved).toHaveClass('kv-wa', 'is-on')
    expect(within(detail).getByText('Terhubung')).toBeInTheDocument()
  })

  it('renders errors.INTERNAL in the popover when the detail fetch fails', async () => {
    let waCalls = 0
    server.use(
      http.get('/api/wa/status', () => {
        waCalls += 1
        return fail('INTERNAL')
      }),
    )
    renderApp(<WaStatusBadge />, { session: adminSession() })
    const badge = screen.getByRole('button', { name: CHECKING_NAME })

    fireEvent.mouseOver(badge)
    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
    expect(waCalls).toBe(1)
  })

  it('retries the detail fetch after a failed attempt', async () => {
    let waCalls = 0
    server.use(
      http.get('/api/wa/status', () => {
        waCalls += 1
        return waCalls === 1 ? fail('INTERNAL') : ok({ connected: true })
      }),
    )
    renderApp(<WaStatusBadge />, { session: adminSession() })
    const badge = screen.getByRole('button', { name: CHECKING_NAME })

    fireEvent.mouseOver(badge)
    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
    expect(waCalls).toBe(1)

    fireEvent.mouseOut(badge)
    await waitFor(() => expect(screen.queryByRole('tooltip')).toBeNull())

    const detail = await hoverOpen(badge)
    expect(waCalls).toBe(2)
    expect(within(detail).getByText('Terhubung')).toBeInTheDocument()
  })

  it('opens the detail popover on keyboard focus', async () => {
    server.use(http.get('/api/wa/status', () => ok({ connected: false })))
    renderApp(<WaStatusBadge />, { session: adminSession() })
    const badge = screen.getByRole('button', { name: CHECKING_NAME })

    fireEvent.focus(badge)

    await waitFor(() => expect(detailOpen()).toBe(true))
    const detail = document.querySelector('.kv-wa-detail') as HTMLElement
    expect(within(detail).getByText('Terputus')).toBeInTheDocument()
  })

  it('shows only the state line when the status carries no extras', async () => {
    server.use(http.get('/api/wa/status', () => ok({ connected: false })))
    renderApp(<WaStatusBadge />, { session: adminSession() })
    const badge = screen.getByRole('button', { name: CHECKING_NAME })

    const detail = await hoverOpen(badge)
    expect(within(detail).getByText('Terputus')).toBeInTheDocument()
    expect(detail.querySelectorAll('div')).toHaveLength(1) // state line only
    expect(detail.querySelectorAll('.kv-meta')).toHaveLength(0)
  })

  it('shows session name and check time alongside the state line', async () => {
    const checkedAt = '2026-10-09T10:00:00+07:00'
    server.use(
      http.get('/api/wa/status', () =>
        ok({ connected: false, session: 'Kantor Pusat', checked_at: checkedAt }),
      ),
    )
    renderApp(<WaStatusBadge />, { session: adminSession() })
    const badge = screen.getByRole('button', { name: CHECKING_NAME })

    const detail = await hoverOpen(badge)
    expect(within(detail).getByText('Terputus')).toBeInTheDocument()
    expect(within(detail).getByText('Kantor Pusat')).toBeInTheDocument()
    expect(within(detail).getByText(new Date(checkedAt).toLocaleString())).toBeInTheDocument()
    expect(detail.querySelectorAll('.kv-meta')).toHaveLength(2)
  })

  it('SA click opens the connect modal', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    renderApp(
      <>
        <WaStatusBadge />
        <RoleProbe />
      </>,
      { session: saSession() },
    )
    await waitForRole('SA')
    const badge = screen.getByRole('button', { name: CHECKING_NAME })

    fireEvent.click(badge)

    expect(await screen.findByText('Hubungkan WhatsApp')).toBeInTheDocument()
    expect(open).not.toHaveBeenCalled()
  })

  it('ADMIN click does nothing', async () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    renderApp(
      <>
        <WaStatusBadge />
        <RoleProbe />
      </>,
      { session: adminSession() },
    )
    await waitForRole('ADMIN')
    const badge = screen.getByRole('button', { name: CHECKING_NAME })

    fireEvent.click(badge)

    expect(screen.queryByText('Hubungkan WhatsApp')).toBeNull()
    expect(open).not.toHaveBeenCalled()
  })

  it('ADMIN badge is marked disabled and keeps its popover', async () => {
    server.use(http.get('/api/wa/status', () => ok({ connected: false })))
    renderApp(
      <>
        <WaStatusBadge />
        <RoleProbe />
      </>,
      { session: adminSession() },
    )
    await waitForRole('ADMIN')
    const badge = screen.getByRole('button', { name: CHECKING_NAME })

    expect(badge).toHaveAttribute('aria-disabled', 'true')
    expect(badge).toHaveAttribute('title', 'Hanya superadmin yang dapat menghubungkan WhatsApp.')

    const detail = await hoverOpen(badge)
    expect(within(detail).getByText('Terputus')).toBeInTheDocument()
  })
})
