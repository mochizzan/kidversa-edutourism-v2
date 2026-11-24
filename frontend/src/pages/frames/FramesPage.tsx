/**
 * Frames master (spec F-06, wave F3) — list page + multipart form modal.
 *
 * Wire contract (B1, 2026-10-09):
 * - GET `/api/frames` {page, per_page, q, program_id, sort, dir}; sort
 *   allowlist id|name|created_at, default id asc.
 * - POST `/api/frames` = multipart FormData via `api.upload`: file field
 *   `image` (required), `name`, `program_ids` repeated (plain key, no `[]`).
 * - PATCH `/api/frames/:id` = multipart FormData, partial: `name`,
 *   `program_ids` (replaced wholesale, so always sent), `image` only when a
 *   new file was chosen (omitting keeps the current image).
 * - DELETE `/api/frames/:id` → {deleted:true}. No is_active.
 * - SSE `frame.changed` → same-params refetch via `registerRefetch`
 *   (StreamGate owns debounce + rev-gate; no polling).
 *
 * Error branching on `ApiError.code`: modal failures show the inline Alert,
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
  Modal,
  Select,
  Space,
  Typography,
  Upload,
  type TableProps,
  type UploadFile,
} from 'antd'
import { DeleteOutlined, EditOutlined, PlusOutlined, UploadOutlined } from '@ant-design/icons'
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
  IMAGE_ACCEPT,
  isAcceptedImage,
  usePagedList,
  useProgramOptions,
} from '../masters/shared'

interface FrameProgram {
  id: number
  name: string
}

interface Frame {
  id: number
  name: string
  image_url: string
  programs: FrameProgram[]
}

const DEFAULT_SORT: ListParams = { sort: 'id', dir: 'asc' }

interface FrameFormValues {
  name?: string
  program_ids?: number[]
  frame_image?: UploadFile[]
}

interface FrameFormProps {
  open: boolean
  record: Frame | null
  onClose: () => void
  onSaved: () => void
}

function FrameForm({ open, record, onClose, onSaved }: FrameFormProps) {
  const { t } = useTranslation(['master', 'common'])
  const { message } = AntApp.useApp()
  const feedback = useFeedback()
  const [form] = Form.useForm<FrameFormValues>()
  const programOptions = useProgramOptions()
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
        name: record.name,
        program_ids: record.programs.map((program) => program.id),
        frame_image: [{ uid: '-1', name: record.name, status: 'done', url: record.image_url }],
      })
    }
  }, [open, record, form])

  const onFinish = async (values: FrameFormValues) => {
    const files = values.frame_image ?? []
    const file = files[0]?.originFileObj
    if (!record && files.length === 0) {
      form.setFields([{ name: 'frame_image', errors: [t('master:frames.imageHint')] }])
      return
    }
    if (file && !isAcceptedImage(file)) {
      form.setFields([{ name: 'frame_image', errors: [t('master:frames.imageHint')] }])
      return
    }
    setSaving(true)
    setSubmitError(null)
    try {
      const body = new FormData()
      const name = (values.name ?? '').trim()
      body.append('name', name)
      for (const id of values.program_ids ?? []) body.append('program_ids', String(id))
      if (file) body.append('image', file)
      if (record) {
        await api.patch(`/api/frames/${record.id}`, undefined, { form: body })
        feedback.saved('frame', name)
      } else {
        await api.upload('/api/frames', body, crypto.randomUUID())
        feedback.created('frame', name)
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
          ? t('master:editTitle', { name: t('master:frames.title') })
          : t('master:frames.new')
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
          name="frame_image"
          label={t('master:frames.image')}
          extra={t('master:frames.imageHint')}
          valuePropName="fileList"
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
                message.error(t('master:frames.imageHint'))
                return Upload.LIST_IGNORE
              }
              return false // hold in the form; uploaded on submit
            }}
            onChange={(info) => {
              // A locally chosen file re-prefills `name` from the filename stem
              // (the field stays editable afterwards); the stored-image entry
              // has no originFileObj, so an unchanged edit never re-prefills.
              const latest = info.fileList[info.fileList.length - 1]
              const file = latest?.originFileObj
              if (file) form.setFieldValue('name', file.name.replace(/\.[^.]+$/, ''))
            }}
          >
            <Button icon={<UploadOutlined />}>{t('common:actions.upload')}</Button>
          </Upload>
        </Form.Item>
        <Form.Item
          name="name"
          label={t('master:frames.name')}
          rules={[{ required: true }]}
          extra={record ? undefined : t('master:frames.nameHint')}
        >
          <Input
            maxLength={MAX_SHORT_TEXT}
            showCount
            placeholder={t('master:frames.namePlaceholder')}
          />
        </Form.Item>
        <Form.Item
          name="program_ids"
          label={t('master:frames.programs')}
          extra={t('master:frames.programsHint')}
          rules={[
            { required: true },
            toValueRule(
              (value) =>
                Array.isArray(value) && value.length >= 1
                  ? null
                  : 'master:frames.programsHint',
              t,
            ),
          ]}
        >
          <Select
            mode="multiple"
            options={programOptions}
            allowClear
            showSearch
            optionFilterProp="label"
            placeholder={t('master:frames.programsHint')}
          />
        </Form.Item>
      </Form>
    </Modal>
  )
}

export default function FramesPage() {
  const { t } = useTranslation(['master', 'common'])
  const toast = useErrorToast()
  const feedback = useFeedback()
  const list = usePagedList<Frame>('/api/frames', 'frame.changed', DEFAULT_SORT)
  const programOptions = useProgramOptions()
  const [search, setSearch] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Frame | null>(null)
  const [deleting, setDeleting] = useState<Frame | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)

  const onTableChange: TableProps<Frame>['onChange'] = (_pagination, _filters, sorter) => {
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
      await api.del(`/api/frames/${deleting.id}`)
      feedback.deleted('frame', deleting.name)
      setDeleting(null)
      list.refetch()
    } catch (error) {
      toast(error) // NOT_FOUND (row already gone elsewhere) → toast + user cancels
    } finally {
      setDeleteBusy(false)
    }
  }

  const columns: TableProps<Frame>['columns'] = [
    {
      title: t('master:frames.image'),
      key: 'image',
      width: 80,
      render: (_, row) => <Avatar size={40} shape="square" src={row.image_url} />,
    },
    {
      title: t('master:frames.name'),
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
      title: t('master:frames.programs'),
      key: 'programs',
      render: (_, row) =>
        row.programs.length ? (
          <Space size={4} wrap>
            {row.programs.map((program) => (
              <StatusPill key={program.id}>{program.name}</StatusPill>
            ))}
          </Space>
        ) : (
          <Typography.Text type="secondary">—</Typography.Text>
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
        title={t('master:frames.title')}
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
              {t('master:frames.new')}
            </Button>
          </>
        }
      />
      <FilterBar
        activeCount={[
          list.filters.q,
          list.filters.program_id === undefined ? undefined : list.filters.program_id,
        ].filter((value) => value !== undefined && value !== '').length}
        onClear={() => {
          setSearch('')
          list.setFilters({ q: undefined, program_id: undefined })
        }}
      >
        <FilterField label={t('common:actions.search')} htmlFor="frame-search" width={260}>
          <Input.Search
            id="frame-search"
            allowClear
            aria-label={t('master:frames.searchPlaceholder')}
            placeholder={t('master:frames.searchPlaceholder')}
            value={search}
            onChange={(event) => {
              const value = event.target.value
              setSearch(value)
              if (value === '') list.setFilters({ q: undefined })
            }}
            onSearch={(value) => list.setFilters({ q: value.trim() || undefined })}
          />
        </FilterField>
        <FilterField label={t('master:frames.programs')} htmlFor="frame-program-filter" width={220}>
          <Select
            id="frame-program-filter"
            allowClear
            showSearch
            optionFilterProp="label"
            aria-label={t('common:filters.allPrograms')}
            placeholder={t('common:filters.allPrograms')}
            options={programOptions}
            value={(list.filters.program_id as number | undefined) ?? undefined}
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
          <DataTable<Frame>
            rowKey="id"
            loading={list.loading}
            columns={columns}
            dataSource={list.rows}
            pagination={false}
            scroll={{ x: 800 }}
            empty={
              <EmptyState
                title={t('master:empty.frames')}
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
      <FrameForm
        open={formOpen}
        record={editing}
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
