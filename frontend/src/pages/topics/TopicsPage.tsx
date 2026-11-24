/**
 * Topics master (spec F-04 / wave F3) — mirrors the Programs reference page:
 * server-side paged list (`api.list` + `Pager`, 02§3 decision 25), search +
 * program filter + column sort (allowlist sort_order|name|id, DEFAULT
 * `sort_order asc`; any change resets page=1), SSE `topic.changed` →
 * same-params refetch via `registerRefetch` (StreamGate owns debounce +
 * rev-gate; no polling), create/edit modal with the badge both-or-neither
 * client mirror (multipart upload jpg/jpeg/png ≤50 MB, ICO rejected —
 * shared gate `lib/validation`, spec 09 2026-11-10),
 * typed-text delete, and activate/deactivate via partial PATCH `{is_active}`.
 *
 * Wire contract (B1, 2026-10-09): POST/PATCH `/api/topics` = multipart
 * form-data. POST: `program_id` (required), `name`, `description`,
 * `sort_order` (key appended only when a number was entered — omitted =
 * auto-append), `badge_name`, `badge_image` (only a newly chosen file).
 * PATCH is partial (omitted key = untouched): `name`, `description`,
 * `sort_order` (only when set), `badge_name`, `badge_image`, `is_active` —
 * `program_id` is NOT editable and is never sent (the edit form's program
 * Select is disabled). `badge_name:""` + no file clears the badge;
 * badge_name set + no file keeps an existing image. Errors branch on
 * `ApiError.code` — NOT_FOUND/VALIDATION show inline in the modal,
 * row-action failures toast `errors.<CODE>`.
 */
import { useEffect, useState } from 'react'
import {
  Alert,
  App as AntApp,
  Avatar,
  Button,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Typography,
  Upload,
  type TableProps,
  type UploadFile,
} from 'antd'
import {
  CheckCircleOutlined,
  DeleteOutlined,
  EditOutlined,
  PlusOutlined,
  StopOutlined,
  UploadOutlined,
} from '@ant-design/icons'
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
import { badgePairValid, MAX_NOTE, MAX_SHORT_TEXT } from '../../lib/validation'
import {
  IMAGE_ACCEPT,
  isAcceptedImage,
  usePagedList,
  useProgramOptions,
  type NameOption,
} from '../masters/shared'

interface Topic {
  id: number
  program_id: number
  program_name: string
  name: string
  description: string | null
  sort_order: number
  badge_name: string | null
  badge_image_url: string | null
  is_active: boolean
}

const DEFAULT_SORT: ListParams = { sort: 'sort_order', dir: 'asc' }

interface TopicFormValues {
  program_id?: number
  name?: string
  description?: string
  sort_order?: number | null
  badge_name?: string
  badge_image?: UploadFile[]
}

interface TopicFormProps {
  open: boolean
  record: Topic | null
  /** Current program filter — prefills the program Select on create. */
  programFilter?: number
  programOptions: NameOption[]
  onClose: () => void
  onSaved: () => void
}

