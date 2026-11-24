/**
 * TokenCountdown (spec F-01 / 02§2c): a LOCAL 1s tick from `expires_at` —
 * no polling. Renders mm:ss from the mount-time clock, clamps at 00:00 for a
 * past expiry (never a negative value), and keeps the `kv-num` tabular-numeral
 * class on the value span. Timers + Date faked so the tick is deterministic.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, screen } from '@testing-library/react'
import TokenCountdown from './TokenCountdown'
import { renderApp } from '../test/render'

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

describe('TokenCountdown local tick', () => {
  beforeEach(() => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'],
    })
  })

  it('counts mm:ss down from the expiry and stays clamped at 00:00', async () => {
    const expiresAt = new Date(Date.now() + 90_000).toISOString()
    renderApp(<TokenCountdown expiresAt={expiresAt} />)

    const start = screen.getByText('01:30')
    expect(start.tagName).toBe('SPAN')
    expect(start).toHaveClass('kv-num')

    await advance(30_000)
    const halfway = screen.getByText('01:00')
    expect(halfway.tagName).toBe('SPAN')
    expect(halfway).toHaveClass('kv-num')

    await advance(60_000)
    const expired = screen.getByText('00:00')
    expect(expired.tagName).toBe('SPAN')
    expect(expired).toHaveClass('kv-num')

    await advance(5_000) // past the expiry: still 00:00, never negative
    expect(screen.getByText('00:00')).toBeInTheDocument()
  })

  it('renders 00:00 immediately for an already-expired timestamp', () => {
    const expiresAt = new Date(Date.now() - 5_000).toISOString()
    renderApp(<TokenCountdown expiresAt={expiresAt} />)

    const span = screen.getByText('00:00')
    expect(span.tagName).toBe('SPAN')
    expect(span).toHaveClass('kv-num')
  })
})
