/**
 * Children master (spec F-07, wave F3) — JSON list + form modal with the
 * age-confirmation flow.
 *
 * Wire contract (B1, 2026-10-09):
 * - GET `/api/children` (all roles, server-scoped) {page, per_page, q,
 *   is_active (bool), sort, dir}; sort allowlist id|name|created_at, default
 *   id asc.
 * - POST `/api/children` / PATCH `/api/children/:id` = JSON full set;
 *   age 11–15 MUST carry age_confirmed:true or the server VALIDATIONs.
 * - POST `/api/children/:id/activate` | `/deactivate` → updated child.
 * - DELETE `/api/children/:id` → {deleted:true}; the typed-delete dialog
 *   opens only after a successful GET `/api/children/:id` counts fetch (the
 *   ONLY counts source) and lists the cascading rows in `deleteCounts`.
 * - SSE `child.changed` → same-params refetch via `registerRefetch`
 *   (StreamGate owns debounce + rev-gate; no polling).
 *
 * FAS is read-only: no create button, no actions column. Mutate failures
 * (VALIDATION / CHILD_INACTIVE / FORBIDDEN_ROLE) show inline in the modal,
 * row-action failures toast `errors.<CODE>`.
 */
import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Select,
  Typography,
  type TableProps,
} from 'antd'
import {
  CheckCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  StopOutlined,
} from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errorCode, type ListParams } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import ConfirmDialog from '../../components/ConfirmDialog'
import DataTable from '../../components/DataTable'
import EmptyState from '../../components/EmptyState'
import FilterBar, { FilterField } from '../../components/FilterBar'
import PageHeader from '../../components/PageHeader'
import Pager from '../../components/Pager'
import { RowAction, RowActions, RowActionsDivider } from '../../components/RowActions'
import RefreshButton from '../../components/RefreshButton'
import StatusPill from '../../components/StatusPill'
import { useErrorToast } from '../../lib/errors'
import { useFeedback } from '../../lib/feedback'
import { MAX_PERSON_NAME, MAX_SHORT_TEXT, normalizeWa, parseAge, toRule } from '../../lib/validation'
import { usePagedList } from '../masters/shared'

interface Child {
  id: number
  name: string
  age: number
  age_confirmed: boolean
  school: string
  mother_name: string
  mother_wa: string
  father_name: string | null
  father_wa: string | null
  guardian_name: string | null
  is_active: boolean
  created_at: string
}

/** Cascade counts from GET `/api/children/:id` — the only counts source. */
interface ChildCounts {
  members: number
  assessments: number
  photos: number
  narratives: number
  missions: number
  links: number
}

/** Non-draft membership from GET `/api/children/:id` (delete warning). */
interface ChildSessionRef {
  id: number
  name: string
  status: 'OPENED' | 'FINISHED'
}

interface ChildPayload {
  name: string
  age: number
  age_confirmed: boolean
  school: string
  mother_name: string
  mother_wa: string
  father_name: string
  father_wa: string
  guardian_name: string
}

const DEFAULT_SORT: ListParams = { sort: 'id', dir: 'asc' }

interface ChildFormValues {
  name?: string
  age?: string | number
  school?: string
  mother_name?: string
  mother_wa?: string
  father_name?: string
  father_wa?: string
  guardian_name?: string
}

interface ChildrenFormProps {
  open: boolean
  record: Child | null
  onClose: () => void
  onSaved: () => void
}

