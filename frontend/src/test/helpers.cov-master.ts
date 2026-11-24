/**
 * cov-master slice helpers (masters + children page tests).
 *
 * Owned by cov-master — sits beside the shared `handlers.ts` / `render.tsx`
 * factories (never edits them): paged-list request recorder, wire fixtures
 * mirroring the contract shapes, file-upload helpers and render shortcuts.
 */
import type { ReactElement } from 'react'
import type { RenderResult } from '@testing-library/react'
import { configure, fireEvent, waitFor, within } from '@testing-library/react'
import { http, type HttpHandler } from 'msw'
import type { Role } from '../lib/auth'
import { fail, makeLoginData, makeUser, ok, paged } from './handlers'
import { renderApp } from './render'

/**
 * tsconfig's lib stops at ES2023, but the suite runs on Node ≥20 (and the
 * browsers Kidversa targets) where `Promise.withResolvers` exists — widen the
 * constructor type here so tests can use the callback-free form without an
 * ES2024 lib bump.
 */
declare global {
  interface PromiseConstructor {
    withResolvers<T>(): {
      promise: Promise<T>
      resolve: (value: T | PromiseLike<T>) => void
      reject: (reason?: unknown) => void
    }
  }
}

/* ---------- render ---------- */

// Full-suite runs pack 50+ files across workers: antd toasts/modals under
// load regularly outlive the 1s default before settling. Scoped to this
// slice — the helper is imported only by the masters/children test files.
configure({ asyncUtilTimeout: 5000 })

/** Render `ui` with a full session of the given role (boot refresh included). */
export function renderAs(
  ui: ReactElement,
  options: { role?: Role; route?: string } = {},
): RenderResult {
  const { role = 'ADMIN', route = '/' } = options
  return renderApp(ui, { route, session: makeLoginData({ user: makeUser({ role }) }) })
}

/* ---------- paged list endpoints ---------- */

export interface ListRecorder {
  /** Every request URL the handler served, in arrival order. */
  seen: URL[]
  handler: HttpHandler
}

/** Record every GET to `path`; `respond(url, call)` builds the reply. */
export function recordList(
  path: string,
  respond: (url: URL, call: number) => Response,
): ListRecorder {
  const seen: URL[] = []
  const handler = http.get(path, ({ request }) => {
    const url = new URL(request.url)
    seen.push(url)
    return respond(url, seen.length)
  })
  return { seen, handler }
}

/** Paged envelope echoing the request's page/per_page with a fixed total. */
export function pagedBody(rows: unknown[], total: number, url: URL): Response {
  const page = Number(url.searchParams.get('page') ?? '1')
  const perPage = Number(url.searchParams.get('per_page') ?? '20')
  return paged(rows, { page, per_page: perPage, total })
}

/** `path` always answers `rows` (meta echoes the request cursor). */
export function pagedList(path: string, rows: unknown[], total = rows.length): ListRecorder {
  return recordList(path, (url) => pagedBody(rows, total, url))
}

/** `path` fails with `code` on the FIRST call, then answers `rows` (retry paths). */
export function failFirstThenList(
  path: string,
  code: string,
  rows: unknown[],
  total = rows.length,
): ListRecorder {
  return recordList(path, (url, call) =>
    call === 1 ? fail(code) : pagedBody(rows, total, url),
  )
}

/** Sorted `k=v&…` query string — cursor-independent request comparison. */
export function queryOf(url: URL): string {
  return [...url.searchParams.entries()]
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([key, value]) => `${key}=${value}`)
    .join('&')
}

/* ---------- mutating request endpoints ---------- */

/**
 * Record every call to `path`, capturing the parsed body (`parse`) and
 * answering `reply(call)`. Multipart bodies MUST be read with
 * `request.text()` — a jsdom `File` part survives the transport but breaks
 * undici's `request.formData()` parser, so tests assert the raw wire fields.
 */
export function recordCall<B>(
  method: 'post' | 'patch' | 'delete',
  path: string,
  parse: (request: Request) => Promise<B>,
  reply: (call: number) => Response = () => ok({}),
): { bodies: B[]; handler: HttpHandler } {
  const bodies: B[] = []
  const handler = http[method](path, async ({ request }) => {
    bodies.push(await parse(request))
    return reply(bodies.length)
  })
  return { bodies, handler }
}

/** Count `name="<field>"` occurrences in a raw multipart body (repeat keys). */
export function fieldCount(raw: string, field: string): number {
  return raw.split(`name="${field}"`).length - 1
}

/** Raw multipart body with CRLF normalised for readable `includes` assertions. */
export function multipartText(raw: string): string {
  return raw.replace(/\r\n/g, '\n')
}

/* ---------- antd Select ---------- */

/**
 * The clickable trigger box of a Select identified by its placeholder —
 * rc-select renders the placeholder itself with `pointer-events: none`, and
 * the query is scoped to placeholder spans so table headers with the same
 * title never collide.
 */
