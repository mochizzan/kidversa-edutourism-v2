/**
 * Assessment page `/assessment/:sid/:cid/:stid` (F-09 / FL-09).
 *
 * One session-detail fetch (topics + status) + `GET P/assessments` +
 * `GET P/readiness`. Per-activity card: StarInput (BB/MB/BSH/BSB) auto-saves
 * on change, the note saves via its own button (≤2000 chars) — partial saves
 * are fine, each card carries its own saving/saved/error state. Progress
 * = starred/total per topic (antd Progress steps).
 *
 * Read-only banners: DRAFT → "not opened yet", FINISHED → finished banner,
 * both via `SESSION_LOCKED` server-side too. FAS foreign child → NOT_OWNER
 * (thumbnail-only: header + banner, inputs disabled). Inactive child banner.
 * Draft-notice (06 §5): while any card holds input that differs from its
 * last saved baseline, a `beforeunload` warning (plus a small hint) is armed.
 * SSE: `assessment.changed` + `copy.completed` refetch assessments/readiness;
 * `readiness.changed {child_id, stid, complete}` renders directly (the
 * full-value exception — no refetch on it alone).
 */
import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Alert, Button, Card, Input, Progress, Space, Tabs, Typography } from 'antd'
import { CheckOutlined, DashOutlined } from '@ant-design/icons'
import { useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import StarInput from '../../components/StarInput'
import { api, errorCode, ApiError } from '../../lib/api'
import { useCrumbName } from '../../lib/crumbs'
import { useErrorToast } from '../../lib/errors'
import { useEventRefetch } from '../../lib/list'
import { MAX_NOTE } from '../../lib/validation'
import { registerSignal } from '../../lib/stream'
import { pPrefix, type AssessmentRow } from '../../lib/session-contracts'
import { useSessionDetail } from '../sessions/shared'

interface CardValue {
  stars: number | null
  note: string
}

export default function AssessmentPage() {
  const { t } = useTranslation('assessment')
  const { t: tc } = useTranslation('common')
  const toast = useErrorToast()
  const navigate = useNavigate()
  const { sid, cid, stid } = useParams()
  const childId = Number(cid)
  const topicId = Number(stid)

  const { detail, error: detailError, reload: reloadDetail } = useSessionDetail(sid)
  // The breadcrumb bar shows the session's name instead of "Detail sesi".
  useCrumbName('sid', detail?.session.name)
  const [childName, setChildName] = useState('')
  const [childInactive, setChildInactive] = useState(false)
  const [rows, setRows] = useState<AssessmentRow[] | null>(null)
  const [listError, setListError] = useState<unknown>(null)
  const [notOwner, setNotOwner] = useState(false)
  const [ready, setReady] = useState<boolean | null>(null)
  const [values, setValues] = useState<Record<number, CardValue>>({})
  const [saveState, setSaveState] = useState<Record<number, 'saving' | 'saved' | 'error'>>({})
  // Last PERSISTED value per card (loadAll adoption + save success) — the
  // draft-notice baseline. `rows` cannot serve as one: GET P/assessments
  // omits unanswered rows, so a freshly saved cell would stay "dirty" forever.
  const [persisted, setPersisted] = useState<Map<number, CardValue>>(() => new Map())
  const dirtyRef = useRef(new Set<number>())

  const prefix = sid !== undefined ? pPrefix(sid, topicId, childId) : null
  const rowsBySaid = useMemo(() => new Map((rows ?? []).map((row) => [row.said, row])), [rows])

  // Draft-notice dirty (06 §5): local input vs the last persisted value —
  // armed on the first keystroke and until a save succeeds (not only after
  // the SSE refetch lands); a whitespace-only edit matches its trimmed
  // baseline, which is exactly what the PUT would store.
  const unsaved = useMemo(() => {
    for (const [key, value] of Object.entries(values)) {
      const baseline = persisted.get(Number(key))
      const differs =
        baseline === undefined
          ? value.stars !== null || value.note !== ''
          : baseline.stars !== value.stars || baseline.note !== value.note.trim()
      if (differs) return true
    }
    return false
  }, [values, persisted])

  useEffect(() => {
    if (!unsaved) return
    const warn = (event: BeforeUnloadEvent) => {
      event.preventDefault()
      event.returnValue = ''
    }
    window.addEventListener('beforeunload', warn)
    return () => window.removeEventListener('beforeunload', warn)
  }, [unsaved])

  const loadAll = useCallback(() => {
    if (!prefix) return
    api.get<AssessmentRow[]>(`${prefix}/assessments`).then(
      (data) => {
        setRows(data)
        setListError(null)
        // Adopt the fresh cells everywhere they are not locally dirty — both
        // into the editable values and into the persisted draft-notice baseline.
        const adopt = data.filter((row) => !dirtyRef.current.has(row.said))
        setValues((current) => {
          const next = { ...current }
          for (const row of adopt) next[row.said] = { stars: row.stars, note: row.note ?? '' }
          return next
        })
        setPersisted((current) => {
          const next = new Map(current)
          for (const row of adopt) next.set(row.said, { stars: row.stars, note: row.note ?? '' })
          return next
        })
      },
      (failure: unknown) => {
        if (failure instanceof ApiError && failure.code === 'NOT_OWNER') setNotOwner(true)
        else setListError(failure)
      },
    )
    api
      .get<{ complete: boolean }>(`${prefix}/readiness`)
      .then((data) => setReady(data.complete), () => undefined)
  }, [prefix])

  useEffect(() => {
    loadAll()
  }, [loadAll])

  useEffect(() => {
    let alive = true
    api.get<{ id: number; name: string; is_active: boolean }>(`/api/children/${childId}`).then(
      (data) => {
        if (alive) {
          setChildName(data.name)
          setChildInactive(!data.is_active)
        }
      },
      () => undefined, // header decoration only; page works without it
    )
    return () => {
      alive = false
    }
  }, [childId])

  useEventRefetch(['assessment.changed', 'copy.completed'], loadAll)

  useEffect(() => {
    const off = registerSignal<{ child_id: number; stid: number; complete: boolean }>(
      'readiness.changed',
      (data) => {
        if (data.child_id === childId && data.stid === topicId) setReady(data.complete)
      },
    )
    return off
  }, [childId, topicId])

  const saveCard = async (said: number, value: CardValue, path: string) => {
    if (value.stars === null) return
    setSaveState((current) => ({ ...current, [said]: 'saving' }))
    dirtyRef.current.delete(said)
    try {
      await api.put(`${path}/activities/${said}`, {
        stars: value.stars,
        note: value.note.trim() || undefined,
      })
      // Adopt the saved values as the new baseline (derived dirty → false now,
      // not only after the SSE refetch lands): `rows` for the card UI and
      // `persisted` for the draft-notice check (which `rows` cannot back —
      // GET P/assessments omits unanswered activities).
      setRows((current) =>
        current
          ? current.map((row) =>
              row.said === said ? { ...row, stars: value.stars, note: value.note.trim() || null } : row,
            )
          : current,
      )
      setPersisted((current) =>
        new Map(current).set(said, { stars: value.stars, note: value.note.trim() }),
      )
      setSaveState((current) => ({ ...current, [said]: 'saved' }))
    } catch (failure: unknown) {
      dirtyRef.current.add(said)
      setSaveState((current) => ({ ...current, [said]: 'error' }))
      const code = errorCode(failure)
      if (code === 'NOT_OWNER') setNotOwner(true)
      else if (code === 'SESSION_LOCKED' || code === 'CHILD_INACTIVE') {
        toast(failure)
        if (code === 'SESSION_LOCKED') reloadDetail()
        else setChildInactive(true)
      } else {
        toast(failure)
      }
    }
  }

  if (detailError) {
    return (
      <>
        <PageHeader title={t('title')} />
        <Alert
          type="error"
          showIcon
          message={tc(
            `errors.${errorCode(detailError)}`,
          )}
          action={<Button onClick={() => navigate('/sessions')}>{tc('actions.back')}</Button>}
        />
      </>
    )
  }

  if (!detail || !prefix) {
    return <PageSkeleton />
  }

  const topic = detail.topics.find((entry) => entry.stid === topicId) ?? null
  if (!topic) {
    return (
      <>
        <PageHeader title={t('title')} />
        <Alert
          type="error"
          showIcon
          message={tc('errors.NOT_FOUND')}
          action={<Button onClick={() => navigate(`/sessions/${sid}`)}>{tc('actions.back')}</Button>}
        />
      </>
    )
  }

  const status = detail.session.status
  const editable = status === 'OPENED' && !notOwner && !childInactive && rows !== null
  const banner = notOwner
    ? t('notOwner')
    : childInactive
      ? tc('errors.CHILD_INACTIVE')
      : status === 'FINISHED'
        ? t('readOnly')
        : status === 'DRAFT'
          ? t('notOpened')
          : null

  const done = topic.activities.filter((activity) => (values[activity.said]?.stars ?? null) !== null)
    .length

  const card = (activity: { said: number; name: string; indicator: string }) => {
    const value = values[activity.said] ?? { stars: null, note: '' }
    const state = saveState[activity.said]
    const baseline = rowsBySaid.get(activity.said)
    const dirty =
      baseline === undefined ||
      baseline.stars !== value.stars ||
      (baseline.note ?? '') !== value.note
    return (
      <Card key={activity.said} style={{ marginBottom: 12 }} title={activity.name}>
        <Typography.Text
          type="secondary"
          className="kv-meta"
          style={{ display: 'block', marginBottom: 8 }}
        >
          {activity.indicator}
        </Typography.Text>
        <StarInput
          value={value.stars}
          disabled={!editable}
          onChange={(stars) => {
            const next = { stars, note: value.note }
            dirtyRef.current.add(activity.said)
            setValues((current) => ({ ...current, [activity.said]: next }))
            if (editable) void saveCard(activity.said, next, prefix)
          }}
        />
        <div style={{ marginTop: 8 }}>
          <Input.TextArea
            maxLength={MAX_NOTE}
            showCount
            rows={2}
            disabled={!editable}
            placeholder={t('note.placeholder')}
            value={value.note}
            onChange={(event) => {
              dirtyRef.current.add(activity.said)
              const note = event.target.value
              setValues((current) => ({
                ...current,
                [activity.said]: { stars: value.stars, note },
              }))
            }}
          />
        </div>
        <Space style={{ marginTop: 8 }}>
          <Button
            size="small"
            disabled={!editable || value.stars === null || !dirty || state === 'saving'}
            onClick={() => void saveCard(activity.said, value, prefix)}
          >
            {tc('actions.save')}
          </Button>
          <Typography.Text type={state === 'error' ? 'danger' : undefined} className="kv-meta">
            {state === 'saving'
              ? t('save.saving')
              : state === 'saved'
                ? t('save.saved')
                : state === 'error'
                  ? t('save.failed')
                  : ''}
          </Typography.Text>
        </Space>
      </Card>
    )
  }

  const body = (
    <>
      {listError ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="error"
          showIcon
          message={tc(`errors.${errorCode(listError)}`)}
          action={<Button onClick={loadAll}>{tc('actions.retry')}</Button>}
        />
      ) : null}
      {banner ? (
        <Alert
          style={{ marginBottom: 12 }}
          type={editable ? 'info' : 'warning'}
          showIcon
          message={banner}
        />
      ) : null}
      <Space style={{ marginBottom: 12 }} wrap>
        <Typography.Text type="secondary" className="kv-meta">
          {t('progress', { done, total: topic.activities.length })}
        </Typography.Text>
        {/* Progress (spec 03 F-09): one step per activity — filled once
            assessed (stars set). Decorative; the counter text above carries
            the same numbers for screen readers. */}
        <Progress
          steps={Math.max(1, topic.activities.length)}
          percent={
            topic.activities.length > 0
              ? Math.round((done / topic.activities.length) * 100)
              : 0
          }
          size="small"
          showInfo={false}
        />
        {unsaved ? (
          <Typography.Text type="secondary" className="kv-meta">
            {t('draftNotice')}
          </Typography.Text>
        ) : null}
        {ready !== null ? (
          <Typography.Text type="secondary" className="kv-meta">
            {ready ? (
              <>
                <CheckOutlined /> {t('topicComplete')}
              </>
            ) : (
              <>
                <DashOutlined /> {t('topicIncomplete')}
              </>
            )}
          </Typography.Text>
        ) : null}
        <Button
          type="link"
          size="small"
          onClick={() => navigate(`/sessions/${sid}/gallery?cid=${childId}&stid=${topicId}`)}
        >
          {tc('actions.view')}
        </Button>
      </Space>
      {rows === null && !listError && !notOwner ? (
        <PageSkeleton variant="list" rows={2} />
      ) : topic.activities.length === 0 ? (
        <Alert type="info" showIcon message={t('empty')} />
      ) : (
        topic.activities.map(card)
      )}
    </>
  )

  return (
    <>
      <PageHeader
        title={t('title')}
        description={
          <Space wrap>
            <strong>{childName || `#${childId}`}</strong>
            <Typography.Text type="secondary" className="kv-meta">
              {detail.session.name} · {topic.name}
            </Typography.Text>
          </Space>
        }
      />
      {detail.topics.length > 1 ? (
        <Tabs
          style={{ marginBottom: 12 }}
          activeKey={String(topicId)}
          items={detail.topics.map((entry) => ({
            key: String(entry.stid),
            label: entry.name,
          }))}
          onChange={(next) => navigate(`/assessment/${sid}/${cid}/${next}`)}
        />
      ) : null}
      {body}
    </>
  )
}
