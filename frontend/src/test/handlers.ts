/**
 * MSW handlers + wire fixtures mirroring
 * docs/superskill/task/2026-10-09-contracts-shapes.md exactly
 * (HTTP 200 envelope: `{data}` / `{error:{code,message}}`, paged
 * `{data:[…], meta:{page,per_page,total}}`).
 *
 * Nothing here is registered globally — tests import the factories they
 * need and register them with `server.use(...)` per test.
 */
import { http, HttpResponse, type HttpHandler } from 'msw'
import type { LoginData, School, User } from '../lib/auth'
import type { ReportData } from '../report/types'

/* ---------- envelope helpers ---------- */

export function ok(data: unknown, init?: ResponseInit): Response {
  return HttpResponse.json({ data }, init)
}

export function fail(code: string, extra?: Record<string, unknown>): Response {
  return HttpResponse.json({ error: { code, message: `${code} message`, ...extra } })
}

export function paged(
  rows: unknown[],
  meta: { page: number; per_page: number; total: number },
): Response {
  return HttpResponse.json({ data: rows, meta })
}

/* ---------- auth fixtures (contracts: Auth section) ---------- */

export function makeUser(overrides: Partial<User> = {}): User {
  return {
    id: 1,
    name: 'Admin Bandung',
    email: 'admin@kidversa.id',
    role: 'ADMIN',
    decision: 'APPROVED',
    is_active: true,
    school_id: 1,
    wa_number: '+62811111111',
    // photo_url stays ABSENT (the backend omits it for photo-less accounts);
    // tests that need an avatar pass it via overrides.
    ...overrides,
  }
}

export function makeSchool(overrides: Partial<School> = {}): School {
  return {
    id: 1,
    school_name: 'Bandung',
    country: 'Indonesia',
    city: null,
    is_active: true,
    ...overrides,
  }
}

export function makeLoginData(overrides: Partial<LoginData> = {}): LoginData {
  return {
    access: 'access-token-1',
    user: makeUser(),
    school: makeSchool(),
    ...overrides,
  }
}

export function loginHandler(data: LoginData = makeLoginData()): HttpHandler {
  return http.post('/api/auth/login', () => ok(data))
}

export function refreshHandler(data: LoginData = makeLoginData()): HttpHandler {
  return http.post('/api/auth/refresh', () => ok(data))
}

export function logoutHandler(): HttpHandler {
  return http.post('/api/auth/logout', () => ok({}))
}

export function switchSchoolHandler(school: School = makeSchool({ id: 2, school_name: 'Jakarta' })) {
  return http.post('/api/auth/switch-school', () => ok({ school }))
}

export function schoolsHandler(
  rows: Array<{
    id: number
    school_name: string
    country: string
    city: string | null
    is_active: boolean
  }> = [{ id: 1, school_name: 'Bandung', country: 'Indonesia', city: null, is_active: true }],
): HttpHandler {
  return http.get('/api/auth/schools', () => ok(rows))
}

/* ---------- SSE channels (spec 05§3 frames over text/event-stream) ---------- */

/**
 * Controllable SSE endpoint: every handshake opens a fresh stream;
 * `push` writes a raw frame to the most recent connection.
 */
export interface SseChannel {
  handler: HttpHandler
  push(chunk: string): void
  /** Handshakes observed so far (reconnect counter). */
  readonly handshakes: number
}

export function sseChannel(path = '/api/stream'): SseChannel {
  const encoder = new TextEncoder()
  const controllers: Array<ReadableStreamDefaultController<Uint8Array>> = []
  let handshakes = 0
  const handler = http.get(path, () => {
    handshakes += 1
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controllers.push(controller)
      },
    })
    return new HttpResponse(stream, { headers: { 'Content-Type': 'text/event-stream' } })
  })
  return {
    handler,
    push(chunk: string) {
      controllers.at(-1)?.enqueue(encoder.encode(chunk))
    },
    get handshakes() {
      return handshakes
    },
  }
}

/** Never-emitting event-stream — pages open a page-level stream in tests. */
export function idleStream(path: string): HttpHandler {
  return http.get(path, () => new HttpResponse(null, { headers: { 'Content-Type': 'text/event-stream' } }))
}

/* ---------- report fixture (contracts: Report section) ---------- */

export function reportData(overrides: Partial<ReportData> = {}): ReportData {
  return {
    session: { id: 5, name: 'Kelas Batik', date: '2026-10-09', location: 'Museum Tekstil', status: 'FINISHED' },
    program: { name: 'Eksplorasi Kota' },
    topic: { stid: 3, name: 'Batik Nusantara' },
    child: {
      id: 2,
      name: 'Aisyah Putri',
      age: 8,
      school: 'SD Cendekia',
      mother_name: 'Sari',
      father_name: 'Budi',
      guardian_name: '',
    },
    group: { name: 'Kelompok Merah' },
    attestation: { name: 'Dewi Lestari', role: 'Fasilitator' },
    narrative: {
      text: 'Aisyah aktif menjelajah motif batik dan sangan antusias berbagi temuan.',
      generated_at: '2026-10-09T10:00:00Z',
    },
    outcomes: [
      {
        said: 10,
        activity_name: 'Mengamati motif',
        indicator: 'Menyebutkan 3 motif',
        stars: 4,
        note: 'Sangat percaya diri',
      },
    ],
    missions: [{ mission_id: 1, name: 'Kumpulkan 5 motif' }],
    badges: {
      topic: { name: 'Badge Batik', image_url: '/uploads/badges/batik.png' },
      program: null,
    },
    candidate_photo: { url: '/uploads/photos/cand.jpg' },
    complete: true,
    missing: [],
    ...overrides,
  }
}
