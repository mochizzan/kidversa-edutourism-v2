/**
 * Shared contract unit tests (004b): the P prefix builder, RFC3339
 * formatting, consent label mapping, clipboard copy feedback (now in
 * lib/session-contracts), the useEventRefetch registration rules (now in
 * lib/list) — latest callback wins, unregisters on unmount — and the
 * useSessionDetail loader (missing-sid guard, error + reload recovery,
 * malformed session.deleted frames ignored).
 */
import { describe, expect, it, vi } from 'vitest'
import { act, render, waitFor } from '@testing-library/react'
import { createElement } from 'react'
import { http, HttpResponse } from 'msw'
import { emitSignal } from '../../lib/stream'
import { useEventRefetch } from '../../lib/list'
import {
  consentStatusLabel,
  copyText,
  fmtDateTime,
  pPrefix,
} from '../../lib/session-contracts'
import i18n from '../../lib/i18n'
import { ok } from '../../test/handlers'
import { server } from '../../test/setup'
import { detailFixture, stubClipboard } from '../../test/helpers.cov-session'
import { useSessionDetail } from './shared'

describe('session shared helpers', () => {
  it('builds the per-child-per-topic binding prefix', () => {
    expect(pPrefix(1, 3, 2)).toBe('/api/sessions/1/topics/3/children/2')
  })

  it('formats RFC3339 datetimes and stays empty-safe', () => {
    const iso = '2026-10-09T10:00:00.000Z'
    expect(fmtDateTime(iso)).toBe(new Date(iso).toLocaleString())
    expect(fmtDateTime(null)).toBe('—')
    expect(fmtDateTime(undefined)).toBe('—')
    expect(fmtDateTime('not-a-date')).toBe('not-a-date')
  })

  it('maps consent statuses to translated labels', () => {
    const t = i18n.getFixedT('id', 'session')
    expect(consentStatusLabel(t, 'SETUJU')).toBe('Disetujui')
    expect(consentStatusLabel(t, 'DITOLAK')).toBe('Ditolak')
    expect(consentStatusLabel(t, 'PENDING')).toBe('Menunggu')
    expect(consentStatusLabel(t, 'WEIRD')).toBe('Menunggu')
  })

  it('reports clipboard copy success and failure', async () => {
    const okClipboard = stubClipboard([true, false])
    expect(await copyText('https://wa.id/v1/consent/2')).toBe(true)
    expect(await copyText('https://wa.id/v1/consent/3')).toBe(false)
    expect(okClipboard.writes).toEqual([
      'https://wa.id/v1/consent/2',
      'https://wa.id/v1/consent/3',
    ])
  })
})

function RefetchProbe({ onRefetch }: { onRefetch: () => void }) {
  useEventRefetch(['probe.a', 'probe.b'], onRefetch)
  return null
}

describe('useEventRefetch', () => {
  it('routes every listed event to the LATEST callback and unregisters on unmount', () => {
    const first = vi.fn()
    const { rerender, unmount } = render(createElement(RefetchProbe, { onRefetch: first }))

    emitSignal('probe.a', { rev: 1 })
    emitSignal('probe.b', { rev: 2 })
    expect(first).toHaveBeenCalledTimes(2)

    // A re-render swaps the callback; events must hit the NEW one only.
    const second = vi.fn()
    rerender(createElement(RefetchProbe, { onRefetch: second }))
    emitSignal('probe.a', { rev: 3 })
    expect(second).toHaveBeenCalledTimes(1)
    expect(first).toHaveBeenCalledTimes(2)

    // Unmount unregisters: later frames reach nobody.
    unmount()
    emitSignal('probe.b', { rev: 4 })
    expect(second).toHaveBeenCalledTimes(1)
  })
})

interface DetailProbeState {
  detail: unknown
  error: unknown
  deleted: boolean
  reload: () => void
}

function DetailProbe({
  sid,
  onChange,
}: {
  sid: string | undefined
  onChange: (state: DetailProbeState) => void
}) {
  const state = useSessionDetail(sid)
  onChange({ detail: state.detail, error: state.error, deleted: state.deleted, reload: state.reload })
  return null
}

describe('useSessionDetail', () => {
  it('skips fetching entirely while sid is missing', async () => {
    let requests = 0
    server.use(
      http.get('/api/sessions/:sid', () => {
        requests += 1
        return ok(detailFixture())
      }),
    )
    const seen: DetailProbeState[] = []
    render(createElement(DetailProbe, { sid: undefined, onChange: (state) => seen.push(state) }))

    await waitFor(() => expect(seen.length).toBeGreaterThan(0))
    expect(seen.at(-1)).toMatchObject({ detail: null, error: null, deleted: false })
    expect(requests).toBe(0)
  })

  it('exposes the fetch failure; reload() re-fetches and clears it', async () => {
    let failNext = true
    server.use(
      http.get('/api/sessions/1', () => {
        if (failNext) {
          return HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'gone' } })
        }
        return ok(detailFixture())
      }),
    )
    const seen: DetailProbeState[] = []
    render(createElement(DetailProbe, { sid: '1', onChange: (state) => seen.push(state) }))

    await waitFor(() => expect(seen.at(-1)?.error).toMatchObject({ code: 'NOT_FOUND' }))

    // The page's retry path: reload() re-runs the SAME fetch; a success
    // clears the error and lands the detail.
    failNext = false
    act(() => seen.at(-1)?.reload())
    await waitFor(() => expect(seen.at(-1)?.detail).not.toBeNull())
    expect(seen.at(-1)?.error).toBeNull()
  })

  it('a session.deleted frame without entity_id never closes the detail', async () => {
    server.use(http.get('/api/sessions/1', () => ok(detailFixture())))
    const seen: DetailProbeState[] = []
    render(createElement(DetailProbe, { sid: '1', onChange: (state) => seen.push(state) }))

    await waitFor(() => expect(seen.at(-1)?.detail).not.toBeNull())

    // Malformed frame: no entity_id → no match → stays open.
    act(() => emitSignal('session.deleted', { rev: 1 }))
    await waitFor(() => expect(seen.at(-1)?.detail).not.toBeNull())
    expect(seen.at(-1)?.deleted).toBe(false)
  })

  it('unmounting before the detail resolves leaves state untouched (alive guards)', async () => {
    const gate = Promise.withResolvers<Response>()
    server.use(http.get('/api/sessions/1', () => gate.promise))
    const seen: DetailProbeState[] = []
    const probe = render(
      createElement(DetailProbe, { sid: '1', onChange: (state) => seen.push(state) }),
    )

    // Unmount while the fetch is in flight — the late SUCCESS and the refetch
    // registrations must all hit the alive=false guards, not setState.
    probe.unmount()
    gate.resolve(ok(detailFixture()))
    await act(async () => {
      await Promise.resolve()
    })
    expect(seen.at(-1)?.detail).toBeNull()
  })
})
