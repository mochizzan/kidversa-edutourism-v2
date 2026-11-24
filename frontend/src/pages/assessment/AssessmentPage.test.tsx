/**
 * Assessment page (F-09 / FL-09): StarInput auto-save PUT contract, the
 * ≤2000 note cap, per-card saved/error states with retry, read-only banners
 * per session status, NOT_OWNER, SESSION_LOCKED → toast + detail reload,
 * CHILD_INACTIVE, the readiness.changed full-value signal, the
 * unknown-topic NOT_FOUND guard, and the draft-notice beforeunload warning
 * that arms only while a card holds unsaved input.
 */
import { describe, expect, it } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import StreamGate from '../../components/StreamGate'
import AssessmentPage from './AssessmentPage'
import { emitSignal } from '../../lib/stream'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, ok, sseChannel } from '../../test/handlers'
import {
  detailFixture,
  pushFrame,
  until,
  useSseFakeTimers,
} from '../../test/helpers.cov-session'
import type { RouteObject } from 'react-router'

const ROUTES: RouteObject[] = [
  { path: '/assessment/:sid/:cid/:stid', element: <AssessmentPage /> },
]

interface PutCapture {
  calls: number
  body: unknown
}

function assessmentHandlers(
  capture: PutCapture,
  options: { status?: 'DRAFT' | 'OPENED' | 'FINISHED'; assessments?: unknown; put?: () => Response } = {},
) {
  const { status = 'OPENED', assessments = [], put } = options
  return [
    http.get('/api/sessions/1', () => ok(detailFixture({ status }))),
    http.get('/api/sessions/1/topics/3/children/2/assessments', () => ok(assessments)),
    http.get('/api/sessions/1/topics/3/children/2/readiness', () =>
      ok({ complete: false, missing: [{ said: 10, name: 'Mengamati motif' }] }),
    ),
    http.get('/api/children/2', () => ok({ child: { id: 2, name: 'Aisyah Putri', is_active: true } })),
    http.put('/api/sessions/1/topics/3/children/2/activities/10', async ({ request }) => {
      capture.calls += 1
      capture.body = await request.json()
      return put ? put() : HttpResponse.json({ data: { said: 10, stars: 4, note: null, updated_at: '2026-10-09T10:00:00Z' } })
    }),
  ]
}

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })

