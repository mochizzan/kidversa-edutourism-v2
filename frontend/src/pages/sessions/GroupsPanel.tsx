/**
 * Session groups panel (F-08 / FL-08): add / rename / reassign owner /
 * add-remove members / cascade-delete.
 *
 * Mutability is split by lifecycle:
 * - DRAFT: every action (roster + group meta).
 * - OPENED / FINISHED: roster is FROZEN (server answers
 *   SESSION_ROSTER_LOCKED); only rename and facilitator replacement remain.
 *   This keeps the reopen gate satisfiable (≥1 non-empty group with an active
 *   facilitator) for a session that has already left DRAFT.
 *
 * FAS sees own groups fully and foreign groups reduced to
 * `{gid, name, own:false, member_count, status}` (no owner/consent/members, no
 * actions; `status` mirrors the parent session status and is render-ignored).
 * Owner picker = active FASILITATORs of the current school
 * (server re-validates → VALIDATION).
 *
 * Copy-data (F-15): after a successful member add the parent (detail page)
 * receives the added children and runs the copy-candidates modal flow.
 * Refreshes: own-200 explicit reload + SSE `group.changed`/`member.changed`;
 * the children picker list also refetches on `child.changed`.
 */
import { useEffect, useState, type ReactNode } from 'react'
import { Alert, App as AntApp, Button, Card, Input, Modal, Space, Typography } from 'antd'
import {
  DeleteOutlined,
  EditOutlined,
  LockOutlined,
  PlusOutlined,
  UserAddOutlined,
} from '@ant-design/icons'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import ConfirmDialog from '../../components/ConfirmDialog'
import EmptyState from '../../components/EmptyState'
import { api, errorCode, ApiError } from '../../lib/api'
import { ChildPicker, ConsentTag, FormError, OwnerPicker } from './controls'
import { useListLoader } from '../../lib/list'
import {
  type ChildRow,
  type ReportsRow,
  type SessionDetail,
  type SessionGroup,
  type SessionMember,
  type SessionStatus,
} from '../../lib/session-contracts'

interface ProgressTopic {
  stid: number
  topic: string
  starred: number
  total: number
  complete: boolean
}

interface ProgressChild {
  child_id: number
  name: string
  gid: number | null
  topics: ProgressTopic[]
}

export interface GroupsPanelProps {
  detail: SessionDetail
  canManage: boolean
  /** Reload the session detail (explicit own-200 refetch). */
  onChanged: () => void
  /** Added children — the owner enqueues the copy-data modal flow (F-15). */
  onMembersAdded: (children: { cid: number; name: string }[]) => void
}

/** Two-letter monogram for the member row (decorative — name is always beside it). */
function initials(name: string): string {
  const parts = name.trim().split(/\s+/).filter(Boolean)
  const first = parts[0]?.[0] ?? ''
  const last = parts.length > 1 ? (parts[parts.length - 1]?.[0] ?? '') : ''
  const letters = `${first}${last}`.toUpperCase()
  return letters || '?'
}

