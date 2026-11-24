/**
 * Session report matrix + batch WA send (F-12 / FL-13): the matrix renders,
 * chunks are ≤10 per request, per-item SENT/FAILED merge into the result
 * table with translated codes + copy fallbacks, whole-chunk failures mark
 * their items and stop, and retry re-sends ONLY the failed rows — an item
 * set that never got a per-item response replays under its ORIGINAL
 * idempotency key (06§3 same-key replay), never-posted sets mint fresh keys.
 */
import { beforeEach, describe, expect, it } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import BatchSendPanel from './BatchSendPanel'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok } from '../../test/handlers'
import { expectNotification, findAlert } from '../../test/toast'
import { reportsRow, stubClipboard } from '../../test/helpers.cov-session'
import type { ReportsRow } from '../../lib/session-contracts'

interface BatchCapture {
  chunkSizes: number[]
  idemKeys: string[]
  bodies: Array<Array<{ child_id: number; session_topic_id: number }>>
}

function batchHandler(capture: BatchCapture, respond: (chunk: BatchCapture['bodies'][number], call: number) => Response) {
  return http.post('/api/sessions/1/batch-send', async ({ request }) => {
    const body = (await request.json()) as { items: BatchCapture['bodies'][number] }
    capture.chunkSizes.push(body.items.length)
    capture.bodies.push(body.items)
    capture.idemKeys.push(request.headers.get('X-Idempotency-Key') ?? '')
    return respond(body.items, capture.chunkSizes.length)
  })
}

function newCapture(): BatchCapture {
  return { chunkSizes: [], idemKeys: [], bodies: [] }
}

function matrixHandler(rows: ReportsRow[]) {
  return http.get('/api/sessions/1/reports', () => ok(rows))
}

function manyRows(count: number): ReportsRow[] {
  return Array.from({ length: count }, (_unused, index) =>
    reportsRow(index + 1, `Anak ${index + 1}`, 3, {
      report_url: index % 2 === 0 ? `/view/report?token=t${index + 1}` : null,
    }),
  )
}

/** SENT for even child ids, FAILED(WA_FAILED) for odds. */
function alternatingRespond(chunk: BatchCapture['bodies'][number]) {
  return ok({
    items: chunk.map((item) => ({
      child_id: item.child_id,
      session_topic_id: item.session_topic_id,
      status: item.child_id % 2 === 0 ? 'SENT' : 'FAILED',
      ...(item.child_id % 2 === 0 ? {} : { code: 'WA_FAILED' }),
    })),
  })
}

