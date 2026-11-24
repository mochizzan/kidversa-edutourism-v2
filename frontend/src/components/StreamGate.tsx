/**
 * StreamGate — owns the authenticated tab's SINGLE SSE connection
 * (spec 02§2c / F-16). Opens `GET /api/stream` once auth is ready, closes on
 * unmount / school switch / logout (the effect keys on user + school).
 *
 * Routing table (02§2c):
 * - full-value exceptions (`readiness.changed`, `pending.changed`,
 *   `wa.changed`) → emitted immediately to registerSignal listeners;
 * - every other event → ignored unless a page registered a refetch for that
 *   event name, rev-gated per table (`rev <= last_seen_rev` dropped), then
 *   debounced/coalesced 300ms before the registered callbacks re-run.
 * - `auth.expiring` is handled inside lib/stream (refresh-once → reconnect).
 */
import { useEffect } from 'react'
import { refreshAccessToken } from '../lib/api'
import { useAuth } from '../lib/auth'
import {
  emitSignal,
  hasListeners,
  openStream,
  refetchEvents,
  type SignalPayload,
} from '../lib/stream'

const VALUE_EVENTS = new Set(['readiness.changed', 'pending.changed', 'wa.changed'])
const DEBOUNCE_MS = 300

export default function StreamGate() {
  const { user, school } = useAuth()

  useEffect(() => {
    if (!user || !school) return
    const lastSeenRev = new Map<string, number>()
    const debounceTimers = new Map<string, number>()
    // Resolved rev of the frame currently being routed: onRev is called
    // synchronously immediately before onEvent with data.rev ?? id; onEvent
    // consumes it (one frame → one rev, then reset to NaN). Control frames
    // (`auth.expiring`) and comment-only frames call neither — safe.
    let frameRev = Number.NaN

    const scheduleRefetch = (event: string, data: SignalPayload): void => {
      if (debounceTimers.has(event)) return // coalesce bursts inside the window
      debounceTimers.set(
        event,
        window.setTimeout(() => {
          debounceTimers.delete(event)
          emitSignal(event, data)
        }, DEBOUNCE_MS),
      )
    }

    const handle = openStream('/api/stream', {
      onRev(rev) {
        frameRev = rev
      },
      onReconnect() {
        // W1 restart rule: the server's rev counter/ring may have reset while we
        // were away — drop every gate and full-refetch the open pages once;
        // replayed events arriving within the window coalesce per table.
        lastSeenRev.clear()
        for (const event of refetchEvents()) scheduleRefetch(event, {})
      },
      onEvent(event, data: SignalPayload) {
        if (!hasListeners(event)) {
          frameRev = Number.NaN // consume the paired onRev: never leak across frames
          return // unknown event or closed page → ignore
        }
        const rev = typeof data.rev === 'number' ? data.rev : frameRev
        frameRev = Number.NaN
        // Rev gate ABOVE the full-value branch (02§2c): a stale readiness/
        // pending/wa.changed must not overwrite fresher state either.
        if (Number.isFinite(rev)) {
          const seen = lastSeenRev.get(event)
          if (seen !== undefined && rev <= seen) return // stale/duplicate signal
          lastSeenRev.set(event, rev) // value + refetch events share one gate
        }
        if (VALUE_EVENTS.has(event)) {
          emitSignal(event, data) // full-value exception: render directly
          return
        }
        scheduleRefetch(event, data)
      },
      onAuthExpiring: async () => {
        try {
          return (await refreshAccessToken()) !== null
        } catch {
          return false // transient refresh failure: keep the current connection
        }
      },
    })

    return () => {
      handle.close()
      for (const timer of debounceTimers.values()) clearTimeout(timer)
      lastSeenRev.clear()
      debounceTimers.clear()
    }
  }, [user, school])

  return null
}
