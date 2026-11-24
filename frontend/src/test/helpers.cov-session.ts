/**
 * cov-session shared test helpers: fake-timer steppers (MSW + undici keep
 * their real macrotasks — only the debounce clocks are faked), a throw-safe
 * SSE frame pusher (pushing into an aborted stream must not fail the test),
 * and wire fixtures for the session/assessment/gallery/consent/reports/view
 * slice (contracts: docs/superskill/task/2026-10-09-contracts-shapes.md).
 */
import { act, fireEvent, waitFor } from '@testing-library/react'
import { expect, vi } from 'vitest'
import type { SseChannel } from './handlers'
import type {
  ConsentItem,
  PhotoItem,
  ReportsRow,
  SessionDetail,
  SessionListItem,
} from '../lib/session-contracts'

/** Drain microtasks + 1ms fake-timer steps until `condition` (bounded, no sleeps). */
export async function until(condition: () => boolean, step = 5, limit = 4000): Promise<void> {
  for (let i = 0; i < limit && !condition(); i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(step)
    })
  }
  expect(condition()).toBe(true)
}

export async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

/**
 * Poll a DOM condition under EITHER clock mode: fake timers are stepped, real
 * timers fall back to RTL waitFor (which deadlocks on partially-faked clocks).
 */
export async function untilDom(
  condition: () => boolean,
  label = 'condition',
  limit = 400,
): Promise<void> {
  if (!vi.isFakeTimers()) {
    await waitFor(() => {
      expect(condition(), label).toBe(true)
    })
    return
  }
  for (let i = 0; i < limit && !condition(); i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(5)
    })
  }
  expect(condition(), label).toBe(true)
}

/** Fake ONLY the debounce/backoff clocks — MSW + undici need real setImmediate. */
export function useSseFakeTimers(): void {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
}

/** Write a named SSE frame; a closed/aborted channel is a no-op, never a failure. */
export function pushFrame(
  channel: SseChannel,
  event: string,
  data: Record<string, unknown> = {},
): void {
  try {
    channel.push(`event: ${event}\ndata: ${JSON.stringify(data)}\n\n`)
  } catch {
    // channel already torn down by the page (e.g. link.revoked → stream.close)
  }
}

/* ---------- wire fixtures ---------- */

export function sessionRow(
  id: number,
  name = `Sesi ${id}`,
  overrides: Partial<SessionListItem> = {},
): SessionListItem {
  return {
    id,
    program_id: 1,
    program_name: 'Eksplorasi Kota',
    name,
    description: '',
    date: '2026-10-10',
    start_time: null,
    end_time: null,
    location: 'Museum Tekstil',
    status: 'DRAFT',
    group_count: 0,
    member_count: 0,
    created_at: '2026-10-01T00:00:00Z',
    ...overrides,
  }
}

export interface DetailOptions {
  status?: SessionListItem['status']
  name?: string
  /** Omit the pinned wipe-scope counts (dialog falls back to detail-derived). */
  withScope?: boolean
  /** Replace the default single own group (e.g. FAS reduced foreign groups). */
  groups?: SessionDetail['groups']
}

const OWN_GROUP: SessionDetail['groups'][number] = {
  gid: 5,
  name: 'Kelompok Merah',
  own: true,
  owner: { id: 7, name: 'Dewi Lestari' },
  owner_missing: false,
  member_count: 2,
  consent: { PENDING: 1, SETUJU: 1 },
  members: [
    { child_id: 2, name: 'Aisyah Putri', age: 8, school: 'SD Cendekia', consent: 'PENDING' },
    { child_id: 3, name: 'Budi Santoso', age: 9, school: 'SD Melati', consent: 'SETUJU' },
  ],
}

