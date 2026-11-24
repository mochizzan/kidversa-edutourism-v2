/** Activities route: `/activities` — mutate-heavy master, SA/ADMIN (02§3). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import ActivitiesPage from '../activities/ActivitiesPage'

export const activitiesRoutes: RouteObject[] = [
  {
    path: '/activities',
    element: (
      <RequireRole roles={['SA', 'ADMIN']}>
        <ActivitiesPage />
      </RequireRole>
    ),
  },
]
