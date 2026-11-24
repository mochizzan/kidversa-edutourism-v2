/**
 * SSE stream client (spec 01§8 / 02§2c / 06§5, FL-02): fetch+reader field
 * parsing (`event:`/`data:`/`id:`/`retry:`/`: comment`), heartbeat comments,
 * auth.expiring refresh-once + reconnect, onReconnect restart hook, backoff,
 * and close/one-stream-per-tab cleanup. Fetch is stubbed per connection and
 * ALL timers are fake.
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setAccessToken, setSessionExpiredHandler, setSessionSchool } from './api'
import { openStream } from './stream'

interface StreamRequest {
  url: string
  headers: Record<string, string>
  aborted: boolean
}

type Handshake =
  | 'stream'
  | 'rate-limited'
  | 'token-invalid'
  | 'invalid-token'
  | 'forbidden-role'
  | 'school-inactive'
  | 'account-inactive'
  | 'school-mismatch'
  | 'broken'
  | 'network-down'

const encoder = new TextEncoder()

/** Per-test fake fetch: scripted handshakes for /api/stream, push-able bodies. */
function installFakeFetch(script: Handshake[] = []) {
  const requests: StreamRequest[] = []
  const connections: Array<{ push(text: string): void; end(): void }> = []
  const pending = [...script]

  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const headers: Record<string, string> = {}
    new Headers(init?.headers).forEach((value, key) => {
      headers[key.toLowerCase()] = value
    })
    const record: StreamRequest = { url: String(input), headers, aborted: false }
    requests.push(record)
    init?.signal?.addEventListener('abort', () => {
      record.aborted = true
    })

    const handshake = pending.shift() ?? 'stream'
    if (handshake === 'rate-limited') {
      return Response.json(
        { error: { code: 'RATE_LIMITED', message: 'slow down' } },
        { headers: { 'Retry-After': '2' } },
      )
    }
    if (handshake === 'token-invalid') {
      return Response.json({ error: { code: 'TOKEN_INVALID', message: 'revoked' } })
    }
    if (handshake === 'invalid-token') {
      return Response.json({ error: { code: 'INVALID_TOKEN', message: 'expired' } })
    }
    if (handshake === 'forbidden-role') {
      return Response.json({ error: { code: 'FORBIDDEN_ROLE', message: 'no access' } })
    }
    if (handshake === 'school-inactive') {
      return Response.json({ error: { code: 'SCHOOL_INACTIVE', message: 'disabled' } })
    }
    if (handshake === 'account-inactive') {
      return Response.json({ error: { code: 'ACCOUNT_INACTIVE', message: 'inactive' } })
    }
    if (handshake === 'school-mismatch') {
      return Response.json({ error: { code: 'SCHOOL_MISMATCH', message: 'wrong school' } })
    }
    if (handshake === 'broken') {
      return Response.json({ error: { code: 'INTERNAL', message: 'busy' } })
    }
    if (handshake === 'network-down') {
      throw new TypeError('fetch failed')
    }

    let controller!: ReadableStreamDefaultController<Uint8Array>
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        controller = c
      },
    })
    init?.signal?.addEventListener('abort', () => {
      try {
        controller.error(new DOMException('aborted', 'AbortError'))
      } catch {
        // already closed/errored
      }
    })
    const conn = {
      push(text: string) {
        try {
          controller.enqueue(encoder.encode(text))
        } catch {
          // connection already torn down
        }
      },
      end() {
        try {
          controller.close()
        } catch {
          // already closed
        }
      },
    }
    connections.push(conn)
    return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
  })

  vi.stubGlobal('fetch', mock)
  return { requests, connections }
}

/** Drain microtasks + 1ms fake-timer steps until `condition` (bounded, no sleeps). */
async function until(condition: () => boolean, limit = 5000): Promise<void> {
  for (let i = 0; i < limit && !condition(); i += 1) {
    await vi.advanceTimersByTimeAsync(1)
  }
  expect(condition()).toBe(true)
}

