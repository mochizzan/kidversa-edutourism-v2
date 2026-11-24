/**
 * Public consent respond integration (FL-11 first-wins): a decision POSTs
 * once; with `already: true` the SERVER's recorded respondent wins (the
 * typed name is not adopted) and the decided state locks the form.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http } from 'msw'
import ConsentPage from './ConsentPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { idleStream, ok } from '../../test/handlers'

const CONSENT = {
  session: { name: 'Kelas Batik', date: '2026-10-09', location: 'Museum Tekstil' },
  child: { name: 'Aisyah Putri', school: 'SD Cendekia' },
  status: 'PENDING',
  respondent_name: null,
  responded_at: null,
}

interface RespondCapture {
  calls: number
  body: unknown
}

function consentHandlers(capture: RespondCapture, response: unknown) {
  return [
    http.get('/public/consent', () => ok(CONSENT)),
    idleStream('/public/stream'),
    http.post('/public/consent/respond', async ({ request }) => {
      capture.calls += 1
      capture.body = await request.json()
      return ok(response)
    }),
  ]
}

async function respond(user: UserEvent): Promise<void> {
  const name = await screen.findByLabelText('Nama responden')
  await user.type(name, 'Ibu Sari')
  await user.click(screen.getByRole('button', { name: 'Setuju' }))
}

describe('consent respond first-wins form', () => {
  it('already:true keeps the server record — the typed name is NOT adopted', async () => {
    const capture: RespondCapture = { calls: 0, body: null }
    server.use(...consentHandlers(capture, { decision: 'SETUJU', already: true }))
    const user = userEvent.setup()
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await respond(user)

    await screen.findByText('Keputusan Anda sudah tercatat: Disetujui.')
    expect(capture.calls).toBe(1)
    expect(capture.body).toEqual({
      token: 'tok-1',
      decision: 'SETUJU',
      respondent_name: 'Ibu Sari',
    })
    // First-wins: the typed name must NOT be adopted over the server record.
    expect(screen.queryByText('Nama responden: Ibu Sari')).toBeNull()
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Setuju' })).toBeDisabled()
      expect(screen.getByRole('button', { name: 'Tolak' })).toBeDisabled()
    })
    expect(screen.getByLabelText('Nama responden')).toBeDisabled()
  })

  it('a first decision renders the typed respondent name', async () => {
    const capture: RespondCapture = { calls: 0, body: null }
    server.use(...consentHandlers(capture, { decision: 'SETUJU' }))
    const user = userEvent.setup()
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-2' })

    await respond(user)

    await screen.findByText('Keputusan Anda sudah tercatat: Disetujui.')
    expect(capture.calls).toBe(1)
    // A first (non-already) decision renders the respondent name it recorded.
    await screen.findByText('Nama responden: Ibu Sari')
  })
})
