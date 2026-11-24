/** Children route: `/children` — every authenticated role (FAS keeps a
 * server-scoped READ-ONLY list; mutate buttons hidden client-side, 02§3). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import ChildrenPage from '../children/ChildrenPage'

export const childrenRoutes: RouteObject[] = [
  {
    path: '/children',
    element: (
      <RequireRole>
        <ChildrenPage />
      </RequireRole>
    ),
  },
]
