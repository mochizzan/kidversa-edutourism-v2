/**
 * Authenticated app shell (spec 02§3): a left rail + white content sheet.
 *
 * Rail: brand at top, role-gated navigation grouped into labelled categories
 * (Utama / Akademik / Data / Sistem), each accordion section hanging its
 * children off a curved tree line, current page = white pill +
 * semibold + aria-current, then the utility cluster pinned to the bottom
 * (SA school switcher, avatar dropdown — THE single profile entry — and the
 * settings gear that opens the Settings MODAL). On ≤1024px the rail becomes
 * a left Drawer opened from a compact top bar.
 *
 * Menu matrix is binding: SA / ADMIN / FASILITATOR each see exactly their
 * items and FAS has NO dashboard. The Users item carries the pending-
 * approvals badge. StreamGate — the tab's single SSE connection — mounts
 * here so it lives for as long as the session does.
 *
 * An impersonated session (admin "Login as", spec 09§9.2/§9.6) gets a
 * persistent strip at the top of the content sheet naming the target and the
 * actor, with the Exit button that ends the session. It renders only while
 * `impersonation` is non-null: the shell never claims a switch that is not
 * happening, and never hides one that is.
 *
 * Icons come from @ant-design/icons; no hand-authored SVG.
 */
import { Fragment, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'
import {
  Avatar,
  Button,
  Drawer,
  Dropdown,
  Layout,
  Select,
  Tooltip,
  Typography,
} from 'antd'
import type { MenuProps } from 'antd'
import {
  ApartmentOutlined,
  AppstoreOutlined,
  BankOutlined,
  CalendarOutlined,
  DownOutlined,
  LogoutOutlined,
  MenuFoldOutlined,
  MenuOutlined,
  MenuUnfoldOutlined,
  SettingOutlined,
  TeamOutlined,
  UserOutlined,
  UserSwitchOutlined,
} from '@ant-design/icons'
import { Link, Outlet, useLocation, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api } from '../lib/api'
import { avatarUrl } from '../lib/avatar'
import { useErrorToast } from '../lib/errors'
import { useAuth, type Role, type School } from '../lib/auth'
import { CrumbNamesProvider } from '../lib/crumbs'
import { registerSignal } from '../lib/stream'
import Breadcrumbs from './Breadcrumbs'
import SettingsModal from './SettingsModal'
import StreamGate from './StreamGate'

const { Content } = Layout

/** The rail's four categories, in reading order. Empty ones are not rendered. */
type SectionKey = 'overview' | 'academic' | 'data' | 'system'

const SECTION_ORDER: SectionKey[] = ['overview', 'academic', 'data', 'system']

interface NavEntry {
  /** Stable identity: a route for a plain link, a slug for a group. */
  key: string
  labelKey: string
  section: SectionKey
  children?: { key: string; labelKey: string }[]
}

const MASTERS: NavEntry = {
  key: 'masters',
  labelKey: 'masters',
  section: 'system',
  children: [
    { key: '/programs', labelKey: 'programs' },
    { key: '/topics', labelKey: 'topics' },
    { key: '/activities', labelKey: 'activities' },
    { key: '/missions', labelKey: 'missions' },
    { key: '/frames', labelKey: 'frames' },
  ],
}

/** Sessions own their downstream artefacts: reports and consent hang off it. */
const SESSIONS: NavEntry = {
  key: 'sessions',
  labelKey: 'sessions',
  section: 'academic',
  children: [
    { key: '/sessions', labelKey: 'allSessions' },
    { key: '/reports', labelKey: 'reports' },
    { key: '/consent', labelKey: 'consent' },
  ],
}

/** Menu matrix (binding, spec 02§3). Profile lives ONLY in the avatar menu. */
const NAV_BY_ROLE: Record<Role, NavEntry[]> = {
  SA: [
    { key: '/', labelKey: 'dashboard', section: 'overview' },
    SESSIONS,
    { key: '/users', labelKey: 'users', section: 'data' },
    { key: '/children', labelKey: 'children', section: 'data' },
    { key: '/schools', labelKey: 'schools', section: 'data' },
    MASTERS,
  ],
  ADMIN: [
    { key: '/', labelKey: 'dashboard', section: 'overview' },
    SESSIONS,
    { key: '/users', labelKey: 'users', section: 'data' },
    { key: '/children', labelKey: 'children', section: 'data' },
    MASTERS,
  ],
  FASILITATOR: [
    SESSIONS,
    { key: '/children', labelKey: 'children', section: 'data' },
  ],
}

const NAV_ICONS: Record<string, ReactNode> = {
  '/': <AppstoreOutlined />,
  '/schools': <BankOutlined />,
  '/users': <UserSwitchOutlined />,
  sessions: <CalendarOutlined />,
  masters: <ApartmentOutlined />,
  '/children': <TeamOutlined />,
}

interface RailNavProps {
  entries: NavEntry[]
  activeKey: string | null
  openGroups: string[]
  pending: number
  collapsed: boolean
  /** '' for the desktop rail, 'drawer-' in the drawer: ids must be unique. */
  idPrefix?: string
  onToggleGroup: (key: string) => void
  onNavigate: () => void
}

/** The rail's navigation list — shared by the desktop rail and mobile drawer. */
function RailNav({
  entries,
  activeKey,
  openGroups,
  pending,
  collapsed,
  idPrefix = '',
  onToggleGroup,
  onNavigate,
}: RailNavProps) {
  const { t } = useTranslation()

  // Id namespace: 'kv-' on the desktop rail, 'kv-drawer-' in the drawer, so the
  // two instances of the same nav never emit duplicate ids.
  const ns = `kv-${idPrefix}`

  // Group the role's entries into the rail's categories; a category with no
  // destination for this role is not rendered at all (FASILITATOR has no
  // overview, so the rail opens on AKADEMIK).
  const sections = SECTION_ORDER.map((section) => ({
    section,
    entries: entries.filter((entry) => entry.section === section),
  })).filter((group) => group.entries.length > 0)

  const renderEntry = (entry: NavEntry) => {
    const children = entry.children
    if (children) {
      const open = openGroups.includes(entry.key)
      const sectionActive = children.some((child) => child.key === activeKey)
      const label = t(`menu:${entry.labelKey}`)
      const submenuId = `${ns}sub-${entry.key}`
      const shown = open && !collapsed
      const content = (
        <>
          <span className="kv-rail-icon" aria-hidden="true">
            {NAV_ICONS[entry.key]}
          </span>
          <span className="kv-rail-label">{label}</span>
          <DownOutlined className="kv-rail-chevron" aria-hidden="true" />
        </>
      )
      return (
        <li
          key={entry.key}
          className={`kv-rail-item kv-rail-item--group${open ? ' is-open' : ''}${
            sectionActive ? ' is-active' : ''
          }`}
        >
          {/* Folded, the submenu is not on screen, so the group stops being a
              toggle that hides a list nobody can see and becomes a link to the
              group's landing page (its first destination). */}
          {collapsed ? (
            <Link
              to={children[0].key}
              className="kv-rail-link"
              aria-label={label}
              title={label}
              onClick={onNavigate}
            >
              {content}
            </Link>
          ) : (
            <button
              type="button"
              className="kv-rail-link"
              aria-expanded={open}
              aria-controls={submenuId}
              onClick={() => onToggleGroup(entry.key)}
            >
              {content}
            </button>
          )}
          <div
            className="kv-rail-subwrap"
            id={submenuId}
            inert={!shown}
            aria-hidden={!shown}
          >
            <ul className="kv-rail-sub" aria-label={label}>
              {children.map((child) => {
                const current = child.key === activeKey
                return (
                  <li
                    key={child.key}
                    className={`kv-rail-subitem${current ? ' is-current' : ''}`}
                  >
                    <Link
                      to={child.key}
                      className="kv-rail-sublink"
                      aria-current={current ? 'page' : undefined}
                      onClick={onNavigate}
                    >
                      {t(`menu:${child.labelKey}`)}
                    </Link>
                  </li>
                )
              })}
            </ul>
          </div>
        </li>
      )
    }

    const current = entry.key === activeKey
    return (
      <li
        key={entry.key}
        className={`kv-rail-item${current ? ' is-current' : ''}`}
      >
        <Link
          to={entry.key}
          className="kv-rail-link"
          aria-current={current ? 'page' : undefined}
          title={collapsed ? t(`menu:${entry.labelKey}`) : undefined}
          onClick={onNavigate}
        >
          <span className="kv-rail-icon" aria-hidden="true">
            {NAV_ICONS[entry.key]}
          </span>
          <span className="kv-rail-label">{t(`menu:${entry.labelKey}`)}</span>
          {entry.key === '/users' && pending > 0 ? (
            <span className="kv-rail-badge" title={t('menu:pendingApprovals', { count: pending })}>
              {pending}
              <span className="kv-sr-only">
                {t('menu:pendingApprovals', { count: pending })}
              </span>
            </span>
          ) : null}
        </Link>
      </li>
    )
  }

  return (
    <ul className="kv-rail-list">
      {sections.map(({ section, entries: sectionEntries }) => (
        <Fragment key={section}>
          <li className="kv-rail-section-header">
            <h2
              className="kv-rail-section-label"
              id={`${ns}rail-section-${section}`}
            >
              {t(`menu:section.${section}`)}
            </h2>
          </li>
          {sectionEntries.map(renderEntry)}
        </Fragment>
      ))}
    </ul>
  )
}

function Brand({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <Link to="/" className="kv-rail-brand" aria-label="Kidversa" onClick={onNavigate}>
      <Avatar
        className="kv-rail-brand__logo"
        size={34}
        shape="square"
        src="/logo.png"
        alt=""
        aria-hidden="true"
      />
      <span className="kv-rail-brand__name">Kidversa</span>
    </Link>
  )
}

export default function AppLayout() {
  const { t } = useTranslation()
  const { user, school, switchSchool, logout, impersonation } = useAuth()
  const location = useLocation()
  const navigate = useNavigate()
  const toast = useErrorToast()
  const [drawerOpen, setDrawerOpen] = useState(false)
  const [settingsOpen, setSettingsOpen] = useState(false)
  // Desktop rail collapse preference survives reloads; the drawer is unaffected.
  const [railCollapsed, setRailCollapsed] = useState(
    () => localStorage.getItem('kv-rail-collapsed') === '1',
  )
  const [schools, setSchools] = useState<School[]>([])
  const [pending, setPending] = useState(0)
  // Manual accordion overrides; sections open themselves by default when one
  // of their children is the current page.
  const [manualOpen, setManualOpen] = useState<Record<string, boolean>>({})

  // SA switcher candidates (active schools; server validates on switch anyway).
  useEffect(() => {
    if (user?.role !== 'SA') return
    let cancelled = false
    api.list<School>('/api/schools', { per_page: 1000 }).then(
      (result) => {
        if (!cancelled) setSchools(result.data.filter((item) => item.is_active))
      },
      (error: unknown) => {
        if (!cancelled) toast(error)
      },
    )
    return () => {
      cancelled = true
    }
  }, [user?.role, toast])

  // Pending-approvals badge on the Users nav item (full-value exception #2).
  useEffect(() => {
    if (!user || user.role === 'FASILITATOR') return
    let cancelled = false
    api.get<{ pending: number }>('/api/users/pending-count').then(
      (data) => {
        if (!cancelled) setPending(data.pending)
      },
      () => {
        // Best-effort decoration; the users page surfaces real API errors.
      },
    )
    const unregister = registerSignal<{ pending: number }>('pending.changed', (data) =>
      setPending(data.pending),
    )
    return () => {
      cancelled = true
      unregister()
    }
  }, [user])

  useEffect(() => {
    localStorage.setItem('kv-rail-collapsed', railCollapsed ? '1' : '0')
  }, [railCollapsed])

  const activeKey = useMemo(() => {
    const path = location.pathname
    const keys = NAV_BY_ROLE[user?.role ?? 'FASILITATOR'].flatMap((entry) =>
      entry.children ? entry.children.map((child) => child.key) : [entry.key],
    )
    if (path === '/') return '/'
    let match = ''
    for (const key of keys) {
      if (key !== '/' && (path === key || path.startsWith(`${key}/`)) && key.length > match.length) {
        match = key
      }
    }
    return match || null
  }, [location.pathname, user?.role])

  // A section opens itself when one of its children is the current page,
  // unless the user has explicitly collapsed or opened it.
  const activeParent = useMemo(
    () =>
      NAV_BY_ROLE[user?.role ?? 'FASILITATOR'].find((entry) =>
        entry.children?.some((child) => child.key === activeKey),
      )?.key ?? null,
    [activeKey, user?.role],
  )

  const openGroups = useMemo(() => {
    const keys = new Set(
      Object.entries(manualOpen)
        .filter(([, open]) => open)
        .map(([key]) => key),
    )
    if (activeParent && manualOpen[activeParent] !== false) keys.add(activeParent)
    return [...keys]
  }, [manualOpen, activeParent])

  const toggleGroup = (key: string) => {
    // A collapsed rail has no room for the submenu: expand it and open the
    // section in one action.
    if (railCollapsed) {
      setRailCollapsed(false)
      setManualOpen((current) => ({ ...current, [key]: true }))
      return
    }
    const currentlyOpen = manualOpen[key] ?? key === activeParent
    setManualOpen((current) => ({ ...current, [key]: !currentlyOpen }))
  }

  const schoolOptions = useMemo(() => {
    const options = schools.map((item) => ({ value: item.id, label: item.school_name }))
    if (school && !schools.some((item) => item.id === school.id)) {
      options.unshift({ value: school.id, label: school.school_name }) // keep a deactivated selection visible
    }
    return options
  }, [schools, school])

  if (!user) return null // RequireRole makes this unreachable; guards hooks above

  const userMenuItems: MenuProps['items'] = [
    {
      key: 'identity',
      label: (
        <div className="kv-user-menu__id">
          <Typography.Text strong className="kv-user-menu__name">
            {user.name}
          </Typography.Text>
          <Typography.Text type="secondary" className="kv-user-menu__role">
            {t(`common:role.${user.role}`)}
          </Typography.Text>
        </div>
      ),
    },
    { type: 'divider' },
    { key: 'profile', icon: <UserOutlined />, label: t('menu:profile') },
    { type: 'divider' },
    { key: 'logout', icon: <LogoutOutlined />, label: t('menu:logout'), danger: true },
  ]

  const schoolSwitch =
    user.role === 'SA' ? (
      <Select
        className="kv-rail-school"
        aria-label={t('menu:schoolSwitcher')}
        value={school?.id}
        options={schoolOptions}
        optionFilterProp="label"
        onChange={(schoolId) => {
          switchSchool(schoolId).catch(toast)
        }}
      />
    ) : null

  const onNavigate = () => setDrawerOpen(false)

  const railFoot = (
    <div className="kv-rail__foot">
      {schoolSwitch ? <div className="kv-rail__school">{schoolSwitch}</div> : null}
      <div className="kv-rail-user">
        <Dropdown
          trigger={['click']}
          menu={{
            items: userMenuItems,
            onClick: ({ key }) => {
              if (key === 'profile') navigate('/profile')
              else if (key === 'logout') void logout()
            },
          }}
        >
          <button
            type="button"
            className={`kv-rail-user__btn${location.pathname === '/profile' ? ' is-active' : ''}`}
            aria-label={user.name}
          >
            <Avatar size={30} src={user.photo_url ?? avatarUrl(user.name)}>
              {user.name.trim().charAt(0).toUpperCase() || '?'}
            </Avatar>
            <span className="kv-rail-user__text">
              <span className="kv-rail-user__name">{user.name}</span>
              <span className="kv-rail-user__role">{t(`common:role.${user.role}`)}</span>
            </span>
            <DownOutlined />
          </button>
        </Dropdown>
        <Tooltip title={t('menu:settings')}>
          <Button
            className="kv-rail-icon-btn"
            type="text"
            icon={<SettingOutlined />}
            aria-label={t('menu:settings')}
            onClick={() => setSettingsOpen(true)}
          />
        </Tooltip>
      </div>
    </div>
  )

  // The rail and the drawer render the same navigation, but ids must stay
  // unique: the drawer gets a 'drawer-' prefix on every generated id.
  const renderNav = (idPrefix = '', collapsed = railCollapsed) => (
    <RailNav
      entries={NAV_BY_ROLE[user.role]}
      activeKey={activeKey}
      openGroups={openGroups}
      pending={pending}
      collapsed={collapsed}
      idPrefix={idPrefix}
      onToggleGroup={toggleGroup}
      onNavigate={onNavigate}
    />
  )

  return (
    <Layout className="kv-shell">
      <aside
        className={`kv-rail${railCollapsed ? ' is-collapsed' : ''}`}
        aria-label={t('menu:navLabel')}
      >
        <div className="kv-rail-head">
          <Brand />
          <Tooltip title={railCollapsed ? t('menu:expandNav') : t('menu:collapseNav')}>
            <Button
              className="kv-rail-toggle"
              type="text"
              icon={railCollapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
              aria-label={railCollapsed ? t('menu:expandNav') : t('menu:collapseNav')}
              aria-expanded={!railCollapsed}
              aria-controls="kv-rail-nav"
              onClick={() => setRailCollapsed((value) => !value)}
            />
          </Tooltip>
        </div>
        <nav className="kv-rail-nav" id="kv-rail-nav" aria-label={t('menu:navLabel')}>
          {renderNav()}
        </nav>
        {railFoot}
      </aside>

      <div className="kv-main">
        <header className="kv-mobilebar">
          <Brand />
          <Tooltip title={t('menu:openNav')}>
            <Button
              className="kv-burger"
              type="text"
              icon={<MenuOutlined />}
              aria-label={t('menu:openNav')}
              onClick={() => setDrawerOpen(true)}
            />
          </Tooltip>
        </header>
        <Content className="kv-content">
          <StreamGate />
          {/* Persistent impersonation strip: `role="status"` + polite live
              region so the identity switch is announced, and Exit ends the
              target session (the admin signs in again to return). */}
          {impersonation ? (
            <div className="kv-impersonation" role="status" aria-live="polite">
              <span className="kv-impersonation__text">
                {t('auth:users.impersonationBanner', {
                  name: user.name,
                  role: t(`common:role.${user.role}`),
                  actor: impersonation.actor_name,
                })}
              </span>
              <Button
                className="kv-impersonation__exit"
                size="small"
                icon={<LogoutOutlined aria-hidden="true" />}
                onClick={() => void logout()}
              >
                {t('auth:users.exitImpersonation')}
              </Button>
            </div>
          ) : null}
          {/* The bar reads the trail, the page below names its dynamic
              segments — both need this one registry. */}
          <CrumbNamesProvider>
            <Breadcrumbs />
            <Outlet />
          </CrumbNamesProvider>
        </Content>
      </div>

      <Drawer
        open={drawerOpen}
        placement="left"
        width={304}
        rootClassName="kv-drawer"
        title={<Brand onNavigate={onNavigate} />}
        onClose={onNavigate}
      >
        <nav
          className="kv-rail-nav"
          id="kv-drawer-nav"
          aria-label={t('menu:navLabel')}
        >
          {renderNav('drawer-', false)}
        </nav>
        {railFoot}
      </Drawer>
      <SettingsModal open={settingsOpen} onClose={() => setSettingsOpen(false)} />
    </Layout>
  )
}
