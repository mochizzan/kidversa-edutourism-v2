/**
 * Public gallery view (FL-15): the locked reasons (CHILD_INACTIVE wins over
 * consent; CONSENT_PENDING / CONSENT_DENIED), the unlocked photo grid with
 * the candidate badge + missing placeholder, the empty state, the
 * transient-error retry, and the terminal `link.revoked` / TOKEN_INVALID
 * views with the public stream closed for good.
 */
import { describe, expect, it } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import GalleryPage from './GalleryPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { idleStream, ok, sseChannel } from '../../test/handlers'
import { advance, pushFrame, until, useSseFakeTimers } from '../../test/helpers.cov-session'

interface PublicPhoto {
  id: number
  url: string
  topic: { stid: number; name: string }
  created_at: string
  is_candidate: boolean
  missing?: boolean
}

function galleryData(overrides: Record<string, unknown> = {}) {
  return {
    child: { id: 2, name: 'Aisyah Putri', age: 8, school: 'SD Cendekia' },
    session: { id: 1, name: 'Kelas Batik', date: '2026-10-09' },
    photos: [
      {
        id: 7,
        url: '/uploads/photos/7.jpg',
        topic: { stid: 3, name: 'Batik Nusantara' },
        created_at: '2026-10-09T10:00:00Z',
        is_candidate: true,
      },
      {
        id: 8,
        url: '/uploads/photos/gone.jpg',
        topic: { stid: 3, name: 'Batik Nusantara' },
        created_at: '2026-10-09T10:01:00Z',
        is_candidate: false,
        missing: true,
      },
    ] as PublicPhoto[],
    locked: false,
    reason: null,
    ...overrides,
  }
}

