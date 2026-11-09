/**
 * HTTP client for the Kidversa API (spec 01§8 / 02§2).
 *
 * - Every /api/* and /public/* response is an envelope: `{data}` or
 *   `{error:{code,message}}` (HTTP 500 only for unhandled faults, same
 *   envelope shape). We branch on the envelope, NEVER on HTTP status.
 * - Access token lives in MEMORY (module var); auth context sets it.
 * - X-Tenant-ID is read from localStorage `kv-tenant` on every
 *   authenticated call; /public/* gets neither auth header.
 * - X-Idempotency-Key is auto-generated (crypto.randomUUID) for POSTs
 *   unless the caller supplies one.
 * - On INVALID_TOKEN the client refreshes once (POST /api/auth/refresh)
 *   and retries the original request exactly once — no loop.
 * - On RATE_LIMITED the Retry-After header (seconds) is exposed as
 *   ApiError.retryAfter; waiting/behaviour is the caller's decision.
 */

export class ApiError extends Error {
  readonly code: string
  /** Seconds from the Retry-After header (RATE_LIMITED only). */
  readonly retryAfter?: number
  /**
   * Extra keys the server attached to `{error:{…}}` beyond code/message —
   * e.g. HAS_OPEN_GROUPS carries `groups:[{gid, session_id, session_name}]`
   * (spec 06 E31 "+ list"). Pure passthrough; branch on `.code` as before.
   */
  readonly details?: Record<string, unknown>

  constructor(
    code: string,
    message: string,
    retryAfter?: number,
    details?: Record<string, unknown>,
  ) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    this.retryAfter = retryAfter
    this.details = details
  }
}

/**
 * ONE envelope-code extraction (004d): ApiError → its code, anything else →
 * INTERNAL. Branch on the envelope code, never on HTTP status.
 */
export function errorCode(e: unknown): string {
  return e instanceof ApiError ? e.code : 'INTERNAL'
}

export interface PageMeta {
  page: number
  per_page: number
  total: number
}

/** Paged list envelope: `{data:[…], meta:{page,per_page,total}}`. */
export interface Paged<T> {
  data: T[]
  meta: PageMeta
}

/** Query params for list(): paging keys + equality filters. Empty values are omitted. */
export type ListParams = Record<string, string | number | boolean | null | undefined>

interface Envelope<T> {
  data: T
  meta?: PageMeta
}

interface CallOptions {
  params?: ListParams
  /** JSON request body (sets Content-Type: application/json). */
  json?: unknown
  /** multipart body; mutually exclusive with `json` (browser sets the boundary). */
  form?: FormData
  idempotencyKey?: string
  signal?: AbortSignal
  /** Internal: skip refresh-once (used by the refresh call itself). */
  skipAuthRefresh?: boolean
}

let accessToken: string | null = null
let sessionTenantId: number | null = null
let sessionExpiredHandler: (() => void) | null = null
let refreshInFlight: Promise<string | null> | null = null

export function setAccessToken(token: string | null): void {
  accessToken = token
}

export function getAccessToken(): string | null {
  return accessToken
}

/** Canonical tenant id of the current session (spec 06 E7: TENANT_MISMATCH recovery). */
export function setSessionTenant(tenantId: number | null): void {
  sessionTenantId = tenantId
}

/** Invoked when the session is unrecoverable (refresh got INVALID_TOKEN / ACCOUNT_INACTIVE). */
export function setSessionExpiredHandler(handler: (() => void) | null): void {
  sessionExpiredHandler = handler
}

/**
 * Report an unrecoverable session to the registered handler. The STREAM
 * client reports `ACCOUNT_INACTIVE` handshakes through this — lib/stream.ts
 * cannot see the module-private handler (REST invokes it inline in `call`).
 */
export function notifySessionExpired(): void {
  sessionExpiredHandler?.()
}

/**
 * Spec 06 E7 self-heal: `kv-tenant` drifted from the session's canonical
 * tenant — restore it. Returns true iff a drift was repaired (false when the
 * header already matches, or there is no session tenant to heal towards).
 * Shared by the REST retry path and the SSE handshake path.
 */
export function healTenantHeader(): boolean {
  if (sessionTenantId === null) return false
  if (localStorage.getItem('kv-tenant') === String(sessionTenantId)) return false
  localStorage.setItem('kv-tenant', String(sessionTenantId))
  return true
}

function buildUrl(path: string, params?: ListParams): string {
  if (!params) return path
  const usp = new URLSearchParams()
  for (const [key, value] of Object.entries(params)) {
    if (value === undefined || value === null || value === '') continue
    usp.set(key, String(value))
  }
  const qs = usp.toString()
  return qs ? `${path}?${qs}` : path
}

function buildHeaders(method: string, path: string, opts: CallOptions): Headers {
  const headers = new Headers({ Accept: 'application/json' })
  if (opts.json !== undefined) headers.set('Content-Type', 'application/json')
  // Authenticated = every /api call except refresh (W1 pin: cookie-only, no
  // Authorization/X-Tenant-ID); /public/* carries neither header.
  const authenticated = !path.startsWith('/public') && path !== '/api/auth/refresh'
  if (authenticated) {
    if (accessToken) headers.set('Authorization', `Bearer ${accessToken}`)
    const tenant = localStorage.getItem('kv-tenant')
    if (tenant) headers.set('X-Tenant-ID', tenant)
  }
  if (method === 'POST') {
    headers.set('X-Idempotency-Key', opts.idempotencyKey ?? crypto.randomUUID())
  }
  return headers
}

