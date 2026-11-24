/**
 * Copy-data modal (F-15 / FL-16): "Salin nilai dari Sesi X?" for ONE child in
 * the target session. Candidates come FINISHED-first then date DESC (server
 * order — first option is the default); explicit Ya/Tidak buttons; the result
 * shows copied/skipped counts and the modal stays re-runnable for another
 * source (fill-empty is idempotent server-side).
 *
 * IN_PROGRESS (B10 / 06§3): the copy runs server-side, so a non-final answer
 * re-POSTs with the SAME `X-Idempotency-Key` every 2 s (bounded by a retry
 * cap) until the `{copied,skipped}` result or a definitive error settles it.
 * At the cap the key is KEPT and "Ya" stays disabled — a fresh-key run could
 * double-send while the first job still runs; the job finishing publishes
 * `copy.completed` (child-scoped) which replays the kept key once for the
 * final counts, and closing the modal resets everything (fresh mount).
 */
import { useEffect, useRef, useState } from 'react'
import { Alert, Modal, Select, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { useFeedback } from '../../lib/feedback'
import { registerSignal } from '../../lib/stream'
import type { CopyCandidate, CopyResult } from '../../lib/session-contracts'

/** Idem-replay cadence: re-POST the same key until the server settles the copy. */
const RETRY_MS = 2000
/** Replay cap (~30 s): hand control back with the "still processing" notice. */
const MAX_RETRIES = 15

export interface CopyDataModalProps {
  sid: number | string
  childId: number
  childName: string
  onClose: () => void
}

/** Contract boundary: copy response is `{copied, skipped}` vs `{status:"IN_PROGRESS"}`. */
function isCopyResult(data: unknown): data is CopyResult {
  if (typeof data !== 'object' || data === null) return false
  return 'copied' in data && 'skipped' in data
}

export default function CopyDataModal({ sid, childId, childName, onClose }: CopyDataModalProps) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const feedback = useFeedback()
  const [candidates, setCandidates] = useState<CopyCandidate[]>([])
  const [sourceId, setSourceId] = useState<number | null>(null)
  const [result, setResult] = useState<CopyResult | null>(null)
  const [notice, setNotice] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  /** true while an idem-replay timer is armed (Ya disabled, close allowed). */
  const [polling, setPolling] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const closeRef = useRef(onClose)
  const idemKeyRef = useRef<string | null>(null)
  const retryTimerRef = useRef<number | undefined>(undefined)
  const retriesRef = useRef(0)
  /** The card fires once per run, even if a replay lands the final counts. */
  const announcedRef = useRef(false)
  const aliveRef = useRef(true)
  const busyRef = useRef(false)
  const sourceIdRef = useRef<number | null>(null)
  useEffect(() => {
    closeRef.current = onClose
  })
  useEffect(() => {
    busyRef.current = busy
  })
  useEffect(() => {
    sourceIdRef.current = sourceId
  })

  // Unmount (queue hand-over / close) drops any armed idem-replay timer.
  useEffect(() => {
    return () => {
      aliveRef.current = false
      clearTimeout(retryTimerRef.current)
    }
  }, [])

  // 06§3 cap: the copy finishing publishes `copy.completed` (child-scoped) —
  // replay once under the kept key to fetch the final counts. Idempotent by
  // construction, so it can never start a second job; a run with an armed
  // timer or an in-flight POST is left to its own loop. `postCopy` reads only
  // refs + stable setters; childId is stable per mount.
  useEffect(() => {
    const off = registerSignal<{ child_id?: number }>('copy.completed', (data) => {
      if (Number(data.child_id) !== Number(childId)) return
      if (idemKeyRef.current === null || retryTimerRef.current !== undefined || busyRef.current) {
        return
      }
      void postCopy()
    })
    return off
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [childId])

  useEffect(() => {
    let alive = true
    api
      .get<CopyCandidate[]>(`/api/sessions/${sid}/children/${childId}/copy-candidates`)
      .then(
        (list) => {
          if (!alive) return
          if (list.length === 0) {
            closeRef.current() // cross-program/never elsewhere → no modal
            return
          }
          setCandidates(list)
          setSourceId(list[0].session_id)
        },
        (failure: unknown) => {
          if (alive) setError(failure)
        },
      )
    return () => {
      alive = false
    }
  }, [sid, childId])

  const source = candidates.find((candidate) => candidate.session_id === sourceId) ?? null

  /** One idem-keyed POST; a non-final answer re-arms the same-key replay. */
  async function postCopy(): Promise<void> {
    try {
      const data = await api.post<unknown>(
        `/api/sessions/${sid}/children/${childId}/copy`,
        { source_session_id: sourceIdRef.current },
        { idempotencyKey: idemKeyRef.current ?? undefined },
      )
      if (isCopyResult(data)) {
        idemKeyRef.current = null
        setPolling(false)
        setNotice(null)
        setResult(data)
        if (!announcedRef.current) {
          announcedRef.current = true
          feedback.heavy(
            t('copy.doneTitle'),
            t('copy.copied', {
              assessments: data.copied.assessments,
              narratives: data.copied.narratives,
              missions: data.copied.missions,
              photos: data.copied.photos,
            }),
          )
        }
      } else {
        // `{status:'IN_PROGRESS'}` — the copy is still running server-side.
        setNotice(t('copy.inProgress'))
        scheduleRetry()
      }
    } catch (failure: unknown) {
      idemKeyRef.current = null // definitive failure → the next submit is a fresh run
      setPolling(false)
      setNotice(null)
      setError(failure)
    } finally {
      setBusy(false)
    }
  }

  /** Re-POST with the SAME key every RETRY_MS until the copy settles (cap: MAX_RETRIES). */
  function scheduleRetry(): void {
    if (!aliveRef.current) return // unmounted while the answer was in flight
    if (retriesRef.current >= MAX_RETRIES) {
      // Cap reached (06§3): KEEP the idem key and the "still processing"
      // notice — "Ya" stays disabled so a later click can never start a
      // fresh-key run while the first job may still be running. Completion
      // arrives via `copy.completed` (child-scoped) → one same-key replay
      // fetches the final counts; closing the modal resets everything.
      setPolling(true)
      return
    }
    retriesRef.current += 1
    clearTimeout(retryTimerRef.current) // at most ONE armed replay at a time
    setPolling(true)
    retryTimerRef.current = window.setTimeout(() => {
      retryTimerRef.current = undefined
      void postCopy()
    }, RETRY_MS)
  }

  const runCopy = async () => {
    if (sourceId === null) return // Ya is also disabled while `polling`
    setBusy(true)
    setError(null)
    setNotice(null)
    announcedRef.current = false // a new run earns a fresh completion card
    idemKeyRef.current ??= crypto.randomUUID() // one key per run → replay, never a new job
    retriesRef.current = 0
    await postCopy()
  }

  return (
    <Modal
      open
      title={t('copy.title', { name: source?.session_name ?? '' })}
      okText={t('copy.yes')}
      cancelText={result ? tc('actions.close') : t('copy.no')}
      okButtonProps={{ loading: busy, disabled: sourceId === null || polling }}
      cancelButtonProps={{ disabled: busy }}
      maskClosable={!busy}
      onOk={runCopy}
      onCancel={onClose}
    >
      <Space direction="vertical" style={{ width: '100%' }}>
        <p>
          {t('consent.child')}: {childName}
        </p>
        <Select
          style={{ width: '100%' }}
          value={sourceId ?? undefined}
          onChange={setSourceId}
          disabled={busy || polling}
          placeholder={t('copy.sourcePlaceholder')}
          options={candidates.map((candidate) => ({
            value: candidate.session_id,
            label: `${candidate.session_name} · ${candidate.session_date} · ${tc(
              `status.${candidate.status.toLowerCase()}`,
            )}`,
          }))}
        />
        <Typography.Text type="secondary">{t('copy.hint')}</Typography.Text>
        {notice ? <Alert type="info" showIcon message={notice} /> : null}
        {error ? (
          <Alert
            type="error"
            showIcon
            message={tc(`errors.${errorCode(error)}`)}
          />
        ) : null}
        {result ? (
          <>
            <Alert
              type="success"
              showIcon
              message={t('copy.copied', {
                assessments: result.copied.assessments,
                narratives: result.copied.narratives,
                missions: result.copied.missions,
                photos: result.copied.photos,
              })}
            />
            <Alert
              type="info"
              showIcon
              message={t('copy.skipped', {
                assessments: result.skipped.assessments,
                narratives: result.skipped.narratives,
                missions: result.skipped.missions,
                photos: result.skipped.photos,
              })}
            />
          </>
        ) : null}
      </Space>
    </Modal>
  )
}
