/**
 * Mission bank master (spec F-05) — wave F3 list page, mirrors ProgramsPage:
 * server-side paged list (`api.list` + `Pager`, 02§3 decision 25), search +
 * program/topic filters + column sort (allowlist id|name|created_at, default
 * id asc; any change resets page=1), SSE `mission.changed` → same-params
 * refetch via `registerRefetch` (StreamGate owns debounce + rev-gate; no
 * polling), create/edit modal, typed-text delete. No is_active, no badge and
 * no sort_order field on this entity. A mission is its name only — the report
 * renders exactly that name, so the form shows a small report preview instead
 * of a separate body field. Topic links are cross-program by contract: the
 * form's topic picker uses ALL school topics labelled `topic · program` so
 * same-named topics across programs stay distinguishable, and `topic_ids`
 * replaces the whole link set on PATCH.
 *
 * Wire contract (B1, 2026-10-09): GET `/api/missions` = {page, per_page, q,
 * program_id, topic_id, sort, dir}. POST = JSON {program_id, name, topic_ids}
 * (≥1 topic, same school — server rejects foreign ids with NOT_FOUND). PATCH
 * sends {program_id, name, topic_ids} — program_id IS editable here (unlike
 * activities/topics). DELETE `/api/missions/:id`. Errors branch on
 * `ApiError.code` — NOT_FOUND/VALIDATION show inline in the modal, row-action
 * failures toast `errors.<CODE>`.
 */
import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Form,
  Input,
  Modal,
  Select,
  Space,
  type TableProps,
} from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined } from '@ant-design/icons'
import { useTranslation } from 'react-i18next'
import { api, errorCode, type ListParams } from '../../lib/api'
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
import { MAX_SHORT_TEXT, toValueRule } from '../../lib/validation'
import {
  usePagedList,
  useProgramOptions,
  useTopicOptions,
  type NameOption,
} from '../masters/shared'

interface MissionTopic {
  topic_id: number
  name: string
}

interface Mission {
  id: number
  program_id: number
  program_name: string
  name: string
  topics: MissionTopic[]
}

const DEFAULT_SORT: ListParams = { sort: 'id', dir: 'asc' }

interface MissionFormValues {
  program_id?: number
  name?: string
  topic_ids?: number[]
}

interface MissionFormProps {
  open: boolean
  record: Mission | null
  /** Current program filter — prefills the program Select on create. */
  programFilter?: number
  programOptions: NameOption[]
  onClose: () => void
  onSaved: () => void
}

