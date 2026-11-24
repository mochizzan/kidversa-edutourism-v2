/**
 * Session routes: list, create, detail — the routes owned by the sessions
 * group. Gallery (`/sessions/:sid/gallery`) + reports (`/sessions/:sid/reports`
 * and `/reports`) moved to their own `../gallery|reports/routes.tsx`; assessment
 * and consent likewise own `../assessment|consent/routes.tsx` — every page
 * group owns its routes.tsx. `RequireRole` is the canonical guard.
 *
 * Guards: create/manage = SA|ADMIN (create page); list/detail = all roles —
 * the server scopes FAS to own groups and reduced foreign groups (02§2b),
 * the UI hides what FAS must not see (no lifecycle/group/consent-send
 * controls).
 */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import SessionCreatePage from './SessionCreatePage'
import SessionDetailPage from './SessionDetailPage'
import SessionsPage from './SessionsPage'

export const sessionRoutes: RouteObject[] = [
  { path: '/sessions', element: <RequireRole><SessionsPage /></RequireRole> },
  {
    path: '/sessions/new',
    element: (
      <RequireRole roles={['SA', 'ADMIN']}>
        <SessionCreatePage />
      </RequireRole>
    ),
  },
  { path: '/sessions/:sid', element: <RequireRole><SessionDetailPage /></RequireRole> },
]
