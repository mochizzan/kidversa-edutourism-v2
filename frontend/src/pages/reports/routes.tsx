/** Report routes: `/sessions/:sid/reports` (detail) + `/reports` (index) —
 * all roles (server scopes FAS to own/reduced groups, 02§2b). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import ReportsIndexPage from '../reports/ReportsIndexPage'
import ReportsPage from '../reports/ReportsPage'

export const reportsRoutes: RouteObject[] = [
  { path: '/sessions/:sid/reports', element: <RequireRole><ReportsPage /></RequireRole> },
  { path: '/reports', element: <RequireRole><ReportsIndexPage /></RequireRole> },
]
