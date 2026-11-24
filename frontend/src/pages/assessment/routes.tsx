/** Assessment route: `/assessment/:sid/:cid/:stid` — all roles (server scopes
 * FAS to own/reduced groups, 02§2b). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import AssessmentPage from '../assessment/AssessmentPage'

export const assessmentRoutes: RouteObject[] = [
  {
    path: '/assessment/:sid/:cid/:stid',
    element: <RequireRole><AssessmentPage /></RequireRole>,
  },
]
