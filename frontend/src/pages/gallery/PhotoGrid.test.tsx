/**
 * Photo grid (F-10 honest-file rule): loading/error/empty states, the
 * missing-file placeholder (E29 item flag), and the candidate/delete row
 * actions routed through the parent.
 */
import { describe, expect, it, vi } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import PhotoGrid from './PhotoGrid'
import { renderApp } from '../../test/render'
import { ApiError } from '../../lib/api'
import { photoItem } from '../../test/helpers.cov-session'

function renderGrid(
  overrides: Partial<{
    photos: ReturnType<typeof photoItem>[] | null
    loading: boolean
    error: unknown
  }> = {},
) {
  const onRetry = vi.fn()
  const onCandidate = vi.fn()
  const onDelete = vi.fn()
  const rendered = renderApp(
    <PhotoGrid
      photos={overrides.photos ?? null}
      loading={overrides.loading ?? false}
      error={overrides.error ?? null}
      onRetry={onRetry}
      onCandidate={onCandidate}
      onDelete={onDelete}
    />,
  )
  return { container: rendered.container, onRetry, onCandidate, onDelete }
}

describe('photo grid states', () => {
  it('shows the tile skeleton while loading without photos', () => {
    const { container } = renderGrid({ loading: true })
    expect(container.querySelector('.kv-page-skeleton--grid')).not.toBeNull()
    expect(container.querySelector('.ant-spin')).toBeNull()
  })

  it('shows the INTERNAL error with a retry action', async () => {
    const { onRetry } = renderGrid({ error: new Error('boom') })
    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
    const user = userEvent.setup()
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    expect(onRetry).toHaveBeenCalledTimes(1)
  })

  it('shows the ApiError code translated', () => {
    renderGrid({ error: new ApiError('PHOTO_LIMIT', 'x') })
    screen.getByText('Maksimal 10 foto. Hapus atau ganti salah satunya.')
  })

  it('shows the empty state', () => {
    renderGrid({ photos: [] })
    screen.getByText('Belum ada foto.')
  })
})

describe('photo grid items', () => {
  it('renders server photos, the missing placeholder, and routes row actions', async () => {
    const { onCandidate, onDelete } = renderGrid({
      photos: [photoItem(7), photoItem(8, { missing: true, url: '/uploads/photos/gone.jpg' })],
    })
    const user = userEvent.setup()

    // Regular item renders the /uploads bytes; the missing row is a notice.
    const img = screen.getByAltText('/uploads/photos/7.jpg')
    expect(img).toHaveAttribute('src', '/uploads/photos/7.jpg')
    screen.getByText('Berkas foto tidak ditemukan di server.')
    expect(screen.queryByAltText('/uploads/photos/gone.jpg')).toBeNull()

    // Candidate toggle routes the id upward.
    const tile = img.closest('.ant-card') as HTMLElement
    await user.click(within(tile).getByRole('button', { name: 'Jadikan foto utama' }))
    expect(onCandidate).toHaveBeenCalledWith(7)

    // Delete confirms first, then routes the id upward.
    await user.click(within(tile).getByRole('button', { name: 'Hapus foto' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('Hapus foto ini?')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Hapus' }))
    expect(onDelete).toHaveBeenCalledWith(7)
  })
})
