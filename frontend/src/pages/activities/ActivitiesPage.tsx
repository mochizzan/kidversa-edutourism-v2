/**
 * Activities master (spec F-04) — wave F3 list page, mirrors ProgramsPage:
 * server-side paged list (`api.list` + `Pager`, 02§3 decision 25), search +
 * program/topic filters + column sort (allowlist sort_order|name|id, default
 * sort_order asc; any change resets page=1), SSE `activity.changed` →
 * same-params refetch via `registerRefetch` (StreamGate owns debounce +
 * rev-gate; no polling), create/edit modal, typed-text delete. This entity
 * has NO is_active/badge and NO activate toggle; program_id/topic_id are
 * immutable after create (both Selects disabled in the edit form).
 *
 * Wire contract (B1, 2026-10-09): GET `/api/activities` = {page, per_page, q,
 * program_id, topic_id, sort, dir}. POST = JSON {program_id, topic_id, name,
 * indicator, sort_order?} — topic_id must belong to program_id (server
 * VALIDATION); indicator is always sent as a string; the sort_order key only
 * when a number was entered (omitted = auto-append in the topic). PATCH is
 * partial {name, indicator, sort_order?} — program_id/topic_id are never sent.
 * DELETE `/api/activities/:id`. Errors branch on `ApiError.code` — inline in
 * the modal, row-action failures toast `errors.<CODE>`.
 */