function MissionForm({
  open,
  record,
  programFilter,
  programOptions,
  onClose,
  onSaved,
}: MissionFormProps) {
  const { t } = useTranslation(['master', 'common'])
  const feedback = useFeedback()
  const [form] = Form.useForm<MissionFormValues>()
  const missionName = Form.useWatch('name', form)
  const [saving, setSaving] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  // No argument = ALL school topics (cross-program links allowed by B1);
  // ` · program_name` disambiguates same-named topics across programs.
  const topicOptions = useTopicOptions().map((option) => ({
    value: option.value,
    label: `${option.label} · ${option.program_name}`,
  }))

  const [wasOpen, setWasOpen] = useState(open)
  if (wasOpen !== open) {
    // Adjust state during render (ConfirmDialog idiom): a freshly opened
    // modal must never carry the previous submit state.
    setWasOpen(open)
    setSaving(false)
    setSubmitError(null)
  }

  useEffect(() => {
    if (!open) return
    form.resetFields()
    if (record) {
      form.setFieldsValue({
        program_id: record.program_id,
        name: record.name,
        topic_ids: record.topics.map((topic) => topic.topic_id),
      })
    } else if (programFilter != null) {
      form.setFieldsValue({ program_id: programFilter })
    }
  }, [open, record, programFilter, form])

  const onFinish = async (values: MissionFormValues) => {
    setSaving(true)
    setSubmitError(null)
    try {
      // PATCH sends the same three keys (program_id IS editable; topic_ids
      // replaces the whole link set); POST is identical in shape.
      const body = {
        program_id: values.program_id as number,
        name: (values.name ?? '').trim(),
        topic_ids: values.topic_ids ?? [],
      }
      if (record) {
        await api.patch(`/api/missions/${record.id}`, body)
        feedback.saved('mission', body.name)
      } else {
        await api.post('/api/missions', body)
        feedback.created('mission', body.name)
      }
      onClose()
      onSaved()
    } catch (error) {
      setSubmitError(errorCode(error))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Modal
      open={open}
      title={
        record
          ? t('master:editTitle', { name: t('master:missions.title') })
          : t('master:missions.new')
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
        <p className="kv-mission-form__intro">{t('master:missions.formIntro')}</p>
        <Form.Item
          name="name"
          label={t('master:missions.name')}
          extra={t('master:missions.nameHint')}
          rules={[{ required: true }]}
        >
          <Input
            autoFocus
            maxLength={MAX_SHORT_TEXT}
            showCount
            placeholder={t('master:missions.namePlaceholder')}
          />
        </Form.Item>
        <div className="kv-mission-preview">
          <span className="kv-mission-preview__label">
            {t('master:missions.reportPreview')}
          </span>
          <div className="kv-mission-preview__section">
            {t('master:missions.reportSection')}
          </div>
          <ol className="kv-mission-preview__list">
            <li className={missionName?.trim() ? undefined : 'is-empty'}>
              {missionName?.trim() || t('master:missions.reportPreviewEmpty')}
            </li>
          </ol>
          <p className="kv-mission-preview__hint">
            {t('master:missions.reportPreviewHint')}
          </p>
        </div>
        <Form.Item
          name="program_id"
          label={t('master:missions.program')}
          extra={t('master:missions.programHint')}
          rules={[{ required: true }]}
        >
          <Select
            showSearch
            optionFilterProp="label"
            options={programOptions}
            placeholder={t('master:missions.programPlaceholder')}
          />
        </Form.Item>
        <Form.Item
          name="topic_ids"
          label={t('master:missions.topics')}
          extra={`${t('master:missions.topicsHint')} ${t('master:missions.limit')}`}
          rules={[
            { required: true },
            toValueRule(
              (value) =>
                Array.isArray(value) && value.length >= 1
                  ? null
                  : 'master:missions.topicsHint',
              t,
            ),
          ]}
        >
          <Select
            mode="multiple"
            options={topicOptions}
            placeholder={t('master:missions.topicsPlaceholder')}
            allowClear
            showSearch
            optionFilterProp="label"
          />
        </Form.Item>
      </Form>
    </Modal>
  )
}

export default function MissionsPage() {
  const { t } = useTranslation(['master', 'common'])
  const toast = useErrorToast()
  const feedback = useFeedback()
  const list = usePagedList<Mission>('/api/missions', 'mission.changed', DEFAULT_SORT)
  const programOptions = useProgramOptions()
  const [search, setSearch] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Mission | null>(null)
  const [deleting, setDeleting] = useState<Mission | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)

  const programFilter =
    typeof list.filters.program_id === 'number' ? list.filters.program_id : undefined
  const topicFilter =
    typeof list.filters.topic_id === 'number' ? list.filters.topic_id : undefined
  const topicOptions = useTopicOptions(programFilter)

  const onTableChange: TableProps<Mission>['onChange'] = (_pagination, _filters, sorter) => {
    const single = Array.isArray(sorter) ? sorter[0] : sorter
    if (!single || single.order == null) list.setFilters(DEFAULT_SORT)
    else
      list.setFilters({
        sort: String(single.field),
        dir: single.order === 'descend' ? 'desc' : 'asc',
      })
  }

  const confirmDelete = async () => {
    if (!deleting) return
    setDeleteBusy(true)
    try {
      await api.del(`/api/missions/${deleting.id}`)
      feedback.deleted('mission', deleting.name)
      setDeleting(null)
      list.refetch()
    } catch (error) {
      toast(error) // NOT_FOUND (row already gone elsewhere) → toast + user cancels
    } finally {
      setDeleteBusy(false)
    }
  }

  const columns: TableProps<Mission>['columns'] = [
    {
      title: t('master:missions.name'),
      dataIndex: 'name',
      key: 'name',
      width: 200,
      sorter: true,
      sortOrder:
        list.filters.sort === 'name'
          ? list.filters.dir === 'desc'
            ? 'descend'
            : 'ascend'
          : null,
    },
    {
      title: t('master:missions.program'),
      dataIndex: 'program_name',
      key: 'program_name',
      width: 180,
    },
    {
      title: t('master:missions.topics'),
      key: 'topics',
      width: 240,
      render: (_, row) =>
        row.topics.length ? (
          <Space size={4} wrap>
            {row.topics.map((topic) => (
              <StatusPill key={topic.topic_id}>{topic.name}</StatusPill>
            ))}
          </Space>
        ) : (
          <span className="kv-meta">—</span>
        ),
    },
    {
      title: t('common:columns.actions'),
      key: 'actions',
      width: 92,
      fixed: 'right',
      render: (_, row) => (
        <RowActions>
          <RowAction
            label={t('common:actions.edit')}
            icon={<EditOutlined />}
            onClick={() => {
              setEditing(row)
              setFormOpen(true)
            }}
          />
          <RowActionsDivider />
          <RowAction
            danger
            label={t('common:actions.delete')}
            icon={<DeleteOutlined />}
            onClick={() => setDeleting(row)}
          />
        </RowActions>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title={t('master:missions.title')}
        actions={
          <>
            <RefreshButton onRefresh={list.refetch} loading={list.loading} />
            <Button
              type="primary"
              icon={<PlusOutlined />}
              onClick={() => {
                setEditing(null)
                setFormOpen(true)
              }}
            >
              {t('master:missions.new')}
            </Button>
          </>
        }
      />
      <FilterBar
        activeCount={[list.filters.q, programFilter, topicFilter].filter(
          (value) => value !== undefined && value !== '',
        ).length}
        onClear={() => {
          setSearch('')
          list.setFilters({ q: undefined, program_id: undefined, topic_id: undefined })
        }}
        hint={t('master:filters.programTopicHint')}
      >
        <FilterField label={t('common:actions.search')} htmlFor="mission-search" width={260}>
          <Input.Search
            id="mission-search"
            allowClear
            aria-label={t('master:missions.searchPlaceholder')}
            placeholder={t('master:missions.searchPlaceholder')}
            value={search}
            onChange={(event) => {
              const value = event.target.value
              setSearch(value)
              if (value === '') list.setFilters({ q: undefined })
            }}
            onSearch={(value) => list.setFilters({ q: value.trim() || undefined })}
          />
        </FilterField>
        <FilterField
          label={t('master:missions.program')}
          htmlFor="mission-program-filter"
          width={220}
        >
          <Select
            id="mission-program-filter"
            allowClear
            showSearch
            optionFilterProp="label"
            aria-label={t('common:filters.allPrograms')}
            placeholder={t('common:filters.allPrograms')}
            value={programFilter}
            options={programOptions}
            onChange={(value) => list.setFilters({ program_id: value, topic_id: undefined })}
          />
        </FilterField>
        <FilterField
          label={t('master:missions.topics')}
          htmlFor="mission-topic-filter"
          width={220}
        >
          <Select
            id="mission-topic-filter"
            allowClear
            showSearch
            optionFilterProp="label"
            aria-label={t('common:filters.allTopics')}
            placeholder={t('common:filters.allTopics')}
            value={topicFilter}
            options={topicOptions}
            onChange={(value) => list.setFilters({ topic_id: value })}
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
          <DataTable<Mission>
            rowKey="id"
            loading={list.loading}
            columns={columns}
            dataSource={list.rows}
            pagination={false}
            scroll={{ x: 860 }}
            empty={
              <EmptyState
                title={t('master:empty.missions')}
                description={t('common:emptyState.hint')}
                action={
                  <Button
                    type="primary"
                    icon={<PlusOutlined />}
                    onClick={() => {
                      setEditing(null)
                      setFormOpen(true)
                    }}
                  >
                    {t('common:emptyState.first')}
                  </Button>
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
      <MissionForm
        open={formOpen}
        record={editing}
        programFilter={programFilter}
        programOptions={programOptions}
        onClose={() => setFormOpen(false)}
        onSaved={list.refetch}
      />
      <ConfirmDialog
        open={deleting !== null}
        title={t('master:delete.confirmTitle', { name: deleting?.name ?? '' })}
        typedText={deleting?.name}
        danger
        loading={deleteBusy}
        onConfirm={() => {
          void confirmDelete()
        }}
        onCancel={() => setDeleting(null)}
      >
        <p>{t('master:delete.counts')}</p>
        <p>{t('common:confirm.irreversible')}</p>
      </ConfirmDialog>
    </>
  )
}