export default function GroupsPanel({
  detail,
  canManage,
  onChanged,
  onMembersAdded,
}: GroupsPanelProps) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const sid = detail.session.id
  const status = detail.session.status
  /** Structural roster actions: DRAFT only (server: SESSION_ROSTER_LOCKED). */
  const canEditRoster = canManage && status === 'DRAFT'
  /** Post-Draft exceptions: rename + facilitator replacement, every status. */
  const canEditGroupMeta = canManage
  const firstStid = detail.topics[0]?.stid

  const { rows: children } = useListLoader<ChildRow>(
    '/api/children',
    {
      is_active: true,
      page: 1,
      per_page: 1000,
    },
    ['child.changed'],
  )

  const [groupModal, setGroupModal] = useState<{ open: boolean; editing: SessionGroup | null }>({
    open: false,
    editing: null,
  })
  const [memberModalGid, setMemberModalGid] = useState<number | null>(null)
  const [removeTarget, setRemoveTarget] = useState<{ group: SessionGroup; member: SessionMember } | null>(
    null,
  )
  const [deleteTarget, setDeleteTarget] = useState<SessionGroup | null>(null)
  const [removeBusy, setRemoveBusy] = useState(false)
  const [removeError, setRemoveError] = useState<unknown>(null)

  // Status is derived into dialog visibility rather than synced via an
  // effect: a transition to OPENED/FINISHED instantly hides every structural
  // dialog (create group / add members / remove / delete). The group editor
  // stays reachable because rename + facilitator replacement remain legal,
  // and a session can never return to DRAFT after leaving it.

  const removeMember = async () => {
    if (!removeTarget) return
    setRemoveBusy(true)
    setRemoveError(null)
    try {
      await api.del(
        `/api/sessions/${sid}/groups/${removeTarget.group.gid}/members/${removeTarget.member.child_id}`,
      )
      message.success(t('group.memberRemoved'))
      setRemoveTarget(null)
      onChanged()
    } catch (failure: unknown) {
      setRemoveError(failure)
    } finally {
      setRemoveBusy(false)
    }
  }

  const memberModalGroup = detail.groups.find((group) => group.gid === memberModalGid) ?? null
  const memberTotal = detail.groups.reduce((sum, group) => sum + group.member_count, 0)

  return (
    <section className="kv-groups-section">
      <div className="kv-section-head">
        <div className="kv-section-head__left">
          <h2 className="kv-section-title">{t('detail.groups')}</h2>
          {detail.groups.length > 0 ? (
            <span className="kv-section-note">
              {memberTotal} {t('list.members').toLowerCase()}
            </span>
          ) : null}
        </div>
        {canEditRoster ? (
          <Button
            size="small"
            icon={<PlusOutlined aria-hidden="true" />}
            onClick={() => setGroupModal({ open: true, editing: null })}
          >
            {t('group.add')}
          </Button>
        ) : null}
      </div>

      {canManage && status !== 'DRAFT' ? (
        <p className="kv-groups-lock">
          <LockOutlined aria-hidden="true" />
          <span>{t(status === 'FINISHED' ? 'group.rosterLockedFinished' : 'group.rosterLockedOpened')}</span>
        </p>
      ) : null}

      {detail.groups.length === 0 ? (
        <div className="kv-groups-empty">
          {status === 'DRAFT' ? (
            <EmptyState title={t('detail.noGroups')} />
          ) : (
            <EmptyState
              title={t('detail.noGroupsLocked')}
              description={t('detail.noGroupsLockedHint')}
            />
          )}
        </div>
      ) : null}

      {detail.groups.map((group) =>
        group.own ? (
          <Card key={group.gid} className="kv-group-card">
            <div className="kv-group-card__head">
              <div className="kv-group-card__id">
                <h3 className="kv-group-card__name">{group.name}</h3>
                <p className="kv-group-card__meta">
                  {group.owner
                    ? `${t('form.owner')}: ${group.owner.name}`
                    : t('group.ownerMissing')}
                  {' · '}
                  {t('list.members')}: {group.member_count}
                </p>
              </div>
              {canEditGroupMeta ? (
                <div className="kv-group-card__actions">
                  <Button
                    size="small"
                    type="text"
                    icon={<EditOutlined aria-hidden="true" />}
                    onClick={() => setGroupModal({ open: true, editing: group })}
                  >
                    {t('group.edit')}
                  </Button>
                  {canEditRoster ? (
                    <>
                      <Button
                        size="small"
                        type="text"
                        icon={<UserAddOutlined aria-hidden="true" />}
                        onClick={() => setMemberModalGid(group.gid)}
                      >
                        {t('group.addMembers')}
                      </Button>
                      <Button
                        size="small"
                        type="text"
                        danger
                        icon={<DeleteOutlined aria-hidden="true" />}
                        onClick={() => setDeleteTarget(group)}
                      >
                        {t('group.delete')}
                      </Button>
                    </>
                  ) : null}
                </div>
              ) : null}
            </div>

            {group.consent && Object.values(group.consent).some((count) => count > 0) ? (
              <div className="kv-group-card__consent">
                {(['PENDING', 'SETUJU', 'DITOLAK'] as const)
                  .filter((status) => (group.consent?.[status] ?? 0) > 0)
                  .map((status) => (
                    <span className="kv-consent" key={status}>
                      <ConsentTag status={status} />
                      <span className="kv-consent__count">×{group.consent?.[status] ?? 0}</span>
                    </span>
                  ))}
              </div>
            ) : null}

            {(group.members ?? []).length === 0 ? (
              <div className="kv-member-empty">
                <EmptyState title={t(status === 'DRAFT' ? 'group.empty' : 'group.emptyLocked')} />
              </div>
            ) : (
              <ul className="kv-member-list">
                {(group.members ?? []).map((member) => {
                  const actions: ReactNode[] = []
                  if (firstStid) {
                    actions.push(
                      <Link key="assess" to={`/assessment/${sid}/${member.child_id}/${firstStid}`}>
                        <Button type="link" size="small">{t('group.assess')}</Button>
                      </Link>,
                    )
                  }
                  actions.push(
                    <Link key="gallery" to={`/sessions/${sid}/gallery/children/${member.child_id}`}>
                      <Button type="link" size="small">{t('group.gallery')}</Button>
                    </Link>,
                  )
                  if (canEditRoster) {
                    actions.push(
                      <Button
                        key="remove"
                        size="small"
                        type="text"
                        danger
                        onClick={() => {
                          setRemoveError(null)
                          setRemoveTarget({ group, member })
                        }}
                      >
                        {t('group.removeMember')}
                      </Button>,
                    )
                  }
                  return (
                    <li className="kv-member" key={member.child_id}>
                      <span className="kv-member__avatar" aria-hidden="true">
                        {initials(member.name)}
                      </span>
                      <div className="kv-member__id">
                        <span className="kv-member__name">{member.name}</span>
                        <span className="kv-member__meta">
                          {member.age} · {member.school}
                        </span>
                      </div>
                      <ConsentTag status={member.consent ?? ''} />
                      <div className="kv-member__actions">{actions}</div>
                    </li>
                  )
                })}
              </ul>
            )}
          </Card>
        ) : (
          <Card key={group.gid} className="kv-group-card kv-group-card--foreign">
            <div className="kv-group-card__id">
              <h3 className="kv-group-card__name">{group.name}</h3>
              <p className="kv-group-card__meta">
                {t('list.members')}: {group.member_count}
              </p>
            </div>
          </Card>
        ),
      )}

      {groupModal.open && (groupModal.editing !== null || canEditRoster) ? (
        <GroupFormModal
          sid={sid}
          sessionStatus={status}
          initial={groupModal.editing}
          onCancel={() => setGroupModal({ open: false, editing: null })}
          onDone={(created) => {
            setGroupModal({ open: false, editing: null })
            message.success(created ? t('group.created') : t('group.updated'))
            onChanged()
          }}
        />
      ) : null}

      {memberModalGroup && canEditRoster ? (
        <MemberAddModal
          sid={sid}
          group={memberModalGroup}
          rows={children}
          onCancel={() => setMemberModalGid(null)}
          onDone={(addedIds) => {
            setMemberModalGid(null)
            message.success(t('group.membersAdded'))
            onChanged()
            onMembersAdded(
              addedIds.map((childId) => ({
                cid: childId,
                name: children.find((child) => child.id === childId)?.name ?? '',
              })),
            )
          }}
        />
      ) : null}

      <ConfirmDialog
        open={canEditRoster && removeTarget !== null}
        title={t('group.removeTitle', { name: removeTarget?.member.name ?? '' })}
        danger
        loading={removeBusy}
        confirmLabel={t('group.removeMember')}
        cancelLabel={tc('actions.cancel')}
        onCancel={() => setRemoveTarget(null)}
        onConfirm={removeMember}
      >
        <p>{t('group.removeWarn')}</p>
        <Typography.Text type="secondary" style={{ display: 'block' }}>{tc('confirm.irreversible')}</Typography.Text>
        {removeError ? (
          <Alert
            type="error"
            showIcon
            message={
              removeError instanceof ApiError && removeError.message !== removeError.code
                ? removeError.message
                : tc(`errors.${errorCode(removeError)}`)
            }
          />
        ) : null}
      </ConfirmDialog>

      {deleteTarget && canEditRoster ? (
        <DeleteGroupDialog
          sid={sid}
          group={deleteTarget}
          onCancel={() => setDeleteTarget(null)}
          onDeleted={() => {
            setDeleteTarget(null)
            message.success(t('group.deleted'))
            onChanged()
          }}
        />
      ) : null}
    </section>
  )
}

