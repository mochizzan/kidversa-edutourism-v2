/** Programs route: `/programs` — mutate-heavy master, SA/ADMIN (02§3). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import ProgramsPage from '../programs/ProgramsPage'

export const programsRoutes: RouteObject[] = [
  {
    path: '/programs',
    element: (
      <RequireRole roles={['SA', 'ADMIN']}>
        <ProgramsPage />
      </RequireRole>
    ),
  },
]
