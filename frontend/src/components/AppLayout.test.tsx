/**
 * AppLayout shell: the desktop rail collapses to icons and expands again, the
 * choice survives a remount, and the nav labels stay in the accessibility tree
 * while collapsed. StreamGate is stubbed (the shell test owns no SSE).
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import AppLayout from './AppLayout'
import { renderApp } from '../test/render'
import { makeLoginData, makeUser, ok } from '../test/handlers'
import type { User } from '../lib/auth'
import { server } from '../test/setup'
import { avatarUrl } from '../lib/avatar'

vi.mock('./StreamGate', () => ({ default: () => null }))

const SESSION = (overrides: { user?: Partial<User> } = {}) =>
  makeLoginData({ user: makeUser({ role: 'ADMIN', ...overrides.user }) })

function renderShell() {
  return renderApp(null, {
    session: SESSION(),
    routes: [
      {
        element: <AppLayout />,
        children: [{ index: true, element: <div>Konten</div> }],
      },
    ],
  })
}

beforeEach(() => {
  localStorage.removeItem('kv-rail-collapsed')
  server.use(
    http.get('/api/schools', () => ok([])),
    http.get('/api/users/pending-count', () => ok({ pending: 0 })),
  )
})

describe('desktop rail collapse', () => {
  it('collapses to icons, keeps labels accessible, and expands again', async () => {
    const user = userEvent.setup()
    renderShell()

    const rail = (await screen.findByRole('link', { name: 'Dasbor' })).closest('.kv-rail')
    expect(rail).not.toBeNull()
    expect(rail).not.toHaveClass('is-collapsed')

    const collapse = await screen.findByRole('button', { name: 'Ciutkan navigasi' })
    expect(collapse).toHaveAttribute('aria-expanded', 'true')
    await user.click(collapse)

    expect(rail).toHaveClass('is-collapsed')
    // The label is visually clipped, not removed: the link keeps its name.
    expect(screen.getByRole('link', { name: 'Dasbor' })).toBeInTheDocument()
    expect(localStorage.getItem('kv-rail-collapsed')).toBe('1')

    const expand = screen.getByRole('button', { name: 'Bentangkan navigasi' })
    expect(expand).toHaveAttribute('aria-expanded', 'false')
    await user.click(expand)

    expect(rail).not.toHaveClass('is-collapsed')
    expect(localStorage.getItem('kv-rail-collapsed')).toBe('0')
  })

  it('restores the collapsed preference on remount', async () => {
    localStorage.setItem('kv-rail-collapsed', '1')
    renderShell()

    const link = await screen.findByRole('link', { name: 'Dasbor' })
    expect(link.closest('.kv-rail')).toHaveClass('is-collapsed')
    expect(screen.getByRole('button', { name: 'Bentangkan navigasi' })).toBeInTheDocument()
  })
})

describe('breadcrumb top bar', () => {
  it('sits above the page content on every shell route', async () => {
    renderShell()

    const nav = await screen.findByRole('navigation', { name: 'Jejak navigasi' })
    expect(within(nav).getByText('Dasbor')).toHaveAttribute('aria-current', 'page')
    // The bar is chrome: the routed page still follows it untouched.
    expect(screen.getByText('Konten')).toBeInTheDocument()
  })
})

describe('rail information architecture', () => {
  it('splits the ADMIN menu into four labelled categories, in order', async () => {
    renderShell()
    await screen.findByRole('link', { name: 'Dasbor' })

    const labels = screen
      .getAllByRole('heading', { level: 2 })
      .map((heading) => heading.textContent)
    expect(labels).toEqual(['Utama', 'Akademik', 'Data', 'Sistem'])
  })

  it('nests sessions, reports and consent under one collapsible parent', async () => {
    const user = userEvent.setup()
    renderShell()

    const toggle = await screen.findByRole('button', { name: 'Sesi' })
    expect(toggle).toHaveAttribute('aria-expanded', 'false')
    expect(toggle).toHaveAttribute('aria-controls', 'kv-sub-sessions')

    const submenu = document.getElementById('kv-sub-sessions')
    expect(submenu).not.toBeNull()
    expect(submenu).toHaveAttribute('aria-hidden', 'true')
    expect(within(submenu as HTMLElement).queryByRole('link')).toBeNull()

    await user.click(toggle)
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(submenu).toHaveAttribute('aria-hidden', 'false')

    const items = within(submenu as HTMLElement).getAllByRole('link')
    expect(items.map((item) => item.textContent)).toEqual([
      'Semua Sesi',
      'Rapor',
      'Persetujuan',
    ])
    expect(items[0]).toHaveAttribute('href', '/sessions')
    expect(items[1]).toHaveAttribute('href', '/reports')
    expect(items[2]).toHaveAttribute('href', '/consent')
  })

  it('opens the parent section and marks the child when its route is current', async () => {
    renderApp(null, {
      route: '/reports',
      session: SESSION(),
      routes: [
        {
          element: <AppLayout />,
          children: [
            { index: true, element: <div>Konten</div> },
            { path: '*', element: <div>Konten</div> },
          ],
        },
      ],
    })

    const toggle = await screen.findByRole('button', { name: 'Sesi' })
    expect(toggle).toHaveAttribute('aria-expanded', 'true')
    expect(screen.getByRole('link', { name: 'Rapor' })).toHaveAttribute(
      'aria-current',
      'page',
    )
  })

  it('drops categories a facilitator has no destination in', async () => {
    renderApp(null, {
      session: SESSION({ user: { role: 'FASILITATOR' } }),
      routes: [
        {
          element: <AppLayout />,
          children: [{ index: true, element: <div>Konten</div> }],
        },
      ],
    })

    await screen.findByRole('button', { name: 'Sesi' })
    const labels = screen
      .getAllByRole('heading', { level: 2 })
      .map((heading) => heading.textContent)
    expect(labels).toEqual(['Akademik', 'Data'])
    expect(screen.queryByRole('link', { name: 'Dasbor' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Master' })).toBeNull()
  })

  it('keeps the category labels in the accessibility tree while collapsed', async () => {
    const user = userEvent.setup()
    renderShell()

    const rail = (await screen.findByRole('link', { name: 'Dasbor' })).closest('.kv-rail')
    await user.click(screen.getByRole('button', { name: 'Ciutkan navigasi' }))

    expect(rail).toHaveClass('is-collapsed')
    const label = screen.getByRole('heading', { name: 'Akademik' })
    expect(label).toHaveClass('kv-rail-section-label')
    // Folded, a group is a link to its landing page, not a toggle over a hidden
    // list: the children are off screen and out of the accessibility tree.
    expect(screen.getByRole('link', { name: 'Sesi' })).toHaveAttribute(
      'href',
      '/sessions',
    )
    expect(screen.queryByRole('button', { name: 'Sesi' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Rapor' })).toBeNull()
  })

  it('gives the mobile drawer its own ids instead of duplicating the rail', async () => {
    const user = userEvent.setup()
    renderShell()

    await screen.findByRole('link', { name: 'Dasbor' })
    await user.click(screen.getByRole('button', { name: 'Buka navigasi' }))

    const drawerNav = document.getElementById('kv-drawer-nav')
    expect(drawerNav).not.toBeNull()

    const drawerToggle = within(drawerNav as HTMLElement).getByRole('button', {
      name: 'Sesi',
    })
    expect(drawerToggle).toHaveAttribute('aria-controls', 'kv-drawer-sub-sessions')
    // Same navigation, one id per instance: nothing is overwritten.
    expect(document.querySelectorAll('#kv-sub-sessions')).toHaveLength(1)
    expect(document.querySelectorAll('#kv-drawer-sub-sessions')).toHaveLength(1)
    expect(
      document.querySelectorAll('#kv-rail-section-academic'),
    ).toHaveLength(1)
    expect(
      document.querySelectorAll('#kv-drawer-rail-section-academic'),
    ).toHaveLength(1)

    // The drawer keeps the interactive accordion even when the rail is folded.
    await user.click(drawerToggle)
    const drawerSubmenu = document.getElementById('kv-drawer-sub-sessions')
    expect(drawerSubmenu).toHaveAttribute('aria-hidden', 'false')
    expect(within(drawerSubmenu as HTMLElement).getByRole('link', { name: 'Rapor' })).toHaveAttribute(
      'href',
      '/reports',
    )
  })
})

// spec 09 decision 8: the rail avatar shows the uploaded photo when the
// session's user carries photo_url, and falls back to the initials
// placeholder (avatarUrl(name)) when it does not.
describe('rail user avatar', () => {
  function railUser(): HTMLElement {
    return screen.getByRole('button', { name: 'Admin Bandung' })
  }

  it('shows the uploaded photo when user.photo_url is set', async () => {
    renderApp(null, {
      session: SESSION({ user: { photo_url: '/uploads/avatars/2/x.png' } }),
      routes: [
        {
          element: <AppLayout />,
          children: [{ index: true, element: <div>Konten</div> }],
        },
      ],
    })
    await screen.findByRole('link', { name: 'Dasbor' })
    expect(railUser().querySelector('img')).toHaveAttribute(
      'src',
      '/uploads/avatars/2/x.png',
    )
  })

  it('falls back to the initials placeholder without photo_url', async () => {
    renderShell()
    await screen.findByRole('link', { name: 'Dasbor' })
    expect(railUser().querySelector('img')).toHaveAttribute(
      'src',
      avatarUrl('Admin Bandung'),
    )
  })
})
