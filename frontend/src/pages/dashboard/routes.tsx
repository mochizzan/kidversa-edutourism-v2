/** Dashboard route: `/` — SA/ADMIN home inside the authenticated AppLayout shell. */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import DashboardPage from '../dashboard/DashboardPage'

export const dashboardRoutes: RouteObject[] = [
  {
    path: '/',
    element: (
      <RequireRole roles={['SA', 'ADMIN']}>
        <DashboardPage />
      </RequireRole>
    ),
  },
]
