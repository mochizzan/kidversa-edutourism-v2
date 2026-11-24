/**
 * Consent list (F-10 / FL-11): paged rows with the copyable pending URL,
 * SEND/RESEND gated to ADMIN/SA and hidden for FAS and Finished sessions,
 * the FL-11 resend envelopes (resent vs already-decided), the status filter,
 * and the inline send-error passthrough.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import type { RouteObject } from 'react-router'
import ConsentListPage from './ConsentListPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, ok, paged } from '../../test/handlers'
import { consentItem, detailFixture, stubClipboard } from '../../test/helpers.cov-session'
import type { ConsentItem } from '../../lib/session-contracts'

const ROUTES: RouteObject[] = [{ path: '/consent/:sid', element: <ConsentListPage /> }]

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })
const FAS_SESSION = () => makeLoginData({ user: makeUser({ role: 'FASILITATOR' }) })

interface ConsentCalls {
  listUrls: URL[]
  sends: number
}

function consentHandlers(
  calls: ConsentCalls,
  rows: () => ConsentItem[],
  options: { status?: 'OPENED' | 'FINISHED'; send?: () => Response } = {},
) {
  return [
    http.get('/api/sessions/1', () => ok(detailFixture({ status: options.status ?? 'OPENED' }))),
    http.get('/api/sessions/1/consents', ({ request }) => {
      calls.listUrls.push(new URL(request.url))
      const total = rows().length
      return paged(rows(), { page: 1, per_page: 20, total })
    }),
    http.post('/api/sessions/1/children/2/send-consent', () => {
      calls.sends += 1
      return options.send
        ? options.send()
        : ok({ token: 'tok', decision: 'PENDING', resent: true })
    }),
  ]
}

function newCalls(): ConsentCalls {
  return { listUrls: [], sends: 0 }
}

describe('consent list send gating', () => {
  it('ADMIN sees send + copy on a PENDING row; send runs the FL-11 resent flow', async () => {
    const calls = newCalls()
    server.use(
      ...consentHandlers(calls, () => [consentItem(2, 'Aisyah Putri')]),
    )
    const user = userEvent.setup()
    // After userEvent.setup(): its own clipboard stub must not shadow ours.
    const clipboard = stubClipboard()
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    await screen.findByText('Aisyah Putri')
    // The pending URL is copyable.
    await user.click(screen.getByRole('button', { name: /Salin tautan/ }))
    await screen.findByText('Tersalin')
    expect(clipboard.writes).toEqual(['https://wa.id/v1/consent/2'])

    // Send asks for confirmation, then posts the contract route.
    await user.click(screen.getByRole('button', { name: /Kirim persetujuan/ }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText('Kirim persetujuan ke WhatsApp ibu Aisyah Putri?'),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: /Kirim persetujuan/ }))

    await screen.findByText('Persetujuan terkirim.')
    await waitFor(() => expect(calls.sends).toBe(1))
    // Own-200 refetch of the list.
    await waitFor(() => expect(calls.listUrls.length).toBeGreaterThan(1))
  })

  it('already-decided resend shows the info envelope (no new link)', async () => {
    const calls = newCalls()
    server.use(
      ...consentHandlers(calls, () => [consentItem(2, 'Aisyah Putri')], {
        send: () => ok({ token: 'tok', decision: 'SETUJU', resent: false }),
      }),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    await screen.findByText('Aisyah Putri')
    await user.click(screen.getByRole('button', { name: /Kirim persetujuan/ }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /Kirim persetujuan/ }))

    await screen.findByText(
      'Sudah Disetujui — tautan tidak berubah dan WhatsApp tidak dikirim ulang.',
    )
  })

  it('FAS sees the read-only state: no send button, but the copy URL stays', async () => {
    const calls = newCalls()
    server.use(...consentHandlers(calls, () => [consentItem(2, 'Aisyah Putri')]))
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: FAS_SESSION() })

    await screen.findByText('Aisyah Putri')
    screen.getByText('Hanya dapat dilihat.')
    expect(screen.queryByRole('button', { name: /Kirim persetujuan/ })).toBeNull()
    screen.getByRole('button', { name: /Salin tautan/ })
  })

  it('Finished sessions hide the send action even for ADMIN', async () => {
    const calls = newCalls()
    server.use(
      ...consentHandlers(calls, () => [consentItem(2, 'Aisyah Putri')], { status: 'FINISHED' }),
    )
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    await screen.findByText('Aisyah Putri')
    expect(screen.queryByRole('button', { name: /Kirim persetujuan/ })).toBeNull()
  })

  it('a decided row shows respondent/time and no action buttons', async () => {
    const calls = newCalls()
    server.use(
      ...consentHandlers(calls, () => [
        consentItem(3, 'Budi Santoso', {
          status: 'SETUJU',
          respondent_name: 'Ibu Sari',
          responded_at: '2026-10-09T10:00:00.000Z',
          consent_url: null,
        }),
      ]),
    )
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    await screen.findByText('Budi Santoso')
    screen.getByText('Ibu Sari')
    // Decided rows carry no copy/send affordances.
    expect(screen.queryByRole('button', { name: /Salin tautan/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Kirim persetujuan/ })).toBeNull()
  })
})

describe('consent list filter and errors', () => {
  it('the status filter narrows the server query', async () => {
    const calls = newCalls()
    server.use(...consentHandlers(calls, () => [consentItem(2, 'Aisyah Putri')]))
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    await screen.findByText('Aisyah Putri')
    expect(calls.listUrls[0].searchParams.get('status')).toBeNull()

    // PageHeader actions hold the status Select (index 0; the pager's
    // page-size Select follows once meta exists).
    await user.click(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('Disetujui'))

    await waitFor(() => {
      expect(calls.listUrls.at(-1)?.searchParams.get('status')).toBe('SETUJU')
    })
  })

  it('the group filter narrows the server query', async () => {
    const calls = newCalls()
    server.use(
      http.get('/api/sessions/1', () =>
        ok(
          detailFixture({
            status: 'OPENED',
            groups: [
              { gid: 5, name: 'Kelompok Merah', own: true, member_count: 1 },
              {
                gid: 6,
                name: 'Kelompok Biru',
                own: false,
                member_count: 1,
                status: 'OPENED',
              },
            ],
          }),
        ),
      ),
      ...consentHandlers(calls, () => [consentItem(2, 'Aisyah Putri')]),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    await screen.findByText('Aisyah Putri')
    expect(calls.listUrls[0].searchParams.get('group_id')).toBeNull()

    // Combobox 0 is the status filter, 1 is the group filter.
    await user.click(screen.getAllByRole('combobox')[1])
    await user.click(await screen.findByText('Kelompok Biru'))

    await waitFor(() => {
      expect(calls.listUrls.at(-1)?.searchParams.get('group_id')).toBe('6')
    })
  })

  it('NOT_SENT is a selectable filter option', async () => {
    const calls = newCalls()
    server.use(...consentHandlers(calls, () => [consentItem(2, 'Aisyah Putri')]))
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    await screen.findByText('Aisyah Putri')
    await user.click(screen.getAllByRole('combobox')[0])
    await user.click(await screen.findByText('Belum diserahkan'))

    await waitFor(() => {
      expect(calls.listUrls.at(-1)?.searchParams.get('status')).toBe('NOT_SENT')
    })
  })

  it('bulk send posts the checked child ids and reports per-item outcomes', async () => {
    const calls = newCalls()
    const bulkBodies: Array<{ child_ids: number[] }> = []
    server.use(
      http.post('/api/sessions/1/send-consent', async ({ request }) => {
        bulkBodies.push((await request.json()) as { child_ids: number[] })
        return ok({
          items: [
            { child_id: 2, status: 'SENT' },
            { child_id: 3, status: 'SKIPPED', decision: 'SETUJU' },
          ],
        })
      }),
      ...consentHandlers(calls, () => [
        consentItem(2, 'Aisyah Putri', { status: 'PENDING', consent_url: null }),
        consentItem(3, 'Budi Santoso', { status: 'PENDING' }),
        consentItem(4, 'Citra Dewi', {
          status: 'SETUJU',
          respondent_name: 'Ibu Sari',
          consent_url: null,
        }),
      ]),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    await screen.findByText('Aisyah Putri')
    screen.getByText('Pilih anak yang masih memerlukan permintaan persetujuan.')

    // Row checkboxes only; index 0 is the select-all header.
    const boxes = screen.getAllByRole('checkbox')
    expect(boxes).toHaveLength(4)
    expect(boxes[3]).toBeDisabled() // already decided
    await user.click(boxes[1])
    await user.click(boxes[2])
    screen.getByText('2 dipilih')

    await user.click(screen.getByRole('button', { name: /Kirim yang dipilih/ }))
    const dialog = await screen.findByRole('dialog')
    expect(
      within(dialog).getByText('Kirim permintaan persetujuan ke 2 anak?'),
    ).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: /Kirim yang dipilih/ }))

    await screen.findByText('1 terkirim, 0 gagal, 1 sudah memutuskan.')
    expect(bulkBodies).toEqual([{ child_ids: [2, 3] }])
    await waitFor(() => expect(calls.listUrls.length).toBeGreaterThan(1))
  })

  it('passes the send-error server message through inline', async () => {
    const calls = newCalls()
    server.use(
      ...consentHandlers(calls, () => [consentItem(2, 'Aisyah Putri')], {
        send: () =>
          HttpResponse.json({
            error: { code: 'VALIDATION', message: 'Nomor WhatsApp ibu tidak terdaftar.' },
          }),
      }),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    await screen.findByText('Aisyah Putri')
    await user.click(screen.getByRole('button', { name: /Kirim persetujuan/ }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: /Kirim persetujuan/ }))

    await within(dialog).findByText('Nomor WhatsApp ibu tidak terdaftar.')
  })

  it('renders the detail envelope error translated with a back link', async () => {
    server.use(
      http.get('/api/sessions/1', () =>
        HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'gone' } }),
      ),
    )
    renderApp(null, { routes: ROUTES, route: '/consent/1', session: ADMIN_SESSION() })

    const back = await screen.findByRole('link', { name: 'Kembali' })
    expect(back).toHaveAttribute('href', '/consent')
    screen.getByText('Data tidak ditemukan.')
  })
})
