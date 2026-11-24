/**
 * Session create wizard integration (F-08 / FL-07): step validation blocks
 * an empty meta step; a valid run posts the contract payload with an
 * auto-generated X-Idempotency-Key and lands on the new detail page.
 * Groups stay empty → no `groups` key in the body.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { useParams, type RouteObject } from 'react-router'
import SessionCreatePage from './SessionCreatePage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, paged } from '../../test/handlers'

function DetailMarker() {
  const { sid } = useParams()
  return <div>SESSION DETAIL {sid}</div>
}

const ROUTES: RouteObject[] = [
  { path: '/sessions/new', element: <SessionCreatePage /> },
  { path: '/sessions/:sid', element: <DetailMarker /> },
]

interface CreateCapture {
  calls: number
  headers: Headers | null
  body: unknown
}

function wizardHandlers(capture: CreateCapture) {
  return [
    http.get('/api/programs', () =>
      paged(
        [
          { id: 1, name: 'Eksplorasi Kota', eligible: true },
          { id: 2, name: 'Program Kosong', eligible: false },
        ],
        { page: 1, per_page: 1000, total: 2 },
      ),
    ),
    http.get('/api/children', () => paged([], { page: 1, per_page: 1000, total: 0 })),
    http.post('/api/sessions', async ({ request }) => {
      capture.calls += 1
      capture.headers = new Headers(request.headers)
      capture.body = await request.json()
      return HttpResponse.json({ data: { session_id: 42 } })
    }),
  ]
}

async function pickProgram(user: UserEvent): Promise<void> {
  await user.click(await screen.findByRole('combobox'))
  // rc-select's virtual list keeps role="option" on a non-interactive ARIA
  // mirror; the interactive option carries the label as its TEXT content.
  await user.click(await screen.findByText('Eksplorasi Kota'))
  await user.click(screen.getByRole('button', { name: /Lanjut/ }))
}

describe('form session create wizard', () => {
  it('blocks an empty meta step with the mirrored error messages', async () => {
    const capture: CreateCapture = { calls: 0, headers: null, body: null }
    server.use(...wizardHandlers(capture))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)
    // Step "Detail": submit as-is.
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))

    await screen.findByText('Nama sesi wajib diisi.')
    await screen.findByText('Tanggal wajib diisi.')
    await screen.findByText('Lokasi wajib diisi.')
    expect(capture.calls).toBe(0)
  })

  it('creates a session with the contract payload and idem key, then opens its detail', async () => {
    const capture: CreateCapture = { calls: 0, headers: null, body: null }
    server.use(...wizardHandlers(capture))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)

    // Meta step: antd Form.Item labels — placeholders (name, description, location).
    await user.type(screen.getByPlaceholderText('mis. Kelas Batik'), 'Kelas Batik')
    await user.type(screen.getByPlaceholderText('mis. Museum Tekstil'), 'Museum Tekstil')
    // antd DatePicker (rc-picker 4 mask input): commits via keyboard + Enter.
    await user.type(document.querySelector('.ant-picker-input > input') as Element, '2026-11-15{Enter}')

    await user.click(screen.getByRole('button', { name: /Lanjut/ }))
    // Groups are optional → create right away.
    await user.click(screen.getByRole('button', { name: 'Buat sesi' }))

    await screen.findByText('SESSION DETAIL 42')
    expect(capture.calls).toBe(1)
    expect(capture.body).toEqual({
      program_id: 1,
      name: 'Kelas Batik',
      date: '2026-11-15',
      location: 'Museum Tekstil',
    }) // description/groups absent when blank/empty
    expect(capture.headers?.get('X-Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/)
    expect(capture.headers?.get('X-School-ID')).toBe('1')
  })
})