import { useEffect, useState } from 'react'
import {
  Alert,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Typography,
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
import { useErrorToast } from '../../lib/errors'
import { useFeedback } from '../../lib/feedback'
import { MAX_NOTE, MAX_SHORT_TEXT } from '../../lib/validation'
import {
  usePagedList,
  useProgramOptions,
  useTopicOptions,
  type NameOption,
} from '../masters/shared'

interface Activity {
  id: number
  program_id: number
  topic_id: number
  program_name: string
  topic_name: string
  name: string
  indicator: string | null
  sort_order: number
}

const DEFAULT_SORT: ListParams = { sort: 'sort_order', dir: 'asc' }

interface ActivityFormValues {
  program_id?: number
  topic_id?: number
  name?: string
  indicator?: string
  sort_order?: number | null
}

interface ActivityFormProps {
  open: boolean
  record: Activity | null
  /** Current program filter — prefills the program Select on create. */
  programFilter?: number
  programOptions: NameOption[]
  onClose: () => void
  onSaved: () => void
}

function ActivityForm({
  open,
  record,
  programFilter,
  programOptions,
  onClose,
  onSaved,
}: ActivityFormProps) {
  const { t } = useTranslation(['master', 'common'])
  const feedback = useFeedback()
  const [form] = Form.useForm<ActivityFormValues>()
  const [saving, setSaving] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const watched = Form.useWatch('program_id', form)
  const topicOptions = useTopicOptions(watched)

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
        topic_id: record.topic_id,
        name: record.name,
        indicator: record.indicator ?? '',
        sort_order: record.sort_order,
      })
    } else if (programFilter != null) {
      form.setFieldsValue({ program_id: programFilter })
    }
  }, [open, record, programFilter, form])

  const onFinish = async (values: ActivityFormValues) => {
    setSaving(true)
    setSubmitError(null)
    try {
      const name = (values.name ?? '').trim()
      const indicator = (values.indicator ?? '').trim()
      const sortOrder =
        typeof values.sort_order === 'number' ? { sort_order: values.sort_order } : {}
      if (record) {
        await api.patch(`/api/activities/${record.id}`, { name, indicator, ...sortOrder })
        feedback.saved('activity', name)
      } else {
        // program_id/topic_id are create-only; both Selects are disabled on edit.
        await api.post('/api/activities', {
          program_id: values.program_id as number,
          topic_id: values.topic_id as number,
          name,
          indicator,
          ...sortOrder,
        })
        feedback.created('activity', name)
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
          ? t('master:editTitle', { name: t('master:activities.title') })
          : t('master:activities.new')
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
          name="program_id"
          label={t('master:activities.program')}
          rules={[{ required: true }]}
        >
          <Select
            disabled={record !== null}
            showSearch
            optionFilterProp="label"
            placeholder={t('master:activities.programPlaceholder')}
            options={programOptions}
            onChange={() => form.setFieldValue('topic_id', undefined)}
          />
        </Form.Item>
        <Form.Item
          name="topic_id"
          label={t('master:activities.topic')}
          rules={[{ required: true }]}
        >
          <Select
            disabled={record !== null || !watched}
            showSearch
            optionFilterProp="label"
            placeholder={
              watched
                ? t('master:activities.topicPlaceholder')
                : t('master:filters.pickProgramFirst')
            }
            options={topicOptions}
          />
        </Form.Item>
        <Form.Item
          name="name"
          label={t('master:activities.name')}
          extra={t('master:activities.nameHint')}
          rules={[{ required: true }]}
        >
          <Input
            maxLength={MAX_SHORT_TEXT}
            showCount
            placeholder={t('master:activities.namePlaceholder')}
          />
        </Form.Item>
        <Form.Item
          name="indicator"
          label={t('master:activities.indicator')}
          extra={t('master:activities.indicatorHint')}
        >
          <Input.TextArea
            rows={3}
            maxLength={MAX_NOTE}
            showCount
            placeholder={t('master:activities.indicatorPlaceholder')}
          />
        </Form.Item>
        <Form.Item
          name="sort_order"
          label={t('master:activities.order')}
          extra={t('master:activities.orderAuto')}
        >
          <InputNumber min={0} precision={0} style={{ width: '100%' }} />
        </Form.Item>
      </Form>
    </Modal>
  )
}

export default function ActivitiesPage() {
  const { t } = useTranslation(['master', 'common'])
  const toast = useErrorToast()
  const feedback = useFeedback()
  const list = usePagedList<Activity>('/api/activities', 'activity.changed', DEFAULT_SORT)
  const programOptions = useProgramOptions()
  const [search, setSearch] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Activity | null>(null)
  const [deleting, setDeleting] = useState<Activity | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)

  const programFilter =
    typeof list.filters.program_id === 'number' ? list.filters.program_id : undefined
  const topicFilter =
    typeof list.filters.topic_id === 'number' ? list.filters.topic_id : undefined
  const topicOptions = useTopicOptions(programFilter)

  const onTableChange: TableProps<Activity>['onChange'] = (_pagination, _filters, sorter) => {
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
      await api.del(`/api/activities/${deleting.id}`)
      feedback.deleted('activity', deleting.name)
      setDeleting(null)
      list.refetch()
    } catch (error) {
      toast(error) // NOT_FOUND (row already gone elsewhere) → toast + user cancels
    } finally {
      setDeleteBusy(false)
    }
  }

  const columns: TableProps<Activity>['columns'] = [
    {
      title: t('master:activities.order'),
      dataIndex: 'sort_order',
      key: 'sort_order',
      align: 'right',
      className: 'kv-num',
      width: 90,
      sorter: true,
      sortOrder:
        list.filters.sort === 'sort_order'
          ? list.filters.dir === 'desc'
            ? 'descend'
            : 'ascend'
          : null,
    },
    {
      title: t('master:activities.name'),
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
      title: t('master:activities.program'),
      dataIndex: 'program_name',
      key: 'program_name',
      width: 180,
    },
    {
      title: t('master:activities.topic'),
      dataIndex: 'topic_name',
      key: 'topic_name',
      width: 160,
    },
    {
      title: t('master:activities.indicator'),
      dataIndex: 'indicator',
      key: 'indicator',
      ellipsis: true,
      width: 240,
      render: (value: string | null) => value || '—',
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
        title={t('master:activities.title')}
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
              {t('master:activities.new')}
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
        <FilterField label={t('common:actions.search')} htmlFor="activity-search" width={260}>
          <Input.Search
            id="activity-search"
            allowClear
            aria-label={t('master:activities.searchPlaceholder')}
            placeholder={t('master:activities.searchPlaceholder')}
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
          label={t('master:activities.program')}
          htmlFor="activity-program-filter"
          width={220}
        >
          <Select
            id="activity-program-filter"
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
          label={t('master:activities.topic')}
          htmlFor="activity-topic-filter"
          width={220}
        >
          <Select
            id="activity-topic-filter"
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
          <DataTable<Activity>
            rowKey="id"
            loading={list.loading}
            columns={columns}
            dataSource={list.rows}
            pagination={false}
            scroll={{ x: 1000 }}
            empty={
              <EmptyState
                title={t('master:empty.activities')}
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
      <ActivityForm
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
        <Typography.Paragraph>{t('master:delete.counts')}</Typography.Paragraph>
        <Typography.Paragraph>{t('common:confirm.irreversible')}</Typography.Paragraph>
      </ConfirmDialog>
    </>
  )
}
