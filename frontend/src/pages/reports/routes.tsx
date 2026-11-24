/** Report routes (REQ9): `/sessions/:sid/reports` (edit) →
 * `/sessions/:sid/reports/topics` (topic picker) →
 * `/sessions/:sid/reports/topics/:stid` (participants) — all roles
 * (server scopes FAS to own/reduced groups, 02§2b). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import ReportsIndexPage from '../reports/ReportsIndexPage'
import ReportsPage from '../reports/ReportsPage'
import ReportsParticipantsPage from '../reports/ReportsParticipantsPage'
import ReportsTopicsPage from '../reports/ReportsTopicsPage'

export const reportsRoutes: RouteObject[] = [
  { path: '/sessions/:sid/reports', element: <RequireRole><ReportsPage /></RequireRole> },
  { path: '/sessions/:sid/reports/topics', element: <RequireRole><ReportsTopicsPage /></RequireRole> },
  {
    path: '/sessions/:sid/reports/topics/:stid',
    element: <RequireRole><ReportsParticipantsPage /></RequireRole>,
  },
  { path: '/reports', element: <RequireRole><ReportsIndexPage /></RequireRole> },
]
