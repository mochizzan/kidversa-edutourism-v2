/**
 * Copy-data modal (F-15 / FL-16): candidates default to the server's first
 * entry (FINISHED-first contract), Ya/Tidak actions, copied/skipped counts
 * render from the `{copied,skipped}` envelope, the IN_PROGRESS idem-replay
 * notice, the same-key replay loop; at the retry cap the key is KEPT and Ya
 * stays disabled until `copy.completed` replays it (or the modal closes),
 * empty candidates closing the modal immediately, and the error envelope
 * rendered translated.
 */
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import CopyDataModal from './CopyDataModal'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, ok } from '../../test/handlers'
import { expectNotification, findAlert, queryAlert } from '../../test/toast'
import { chooseOption, openDropdown } from '../../test/helpers.cov-session'
import { emitSignal } from '../../lib/stream'
import type { CopyCandidate } from '../../lib/session-contracts'

const FINISHED: CopyCandidate = {
  session_id: 8,
  session_name: 'Sesi Oktober',
  session_date: '2026-10-01',
  status: 'FINISHED',
  counts: { assessments: 2, narratives: 1, missions: 1, photos: 3 },
}
const OPENED: CopyCandidate = {
  session_id: 9,
  session_name: 'Sesi November',
  session_date: '2026-11-01',
  status: 'OPENED',
  counts: { assessments: 0, narratives: 0, missions: 0, photos: 0 },
}

function renderModal(onClose = vi.fn()) {
  renderApp(<CopyDataModal sid={1} childId={2} childName="Aisyah Putri" onClose={onClose} />)
  return { onClose }
}

const IN_PROGRESS_NOTICE = 'Salinan sedang diproses server. Buka kembali bila hasil belum muncul.'

/* ---------- fake-timer helpers (DashboardPage.test.tsx pattern): only the
   retry clock is faked — MSW/undici need their real macrotasks ---------- */

async function until(condition: () => boolean, step = 5, limit = 4000): Promise<void> {
  for (let i = 0; i < limit && !condition(); i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(step)
    })
  }
  expect(condition()).toBe(true)
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

function fakeRetryClock(): void {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
}

