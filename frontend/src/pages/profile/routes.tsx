/** Profile route: `/profile` — any authenticated role (no roles gate). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import ProfilePage from '../profile/ProfilePage'

export const profileRoutes: RouteObject[] = [
  {
    path: '/profile',
    element: (
      <RequireRole>
        <ProfilePage />
      </RequireRole>
    ),
  },
]
