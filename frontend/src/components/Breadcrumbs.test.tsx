/**
 * Breadcrumb top bar (DESIGN §global shell): the trail resolves from the URL
 * alone for every shell path, a page can name its own dynamic segment, and the
 * crumb you are on is never a link.
 */
import { describe, expect, it } from 'vitest'
import { screen, within } from '@testing-library/react'
import Breadcrumbs from './Breadcrumbs'
import { CrumbNamesProvider, useCrumbName } from '../lib/crumbs'
import { renderApp } from '../test/render'

/** Render the bar on its own at `route` and return its <nav> landmark. */
function renderTrail(route: string) {
  renderApp(
    <CrumbNamesProvider>
      <Breadcrumbs />
    </CrumbNamesProvider>,
    { route },
  )
  return screen.getByRole('navigation', { name: 'Jejak navigasi' })
}

describe('breadcrumb top bar', () => {
  it('marks the dashboard as the only, current crumb', () => {
    const nav = renderTrail('/')
    expect(within(nav).getByText('Dasbor')).toHaveAttribute('aria-current', 'page')
    expect(within(nav).queryAllByRole('link')).toHaveLength(0)
  })

  it('nests master pages under the Masters section', () => {
    const nav = renderTrail('/programs')
    expect(within(nav).getByText('Master')).toBeInTheDocument()
    // No `/masters` route exists, so the section label is not a link.
    expect(within(nav).queryByRole('link', { name: 'Master' })).toBeNull()
    expect(within(nav).getByText('Program')).toHaveAttribute('aria-current', 'page')
  })

  it('links every ancestor of a session page and leaves the leaf unlinked', () => {
    const nav = renderTrail('/sessions/12/reports')
    expect(within(nav).getByRole('link', { name: 'Sesi' })).toHaveAttribute('href', '/sessions')
    expect(within(nav).getByRole('link', { name: 'Detail sesi' })).toHaveAttribute(
      'href',
      '/sessions/12',
    )
    const leaf = within(nav).getByText('Rapor')
    expect(leaf).toHaveAttribute('aria-current', 'page')
    expect(within(nav).queryByRole('link', { name: 'Rapor' })).toBeNull()
  })

  it('rebuilds the hierarchy under a flat assessment URL', () => {
    const nav = renderTrail('/assessment/12/5/3')
    expect(within(nav).getByRole('link', { name: 'Sesi' })).toHaveAttribute('href', '/sessions')
    expect(within(nav).getByRole('link', { name: 'Detail sesi' })).toHaveAttribute(
      'href',
      '/sessions/12',
    )
    expect(within(nav).getByText('Penilaian')).toHaveAttribute('aria-current', 'page')
  })

  it('shows the create page as a child of the session list', () => {
    const nav = renderTrail('/sessions/new')
    expect(within(nav).getByRole('link', { name: 'Sesi' })).toHaveAttribute('href', '/sessions')
    expect(within(nav).getByText('Sesi baru')).toHaveAttribute('aria-current', 'page')
  })

  it('keeps single-level pages to one current crumb', () => {
    const nav = renderTrail('/consent')
    expect(within(nav).getByText('Persetujuan')).toHaveAttribute('aria-current', 'page')
    expect(within(nav).queryAllByRole('link')).toHaveLength(0)
  })

  it('prints the name a page registered for its dynamic segment', () => {
    function NamedBar() {
      useCrumbName('sid', 'Kunjungan SDN 3')
      return <Breadcrumbs />
    }

    renderApp(
      <CrumbNamesProvider>
        <NamedBar />
      </CrumbNamesProvider>,
      { route: '/sessions/12/gallery' },
    )
    const nav = screen.getByRole('navigation', { name: 'Jejak navigasi' })
    expect(within(nav).getByRole('link', { name: 'Kunjungan SDN 3' })).toHaveAttribute(
      'href',
      '/sessions/12',
    )
    expect(within(nav).getByText('Galeri')).toHaveAttribute('aria-current', 'page')
  })

  it('nests a child gallery under the gallery trail', () => {
    function NamedBar() {
      useCrumbName('sid', 'Kunjungan SDN 3')
      useCrumbName('cid', 'Aisyah Putri')
      return <Breadcrumbs />
    }

    renderApp(
      <CrumbNamesProvider>
        <NamedBar />
      </CrumbNamesProvider>,
      { route: '/sessions/12/gallery/children/2' },
    )
    const nav = screen.getByRole('navigation', { name: 'Jejak navigasi' })
    expect(within(nav).getByRole('link', { name: 'Galeri' })).toHaveAttribute(
      'href',
      '/sessions/12/gallery',
    )
    expect(within(nav).getByText('Aisyah Putri')).toHaveAttribute('aria-current', 'page')
    expect(within(nav).queryByRole('link', { name: 'Aisyah Putri' })).toBeNull()
  })

  it('nests the camera under the child gallery', () => {
    function NamedBar() {
      useCrumbName('sid', 'Kunjungan SDN 3')
      useCrumbName('cid', 'Aisyah Putri')
      return <Breadcrumbs />
    }

    renderApp(
      <CrumbNamesProvider>
        <NamedBar />
      </CrumbNamesProvider>,
      { route: '/sessions/12/gallery/children/2/capture?stid=3' },
    )
    const nav = screen.getByRole('navigation', { name: 'Jejak navigasi' })
    expect(within(nav).getByRole('link', { name: 'Galeri' })).toHaveAttribute(
      'href',
      '/sessions/12/gallery',
    )
    // Walking back keeps the camera's topic scope.
    expect(within(nav).getByRole('link', { name: 'Aisyah Putri' })).toHaveAttribute(
      'href',
      '/sessions/12/gallery/children/2?stid=3',
    )
    expect(within(nav).getByText('Kamera')).toHaveAttribute('aria-current', 'page')
    expect(within(nav).queryByRole('link', { name: 'Kamera' })).toBeNull()
  })
})