describe('batch send panel', () => {
  beforeEach(() => {
    // The pending per-chunk idem map persists per session (06§3) — start clean.
    sessionStorage.clear()
  })

  it('renders the matrix rows and the empty selection hint', async () => {
    server.use(matrixHandler([reportsRow(2, 'Aisyah Putri'), reportsRow(3, 'Budi Santoso')]))
    renderApp(<BatchSendPanel sid="1" />)

    await screen.findByText('Aisyah Putri')
    screen.getByText('Budi Santoso')
    expect(screen.getAllByText('Penilaian lengkap')).toHaveLength(2)
    screen.getByText('Pilih minimal satu baris.')
    expect(screen.getByRole('button', { name: /Kirim$/ })).toBeDisabled()
  })

  it('an empty matrix response renders the Ant Design empty state, never a stuck loading state', async () => {
    // Zero report records (e.g. a session with no members): the empty array
    // settles the panel into the Table's standard empty state.
    server.use(matrixHandler([]))
    renderApp(<BatchSendPanel sid="1" />)

    expect(await screen.findByText('Tidak ada data')).toBeInTheDocument()
    expect(screen.getByText('Kirim massal')).toBeInTheDocument()
  })

  it('a null data payload normalizes to an empty list instead of loading forever', async () => {
    // Defensive: a `{"data":null}` envelope (Go nil-slice serialization) must
    // not pin the Table in `loading` — normalize to [] and render empty.
    server.use(
      http.get('/api/sessions/1/reports', () => HttpResponse.json({ data: null })),
    )
    renderApp(<BatchSendPanel sid="1" />)

    expect(await screen.findByText('Tidak ada data')).toBeInTheDocument()
    expect(screen.getByText('Kirim massal')).toBeInTheDocument()
  })

  it('chunks ≤10 per request, merges per-item results, and retry of never-posted sets mints fresh keys', async () => {
    const capture = newCapture()
    const rows = manyRows(23)
    server.use(
      matrixHandler(rows),
      batchHandler(capture, alternatingRespond),
    )
    const user = userEvent.setup()
    renderApp(<BatchSendPanel sid="1" />)

    await screen.findByText('Anak 1')
    // Select all 23 via the header checkbox (fireEvent: antd hides the input
    // under pointer-events:none and forwards label clicks itself).
    fireEvent.click(screen.getAllByRole('checkbox')[0])
    await screen.findByText('23 baris dipilih')

    await user.click(screen.getByRole('button', { name: /Kirim$/ }))
    await findAlert(/terkirim, \d+ gagal\./)

    // Chunk contract: 10 + 10 + 3, sequential.
    expect(capture.chunkSizes).toEqual([10, 10, 3])
    expect(capture.idemKeys.every((key) => /^[0-9a-f-]{36}$/.test(key))).toBe(true)
    expect(new Set(capture.idemKeys).size).toBe(3)

    // Per-item results: 11 SENT (even ids 2..22), 12 FAILED (odds).
    const done = await findAlert('11 terkirim, 12 gagal.')
    expect(done).toBeInTheDocument()
    // Heavy action → one result card, not a transient toast.
    await expectNotification('Pengiriman batch selesai', '11 terkirim, 12 gagal.')
    // The result table shows the translated failure code + copy fallback.
    screen.getAllByText('WhatsApp gagal mengirim. Coba lagi atau salin URL manual.')
    const fallback = screen.getAllByRole('button', { name: 'Salin URL rapor' })[0]
    const clipboard = stubClipboard()
    await user.click(fallback)
    await screen.findByText('Tersalin')
    expect(clipboard.writes[0]).toMatch(/\/view\/report\?token=t/)

    // Retry: only the FAILED rows go out, with NEW idempotency keys.
    const firstRunKeys = [...capture.idemKeys]
    await user.click(screen.getByRole('button', { name: 'Ulangi yang gagal' }))
    await waitFor(() => expect(capture.chunkSizes.length).toBe(5))

    const retryItems = capture.bodies.slice(3).flat()
    expect(retryItems).toHaveLength(12)
    expect(retryItems.every((item) => item.child_id % 2 === 1)).toBe(true)
    const retryKeys = capture.idemKeys.slice(3)
    expect(retryKeys.some((key) => firstRunKeys.includes(key))).toBe(false)
  })

  it('a whole-chunk envelope failure marks its items, stops the loop, and retries under the SAME key', async () => {
    const capture = newCapture()
    const rows = manyRows(12)
    server.use(
      matrixHandler(rows),
      batchHandler(capture, (chunk, call) =>
        // Only the FIRST run's second chunk is refused; the same-key retry succeeds.
        call === 2
          ? HttpResponse.json({ error: { code: 'RATE_LIMITED', message: 'slow' } })
          : ok({
              items: chunk.map((item) => ({
                child_id: item.child_id,
                session_topic_id: item.session_topic_id,
                status: 'SENT',
              })),
            }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<BatchSendPanel sid="1" />)

    await screen.findByText('Anak 1')
    // Select all 12 via the header checkbox (fireEvent: antd hides the input
    // under pointer-events:none and forwards label clicks itself).
    fireEvent.click(screen.getAllByRole('checkbox')[0])
    await screen.findByText('12 baris dipilih')
    await user.click(screen.getByRole('button', { name: /Kirim$/ }))

    // Second chunk refused: its items FAILED, chunk alert shown, loop stops.
    await screen.findAllByText('Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.')
    await findAlert('10 terkirim, 2 gagal.')
    expect(capture.chunkSizes).toEqual([10, 2])

    // The failed set got NO per-item response → its key stays pending in
    // sessionStorage (06§3 crash survival) under the original chunk key.
    const pending = JSON.parse(sessionStorage.getItem('kv-batch-idem:1') ?? '[]') as Array<
      [string, string]
    >
    expect(pending).toHaveLength(1)
    expect(pending[0][1]).toBe(capture.idemKeys[1])

    // Chunk-level retry replays the identical item set under the SAME key.
    // (The button is briefly `loading` while busy settles.)
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /Ulangi yang gagal/ })).toBeEnabled(),
    )
    await user.click(screen.getByRole('button', { name: /Ulangi yang gagal/ }))
    await findAlert('12 terkirim, 0 gagal.')
    expect(capture.chunkSizes).toEqual([10, 2, 2])
    expect(capture.idemKeys[2]).toBe(capture.idemKeys[1])
    // Settled → the pending map is empty again.
    expect(JSON.parse(sessionStorage.getItem('kv-batch-idem:1') ?? '[]')).toHaveLength(0)
  })

  it('renders the matrix cell variants: incomplete, no-narrative, photo-less', async () => {
    // Cell renderers: readiness tag both ways, narrative yes/no, candidate
    // photo null → em dash (and false → no).
    const rows = [
      reportsRow(2, 'Aisyah Putri', 3, { complete: false, narrative: false, candidate_photo: null }),
      reportsRow(3, 'Budi Santoso', 3, { complete: true, narrative: true, candidate_photo: false }),
    ]
    server.use(matrixHandler(rows))
    renderApp(<BatchSendPanel sid="1" />)

    await screen.findByText('Aisyah Putri')
    const aisyah = screen.getByText('Aisyah Putri').closest('tr') as HTMLElement
    expect(within(aisyah).getAllByText('Belum')).toHaveLength(2) // not ready + no narrative
    const budi = screen.getByText('Budi Santoso').closest('tr') as HTMLElement
    within(budi).getByText('Penilaian lengkap')
    within(budi).getByText('Ya')
    // candidate_photo null → em dash (the row's link cell also has none).
    expect(within(aisyah).getAllByText('—').length).toBeGreaterThanOrEqual(1)
  })

  it('a fully successful batch renders the success banner, not the warning', async () => {
    const capture = newCapture()
    const rows = [reportsRow(2, 'Aisyah Putri'), reportsRow(3, 'Budi Santoso')]
    server.use(
      matrixHandler(rows),
      batchHandler(capture, (chunk) =>
        ok({
          items: chunk.map((item) => ({
            child_id: item.child_id,
            session_topic_id: item.session_topic_id,
            status: 'SENT' as const,
          })),
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<BatchSendPanel sid="1" />)

    await screen.findByText('Aisyah Putri')
    fireEvent.click(screen.getAllByRole('checkbox')[0])
    await screen.findByText('2 baris dipilih')
    await user.click(screen.getByRole('button', { name: /Kirim$/ }))

    // failedCount === 0 → the green success banner (not the warning one).
    const done = await findAlert('2 terkirim, 0 gagal.')
    expect(done.closest('.ant-alert')?.className).toContain('ant-alert-success')
    expect(screen.queryByText(/gagal mengirim/)).toBeNull()
  })

  it('a FAILED item without a code falls back to WA_FAILED', async () => {
    const capture = newCapture()
    const rows = [reportsRow(2, 'Aisyah Putri')]
    server.use(
      matrixHandler(rows),
      batchHandler(capture, (chunk) =>
        ok({
          items: chunk.map((item) => ({
            child_id: item.child_id,
            session_topic_id: item.session_topic_id,
            status: 'FAILED' as const,
            // no `code` → the renderer must default to WA_FAILED
          })),
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<BatchSendPanel sid="1" />)

    await screen.findByText('Aisyah Putri')
    fireEvent.click(screen.getAllByRole('checkbox')[0])
    await screen.findByText('1 baris dipilih')
    await user.click(screen.getByRole('button', { name: /Kirim$/ }))

    await screen.findByText('WhatsApp gagal mengirim. Coba lagi atau salin URL manual.')
  })

  it('a denied clipboard copy toasts INTERNAL', async () => {
    const rows = [reportsRow(2, 'Aisyah Putri', 3, { report_url: '/view/report?token=rt' })]
    server.use(
      matrixHandler(rows),
      batchHandler(newCapture(), (chunk) =>
        ok({
          items: chunk.map((item) => ({
            child_id: item.child_id,
            session_topic_id: item.session_topic_id,
            status: 'FAILED' as const,
            code: 'WA_FAILED',
          })),
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<BatchSendPanel sid="1" />)

    await screen.findByText('Aisyah Putri')
    fireEvent.click(screen.getAllByRole('checkbox')[0])
    await user.click(screen.getByRole('button', { name: /Kirim$/ }))
    await screen.findByText('WhatsApp gagal mengirim. Coba lagi atau salin URL manual.')

    stubClipboard([false]) // navigator.clipboard.writeText rejects
    await user.click(screen.getByRole('button', { name: 'Salin URL rapor' }))
    await screen.findByText('Terjadi kesalahan tak terduga. Coba lagi.')
  })

  it('a FAILED row offers the gallery fallback copy beside the report one', async () => {
    // One failure row carries BOTH urls → both fallback buttons render and
    // the G button copies the gallery link (the report one is covered above).
    const rows = [reportsRow(2, 'Aisyah Putri', 3, { report_url: '/view/report?token=rt', gallery_url: '/view/gallery?token=gt' })]
    server.use(
      matrixHandler(rows),
      batchHandler(newCapture(), (chunk) =>
        ok({
          items: chunk.map((item) => ({
            child_id: item.child_id,
            session_topic_id: item.session_topic_id,
            status: 'FAILED' as const,
            code: 'WA_FAILED',
          })),
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<BatchSendPanel sid="1" />)

    await screen.findByText('Aisyah Putri')
    fireEvent.click(screen.getAllByRole('checkbox')[0])
    await screen.findByText('1 baris dipilih')
    await user.click(screen.getByRole('button', { name: /Kirim$/ }))

    await screen.findByText('WhatsApp gagal mengirim. Coba lagi atau salin URL manual.')
    const clipboard = stubClipboard()
    await user.click(screen.getByRole('button', { name: 'Salin URL galeri' }))
    await screen.findByText('Tersalin')
    expect(clipboard.writes[0]).toBe('/view/gallery?token=gt')
  })

  it('renders the matrix error translated with a retry', async () => {
    let calls = 0
    server.use(
      http.get('/api/sessions/1/reports', () => {
        calls += 1
        if (calls === 1) {
          return HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } })
        }
        return ok([reportsRow(2, 'Aisyah Putri')])
      }),
    )
    const user = userEvent.setup()
    renderApp(<BatchSendPanel sid="1" />)

    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
    await user.click(screen.getByRole('button', { name: 'Coba lagi' }))
    await screen.findByText('Aisyah Putri')
    expect(calls).toBe(2)
  })
})