export function selectTrigger(placeholder: string): HTMLElement {
  for (const span of document.querySelectorAll<HTMLElement>('.ant-select-selection-placeholder')) {
    if (span.textContent !== placeholder) continue
    const anchor = span.closest('.ant-select-selector')
    if (anchor instanceof HTMLElement) return anchor
  }
  throw new Error(`no open select trigger with placeholder "${placeholder}"`)
}

/**
 * The option element carrying `label` inside the open Select popup. rc-select
 * mirrors options into a hidden zero-size a11y listbox whose `role="option"`
 * nodes are inert, so text queries must target the rendered popup instead.
 */
export function findOption(label: string): Promise<HTMLElement> {
  return waitFor(() => {
    for (const popup of document.querySelectorAll<HTMLElement>('.ant-select-dropdown')) {
      const match = within(popup).queryByText(label)
      if (match) return match
    }
    throw new Error(`no open select option "${label}"`)
  })
}

/* ---------- wire fixtures (contracts: Masters / Children sections) ---------- */

export interface ProgramRow {
  id: number
  name: string
  description: string | null
  badge_name: string | null
  badge_image_url: string | null
  is_active: boolean
  topic_count: number
  activity_count: number
  eligible: boolean
  created_at: string
}

export function programRow(id: number, name: string, over: Partial<ProgramRow> = {}): ProgramRow {
  return {
    id,
    name,
    description: `${name} — deskripsi`,
    badge_name: null,
    badge_image_url: null,
    is_active: true,
    topic_count: 1,
    activity_count: 1,
    eligible: true,
    created_at: '2026-10-01T00:00:00Z',
    ...over,
  }
}

export interface TopicRow {
  id: number
  program_id: number
  program_name: string
  name: string
  description: string | null
  sort_order: number
  badge_name: string | null
  badge_image_url: string | null
  is_active: boolean
}

export function topicRow(id: number, name: string, over: Partial<TopicRow> = {}): TopicRow {
  return {
    id,
    program_id: 1,
    program_name: 'Eksplorasi Kota',
    name,
    description: `${name} — deskripsi`,
    sort_order: id,
    badge_name: null,
    badge_image_url: null,
    is_active: true,
    ...over,
  }
}

export interface ActivityRow {
  id: number
  program_id: number
  topic_id: number
  program_name: string
  topic_name: string
  name: string
  indicator: string | null
  sort_order: number
}

export function activityRow(
  id: number,
  name: string,
  over: Partial<ActivityRow> = {},
): ActivityRow {
  return {
    id,
    program_id: 1,
    topic_id: 2,
    program_name: 'Eksplorasi Kota',
    topic_name: 'Batik Nusantara',
    name,
    indicator: 'Menyebutkan 3 motif',
    sort_order: id,
    ...over,
  }
}

export interface MissionRow {
  id: number
  program_id: number
  program_name: string
  name: string
  body: string | null
  topics: Array<{ topic_id: number; name: string }>
}

export function missionRow(id: number, name: string, over: Partial<MissionRow> = {}): MissionRow {
  return {
    id,
    program_id: 1,
    program_name: 'Eksplorasi Kota',
    name,
    body: `${name} — isi misi`,
    topics: [{ topic_id: 2, name: 'Batik Nusantara' }],
    ...over,
  }
}

export interface FrameRow {
  id: number
  name: string
  image_url: string
  programs: Array<{ id: number; name: string }>
}

export function frameRow(id: number, name: string, over: Partial<FrameRow> = {}): FrameRow {
  return {
    id,
    name,
    image_url: `/uploads/frames/${name}.png`,
    programs: [{ id: 1, name: 'Eksplorasi Kota' }],
    ...over,
  }
}

export interface ChildRow {
  id: number
  name: string
  age: number
  age_confirmed: boolean
  school: string
  mother_name: string
  mother_wa: string
  father_name: string | null
  father_wa: string | null
  guardian_name: string | null
  is_active: boolean
  created_at: string
}

export function childRow(id: number, name: string, over: Partial<ChildRow> = {}): ChildRow {
  return {
    id,
    name,
    age: 8,
    age_confirmed: false,
    school: 'SD Ceria',
    mother_name: 'Sari',
    mother_wa: '+6281234567890',
    father_name: null,
    father_wa: null,
    guardian_name: null,
    is_active: true,
    created_at: '2026-10-01T00:00:00Z',
    ...over,
  }
}

/* ---------- uploads ---------- */

/** File with an optional forced `size` (pre-check branch, without the bytes). */
export function imageFile(name: string, type: string, size?: number): File {
  const file = new File(['x'], name, { type })
  if (size !== undefined) Object.defineProperty(file, 'size', { value: size })
  return file
}

/**
 * Dispatch a change on the page's (hidden) file input — modals portal to
 * `document.body`, so the input is looked up in the whole document.
 */
export function uploadFiles(files: File[]): HTMLInputElement {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]')
  if (!input) throw new Error('no input[type=file] in the document')
  fireEvent.change(input, { target: { files } })
  return input
}