function ChildrenForm({ open, record, onClose, onSaved }: ChildrenFormProps) {
  const { t } = useTranslation(['master', 'common'])
  const { school } = useAuth()
  const feedback = useFeedback()
  const [form] = Form.useForm<ChildFormValues>()
  const [saving, setSaving] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [agePrompt, setAgePrompt] = useState<{ age: number; payload: ChildPayload } | null>(null)
  const motherWaRaw = Form.useWatch('mother_wa', form)
  const fatherWaRaw = Form.useWatch('father_wa', form)
  const motherWaPreview = motherWaRaw ? normalizeWa(String(motherWaRaw)) : null
  const fatherWaPreview = fatherWaRaw ? normalizeWa(String(fatherWaRaw)) : null

  const [wasOpen, setWasOpen] = useState(open)
  if (wasOpen !== open) {
    // Adjust state during render (ConfirmDialog idiom): a freshly opened
    // modal must never carry the previous submit or age-prompt state.
    setWasOpen(open)
    setSaving(false)
    setSubmitError(null)
    setAgePrompt(null)
  }

  useEffect(() => {
    if (!open) return
    form.resetFields()
    if (record) {
      form.setFieldsValue({
        name: record.name,
        age: String(record.age),
        school: record.school,
        mother_name: record.mother_name,
        mother_wa: record.mother_wa,
        father_name: record.father_name ?? '',
        father_wa: record.father_wa ?? '',
        guardian_name: record.guardian_name ?? '',
      })
    } else {
      // New child: prefill the active school's name, but keep the field
      // editable (children.school stays free text).
      form.setFieldsValue({ school: school?.school_name ?? '' })
    }
  }, [open, record, form, school])

  const submitChild = async (payload: ChildPayload) => {
    setSaving(true)
    setSubmitError(null)
    try {
      if (record) {
        await api.patch(`/api/children/${record.id}`, payload)
        feedback.saved('child', payload.name)
      } else {
        await api.post('/api/children', payload)
        feedback.created('child', payload.name)
      }
      onClose()
      onSaved()
    } catch (error) {
      setSubmitError(errorCode(error))
    } finally {
      setSaving(false)
    }
  }

  const onFinish = async (values: ChildFormValues) => {
    const age = parseAge(String(values.age))
    if (!age.ok) {
      form.setFields([{ name: 'age', errors: [t('master:children.ageInvalid')] }])
      return
    }
    const motherWa = normalizeWa(String(values.mother_wa).trim())
    if (!motherWa) {
      form.setFields([{ name: 'mother_wa', errors: [t('master:children.waInvalid')] }])
      return
    }
    const fatherWaInput = (values.father_wa ?? '').trim()
    const fatherWa = fatherWaInput ? (normalizeWa(fatherWaInput) ?? '') : ''
    if (fatherWaInput && !fatherWa) {
      form.setFields([{ name: 'father_wa', errors: [t('master:children.waInvalid')] }])
      return
    }
    const payload: ChildPayload = {
      name: (values.name ?? '').trim(),
      age: age.age,
      age_confirmed: age.age >= 11,
      school: (values.school ?? '').trim(),
      mother_name: (values.mother_name ?? '').trim(),
      mother_wa: motherWa,
      father_name: (values.father_name ?? '').trim(),
      father_wa: fatherWa,
      guardian_name: (values.guardian_name ?? '').trim(),
    }
    // Age 11–15 needs one explicit confirmation per child. A stored
    // `age_confirmed === true` acknowledgement STANDS even when the age is
    // edited to another 11–15 value — no re-prompt; only a previously
    // unconfirmed child (or a ≤10 child grown into 11–15) prompts.
    if (age.needConfirm && !(record && record.age_confirmed)) {
      setAgePrompt({ age: age.age, payload })
      return
    }
    await submitChild(payload)
  }

  return (
    <>
      <Modal
        open={open}
        width={640}
        title={
          record
            ? t('master:editTitle', { name: t('master:children.title') })
            : t('master:children.new')
        }
        okText={t('common:actions.save')}
        cancelText={t('common:actions.cancel')}
        confirmLoading={saving}
        maskClosable={!saving}
        onOk={() => form.submit()}
        onCancel={onClose}
      >
        {submitError ? (
          <Alert
            style={{ marginBottom: 12 }}
            type="error"
            showIcon
            message={t(`common:errors.${submitError}`)}
          />
        ) : null}
        <Form form={form} layout="vertical" onFinish={onFinish} scrollToFirstError>
          <Form.Item
            name="name"
            label={t('master:children.name')}
            extra={t('master:children.nameHint')}
            rules={[{ required: true }]}
          >
            <Input
              maxLength={MAX_PERSON_NAME}
              showCount
              placeholder={t('master:children.namePlaceholder')}
            />
          </Form.Item>
          <Form.Item
            name="age"
            label={t('master:children.age')}
            extra={t('master:children.ageHint')}
            rules={[
              { required: true },
              toRule(
                (value) =>
                  value.trim() === '' || parseAge(value).ok ? null : 'master:children.ageInvalid',
                t,
              ),
            ]}
          >
            <Input inputMode="numeric" placeholder={t('master:children.agePlaceholder')} />
          </Form.Item>
          <Form.Item
            name="school"
            label={t('master:children.school')}
            rules={[{ required: true }]}
          >
            <Input maxLength={MAX_SHORT_TEXT} placeholder={t('master:children.schoolPlaceholder')} />
          </Form.Item>
          <Form.Item
            name="mother_name"
            label={t('master:children.motherName')}
            rules={[{ required: true }]}
          >
            <Input maxLength={MAX_PERSON_NAME} placeholder={t('master:children.personPlaceholder')} />
          </Form.Item>
          <Form.Item
            name="mother_wa"
            label={t('master:children.motherWa')}
            rules={[
              { required: true },
              toRule(
                (value) =>
                  value.trim() === '' || normalizeWa(value) !== null
                    ? null
                    : 'master:children.waInvalid',
                t,
              ),
            ]}
            extra={motherWaPreview ? t('master:children.waPreview', { number: motherWaPreview }) : undefined}
          >
            <Input placeholder={t('master:children.waPlaceholder')} />
          </Form.Item>
          <Form.Item name="father_name" label={t('master:children.fatherName')}>
            <Input maxLength={MAX_PERSON_NAME} placeholder={t('master:children.personPlaceholder')} />
          </Form.Item>
          <Form.Item
            name="father_wa"
            label={t('master:children.fatherWa')}
            rules={[
              toRule(
                (value) =>
                  value.trim() === '' || normalizeWa(value) !== null
                    ? null
                    : 'master:children.waInvalid',
                t,
              ),
            ]}
            extra={fatherWaPreview ? t('master:children.waPreview', { number: fatherWaPreview }) : undefined}
          >
            <Input placeholder={t('master:children.waPlaceholder')} />
          </Form.Item>
          <Form.Item name="guardian_name" label={t('master:children.guardianName')}>
            <Input maxLength={MAX_PERSON_NAME} placeholder={t('master:children.personPlaceholder')} />
          </Form.Item>
        </Form>
      </Modal>
      <Modal
        open={agePrompt !== null}
        title={t('master:children.ageWarningTitle')}
        okText={t('common:actions.yes')}
        cancelText={t('common:actions.no')}
        onOk={() => {
          if (!agePrompt) return
          const { payload } = agePrompt
          setAgePrompt(null)
          void submitChild(payload)
        }}
        onCancel={() => setAgePrompt(null)}
      >
        <Typography.Paragraph>
          {t('master:children.ageWarning', { age: agePrompt?.age ?? '' })}
        </Typography.Paragraph>
      </Modal>
    </>
  )
}

