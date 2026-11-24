/** Frames route: `/frames` — mutate-heavy master, SA/ADMIN (02§3). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import FramesPage from '../frames/FramesPage'

export const framesRoutes: RouteObject[] = [
  {
    path: '/frames',
    element: (
      <RequireRole roles={['SA', 'ADMIN']}>
        <FramesPage />
      </RequireRole>
    ),
  },
]
