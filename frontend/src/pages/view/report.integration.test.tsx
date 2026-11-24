/**
 * Public report view integration (FL-13 / F-12): `/view/report?token=`
 * fetches the public report (no auth headers) and renders `#report-a4`;
 * the PNG/PDF export buttons run client-side on the rendered sheet.
 */
import { beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import ReportPage from './ReportPage'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { idleStream, ok, reportData } from '../../test/handlers'

const mocks = vi.hoisted(() => ({
  toPng: vi.fn(async () => 'data:image/png;base64,AAA'),
  pdfDocs: [] as Array<{ opts: unknown; addImage: Mock; save: Mock }>,
}))

vi.mock('html-to-image', () => ({ toPng: mocks.toPng }))
vi.mock('jspdf', () => ({
  jsPDF: class {
    opts: unknown
    addImage: Mock
    save: Mock
    constructor(opts: unknown) {
      this.opts = opts
      this.addImage = vi.fn()
      this.save = vi.fn()
      mocks.pdfDocs.push(this)
    }
  },
}))

describe('report public view fetch and render', () => {
  beforeEach(() => {
    mocks.toPng.mockClear()
    mocks.pdfDocs.length = 0
  })

  it('loads the tokenised report and renders the A4 sheet without auth headers', async () => {
    const requests: Array<{ url: URL; authorization: string | null }> = []
    server.use(
      http.get('/public/report', ({ request }) => {
        requests.push({
          url: new URL(request.url),
          authorization: request.headers.get('Authorization'),
        })
        return ok(reportData())
      }),
      idleStream('/public/stream'),
    )
    renderApp(<ReportPage />, { route: '/view/report?token=tok-9' })

    await screen.findByText('Rapor Aisyah Putri') // PageHeader title
    const sheet = document.getElementById('report-a4')
    expect(sheet).not.toBeNull()
    expect(within(sheet as HTMLElement).getByText('Mengamati motif')).toBeInTheDocument()

    expect(requests).toHaveLength(1)
    expect(requests[0].url.searchParams.get('token')).toBe('tok-9')
    expect(requests[0].authorization).toBeNull() // public endpoint: no Bearer, no school
  })

  it('exports the rendered sheet to PNG with the sanitised filename', async () => {
    server.use(
      http.get('/public/report', () => ok(reportData())),
      idleStream('/public/stream'),
    )
    const seen: Array<{ download: string; href: string }> = []
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
      this: HTMLAnchorElement,
    ) {
      seen.push({ download: this.download, href: this.href })
    })
    const user = userEvent.setup()
    renderApp(<ReportPage />, { route: '/view/report?token=tok-9' })

    await screen.findByText('Rapor Aisyah Putri')
    await user.click(screen.getByRole('button', { name: 'Unduh PNG' }))

    expect(mocks.toPng).toHaveBeenCalledWith(document.getElementById('report-a4'), {
      cacheBust: true,
    })
    expect(seen).toHaveLength(1)
    expect(seen[0].download).toBe('Rapor-Aisyah_Putri-Batik_Nusantara-20261009.png')
    expect(seen[0].href).toBe('data:image/png;base64,AAA')
  })

  it('exports the rendered sheet to PDF via jsPDF A4', async () => {
    server.use(
      http.get('/public/report', () => ok(reportData())),
      idleStream('/public/stream'),
    )
    const user = userEvent.setup()
    renderApp(<ReportPage />, { route: '/view/report?token=tok-9' })

    await screen.findByText('Rapor Aisyah Putri')
    await user.click(screen.getByRole('button', { name: 'Unduh PDF' }))

    expect(mocks.pdfDocs).toHaveLength(1)
    expect(mocks.pdfDocs[0].opts).toEqual({ orientation: 'p', unit: 'mm', format: 'a4' })
    expect(mocks.pdfDocs[0].save).toHaveBeenCalledWith(
      'Rapor-Aisyah_Putri-Batik_Nusantara-20261009.pdf',
    )
  })
})
