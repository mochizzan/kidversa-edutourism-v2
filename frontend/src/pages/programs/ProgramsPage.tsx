/**
 * Programs master (spec F-04 / FL-04) — the reference list page for wave F3:
 * server-side paged list (`api.list` + `Pager`, 02§3 decision 25), search +
 * column sort (allowlist id|name|created_at, default id asc; any change
 * resets page=1), SSE `program.changed` → same-params refetch via
 * `registerRefetch` (StreamGate owns debounce + rev-gate; no polling),
 * create/edit modal with the badge both-or-neither client mirror (multipart
 * upload jpg/jpeg/png ≤50 MB, ICO rejected — shared gate `lib/validation`,
 * spec 09 2026-11-10), typed-text delete, and
 * activate/deactivate via partial PATCH `{is_active}`.
 *
 * Wire contract (B1, 2026-10-09): POST/PATCH `/api/programs` =
 * multipart/form-data {name, description, badge_name, badge_image?, is_active?};
 * `badge_name:""` + no file clears the badge; badge_name set + no file keeps an
 * existing image. Errors branch on `ApiError.code` — NOT_FOUND/VALIDATION show
 * inline in the modal, row-action failures toast `errors.<CODE>`.
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
  Space,
  Tooltip,
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
} from '../masters/shared'

interface Program {
  id: number
  name: string
  description: string | null
  badge_name: string | null
  badge_image_url: string | null
  is_active: boolean
  topic_count: number
  activity_count: number
  eligible: boolean
  created_at: string
}

const DEFAULT_SORT: ListParams = { sort: 'id', dir: 'asc' }

interface ProgramFormValues {
  name?: string
  description?: string
  badge_name?: string
  badge_image?: UploadFile[]
}

interface ProgramFormProps {
  open: boolean
  record: Program | null
  onClose: () => void
  onSaved: () => void
}

function ProgramForm({ open, record, onClose, onSaved }: ProgramFormProps) {
  const { t } = useTranslation(['master', 'common'])
  const { message } = AntApp.useApp()
  const feedback = useFeedback()
  const [form] = Form.useForm<ProgramFormValues>()
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
        description: record.description ?? '',
        badge_name: record.badge_name ?? '',
        badge_image: record.badge_image_url
          ? [{ uid: '-1', name: 'badge', status: 'done', url: record.badge_image_url }]
          : [],
      })
    }
  }, [open, record, form])

  const onFinish = async (values: ProgramFormValues) => {
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
      body.append('badge_name', badgeName)
      if (file) body.append('badge_image', file)
      if (record) await api.patch(`/api/programs/${record.id}`, undefined, { form: body })
      else await api.post('/api/programs', undefined, { form: body })
      if (record) feedback.saved('program', name)
      else feedback.created('program', name)
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
          ? t('master:editTitle', { name: t('master:programs.title') })
          : t('master:programs.new')
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
          label={t('master:programs.name')}
          extra={t('master:programs.nameHint')}
          rules={[{ required: true }]}
        >
          <Input
            maxLength={MAX_SHORT_TEXT}
            showCount
            placeholder={t('master:programs.namePlaceholder')}
          />
        </Form.Item>
        <Form.Item
          name="description"
          label={t('master:programs.description')}
          extra={t('master:programs.descriptionHint')}
        >
          <Input.TextArea
            rows={3}
            maxLength={MAX_NOTE}
            showCount
            placeholder={t('master:programs.descriptionPlaceholder')}
          />
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

export default function ProgramsPage() {
  const { t } = useTranslation(['master', 'common', 'menu'])
  const toast = useErrorToast()
  const feedback = useFeedback()
  const list = usePagedList<Program>('/api/programs', 'program.changed', DEFAULT_SORT)
  const [search, setSearch] = useState('')
  const [formOpen, setFormOpen] = useState(false)
  const [editing, setEditing] = useState<Program | null>(null)
  const [deleting, setDeleting] = useState<Program | null>(null)
  const [deleteBusy, setDeleteBusy] = useState(false)
  const [togglingId, setTogglingId] = useState<number | null>(null)

  const onTableChange: TableProps<Program>['onChange'] = (_pagination, _filters, sorter) => {
    const single = Array.isArray(sorter) ? sorter[0] : sorter
    if (!single || single.order == null) list.setFilters(DEFAULT_SORT)
    else
      list.setFilters({
        sort: String(single.field),
        dir: single.order === 'descend' ? 'desc' : 'asc',
      })
  }

  const toggleActive = async (row: Program) => {
    setTogglingId(row.id)
    const next = !row.is_active
    try {
      const body = new FormData()
      body.append('is_active', String(next))
      await api.patch(`/api/programs/${row.id}`, undefined, { form: body })
      feedback.changed('program', row.name, next ? 'activated' : 'deactivated')
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
      await api.del(`/api/programs/${deleting.id}`)
      feedback.deleted('program', deleting.name)
      setDeleting(null)
      list.refetch()
    } catch (error) {
      toast(error) // NOT_FOUND (row already gone elsewhere) → toast + user cancels
    } finally {
      setDeleteBusy(false)
    }
  }

  const columns: TableProps<Program>['columns'] = [
    {
      title: t('master:programs.name'),
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
      title: t('master:programs.description'),
      dataIndex: 'description',
      key: 'description',
      ellipsis: true,
      width: 240,
      render: (value: string | null) => value || '—',
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
      title: t('menu:topics'),
      dataIndex: 'topic_count',
      key: 'topic_count',
      align: 'right',
      className: 'kv-num',
      width: 80,
    },
    {
      title: t('menu:activities'),
      dataIndex: 'activity_count',
      key: 'activity_count',
      align: 'right',
      className: 'kv-num',
      width: 90,
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
      title: t('master:programs.eligibility'),
      key: 'eligible',
      width: 150,
      render: (_, row) =>
        row.eligible ? (
          <StatusPill tone="positive">{t('master:programs.eligible')}</StatusPill>
        ) : (
          <Tooltip title={t('master:programs.ineligible')}>
            <StatusPill tone="attention">{t('master:programs.notReady')}</StatusPill>
          </Tooltip>
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
        title={t('master:programs.title')}
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
              {t('master:programs.new')}
            </Button>
          </>
        }
      />
      <FilterBar
        activeCount={list.filters.q ? 1 : 0}
        onClear={() => {
          setSearch('')
          list.setFilters({ q: undefined })
        }}
      >
        <FilterField label={t('common:actions.search')} htmlFor="program-search" width={280}>
          <Input.Search
            id="program-search"
            allowClear
            aria-label={t('master:programs.searchPlaceholder')}
            placeholder={t('master:programs.searchPlaceholder')}
            value={search}
            onChange={(event) => {
              const value = event.target.value
              setSearch(value)
              if (value === '') list.setFilters({ q: undefined })
            }}
            onSearch={(value) => list.setFilters({ q: value.trim() || undefined })}
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
          <DataTable<Program>
            rowKey="id"
            loading={list.loading}
            columns={columns}
            dataSource={list.rows}
            pagination={false}
            scroll={{ x: 1020 }}
            empty={
              <EmptyState
                title={t('master:empty.programs')}
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
      <ProgramForm
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