function GroupFormModal({
  sid,
  sessionStatus,
  initial,
  onCancel,
  onDone,
}: {
  sid: number
  sessionStatus: SessionStatus
  initial: SessionGroup | null
  onCancel: () => void
  onDone: (created: boolean) => void
}) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const draft = sessionStatus === 'DRAFT'
  const initialOwner = initial?.owner?.id ?? null
  const [name, setName] = useState(initial?.name ?? '')
  const [owner, setOwner] = useState<number | null>(initialOwner)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [nameError, setNameError] = useState<string | null>(null)
  const [ownerError, setOwnerError] = useState<string | null>(null)

  const save = async () => {
    const trimmed = name.trim()
    let invalid = false
    if (!trimmed) {
      setNameError(t('form.errors.groupName'))
      invalid = true
    } else {
      setNameError(null)
    }
    // Outside DRAFT the group must keep an owner: clearing it would make the
    // reopen gate unsatisfiable (server: VALIDATION / MsgFacilitatorRequired).
    if (!draft && owner === null) {
      setOwnerError(t('group.ownerRequired'))
      invalid = true
    } else {
      setOwnerError(null)
    }
    if (invalid) return

    setBusy(true)
    setError(null)
    try {
      if (!initial) {
        await api.post(`/api/sessions/${sid}/groups`, {
          name: trimmed,
          facilitator_id: owner ?? 0,
        })
      } else {
        // PATCH only the fields the user actually changed: a no-op body is
        // VALIDATION, and outside DRAFT an explicit facilitator_id 0 is
        // rejected because it would leave the group ownerless.
        const body: { name?: string; facilitator_id?: number } = {}
        if (trimmed !== initial.name) body.name = trimmed
        if (owner !== initialOwner) body.facilitator_id = owner ?? 0
        if (body.name === undefined && body.facilitator_id === undefined) {
          onCancel()
          return
        }
        await api.patch(`/api/sessions/${sid}/groups/${initial.gid}`, body)
      }
      onDone(initial === null)
    } catch (failure: unknown) {
      setError(failure)
    } finally {
      setBusy(false)
    }
  }

  const errorMessage = (() => {
    if (!error) return null
    if (
      error instanceof ApiError &&
      error.code === 'VALIDATION' &&
      (initial === null || owner !== initialOwner)
    ) {
      return t('group.ownerInactive')
    }
    return error instanceof ApiError && error.message !== error.code
      ? error.message
      : tc(`errors.${errorCode(error)}`)
  })()

  return (
    <Modal
      open
      title={initial ? t('group.edit') : t('group.add')}
      okText={tc('actions.save')}
      cancelText={tc('actions.cancel')}
      confirmLoading={busy}
      maskClosable={!busy}
      onOk={save}
      onCancel={onCancel}
    >
      <Space direction="vertical" style={{ width: '100%' }}>
        <div className="kv-field">
          <label className="kv-field-label" htmlFor="kv-group-name">
            {t('form.groupName')}
          </label>
          <Input
            id="kv-group-name"
            placeholder={t('form.groupNamePlaceholder')}
            value={name}
            aria-invalid={nameError !== null}
            onChange={(event) => setName(event.target.value)}
          />
        </div>
        {nameError ? <FormError>{nameError}</FormError> : null}
        <div className="kv-field">
          <label className="kv-field-label" htmlFor="kv-group-owner">
            {t('form.owner')}
          </label>
          <OwnerPicker
            id="kv-group-owner"
            ariaLabel={t('form.owner')}
            value={owner}
            onChange={setOwner}
            allowClear={draft}
          />
        </div>
        {ownerError ? <FormError>{ownerError}</FormError> : null}
        <Typography.Text type="secondary">
          {draft ? t('group.ownerOptional') : t('group.ownerLockedHint')}
        </Typography.Text>
        {errorMessage ? <Alert type="error" showIcon message={errorMessage} /> : null}
      </Space>
    </Modal>
  )
}

