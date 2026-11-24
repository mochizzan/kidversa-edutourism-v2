/**
 * Session detail loader for the session/assessment/gallery/consent/reports
 * pages. The shared wire types moved to lib/session-contracts (004b), the
 * paged-list/useEventRefetch primitives to lib/list.
 *
 * All realtime wiring is signal + refetch (02§2c) — never a mirror of server
 * state beyond the current view, never polling.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { api } from '../../lib/api'
import { registerRefetch, registerSignal } from '../../lib/stream'
import type { SessionDetail } from '../../lib/session-contracts'

export interface SessionDetailState {
  detail: SessionDetail | null
  error: unknown
  /** true once this session's `session.deleted` frame arrived. */
  deleted: boolean
  reload: () => void
}

/**
 * ONE-fetch session detail (02§2b: session + topics + groups so assessment/
 * gallery/report never waterfall) + SSE refetch on the detail-scoped events.
 * `session.deleted` for THIS id closes the detail (spec 02§2c): the frame
 * carries the deleted session's `entity_id` and the publish is post-commit
 * (deletion certain), so the listener compares ids and never re-fetches
 * (004g — the old NOT_FOUND probe is gone).
 */
export function useSessionDetail(sid: string | undefined): SessionDetailState {
  const [detail, setDetail] = useState<SessionDetail | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [deleted, setDeleted] = useState(false)
  const loadRef = useRef<() => void>(() => {})

  useEffect(() => {
    if (!sid) return
    let alive = true
    setDetail(null)
    setError(null)
    setDeleted(false)
    const load = () => {
      api.get<SessionDetail>(`/api/sessions/${sid}`).then(
        (data) => {
          if (alive) {
            setDetail(data)
            setError(null)
          }
        },
        (failure: unknown) => {
          if (alive) setError(failure)
        },
      )
    }
    loadRef.current = load
    load()
    const offs = ['session.changed', 'session.status', 'group.changed', 'member.changed', 'consent.changed'].map(
      (event) => registerRefetch(event, load),
    )
    const offDeleted = registerSignal<{ entity_id?: number }>('session.deleted', (frame) => {
      if (frame.entity_id !== undefined && frame.entity_id === Number(sid)) setDeleted(true)
    })
    return () => {
      alive = false
      offs.forEach((off) => off())
      offDeleted()
    }
  }, [sid])

  const reload = useCallback(() => loadRef.current(), [])
  return { detail, error, deleted, reload }
}