function parseRetryAfter(value: string | null): number | undefined {
  if (!value) return undefined
  const seconds = Number(value)
  if (Number.isFinite(seconds)) return Math.max(0, Math.ceil(seconds))
  const date = Date.parse(value)
  if (Number.isFinite(date)) return Math.max(0, Math.ceil((date - Date.now()) / 1000))
  return undefined
}

async function call<T>(method: string, path: string, opts: CallOptions = {}): Promise<Envelope<T>> {
  const url = buildUrl(path, opts.params)
  let res: Response
  try {
    res = await fetch(url, {
      method,
      headers: buildHeaders(method, path, opts),
      body: opts.form ?? (opts.json !== undefined ? JSON.stringify(opts.json) : undefined),
      signal: opts.signal,
      cache: 'no-store',
    })
  } catch {
    throw new ApiError('INTERNAL', 'network request failed')
  }

  let payload: unknown
  try {
    payload = await res.json()
  } catch {
    throw new ApiError('INTERNAL', `non-JSON response (HTTP ${res.status})`)
  }

  const env = payload as {
    data?: unknown
    error?: { code?: unknown; message?: unknown } & Record<string, unknown>
  } | null
  if (env && typeof env === 'object' && env.error) {
    const { code: rawCode, message: rawMessage, ...extraKeys } = env.error
    const code = typeof rawCode === 'string' ? rawCode : 'INTERNAL'
    const message = typeof rawMessage === 'string' ? rawMessage : code
    const retryAfter =
      code === 'RATE_LIMITED' ? parseRetryAfter(res.headers.get('Retry-After')) : undefined
    const details = Object.keys(extraKeys).length > 0 ? extraKeys : undefined
    const err = new ApiError(code, message, retryAfter, details)

    if (code === 'INVALID_TOKEN' && !opts.skipAuthRefresh && accessToken) {
      // Refresh once, then retry the original request exactly once.
      const token = await refreshAccessToken() // transient failures throw here (no logout)
      if (token) return call<T>(method, path, { ...opts, skipAuthRefresh: true })
      throw err // refresh was fatal: session already cleared
    }
    if (code === 'TENANT_MISMATCH' && healTenantHeader()) {
      // Spec 06 E7: the header drifted from the session tenant — healTenantHeader
      // restored it; retry once (self-terminating: the header now equals the
      // canonical tenant). No drift / no session tenant → terminal as before.
      return call<T>(method, path, { ...opts })
    }
    if (code === 'ACCOUNT_INACTIVE') sessionExpiredHandler?.()
    throw err
  }

  if (env && typeof env === 'object' && 'data' in env) return env as Envelope<T>
  throw new ApiError('INTERNAL', 'response is not a valid envelope')
}

async function doRefresh(): Promise<string | null> {
  try {
    const env = await call<{ access: string }>('POST', '/api/auth/refresh', {
      skipAuthRefresh: true,
    })
    setAccessToken(env.data.access)
    return env.data.access
  } catch (error) {
    if (error instanceof ApiError && error.code === 'INVALID_TOKEN') {
      sessionExpiredHandler?.() // dead refresh cookie → re-login
      return null
    }
    // Transient failure (DB_UNAVAILABLE / RATE_LIMITED / network): the old jti is
    // still valid (spec 06 §3) — keep the session and surface the error; the next
    // request starts a fresh refresh attempt.
    throw error
  }
}

/**
 * Refresh the access token (single shared attempt for concurrent callers).
 * Returns null when the session is dead (INVALID_TOKEN → handler ran);
 * throws on transient failures (e.g. DB_UNAVAILABLE) without logging out.
 */
export function refreshAccessToken(): Promise<string | null> {
  if (!refreshInFlight) {
    refreshInFlight = doRefresh().finally(() => {
      refreshInFlight = null
    })
  }
  return refreshInFlight
}

async function send<T>(method: string, path: string, opts: CallOptions = {}): Promise<T> {
  return (await call<T>(method, path, opts)).data
}

function get<T>(
  path: string,
  params?: ListParams,
  opts: Pick<CallOptions, 'signal'> = {},
): Promise<T> {
  return send<T>('GET', path, { params, ...opts })
}

function post<T>(path: string, json?: unknown, opts: Omit<CallOptions, 'json' | 'params'> = {}): Promise<T> {
  return send<T>('POST', path, { ...opts, json })
}

function put<T>(path: string, json?: unknown, opts: Omit<CallOptions, 'json' | 'params'> = {}): Promise<T> {
  return send<T>('PUT', path, { ...opts, json })
}

function patch<T>(path: string, json?: unknown, opts: Omit<CallOptions, 'json' | 'params'> = {}): Promise<T> {
  return send<T>('PATCH', path, { ...opts, json })
}

function del<T>(path: string, params?: ListParams): Promise<T> {
  return send<T>('DELETE', path, { params })
}

/** Paged list: returns `{data, meta}`; server rejects unknown/invalid paging keys. */
async function list<T>(path: string, params?: ListParams): Promise<Paged<T>> {
  const env = await call<T[]>('GET', path, { params })
  if (!env.meta) throw new ApiError('INTERNAL', 'paged response missing meta')
  return { data: env.data, meta: env.meta }
}

/** multipart/form-data POST; the caller supplies the idempotency key (spec-required routes). */
function upload<T>(path: string, form: FormData, idempotencyKey: string): Promise<T> {
  return send<T>('POST', path, { form, idempotencyKey })
}

export const api = { get, post, put, patch, del, list, upload }