export function detailFixture(options: DetailOptions = {}): SessionDetail {
  const { status = 'DRAFT', name = 'Kelas Batik', withScope = true, groups } = options
  return {
    session: {
      id: 1,
      program_id: 1,
      program_name: 'Eksplorasi Kota',
      name,
      description: '',
      date: '2026-10-09',
      start_time: null,
      end_time: null,
      location: 'Museum Tekstil',
      status,
      created_by: 1,
      created_at: '2026-10-01T00:00:00Z',
    },
    topics: [
      {
        stid: 3,
        topic_id: 9,
        name: 'Batik Nusantara',
        sort_order: 1,
        activities: [
          {
            said: 10,
            activity_id: 99,
            name: 'Mengamati motif',
            indicator: 'Menyebutkan 3 motif',
            sort_order: 1,
          },
          {
            said: 11,
            activity_id: 100,
            name: 'Menggambar motif',
            indicator: 'Meniru pola',
            sort_order: 2,
          },
        ],
      },
    ],
    // Fresh copy per fixture — a test may mutate the returned group.
    groups: groups ?? structuredClone([OWN_GROUP]),
    ...(withScope
      ? {
          scope: {
            topics: 3,
            groups: 2,
            members: 5,
            assessments: 12,
            photos: 4,
            narratives: 1,
            reports_sent: 2,
            links_report: 1,
            links_gallery: 1,
            links_consent: 3,
          },
        }
      : {}),
  }
}

export function sessionChildRow(id: number, name = `Anak ${id}`) {
  return {
    id,
    name,
    age: 8,
    age_confirmed: true,
    school: 'SD Cendekia',
    mother_name: 'Sari',
    mother_wa: '+6281111111',
    father_name: 'Budi',
    father_wa: '+6282222222',
    guardian_name: '',
    is_active: true,
    created_at: '2026-09-01T00:00:00Z',
  }
}

export function photoItem(id: number, overrides: Partial<PhotoItem> = {}): PhotoItem {
  return {
    id,
    url: `/uploads/photos/${id}.jpg`,
    is_candidate: false,
    created_at: '2026-10-09T10:00:00Z',
    mime: 'image/jpeg',
    file_size: 1000,
    ...overrides,
  }
}

export function consentItem(
  childId: number,
  name: string,
  overrides: Partial<ConsentItem> = {},
): ConsentItem {
  return {
    child_id: childId,
    child_name: name,
    group_name: 'Kelompok Merah',
    status: 'PENDING',
    respondent_name: null,
    responded_at: null,
    consent_url: `https://wa.id/v1/consent/${childId}`,
    ...overrides,
  }
}

export function reportsRow(
  childId: number,
  childName: string,
  stid = 3,
  overrides: Partial<ReportsRow> = {},
): ReportsRow {
  return {
    child_id: childId,
    child_name: childName,
    session_topic_id: stid,
    topic_name: 'Batik Nusantara',
    complete: true,
    narrative: true,
    missions: 2,
    candidate_photo: true,
    report_url: null,
    gallery_url: null,
    ...overrides,
  }
}

/** Stub navigator.clipboard.writeText with scripted success/failure. */
export function stubClipboard(results: boolean[] = []): { writes: string[] } {
  const writes: string[] = []
  let index = 0
  Object.defineProperty(navigator, 'clipboard', {
    configurable: true,
    value: {
      writeText: async (text: string) => {
        writes.push(text)
        const okResult = results[index] ?? true
        index += 1
        if (!okResult) throw new Error('clipboard denied')
      },
    },
  })
  return { writes }
}

/* ---------- antd Select interaction ---------- */

/** antd dropdowns open on mousedown at the trigger; options live in a portal. */
export async function openDropdown(input: HTMLElement): Promise<void> {
  fireEvent.mouseDown(input)
  await waitFor(() => {
    expect(document.querySelector('.ant-select-dropdown:not(.ant-select-dropdown-hidden)')).not.toBeNull()
  })
}

/**
 * Click a dropdown option by label text and return its element. The NEWEST
 * open dropdown wins: an older dropdown may still be mid-leave (motion) and
 * linger in the DOM without the hidden class for a beat.
 */
export async function chooseOption(label: string): Promise<HTMLElement> {
  let chosen: HTMLElement | undefined
  await waitFor(() => {
    const openDropdowns = Array.from(
      document.querySelectorAll<HTMLElement>('.ant-select-dropdown'),
    ).filter((element) => !element.classList.contains('ant-select-dropdown-hidden'))
    const newest = openDropdowns.at(-1)
    const options = newest
      ? Array.from(newest.querySelectorAll<HTMLElement>('.ant-select-item-option'))
      : []
    chosen = options.find((option) => option.textContent?.includes(label))
    expect(
      chosen,
      `dropdowns=${openDropdowns.length} opts=${options.map((option) => option.textContent).join('|')}`,
    ).toBeTruthy()
  })
  fireEvent.click(chosen as HTMLElement)
  return chosen as HTMLElement
}