function MemberAddModal({
  sid,
  group,
  rows,
  onCancel,
  onDone,
}: {
  sid: number
  group: SessionGroup
  rows: ChildRow[]
  onCancel: () => void
  onDone: (addedChildIds: number[]) => void
}) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const [selected, setSelected] = useState<number[]>([])
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const existing = (group.members ?? []).map((member) => member.child_id)

  const add = async () => {
    setBusy(true)
    setError(null)
    try {
      await api.post(`/api/sessions/${sid}/groups/${group.gid}/members`, {
        child_ids: selected,
      })
      onDone(selected)
    } catch (failure: unknown) {
      setError(failure)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open
      title={`${t('group.addMembers')} — ${group.name}`}
      okText={t('group.addMembers')}
      cancelText={tc('actions.cancel')}
      confirmLoading={busy}
      okButtonProps={{ disabled: selected.length === 0 }}
      maskClosable={!busy}
      onOk={add}
      onCancel={onCancel}
    >
      <Space direction="vertical" style={{ width: '100%' }}>
        <Typography.Text type="secondary">{t('form.childrenHint')}</Typography.Text>
        <ChildPicker
          rows={rows}
          value={selected}
          excludeIds={existing}
          onChange={setSelected}
          ariaLabel={t('form.children')}
        />
        {error ? (
          <Alert
            type="error"
            showIcon
            message={
              error instanceof ApiError && error.message !== error.code
                ? error.message
                : tc(`errors.${errorCode(error)}`)
            }
          />
        ) : null}
      </Space>
    </Modal>
  )
}

