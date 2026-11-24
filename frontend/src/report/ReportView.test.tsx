/**
 * ReportView (spec F-12 / F-12.1): renders the canonical ReportData inside
 * the `#report-a4` A4 root — sections, em-dash placeholders, incomplete
 * banner, hidden null badges, and the report.css A4 contract (no visual
 * testing, class hooks + stylesheet assertions only). Report content stays
 * Indonesian regardless of UI language.
 */
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18n from '../lib/i18n'
import { reportData } from '../test/handlers'
import ReportView from './ReportView'

describe('report view sections', () => {
  it('renders every contract section from the ReportData fixture', () => {
    const { container } = render(<ReportView data={reportData()} />)

    const root = container.querySelector('#report-a4')
    expect(root).not.toBeNull()
    expect(root?.classList.contains('report-a4')).toBe(true)

    expect(screen.getByText('Kidversa EduTourism')).toBeInTheDocument()
    expect(screen.getByText('Rapor Kegiatan')).toBeInTheDocument()
    expect(screen.getByText('Kelas Batik')).toBeInTheDocument()
    expect(screen.getByText('Batik Nusantara')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Profil Anak' })).toBeInTheDocument()
    expect(screen.getByText('SD Cendekia')).toBeInTheDocument()
    expect(
      screen.getByText(
        'Aisyah aktif menjelajah motif batik dan sangan antusias berbagi temuan.',
      ),
    ).toBeInTheDocument()
    expect(screen.getByText('Kumpulkan 5 motif')).toBeInTheDocument()
    expect(screen.getByText('Mengamati motif')).toBeInTheDocument()
    expect(screen.getByText('BSB')).toBeInTheDocument() // 4 stars
    expect(screen.getByText('Sangat percaya diri')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Badge' })).toBeInTheDocument()
    expect(screen.getByText('Badge Batik')).toBeInTheDocument()
    expect(screen.getByAltText('Foto utama Aisyah Putri')).toBeInTheDocument()
    expect(screen.getByText('Dewi Lestari')).toBeInTheDocument()
    expect(screen.getByText('Fasilitator')).toBeInTheDocument()
    expect(screen.getByText('Kelompok Merah')).toBeInTheDocument()
    // No incomplete banner on a complete report.
    expect(screen.queryByText(/Rapor belum lengkap/)).toBeNull()
  })

  it('renders an em dash for a null narrative (report works without AI)', () => {
    const { container } = render(
      <ReportView data={reportData({ narrative: { text: null, generated_at: null } })} />,
    )
    const narrative = container.querySelector('.ra-narrative')
    expect(narrative?.textContent).toBe('—')
  })

  it('shows the incomplete banner with the missing activity names', () => {
    render(
      <ReportView
        data={reportData({
          complete: false,
          missing: [
            { said: 1, name: 'Mengamati motif' },
            { said: 2, name: 'Menggambar motif' },
          ],
        })}
      />,
    )
    expect(
      screen.getByText('Rapor belum lengkap — 2 kegiatan belum dinilai: Mengamati motif, Menggambar motif'),
    ).toBeInTheDocument()
  })

  it('hides badge/candidate/group sections when they are null', () => {
    const { rerender } = render(
      <ReportView
        data={reportData({ badges: { topic: null, program: null }, candidate_photo: null, group: null })}
      />,
    )
    expect(screen.queryByRole('heading', { name: 'Badge' })).toBeNull()
    expect(screen.queryByRole('heading', { name: 'Foto Utama' })).toBeNull()
    expect(screen.queryByText('Kelompok Merah')).toBeNull()

    rerender(<ReportView data={reportData()} />)
    expect(screen.getByRole('heading', { name: 'Badge' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Foto Utama' })).toBeInTheDocument()
    expect(screen.getByText('Kelompok Merah')).toBeInTheDocument()
  })

  it('renders out-of-range stars and body-less missions as em dashes', () => {
    render(
      <ReportView
        data={reportData({
          outcomes: [
            { said: 10, activity_name: 'Mengamati motif', indicator: 'X', stars: 0, note: '' },
            { said: 11, activity_name: 'Menggambar motif', indicator: 'Y', stars: 5, note: '' },
          ],
          missions: [{ mission_id: 1, name: 'Kumpulkan 5 motif', body: '' }],
        })}
      />,
    )
    // Neither 0 nor 5 maps to a star label — the cell falls back to em dash.
    expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(2)
    // A body-less mission renders its name without the " — body" tail.
    screen.getByText('Kumpulkan 5 motif')
    expect(screen.queryByText(/— Foto motif di rumah/)).toBeNull()
  })

  it('the incomplete banner without missing rows ends with a bare period', () => {
    render(<ReportView data={reportData({ complete: false, missing: [] })} />)
    expect(
      screen.getByText('Rapor belum lengkap — 0 kegiatan belum dinilai.'),
    ).toBeInTheDocument()
  })

  it('an empty mission list renders the em-dash row', () => {
    const { container } = render(<ReportView data={reportData({ missions: [] })} />)
    // The <ol> is replaced by a single dash paragraph — never an empty list.
    expect(container.querySelector('.ra-missions')).toBeNull()
    expect(
      Array.from(container.querySelectorAll('p')).some((p) => p.textContent === '—'),
    ).toBe(true)
  })

  it('a program badge without an image renders its name alone', () => {
    const { container } = render(
      <ReportView
        data={reportData({
          badges: {
            topic: { name: 'Badge Batik', image_url: '/uploads/badges/batik.png' },
            // badge present but no picture yet → the image guard skips it
            program: { name: 'Program Nusantara', image_url: '' },
          },
        })}
      />,
    )
    screen.getByText('Badge Batik')
    screen.getByText('Program Nusantara')
    // The picture-less badge keeps the text-only slot: only the topic badge
    // contributes an <img>.
    expect(container.querySelectorAll('.ra-badge img')).toHaveLength(1)
    expect(screen.queryByAltText('Badge Program Nusantara')).toBeNull()
  })

  it('content taller than the A4 box scales down and shows the overflow notice', () => {
    // jsdom heights are 0; force the sheet/content measures the layout effect
    // reads so the fixed-A4 fit computation takes the overflow branch.
    const sheet = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'clientHeight')
    const content = Object.getOwnPropertyDescriptor(HTMLElement.prototype, 'offsetHeight')
    Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => 400 })
    Object.defineProperty(HTMLElement.prototype, 'offsetHeight', { configurable: true, get: () => 1600 })
    try {
      const { container } = render(<ReportView data={reportData()} />)
      screen.getByText('Konten melebihi satu halaman A4 — tampilan diperkecil.')
      // The sheet content carries the scale transform (400/1123 ≈ 0.36).
      const contentDiv = container.querySelector('.report-a4__content') as HTMLElement
      expect(contentDiv.style.transform).toMatch(/^scale\(0\./)
    } finally {
      if (sheet) Object.defineProperty(HTMLElement.prototype, 'clientHeight', sheet)
      if (content) Object.defineProperty(HTMLElement.prototype, 'offsetHeight', content)
    }
  })
})

describe('report A4 layout contract', () => {
  it('uses the fixed-A4 class hooks on the rendered root (css:false: no stylesheet read)', () => {
    // report.css anchors #report-a4 to 210×297mm with 10mm margins; the test
    // surface asserts the structural hooks the stylesheet and export depend on.
    const { container } = render(<ReportView data={reportData()} />)
    const root = container.querySelector('#report-a4')
    expect(root?.classList.contains('report-a4')).toBe(true)
    expect(container.querySelector('.report-a4__sheet')).not.toBeNull()
    expect(container.querySelector('.report-a4__content')).not.toBeNull()
    // jsdom reports zero heights → no scale-down → no overflow notice.
    expect(screen.queryByText(/Konten melebihi satu halaman/)).toBeNull()
  })
})

describe('report image gate', () => {
  it('an image error swaps the placeholder to a retry that cache-busts the URL', async () => {
    const { container } = render(<ReportView data={reportData()} />)
    const img = container.querySelector('.ra-img img') as HTMLImageElement

    fireEvent.error(img)

    const retry = await screen.findByRole('button', { name: 'Coba lagi' })
    await userEvent.setup().click(retry)

    // The retry re-requests the same URL with a cache-buster (no query yet).
    await waitFor(() =>
      expect(container.querySelector('.ra-img img')).toHaveAttribute(
        'src',
        '/uploads/badges/batik.png?r=1',
      ),
    )
  })

  it('a retry on a URL that already carries a query appends with &r=', async () => {
    const { container } = render(
      <ReportView
        data={reportData({
          badges: { topic: { name: 'Badge Batik', image_url: '/uploads/badges/batik.png?v=9' }, program: null },
        })}
      />,
    )
    const img = container.querySelector('.ra-img img') as HTMLImageElement

    fireEvent.error(img)
    await userEvent.setup().click(await screen.findByRole('button', { name: 'Coba lagi' }))

    await waitFor(() =>
      expect(container.querySelector('.ra-img img')).toHaveAttribute(
        'src',
        '/uploads/badges/batik.png?v=9&r=1',
      ),
    )
  })

  it('the 10s load gate flips a silent image to the retry affordance', () => {
    vi.useFakeTimers()
    const { container } = render(<ReportView data={reportData()} />)
    // No load/error ever fires (slow bytes) — the timeout still flips BOTH
    // gated images (badge + candidate photo) to their retry affordance.
    act(() => {
      vi.advanceTimersByTime(10_000)
    })
    expect(screen.getAllByRole('button', { name: 'Coba lagi' })).toHaveLength(2)
    // The candidate photo keeps its box (placeholder → retry, not gone).
    expect(container.querySelector('.ra-img img')).not.toBeNull()
  })

  it('a loaded image clears the placeholder', async () => {
    const { container } = render(<ReportView data={reportData()} />)
    // Two gated images render (badge + candidate photo) — load them both.
    for (const img of container.querySelectorAll<HTMLImageElement>('.ra-img img')) {
      fireEvent.load(img)
    }

    await waitFor(() =>
      expect(container.querySelectorAll('.ra-img.is-ready')).toHaveLength(2),
    )
    expect(screen.queryByText('Memuat…')).toBeNull()
  })
})

describe('report language', () => {
  it('stays Indonesian while the UI language is EN (spec F-14)', async () => {
    await i18n.changeLanguage('en')
    render(<ReportView data={reportData()} />)
    expect(screen.getByText('Rapor Kegiatan')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Profil Anak' })).toBeInTheDocument()
    await i18n.changeLanguage('id')
  })
})