const flush = async () => {
  await vi.advanceTimersByTimeAsync(0)
  await vi.advanceTimersByTimeAsync(0)
}

describe('sse stream parsing and lifecycle', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    setAccessToken(null)
  })
  afterEach(() => {
    setAccessToken(null)
  })

  it('parses event/data/id fields and reports the rev (data.rev wins over id)', async () => {
    const { requests, connections } = installFakeFetch()
    const onEvent = vi.fn()
    const onRev = vi.fn()
    const handle = openStream('/api/stream', { onEvent, onRev })
    await until(() => requests.length === 1)

    connections[0].push('event: program.changed\ndata: {"rev":5,"entity":"program"}\nid: 42\n\n')
    await flush()
    expect(onEvent).toHaveBeenCalledWith('program.changed', { rev: 5, entity: 'program' })
    expect(onRev).toHaveBeenCalledWith(5)

    // No data.rev → the id field drives the rev.
    connections[0].push('event: session.status\ndata: {"entity":"session"}\nid: 43\n\n')
    await flush()
    expect(onRev).toHaveBeenLastCalledWith(43)
    expect(onEvent).toHaveBeenLastCalledWith('session.status', { entity: 'session' })
    handle.close()
  })

  it('joins multi-line data and falls back to raw for non-JSON payloads', async () => {
    const { requests, connections } = installFakeFetch()
    const onEvent = vi.fn()
    const handle = openStream('/api/stream', { onEvent })
    await until(() => requests.length === 1)

    connections[0].push('data: {"rev":\ndata: 1}\n\n')
    await flush()
    expect(onEvent).toHaveBeenCalledWith('message', { rev: 1 })

    connections[0].push('data: not-json\n\n')
    await flush()
    expect(onEvent).toHaveBeenLastCalledWith('message', { raw: 'not-json' })
    handle.close()
  })

  it('ignores comment-only heartbeat frames without dispatching an event', async () => {
    const { requests, connections } = installFakeFetch()
    const onEvent = vi.fn()
    const handle = openStream('/api/stream', { onEvent })
    await until(() => requests.length === 1)

    connections[0].push(': ping\n\n')
    connections[0].push(': ping\n\n')
    await flush()
    expect(onEvent).not.toHaveBeenCalled()
    handle.close()
  })

  it('a heartbeat comment resets the backoff step after a failed attempt', async () => {
    const { requests, connections } = installFakeFetch(['broken'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    // First handshake fails transiently → one failure step is armed.
    await until(() => requests.length === 2)

    // Second connection delivers only a heartbeat, then drops: the comment
    // reset the failure counter, so the next wait is the base 1000ms —
    // NOT the doubled 2000ms step the armed counter would demand.
    connections[0].push(': ping\n\n')
    await flush()
    connections[0].end()
    await flush()
    await vi.advanceTimersByTimeAsync(999)
    expect(requests).toHaveLength(2)
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(requests).toHaveLength(3)
    handle.close()
  })

  it('honours a retry: field for the reconnect delay', async () => {
    const { requests, connections } = installFakeFetch()
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    connections[0].push('retry: 5000\n\n')
    await flush()
    connections[0].end()
    await flush()
    await vi.advanceTimersByTimeAsync(4999)
    expect(requests).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(requests).toHaveLength(2)
    handle.close()
  })

  it('auth.expiring triggers the refresh callback and reconnects with the new Bearer + last id', async () => {
    setAccessToken('old-token')
    const { requests, connections } = installFakeFetch()
    const onEvent = vi.fn()
    const onAuthExpiring = vi.fn(async () => {
      setAccessToken('fresh-token')
      return true
    })
    const handle = openStream('/api/stream', { onEvent, onAuthExpiring })
    await until(() => requests.length === 1)

    connections[0].push('event: session.status\ndata: {"rev":1}\nid: 7\n\n')
    await flush()
    connections[0].push('event: auth.expiring\ndata: {}\n\n')
    await until(() => requests.length === 2)

    expect(onAuthExpiring).toHaveBeenCalledTimes(1)
    expect(onEvent).not.toHaveBeenCalledWith('auth.expiring', expect.anything())
    expect(requests[1].headers.authorization).toBe('Bearer fresh-token')
    expect(requests[1].headers['last-event-id']).toBe('7')
    handle.close()
  })

  it('auth.expiring returning false keeps the current connection open', async () => {
    const { requests, connections } = installFakeFetch()
    const onAuthExpiring = vi.fn(async () => false)
    const handle = openStream('/api/stream', { onEvent: vi.fn(), onAuthExpiring })
    await until(() => requests.length === 1)

    connections[0].push('event: auth.expiring\ndata: {}\n\n')
    await flush()
    await vi.advanceTimersByTimeAsync(2000)
    expect(onAuthExpiring).toHaveBeenCalledTimes(1)
    expect(requests).toHaveLength(1) // no reconnect
    handle.close()
  })

  it('fires onReconnect on the working handshake AFTER a drop (restart hook)', async () => {
    const { requests, connections } = installFakeFetch()
    const onReconnect = vi.fn()
    const handle = openStream('/api/stream', { onEvent: vi.fn(), onReconnect })
    await until(() => requests.length === 1)
    expect(onReconnect).not.toHaveBeenCalled() // first handshake never counts

    connections[0].end()
    await until(() => requests.length === 2)
    expect(onReconnect).toHaveBeenCalledTimes(1)
    handle.close()
  })

  it('close() aborts the connection and stops every reconnect', async () => {
    const { requests } = installFakeFetch()
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    handle.close()
    expect(requests[0].aborted).toBe(true)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(requests).toHaveLength(1)
  })

  it('opening a new stream closes the previous one (one stream per tab)', async () => {
    const { requests } = installFakeFetch()
    openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    const second = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 2)
    expect(requests[0].aborted).toBe(true)

    // The dead first connection never reconnects, the second one lives.
    await vi.advanceTimersByTimeAsync(60_000)
    expect(requests).toHaveLength(2)
    second.close()
  })

  it('RATE_LIMITED handshake waits for Retry-After before reconnecting', async () => {
    const { requests } = installFakeFetch(['rate-limited'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    await vi.advanceTimersByTimeAsync(1999)
    expect(requests).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    await flush()
    expect(requests).toHaveLength(2)
    handle.close()
  })

  it('TOKEN_INVALID handshake is terminal — the stream never retries', async () => {
    const { requests } = installFakeFetch(['token-invalid'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    await vi.advanceTimersByTimeAsync(60_000)
    expect(requests).toHaveLength(1)
    handle.close()
  })

  it('FORBIDDEN_ROLE handshake is terminal — closed, no reconnect scheduled', async () => {
    const { requests } = installFakeFetch(['forbidden-role'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    await vi.advanceTimersByTimeAsync(60_000)
    expect(requests).toHaveLength(1)
    expect(requests[0].aborted).toBe(true) // handle.close() ran: no retry loop
    handle.close()
  })

  it('SCHOOL_INACTIVE handshake is terminal — closed, no reconnect scheduled', async () => {
    const { requests } = installFakeFetch(['school-inactive'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    await vi.advanceTimersByTimeAsync(60_000)
    expect(requests).toHaveLength(1)
    expect(requests[0].aborted).toBe(true)
    handle.close()
  })

  it('ACCOUNT_INACTIVE handshake invokes the session-expired handler and closes', async () => {
    const expired: string[] = []
    setSessionExpiredHandler(() => expired.push('expired'))
    const { requests } = installFakeFetch(['account-inactive'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => expired.length === 1) // handshake processed, handler ran

    expect(expired).toEqual(['expired'])
    await vi.advanceTimersByTimeAsync(60_000)
    expect(requests).toHaveLength(1)
    expect(requests[0].aborted).toBe(true)
    handle.close()
    setSessionExpiredHandler(null)
  })

  it('SCHOOL_MISMATCH heals the kv-school drift and reconnects ONCE with the corrected header', async () => {
    localStorage.setItem('kv-school', 'wrong')
    setSessionSchool(7)
    const { requests } = installFakeFetch(['school-mismatch'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 2)

    expect(localStorage.getItem('kv-school')).toBe('7') // drift healed
    expect(requests[0].headers['x-school-id']).toBe('wrong')
    expect(requests[1].headers['x-school-id']).toBe('7') // healed retry
    await vi.advanceTimersByTimeAsync(60_000)
    expect(requests).toHaveLength(2) // the healed handshake connects: no loop
    handle.close()
    setSessionSchool(null)
  })

  it('a second SCHOOL_MISMATCH after the healed attempt is terminal', async () => {
    localStorage.setItem('kv-school', 'wrong')
    setSessionSchool(7)
    const { requests } = installFakeFetch(['school-mismatch', 'school-mismatch'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 2)

    // The healed retry ALSO mismatches → close; no third attempt ever.
    await vi.advanceTimersByTimeAsync(60_000)
    expect(requests).toHaveLength(2)
    expect(requests[1].aborted).toBe(true)
    handle.close()
    setSessionSchool(null)
  })

  it('SCHOOL_MISMATCH with no kv-school drift is terminal (nothing to heal)', async () => {
    setSessionSchool(7)
    localStorage.setItem('kv-school', '7')
    const { requests } = installFakeFetch(['school-mismatch'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    await vi.advanceTimersByTimeAsync(60_000)
    expect(requests).toHaveLength(1)
    expect(requests[0].aborted).toBe(true)
    handle.close()
    setSessionSchool(null)
  })

  it('INVALID_TOKEN handshake refreshes once, then closes for good on a second one', async () => {
    const { requests } = installFakeFetch(['invalid-token', 'invalid-token'])
    const onAuthExpiring = vi.fn(async () => true)
    const handle = openStream('/api/stream', { onEvent: vi.fn(), onAuthExpiring })
    await until(() => requests.length === 2)

    await vi.advanceTimersByTimeAsync(60_000)
    expect(onAuthExpiring).toHaveBeenCalledTimes(1)
    expect(requests).toHaveLength(2) // refresh-once, no third attempt
    handle.close()
  })

  it('transient handshake failures retry at the doubling backoff step', async () => {
    const { requests } = installFakeFetch(['broken'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(requests).toHaveLength(2)
    handle.close()
  })

  it('a fetch-level network failure reschedules the reconnect', async () => {
    const { requests } = installFakeFetch(['network-down'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    // The transport throw is a transient fault — one backoff step lands.
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(requests).toHaveLength(2)
    handle.close()
  })

  it('an INVALID_TOKEN handshake whose refresh THROWS retries later (transient)', async () => {
    const { requests } = installFakeFetch(['invalid-token', 'invalid-token'])
    const onAuthExpiring = vi.fn(async () => {
      throw new Error('refresh endpoint down')
    })
    const handle = openStream('/api/stream', { onEvent: vi.fn(), onAuthExpiring })
    await until(() => requests.length === 1)

    // The refresh callback failed → no Bearer swap, but the stream retries
    // (and per the transient rule, may retry the refresh on the next step).
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(onAuthExpiring).toHaveBeenCalled()
    expect(requests.length).toBeGreaterThanOrEqual(2)
    handle.close()
  })

  it('an INVALID_TOKEN handshake without a refresh callback just closes', async () => {
    const { requests } = installFakeFetch(['invalid-token'])
    const handle = openStream('/api/stream', { onEvent: vi.fn() })
    await until(() => requests.length === 1)

    // No onAuthExpiring → nothing to refresh with → terminal, no retry.
    await vi.advanceTimersByTimeAsync(1000)
    await flush()
    expect(requests).toHaveLength(1)
    handle.close()
  })
})
