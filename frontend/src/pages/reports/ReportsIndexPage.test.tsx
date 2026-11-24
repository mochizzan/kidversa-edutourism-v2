/**
 * `/reports` index: the shared session picker routes each row into the
 * per-session reports matrix.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import { http, HttpResponse } from 'msw'
import ReportsIndexPage from './ReportsIndexPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { sessionRow } from '../../test/helpers.cov-session'

describe('reports index page', () => {
  it('lists sessions with a row action into their reports matrix', async () => {
    server.use(
      http.get('/api/sessions', () =>
        HttpResponse.json({
          data: [sessionRow(1, 'Kelas Batik Senin'), sessionRow(2, 'Kelas Batik Selasa')],
          meta: { page: 1, per_page: 20, total: 2 },
        }),
      ),
    )
    renderApp(<ReportsIndexPage />, { route: '/reports' })

    expect(await screen.findByText('Kelas Batik Senin')).toBeInTheDocument()
    expect(screen.getByText('Kelas Batik Selasa')).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: 'Lihat' })).toHaveLength(2)
    screen.getByText('Rapor sesi')
    screen.getByText('Pilih sesi')
  })
})
