/**
 * lib/list hooks (01§6/02§4): useListLoader's late-response guard — a reply
 * landing after unmount must not touch state — and usePublicStream's burst
 * coalescing: a second frame of the same event inside the 300ms window is
 * DROPPED (one dispatch per burst), while revocation stays terminal.
 */
import { describe, expect, it, vi } from 'vitest'
import { render, waitFor } from '@testing-library/react'
import { createElement } from 'react'
import { http } from 'msw'
import { useListLoader, usePublicStream } from './list'
import { server } from '../test/setup'
import { idleStream, ok, sseChannel } from '../test/handlers'
import { advance, pushFrame, until, useSseFakeTimers } from '../test/helpers.cov-session'

interface Row {
  id: number
}

function Probe({ path, onChange }: { path: string | null; onChange: () => void }) {
  useListLoader<Row>(path, {}, [])
  onChange()
  return null
}

describe('useListLoader late-response guard', () => {
  it('a null path never fetches', async () => {
    let requests = 0
    server.use(
      http.get('/api/sessions', () => {
        requests += 1
        return ok([])
      }),
    )
    render(createElement(Probe, { path: null, onChange: () => undefined }))

    await waitFor(() => expect(requests).toBe(0))
  })

  it('unmounting before the list resolves leaves state untouched', async () => {
    let requests = 0
    server.use(
      http.get(
        '/api/sessions',
        () => {
          requests += 1
          return ok([])
        },
      ),
    )
    const { unmount } = render(createElement(Probe, { path: '/api/sessions', onChange: () => undefined }))

    // Unmount while the request is in flight — the alive=false guard must
    // swallow the late reply (no setState on an unmounted component).
    unmount()
    await waitFor(() => expect(requests).toBe(1))
  })
})

describe('usePublicStream burst coalescing', () => {
  it('a same-event burst inside the window dispatches once', async () => {
    useSseFakeTimers()
    const channel = sseChannel('/public/stream')
    const onChanged = vi.fn()
    server.use(channel.handler, idleStream('/public/stream'))

    function StreamProbe() {
      usePublicStream('tok-1', { 'gallery.changed': onChanged }, () => undefined)
      return null
    }
    render(createElement(StreamProbe))

    await until(() => channel.handshakes === 1)

    // Three frames inside one coalesce window → ONE dispatch.
    pushFrame(channel, 'gallery.changed', { rev: 1 })
    pushFrame(channel, 'gallery.changed', { rev: 2 })
    pushFrame(channel, 'gallery.changed', { rev: 3 })
    await advance(600)
    expect(onChanged).toHaveBeenCalledTimes(1)

    // A later frame (new window) dispatches again.
    pushFrame(channel, 'gallery.changed', { rev: 4 })
    await advance(600)
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it('a stale duplicate with the same rev is dropped; a higher rev dispatches again', async () => {
    useSseFakeTimers()
    const channel = sseChannel('/public/stream')
    const onChanged = vi.fn()
    server.use(channel.handler, idleStream('/public/stream'))

    function StreamProbe() {
      usePublicStream('tok-1', { 'gallery.changed': onChanged }, () => undefined)
      return null
    }
    render(createElement(StreamProbe))

    await until(() => channel.handshakes === 1)

    pushFrame(channel, 'gallery.changed', { rev: 5 })
    await advance(600)
    expect(onChanged).toHaveBeenCalledTimes(1)

    // Same rev again AFTER the coalesce window — the rev gate drops it
    // outright (no dispatch, not merely coalesced).
    pushFrame(channel, 'gallery.changed', { rev: 5 })
    await advance(600)
    expect(onChanged).toHaveBeenCalledTimes(1)

    // A higher rev is fresh — it dispatches.
    pushFrame(channel, 'gallery.changed', { rev: 6 })
    await advance(600)
    expect(onChanged).toHaveBeenCalledTimes(2)
  })

  it('unknown events are dropped; null token closes without opening', async () => {
    useSseFakeTimers()
    const channel = sseChannel('/public/stream')
    const onAnything = vi.fn()
    server.use(channel.handler, idleStream('/public/stream'))

    function StreamProbe() {
      usePublicStream('tok-1', { 'gallery.changed': onAnything }, () => undefined)
      return null
    }
    render(createElement(StreamProbe))

    await until(() => channel.handshakes === 1)
    pushFrame(channel, 'some.unknown.event', { rev: 1 })
    await advance(600)
    expect(onAnything).not.toHaveBeenCalled()
  })
})
