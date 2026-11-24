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
 * DELETE = typed-text wipe dialog (scope counts), `session.deleted` closes
 * this page. Copy-data queue (F-15): children added at create (navigate
 * state), by member-add, or hinted by the SSE `copy_available` frame run the
 * modal flow one by one.
 */
import { useEffect, useState } from 'react'
import { Alert, App as AntApp, Button, DatePicker, Form, Input, Modal, Space, TimePicker, Typography } from 'antd'
import { FileTextOutlined, PictureOutlined } from '@ant-design/icons'
import { Link, useLocation, useNavigate, useParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import ConfirmDialog from '../../components/ConfirmDialog'
import StatusChip from '../../components/StatusChip'
import { api, errorCode, ApiError } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { useCrumbName } from '../../lib/crumbs'
import dayjs from '../../lib/dayjs'
import { useErrorToast } from '../../lib/errors'
import { registerSignal } from '../../lib/stream'
import type { SessionDetail } from '../../lib/session-contracts'
import CopyDataModal from './CopyDataModal'
import DeleteSessionDialog from './DeleteSessionDialog'
import GroupsPanel from './GroupsPanel'
import { validateSessionMeta } from './meta'
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
interface GateGroup {
  name: string
}

function gateGroupsOf(failure: unknown): GateGroup[] {
  if (!(failure instanceof ApiError)) return []
  const raw = failure.details?.groups
  if (!Array.isArray(raw)) return []
  return raw.flatMap((entry) => {
    if (typeof entry !== 'object' || entry === null) return []
    const name = (entry as Record<string, unknown>).name
    return typeof name === 'string' && name.trim() !== '' ? [{ name }] : []
  })
}

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
  const [finishOpen, setFinishOpen] = useState(false)
  const [finishItems, setFinishItems] = useState<string[] | null>(null)
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

  const transition = async (action: 'open' | 'finish' | 'reopen') => {
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

  const startFinish = async () => {
    setFinishItems(null)
    setFinishOpen(true)
    try {
      const progress = await api.get<{
        children: { name: string; topics: { topic: string; complete: boolean }[] }[]
      }>(`/api/sessions/${sid}/progress`)
      setFinishItems(
        progress.children.flatMap((child) =>
          child.topics
            .filter((topic) => !topic.complete)
            .map((topic) => `${child.name} — ${topic.topic}`),
        ),
      )
    } catch {
      // Warn list unavailable → still allow the (spec-permitted) partial finish.
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
        return groups.length === 0
          ? t('detail.openBlockedNoGroups')
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

  const actionButton = (action: 'open' | 'finish' | 'reopen', label: string, primary = false) => (
    <Button
      type={primary ? 'primary' : 'default'}
      loading={busy}
      onClick={() => {
        if (action === 'finish') void startFinish()
        else void transition(action)
      }}
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
              <Link className="kv-session-link" to={`/sessions/${sid}/reports`}>
                <FileTextOutlined aria-hidden="true" />
                <span>{t('reports.openReport')}</span>
              </Link>
            </div>
            {canManage ? (
              <div className="kv-session-actions__manage">
                {status === 'DRAFT' ? actionButton('open', t('detail.open'), true) : null}
                {status === 'OPENED' ? actionButton('finish', t('detail.finish'), true) : null}
                {status === 'FINISHED' ? actionButton('reopen', t('detail.reopen')) : null}
                {status === 'DRAFT' ? (
                  <Button onClick={() => setMetaOpen(true)}>{tc('actions.edit')}</Button>
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
        <MetaEditModal
          detail={detail}
          onClose={() => setMetaOpen(false)}
          onSaved={() => {
            setMetaOpen(false)
            message.success(t('detail.metaSaved'))
            reload()
          }}
        />
      ) : null}

      <ConfirmDialog
        open={finishOpen}
        title={t('detail.finishTitle', { name: session.name })}
        loading={busy}
        confirmLabel={t('detail.finish')}
        cancelLabel={tc('actions.cancel')}
        onCancel={() => setFinishOpen(false)}
        onConfirm={async () => {
          setFinishOpen(false)
          await transition('finish')
        }}
      >
        <p>{t('detail.partialFinish')}</p>
        {finishItems && finishItems.length > 0 ? (
          <>
            <Typography.Text type="secondary" style={{ display: 'block' }}>{t('detail.incompleteList')}</Typography.Text>
            <ul style={{ maxHeight: 200, overflowY: 'auto', margin: 0, paddingLeft: 18 }}>
              {finishItems.map((item) => (
                <li key={item} style={{ fontSize: 12 }}>
                  {item}
                </li>
              ))}
            </ul>
          </>
        ) : null}
      </ConfirmDialog>

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

/** Draft-only snapshot edit (Opened → SESSION_LOCKED server-side; button hidden). */
function MetaEditModal({
  detail,
  onClose,
  onSaved,
}: {
  detail: SessionDetail
  onClose: () => void
  onSaved: () => void
}) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const [name, setName] = useState(detail.session.name)
  const [description, setDescription] = useState(detail.session.description)
  const [date, setDate] = useState(detail.session.date)
  const [startTime, setStartTime] = useState(detail.session.start_time?.slice(0, 5) ?? '')
  const [endTime, setEndTime] = useState(detail.session.end_time?.slice(0, 5) ?? '')
  const [location, setLocation] = useState(detail.session.location)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const save = async () => {
    const next: Record<string, string> = {}
    for (const [field, key] of Object.entries(
      validateSessionMeta({ name, date, location, startTime, endTime }),
    )) {
      if (key) next[field] = t(key)
    }
    setErrors(next)
    if (Object.keys(next).length > 0) return

    setBusy(true)
    setError(null)
    try {
      await api.patch(`/api/sessions/${detail.session.id}`, {
        name: name.trim(),
        description: description.trim() || undefined,
        date,
        start_time: startTime ? `${startTime}:00` : undefined,
        end_time: endTime ? `${endTime}:00` : undefined,
        location: location.trim(),
      })
      onSaved()
    } catch (failure: unknown) {
      setError(failure)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open
      title={t('detail.editTitle')}
      okText={tc('actions.save')}
      cancelText={tc('actions.cancel')}
      confirmLoading={busy}
      maskClosable={!busy}
      onOk={save}
      onCancel={onClose}
    >
      <Form layout="vertical">
        <Form.Item
          label={t('list.name')}
          validateStatus={errors.name ? 'error' : undefined}
          help={errors.name}
        >
          <Input value={name} onChange={(event) => setName(event.target.value)} />
        </Form.Item>
        <Form.Item label={t('form.description')}>
          <Input.TextArea rows={3} value={description} onChange={(event) => setDescription(event.target.value)} />
        </Form.Item>
        <Form.Item
          label={t('list.date')}
          validateStatus={errors.date ? 'error' : undefined}
          help={errors.date}
        >
          <DatePicker
            value={date ? dayjs(date, 'YYYY-MM-DD') : null}
            onChange={(value) => setDate(value ? value.format('YYYY-MM-DD') : '')}
            format="YYYY-MM-DD"
            style={{ width: '100%' }}
          />
        </Form.Item>
        <Space size="middle">
          <Form.Item label={t('form.startTime')} style={{ marginBottom: 0 }}>
            <TimePicker
              value={startTime ? dayjs(startTime, 'HH:mm') : null}
              onChange={(value) => setStartTime(value ? value.format('HH:mm') : '')}
              format="HH:mm"
              style={{ width: 120 }}
            />
          </Form.Item>
          <Form.Item
            label={t('form.endTime')}
            style={{ marginBottom: 0 }}
            validateStatus={errors.endTime ? 'error' : undefined}
            help={errors.endTime}
          >
            <TimePicker
              value={endTime ? dayjs(endTime, 'HH:mm') : null}
              onChange={(value) => setEndTime(value ? value.format('HH:mm') : '')}
              format="HH:mm"
              style={{ width: 120 }}
            />
          </Form.Item>
        </Space>
        <Form.Item
          label={t('list.location')}
          validateStatus={errors.location ? 'error' : undefined}
          help={errors.location}
        >
          <Input value={location} onChange={(event) => setLocation(event.target.value)} />
        </Form.Item>
        {error ? (
          <Alert
            type="error"
            showIcon
            message={
              error instanceof ApiError && error.code === 'SESSION_LOCKED'
                ? t('detail.locked')
                : tc(`errors.${errorCode(error)}`)
            }
          />
        ) : null}
      </Form>
    </Modal>
  )
}