function TopicForm({
  open,
  record,
  programFilter,
  programOptions,
  onClose,
  onSaved,
}: TopicFormProps) {
  const { t } = useTranslation(['master', 'common'])
  const { message } = AntApp.useApp()
  const feedback = useFeedback()
  const [form] = Form.useForm<TopicFormValues>()
  const [saving, setSaving] = useState(false)
  const [submitError, setSubmitError] = useState<string | null>(null)

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
        description: record.description ?? '',
        sort_order: record.sort_order,
        badge_name: record.badge_name ?? '',
        badge_image: record.badge_image_url
          ? [{ uid: '-1', name: 'badge', status: 'done', url: record.badge_image_url }]
          : [],
      })
    } else if (programFilter != null) {
      form.setFieldsValue({ program_id: programFilter })
    }
  }, [open, record, programFilter, form])

  const onFinish = async (values: TopicFormValues) => {
    const name = (values.name ?? '').trim()
    const badgeName = (values.badge_name ?? '').trim()
    const badgeFiles = values.badge_image ?? []
    const file = badgeFiles[0]?.originFileObj
    if (!badgePairValid(badgeName, badgeFiles.length > 0)) {
      form.setFields([
        { name: 'badge_name', errors: [t('master:badge.bothOrNeither')] },
        { name: 'badge_image', errors: [t('master:badge.bothOrNeither')] },
      ])
      return
    }
    if (file && !isAcceptedImage(file)) {
      form.setFields([{ name: 'badge_image', errors: [t('master:badge.imageHint')] }])
      return
    }
    setSaving(true)
    setSubmitError(null)
    try {
      const body = new FormData()
      body.append('name', name)
      body.append('description', (values.description ?? '').trim())
      if (values.sort_order != null) body.append('sort_order', String(values.sort_order))
      body.append('badge_name', badgeName)
      if (file) body.append('badge_image', file)
      if (record) await api.patch(`/api/topics/${record.id}`, undefined, { form: body })
      else {
        // program_id is create-only; the edit form's Select is disabled.
        body.append('program_id', String(values.program_id))
        await api.post('/api/topics', undefined, { form: body })
      }
      if (record) feedback.saved('topic', name)
      else feedback.created('topic', name)
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
          ? t('master:editTitle', { name: t('master:topics.title') })
          : t('master:topics.new')
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
          label={t('master:topics.program')}
          rules={[{ required: true }]}
        >
          <Select
            disabled={record !== null}
            showSearch
            optionFilterProp="label"
            options={programOptions}
            placeholder={t('master:topics.programPlaceholder')}
          />
        </Form.Item>
        <Form.Item
          name="name"
          label={t('master:topics.name')}
          extra={t('master:topics.nameHint')}
          rules={[{ required: true }]}
        >
          <Input
            maxLength={MAX_SHORT_TEXT}
            showCount
            placeholder={t('master:topics.namePlaceholder')}
          />
        </Form.Item>
        <Form.Item
          name="description"
          label={t('master:topics.description')}
          extra={t('master:topics.descriptionHint')}
        >
          <Input.TextArea
            rows={3}
            maxLength={MAX_NOTE}
            showCount
            placeholder={t('master:topics.descriptionPlaceholder')}
          />
        </Form.Item>
        <Form.Item
          name="sort_order"
          label={t('master:topics.order')}
          extra={t('master:topics.orderAuto')}
        >
          <InputNumber min={0} precision={0} style={{ width: '100%' }} />
        </Form.Item>
        <Form.Item label={t('master:badge.title')} style={{ marginBottom: 0 }}>
          <Form.Item
            name="badge_name"
            label={t('master:badge.name')}
            style={{ marginBottom: 12 }}
          >
            <Input maxLength={MAX_SHORT_TEXT} />
          </Form.Item>
          <Form.Item
            name="badge_image"
            label={t('master:badge.image')}
            extra={t('master:badge.imageHint')}
            style={{ marginBottom: 0 }}
            getValueFromEvent={(info: unknown) =>
              Array.isArray(info)
                ? info
                : ((info as { fileList?: UploadFile[] } | undefined)?.fileList ?? [])
            }
          >
            <Upload
              accept={IMAGE_ACCEPT}
              maxCount={1}
              listType="picture"
              beforeUpload={(file) => {
                if (!isAcceptedImage(file)) {
                  message.error(t('master:badge.imageHint'))
                  return Upload.LIST_IGNORE
                }
                return false // hold in the form; uploaded on submit
              }}
            >
              <Button icon={<UploadOutlined />}>{t('common:actions.upload')}</Button>
            </Upload>
          </Form.Item>
        </Form.Item>
      </Form>
    </Modal>
  )
}

