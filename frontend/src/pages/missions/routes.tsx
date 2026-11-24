/** Missions route: `/missions` — mutate-heavy master, SA/ADMIN (02§3). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import MissionsPage from '../missions/MissionsPage'

export const missionsRoutes: RouteObject[] = [
  {
    path: '/missions',
    element: (
      <RequireRole roles={['SA', 'ADMIN']}>
        <MissionsPage />
      </RequireRole>
    ),
  },
]
