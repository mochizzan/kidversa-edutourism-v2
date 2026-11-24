/**
 * Shared F4 controls: active-child multi-picker with search (session-create
 * groups + group member add), active-facilitator owner picker, and the
 * consent status tag (never colour alone — label always rendered).
 */
import { Select, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { useAuth } from '../../lib/auth'
import { useListLoader } from '../../lib/list'
import StatusPill from '../../components/StatusPill'
import {
  consentStatusLabel,
  type ChildRow,
  type FacilitatorRow,
} from '../../lib/session-contracts'

export interface ChildPickerProps {
  /** Active children (parent fetches once — also used for names in copy flow). */
  rows: ChildRow[]
  value: number[]
  onChange: (childIds: number[]) => void
  /** Children already chosen in another group / already members — disabled with reason. */
  excludeIds?: number[]
  disabled?: boolean
}

export function ChildPicker({
  rows,
  value,
  onChange,
  excludeIds = [],
  disabled = false,
}: ChildPickerProps) {
  const { t } = useTranslation('session')
  const excluded = new Set(excludeIds)
  return (
    <Select
      mode="multiple"
      allowClear
      showSearch
      filterOption={(input, option) =>
        String(option?.label ?? '')
          .toLowerCase()
          .includes(input.toLowerCase())
      }
      value={value}
      onChange={onChange}
      disabled={disabled}
      placeholder={t('form.childrenPlaceholder')}
      maxTagCount="responsive"
      options={rows.map((child) => ({
        value: child.id,
        label: `${child.name} · ${child.school}`,
        // Already chosen elsewhere → blocked (client-side CHILD_DUPLICATE dedup).
        disabled: excluded.has(child.id) && !value.includes(child.id),
      }))}
    />
  )
}

export interface OwnerPickerProps {
  value: number | null
  onChange: (childId: number | null) => void
  allowClear?: boolean
  disabled?: boolean
}

export function OwnerPicker({ value, onChange, allowClear = true, disabled = false }: OwnerPickerProps) {
  const { t } = useTranslation('session')
  const { school } = useAuth()
  const { rows, loading } = useListLoader<FacilitatorRow>('/api/users', {
    role: 'FASILITATOR',
    is_active: true,
    per_page: 1000,
  })
  // SA sees cross-school rows; an owner must belong to the ACTIVE school.
  const own = rows.filter((user) => user.school_id === school?.id)
  return (
    <Select
      showSearch
      allowClear={allowClear}
      filterOption={(input, option) =>
        String(option?.label ?? '')
          .toLowerCase()
          .includes(input.toLowerCase())
      }
      value={value ?? undefined}
      onChange={(next) => onChange(next ?? null)}
      disabled={disabled}
      loading={loading}
      placeholder={t('form.ownerPlaceholder')}
      options={own.map((user) => ({ value: user.id, label: `${user.name} · ${user.email}` }))}
    />
  )
}

/** Inline field-error text (antd danger token = palette coral — never colour alone: it is a message). */
export function FormError({ children }: { children: string }) {
  return (
    <Typography.Text type="danger" style={{ display: 'block', fontSize: 11, lineHeight: '16px', margin: '4px 0 0' }}>
      {children}
    </Typography.Text>
  )
}

export function ConsentTag({ status }: { status: string }) {
  const { t } = useTranslation('session')
  const tone = status === 'SETUJU' ? 'positive' : status === 'DITOLAK' ? 'attention' : 'neutral'
  return <StatusPill tone={tone}>{consentStatusLabel(t, status)}</StatusPill>
}