export default function TopicsPage() {
  const { t } = useTranslation(['master', 'common'])
  const toast = useErrorToast()
  const feedback = useFeedback()
  const list = usePagedList<Topic>('/api/topics', 'topic.changed', DEFAULT_SORT)
  const programOptions = useProgramOptions()
  const [search, setSearch] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Topic | null>(null)
  const [deleting, setDeleting] = useState<Topic | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [togglingId, setTogglingId] = useState<number | null>(null)

  const programFilter = (list.filters.program_id as number | undefined) ?? undefined

  const onTableChange: TableProps<Topic>['onChange'] = (_pagination, _filters, sorter) => {
    const single = Array.isArray(sorter) ? sorter[0] : sorter
    if (!single || single.order == null) list.setFilters(DEFAULT_SORT)
    else
      list.setFilters({
        sort: String(single.field),
        dir: single.order === 'descend' ? 'desc' : 'asc',
      })
  }

  const toggleActive = async (row: Topic) => {
    setTogglingId(row.id)
    const next = !row.is_active
    try {
      const body = new FormData()
      body.append('is_active', String(next))
      await api.patch(`/api/topics/${row.id}`, undefined, { form: body })
      feedback.changed('topic', row.name, next ? 'activated' : 'deactivated')
      list.refetch()
    } catch (error) {
      toast(error)
    } finally {
      setTogglingId(null)
    }
  }

  const confirmDelete = async () => {
    if (!deleting) return
    setDeleteBusy(true)
    try {
      await api.del(`/api/topics/${deleting.id}`)
      feedback.deleted('topic', deleting.name)
      setDeleting(null)
      list.refetch()
    } catch (error) {
      toast(error) // NOT_FOUND (row already gone elsewhere) → toast + user cancels
    } finally {
      setDeleteBusy(false)
    }
  }

  const columns: TableProps<Topic>['columns'] = [
    {
      title: t('master:topics.order'),
      dataIndex: 'sort_order',
      key: 'sort_order',
      align: 'right',
      className: 'kv-num',
      sorter: true,
      sortOrder:
        list.filters.sort === 'sort_order'
          ? list.filters.dir === 'desc'
            ? 'descend'
            : 'ascend'
          : null,
      width: 90,
    },
    {
      title: t('master:topics.name'),
      dataIndex: 'name',
      key: 'name',
      sorter: true,
      sortOrder:
        list.filters.sort === 'name'
          ? list.filters.dir === 'desc'
            ? 'descend'
            : 'ascend'
          : null,
      width: 220,
    },
    {
      title: t('master:topics.description'),
      dataIndex: 'description',
      key: 'description',
      ellipsis: true,
      width: 240,
      render: (value: string | null) => value || '—',
    },
    {
      title: t('master:topics.program'),
      dataIndex: 'program_name',
      key: 'program_name',
      width: 180,
    },
    {
      title: t('master:badge.title'),
      key: 'badge',
      width: 180,
      render: (_, row) =>
        row.badge_name ? (
          <Space size={8}>
            <Avatar size={28} shape="square" src={row.badge_image_url ?? undefined} />
            <Typography.Text>{row.badge_name}</Typography.Text>
          </Space>
        ) : (
          <Typography.Text type="secondary">—</Typography.Text>
        ),
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
    {
      title: t('common:columns.actions'),
      key: 'actions',
      width: 122,
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
          <RowAction
            accent={!row.is_active}
            label={t(row.is_active ? 'master:actions.deactivate' : 'master:actions.activate')}
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
            onClick={() => setDeleting(row)}
          />
        </RowActions>
      ),
    },
  ]

  return (
    <>
      <PageHeader
        title={t('master:topics.title')}
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
              {t('master:topics.new')}
            </Button>
          </>
        }
      />
      <FilterBar
        activeCount={[list.filters.q, programFilter].filter(
          (value) => value !== undefined && value !== '',
        ).length}
        onClear={() => {
          setSearch('')
          list.setFilters({ q: undefined, program_id: undefined })
        }}
      >
        <FilterField label={t('common:actions.search')} htmlFor="topic-search" width={260}>
          <Input.Search
            id="topic-search"
            allowClear
            aria-label={t('master:topics.searchPlaceholder')}
            placeholder={t('master:topics.searchPlaceholder')}
            value={search}
            onChange={(event) => {
              const value = event.target.value
              setSearch(value)
              if (value === '') list.setFilters({ q: undefined })
            }}
            onSearch={(value) => list.setFilters({ q: value.trim() || undefined })}
          />
        </FilterField>
        <FilterField label={t('master:topics.program')} htmlFor="topic-program-filter" width={220}>
          <Select
            id="topic-program-filter"
            allowClear
            showSearch
            optionFilterProp="label"
            aria-label={t('common:filters.allPrograms')}
            placeholder={t('common:filters.allPrograms')}
            value={programFilter}
            options={programOptions}
            onChange={(value) => list.setFilters({ program_id: value })}
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
          <DataTable<Topic>
            rowKey="id"
            loading={list.loading}
            columns={columns}
            dataSource={list.rows}
            pagination={false}
            scroll={{ x: 920 }}
            empty={
              <EmptyState
                title={t('master:empty.topics')}
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
      <TopicForm
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
