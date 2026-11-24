/** Create/edit school modal (school_name + country + city + is_active). */
import { useEffect, useState } from 'react'
import { Alert, Form, Input, Modal, Switch } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { useFeedback } from '../../lib/feedback'
import type { SchoolRow } from './SchoolsPage'
import { toRule } from '../../lib/validation'
import { checkRequired } from '../auth/validation'

interface SchoolForm {
  school_name: string
  country: string
  city: string
  is_active: boolean
}

export interface SchoolModalProps {
  open: boolean
  /** null → create mode. */
  school: SchoolRow | null
  onClose: () => void
  onSaved: () => void
}

export default function SchoolModal({ open, school, onClose, onSaved }: SchoolModalProps) {
  const { t } = useTranslation(['auth', 'common'])
  const feedback = useFeedback()
  const [form] = Form.useForm<SchoolForm>()
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (!open) return
    form.resetFields()
    form.setFieldsValue(
      school
        ? {
            school_name: school.school_name,
            country: school.country,
            city: school.city ?? '',
            is_active: school.is_active,
          }
        : { school_name: '', country: 'Indonesia', city: '', is_active: true },
    )
  }, [open, school, form])

  const onFinish = (values: SchoolForm) => {
    setLoading(true)
    setError(null)
    const schoolName = values.school_name.trim()
    const request =
      school === null
        ? api.post<{ school: SchoolRow }>('/api/schools', values)
        : api.patch<{ school: SchoolRow }>(`/api/schools/${school.id}`, values)
    request.then(
      () => {
        if (school === null) feedback.created('school', schoolName)
        else feedback.saved('school', schoolName)
        setLoading(false)
        onSaved()
      },
      (cause: unknown) => {
        setLoading(false)
        setError(errorCode(cause))
      },
    )
  }

  return (
    <Modal
      open={open}
      title={t(school === null ? 'auth:schools.create' : 'auth:schools.edit')}
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
      <Form<SchoolForm> form={form} layout="vertical" requiredMark={false} onFinish={onFinish}>
        <Form.Item
          label={t('auth:schools.schoolName')}
          name="school_name"
          rules={[toRule(checkRequired, t)]}
        >
          <Input autoFocus />
        </Form.Item>
        <Form.Item label={t('auth:schools.country')} name="country">
          <Input />
        </Form.Item>
        <Form.Item label={t('auth:schools.city')} name="city">
          <Input />
        </Form.Item>
        <Form.Item
          label={t('auth:schools.status')}
          name="is_active"
          valuePropName="checked"
        >
          <Switch />
        </Form.Item>
        {error ? (
          <Alert type="error" showIcon message={t(`common:errors.${error}`)} />
        ) : null}
      </Form>
    </Modal>
  )
}
