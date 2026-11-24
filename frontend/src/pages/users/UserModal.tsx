/**
 * Create / edit user modal. Create = POST /api/users {name,email,wa_number,
 * password, role[,school_id]} (SA picks school+role ADMIN|FASILITATOR; ADMIN
 * creates FASILITATOR only, no school field). Edit = PATCH {name,email,
 * wa_number} — decision/is_active/role live on their own routes.
 * EMAIL_TAKEN maps onto the email field; other codes surface as an alert.
 */
import { useEffect, useState } from 'react'
import { Alert, Form, Input, Modal, Select } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errorCode, ApiError } from '../../lib/api'
import { useAuth, type Role, type UserDecision } from '../../lib/auth'
import { useFeedback } from '../../lib/feedback'
import { toRule } from '../../lib/validation'
import { checkEmail, checkName, checkPassword, checkWa } from '../auth/validation'

export interface UserRow {
  id: number
  name: string
  email: string
  role: Role
  decision: UserDecision
  is_active: boolean
  school_id: number | null
  wa_number: string | null
  school_name: string | null
}

interface UserForm {
  name: string
  email: string
  wa_number: string
  password: string
  role: Role
  school_id: number
}

export interface UserModalProps {
  open: boolean
  /** null → create mode. */
  user: UserRow | null
  onClose: () => void
  onSaved: () => void
}

export default function UserModal({ open, user, onClose, onSaved }: UserModalProps) {
  const { t } = useTranslation(['auth', 'common'])
  const { user: me, school } = useAuth()
  const feedback = useFeedback()
  const [form] = Form.useForm<UserForm>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [schools, setSchools] = useState<{ id: number; school_name: string }[] | null>(null)

  const creating = user === null
  const saCreating = creating && me?.role === 'SA'

  useEffect(() => {
    if (!open) return
    form.resetFields()
    if (user) {
      form.setFieldsValue({
        name: user.name,
        email: user.email,
        wa_number: user.wa_number ?? '',
        role: user.role,
      })
    } else {
      form.setFieldsValue({ role: 'FASILITATOR', school_id: school?.id })
    }
  }, [open, user, form, school])

  useEffect(() => {
    if (!open || !saCreating) return
    api.list<{ id: number; school_name: string; is_active: boolean }>('/api/schools', {
      per_page: 1000,
    }).then(
      (result) => setSchools(result.data.filter((row) => row.is_active)),
      () => setSchools([]),
    )
  }, [open, saCreating])

  const onFinish = (values: UserForm) => {
    setLoading(true)
    setError(null)
    const name = values.name.trim()
    const request = user
      ? api.patch<{ user: UserRow }>(`/api/users/${user.id}`, {
          name,
          email: values.email.trim().toLowerCase(),
          wa_number: values.wa_number.trim(),
        })
      : api.post<{ user: UserRow }>('/api/users', {
          name,
          email: values.email.trim().toLowerCase(),
          wa_number: values.wa_number.trim(),
          password: values.password,
          role: values.role,
          ...(saCreating ? { school_id: values.school_id } : {}),
        })
    request.then(
      () => {
        if (user) feedback.saved('user', name)
        else feedback.created('user', name)
        setLoading(false)
        onSaved()
      },
      (cause: unknown) => {
        setLoading(false)
        if (cause instanceof ApiError && cause.code === 'EMAIL_TAKEN') {
          form.setFields([{ name: 'email', errors: [t('common:errors.EMAIL_TAKEN')] }])
        } else {
          setError(errorCode(cause))
        }
      },
    )
  }

  const roleOptions = (me?.role === 'SA'
    ? (['FASILITATOR', 'ADMIN'] as Role[])
    : (['FASILITATOR'] as Role[])
  ).map((role) => ({ value: role, label: t(`common:role.${role}`) }))

  return (
    <Modal
      open={open}
      title={t(creating ? 'auth:users.create' : 'auth:users.edit')}
      okText={t('common:actions.save')}
      cancelText={t('common:actions.cancel')}
      confirmLoading={loading}
      cancelButtonProps={{ disabled: loading }}
      maskClosable={!loading}
      onOk={() => form.submit()}
      onCancel={() => {
        setError(null)
        onClose()
      }}
    >
      <Form<UserForm> form={form} layout="vertical" requiredMark={false} onFinish={onFinish}>
        <Form.Item label={t('auth:users.name')} name="name" rules={[toRule(checkName, t)]}>
          <Input autoFocus />
        </Form.Item>
        <Form.Item label={t('auth:users.email')} name="email" rules={[toRule(checkEmail, t)]}>
          <Input type="email" />
        </Form.Item>
        <Form.Item
          label={t('auth:users.wa')}
          name="wa_number"
          rules={[toRule(checkWa, t)]}
          extra={t('auth:register.waHint')}
        >
          <Input inputMode="numeric" placeholder="081234567890" />
        </Form.Item>
        {creating ? (
          <>
            <Form.Item
              label={t('auth:users.password')}
              name="password"
              rules={[toRule(checkPassword, t)]}
              extra={t('auth:register.passwordHint')}
            >
              <Input.Password autoComplete="new-password" />
            </Form.Item>
            <Form.Item
              label={t('auth:users.role')}
              name="role"
              rules={[{ required: true, message: t('auth:validation.required') }]}
            >
              <Select options={roleOptions} />
            </Form.Item>
            {saCreating ? (
              <Form.Item
                label={t('auth:users.school')}
                name="school_id"
                rules={[{ required: true, message: t('auth:validation.required') }]}
              >
                <Select
                  showSearch
                  optionFilterProp="label"
                  placeholder={t('auth:register.schoolPlaceholder')}
                  options={(schools ?? []).map((row) => ({
                    value: row.id,
                    label: row.school_name,
                  }))}
                />
              </Form.Item>
            ) : null}
          </>
        ) : null}
        {error ? (
          <Alert type="error" showIcon message={t(`common:errors.${error}`)} />
        ) : null}
      </Form>
    </Modal>
  )
}