/**
 * Typed-text cascade wipe (02§2b option B): group + ALL its members leave the
 * session; their assessments, photo rows+files, narratives, mission picks and
 * consent/public links in this session are wiped in one Tx. Counts come from
 * the session progress matrix + reports matrix (best-effort, this dialog
 * stays usable if either read fails).
 */
function DeleteGroupDialog({
  sid,
  group,
  onCancel,
  onDeleted,
}: {
  sid: number
  group: SessionGroup
  onCancel: () => void
  onDeleted: () => void
}) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const [assessments, setAssessments] = useState<number | null>(null)
  const [reportLinks, setReportLinks] = useState<number | null>(null)

  const consentLinks = Object.values(group.consent ?? {}).reduce((sum, count) => sum + count, 0)

  useEffect(() => {
    let alive = true
    const memberIds = new Set((group.members ?? []).map((member) => member.child_id))
    api.get<{ children: ProgressChild[] }>(`/api/sessions/${sid}/progress`).then(
      (data) => {
        if (!alive) return
        const starred = data.children
          .filter((child) => child.gid === group.gid)
          .reduce(
            (sum, child) => sum + child.topics.reduce((inner, topic) => inner + topic.starred, 0),
            0,
          )
        setAssessments(starred)
      },
      () => undefined, // best-effort counts: dialog still wipes with member counts
    )
    api.get<ReportsRow[]>(`/api/sessions/${sid}/reports`).then(
      (rows) => {
        if (!alive) return
        const list = Array.isArray(rows) ? rows : []
        setReportLinks(
          list.filter(
            (row) =>
              memberIds.has(row.child_id) && (row.report_url !== null || row.gallery_url !== null),
          ).length,
        )
      },
      () => undefined,
    )
    return () => {
      alive = false
    }
  }, [sid, group])

  const confirm = async () => {
    setBusy(true)
    setError(null)
    try {
      await api.del(`/api/sessions/${sid}/groups/${group.gid}`)
      onDeleted()
    } catch (failure: unknown) {
      setError(failure)
    } finally {
      setBusy(false)
    }
  }

  return (
    <ConfirmDialog
      open
      title={t('group.deleteTitle', { name: group.name })}
      typedText={group.name}
      danger
      loading={busy}
      confirmLabel={t('group.delete')}
      onCancel={onCancel}
      onConfirm={confirm}
    >
      <p>{t('group.deleteCounts', { members: group.member_count })}</p>
      {assessments !== null && reportLinks !== null ? (
        <Alert
          style={{ marginBottom: 12 }}
          type="warning"
          showIcon
          message={t('group.deleteScope', {
            assessments,
            consent: consentLinks,
            links: reportLinks,
          })}
        />
      ) : null}
      <Typography.Text type="secondary" style={{ display: 'block' }}>{tc('confirm.irreversible')}</Typography.Text>
      {error ? (
        <Alert
          type="error"
          showIcon
          message={tc(`errors.${errorCode(error)}`)}
        />
      ) : null}
    </ConfirmDialog>
  )
}
