/** Users route: `/users` — SA/ADMIN inside the authenticated AppLayout shell. */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import UsersPage from '../users/UsersPage'

export const usersRoutes: RouteObject[] = [
  {
    path: '/users',
    element: (
      <RequireRole roles={['SA', 'ADMIN']}>
        <UsersPage />
      </RequireRole>
    ),
  },
]
