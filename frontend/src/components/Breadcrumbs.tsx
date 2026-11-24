/**
 * The shell's top bar: a breadcrumb trail and nothing else.
 *
 * Wayfinding only — the page's own title, description and actions stay in the
 * PageHeader below it, so the strip never competes with the page. It borrows the
 * sheet's hairline language (no shadow, no fill) and sits at 11px/secondary so
 * it reads as context rather than content.
 *
 * The trail is derived from the URL, which is why it appears on every
 * authenticated page without any page having to opt in. Dynamic segments
 * (`:sid`) take their display name from `useCrumbName` when the page knows it
 * and fall back to "Detail sesi" while the data is still loading.
 */
import { useMemo } from 'react'
import type { ComponentType } from 'react'
import { Breadcrumb } from 'antd'
import type { BreadcrumbProps } from 'antd'
import type { TFunction } from 'i18next'
import { useTranslation } from 'react-i18next'
import { Link, useLocation } from 'react-router'
import { useCrumbNames, type CrumbNames } from '../lib/crumbs'

interface Crumb {
  key: string
  label: string
  /** Ancestors only — the crumb you are on is never a link. */
  to?: string
}

/** The five masters hang off the rail's Masters accordion, so they nest too. */
const MASTER_PAGES = new Set(['programs', 'topics', 'activities', 'missions', 'frames'])

function buildCrumbs(pathname: string, names: CrumbNames, t: TFunction<'menu'>): Crumb[] {
  const parts = pathname.split('/').filter(Boolean)
  const home: Crumb = { key: 'home', label: t('dashboard') }
  if (parts.length === 0) return [home]

  const [top, second, third] = parts
  const session: string = names.sid ?? t('breadcrumb.sessionDetail')

  if (MASTER_PAGES.has(top)) {
    // No `/masters` route exists, so the section label stays plain text.
    return [
      { key: 'masters', label: t('masters') },
      { key: top, label: t(top) },
    ]
  }

  switch (top) {
    case 'sessions': {
      const list: Crumb = { key: 'sessions', label: t('sessions') }
      if (!second) return [list]
      if (second === 'new') {
        return [{ ...list, to: '/sessions' }, { key: 'new', label: t('breadcrumb.newSession') }]
      }
      if (!third) return [{ ...list, to: '/sessions' }, { key: 'session', label: session }]
      return [
        { ...list, to: '/sessions' },
        { key: 'session', label: session, to: `/sessions/${second}` },
        third === 'gallery'
          ? { key: 'gallery', label: t('breadcrumb.gallery') }
          : { key: 'reports', label: t('reports') },
      ]
    }
    // The URL is flat, but the hierarchy is not: assessment always belongs to
    // one session, so the session crumb links back to its detail page.
    case 'assessment':
      if (!second) return [home]
      return [
        { key: 'sessions', label: t('sessions'), to: '/sessions' },
        { key: 'session', label: session, to: `/sessions/${second}` },
        { key: 'assessment', label: t('breadcrumb.assessment') },
      ]
    case 'consent':
      if (!second) return [{ key: 'consent', label: t('consent') }]
      return [
        { key: 'consent', label: t('consent'), to: '/consent' },
        { key: 'session', label: session },
      ]
    case 'reports':
      return [{ key: 'reports', label: t('reports') }]
    case 'children':
      return [{ key: 'children', label: t('children') }]
    case 'users':
      return [{ key: 'users', label: t('users') }]
    case 'schools':
      return [{ key: 'schools', label: t('schools') }]
    case 'profile':
      return [{ key: 'profile', label: t('profile') }]
    default:
      return [home]
  }
}

// antd spreads unknown props onto its <nav>, but leaves them out of the public
// type — the accessible name has to go through a cast.
const LabelledBreadcrumb = Breadcrumb as ComponentType<BreadcrumbProps & { 'aria-label': string }>

export default function Breadcrumbs() {
  const { t } = useTranslation('menu')
  const { pathname } = useLocation()
  const names = useCrumbNames()

  const items = useMemo(() => {
    const crumbs = buildCrumbs(pathname, names, t)
    return crumbs.map((crumb, index) => {
      const last = index === crumbs.length - 1
      return {
        key: crumb.key,
        title: crumb.to && !last ? <Link to={crumb.to}>{crumb.label}</Link> : crumb.label,
        'aria-current': last ? ('page' as const) : undefined,
      }
    })
  }, [pathname, names, t])

  return (
    <div className="kv-topbar">
      <LabelledBreadcrumb aria-label={t('breadcrumb.label')} items={items} />
    </div>
  )
}
