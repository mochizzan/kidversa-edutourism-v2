/** Public auth routes (F2): /login /register /forgot /reset — outside the
 * authenticated AppLayout shell; PublicOnly bounces a live session home. */
import type { RouteObject } from 'react-router'
import ForgotPage from './ForgotPage'
import LoginPage from './LoginPage'
import PublicOnly from './PublicOnly'
import RegisterPage from './RegisterPage'
import ResetPage from './ResetPage'

export const authRoutes: RouteObject[] = [
  { path: '/login', element: <PublicOnly><LoginPage /></PublicOnly> },
  { path: '/register', element: <PublicOnly><RegisterPage /></PublicOnly> },
  { path: '/forgot', element: <PublicOnly><ForgotPage /></PublicOnly> },
  { path: '/reset', element: <PublicOnly><ResetPage /></PublicOnly> },
]
