/**
 * Single report send (F-12 / FL-13): the default (mother) send carries no
 * body, a chosen `to` rides along, the SENT envelope toasts, the FAILED
 * envelope surfaces the code with the manual copy-URL fallback, and
 * ApiErrors render translated inline.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import SendReportPanel from './SendReportPanel'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok } from '../../test/handlers'
import { stubClipboard } from '../../test/helpers.cov-session'
import type { PublicLink } from '../../lib/session-contracts'

const P = '/api/sessions/1/topics/3/children/2'

const LINKS: PublicLink[] = [
  {
    token: 't1',
    kind: 'REPORT',
    url: 'https://kidversa.id/view/report?token=t1',
    created_at: '2026-10-09T10:00:00Z',
  },
  {
    token: 'g1',
    kind: 'GALLERY',
    url: 'https://kidversa.id/view/gallery?token=g1',
    created_at: '2026-10-09T10:00:00Z',
  },
]

function childHandler(fatherWa: string | null) {
  return http.get('/api/children/2', () =>
    ok({ id: 2, name: 'Aisyah Putri', mother_wa: '+62811', father_wa: fatherWa }),
  )
}

function renderPanel(links: PublicLink[] = LINKS) {
  renderApp(<SendReportPanel prefix={P} cid={2} links={links} />)
}

describe('send report panel', () => {
  it('default send posts no body and toasts Terkirim', async () => {
    let body = 'UNSET'
    let calls = 0
    server.use(
      childHandler(null),
      http.post(`${P}/send-report`, async ({ request }) => {
        calls += 1
        body = await request.text()
        return ok({ status: 'SENT' })
      }),
    )
    const user = userEvent.setup()
    renderPanel()

    await user.click(await screen.findByRole('button', { name: /Kirim WhatsApp/ }))

    await screen.findByText('Terkirim.')
    await waitFor(() => expect(calls).toBe(1))
    // Default target: the server picks the mother's number — no `to` sent.
    expect(body).toBe('')
  })

  it('a chosen father target rides in the {to} body', async () => {
    let body: unknown = null
    server.use(
      childHandler('+6282222222'),
      http.post(`${P}/send-report`, async ({ request }) => {
        body = await request.json()
        return ok({ status: 'SENT' })
      }),
    )
    const user = userEvent.setup()
    renderPanel()

    await screen.findByRole('button', { name: /Kirim WhatsApp/ })
    await user.click(screen.getByRole('combobox'))
    await user.click(await screen.findByText('Ayah · +6282222222'))
    await user.click(screen.getByRole('button', { name: /Kirim WhatsApp/ }))

    await waitFor(() => expect(body).toEqual({ to: '+6282222222' }))
    await screen.findByText('Terkirim.')
  })

  it('the FAILED envelope shows the code and the manual copy fallback', async () => {
    server.use(
      childHandler(null),
      http.post(`${P}/send-report`, () => ok({ status: 'FAILED', code: 'WA_FAILED' })),
    )
    const user = userEvent.setup()
    const clipboard = stubClipboard()
    renderPanel()

    await user.click(await screen.findByRole('button', { name: /Kirim WhatsApp/ }))

    await screen.findByText('WhatsApp gagal mengirim. Coba lagi atau salin URL manual.')
    screen.getByText('Salin tautan rapor manual:')
    await user.click(screen.getByRole('button', { name: 'REPORT' }))

    await screen.findByText('Tersalin')
    expect(clipboard.writes).toEqual(['https://kidversa.id/view/report?token=t1'])
  })

  it('an ApiError send renders the translated code inline', async () => {
    server.use(
      childHandler(null),
      http.post(`${P}/send-report`, () =>
        HttpResponse.json({ error: { code: 'VALIDATION', message: 'bad target' } }),
      ),
    )
    const user = userEvent.setup()
    renderPanel()

    await user.click(await screen.findByRole('button', { name: /Kirim WhatsApp/ }))

    // The h2 title shares the button's label — scope the alert to the card.
    const [title] = await screen.findAllByText('Kirim WhatsApp')
    const card = title.closest('.ant-card') as HTMLElement
    await within(card).findByText('Data tidak valid. Periksa kembali isian Anda.')
  })

  it('a FAILED envelope without a code falls back to WA_FAILED; a denied copy toasts', async () => {
    server.use(
      childHandler(null),
      // No `code` in the envelope → the renderer must default the message.
      http.post(`${P}/send-report`, () => ok({ status: 'FAILED' })),
    )
    const user = userEvent.setup()
    renderPanel()

    await user.click(await screen.findByRole('button', { name: /Kirim WhatsApp/ }))
    await screen.findByText('WhatsApp gagal mengirim. Coba lagi atau salin URL manual.')

    // The clipboard rejects → the error toast, not "Tersalin".
    stubClipboard([false])
    await user.click(screen.getByRole('button', { name: 'REPORT' }))
    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
  })
})