describe('assessment form save', () => {
  it('PUTs {stars} to the per-activity path and reports Tersimpan with the new progress', async () => {
    const capture: PutCapture = { calls: 0, body: null }
    server.use(...assessmentHandlers(capture))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    await screen.findAllByText(/Mengamati motif/)
    expect(screen.getByText('Penilaian 0/2')).toBeInTheDocument()
    // Progress steps: one per activity, filled only once assessed.
    expect(document.querySelectorAll('.ant-progress-steps-item')).toHaveLength(2)
    expect(document.querySelectorAll('.ant-progress-steps-item-active')).toHaveLength(0)

    const star4 = screen.getAllByRole('radio', { name: '4 — BSB' })[0]
    await user.click(star4)

    await screen.findByText('Tersimpan')
    expect(capture.calls).toBe(1)
    expect(capture.body).toEqual({ stars: 4 })
    expect(screen.getByText('Penilaian 1/2')).toBeInTheDocument()
    expect(document.querySelectorAll('.ant-progress-steps-item')).toHaveLength(2)
    expect(document.querySelectorAll('.ant-progress-steps-item-active')).toHaveLength(1)
  })

  it('caps the note field at 2000 chars and saves it through the save button', async () => {
    const capture: PutCapture = { calls: 0, body: null }
    server.use(...assessmentHandlers(capture))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    const cards = (await screen.findAllByText(/Mengamati motif/))[0].closest('.ant-card') as HTMLElement
    const note = within(cards).getByPlaceholderText(
      'Catatan untuk kegiatan ini (maks. 2000 karakter)',
    )
    expect(note).toHaveAttribute('maxlength', '2000')

    // Assess first (the note save button needs a value), then type + save.
    await user.click(within(cards).getAllByRole('radio', { name: '3 — BSH' })[0])
    await screen.findByText('Tersimpan')
    await user.type(note, 'Sangat aktif')
    await user.click(within(cards).getByRole('button', { name: 'Simpan' }))

    await waitFor(() => expect(capture.body).toEqual({ stars: 3, note: 'Sangat aktif' }))
    await screen.findAllByText('Tersimpan')
  })

  it('marks the card failed on an error envelope and allows a retry save', async () => {
    const capture: PutCapture = { calls: 0, body: null }
    let failNext = true
    server.use(
      ...assessmentHandlers(capture, {
        put: () => {
          if (failNext) {
            return HttpResponse.json({ error: { code: 'INTERNAL', message: 'boom' } })
          }
          return HttpResponse.json({ data: { said: 10, stars: 2, note: null, updated_at: 'x' } })
        },
      }),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    const cards = (await screen.findAllByText(/Mengamati motif/))[0].closest('.ant-card') as HTMLElement
    await user.click(within(cards).getAllByRole('radio', { name: '2 — MB' })[0])

    await within(cards).findByText('Gagal menyimpan')
    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')

    // Retry: flip the server, edit again (dirty) → save succeeds.
    failNext = false
    await user.click(within(cards).getAllByRole('radio', { name: '1 — BB' })[0])
    await within(cards).findByText('Tersimpan')
    expect(capture.calls).toBe(2)
  })

  it('SESSION_LOCKED on save toasts and reloads the session detail', async () => {
    const capture: PutCapture = { calls: 0, body: null }
    let detailGets = 0
    server.use(
      // Counting detail answer must precede the generic batch (first match wins).
      http.get('/api/sessions/1', () => {
        detailGets += 1
        return ok(detailFixture({ status: 'OPENED' }))
      }),
      ...assessmentHandlers(capture, {
        put: () => HttpResponse.json({ error: { code: 'SESSION_LOCKED', message: 'locked' } }),
      }),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    const cards = (await screen.findAllByText(/Mengamati motif/))[0].closest('.ant-card') as HTMLElement
    const before = detailGets
    await user.click(within(cards).getAllByRole('radio', { name: '4 — BSB' })[0])

    await screen.findByText('Sesi terkunci untuk perubahan ini.')
    await waitFor(() => expect(detailGets).toBeGreaterThan(before))
  })

  it('CHILD_INACTIVE on save toasts and flips the inactive banner', async () => {
    const capture: PutCapture = { calls: 0, body: null }
    server.use(
      ...assessmentHandlers(capture, {
        put: () => HttpResponse.json({ error: { code: 'CHILD_INACTIVE', message: 'gone' } }),
      }),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    const cards = (await screen.findAllByText(/Mengamati motif/))[0].closest('.ant-card') as HTMLElement
    await user.click(within(cards).getAllByRole('radio', { name: '4 — BSB' })[0])

    // Toast + banner both carry the message.
    const notices = await screen.findAllByText('Anak sudah dinonaktifkan.')
    expect(notices.length).toBeGreaterThanOrEqual(2)
    // Inputs are now disabled by the inactive banner (Rate radios expose
    // aria-disabled rather than the disabled attribute).
    expect(within(cards).getAllByRole('radio', { name: '4 — BSB' })[0]).toHaveAttribute(
      'aria-disabled',
      'true',
    )
  })
})

describe('assessment read-only banners', () => {
  it('FINISHED renders the finished banner and disables the inputs', async () => {
    server.use(...assessmentHandlers({ calls: 0, body: null }, { status: 'FINISHED' }))
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    await screen.findAllByText(/Mengamati motif/)
    await screen.findByText('Sesi sudah selesai — penilaian hanya dapat dilihat.')
    const star = screen.getAllByRole('radio', { name: '4 — BSB' })[0]
    expect(star).toHaveAttribute('aria-disabled', 'true')
  })

  it('DRAFT renders the not-opened banner', async () => {
    server.use(...assessmentHandlers({ calls: 0, body: null }, { status: 'DRAFT' }))
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Sesi belum dibuka — penilaian dimulai setelah sesi dibuka.')
  })

  it('NOT_OWNER assessments render the foreign-child banner', async () => {
    server.use(
      http.get('/api/sessions/1/topics/3/children/2/assessments', () =>
        HttpResponse.json({ error: { code: 'NOT_OWNER', message: 'foreign' } }),
      ),
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get('/api/sessions/1/topics/3/children/2/readiness', () =>
        ok({ complete: false, missing: [] }),
      ),
      http.get('/api/children/2', () => ok({ child: { id: 2, name: 'Aisyah Putri', is_active: true } })),
    )
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: makeLoginData({ user: makeUser({ role: 'FASILITATOR' }) }),
    })

    await screen.findByText('Hanya fasilitator kelompok ini yang dapat menilai.')
  })

  it('unknown stid renders the translated NOT_FOUND guard', async () => {
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get('/api/children/2', () => ok({ child: { id: 2, name: 'Aisyah Putri', is_active: true } })),
      http.get('/api/sessions/1/topics/99/children/2/assessments', () => ok([])),
      http.get('/api/sessions/1/topics/99/children/2/readiness', () =>
        ok({ complete: false, missing: [] }),
      ),
    )
    renderApp(null, {
      routes: [
        ...ROUTES,
        { path: '/sessions/:sid', element: <div>SESSION DETAIL</div> },
      ],
      route: '/assessment/1/2/99',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Data tidak ditemukan.')
    // The guard's back action returns to THIS session's detail.
    await userEvent.setup().click(screen.getByRole('button', { name: /Kembali/ }))
    await screen.findByText('SESSION DETAIL')
  })
})

describe('assessment header and body states', () => {
  it('a failed child fetch falls back to the #id header; a failed readiness read hides the indicator', async () => {
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get('/api/sessions/1/topics/3/children/2/assessments', () => ok([])),
      http.get('/api/sessions/1/topics/3/children/2/readiness', () =>
        HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } }),
      ),
      http.get('/api/children/2', () =>
        HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } }),
      ),
    )
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    // Header decoration only: the page works without the child name…
    await screen.findByText('#2')
    // …and without a readiness answer there is NO indicator (never a guess).
    expect(screen.queryByText('Belum lengkap')).toBeNull()
    expect(screen.queryByText('Topik lengkap')).toBeNull()
    expect(document.querySelector('.anticon-dash')).toBeNull()
    expect(document.querySelector('.anticon-check')).toBeNull()
  })

  it('a topic with zero activities renders the empty notice', async () => {
    const detail = detailFixture({ status: 'OPENED' })
    detail.topics[0].activities = []
    server.use(
      http.get('/api/sessions/1', () => ok(detail)),
      http.get('/api/sessions/1/topics/3/children/2/assessments', () => ok([])),
      http.get('/api/sessions/1/topics/3/children/2/readiness', () =>
        ok({ complete: false, missing: [] }),
      ),
      http.get('/api/children/2', () => ok({ child: { id: 2, name: 'Aisyah Putri', is_active: true } })),
    )
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Belum ada kegiatan untuk dinilai.')
  })

  it('a generic list failure renders the retry alert which re-runs the load', async () => {
    let assessmentsCalls = 0
    let failNext = true
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get('/api/sessions/1/topics/3/children/2/assessments', () => {
        assessmentsCalls += 1
        if (failNext) {
          return HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } })
        }
        return ok([])
      }),
      http.get('/api/sessions/1/topics/3/children/2/readiness', () =>
        ok({ complete: false, missing: [] }),
      ),
      http.get('/api/children/2', () => ok({ child: { id: 2, name: 'Aisyah Putri', is_active: true } })),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
    failNext = false
    const before = assessmentsCalls
    await user.click(screen.getByRole('button', { name: /Coba lagi/ }))

    // The retry re-runs loadAll — the same assessments GET succeeds now.
    await waitFor(() => expect(assessmentsCalls).toBeGreaterThan(before))
    expect(screen.queryByText('Layanan sedang sibuk. Coba lagi.')).toBeNull()
  })

  it('multi-topic sessions render the topic tabs and navigating swaps the stid', async () => {
    const detail = detailFixture({ status: 'OPENED' })
    detail.topics.push({
      stid: 4,
      topic_id: 10,
      name: 'Tenun Nusantara',
      sort_order: 2,
      activities: [
        { said: 20, activity_id: 110, name: 'Mengamati tenun', indicator: 'Menyebut 2 motif', sort_order: 1 },
      ],
    })
    server.use(
      http.get('/api/sessions/1', () => ok(detail)),
      http.get('/api/sessions/1/topics/3/children/2/assessments', () => ok([])),
      http.get('/api/sessions/1/topics/3/children/2/readiness', () =>
        ok({ complete: false, missing: [] }),
      ),
      http.get('/api/sessions/1/topics/4/children/2/assessments', () => ok([])),
      http.get('/api/sessions/1/topics/4/children/2/readiness', () =>
        ok({ complete: true, missing: [] }),
      ),
      http.get('/api/children/2', () => ok({ child: { id: 2, name: 'Aisyah Putri', is_active: true } })),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/assessment/1/2/3', session: ADMIN_SESSION() })

    await screen.findAllByText(/Mengamati motif/)
    await user.click(screen.getByRole('tab', { name: 'Tenun Nusantara' }))

    // The tab navigates to the other topic binding — its activity renders.
    await screen.findByText(/Mengamati tenun/)
    expect(screen.getByText('Penilaian 0/1')).toBeInTheDocument()
  })

  it('assessment.changed refetches the rows through the event table', async () => {
    let assessmentsCalls = 0
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get('/api/sessions/1/topics/3/children/2/assessments', () => {
        assessmentsCalls += 1
        return ok([])
      }),
      http.get('/api/sessions/1/topics/3/children/2/readiness', () =>
        ok({ complete: false, missing: [] }),
      ),
      http.get('/api/children/2', () => ok({ child: { id: 2, name: 'Aisyah Putri', is_active: true } })),
    )
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    await waitFor(() => expect(assessmentsCalls).toBe(1))
    const before = assessmentsCalls
    act(() => emitSignal('assessment.changed', { rev: 1 }))
    await waitFor(() => expect(assessmentsCalls).toBeGreaterThan(before))
  })

  it('an SSE refetch never clobbers a still-dirty local edit', async () => {
    const capture: PutCapture = { calls: 0, body: null }
    // First answer prefills 'Catatan awal'; the SSE-triggered refetch answers
    // a DIFFERENT stale note so a clobber would be visible in the field.
    let refetchCalls = 0
    server.use(
      ...assessmentHandlers(capture, {
        assessments: [{ said: 10, stars: 4, note: 'Catatan awal', updated_at: '2026-09-30T08:00:00Z' }],
      }),
      http.get('/api/sessions/1/topics/3/children/2/assessments', () => {
        refetchCalls += 1
        const note = refetchCalls === 1 ? 'Catatan awal' : 'Nilai server basi'
        return ok([{ said: 10, stars: 4, note, updated_at: '2026-09-30T08:00:00Z' }])
      }),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    const cards = (await screen.findAllByText(/Mengamati motif/))[0].closest('.ant-card') as HTMLElement
    // Assess first, then REPLACE the prefilled note (dirty) WITHOUT saving.
    await user.click(within(cards).getAllByRole('radio', { name: '4 — BSB' })[0])
    await screen.findByText('Tersimpan')
    const note = within(cards).getByPlaceholderText(
      'Catatan untuk kegiatan ini (maks. 2000 karakter)',
    )
    await user.clear(note)
    await user.type(note, 'Edit lokal')

    // The refetch lands with the stale note — the dirty local edit wins.
    act(() => emitSignal('assessment.changed', { rev: 1 }))
    await waitFor(() => expect(capture.calls).toBe(1))
    expect(note).toHaveValue('Edit lokal')
  })

  it('a detail fetch failure renders the translated alert with a back action', async () => {
    server.use(
      http.get('/api/sessions/1', () =>
        HttpResponse.json({ error: { code: 'FORBIDDEN_ROLE', message: 'nope' } }),
      ),
      http.get('/api/children/2', () => ok({ child: { id: 2, name: 'Aisyah Putri', is_active: true } })),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: [
        ...ROUTES,
        { path: '/sessions', element: <div>SESSIONS LIST</div> },
      ],
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    await screen.findByText('Anda tidak punya akses untuk tindakan ini.')
    await user.click(screen.getByRole('button', { name: /Kembali/ }))
    await screen.findByText('SESSIONS LIST')
  })
})

