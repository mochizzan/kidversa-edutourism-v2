/**
 * Router root (spec 02§3): AuthProvider → I18nextProvider → ConfigProvider →
 * BrowserRouter. `RequireRole` is THE canonical guard (reads auth state, roles
 * gate per route); `Protected` only renders its decision.
 *
 * Route ownership: every page group under src/pages/<group>/ owns its own
 * routes.tsx (17 groups) — this file only spreads the group arrays into the
 * public and authenticated slots below.
 */
import type { ReactNode } from 'react'
import { App as AntApp, Button, ConfigProvider, Result, type ThemeConfig } from 'antd'
import antdEnUS from 'antd/locale/en_US'
import antdIdID from 'antd/locale/id_ID'
import { I18nextProvider, useTranslation } from 'react-i18next'
import { BrowserRouter, useNavigate, useRoutes, type RouteObject } from 'react-router'
import AppLayout from './components/AppLayout'
import Protected from './components/Protected'
import { AuthProvider, requireAuth, useAuth, type Role } from './lib/auth'
import { setDayjsLocale } from './lib/dayjs'
import i18n from './lib/i18n'
import { activitiesRoutes } from './pages/activities/routes'
import { assessmentRoutes } from './pages/assessment/routes'
import { authRoutes } from './pages/auth/routes'
import { childrenRoutes } from './pages/children/routes'
import { consentRoutes } from './pages/consent/routes'
import { dashboardRoutes } from './pages/dashboard/routes'
import { framesRoutes } from './pages/frames/routes'
import { galleryRoutes } from './pages/gallery/routes'
import { missionsRoutes } from './pages/missions/routes'
import { profileRoutes } from './pages/profile/routes'
import { programsRoutes } from './pages/programs/routes'
import { reportsRoutes } from './pages/reports/routes'
import { sessionRoutes } from './pages/sessions/routes'
import { schoolsRoutes } from './pages/schools/routes'
import { topicsRoutes } from './pages/topics/routes'
import { usersRoutes } from './pages/users/routes'
import { viewRoutes } from './pages/view/routes'

/** Palette + shape from the binding DESIGN→project mapping (task §Design mapping). */
const APP_THEME: ThemeConfig = {
  token: {
    colorPrimary: '#E22F80', // olive-deep: brand / primary actions
    colorInfo: '#8140DC', // violet: contextual emphasis
    colorSuccess: '#FFCA95', // olive: soft positive fills
    colorWarning: '#FF7873', // coral: attention
    colorError: '#FF7873', // coral
    colorText: '#20201E',
    colorTextSecondary: '#777873',
    colorBorder: '#E8E8E4',
    colorBgContainer: '#FFFFFF',
    colorBgLayout: '#FCFCFB',
    borderRadius: 8,
    borderRadiusLG: 10,
    controlHeight: 32,
    fontSize: 12,
    fontFamily:
      "system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', Arial, sans-serif",
    motionDurationMid: '0.15s', // 120–180ms hover language
  },
}

export interface RequireRoleProps {
  /** Omit for "any authenticated user" (the AppLayout shell). */
  roles?: readonly Role[]
  children: ReactNode
}

/** Canonical guard: anonymous → /login; role mismatch → role home (02§3). */
export function RequireRole({ roles, children }: RequireRoleProps) {
  const { user, ready } = useAuth()
  return (
    <Protected decision={requireAuth(user, roles)} ready={ready}>
      {children}
    </Protected>
  )
}

function NotFound() {
  const { t } = useTranslation('common')
  const navigate = useNavigate()
  return (
    <Result
      status="404"
      title={t('pageNotFound')}
      extra={
        <Button type="primary" onClick={() => navigate('/')}>
          {t('actions.home')}
        </Button>
      }
    />
  )
}

/** Object route table: public groups first, then the authenticated layout shell. */
const ROUTE_GROUPS: RouteObject[] = [
  // Public groups — outside the authenticated AppLayout shell.
  ...authRoutes,
  ...viewRoutes,
  // Authenticated shell: RequireRole → AppLayout (+StreamGate) → Outlet.
  // Each group owns its pages/<group>/routes.tsx and role-gates its own routes
  // with RequireRole (render-time use only).
  {
    element: <RequireRole><AppLayout /></RequireRole>,
    children: [
      ...dashboardRoutes,
      ...schoolsRoutes,
      ...usersRoutes,
      ...profileRoutes,
      ...programsRoutes,
      ...topicsRoutes,
      ...activitiesRoutes,
      ...missionsRoutes,
      ...framesRoutes,
      ...childrenRoutes,
      ...sessionRoutes,
      ...galleryRoutes,
      ...reportsRoutes,
      ...assessmentRoutes,
      ...consentRoutes,
    ],
  },
  { path: '*', element: <NotFound /> },
]

function AppRoutes() {
  return useRoutes(ROUTE_GROUPS)
}

export default function App() {
  // Subscribes to languageChanged so ConfigProvider's widget locale follows ID/EN.
  const { i18n: current } = useTranslation()
  // dayjs's global locale drives the calendar panel text (weekday/month
  // names) — keep it in step with the same language ConfigProvider uses.
  setDayjsLocale(current.language)
  return (
    <AuthProvider>
      <I18nextProvider i18n={i18n}>
        <ConfigProvider
          theme={APP_THEME}
          locale={current.language.startsWith('en') ? antdEnUS : antdIdID}
        >
          <AntApp>
            <BrowserRouter>
              <AppRoutes />
            </BrowserRouter>
          </AntApp>
        </ConfigProvider>
      </I18nextProvider>
    </AuthProvider>
  )
}
