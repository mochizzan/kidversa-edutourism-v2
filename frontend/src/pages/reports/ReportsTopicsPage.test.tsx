/**
 * `/sessions/:sid/reports/topics` (REQ9): topic picker — lists the session's
 * topics (`useSessionDetail` → `detail.topics`); each topic row opens the
 * participant page; a topic-less session shows the no-topics empty state.
 */
import { describe, expect, it } from 'vitest'
import { screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import ReportsTopicsPage from './ReportsTopicsPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok } from '../../test/handlers'
import { detailFixture } from '../../test/helpers.cov-session'
import type { RouteObject } from 'react-router'

const ROUTES: RouteObject[] = [
  { path: '/sessions/:sid/reports/topics', element: <ReportsTopicsPage /> },
  { path: '/sessions/:sid/reports/topics/:stid', element: <div>Peserta dipilih</div> },
  { path: '/reports', element: <div>Daftar rapor</div> },
]

describe('reports topics page', () => {
  it('lists the session topics; each row opens the participant page', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))))
    renderApp(null, { routes: ROUTES, route: '/sessions/1/reports/topics' })

    expect(await screen.findByText('Batik Nusantara')).toBeInTheDocument()
    // The session context is plain text now, not a bare title link.
    screen.getByText('Kelas Batik')
    expect(screen.queryByRole('link', { name: 'Kelas Batik' })).toBeNull()
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Buka topik' }))
    expect(await screen.findByText('Peserta dipilih')).toBeInTheDocument()
  })

  it('the back action returns to the session picker', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))))
    const user = userEvent.setup()
    renderApp(null, { routes: ROUTES, route: '/sessions/1/reports/topics' })

    await screen.findByText('Batik Nusantara')
    await user.click(screen.getByRole('button', { name: /Kembali/ }))
    expect(await screen.findByText('Daftar rapor')).toBeInTheDocument()
  })

  it('shows the no-topics empty state when the session has none', async () => {
    const detail = detailFixture({ status: 'OPENED' })
    detail.topics = []
    server.use(http.get('/api/sessions/1', () => ok(detail)))
    renderApp(null, { routes: ROUTES, route: '/sessions/1/reports/topics' })

    expect(await screen.findByText('Belum ada topik di sesi ini')).toBeInTheDocument()
  })
})
