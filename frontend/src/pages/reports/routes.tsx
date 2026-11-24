/** Report routes (REQ9): `/sessions/:sid/reports` redirects to the topic
 * picker; `/topics` → `/topics/:stid` (participant table + batch submit) →
 * `/topics/:stid/children/:cid` (focused single-child report edit) — all roles
 * (server scopes FAS to own/reduced groups, 02§2b). */
import type { RouteObject } from 'react-router'
import { Navigate, useParams } from 'react-router'
import { RequireRole } from '../../router'
import ReportsIndexPage from '../reports/ReportsIndexPage'
import ReportsPage from '../reports/ReportsPage'
import ReportsParticipantsPage from '../reports/ReportsParticipantsPage'
import ReportsTopicsPage from '../reports/ReportsTopicsPage'

/** Old entry point: reports are entered through the topic picker now. */
function ReportsRedirect() {
  const { sid } = useParams()
  return <Navigate to={`/sessions/${sid}/reports/topics`} replace />
}

export const reportsRoutes: RouteObject[] = [
  {
    path: '/sessions/:sid/reports',
    element: (
      <RequireRole>
        <ReportsRedirect />
      </RequireRole>
    ),
  },
  {
    path: '/sessions/:sid/reports/topics',
    element: (
      <RequireRole>
        <ReportsTopicsPage />
      </RequireRole>
    ),
  },
  {
    path: '/sessions/:sid/reports/topics/:stid',
    element: (
      <RequireRole>
        <ReportsParticipantsPage />
      </RequireRole>
    ),
  },
  {
    path: '/sessions/:sid/reports/topics/:stid/children/:cid',
    element: (
      <RequireRole>
        <ReportsPage />
      </RequireRole>
    ),
  },
  {
    path: '/reports',
    element: (
      <RequireRole>
        <ReportsIndexPage />
      </RequireRole>
    ),
  },
]
