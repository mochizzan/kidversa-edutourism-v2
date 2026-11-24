/**
 * Public links manager (F-12 / FL-13): create posts {kind:'REPORT'} and
 * toasts, rows copy their URL through the clipboard, revoke and regenerate
 * both confirm then hit their endpoints, and failures render inline.
 */
import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import LinksPanel from './LinksPanel'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok } from '../../test/handlers'
import { expectToast } from '../../test/toast'
import { stubClipboard } from '../../test/helpers.cov-session'
import type { PublicLink } from '../../lib/session-contracts'

const P = '/api/sessions/1/topics/3/children/2'

const LINK: PublicLink = {
  token: 't1',
  kind: 'REPORT',
  url: 'https://kidversa.id/view/report?token=t1',
  created_at: '2026-10-09T10:00:00Z',
}

function renderPanel(links: PublicLink[], onChanged = vi.fn()) {
  renderApp(<LinksPanel prefix={P} links={links} onChanged={onChanged} />)
  return { onChanged }
}

describe('links panel', () => {
  it('creates the REPORT link pair and toasts', async () => {
    let posted: unknown = null
    server.use(
      http.post(`${P}/public-links`, async ({ request }) => {
        posted = await request.json()
        return ok([LINK, { ...LINK, token: 'g1', kind: 'GALLERY', url: 'https://kidversa.id/view/gallery?token=g1' }])
      }),
    )
    const { onChanged } = renderPanel([])
    const user = userEvent.setup()

    await screen.findByText('Belum ada tautan aktif.')
    await user.click(screen.getByRole('button', { name: /Buat tautan/ }))

    await screen.findByText('Tautan dibuat.')
    expect(posted).toEqual({ kind: 'REPORT' })
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })

  it('copies a row URL through the clipboard', async () => {
    const user = userEvent.setup()
    const clipboard = stubClipboard()
    renderPanel([LINK])

    await screen.findByText(LINK.url)
    await user.click(screen.getByRole('button', { name: /Salin/ }))

    await screen.findByText('Tersalin')
    expect(clipboard.writes).toEqual([LINK.url])
  })

  it('renders a GALLERY link with the neutral status tone and toasts a denied copy', async () => {
    const user = userEvent.setup()
    const GALLERY: PublicLink = {
      token: 'g1',
      kind: 'GALLERY',
      url: 'https://kidversa.id/view/gallery?token=g1',
      created_at: '2026-09-01T08:00:00Z',
    }
    renderPanel([GALLERY])

    await screen.findByText(GALLERY.url)
    // kind-dependent pill: the GALLERY tone (REPORT is covered above).
    const tag = screen.getByText('GALLERY')
    expect(tag.closest('.kv-status')).toHaveClass('kv-status--neutral')

    stubClipboard([false]) // navigator.clipboard.writeText rejects
    await user.click(screen.getByRole('button', { name: /Salin/ }))
    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
  })

  it('revokes after the confirm dialog', async () => {
    let revoked = 0
    server.use(
      http.post('/api/public-links/t1/revoke', () => {
        revoked += 1
        return ok({})
      }),
    )
    const { onChanged } = renderPanel([LINK])
    const user = userEvent.setup()

    await screen.findByText(LINK.url)
    await user.click(screen.getByRole('button', { name: 'Cabut' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Cabut tautan ini?')).toBeInTheDocument()
    expect(
      within(dialog).getByText('Tautan langsung tidak berlaku bagi siapa pun.'),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Cabut' }))

    await waitFor(() => expect(revoked).toBe(1))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
    await expectToast('Tautan dicabut.')
  })

  it('regenerates the pair after the confirm dialog', async () => {
    let regenerated = 0
    server.use(
      http.post('/api/public-links/t1/regenerate', () => {
        regenerated += 1
        return ok([LINK])
      }),
    )
    const { onChanged } = renderPanel([LINK])
    const user = userEvent.setup()

    await screen.findByText(LINK.url)
    await user.click(screen.getByRole('button', { name: /Buat ulang/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Buat ulang tautan?')).toBeInTheDocument()
    expect(
      within(dialog).getByText('Tautan lama dicabut; tautan baru langsung aktif.'),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Buat ulang' }))

    await screen.findByText('Tautan dibuat.')
    await waitFor(() => expect(regenerated).toBe(1))
    await waitFor(() => expect(onChanged).toHaveBeenCalled())
  })

  it('renders the action error inline', async () => {
    server.use(
      http.post(`${P}/public-links`, () =>
        HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } }),
      ),
    )
    renderPanel([])
    const user = userEvent.setup()

    await screen.findByText('Belum ada tautan aktif.')
    await user.click(screen.getByRole('button', { name: /Buat tautan/ }))

    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
  })

  it('a failed revoke renders the error INSIDE the open dialog', async () => {
    server.use(
      http.post('/api/public-links/t1/revoke', () =>
        HttpResponse.json({ error: { code: 'STATE_CONFLICT', message: 'STATE_CONFLICT' } }),
      ),
    )
    renderPanel([LINK])
    const user = userEvent.setup()

    await screen.findByText(LINK.url)
    await user.click(screen.getByRole('button', { name: 'Cabut' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Cabut' }))

    // message === code → the TRANSLATED fallback renders in the dialog body.
    await within(dialog).findByText('Data berubah di tempat lain. Muat ulang halaman.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('a failed regenerate renders the error INSIDE the open dialog', async () => {
    server.use(
      http.post('/api/public-links/t1/regenerate', () =>
        HttpResponse.json({ error: { code: 'STATE_CONFLICT', message: 'STATE_CONFLICT' } }),
      ),
    )
    renderPanel([LINK])
    const user = userEvent.setup()

    await screen.findByText(LINK.url)
    await user.click(screen.getByRole('button', { name: /Buat ulang/ }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Buat ulang' }))

    await within(dialog).findByText('Data berubah di tempat lain. Muat ulang halaman.')
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })
})
