/**
 * `/sessions/:sid/reports/topics/:stid` (REQ9): the participant table for one
 * topic. Rows come from the session reports matrix scoped by `stid`; each row
 * opens the focused child report page, and the batch toolbar is the submit
 * action. The header links back to the topic picker with an explicit button.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import ReportsParticipantsPage from './ReportsParticipantsPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok } from '../../test/handlers'
import { detailFixture, reportsRow } from '../../test/helpers.cov-session'
import type { RouteObject } from 'react-router'

const ROUTES: RouteObject[] = [
  { path: '/sessions/:sid/reports/topics/:stid', element: <ReportsParticipantsPage /> },
  {
    path: '/sessions/:sid/reports/topics/:stid/children/:cid',
    element: <div>Rapor anak</div>,
  },
  { path: '/sessions/:sid/reports/topics', element: <div>Daftar topik</div> },
]

describe('reports participants page', () => {
  it('lists the topic participants; each row opens the focused report page', async () => {
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get('/api/sessions/1/reports', () =>
        ok([
          reportsRow(2, 'Aisyah Putri', 3),
          reportsRow(3, 'Budi Santoso', 3),
          reportsRow(2, 'Aisyah Putri', 4, { topic_name: 'Tenun Nusantara' }),
        ]),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/sessions/1/reports/topics/3' })

    // The stid scope keeps only topic 3 rows (child 2 appears twice in the
    // raw matrix but once here).
    expect(await screen.findAllByText('Aisyah Putri', {}, { timeout: 5000 })).toHaveLength(1)
    screen.getByText('Budi Santoso')
    screen.getByText('Kelas Batik')
    screen.getByText('Batik Nusantara')

    await user.click(screen.getAllByRole('button', { name: 'Buka rapor' })[0])
    expect(await screen.findByText('Rapor anak')).toBeInTheDocument()
  })

  it('the back action returns to the topic picker', async () => {
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get('/api/sessions/1/reports', () => ok([reportsRow(2, 'Aisyah Putri')])),
    )
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/sessions/1/reports/topics/3' })

    await screen.findByText('Aisyah Putri', {}, { timeout: 5000 })
    await user.click(screen.getByRole('button', { name: /Kembali ke topik/ }))
    expect(await screen.findByText('Daftar topik')).toBeInTheDocument()
  })

  it('an unknown topic renders NOT_FOUND instead of an empty participant table', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))))
    renderApp(null, { routes: ROUTES, route: '/sessions/1/reports/topics/99' })

    expect(await screen.findByText('Data tidak ditemukan.')).toBeInTheDocument()
  })
})
