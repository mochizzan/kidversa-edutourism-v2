/**
 * Gallery index (`/sessions/:sid/gallery`): a group filter that defaults to all
 * groups, a clickable children table with consent status, the dedicated child
 * route as the row's destination, and the legacy `?cid=&stid=` redirect.
 *
 * The page is a directory: it must never fetch photos or frames — that work
 * belongs to the child gallery.
 */
import { describe, expect, it } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import GalleryPage from './GalleryPage'
import ChildGalleryPage from './ChildGalleryPage'
import { emitSignal } from '../../lib/stream'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, ok, paged } from '../../test/handlers'
import { detailFixture } from '../../test/helpers.cov-session'
import type { RouteObject } from 'react-router'

const GALLERY_ROUTES: RouteObject[] = [
  { path: '/sessions/:sid/gallery', element: <GalleryPage /> },
  { path: '/sessions/:sid/gallery/children/:cid', element: <ChildGalleryPage /> },
]

const ADMIN_SESSION = () => makeLoginData({ user: makeUser({ role: 'ADMIN' }) })

function framesHandler() {
  return http.get('/api/frames', () =>
    paged([{ id: 11, name: 'Bingkai Cantik', image_url: '/uploads/frames/11.png' }], {
      page: 1,
      per_page: 1000,
      total: 1,
    }),
  )
}

function renderGallery(route = '/sessions/1/gallery') {
  renderApp(null, { routes: GALLERY_ROUTES, route, session: ADMIN_SESSION() })
}

describe('gallery index filter', () => {
  it('defaults to all groups and lists every child with group, school and consent', async () => {
    let photoCalls = 0
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get('/api/sessions/1/topics/:stid/children/:cid/photos', () => {
        photoCalls += 1
        return ok([])
      }),
    )
    renderGallery()

    await screen.findByText('Aisyah Putri')
    screen.getByText('Budi Santoso')

    // One filter, and it starts on the whole session.
    expect(screen.getAllByRole('combobox')).toHaveLength(1)
    expect(screen.getByTitle('Semua kelompok')).toBeInTheDocument()

    // Group + school + consent are columns, not buried in a picker.
    expect(screen.getAllByText('Kelompok Merah').length).toBeGreaterThan(0)
    screen.getByText('SD Cendekia')
    screen.getByText('SD Melati')
    screen.getByText('Menunggu')
    screen.getByText('Disetujui')

    // The row action states its verb and points at the dedicated gallery.
    const openLinks = screen.getAllByRole('link', { name: 'Buka galeri' })
    expect(openLinks).toHaveLength(2)
    expect(openLinks[0]).toHaveAttribute('href', '/sessions/1/gallery/children/2')

    // A directory, not a workspace: no photo request was made.
    expect(photoCalls).toBe(0)
  })

  it('filters the table by group and restores every child on clear', async () => {
    const detail = detailFixture({ status: 'OPENED' })
    detail.groups.push({
      gid: 6,
      name: 'Kelompok Biru',
      own: true,
      owner: { id: 7, name: 'Dewi Lestari' },
      owner_missing: false,
      member_count: 1,
      consent: { SETUJU: 1 },
      members: [
        { child_id: 4, name: 'Citra Lestari', age: 7, school: 'SD Harapan', consent: 'SETUJU' },
      ],
    })
    server.use(http.get('/api/sessions/1', () => ok(detail)))
    const user = userEvent.setup()
    renderGallery()

    await screen.findByText('Aisyah Putri')
    await user.click(screen.getByRole('combobox', { name: 'Kelompok' }))
    // The table already prints the group name; the dropdown option is last.
    const options = await screen.findAllByText('Kelompok Biru')
    await user.click(options[options.length - 1])

    await waitFor(() => expect(screen.queryByText('Aisyah Putri')).toBeNull())
    screen.getByText('Citra Lestari')
    expect(screen.queryByText('Budi Santoso')).toBeNull()

    // The tray says one filter is on; clearing brings the whole session back.
    await user.click(screen.getByRole('button', { name: 'Hapus filter (1)' }))
    await screen.findByText('Aisyah Putri')
    screen.getByText('Budi Santoso')
  })

  it('shows the session empty state and no group filter when there are no children', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ groups: [] }))))
    renderGallery()

    await screen.findByText('Belum ada anak di sesi ini')
    expect(screen.queryByRole('combobox')).toBeNull()
  })
})

describe('gallery index navigation', () => {
  it('opens the dedicated child gallery when the row is clicked', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))))
    renderGallery()

    await screen.findByText('Aisyah Putri')
    // Click the row itself (a cell that is not the name link or action).
    fireEvent.click(screen.getByText('SD Cendekia'))

    await screen.findByRole('heading', { name: 'Aisyah Putri' })
    screen.getByText('Galeri terkunci sampai orang tua menyetujui.')
  })

  it('redirects a legacy child deep link to the child route with its topic', async () => {
    let photoPath = ''
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      framesHandler(),
      http.get('/api/sessions/1/topics/:stid/children/:cid/photos', ({ request }) => {
        photoPath = new URL(request.url).pathname
        return ok([])
      }),
    )
    renderGallery('/sessions/1/gallery?gid=5&cid=3&stid=3')

    await screen.findByRole('heading', { name: 'Budi Santoso' })
    await waitFor(() =>
      expect(photoPath).toBe('/api/sessions/1/topics/3/children/3/photos'),
    )
  })
})

/**
 * Kept as a regression guard for the old gallery's realtime contract: the
 * index must not register a photo refetch that would fire on a stray frame.
 */
describe('gallery index realtime', () => {
  it('does not fetch photos when a photo.changed frame arrives', async () => {
    let photoCalls = 0
    server.use(
      http.get('/api/sessions/1', () => ok(detailFixture({ status: 'OPENED' }))),
      http.get('/api/sessions/1/topics/:stid/children/:cid/photos', () => {
        photoCalls += 1
        return ok([])
      }),
    )
    renderGallery()

    await screen.findByText('Aisyah Putri')
    emitSignal('photo.changed', { rev: 1 })
    await new Promise((resolve) => setTimeout(resolve, 50))
    expect(photoCalls).toBe(0)
  })
})
