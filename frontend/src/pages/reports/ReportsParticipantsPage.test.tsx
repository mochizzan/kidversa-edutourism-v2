/**
 * `/sessions/:sid/reports/topics/:stid` (REQ9): participants/children table —
 * each row opens the reports edit page (`ReportsPage`); the header links back
 * to the topic picker.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import ReportsParticipantsPage from './ReportsParticipantsPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok } from '../../test/handlers'
import { detailFixture } from '../../test/helpers.cov-session'
import type { RouteObject } from 'react-router'

const ROUTES: RouteObject[] = [
  { path: '/sessions/:sid/reports/topics/:stid', element: <ReportsParticipantsPage /> },
  { path: '/sessions/:sid/reports', element: <div>Mendaftar rapor sesi</div> },
]

describe('reports participants page', () => {
  it('lists the session members; each row opens the reports edit page', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))))
    renderApp(null, { routes: ROUTES, route: '/sessions/1/reports/topics/3' })

    expect(await screen.findByText('Aisyah Putri')).toBeInTheDocument()
    screen.getByText('SD Cendekia')
    const user = userEvent.setup()
    // detailFixture has two members; each row carries the reports-edit action.
    await user.click(screen.getAllByRole('button', { name: 'Buka rapor' })[0])
    expect(await screen.findByText('Mendaftar rapor sesi')).toBeInTheDocument()
  })

  it('shows the empty state when the session has no members', async () => {
    const detail = detailFixture({ status: 'OPENED' })
    detail.groups = []
    server.use(http.get('/api/sessions/1', () => ok(detail)))
    renderApp(null, { routes: ROUTES, route: '/sessions/1/reports/topics/3' })

    expect(await screen.findByText('Belum ada peserta')).toBeInTheDocument()
  })
})
