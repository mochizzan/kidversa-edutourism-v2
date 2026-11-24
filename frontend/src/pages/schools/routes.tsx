/** Schools route: `/schools` — SA only (02§3). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import SchoolsPage from '../schools/SchoolsPage'

export const schoolsRoutes: RouteObject[] = [
  {
    path: '/schools',
    element: (
      <RequireRole roles={['SA']}>
        <SchoolsPage />
      </RequireRole>
    ),
  },
]