describe('gallery public view locked reasons', () => {
  it('CHILD_INACTIVE wins over consent and shows no photos', async () => {
    server.use(
      http.get('/public/gallery', () =>
        ok(galleryData({ photos: [], locked: true, reason: 'CHILD_INACTIVE' })),
      ),
      idleStream('/public/stream'),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery?token=tok-1' })

    await screen.findByText('Galeri Aisyah Putri')
    screen.getByText('Anak ini sudah dinonaktifkan.')
    expect(document.querySelector('img')).toBeNull()
  })

  it('CONSENT_PENDING renders the locked notice', async () => {
    server.use(
      http.get('/public/gallery', () =>
        ok(galleryData({ photos: [], locked: true, reason: 'CONSENT_PENDING' })),
      ),
      idleStream('/public/stream'),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery?token=tok-1' })

    await screen.findByText('Galeri terkunci sampai persetujuan diberikan.')
  })

  it('CONSENT_DENIED renders the denied notice', async () => {
    server.use(
      http.get('/public/gallery', () =>
        ok(galleryData({ photos: [], locked: true, reason: 'CONSENT_DENIED' })),
      ),
      idleStream('/public/stream'),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery?token=tok-1' })

    await screen.findByText('Persetujuan ditolak — foto tidak ditampilkan.')
  })
})

describe('gallery public view grid', () => {
  it('renders the photo grid with the candidate badge and missing placeholder', async () => {
    server.use(
      http.get('/public/gallery', () => ok(galleryData())),
      idleStream('/public/stream'),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery?token=tok-1' })

    await screen.findByText('Batik Nusantara')
    // Regular photo renders its /uploads bytes + candidate badge + topic.
    expect(document.querySelector('img[src="/uploads/photos/7.jpg"]')).not.toBeNull()
    screen.getByText('Foto utama')
    // The durable-but-gone row is a placeholder notice, never an img.
    expect(document.querySelector('img[src="/uploads/photos/gone.jpg"]')).toBeNull()
    screen.getByText('Berkas foto tidak ditemukan di server.')
  })

  it('a photo that fails to load shows the error with a cache-busted retry', async () => {
    const data = galleryData({ photos: [galleryData().photos[0]] })
    server.use(
      http.get('/public/gallery', () => ok(data)),
      idleStream('/public/stream'),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery?token=tok-1' })

    // Empty alt → query the DOM directly.
    await waitFor(() => expect(document.querySelector('img')).not.toBeNull())
    fireEvent.error(document.querySelector('img') as HTMLImageElement)

    // Honest error state: INTERNAL + a retry that re-requests with ?r=1.
    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
    await userEvent.setup().click(screen.getByRole('button', { name: 'Coba lagi' }))

    await waitFor(() => {
      expect(
        document.querySelector('img[src="/uploads/photos/7.jpg?r=1"]'),
      ).not.toBeNull()
    })
  })

  it('a loaded photo shows the Image placeholder mask until the load event fires', async () => {
    const data = galleryData({ photos: [galleryData().photos[0]] })
    server.use(
      http.get('/public/gallery', () => ok(data)),
      idleStream('/public/stream'),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery?token=tok-1' })

    await waitFor(() => expect(document.querySelector('img')).not.toBeNull())
    const img = document.querySelector('img') as HTMLImageElement
    // antd Image's placeholder mask (a Spin) is up until the bytes arrive…
    expect(document.querySelector('.ant-image-placeholder')).not.toBeNull()
    expect(document.querySelector('.ant-spin')).not.toBeNull()
    fireEvent.load(img)
    // …then rc-image flips its own status on the img's load event and the
    // placeholder mask disappears — the img stays in the DOM throughout.
    await waitFor(() => {
      expect(document.querySelector('.ant-image-placeholder')).toBeNull()
      expect(document.querySelector('.ant-spin')).toBeNull()
      expect(document.querySelector('img[src="/uploads/photos/7.jpg"]')).not.toBeNull()
    })
  })

  it('renders the empty state', async () => {
    server.use(
      http.get('/public/gallery', () => ok(galleryData({ photos: [] }))),
      idleStream('/public/stream'),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery?token=tok-1' })

    await screen.findByText('Belum ada foto.')
  })

  it('a transient error renders translated with a working retry', async () => {
    let calls = 0
    server.use(
      http.get('/public/gallery', () => {
        calls += 1
        if (calls === 1) {
          return HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } })
        }
        return ok(galleryData({ photos: [] }))
      }),
      idleStream('/public/stream'),
    )
    const user = userEvent.setup()
    renderApp(<GalleryPage />, { route: '/view/gallery?token=tok-1' })

    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))

    await screen.findByText('Belum ada foto.')
    expect(calls).toBe(2)
  })

  it('TOKEN_INVALID renders the terminal invalid view', async () => {
    server.use(
      http.get('/public/gallery', () =>
        HttpResponse.json({ error: { code: 'TOKEN_INVALID', message: 'revoked' } }),
      ),
      idleStream('/public/stream'),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery?token=dead' })

    await screen.findByText('Tautan tidak valid atau sudah dicabut.')
  })

  it('a route without a token renders the invalid view and never fetches', async () => {
    let calls = 0
    server.use(
      http.get('/public/gallery', () => {
        calls += 1
        return ok(galleryData())
      }),
      idleStream('/public/stream'),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery' })

    await screen.findByText('Tautan tidak valid atau sudah dicabut.')
    expect(calls).toBe(0)
  })
})

describe('gallery public view realtime', () => {
  it('gallery.changed refetches; link.revoked closes the stream for good', async () => {
    useSseFakeTimers()
    let calls = 0
    const photos: PublicPhoto[] = [
      {
        id: 7,
        url: '/uploads/photos/7.jpg',
        topic: { stid: 3, name: 'Batik Nusantara' },
        created_at: '2026-10-09T10:00:00Z',
        is_candidate: false,
      },
    ]
    const channel = sseChannel('/public/stream')
    server.use(
      channel.handler,
      http.get('/public/gallery', () => {
        calls += 1
        return ok(galleryData({ photos }))
      }),
    )
    renderApp(<GalleryPage />, { route: '/view/gallery?token=tok-1' })

    await until(() => channel.handshakes === 1)
    await until(() => document.querySelector('img[src="/uploads/photos/7.jpg"]') !== null)

    // A gallery.changed frame triggers a same-token refetch.
    photos.push({
      id: 9,
      url: '/uploads/photos/9.jpg',
      topic: { stid: 3, name: 'Batik Nusantara' },
      created_at: '2026-10-09T11:00:00Z',
      is_candidate: true,
    })
    pushFrame(channel, 'gallery.changed', { rev: 1 })
    await until(() => document.querySelector('img[src="/uploads/photos/9.jpg"]') !== null)
    expect(calls).toBe(2)

    // link.revoked is terminal: the invalid view renders and the page never
    // reconnects. NB: the "late frame reaches nobody" check is not assertible
    // in this harness — jsdom+MSW keeps the in-memory SSE controller alive
    // after AbortController.abort() (a real browser errors the reader), so a
    // synthetic late frame would still hit the page's registered refetch.
    pushFrame(channel, 'link.revoked', { rev: 2 })
    await until(() => screen.queryByText('Tautan tidak valid atau sudah dicabut.') !== null)
    await advance(120_000)
    expect(channel.handshakes).toBe(1)
  })
})