export default function ChildrenPage() {
  const { t } = useTranslation(['master', 'common'])
  const { user } = useAuth()
  const canMutate = user?.role === 'SA' || user?.role === 'ADMIN'
  const toast = useErrorToast()
  const feedback = useFeedback()
  const list = usePagedList<Child>('/api/children', 'child.changed', DEFAULT_SORT)
  const [search, setSearch] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Child | null>(null)
  const [deleting, setDeleting] = useState<{
    row: Child
    counts: ChildCounts
    sessions: ChildSessionRef[]
  } | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [togglingId, setTogglingId] = useState<number | null>(null)

  const onTableChange: TableProps<Child>['onChange'] = (_pagination, _filters, sorter) => {
    const single = Array.isArray(sorter) ? sorter[0] : sorter
    if (!single || single.order == null) list.setFilters(DEFAULT_SORT)
    else
      list.setFilters({
        sort: String(single.field),
        dir: single.order === 'descend' ? 'desc' : 'asc',
      })
  }

  const toggleActive = async (row: Child) => {
    setTogglingId(row.id)
    try {
      await api.post(`/api/children/${row.id}/${row.is_active ? 'deactivate' : 'activate'}`)
      feedback.changed('child', row.name, row.is_active ? 'deactivated' : 'activated')
      list.refetch()
    } catch (error) {
      toast(error) // CHILD_INACTIVE / VALIDATION / FORBIDDEN_ROLE → toast
    } finally {
      setTogglingId(null)
    }
  }

  const startDelete = async (row: Child) => {
    try {
      const data = await api.get<{ counts: ChildCounts; sessions: ChildSessionRef[] }>(
        `/api/children/${row.id}`,
      )
      setDeleting({ row, counts: data.counts, sessions: data.sessions ?? [] })
    } catch (error) {
      toast(error) // NOT_FOUND (stale row) → toast, no dialog
      list.refetch()
    }
  }

  const confirmDelete = async () => {
    if (!deleting) return
    setDeleteBusy(true)
    try {
      await api.del(`/api/children/${deleting.row.id}`)
      feedback.deleted('child', deleting.row.name)
      setDeleting(null)
      list.refetch()
    } catch (error) {
      toast(error) // dialog stays open; user retries or cancels
    } finally {
      setDeleteBusy(false)
    }
  }

  const columns: TableProps<Child>['columns'] = [
    {
      title: t('master:children.name'),
      dataIndex: 'name',
      key: 'name',
      sorter: true,
      sortOrder:
        list.filters.sort === 'name'
          ? list.filters.dir === 'desc'
            ? 'descend'
            : 'ascend'
          : null,
      width: 180,
    },
    {
      title: t('master:children.age'),
      dataIndex: 'age',
      key: 'age',
      align: 'right',
      className: 'kv-num',
      width: 70,
    },
    {
      title: t('master:children.school'),
      dataIndex: 'school',
      key: 'school',
      ellipsis: true,
      width: 160,
    },
    {
      title: t('master:children.motherName'),
      dataIndex: 'mother_name',
      key: 'mother_name',
      ellipsis: true,
    },
    {
      title: t('master:children.motherWa'),
      dataIndex: 'mother_wa',
      key: 'mother_wa',
      className: 'kv-num',
    },
    {
      title: t('master:status'),
      key: 'is_active',
      width: 110,
      render: (_, row) =>
        row.is_active ? (
          <StatusPill tone="positive">{t('common:status.active')}</StatusPill>
        ) : (
          <StatusPill>{t('common:status.inactive')}</StatusPill>
        ),
    },
    ...(canMutate
      ? [
          {
            title: t('common:columns.actions'),
            key: 'actions',
            width: 122,
            fixed: 'right' as const,
            render: (_: unknown, row: Child) => (
              <RowActions>
                <RowAction
                  label={t('common:actions.edit')}
                  icon={<EditOutlined />}
                  onClick={() => {
                    setEditing(row)
                    setFormOpen(true)
                  }}
                />
                <RowAction
                  accent={!row.is_active}
                  label={t(
                    row.is_active ? 'master:children.deactivate' : 'master:children.activate',
                  )}
                  icon={row.is_active ? <StopOutlined /> : <CheckCircleOutlined />}
                  loading={togglingId === row.id}
                  onClick={() => {
                    void toggleActive(row)
                  }}
                />
                <RowActionsDivider />
                <RowAction
                  danger
                  label={t('common:actions.delete')}
                  icon={<DeleteOutlined />}
                  onClick={() => {
                    void startDelete(row)
                  }}
                />
              </RowActions>
            ),
          },
        ]
      : []),
  ]

  const openCreate = () => {
    setEditing(null)
    setFormOpen(true)
  }

  return (
    <>
      <PageHeader
        title={t('master:children.title')}
        description={
          canMutate ? (
            <>
              {t('master:children.disambiguate')} {t('master:children.inactiveHint')}
            </>
          ) : (
            t('master:children.readonly')
          )
        }
        actions={
          <>
            <RefreshButton onRefresh={list.refetch} loading={list.loading} />
            {canMutate ? (
              <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
                {t('master:children.new')}
              </Button>
            ) : null}
          </>
        }
      />
      <FilterBar
        activeCount={[
          list.filters.q,
          list.filters.is_active === undefined ? undefined : list.filters.is_active,
        ].filter((value) => value !== undefined && value !== '').length}
        onClear={() => {
          setSearch('')
          list.setFilters({ q: undefined, is_active: undefined })
        }}
      >
        <FilterField label={t('common:actions.search')} htmlFor="child-search" width={260}>
          <Input.Search
            id="child-search"
            allowClear
            aria-label={t('master:children.searchPlaceholder')}
            placeholder={t('master:children.searchPlaceholder')}
            value={search}
            onChange={(event) => {
              const value = event.target.value
              setSearch(value)
              if (value === '') list.setFilters({ q: undefined })
            }}
            onSearch={(value) => list.setFilters({ q: value.trim() || undefined })}
          />
        </FilterField>
        <FilterField label={t('master:status')} htmlFor="child-status-filter" width={180}>
          <Select
            id="child-status-filter"
            allowClear
            aria-label={t('common:filters.allStatuses')}
            placeholder={t('common:filters.allStatuses')}
            options={[
              { value: true, label: t('common:status.active') },
              { value: false, label: t('common:status.inactive') },
            ]}
            value={(list.filters.is_active as boolean | undefined) ?? undefined}
            onChange={(value) => list.setFilters({ is_active: value })}
          />
        </FilterField>
      </FilterBar>
      {list.error ? (
        <Alert
          className="kv-list-error"
          type="error"
          showIcon
          message={t(`common:errors.${list.error}`)}
          action={
            <Button size="small" onClick={list.refetch}>
              {t('common:actions.retry')}
            </Button>
          }
        />
      ) : (
        <>
          <DataTable<Child>
            rowKey="id"
            loading={list.loading}
            columns={columns}
            dataSource={list.rows}
            pagination={false}
            scroll={{ x: 1010 }}
            empty={
              <EmptyState
                title={t('master:empty.children')}
                description={t('common:emptyState.hint')}
                action={
                  canMutate ? (
                    <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
                      {t('common:emptyState.first')}
                    </Button>
                  ) : undefined
                }
              />
            }
            onChange={onTableChange}
          />
          <Pager
            page={list.page}
            perPage={list.perPage}
            total={list.total}
            onPage={list.setPage}
            onPerPage={list.setPerPage}
          />
        </>
      )}
      <ChildrenForm
        open={formOpen}
        record={editing}
        onClose={() => setFormOpen(false)}
        onSaved={list.refetch}
      />
      <ConfirmDialog
        open={deleting !== null}
        title={t('master:delete.confirmTitle', { name: deleting?.row.name ?? '' })}
        typedText={deleting?.row.name}
        danger
        loading={deleteBusy}
        onConfirm={() => {
          void confirmDelete()
        }}
        onCancel={() => setDeleting(null)}
      >
        {deleting && deleting.sessions.length > 0 ? (
          <Alert
            style={{ marginBottom: 12 }}
            type="warning"
            showIcon
            message={t('master:children.deleteSessionsWarning', {
              sessions: deleting.sessions
                .map(
                  (session) =>
                    `${session.name} (${t(`common:status.${session.status.toLowerCase()}`)})`,
                )
                .join(', '),
            })}
          />
        ) : null}
        <Typography.Paragraph>
          {t('master:children.deleteCounts', {
            members: deleting?.counts.members ?? 0,
            assessments: deleting?.counts.assessments ?? 0,
            photos: deleting?.counts.photos ?? 0,
            narratives: deleting?.counts.narratives ?? 0,
            missions: deleting?.counts.missions ?? 0,
            links: deleting?.counts.links ?? 0,
          })}
        </Typography.Paragraph>
        <Typography.Paragraph>{t('common:confirm.irreversible')}</Typography.Paragraph>
      </ConfirmDialog>
    </>
  )
}
