/**
 * Minimal SSE client for Kidversa (spec 01§8, 02§2c, 06§5).
 *
 * Transport: fetch + ReadableStream reader — NOT native EventSource, which
 * cannot send Authorization / X-School-ID headers. Parses `event:`/`data:`/
 * `id:`/`retry:` fields and `:ping` heartbeat comments. Auto-reconnects with
 * backoff 1s → 30s carrying `Last-Event-ID`; `retry:` (ms) from the server
 * overrides the doubling (clamped to the same window). ONE active stream per
 * tab: opening a new stream closes the previous one.
 *
 * This module also hosts the signal registry used by StreamGate and pages:
 * `registerRefetch(event, fn)` → debounced/rev-gated refetch of the page that
 * owns the event; `registerSignal(event, fn)` → full-value exceptions
 * (readiness.changed / pending.changed / wa.changed) rendered directly.
 */
import { getAccessToken, healSchoolHeader, notifySessionExpired } from './api'

export type SignalPayload = Record<string, unknown> & { rev?: number }

export interface StreamOptions {
  /** Extra handshake headers; auth headers are added automatically. */
  headers?: Record<string, string>
  /** Called for every named event frame (control frame `auth.expiring` excluded). */
  onEvent: (event: string, data: SignalPayload) => void
  /** Called before onEvent with the frame's numeric rev (data.rev, else id). */
  onRev?: (rev: number) => void
  /** Last-Event-ID source for reconnect; defaults to the last id seen on this connection. */
  getLastId?: () => string | null
  /**
   * Fired when a connection attempt AFTER the first achieves a working
   * event-stream handshake. The server's rev counter/ring may have restarted
   * while we were away — W1 restart rule: the gate owner drops its rev gates
   * and full-refetches the active page once (replayed/new events inside the
   * 300ms window coalesce per table).
   */
  onReconnect?: () => void
  /**
   * Handle `auth.expiring` (T-60s): refresh the access token. `true` → the
   * stream reconnects immediately with the new Bearer + last id; `false` →
   * the current connection stays open (its token was accepted at handshake)
   * and the next reconnect re-attempts the refresh.
   */
  onAuthExpiring?: () => Promise<boolean>
}

export interface StreamHandle {
  close(): void
}

type SignalListener = (data: SignalPayload) => void

interface ListenerEntry {
  listener: SignalListener
  /** true for registerRefetch entries (table refetches), false for value signals. */
  refetch: boolean
}

const listeners = new Map<string, Set<ListenerEntry>>()

function addListener(event: string, entry: ListenerEntry): () => void {
  const set = listeners.get(event) ?? new Set<ListenerEntry>()
  set.add(entry)
  listeners.set(event, set)
  return () => {
    set.delete(entry)
    if (set.size === 0) listeners.delete(event)
  }
}

/**
 * Register the refetch for an SSE event name (02§2c routing table keys such as
 * `program.changed`, `session.status`, `photo.changed`). Unregister on unmount.
 */
export function registerRefetch(event: string, fn: () => void): () => void {
  return addListener(event, { listener: () => fn(), refetch: true })
}

/** Register a handler for a full-value event (payload passed through as-is). */
export function registerSignal<T>(event: string, fn: (data: T) => void): () => void {
  return addListener(event, { listener: fn as unknown as SignalListener, refetch: false })
}

/** True when at least one page/component listens for this event (closed pages are ignored). */
export function hasListeners(event: string): boolean {
  return (listeners.get(event)?.size ?? 0) > 0
}

/** Events that currently have at least one registered refetch (the open pages' tables). */
export function refetchEvents(): string[] {
  const events: string[] = []
  for (const [event, set] of listeners) {
    for (const entry of set) {
      if (entry.refetch) {
        events.push(event)
        break
      }
    }
  }
  return events
}

/** Invoke every listener registered for the event (StreamGate's routing table drives this). */
export function emitSignal(event: string, data: SignalPayload): void {
  for (const entry of listeners.get(event) ?? []) entry.listener(data)
}

const BACKOFF_MIN = 1000
const BACKOFF_MAX = 30000
const FRAME_END = /\r?\n\r?\n/

let active: StreamHandle | null = null

/**
 * Open the tab's stream. Any previously active stream is closed first
 * (one stream per tab, spec 02§3). The returned handle closes it.
 */
export function openStream(path: string, options: StreamOptions): StreamHandle {
  active?.close()
  const handle = connect(path, options)
  active = handle
  return handle
}

