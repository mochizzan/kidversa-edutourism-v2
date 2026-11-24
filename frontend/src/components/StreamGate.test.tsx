/**
 * StreamGate (spec 02§2c routing table): opens the tab stream once auth is
 * ready, rev-gates per table, debounces/coalesces refetches 300ms, dispatches
 * full-value signals immediately, full-refetches after a reconnect (rev
 * reset), refreshes on auth.expiring, and closes on unmount.
 * All timers fake; fetch stubbed (boot refresh + stream).
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { act, type RenderResult } from '@testing-library/react'
import { setAccessToken } from '../lib/api'
import { emitSignal, registerRefetch, registerSignal } from '../lib/stream'
import StreamGate from './StreamGate'
import { renderApp } from '../test/render'
import { makeLoginData } from '../test/handlers'

interface TrackedRequest {
  url: string
  headers: Record<string, string>
  aborted: boolean
}

function installGateFetch(nextRefreshAccess = 'next-access') {
  const encoder = new TextEncoder()
  const requests: TrackedRequest[] = []
  const controllers: Array<ReadableStreamDefaultController<Uint8Array>> = []
  let refreshes = 0

  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
      const headers: Record<string, string> = {}
      new Headers(init?.headers).forEach((value, key) => {
        headers[key.toLowerCase()] = value
      })
      const record: TrackedRequest = { url: String(input), headers, aborted: false }
      requests.push(record)
      init?.signal?.addEventListener('abort', () => {
        record.aborted = true
      })

      if (record.url.includes('/api/auth/refresh')) {
        // First refresh = boot restore (session access); later ones rotate.
        refreshes += 1
        const session = makeLoginData({
          access: refreshes === 1 ? makeLoginData().access : nextRefreshAccess,
        })
        return Response.json({ data: session })
      }

      const body = new ReadableStream<Uint8Array>({
        start(controller) {
          controllers.push(controller)
          init?.signal?.addEventListener('abort', () => {
            try {
              controller.error(new DOMException('aborted', 'AbortError'))
            } catch {
              // already closed/errored
            }
          })
        },
      })
      return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } })
    }),
  )

  const streamRequests = () => requests.filter((r) => r.url.includes('/api/stream'))
  return {
    requests,
    streamCount: () => streamRequests().length,
    lastStreamRequest: () => streamRequests().at(-1),
    push(chunk: string) {
      controllers.at(-1)?.enqueue(encoder.encode(chunk))
    },
    end() {
      try {
        controllers.at(-1)?.close()
      } catch {
        // already closed
      }
    },
  }
}

/** React state (session restore) updates during these → wrap in act. */
async function until(condition: () => boolean, step = 50, limit = 400): Promise<void> {
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

function openGate(): RenderResult {
  return renderApp(<StreamGate />, { session: makeLoginData() })
}

describe('sse StreamGate', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    setAccessToken(null)
  })

  it('coalesces a signal burst into ONE refetch after the 300ms debounce', async () => {
    const gate = installGateFetch()
    const spy = vi.fn()
    const off = registerRefetch('program.changed', spy)
    openGate()
    await until(() => gate.streamCount() === 1)

    gate.push('event: program.changed\ndata: {"rev":1}\n\n')
    gate.push('event: program.changed\ndata: {"rev":2}\n\n')
    gate.push('event: program.changed\ndata: {"rev":3}\n\n')

    await advance(299)
    expect(spy).not.toHaveBeenCalled()
    await advance(1)
    expect(spy).toHaveBeenCalledTimes(1)
    await advance(1000)
    expect(spy).toHaveBeenCalledTimes(1) // burst stayed one refetch
    off()
  })

  it('drops rev <= last_seen signals without a refetch', async () => {
    const gate = installGateFetch()
    const spy = vi.fn()
    const off = registerRefetch('session.changed', spy)
    openGate()
    await until(() => gate.streamCount() === 1)

    gate.push('event: session.changed\ndata: {"rev":5}\n\n')
    await advance(300)
    expect(spy).toHaveBeenCalledTimes(1)

    gate.push('event: session.changed\ndata: {"rev":5}\n\n') // duplicate
    await advance(300)
    gate.push('event: session.changed\ndata: {"rev":4}\n\n') // stale
    await advance(300)
    expect(spy).toHaveBeenCalledTimes(1)

    gate.push('event: session.changed\ndata: {"rev":6}\n\n') // fresh
    await advance(300)
    expect(spy).toHaveBeenCalledTimes(2)
    off()
  })

  it('dispatches full-value signals immediately, without the debounce', async () => {
    const gate = installGateFetch()
    const spy = vi.fn()
    const off = registerSignal<{ connected: boolean; rev: number }>('wa.changed', spy)
    openGate()
    await until(() => gate.streamCount() === 1)

    gate.push('event: wa.changed\ndata: {"connected":false,"rev":9}\n\n')
    await advance(0)
    expect(spy).toHaveBeenCalledTimes(1)
    expect(spy).toHaveBeenCalledWith({ connected: false, rev: 9 })
    off()
  })

  it('rev-gates full-value signals: a stale readiness.changed cannot overwrite fresher state', async () => {
    const gate = installGateFetch()
    const spy = vi.fn()
    const off = registerSignal<{ ready: boolean; rev: number }>('readiness.changed', spy)
    openGate()
    await until(() => gate.streamCount() === 1)

    gate.push('event: readiness.changed\ndata: {"ready":true,"rev":5}\n\n')
    await advance(0)
    expect(spy).toHaveBeenCalledTimes(1)

    gate.push('event: readiness.changed\ndata: {"ready":false,"rev":3}\n\n') // stale (≤ seen)
    await advance(0)
    expect(spy).toHaveBeenCalledTimes(1) // no overwrite

    gate.push('event: readiness.changed\ndata: {"ready":true,"rev":6}\n\n') // fresh
    await advance(0)
    expect(spy).toHaveBeenCalledTimes(2)
    off()
  })

  it('ignores frames for events nobody listens to — without polluting rev gates', async () => {
    const gate = installGateFetch()
    openGate()
    await until(() => gate.streamCount() === 1)

    gate.push('event: photo.changed\ndata: {"rev":9}\n\n') // no listener yet
    await advance(400)

    const spy = vi.fn()
    const off = registerRefetch('photo.changed', spy)
    gate.push('event: photo.changed\ndata: {"rev":1}\n\n') // would drop IF rev 9 were kept
    await advance(300)
    expect(spy).toHaveBeenCalledTimes(1)
    off()
  })

  it('reconnect resets rev gates and full-refetches open pages exactly once', async () => {
    const gate = installGateFetch()
    const spy = vi.fn()
    const off = registerRefetch('program.changed', spy)
    openGate()
    await until(() => gate.streamCount() === 1)

    gate.push('event: program.changed\ndata: {"rev":10}\n\n')
    await advance(300)
    expect(spy).toHaveBeenCalledTimes(1)

    gate.end() // drop the connection → backoff reconnect
    await until(() => gate.streamCount() === 2)
    await advance(300)
    expect(spy).toHaveBeenCalledTimes(2) // W1 restart rule: one full refetch

    gate.push('event: program.changed\ndata: {"rev":2}\n\n') // ≤ old gate, gates were cleared
    await advance(300)
    expect(spy).toHaveBeenCalledTimes(3)
    off()
  })

  it('auth.expiring refreshes the token and reconnects with the new Bearer', async () => {
    const gate = installGateFetch('fresh-bearer')
    openGate()
    await until(() => gate.streamCount() === 1)
    expect(gate.lastStreamRequest()?.headers.authorization).toBe('Bearer access-token-1')

    gate.push('event: session.status\ndata: {"rev":1}\nid: 7\n\n')
    await advance(0)
    gate.push('event: auth.expiring\ndata: {}\n\n')
    await until(() => gate.streamCount() === 2)

    const reconnected = gate.lastStreamRequest()
    expect(reconnected?.headers.authorization).toBe('Bearer fresh-bearer')
    expect(reconnected?.headers['last-event-id']).toBe('7')
    const refreshes = gate.requests.filter((r) => r.url.includes('/api/auth/refresh'))
    expect(refreshes).toHaveLength(2) // boot restore + auth.expiring rotation
  })

  it('unmount closes the stream and stops reconnection', async () => {
    const gate = installGateFetch()
    const result = openGate()
    await until(() => gate.streamCount() === 1)

    result.unmount()
    expect(gate.lastStreamRequest()?.aborted).toBe(true)
    await advance(60_000)
    expect(gate.streamCount()).toBe(1)
  })
})

describe('sse StreamGate registry', () => {
  it('emitSignal fans out to every registered listener of an event', () => {
    const first = vi.fn()
    const second = vi.fn()
    const offFirst = registerRefetch('stats.changed', first)
    const offSecond = registerRefetch('stats.changed', second)

    emitSignal('stats.changed', { rev: 1 })
    expect(first).toHaveBeenCalledTimes(1)
    expect(second).toHaveBeenCalledTimes(1)

    offFirst()
    emitSignal('stats.changed', { rev: 2 })
    expect(first).toHaveBeenCalledTimes(1) // unregistered
    expect(second).toHaveBeenCalledTimes(2)
    offSecond()
    emitSignal('stats.changed', { rev: 3 })
    expect(second).toHaveBeenCalledTimes(2)
  })
})
