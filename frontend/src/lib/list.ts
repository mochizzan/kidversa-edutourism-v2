/**
 * Shared realtime list layer (004a/004b/004e).
 *
 * - `useEventRefetch` — registers `registerRefetch(event)` for a list of SSE
 *   events, always calling the LATEST callback (the 02§2c refetch table).
 * - `useListLoader` — the ONE server-side paged-list primitive (decision 25):
 *   path + params + events → rows/meta/loading/error/reload (soft same-params
 *   refetch). A `null` path means "not ready yet" (no fetch, empty list).
 * - `usePublicStream` — the public-side stream block (02§4): opens
 *   `/public/stream?token=`, maps events to handlers, treats `link.revoked`
 *   as terminal (closes the stream), and coalesces per-event bursts in a
 *   300ms window like StreamGate. Lives here (not lib/stream.ts) because it
 *   is a React hook and lib/stream.ts stays framework-free.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { api, type ListParams, type PageMeta } from './api'
import { openStream, registerRefetch, type SignalPayload } from './stream'

export function useEventRefetch(events: readonly string[], fn: () => void): void {
  const fnRef = useRef(fn)
  useEffect(() => {
    fnRef.current = fn
  })
  const key = events.join(' ')
  useEffect(() => {
    const offs = key
      .split(' ')
      .map((event) => registerRefetch(event, () => fnRef.current()))
    return () => {
      offs.forEach((off) => off())
    }
  }, [key])
}

export interface ListState<T> {
  rows: T[]
  meta: PageMeta | null
  error: unknown
  loading: boolean
  /** Soft refetch with the SAME params (SSE + manual refresh). */
  reload: () => void
}

/**
 * Server-side paged list: holds only the active server page; `events`
 * trigger a soft same-params refetch. Changing params reloads immediately
 * (the owning page resets page=1 on filter changes). `path === null` skips
 * fetching entirely (empty, not loading) until a real path arrives.
 */
export function useListLoader<T>(
  path: string | null,
  params: ListParams,
  events: readonly string[] = [],
): ListState<T> {
  const [rows, setRows] = useState<T[]>([])
  const [meta, setMeta] = useState<PageMeta | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [loading, setLoading] = useState(path !== null)
  const key = JSON.stringify([path, params])
  const loadRef = useRef<() => void>(() => {})

  useEffect(() => {
    if (path === null) return
    let alive = true
    const load = (initial: boolean) => {
      if (initial) setLoading(true)
      api.list<T>(path, params).then(
        (result) => {
          if (!alive) return
          setRows(result.data)
          setMeta(result.meta)
          setError(null)
          setLoading(false)
        },
        (failure: unknown) => {
          if (!alive) return
          setError(failure)
          setLoading(false)
        },
      )
    }
    loadRef.current = () => load(false)
    load(true)
    return () => {
      alive = false
    }
    // `path`/`params` are captured through `key` (their exact serialization).
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])

  const reload = useCallback(() => loadRef.current(), [])
  useEventRefetch(events, reload)

  return { rows, meta, error, loading, reload }
}

const COALESCE_MS = 300

/**
 * Page-level public stream (spec 02§4): opens `/public/stream?token=` while
 * `token` is set, routes each named frame to its handler (per-event 300ms
 * coalescing mirrors StreamGate's debounce), and treats `link.revoked` as
 * terminal — pending timers are dropped, `onRevoked` runs, the stream closes.
 * Passing a null `token` (e.g. once the page flipped invalid) closes it too.
 */
export function usePublicStream(
  token: string | null,
  handlers: Record<string, (data: SignalPayload) => void>,
  onRevoked: () => void,
): void {
  const handlersRef = useRef(handlers)
  useEffect(() => {
    handlersRef.current = handlers
  })
  const revokedRef = useRef(onRevoked)
  useEffect(() => {
    revokedRef.current = onRevoked
  })

  useEffect(() => {
    if (!token) return
    const coalesce = new Map<string, number>()
    const lastRev = new Map<string, number>()
    const stream = openStream(`/public/stream?token=${encodeURIComponent(token)}`, {
      onEvent: (event, data) => {
        if (event === 'link.revoked') {
          for (const timer of coalesce.values()) clearTimeout(timer)
          coalesce.clear()
          revokedRef.current()
          stream.close()
          return
        }
        // Rev gate (02§2c): drop stale/duplicate frames BEFORE the coalesce
        // window. A non-finite rev (no data.rev) passes through ungated.
        const rev = typeof data.rev === 'number' ? data.rev : Number.NaN
        if (Number.isFinite(rev)) {
          const seen = lastRev.get(event)
          if (seen !== undefined && rev <= seen) return // stale/duplicate frame
          lastRev.set(event, rev)
        }
        const handler = handlersRef.current[event]
        if (!handler || coalesce.has(event)) return // coalesce bursts inside the window
        coalesce.set(
          event,
          window.setTimeout(() => {
            coalesce.delete(event)
            handlersRef.current[event]?.(data)
          }, COALESCE_MS),
        )
      },
    })
    return () => {
      for (const timer of coalesce.values()) clearTimeout(timer)
      stream.close()
    }
  }, [token])
}