function connect(path: string, options: StreamOptions): StreamHandle {
  let closed = false
  let controller: AbortController | null = null
  let reconnectTimer: number | undefined = undefined
  let attempt = 0
  let retryMs: number | null = null
  let lastId: string | null = null
  let refreshedAuth = false
  let schoolHealed = false
  let connections = 0

  const handle: StreamHandle = {
    close() {
      closed = true
      clearTimeout(reconnectTimer)
      controller?.abort()
      if (active === handle) active = null
    },
  }

  const backoffDelay = (): number =>
    Math.min(BACKOFF_MAX, Math.max(BACKOFF_MIN, retryMs ?? BACKOFF_MIN * 2 ** attempt))

  const scheduleReconnect = (delay: number): void => {
    if (closed) return
    clearTimeout(reconnectTimer)
    reconnectTimer = setTimeout(() => {
      reconnectTimer = undefined
      void openOnce()
    }, delay)
  }

  /** Retry at the current backoff step and grow it for the next failure. */
  const reconnectLater = (): void => {
    scheduleReconnect(backoffDelay())
    attempt += 1
  }

  async function handleFrame(block: string): Promise<boolean> {
    let event = ''
    let id: string | null = null
    const dataLines: string[] = []
    for (const line of block.split(/\r?\n/)) {
      if (line.startsWith(':')) {
        attempt = 0 // any comment is the heartbeat (`: ping` per W1): connection healthy
        continue
      }
      const colon = line.indexOf(':')
      const field = colon === -1 ? line : line.slice(0, colon)
      let value = colon === -1 ? '' : line.slice(colon + 1)
      if (value.startsWith(' ')) value = value.slice(1)
      if (field === 'event') event = value
      else if (field === 'data') dataLines.push(value)
      else if (field === 'id') id = value
      else if (field === 'retry') {
        const ms = Number(value)
        if (Number.isFinite(ms) && ms > 0) retryMs = ms
      }
    }
    if (id !== null) lastId = id
    if (dataLines.length === 0 && event === '') return false // comment-only frame

    attempt = 0 // any complete frame ⇒ server reachable
    let data: SignalPayload = {}
    if (dataLines.length > 0) {
      const raw = dataLines.join('\n')
      try {
        data = JSON.parse(raw) as SignalPayload
      } catch {
        data = { raw }
      }
    }

    if (event === 'auth.expiring') {
      // Control frame: refresh; only a successful refresh forces a reconnect.
      try {
        return (await options.onAuthExpiring?.()) ?? false
      } catch {
        return false
      }
    }

    const numericId = id === null ? Number.NaN : Number(id)
    const rev = typeof data.rev === 'number' ? data.rev : numericId
    if (Number.isFinite(rev)) options.onRev?.(rev)
    options.onEvent(event === '' ? 'message' : event, data)
    return false
  }

  async function openOnce(): Promise<void> {
    if (closed) return
    controller = new AbortController()
    const school = localStorage.getItem('kv-school')
    const token = getAccessToken()
    const resumeId = options.getLastId?.() ?? lastId
    let res: Response
    try {
      res = await fetch(path, {
        headers: {
          Accept: 'text/event-stream',
          ...(token ? { Authorization: `Bearer ${token}` } : {}),
          ...(school ? { 'X-School-ID': school } : {}),
          ...(resumeId ? { 'Last-Event-ID': resumeId } : {}),
          ...options.headers,
        },
        signal: controller.signal,
        cache: 'no-store',
      })
    } catch {
      if (!closed) reconnectLater()
      return
    }

    if (!(res.headers.get('Content-Type') ?? '').includes('text/event-stream')) {
      // Handshake failure = a normal 200 envelope (spec 01§8); the stream never opens.
      let code = 'INTERNAL'
      try {
        const body = (await res.json()) as { error?: { code?: string } }
        if (body?.error?.code) code = body.error.code
      } catch {
        // Non-envelope body → retry like a transient fault.
      }
      if (code === 'INVALID_TOKEN') {
        // Spec 06: refresh-once, then reconnect with the new Bearer; a second
        // INVALID_TOKEN after a successful refresh means re-login → close.
        if (refreshedAuth || !options.onAuthExpiring) {
          handle.close()
          return
        }
        let refreshed = false
        try {
          refreshed = await options.onAuthExpiring()
        } catch {
          refreshed = false
        }
        if (refreshed) {
          refreshedAuth = true
          scheduleReconnect(0)
        } else {
          reconnectLater() // transient refresh failure — may retry the refresh next step
        }
        return
      }
      if (code === 'RATE_LIMITED') {
        const wait = res.headers.get('Retry-After')
        const seconds = Number(wait)
        scheduleReconnect((Number.isFinite(seconds) ? seconds : 30) * 1000)
        return
      }
      if (code === 'TOKEN_INVALID') {
        handle.close() // revoked public link: terminal (spec 02§3)
        return
      }
      if (code === 'ACCOUNT_INACTIVE') {
        // Session is dead (mirrors the REST ACCOUNT_INACTIVE path): report it
        // and terminate — the auth context clears and routes to re-login.
        notifySessionExpired()
        handle.close()
        return
      }
      if (code === 'SCHOOL_MISMATCH') {
        // Spec 06 E7 self-heal (same helper as the REST retry): restore the
        // drifted kv-school header and reconnect ONCE with it. A second
        // mismatch — or a mismatch with no drift to heal — is terminal:
        // retrying on a wrong school must never loop.
        if (!schoolHealed && healSchoolHeader()) {
          schoolHealed = true
          scheduleReconnect(0)
          return
        }
        handle.close()
        return
      }
      if (code === 'FORBIDDEN_ROLE' || code === 'SCHOOL_INACTIVE') {
        handle.close() // terminal verdicts: retrying cannot change them
        return
      }
      reconnectLater()
      return
    }

    refreshedAuth = false // a working handshake re-arms the refresh-once path
    schoolHealed = false // …and re-arms the SCHOOL_MISMATCH self-heal
    if (connections > 0) options.onReconnect?.() // W1 restart rule hook
    connections += 1
    const reader = res.body?.getReader()
    if (!reader) {
      reconnectLater()
      return
    }
    const decoder = new TextDecoder()
    let buffer = ''
    let reconnectNow = false
    try {
      read: for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buffer += decoder.decode(value, { stream: true })
        for (;;) {
          const match = FRAME_END.exec(buffer)
          if (!match) break
          const block = buffer.slice(0, match.index)
          buffer = buffer.slice(match.index + match[0].length)
          if (await handleFrame(block)) {
            reconnectNow = true
            break read
          }
        }
      }
    } catch {
      // Aborted (close / auth refresh) or the connection dropped — handled below.
    }
    controller.abort() // tear down this attempt's fetch + reader
    if (closed) return
    if (reconnectNow) {
      attempt = 0
      void openOnce() // reconnect immediately with the refreshed Bearer + last id
      return
    }
    reconnectLater()
  }

  void openOnce()
  return handle
}
