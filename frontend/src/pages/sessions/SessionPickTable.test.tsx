/**
 * Paged session picker (/reports + /consent index): one row action opens the
 * `to` mapper target, paging keeps the same contract, and the envelope error
 * renders translated with a working retry.
 */
import { describe, expect, it } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import SessionPickTable from './SessionPickTable'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { paged } from '../../test/handlers'
import { sessionRow } from '../../test/helpers.cov-session'

function listHandler(seen: URL[]) {
  return http.get('/api/sessions', ({ request }) => {
    const url = new URL(request.url)
    seen.push(url)
    const page = Number(url.searchParams.get('page') ?? '1')
    return HttpResponse.json({
      data: [sessionRow(1, 'Kelas Batik Senin'), sessionRow(2, 'Kelas Batik Selasa')],
      meta: { page, per_page: 20, total: 45 },
    })
  })
}

describe('session pick table', () => {
  it('renders one action per row and pages server-side', async () => {
    const seen: URL[] = []
    server.use(listHandler(seen))
    const user = userEvent.setup()
    renderApp(<SessionPickTable title="Pilih sesi" to={(id) => `/reports/${id}`} />, {
      route: '/reports',
    })

    expect(await screen.findByText('Kelas Batik Senin')).toBeInTheDocument()
    expect(screen.getByText('Kelas Batik Selasa')).toBeInTheDocument()
    // The row action is the only way in; the name is not a second link.
    expect(screen.queryByRole('link')).toBeNull()
    expect(screen.getAllByRole('button', { name: 'Lihat' })).toHaveLength(2)

    await user.click(screen.getByTitle('2'))
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('page')).toBe('2'))

    // Changing page size resets to page 1 (Pager hands the reset to onPage).
    await user.click(screen.getByRole('combobox', { name: 'Page Size' }))
    await user.click(
      await screen.findByText('50 / page', { selector: '.ant-select-item-option-content' }),
    )
    await waitFor(() => expect(seen.at(-1)?.searchParams.get('per_page')).toBe('50'))
    expect(seen.at(-1)?.searchParams.get('page')).toBe('1')
  })

  it('opens the to() target from the row action', async () => {
    server.use(listHandler([]))
    const user = userEvent.setup()
    renderApp(null, {
      route: '/reports',
      routes: [
        {
          path: '/reports',
          element: <SessionPickTable title="Pilih sesi" to={(id) => `/reports/${id}`} />,
        },
        { path: '/reports/:sid', element: <p>Sesi dipilih</p> },
      ],
    })

    await screen.findByText('Kelas Batik Senin')
    await user.click(screen.getAllByRole('button', { name: 'Lihat' })[0])
    expect(await screen.findByText('Sesi dipilih')).toBeInTheDocument()
  })

  it('renders the translated envelope error with a working retry', async () => {
    let failNext = true
    server.use(
      http.get('/api/sessions', () => {
        if (failNext) {
          failNext = false
          return HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } })
        }
        return paged([sessionRow(1, 'Kelas Batik Senin')], { page: 1, per_page: 20, total: 1 })
      }),
    )
    const user = userEvent.setup()
    renderApp(<SessionPickTable title="Pilih sesi" to={(id) => `/consent/${id}`} />, {
      route: '/consent',
    })

    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
    expect(screen.queryByRole('table')).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    await screen.findByText('Kelas Batik Senin')
  })
})
