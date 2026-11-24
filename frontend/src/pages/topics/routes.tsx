/** Topics route: `/topics` — mutate-heavy master, SA/ADMIN (02§3). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import TopicsPage from '../topics/TopicsPage'

export const topicsRoutes: RouteObject[] = [
  {
    path: '/topics',
    element: (
      <RequireRole roles={['SA', 'ADMIN']}>
        <TopicsPage />
      </RequireRole>
    ),
  },
]