describe('copy data modal', () => {
  it('defaults to the first candidate (FINISHED-first) and switches source', async () => {
    server.use(
      http.get('/api/sessions/1/children/2/copy-candidates', () => ok([FINISHED, OPENED])),
    )
    const { onClose } = renderModal()
    const user = userEvent.setup()

    // Title carries the default (first) source; select shows it selected.
    await screen.findByText('Salin nilai dari Sesi Sesi Oktober?')

    // Switching the source re-titles the modal.
    const select = document.querySelector('.ant-select .ant-select-selection-search-input') as HTMLElement
    await openDropdown(select)
    await chooseOption('Sesi November · 2026-11-01 · Dibuka')
    await screen.findByText('Salin nilai dari Sesi Sesi November?')

    // "Tidak" closes without any request.
    await user.click(screen.getByRole('button', { name: 'Tidak' }))
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('Ya posts the chosen source and renders the copied/skipped counts', async () => {
    let posted: unknown = null
    server.use(
      http.get('/api/sessions/1/children/2/copy-candidates', () => ok([FINISHED])),
      http.post('/api/sessions/1/children/2/copy', async ({ request }) => {
        posted = await request.json()
        return ok({
          copied: { assessments: 2, narratives: 1, missions: 1, photos: 3 },
          skipped: { assessments: 0, narratives: 0, missions: 0, photos: 1 },
        })
      }),
    )
    renderModal()
    const user = userEvent.setup()

    await screen.findByText('Salin nilai dari Sesi Sesi Oktober?')
    await user.click(screen.getByRole('button', { name: 'Ya, salin' }))

    expect(posted).toEqual({ source_session_id: 8 })
    await findAlert('Disalin — penilaian 2, narasi 1, misi 1, foto 3.')
    await screen.findByText('Dilewati (sudah terisi) — penilaian 0, narasi 0, misi 0, foto 1.')
    // Heavy action → the completion card mirrors the copied counts.
    await expectNotification('Data disalin', 'Disalin — penilaian 2, narasi 1, misi 1, foto 3.')
  })

  it('IN_PROGRESS idem replay shows the notice instead of counts', async () => {
    server.use(
      http.get('/api/sessions/1/children/2/copy-candidates', () => ok([FINISHED])),
      http.post('/api/sessions/1/children/2/copy', () => ok({ status: 'IN_PROGRESS' })),
    )
    renderModal()
    const user = userEvent.setup()

    await screen.findByText('Salin nilai dari Sesi Sesi Oktober?')
    await user.click(screen.getByRole('button', { name: 'Ya, salin' }))

    await screen.findByText(IN_PROGRESS_NOTICE)
    expect(queryAlert(/Disalin —/)).toBeNull()
    // The replay timer is armed but the real 2 s clock never ticks this far
    // inside the test — the loop itself is driven below under a fake clock.
  })

  it('replays with the SAME idempotency key every 2 s until the result settles', async () => {
    fakeRetryClock()
    const keys: (string | null)[] = []
    let calls = 0
    server.use(
      http.get('/api/sessions/1/children/2/copy-candidates', () => ok([FINISHED])),
      http.post('/api/sessions/1/children/2/copy', ({ request }) => {
        calls += 1
        keys.push(request.headers.get('X-Idempotency-Key'))
        if (calls === 1) return ok({ status: 'IN_PROGRESS' })
        return ok({
          copied: { assessments: 2, narratives: 1, missions: 1, photos: 3 },
          skipped: { assessments: 0, narratives: 0, missions: 0, photos: 1 },
        })
      }),
    )
    renderModal()

    await until(() => screen.queryByText('Salin nilai dari Sesi Sesi Oktober?') !== null)
    fireEvent.click(screen.getByRole('button', { name: 'Ya, salin' }))

    // Non-final answer → notice up, Ya disabled while the loop can still run.
    await until(() => screen.queryByText(IN_PROGRESS_NOTICE) !== null)
    expect(calls).toBe(1)
    expect(keys[0]).not.toBeNull()
    // The name carries the transient loading icon → match on the label only.
    expect(screen.getByRole('button', { name: /Ya, salin/ })).toBeDisabled()

    // One tick later the SAME key replays and the counts land.
    await advance(2000)
    await until(() => queryAlert(/Disalin — penilaian 2/) !== null)
    expect(calls).toBe(2)
    expect(keys[1]).toBe(keys[0]) // replay, never a fresh run
    expect(screen.queryByText(IN_PROGRESS_NOTICE)).toBeNull() // notice cleared
  })

  it('a failing replay surfaces the error envelope and stops the loop', async () => {
    fakeRetryClock()
    let calls = 0
    server.use(
      http.get('/api/sessions/1/children/2/copy-candidates', () => ok([FINISHED])),
      http.post('/api/sessions/1/children/2/copy', () => {
        calls += 1
        return calls === 1 ? ok({ status: 'IN_PROGRESS' }) : fail('DB_UNAVAILABLE')
      }),
    )
    renderModal()

    await until(() => screen.queryByText('Salin nilai dari Sesi Sesi Oktober?') !== null)
    fireEvent.click(screen.getByRole('button', { name: 'Ya, salin' }))
    await until(() => screen.queryByText(IN_PROGRESS_NOTICE) !== null)

    await advance(2000)
    await until(() => screen.queryByText('Layanan sedang sibuk. Coba lagi.') !== null)
    expect(calls).toBe(2)
    expect(screen.queryByText(IN_PROGRESS_NOTICE)).toBeNull() // definitive failure → no notice
    expect(queryAlert(/Disalin —/)).toBeNull()
  })

  /** IN_PROGRESS until the 16-call cap, then the final counts. */
  function capForeverServer() {
    const keys: (string | null)[] = []
    let calls = 0
    server.use(
      http.get('/api/sessions/1/children/2/copy-candidates', () => ok([FINISHED])),
      http.post('/api/sessions/1/children/2/copy', ({ request }) => {
        calls += 1
        keys.push(request.headers.get('X-Idempotency-Key'))
        if (calls <= 16) return ok({ status: 'IN_PROGRESS' })
        return ok({
          copied: { assessments: 2, narratives: 1, missions: 1, photos: 3 },
          skipped: { assessments: 0, narratives: 0, missions: 0, photos: 1 },
        })
      }),
    )
    return { keys, callCount: () => calls }
  }

  /** Clicks Ya and advances the fake clock to the 16-call cap. */
  async function driveToCap(callCount: () => number): Promise<void> {
    await until(() => screen.queryByText('Salin nilai dari Sesi Sesi Oktober?') !== null)
    fireEvent.click(screen.getByRole('button', { name: 'Ya, salin' }))
    await until(() => screen.queryByText(IN_PROGRESS_NOTICE) !== null)
    // 1 initial + 15 idem replays = the cap.
    for (let i = 2; i <= 16; i += 1) {
      await advance(2000)
      await until(() => callCount() === i)
    }
  }

  it('the retry cap KEEPS the key with Ya disabled until copy.completed settles it (F-F09)', async () => {
    fakeRetryClock()
    const { keys, callCount } = capForeverServer()
    renderModal()
    await driveToCap(callCount)

    // Cap: the key is KEPT and "Ya" stays disabled — a fresh-key run could
    // double-send while the first job may still be running.
    expect(callCount()).toBe(16)
    expect(screen.getByRole('button', { name: /Ya, salin/ })).toBeDisabled()
    expect(screen.getByText(IN_PROGRESS_NOTICE)).toBeInTheDocument()

    // Another child's copy.completed is ignored (no replay fired).
    act(() => emitSignal('copy.completed', { child_id: 99, rev: 1 }))
    await act(async () => {
      await Promise.resolve()
    })
    expect(callCount()).toBe(16)

    // This child's copy.completed → one same-key replay fetches the counts.
    act(() => emitSignal('copy.completed', { child_id: 2, rev: 1 }))
    await until(() => queryAlert(/Disalin — penilaian 2/) !== null)
    expect(callCount()).toBe(17)
    expect(keys[16]).toBe(keys[0]) // still the ORIGINAL key
    expect(screen.queryByText(IN_PROGRESS_NOTICE)).toBeNull()

    // Settled → a resubmit is a fresh run with a FRESH key.
    fireEvent.click(screen.getByRole('button', { name: /Ya, salin/ }))
    await until(() => callCount() === 18)
    expect(keys[17]).not.toBe(keys[0])
  })

  it('closing at the cap resets the run so a reopen mints a fresh key (F-F09)', async () => {
    fakeRetryClock()
    const { keys, callCount } = capForeverServer()
    // Parent-style harness: close unmounts the modal, reopen mounts it fresh.
    function Harness() {
      const [open, setOpen] = useState(false)
      return (
        <>
          <button onClick={() => setOpen(true)}>buka lagi</button>
          {open ? (
            <CopyDataModal
              sid={1}
              childId={2}
              childName="Aisyah Putri"
              onClose={() => setOpen(false)}
            />
          ) : null}
        </>
      )
    }
    renderApp(<Harness />)

    await until(() => screen.queryByText('buka lagi') !== null)
    fireEvent.click(screen.getByRole('button', { name: 'buka lagi' }))
    await driveToCap(callCount)
    expect(screen.getByRole('button', { name: /Ya, salin/ })).toBeDisabled()

    // Close at the cap: allowed (close is never blocked) and resets everything.
    fireEvent.click(screen.getByRole('button', { name: 'Tidak' }))
    await until(() => screen.queryByText('Salin nilai dari Sesi Sesi Oktober?') === null)

    fireEvent.click(screen.getByRole('button', { name: 'buka lagi' }))
    await until(() => screen.queryByText('Salin nilai dari Sesi Sesi Oktober?') !== null)
    fireEvent.click(screen.getByRole('button', { name: 'Ya, salin' }))
    await until(() => callCount() === 17)
    expect(keys[16]).not.toBe(keys[0]) // fresh mount → fresh key
  })

  it('closes immediately when there are no candidates', async () => {
    server.use(http.get('/api/sessions/1/children/2/copy-candidates', () => ok([])))
    const { onClose } = renderModal()

    await vi.waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    // The modal owns its open state; with a mock onClose it simply stays —
    // the parent unmounts it in production (the flow continues elsewhere).
  })

  it('renders the error envelope translated', async () => {
    server.use(
      http.get('/api/sessions/1/children/2/copy-candidates', () =>
        HttpResponse.json({ error: { code: 'NOT_FOUND', message: 'gone' } }),
      ),
    )
    renderModal()

    await screen.findByText('Data tidak ditemukan.')
  })
})
