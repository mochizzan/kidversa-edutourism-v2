/** Consent routes: `/consent` (index) + `/consent/:sid` (list) — all roles
 * (server scopes FAS to own/reduced groups, 02§2b). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import ConsentIndexPage from '../consent/ConsentIndexPage'
import ConsentListPage from '../consent/ConsentListPage'

export const consentRoutes: RouteObject[] = [
  { path: '/consent', element: <RequireRole><ConsentIndexPage /></RequireRole> },
  { path: '/consent/:sid', element: <RequireRole><ConsentListPage /></RequireRole> },
]
