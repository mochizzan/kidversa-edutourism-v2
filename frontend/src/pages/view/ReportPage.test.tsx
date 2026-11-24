/**
 * Public report view (FL-15): tokenised fetch without auth headers, the A4
 * render + client-only PNG/PDF export (html-to-image / jsPDF mocked like
 * report/export.test), the terminal TOKEN_INVALID view, the transient-error
 * retry, and `link.revoked` closing the public stream for good.
 */
import { beforeEach, describe, expect, it, vi, type Mock } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import ReportPage from './ReportPage'
import { ApiError } from '../../lib/api'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { idleStream, ok, reportData, sseChannel } from '../../test/handlers'
import {
  advance,
  pushFrame,
  until,
  useSseFakeTimers,
} from '../../test/helpers.cov-session'

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

describe('report public view fetch, render and export', () => {
  beforeEach(() => {
    mocks.toPng.mockClear()
    mocks.pdfDocs.length = 0
  })

  it('loads the tokenised report without auth headers and renders the A4 sheet', async () => {
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

    await screen.findByText('Rapor Aisyah Putri')
    const sheet = document.getElementById('report-a4')
    expect(sheet).not.toBeNull()
    expect(within(sheet as HTMLElement).getByText('Mengamati motif')).toBeInTheDocument()

    expect(requests).toHaveLength(1)
    expect(requests[0].url.searchParams.get('token')).toBe('tok-9')
    expect(requests[0].authorization).toBeNull()
  })

  it('exports PNG with the sanitised filename through the export buttons', async () => {
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

  it('exports PDF through the jsPDF A4 path', async () => {
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

  it('a failed export renders the translated error inline', async () => {
    mocks.toPng.mockRejectedValueOnce(new Error('canvas exploded'))
    server.use(
      http.get('/public/report', () => ok(reportData())),
      idleStream('/public/stream'),
    )
    const user = userEvent.setup()
    renderApp(<ReportPage />, { route: '/view/report?token=tok-9' })

    await screen.findByText('Rapor Aisyah Putri')
    await user.click(screen.getByRole('button', { name: 'Unduh PNG' }))

    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
    // The button is usable again — exporting state cleared in `finally`.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: 'Unduh PNG' })).toBeEnabled(),
    )
  })

  it('a transient load error renders translated with a working retry', async () => {
    let calls = 0
    server.use(
      http.get('/public/report', () => {
        calls += 1
        if (calls === 1) {
          return HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } })
        }
        return ok(reportData())
      }),
      idleStream('/public/stream'),
    )
    const user = userEvent.setup()
    renderApp(<ReportPage />, { route: '/view/report?token=tok-9' })

    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))

    await screen.findByText('Rapor Aisyah Putri')
    expect(calls).toBe(2)
  })

  it('TOKEN_INVALID on load renders the terminal invalid view', async () => {
    server.use(
      http.get('/public/report', () =>
        HttpResponse.json({ error: { code: 'TOKEN_INVALID', message: 'revoked' } }),
      ),
      idleStream('/public/stream'),
    )
    renderApp(<ReportPage />, { route: '/view/report?token=dead' })

    await screen.findByText('Tautan tidak valid atau sudah dicabut.')
    expect(screen.queryByRole('button', { name: 'Unduh PNG' })).toBeNull()
  })

  it('an ApiError export failure renders its CODE translation inline', async () => {
    mocks.toPng.mockRejectedValueOnce(new ApiError('RATE_LIMITED', 'slow down', 3))
    server.use(
      http.get('/public/report', () => ok(reportData())),
      idleStream('/public/stream'),
    )
    const user = userEvent.setup()
    renderApp(<ReportPage />, { route: '/view/report?token=tok-9' })

    await screen.findByText('Rapor Aisyah Putri')
    await user.click(screen.getByRole('button', { name: 'Unduh PNG' }))

    await screen.findByText('Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.')
  })

  it('a route without a token renders the invalid view and never fetches', async () => {
    let calls = 0
    server.use(
      http.get('/public/report', () => {
        calls += 1
        return ok(reportData())
      }),
      idleStream('/public/stream'),
    )
    renderApp(<ReportPage />, { route: '/view/report' })

    await screen.findByText('Tautan tidak valid atau sudah dicabut.')
    expect(calls).toBe(0)
  })
})

describe('report public view failure paths', () => {
  it('TOKEN_INVALID renders the terminal invalid view (no retry)', async () => {
    server.use(
      http.get('/public/report', () =>
        HttpResponse.json({ error: { code: 'TOKEN_INVALID', message: 'revoked' } }),
      ),
      idleStream('/public/stream'),
    )
    renderApp(<ReportPage />, { route: '/view/report?token=dead' })

    await screen.findByText('Tautan tidak valid atau sudah dicabut.')
    expect(screen.queryByRole('button', { name: 'Coba lagi' })).toBeNull()
    expect(document.getElementById('report-a4')).toBeNull()
  })

  it('a transient error renders translated with a working retry', async () => {
    let calls = 0
    server.use(
      http.get('/public/report', () => {
        calls += 1
        if (calls === 1) {
          return HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } })
        }
        return ok(reportData())
      }),
      idleStream('/public/stream'),
    )
    const user = userEvent.setup()
    renderApp(<ReportPage />, { route: '/view/report?token=tok-9' })

    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))

    await screen.findByText('Rapor Aisyah Putri')
    expect(calls).toBe(2)
  })

  it('report.changed refetches; link.revoked flips to the invalid view and closes the public stream', async () => {
    useSseFakeTimers()
    let calls = 0
    const channel = sseChannel('/public/stream')
    server.use(
      channel.handler,
      http.get('/public/report', () => {
        calls += 1
        return ok(reportData())
      }),
    )
    renderApp(<ReportPage />, { route: '/view/report?token=tok-9' })

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Rapor Aisyah Putri') !== null)

    // A report.changed frame triggers a same-token refetch.
    pushFrame(channel, 'report.changed', { rev: 1 })
    await until(() => calls === 2)

    const callsBeforeRevoked = calls

    pushFrame(channel, 'link.revoked', { rev: 1 })
    await until(() => screen.queryByText('Tautan tidak valid atau sudah dicabut.') !== null)

    // The stream closed with the terminal state: no reconnect ever happens
    // (backoff/retry would handshake again). NB: a synthetic late frame cannot
    // be asserted inert — jsdom+MSW keeps the in-memory controller alive after
    // abort, unlike a real browser where the errored reader stops dispatching.
    expect(calls).toBe(callsBeforeRevoked)
    await advance(120_000)
    expect(calls).toBe(callsBeforeRevoked)
    expect(channel.handshakes).toBe(1)
  })
})
