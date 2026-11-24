/**
 * SA school switch is a data-scope change, not just a header change: the page
 * under the shell must immediately refetch for the newly selected school.
 *
 * Regression for the reported bug: a superadmin switched Bandung → Jakarta in
 * the rail and the open Programs page kept showing Bandung's rows until a
 * route change or browser reload remounted it.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import AppLayout from './AppLayout'
import ProgramsPage from '../pages/programs/ProgramsPage'
import { renderApp } from '../test/render'
import {
  makeLoginData,
  makeSchool,
  makeUser,
  ok,
  paged,
} from '../test/handlers'
import { programRow, findOption } from '../test/helpers.cov-master'
import { server } from '../test/setup'

vi.mock('./StreamGate', () => ({ default: () => null }))

const BANDUNG = makeSchool({ id: 1, school_name: 'Bandung' })
const JAKARTA = makeSchool({ id: 2, school_name: 'Jakarta' })

function renderShell() {
  return renderApp(null, {
    route: '/programs',
    session: makeLoginData({
      user: makeUser({ role: 'SA', school_id: BANDUNG.id }),
      school: BANDUNG,
    }),
    routes: [
      {
        element: <AppLayout />,
        children: [{ path: '/programs', element: <ProgramsPage /> }],
      },
    ],
  })
}

beforeEach(() => {
  server.use(
    http.get('/api/schools', () =>
      paged([BANDUNG, JAKARTA], { page: 1, per_page: 1000, total: 2 }),
    ),
    http.get('/api/users/pending-count', () => ok({ pending: 0 })),
  )
})

describe('SA school switch', () => {
  it('refetches the open page for the newly selected school without navigating', async () => {
    const user = userEvent.setup()
    const schoolHeaders: Array<string | null> = []
    const switchBodies: number[] = []

    server.use(
      http.post('/api/auth/switch-school', async ({ request }) => {
        const body = (await request.json()) as { school_id: number }
        switchBodies.push(body.school_id)
        return ok({ school: body.school_id === JAKARTA.id ? JAKARTA : BANDUNG })
      }),
      http.get('/api/programs', ({ request }) => {
        const schoolId = request.headers.get('X-School-ID')
        schoolHeaders.push(schoolId)
        const rows =
          schoolId === String(JAKARTA.id)
            ? [programRow(20, 'Program Jakarta')]
            : [programRow(10, 'Program Bandung')]
        return paged(rows, { page: 1, per_page: 20, total: rows.length })
      }),
    )

    renderShell()

    // First load is scoped to the restored session school.
    expect(await screen.findByText('Program Bandung')).toBeInTheDocument()
    expect(schoolHeaders).toContain(String(BANDUNG.id))

    // Switch through the REAL rail control (antd Select), staying on /programs.
    await user.click(screen.getByRole('combobox', { name: 'Ganti sekolah' }))
    await user.click(await findOption('Jakarta'))
    await waitFor(() => expect(switchBodies).toEqual([JAKARTA.id]))

    // The same mounted page must now show the new school's data, with no
    // route change or reload to force it.
    await waitFor(() => expect(schoolHeaders).toContain(String(JAKARTA.id)))
    expect(await screen.findByText('Program Jakarta')).toBeInTheDocument()
    expect(screen.queryByText('Program Bandung')).toBeNull()
  })
})
