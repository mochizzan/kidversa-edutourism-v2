/**
 * Envelope handling (spec 01§8 / 06§1, FL-02): the api client branches on
 * `{data}` / `{error:{code}}` — never on HTTP status — exposes Retry-After,
 * and refreshes exactly once on INVALID_TOKEN (second failure does not loop;
 * transient refresh failures never log out).
 */
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { http, HttpResponse } from 'msw'
import {
  ApiError,
  api,
  getAccessToken,
  refreshAccessToken,
  setAccessToken,
  setSessionExpiredHandler,
  setSessionSchool,
} from './api'
import { server } from '../test/setup'
import { fail, makeLoginData, ok } from '../test/handlers'

interface Seen {
  url: string
  headers: Headers
}

function capture(method: 'get' | 'post' | 'put', path: string, respond: (count: number) => Response) {
  const seen: Seen[] = []
  server.use(
    http[method](path, ({ request }) => {
      seen.push({ url: request.url, headers: new Headers(request.headers) })
      return respond(seen.length)
    }),
  )
  return seen
}

describe('envelope handling', () => {
  beforeEach(() => {
    setAccessToken(null)
    setSessionSchool(null)
    setSessionExpiredHandler(null)
  })

  it('returns data for a {data} success envelope', async () => {
    server.use(http.get('/api/children/7', () => ok({ id: 7, name: 'Aisyah' })))
    await expect(api.get('/api/children/7')).resolves.toEqual({ id: 7, name: 'Aisyah' })
  })

  it('branches on the envelope, not on HTTP status: 500 + {data} still resolves', async () => {
    server.use(
      http.get('/api/programs', () =>
        HttpResponse.json({ data: [{ id: 1 }] }, { status: 500 }),
      ),
    )
    await expect(api.get('/api/programs')).resolves.toEqual([{ id: 1 }])
  })

  it('throws ApiError with code + message from a 200 error envelope', async () => {
    server.use(http.post('/api/auth/login', () => HttpResponse.json({ error: { code: 'INVALID_CREDENTIALS', message: 'generic' } })))
    const error = await api.post('/api/auth/login', {}).catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ name: 'ApiError', code: 'INVALID_CREDENTIALS', message: 'generic' })
  })

  it('passes extra error keys through as ApiError.details', async () => {
    server.use(
      http.delete('/api/users/4', () =>
        HttpResponse.json({
          error: {
            code: 'HAS_OPEN_GROUPS',
            message: 'reassign first',
            groups: [{ gid: 9, session_id: 3, session_name: 'Kelas Batik' }],
          },
        }),
      ),
    )
    const error = (await api.del('/api/users/4').catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('HAS_OPEN_GROUPS')
    expect(error.details).toEqual({ groups: [{ gid: 9, session_id: 3, session_name: 'Kelas Batik' }] })
  })

  it('exposes Retry-After seconds on RATE_LIMITED (and nothing on other codes)', async () => {
    server.use(
      http.get('/api/sessions', () =>
        HttpResponse.json({ error: { code: 'RATE_LIMITED', message: 'slow down' } }, {
          headers: { 'Retry-After': '7' },
        }),
      ),
      http.get('/api/programs', () => HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'busy' } })),
    )
    const limited = (await api.get('/api/sessions').catch((e: unknown) => e)) as ApiError
    expect(limited.retryAfter).toBe(7)
    const transient = (await api.get('/api/programs').catch((e: unknown) => e)) as ApiError
    expect(transient.retryAfter).toBeUndefined()
  })

  it('rejects a non-envelope body as INTERNAL', async () => {
    server.use(http.get('/api/schools', () => HttpResponse.json({ nope: true })))
    const error = (await api.get('/api/schools').catch((e: unknown) => e)) as ApiError
    expect(error).toMatchObject({ code: 'INTERNAL' })
  })

  it('rejects a non-JSON body as INTERNAL (envelope parsing failure)', async () => {
    server.use(
      http.get('/api/schools', () => new HttpResponse('<html>maintenance</html>', { status: 200, headers: { 'Content-Type': 'text/html' } })),
    )
    const error = (await api.get('/api/schools').catch((e: unknown) => e)) as ApiError
    expect(error).toMatchObject({ code: 'INTERNAL' })
    expect(error.message).toMatch(/non-JSON response/)
  })

  it('SCHOOL_MISMATCH restores the drifted kv-school header and retries once', async () => {
    // Session canonical school is 3, but the localStorage header drifted to 7.
    setAccessToken('tok-1')
    setSessionSchool(3)
    localStorage.setItem('kv-school', '7')
    let calls = 0
    server.use(
      http.get('/api/sessions', ({ request }) => {
        calls += 1
        const school = request.headers.get('X-School-ID')
        if (school === '7') {
          return HttpResponse.json({ error: { code: 'SCHOOL_MISMATCH', message: 'wrong school' } })
        }
        return ok([])
      }),
    )

    await expect(api.get('/api/sessions')).resolves.toEqual([])
    expect(calls).toBe(2)
    // Header restored to the canonical school — the drift cannot recur.
    expect(localStorage.getItem('kv-school')).toBe('3')
  })

  it('rejects a paged list without meta as INTERNAL', async () => {
    server.use(http.get('/api/users', () => ok([])))
    const error = (await api.list('/api/users').catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('INTERNAL')
  })

  it('returns {data, meta} from a paged list and omits empty params', async () => {
    const seen = capture('get', '/api/sessions', () =>
      HttpResponse.json({ data: [{ id: 1 }], meta: { page: 2, per_page: 20, total: 45 } }),
    )
    const result = await api.list('/api/sessions', { page: 2, per_page: 20, q: '', status: undefined })
    expect(result).toEqual({ data: [{ id: 1 }], meta: { page: 2, per_page: 20, total: 45 } })
    const url = new URL(seen[0].url)
    expect(url.searchParams.get('page')).toBe('2')
    expect(url.searchParams.has('q')).toBe(false)
    expect(url.searchParams.has('status')).toBe(false)
  })

  it('sends Bearer + X-School-ID on /api calls but neither on /public or refresh', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '3')
    const apiSeen = capture('get', '/api/dashboard/overview', () => ok({ children: 0 }))
    await api.get('/api/dashboard/overview')
    expect(apiSeen[0].headers.get('Authorization')).toBe('Bearer tok-1')
    expect(apiSeen[0].headers.get('X-School-ID')).toBe('3')

    const publicSeen = capture('get', '/public/report', () => ok({}))
    await api.get('/public/report', { token: 'abc' })
    expect(publicSeen[0].headers.get('Authorization')).toBeNull()
    expect(publicSeen[0].headers.get('X-School-ID')).toBeNull()

    const refreshSeen = capture('post', '/api/auth/refresh', () => ok(makeLoginData()))
    await api.post('/api/auth/refresh')
    expect(refreshSeen[0].headers.get('Authorization')).toBeNull()
    expect(refreshSeen[0].headers.get('X-School-ID')).toBeNull()
    expect(refreshSeen[0].headers.get('X-Idempotency-Key')).not.toBeNull()
  })

  it('refreshes once on INVALID_TOKEN, then retries the original exactly once with the new Bearer', async () => {
    const original = capture('get', '/api/children/7', (count) =>
      count === 1 ? fail('INVALID_TOKEN') : ok({ id: 7 }),
    )
    const refresh = capture('post', '/api/auth/refresh', () =>
      HttpResponse.json({ data: { access: 'tok-2', user: {}, school: {} } }),
    )
    setAccessToken('tok-1')

    await expect(api.get('/api/children/7')).resolves.toEqual({ id: 7 })
    expect(original).toHaveLength(2)
    expect(original[0].headers.get('Authorization')).toBe('Bearer tok-1')
    expect(original[1].headers.get('Authorization')).toBe('Bearer tok-2')
    expect(refresh).toHaveLength(1)
    expect(getAccessToken()).toBe('tok-2')
  })

  it('does not loop when the retried request fails with INVALID_TOKEN again', async () => {
    const original = capture('get', '/api/sessions', () => fail('INVALID_TOKEN'))
    const refresh = capture('post', '/api/auth/refresh', () =>
      HttpResponse.json({ data: { access: 'tok-2', user: {}, school: {} } }),
    )
    setAccessToken('tok-1')

    const error = (await api.get('/api/sessions').catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('INVALID_TOKEN')
    expect(original).toHaveLength(2) // original + one retry, then stop
    expect(refresh).toHaveLength(1)
  })

  it('treats INVALID_TOKEN from refresh itself as a dead session: no retry, expiry handler runs', async () => {
    const expired: string[] = []
    setSessionExpiredHandler(() => expired.push('expired'))
    const original = capture('get', '/api/sessions', () => fail('INVALID_TOKEN'))
    const refresh = capture('post', '/api/auth/refresh', () => fail('INVALID_TOKEN'))
    setAccessToken('tok-1')

    const error = (await api.get('/api/sessions').catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('INVALID_TOKEN')
    expect(original).toHaveLength(1) // retried request never sent
    expect(refresh).toHaveLength(1)
    expect(expired).toEqual(['expired'])
  })

  it('keeps the session on transient DB_UNAVAILABLE during refresh (no logout, no retry)', async () => {
    const expired: string[] = []
    setSessionExpiredHandler(() => expired.push('expired'))
    const original = capture('get', '/api/sessions', () => fail('INVALID_TOKEN'))
    const refresh = capture('post', '/api/auth/refresh', () => fail('DB_UNAVAILABLE'))
    setAccessToken('tok-1')

    const error = (await api.get('/api/sessions').catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('DB_UNAVAILABLE') // transient failure surfaces as-is
    expect(original).toHaveLength(1)
    expect(refresh).toHaveLength(1)
    expect(expired).toEqual([]) // old jti still valid → no logout
    expect(getAccessToken()).toBe('tok-1')
  })

  it('keeps the session on RATE_LIMITED during refresh too', async () => {
    const expired: string[] = []
    setSessionExpiredHandler(() => expired.push('expired'))
    server.use(
      http.get('/api/sessions', () => fail('INVALID_TOKEN')),
      http.post('/api/auth/refresh', () =>
        HttpResponse.json({ error: { code: 'RATE_LIMITED', message: 'slow down' } }, {
          headers: { 'Retry-After': '3' },
        }),
      ),
    )
    setAccessToken('tok-1')

    const error = (await api.get('/api/sessions').catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('RATE_LIMITED')
    expect(error.retryAfter).toBe(3)
    expect(expired).toEqual([])
  })

  it('shares one refresh across concurrent INVALID_TOKEN failures', async () => {
    let refreshed = false
    let refreshCalls = 0
    server.use(
      http.get('/api/a', () => (refreshed ? ok('a') : fail('INVALID_TOKEN'))),
      http.post('/api/auth/refresh', () => {
        refreshed = true
        refreshCalls += 1
        return HttpResponse.json({ data: { access: 'tok-2', user: {}, school: {} } })
      }),
    )
    setAccessToken('tok-1')

    // Both calls fail while the shared refresh promise is still in flight.
    const [a, b] = await Promise.all([api.get('/api/a'), api.get('/api/a')])
    expect([a, b]).toEqual(['a', 'a'])
    expect(refreshCalls).toBe(1)
  })

  it('maps a network failure to ApiError INTERNAL', async () => {
    server.use(http.get('/api/sessions', () => HttpResponse.error()))
    const error = (await api.get('/api/sessions').catch((e: unknown) => e)) as ApiError
    expect(error).toMatchObject({ code: 'INTERNAL' })
  })

  it('POST carries an auto-generated X-Idempotency-Key and honors a supplied one', async () => {
    const auto = capture('post', '/api/auth/register', () => ok({ id: 1, status: 'pending' }))
    await api.post('/api/auth/register', { email: 'x@y.id' })
    expect(auto[0].headers.get('X-Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/)

    const explicit = capture('post', '/api/sessions', () => ok({ session_id: 1 }))
    await api.post('/api/sessions', {}, { idempotencyKey: 'fixed-key-1' })
    expect(explicit[0].headers.get('X-Idempotency-Key')).toBe('fixed-key-1')
  })
})

describe('auth session expiry routing', () => {
  beforeEach(() => {
    setAccessToken(null)
    setSessionExpiredHandler(null)
  })

  it('ACCOUNT_INACTIVE on any call invokes the session-expired handler', async () => {
    const expired: string[] = []
    setSessionExpiredHandler(() => expired.push('expired'))
    server.use(http.get('/api/sessions', () => fail('ACCOUNT_INACTIVE')))
    setAccessToken('tok-1')

    const error = (await api.get('/api/sessions').catch((e: unknown) => e)) as ApiError
    expect(error.code).toBe('ACCOUNT_INACTIVE')
    expect(expired).toEqual(['expired'])
  })

  it('refreshAccessToken resolves null after a dead refresh cookie (handler ran)', async () => {
    const expired: string[] = []
    setSessionExpiredHandler(() => expired.push('expired'))
    server.use(http.post('/api/auth/refresh', () => fail('INVALID_TOKEN')))
    setAccessToken('tok-1')

    await expect(refreshAccessToken()).resolves.toBeNull()
    expect(expired).toEqual(['expired'])
  })
})

/**
 * uploadWithProgress (spec 09): XHR-based photo upload — same envelope +
 * token + idempotency semantics as `upload()`, plus real-time 0–100%
 * progress for the editor's progress display. The photo POST is its only
 * consumer.
 */
describe('uploadWithProgress', () => {
  let active: FakeXHR[]

  class FakeXHR {
    upload = { onprogress: null as null | ((e: ProgressEvent) => void) }
    onload: null | (() => void) = null
    onerror: null | (() => void) = null
    onabort: null | (() => void) = null
    method = ''
    url = ''
    status = 200
    responseText = ''
    requestHeaders: Record<string, string> = {}
    body: FormData | null = null

    open(method: string, url: string): void {
      this.method = method
      this.url = url
      active.push(this)
    }

    setRequestHeader(name: string, value: string): void {
      this.requestHeaders[name] = value
    }

    send(body: FormData | null): void {
      this.body = body
    }

    /** Test driver: fire an upload progress event. */
    progress(loaded: number, total: number): void {
      this.upload.onprogress?.(new ProgressEvent('progress', { loaded, total }))
    }

    /** Test driver: answer with one envelope body. */
    respond(body: unknown, status = 200): void {
      this.status = status
      this.responseText = JSON.stringify(body)
      this.onload?.()
    }

    /** Test driver: answer with a raw (non-JSON / non-envelope) body. */
    respondRaw(text: string, status = 200): void {
      this.status = status
      this.responseText = text
      this.onload?.()
    }

    fail(): void {
      this.onerror?.()
    }

    abort(): void {
      this.onabort?.()
    }
  }

  beforeEach(() => {
    active = []
    vi.stubGlobal('XMLHttpRequest', FakeXHR as unknown as typeof XMLHttpRequest)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  function assertSingleOpen(): FakeXHR {
    expect(active).toHaveLength(1)
    const xhr = active[0]
    expect(xhr.method).toBe('POST')
    expect(xhr.url).toBe('/api/auth/profile-photo')
    return xhr
  }

  function makeForm(): FormData {
    const form = new FormData()
    form.append('file', new Blob([new Uint8Array([1])], { type: 'image/png' }), 'me.png')
    return form
  }

  it('POSTs the form with Bearer/X-School-ID/X-Idempotency-Key headers and parses the envelope', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    const promise = api.uploadWithProgress(
      '/api/auth/profile-photo', makeForm(), 'pp-key-1',
    )

    const xhr = assertSingleOpen()
    expect(xhr.requestHeaders['Authorization']).toBe('Bearer tok-1')
    expect(xhr.requestHeaders['X-School-ID']).toBe('1')
    expect(xhr.requestHeaders['X-Idempotency-Key']).toBe('pp-key-1')
    expect(xhr.requestHeaders['Accept']).toBe('application/json')
    expect(xhr.body).toBeInstanceOf(FormData)

    xhr.respond({ data: { user: { id: 2 } } })
    await expect(promise).resolves.toEqual({ user: { id: 2 } })
  })

  it('reports progress 0–100 monotonic and pins 100 before resolving at load', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    const seen: number[] = []
    const promise = api.uploadWithProgress(
      '/api/auth/profile-photo', makeForm(), 'pp-key-1', (p) => seen.push(p),
    )

    const xhr = assertSingleOpen()
    xhr.progress(0, 100)
    xhr.progress(33, 100)
    xhr.progress(99, 100)
    xhr.respond({ data: { user: { id: 2 } } })

    await expect(promise).resolves.toEqual({ user: { id: 2 } })
    // Monotonic 0..99 during upload; the final 100 is pinned at load.
    expect(seen).toEqual([0, 33, 99, 100])
  })

  it('keeps unlimited-total progress silent (still 100 at load)', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    const seen: number[] = []
    const promise = api.uploadWithProgress(
      '/api/auth/profile-photo', makeForm(), 'pp-key-1', (p) => seen.push(p),
    )

    const xhr = assertSingleOpen()
    xhr.progress(500, 0) // e.total === 0 → no percentage is fabricated
    xhr.respond({ data: { user: { id: 2 } } })

    await expect(promise).resolves.toEqual({ user: { id: 2 } })
    expect(seen).toEqual([100])
  })

  it('throws ApiError on an error envelope (never on HTTP status)', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    const promise = api.uploadWithProgress('/api/auth/profile-photo', makeForm(), 'pp-key-1')

    const xhr = assertSingleOpen()
    xhr.respond({ error: { code: 'VALIDATION', message: 'bad image' } }, 200)

    const error = await promise.catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ code: 'VALIDATION', message: 'bad image' })
  })

  it('throws INTERNAL on a network failure', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    const promise = api.uploadWithProgress('/api/auth/profile-photo', makeForm(), 'pp-key-1')

    const xhr = assertSingleOpen()
    xhr.fail()

    const error = await promise.catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ code: 'INTERNAL', name: 'ApiError' })
  })

  it('refreshes once on INVALID_TOKEN and retries the original upload exactly once', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    let refreshes = 0
    server.use(http.post('/api/auth/refresh', () => {
      refreshes += 1
      return ok({ access: 'tok-2' }) as unknown as Response
    }))
    const promise = api.uploadWithProgress('/api/auth/profile-photo', makeForm(), 'pp-key-1')

    // First attempt: answered INVALID_TOKEN; the refresh cookie is MSW-usable.
    assertSingleOpen().respond({ error: { code: 'INVALID_TOKEN', message: 'stale token' } })

    // The refresh + retry chain is async: wait for the second attempt.
    await vi.waitFor(() => expect(active).toHaveLength(2))
    expect(refreshes).toBe(1)
    // Retry carries the NEW access token, then answers the promise.
    expect(active[1].requestHeaders['Authorization']).toBe('Bearer tok-2')
    active[1].respond({ data: { user: { id: 2 } } })
    await expect(promise).resolves.toEqual({ user: { id: 2 } })
  })

  it('surfaces the original INVALID_TOKEN when the retry also fails (no loop)', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    server.use(http.post('/api/auth/refresh', () => ok({ access: 'tok-2' }) as unknown as Response))
    const promise = api.uploadWithProgress('/api/auth/profile-photo', makeForm(), 'pp-key-1')

    assertSingleOpen().respond({ error: { code: 'INVALID_TOKEN', message: 'stale token' } })
    await vi.waitFor(() => expect(active).toHaveLength(2))
    active[1].respond({ error: { code: 'INVALID_TOKEN', message: 'still stale' } })

    const error = await promise.catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ code: 'INVALID_TOKEN', message: 'still stale' })
    expect(active).toHaveLength(2) // exactly one retry, no loop
  })

  it('rejects a non-envelope body as INTERNAL', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    const promise = api.uploadWithProgress('/api/auth/profile-photo', makeForm(), 'pp-key-1')

    const xhr = assertSingleOpen()
    xhr.respond({ nope: true })

    const error = await promise.catch((e: unknown) => e)
    expect(error).toMatchObject({ code: 'INTERNAL' })
  })

  it('rejects a non-JSON body as INTERNAL', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    const promise = api.uploadWithProgress('/api/auth/profile-photo', makeForm(), 'pp-key-1')

    const xhr = assertSingleOpen()
    xhr.respondRaw('<html>proxy error</html>')

    const error = await promise.catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ code: 'INTERNAL', message: 'non-JSON response' })
  })

  it('rejects the original INVALID_TOKEN without any retry when the session is dead', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    server.use(http.post('/api/auth/refresh', () => fail('INVALID_TOKEN') as unknown as Response))
    const promise = api.uploadWithProgress('/api/auth/profile-photo', makeForm(), 'pp-key-1')

    assertSingleOpen().respond({ error: { code: 'INVALID_TOKEN', message: 'stale token' } })

    // refreshAccessToken → null (dead cookie; the expiry handler has run) →
    // the ORIGINAL error is surfaced and no retry XHR ever opens.
    const error = await promise.catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ code: 'INVALID_TOKEN', message: 'stale token' })
    expect(active).toHaveLength(1)
  })

  it('surfaces a transient refresh failure as-is (the session survives)', async () => {
    setAccessToken('tok-1')
    localStorage.setItem('kv-school', '1')
    server.use(http.post('/api/auth/refresh', () => fail('DB_UNAVAILABLE') as unknown as Response))
    const promise = api.uploadWithProgress('/api/auth/profile-photo', makeForm(), 'pp-key-1')

    assertSingleOpen().respond({ error: { code: 'INVALID_TOKEN', message: 'stale token' } })

    // doRefresh throws on non-INVALID_TOKEN refresh failures (spec 06 §3):
    // the client keeps the session and lets the refresh error through.
    const error = await promise.catch((e: unknown) => e)
    expect(error).toBeInstanceOf(ApiError)
    expect(error).toMatchObject({ code: 'DB_UNAVAILABLE', message: expect.any(String) })
    expect(active).toHaveLength(1)
  })

  it('sends no Authorization header without an access token', async () => {
    setAccessToken(null)
    localStorage.removeItem('kv-school')
    const promise = api.uploadWithProgress('/api/auth/profile-photo', makeForm(), 'pp-key-1')

    const xhr = assertSingleOpen()
    expect(xhr.requestHeaders['Authorization']).toBeUndefined()
    expect(xhr.requestHeaders['X-School-ID']).toBeUndefined()
    expect(xhr.requestHeaders['X-Idempotency-Key']).toBe('pp-key-1')

    xhr.respond({ data: { user: { id: 2 } } })
    await expect(promise).resolves.toEqual({ user: { id: 2 } })
  })

  // NOTE (wire-through): no real-XHR-through-MSW test here. The mswjs XHR
  // interceptor marshals jsdom XHRs into undici requests and DROPS custom
  // request headers in the conversion (only Content-Type survives), so a
  // wire assertion would test the interceptor, not the client. The header /
  // envelope / progress / retry semantics are pinned exactly, call-for-call,
  // by the FakeXHR tests above.
})
