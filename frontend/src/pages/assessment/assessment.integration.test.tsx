/**
 * Assessment save integration (FL-09 / F-09): the topic page loads
 * detail + assessments + readiness + child, and clicking a star PUTs
 * `{stars}` to the per-activity binding path and shows "Tersimpan".
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import type { RouteObject } from 'react-router'
import AssessmentPage from './AssessmentPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, ok } from '../../test/handlers'

const ROUTES: RouteObject[] = [
  { path: '/assessment/:sid/:cid/:stid', element: <AssessmentPage /> },
]

interface PutCapture {
  calls: number
  url: string | null
  body: unknown
}

function assessmentHandlers(capture: PutCapture) {
  return [
    http.get('/api/sessions/1', () =>
      ok({
        session: {
          id: 1,
          program_id: 1,
          program_name: 'Eksplorasi Kota',
          name: 'Kelas Batik',
          description: '',
          date: '2026-10-09',
          start_time: null,
          end_time: null,
          location: 'Museum Tekstil',
          status: 'OPENED',
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
              { said: 10, activity_id: 99, name: 'Mengamati motif', indicator: 'Menyebutkan 3 motif', sort_order: 1 },
              { said: 11, activity_id: 100, name: 'Menggambar motif', indicator: 'Meniru pola', sort_order: 2 },
            ],
          },
        ],
        groups: [],
      }),
    ),
    http.get('/api/sessions/1/topics/3/children/2/assessments', () => ok([])),
    http.get('/api/sessions/1/topics/3/children/2/readiness', () =>
      ok({ complete: false, missing: [{ said: 10, name: 'Mengamati motif' }] }),
    ),
    http.get('/api/children/2', () => ok({ id: 2, name: 'Aisyah Putri', is_active: true })),
    http.put('/api/sessions/1/topics/3/children/2/activities/10', async ({ request }) => {
      capture.calls += 1
      capture.url = request.url
      capture.body = await request.json()
      return HttpResponse.json({
        data: { said: 10, stars: 4, note: null, updated_at: '2026-10-09T10:00:00Z' },
      })
    }),
  ]
}

describe('assessment form save', () => {
  it('PUTs {stars} to the per-activity path and reports Tersimpan', async () => {
    const capture: PutCapture = { calls: 0, url: null, body: null }
    server.use(...assessmentHandlers(capture))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/assessment/1/2/3',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    // Cards render once detail + assessments are loaded.
    await screen.findByText('Mengamati motif')
    await screen.findByText('Menggambar motif')

    const star4 = screen.getAllByRole('radio', { name: '4 — BSB' })[0]
    await user.click(star4)

    await screen.findByText('Tersimpan')
    expect(capture.calls).toBe(1)
    expect(capture.url).toContain('/api/sessions/1/topics/3/children/2/activities/10')
    expect(capture.body).toEqual({ stars: 4 })
  })
})