describe('assessment realtime readiness', () => {
  it('readiness.changed flips the topic-complete indicator through the gate', async () => {
    useSseFakeTimers()
    const capture: PutCapture = { calls: 0, body: null }
    const channel = sseChannel('/api/stream')
    server.use(channel.handler, ...assessmentHandlers(capture))
    renderApp(null, {
      routes: [
        {
          path: '/assessment/:sid/:cid/:stid',
          element: (
            <>
              <StreamGate />
              <AssessmentPage />
            </>
          ),
        },
      ],
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText(/Mengamati motif/) !== null)
    await until(() => screen.queryByText('Belum lengkap') !== null)
    expect(document.querySelector('.anticon-dash')).not.toBeNull()

    pushFrame(channel, 'readiness.changed', {
      rev: 1,
      child_id: 2,
      stid: 3,
      complete: true,
    })
    await until(() => screen.queryByText('Topik lengkap') !== null)
    expect(document.querySelector('.anticon-check')).not.toBeNull()
    expect(document.querySelector('.anticon-dash')).toBeNull()
  })
})

describe('assessment draft notice (beforeunload)', () => {
  /** Dispatch a cancellable beforeunload and report whether it was blocked. */
  const fireUnload = () => {
    const event = new Event('beforeunload', { cancelable: true })
    window.dispatchEvent(event)
    return event.defaultPrevented
  }

  it('arms the warning only while a card holds unsaved input', async () => {
    const capture: PutCapture = { calls: 0, body: null }
    server.use(...assessmentHandlers(capture))
    const user = userEvent.setup()
    const { unmount } = renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    const cards = (await screen.findAllByText(/Mengamati motif/))[0].closest('.ant-card') as HTMLElement

    // Clean load: nothing to lose → no warning, no hint.
    expect(fireUnload()).toBe(false)
    expect(screen.queryByText(/belum tersimpan/)).toBeNull()

    // Assess (auto-save) — once Tersimpan lands the baseline adopts it.
    await user.click(within(cards).getAllByRole('radio', { name: '4 — BSB' })[0])
    await screen.findByText('Tersimpan')
    expect(fireUnload()).toBe(false)

    // Type a note WITHOUT saving → warning armed + hint visible.
    const note = within(cards).getByPlaceholderText(
      'Catatan untuk kegiatan ini (maks. 2000 karakter)',
    )
    await user.type(note, 'Draft belum dikirim')
    expect(fireUnload()).toBe(true)
    expect(screen.getByText(/belum tersimpan/)).toBeInTheDocument()

    // Save success adopts the note into the baseline → warning disarms.
    await user.click(within(cards).getByRole('button', { name: 'Simpan' }))
    await waitFor(() => expect(capture.calls).toBe(2))
    await waitFor(() => expect(fireUnload()).toBe(false))
    expect(screen.queryByText(/belum tersimpan/)).toBeNull()

    // Unmount removes the listener entirely.
    unmount()
    expect(fireUnload()).toBe(false)
  })

  it('stays armed when the save fails', async () => {
    const capture: PutCapture = { calls: 0, body: null }
    server.use(
      ...assessmentHandlers(capture, {
        put: () => HttpResponse.json({ error: { code: 'INTERNAL', message: 'boom' } }),
      }),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: ADMIN_SESSION(),
    })

    const cards = (await screen.findAllByText(/Mengamati motif/))[0].closest('.ant-card') as HTMLElement
    await user.click(within(cards).getAllByRole('radio', { name: '4 — BSB' })[0])

    await within(cards).findByText('Gagal menyimpan')
    // The failed save never adopted a baseline → the input is still at risk.
    await waitFor(() => expect(fireUnload()).toBe(true))
    expect(screen.getByText(/belum tersimpan/)).toBeInTheDocument()
  })
})
