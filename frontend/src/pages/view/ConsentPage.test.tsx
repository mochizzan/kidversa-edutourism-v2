/**
 * Public consent respond form (FL-11): the respondent name is required
 * before the first-wins POST is allowed; a missing name shows the inline
 * message and no request leaves the tab.
 *
 * Plus the load/realtime/respond contract (FL-11, 02§4): terminal
 * TOKEN_INVALID / no-token views, transient-error retry, the decided state
 * (labels per status, respondent record, first-wins `already`), envelope
 * errors on respond, `consent.ack` rendering the new status WITHOUT a
 * refetch, and `link.revoked` staying terminal even for an in-flight load.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import ConsentPage from './ConsentPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { idleStream, ok, sseChannel } from '../../test/handlers'
import { advance, pushFrame, until, useSseFakeTimers } from '../../test/helpers.cov-session'

const CONSENT = {
  session: { name: 'Kelas Batik', date: '2026-10-09', location: 'Museum Tekstil' },
  child: { name: 'Aisyah Putri', school: 'SD Cendekia' },
  status: 'PENDING',
  respondent_name: null,
  responded_at: null,
}

function baseHandlers(response: unknown = CONSENT) {
  return [http.get('/public/consent', () => ok(response)), idleStream('/public/stream')]
}

async function typeAndApprove(user: UserEvent): Promise<void> {
  await user.type(await screen.findByLabelText('Nama responden'), 'Ibu Sari')
  await user.click(screen.getByRole('button', { name: 'Setuju' }))
}

describe('form consent respond', () => {
  it('requires the respondent name before posting the decision', async () => {
    let respondCalls = 0
    server.use(
      ...baseHandlers(),
      http.post('/public/consent/respond', () => {
        respondCalls += 1
        return ok({ decision: 'SETUJU' })
      }),
    )
    const user = userEvent.setup()
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await screen.findByText('Apakah Anda memberikan izin agar anak Anda difoto atau didokumentasikan?')
    await user.click(screen.getByRole('button', { name: 'Setuju' }))

    await screen.findByText('Nama responden wajib diisi.')
    expect(respondCalls).toBe(0)
  })
})

describe('consent load states', () => {
  it('renders the session info with em-dash fallbacks for empty fields', async () => {
    server.use(
      ...baseHandlers({
        ...CONSENT,
        session: { ...CONSENT.session, location: '' },
        child: { name: 'Aisyah Putri', school: '' },
      }),
    )
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await screen.findByText('Sesi')
    // Empty location/school degrade to an em dash, never a blank slot.
    expect(screen.queryByText('Museum Tekstil')).toBeNull()
    expect(screen.getAllByText('—')).toHaveLength(2)
  })

  it('a transient load error renders translated with a working retry', async () => {
    let calls = 0
    server.use(
      http.get('/public/consent', () => {
        calls += 1
        if (calls === 1) {
          return HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } })
        }
        return ok(CONSENT)
      }),
      idleStream('/public/stream'),
    )
    const user = userEvent.setup()
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))

    await screen.findByText('Apakah Anda memberikan izin agar anak Anda difoto atau didokumentasikan?')
    expect(calls).toBe(2)
  })

  it('TOKEN_INVALID on load renders the terminal invalid view', async () => {
    server.use(
      http.get('/public/consent', () =>
        HttpResponse.json({ error: { code: 'TOKEN_INVALID', message: 'revoked' } }),
      ),
      idleStream('/public/stream'),
    )
    renderApp(<ConsentPage />, { route: '/view/consent?token=dead' })

    await screen.findByText('Tautan tidak valid atau sudah dicabut.')
    expect(screen.queryByRole('button', { name: 'Coba lagi' })).toBeNull()
  })

  it('a link without a token renders the invalid view and never fetches', async () => {
    server.use(...baseHandlers())
    renderApp(<ConsentPage />, { route: '/view/consent' })

    await screen.findByText('Tautan tidak valid atau sudah dicabut.')
  })
})

describe('consent decided state', () => {
  it('a first decision renders the typed name and locks the form', async () => {
    let body: unknown = null
    server.use(
      ...baseHandlers(),
      http.post('/public/consent/respond', async ({ request }) => {
        body = await request.json()
        return ok({ decision: 'SETUJU' })
      }),
    )
    const user = userEvent.setup()
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await typeAndApprove(user)

    await screen.findByText('Keputusan Anda sudah tercatat: Disetujui.')
    await screen.findByText('Nama responden: Ibu Sari')
    expect(body).toEqual({ token: 'tok-1', decision: 'SETUJU', respondent_name: 'Ibu Sari' })
    await waitFor(() => {
      expect(screen.getByRole('button', { name: 'Setuju' })).toBeDisabled()
      expect(screen.getByRole('button', { name: 'Tolak' })).toBeDisabled()
      expect(screen.getByLabelText('Nama responden')).toBeDisabled()
    })
  })

  it('already:true keeps the server record — the typed name is NOT adopted', async () => {
    server.use(
      ...baseHandlers(),
      http.post('/public/consent/respond', () => ok({ decision: 'SETUJU', already: true })),
    )
    const user = userEvent.setup()
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await typeAndApprove(user)

    await screen.findByText('Keputusan Anda sudah tercatat: Disetujui.')
    // First-wins: the typed name must NOT be adopted over the server record.
    expect(screen.queryByText('Nama responden: Ibu Sari')).toBeNull()
  })

  it('rejecting posts DITOLAK and renders the rejected label', async () => {
    let decision: unknown = null
    server.use(
      ...baseHandlers(),
      http.post('/public/consent/respond', async ({ request }) => {
        const payload: unknown = await request.json()
        if (payload && typeof payload === 'object' && 'decision' in payload) {
          decision = payload.decision
        }
        return ok({ decision: 'DITOLAK' })
      }),
    )
    const user = userEvent.setup()
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await user.type(await screen.findByLabelText('Nama responden'), 'Ibu Sari')
    await user.click(screen.getByRole('button', { name: 'Tolak' }))

    await screen.findByText('Keputusan Anda sudah tercatat: Ditolak.')
    expect(decision).toBe('DITOLAK')
  })

  it('an already-decided consent renders its record with the response time', async () => {
    server.use(
      ...baseHandlers({
        ...CONSENT,
        status: 'SETUJU',
        respondent_name: 'Ibu Sari',
        responded_at: '2026-10-09T10:00:00Z',
      }),
    )
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await screen.findByText('Keputusan Anda sudah tercatat: Disetujui.')
    screen.getByText('Nama responden: Ibu Sari')
    screen.getByText('2026-10-09T10:00:00Z')
    expect(screen.getByLabelText('Nama responden')).toBeDisabled()
  })

  it('an envelope error on respond renders translated inline', async () => {
    server.use(
      ...baseHandlers(),
      http.post('/public/consent/respond', () =>
        HttpResponse.json({ error: { code: 'RATE_LIMITED', message: 'slow' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await typeAndApprove(user)

    await screen.findByText('Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.')
    // The form stays usable: the decision was never recorded. (The button may
    // still be in its loading state for a beat after the toast renders.)
    await waitFor(() => expect(screen.getByRole('button', { name: 'Setuju' })).toBeEnabled())
  })
})

describe('consent realtime', () => {
  it('consent.ack renders the new status without a refetch; foreign decisions are ignored', async () => {
    useSseFakeTimers()
    let calls = 0
    const channel = sseChannel('/public/stream')
    server.use(
      channel.handler,
      http.get('/public/consent', () => {
        calls += 1
        return ok(CONSENT)
      }),
    )
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Apakah Anda memberikan izin agar anak Anda difoto atau didokumentasikan?') !== null)
    const loads = calls

    // An unknown decision must not flip the rendered status.
    pushFrame(channel, 'consent.ack', { rev: 1, decision: 'PENDING' })
    await advance(600)
    expect(screen.queryByText('Keputusan Anda sudah tercatat: Disetujui.')).toBeNull()

    pushFrame(channel, 'consent.ack', { rev: 2, decision: 'SETUJU' })
    await until(() => screen.queryByText('Keputusan Anda sudah tercatat: Disetujui.') !== null)
    // Signal → render only: no extra GET left the tab.
    expect(calls).toBe(loads)
  })

  it('link.revoked stays terminal even for a load still in flight', async () => {
    useSseFakeTimers()
    const gate = Promise.withResolvers<Response>()
    const channel = sseChannel('/public/stream')
    server.use(
      channel.handler,
      http.get('/public/consent', () => gate.promise),
    )
    renderApp(<ConsentPage />, { route: '/view/consent?token=tok-1' })

    await until(() => channel.handshakes === 1)
    pushFrame(channel, 'link.revoked', { rev: 1 })
    await until(() => screen.queryByText('Tautan tidak valid atau sudah dicabut.') !== null)

    // The slow response lands AFTER the revoke — it must not resurrect data.
    gate.resolve(ok(CONSENT))
    await advance(120_000)
    expect(screen.queryByText('Apakah Anda memberikan izin agar anak Anda difoto atau didokumentasikan?')).toBeNull()
    expect(within(document.body).queryByText('Kelas Batik')).toBeNull()
  })
})
