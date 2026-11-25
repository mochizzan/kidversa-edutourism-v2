/**
 * Session detail — ONE fetch (`useSessionDetail`: session + topics + groups,
 * so assessment/gallery/report never waterfall) + lifecycle buttons with the
 * guarded-transition error contract (E10/E11/E13/E14):
 * - SESSION_LOCKED → read-only notice (+reload), STATE_CONFLICT → toast
 *   "changed elsewhere" + reload (06 E11);
 * - open failures carrying lists (GROUP_WITHOUT_FACILITATOR / VALIDATION)
 *   render a localized gate reason naming the offending groups inline above
 *   the groups (`error.groups`, never the generic server default);
 * - transitions reload explicitly on 200 (covers `already:true`, which
 *   publishes nothing) and SSE `session.status` keeps other tabs honest.
 * Lifecycle is two actions: Open (DRAFT/FINISHED → OPENED, gates on every
 * start) and Close (OPENED → FINISHED, no completion prerequisites).
 * DELETE = typed-text wipe dialog (scope counts), `session.deleted` closes
 * this page. Copy-data queue (F-15): children added at create (navigate
 * state), by member-add, or hinted by the SSE `copy_available` frame run the
 * modal flow one by one.
 */
import { useEffect, useState } from 'react'
import { Alert, App as AntApp, Button } from 'antd'
import { FileTextOutlined, PictureOutlined } from '@ant-design/icons'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import StatusChip from '../../components/StatusChip'
import { api, errorCode, ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { useCrumbName } from '../../lib/crumbs'
import dayjs from '../../lib/dayjs'
import { useErrorToast } from '../../lib/errors'
import { registerSignal } from '../../lib/stream'
import CopyDataModal from './CopyDataModal'
import DeleteSessionDialog from './DeleteSessionDialog'
import GroupsPanel from './GroupsPanel'
import { gateGroupsOf } from './gates'
import SessionMetaModal from './SessionMetaModal'
import { useSessionDetail } from './shared'
import './session-detail.css'

interface CopyChild {
  cid: number
  name: string
}

function isCopyChild(value: unknown): value is CopyChild {
  if (typeof value !== 'object' || value === null) return false
  const candidate = value as Record<string, unknown>
  return typeof candidate.cid === 'number' && typeof candidate.name === 'string'
}

function readCopyChildren(state: unknown): CopyChild[] {
  if (typeof state !== 'object' || state === null || !('copyChildren' in state)) return []
  const list: unknown = (state as Record<string, unknown>).copyChildren
  if (!Array.isArray(list)) return []
  return list.filter(isCopyChild)
}

/** One offending group from an open-gate failure (`error.groups`, B11). */
export default function SessionDetailPage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const toastError = useErrorToast()
  const { user } = useAuth()
  const { sid } = useParams()
  const navigate = useNavigate()
  const location = useLocation()

  const { detail, error, deleted, reload } = useSessionDetail(sid)
  // The breadcrumb bar shows the session's name instead of "Detail sesi".
  useCrumbName('sid', detail?.session.name)
  const [busy, setBusy] = useState(false)
  const [actionError, setActionError] = useState<unknown>(null)
  const [metaOpen, setMetaOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [copyQueue, setCopyQueue] = useState<CopyChild[]>([])

  // copy_available hint (P5): `member.changed` / `session.changed` frames
  // carry it — registerSignal keeps the payload (registerRefetch would drop
  // it), so a hinted child goes straight into the copy queue. StreamGate
  // rev-gates + debounces both events (never VALUE_EVENTS → one frame per
  // window, one modal) and CopyDataModal auto-closes when it finds no
  // candidates, so a foreign/duplicate hint cannot stack modals.
  useEffect(() => {
    const onHint = (frame: { copy_available?: boolean; child_id?: number }) => {
      if (frame.copy_available === true && frame.child_id) {
        setCopyQueue((queue) => [...queue, { cid: Number(frame.child_id), name: '' }])
      }
    }
    const offs = ['member.changed', 'session.changed'].map((event) =>
      registerSignal<{ copy_available?: boolean; child_id?: number }>(event, onHint),
    )
    return () => offs.forEach((off) => off())
  }, [])

  // Create-with-groups hands its added children over once, then clears history
  // state so a Back navigation never replays the flow.
  useEffect(() => {
    const initial = readCopyChildren(location.state)
    if (initial.length > 0) setCopyQueue(initial)
    if (location.state !== null) {
      navigate(location.pathname, { replace: true, state: null })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (deleted) {
      message.success(t('detail.deleted'))
      navigate('/sessions', { replace: true })
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [deleted])

  const canManage = user?.role === 'SA' || user?.role === 'ADMIN'

  const transition = async (action: 'open' | 'close') => {
    setBusy(true)
    setActionError(null)
    try {
      await api.post<{ status: string; already?: boolean }>(`/api/sessions/${sid}/${action}`)
      reload() // own-200 refetch (`already:true` publishes no frame)
      message.success(t(`detail.${action}Done`))
    } catch (failure: unknown) {
      const code = errorCode(failure)
      if (code === 'STATE_CONFLICT' || code === 'SESSION_LOCKED') {
        toastError(failure)
        reload()
      } else if (code === 'GROUP_WITHOUT_FACILITATOR' || code === 'VALIDATION') {
        // Open-gate lists come from the server message (group names etc.).
        setActionError(failure)
      } else {
        toastError(failure)
      }
    } finally {
      setBusy(false)
    }
  }

  if (deleted) return null

  if (error) {
    return (
      <>
        <PageHeader title={t('list.title')} />
        <Alert
          type="error"
          showIcon
          message={tc(`errors.${errorCode(error)}`)}
          action={<Button onClick={() => navigate('/sessions')}>{tc('actions.back')}</Button>}
        />
      </>
    )
  }

  if (!detail) {
    return <PageSkeleton />
  }

  const { session } = detail
  const status = session.status

  // Open-gate failures (E1/E14) carry the offending groups under
  // `error.groups`; translate the reason and name them instead of echoing the
  // generic backend default ("invalid or missing field"). Other codes keep the
  // server message when it is more specific than the code itself.
  const actionAlertMessage = (() => {
    if (!actionError) return null
    const code = errorCode(actionError)
    if (code === 'VALIDATION' || code === 'GROUP_WITHOUT_FACILITATOR') {
      const groups = gateGroupsOf(actionError)
      const names = groups.map((group) => group.name).join(', ')
      if (code === 'VALIDATION') {
        if (groups.length === 0) {
          return status === 'FINISHED'
            ? t('detail.openBlockedNoGroupsFinished')
            : t('detail.openBlockedNoGroups')
        }
        return status === 'FINISHED'
          ? t('detail.openBlockedEmptyGroupsFinished', { groups: names })
          : t('detail.openBlockedEmptyGroups', { groups: names })
      }
      return names
        ? t('detail.openBlockedOwnerMissing', { groups: names })
        : tc(`errors.${code}`)
    }
    return actionError instanceof ApiError && actionError.message !== actionError.code
      ? actionError.message
      : tc(`errors.${code}`)
  })()

  const actionButton = (action: 'open' | 'close', label: string, primary = false) => (
    <Button
      type={primary ? 'primary' : 'default'}
      loading={busy}
      onClick={() => void transition(action)}
    >
      {label}
    </Button>
  )

  // Facts read at a glance; the description (when present) stays prose above
  // them, so the strip never repeats the header.
  const startTime = session.start_time?.slice(0, 5) ?? ''
  const endTime = session.end_time?.slice(0, 5) ?? ''
  const timeRange = startTime && endTime ? `${startTime} – ${endTime}` : startTime || endTime || '—'
  const dateLabel = dayjs(session.date).isValid()
    ? dayjs(session.date).format('D MMMM YYYY')
    : session.date
  const activityCount = detail.topics.reduce((sum, topic) => sum + topic.activities.length, 0)

  return (
    <div className="kv-session-page">
      <PageHeader
        title={session.name}
        description={<StatusChip status={status} />}
        actions={
          <div className="kv-session-actions">
            <div className="kv-session-actions__nav">
              <Link className="kv-session-link" to={`/sessions/${sid}/gallery`}>
                <PictureOutlined aria-hidden="true" />
                <span>{t('reports.openGallery')}</span>
              </Link>
              <Link className="kv-session-link" to={`/sessions/${sid}/reports/topics`}>
                <FileTextOutlined aria-hidden="true" />
                <span>{t('reports.openReport')}</span>
              </Link>
            </div>
            {canManage ? (
              <div className="kv-session-actions__manage">
                {status === 'OPENED'
                  ? actionButton('close', t('detail.close'), true)
                  : actionButton('open', t('detail.open'), true)}
                {status === 'DRAFT' ? (
                  <Button onClick={() => setMetaOpen(true)}>{t('detail.edit')}</Button>
                ) : null}
                {status !== 'OPENED' ? (
                  <Button type="text" danger onClick={() => setDeleteOpen(true)}>
                    {t('detail.delete')}
                  </Button>
                ) : null}
              </div>
            ) : null}
          </div>
        }
      />

      <div className="kv-session-summary">
        {session.description ? <p className="kv-session-description">{session.description}</p> : null}
        <dl className="kv-session-facts">
          <div className="kv-session-fact">
            <dt className="kv-session-fact__label">{t('list.program')}</dt>
            <dd className="kv-session-fact__value">{session.program_name}</dd>
          </div>
          <div className="kv-session-fact">
            <dt className="kv-session-fact__label">{t('list.date')}</dt>
            <dd className="kv-session-fact__value">{dateLabel}</dd>
          </div>
          <div className="kv-session-fact">
            <dt className="kv-session-fact__label">{t('detail.time')}</dt>
            <dd className="kv-session-fact__value kv-session-fact__value--num">{timeRange}</dd>
          </div>
          <div className="kv-session-fact">
            <dt className="kv-session-fact__label">{t('list.location')}</dt>
            <dd className="kv-session-fact__value">{session.location}</dd>
          </div>
        </dl>
      </div>

      {actionError ? (
        <Alert
          type="warning"
          showIcon
          closable
          message={actionAlertMessage}
          onClose={() => setActionError(null)}
        />
      ) : null}

      <div className="kv-session-body">
        <section className="kv-session-topics">
          <div className="kv-section-head">
            <div className="kv-section-head__left">
              <h2 className="kv-section-title">{t('detail.topics')}</h2>
              <span className="kv-section-note">
                {activityCount} {t('detail.activities')}
              </span>
            </div>
          </div>
          <div className="kv-topic-panel">
            {detail.topics.map((topic) => (
              <div className="kv-topic" key={topic.stid}>
                <div className="kv-topic__head">
                  <h3 className="kv-topic__name">{topic.name}</h3>
                  <span className="kv-topic__count">
                    {topic.activities.length} {t('detail.activities')}
                  </span>
                </div>
                {topic.activities.length > 0 ? (
                  <ul className="kv-topic__activities">
                    {topic.activities.map((activity) => (
                      <li className="kv-topic__activity" key={activity.said}>
                        {activity.name}
                      </li>
                    ))}
                  </ul>
                ) : (
                  <p className="kv-topic__empty">{t('detail.noActivities')}</p>
                )}
              </div>
            ))}
          </div>
        </section>

        <GroupsPanel
          detail={detail}
          canManage={canManage}
          onChanged={reload}
          onMembersAdded={(added) => setCopyQueue((queue) => [...queue, ...added])}
        />
      </div>

      {metaOpen ? (
        <SessionMetaModal
          session={detail.session}
          onClose={() => setMetaOpen(false)}
          onSaved={() => {
            setMetaOpen(false)
            message.success(t('detail.metaSaved'))
            reload()
          }}
        />
      ) : null}

      <DeleteSessionDialog
        detail={detail}
        open={deleteOpen}
        onCancel={() => setDeleteOpen(false)}
        onDeleted={() => {
          setDeleteOpen(false)
          // `session.deleted` frame (or its own 200) closes this page.
          navigate('/sessions', { replace: true })
          message.success(t('detail.deleted'))
        }}
      />

      {copyQueue.length > 0 && detail ? (
        <CopyDataModal
          key={copyQueue[0].cid}
          sid={detail.session.id}
          childId={copyQueue[0].cid}
          childName={copyQueue[0].name}
          onClose={() => setCopyQueue((queue) => queue.slice(1))}
        />
      ) : null}
    </div>
  )
}

