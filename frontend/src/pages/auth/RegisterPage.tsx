/** Register (F-01 / FL-01): mirrors server rules, idempotency key auto,
 * pending success state ("menunggu persetujuan admin"). */
import { useEffect, useState } from 'react'
import { Alert, Button, Form, Input, Radio, Select, Spin } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import AuthLayout from './AuthLayout'
import { toRule } from '../../lib/validation'
import { checkEmail, checkName, checkPassword, checkWa } from './validation'

interface SchoolOption {
  id: number
  school_name: string
}

interface RegisterForm {
  name: string
  email: string
  wa_number: string
  password: string
  role: 'ADMIN' | 'FASILITATOR'
  school_id: number
}

export default function RegisterPage() {
  const { t } = useTranslation(['auth', 'common'])
  const [schools, setSchools] = useState<SchoolOption[] | null>(null)
  const [schoolsError, setSchoolsError] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [registered, setRegistered] = useState(false)

  const loadSchools = () => {
    api
      .get<SchoolOption[]>('/api/auth/schools')
      .then((data) => {
        setSchools(data)
        setSchoolsError(false)
      })
      .catch(() => setSchoolsError(true))
  }
  useEffect(() => {
    loadSchools()
  }, [])

  const onFinish = async (values: RegisterForm) => {
    setError(null)
    setLoading(true)
    try {
      await api.post<{ id: number; status: string }>('/api/auth/register', {
        name: values.name.trim(),
        email: values.email.trim().toLowerCase(),
        wa_number: values.wa_number.trim(),
        password: values.password,
        role: values.role,
        school_id: values.school_id,
      })
      setRegistered(true)
    } catch (cause) {
      setError(errorCode(cause))
      setLoading(false)
    }
  }

  if (registered) {
    return (
      <AuthLayout
        title={t('auth:register.title')}
        activeTab="register"
        slide="register"
      >
        <div className="kv-auth__success">
          <p>{t('auth:register.pending')}</p>
          <Link to="/login">
            <Button type="primary" block className="kv-auth__submit">
              {t('auth:register.loginLink')}
            </Button>
          </Link>
        </div>
      </AuthLayout>
    )
  }

  return (
    <AuthLayout
      title={t('auth:register.title')}
      subtitle={t('auth:register.subtitle')}
      activeTab="register"
      slide="register"
    >
      <Form<RegisterForm>
        layout="vertical"
        requiredMark={false}
        onFinish={onFinish}
        initialValues={{ role: 'FASILITATOR' }}
        className="kv-auth__form--pair"
      >
        <Form.Item label={t('auth:register.name')} name="name" rules={[toRule(checkName, t)]}>
          <Input autoComplete="name" autoFocus />
        </Form.Item>
        <Form.Item label={t('auth:register.email')} name="email" rules={[toRule(checkEmail, t)]}>
          <Input type="email" autoComplete="email" placeholder="nama@contoh.id" />
        </Form.Item>
        <Form.Item
          label={t('auth:register.wa')}
          name="wa_number"
          rules={[toRule(checkWa, t)]}
          extra={t('auth:register.waHint')}
        >
          <Input inputMode="numeric" autoComplete="tel" placeholder="081234567890" />
        </Form.Item>
        <Form.Item
          label={t('auth:register.password')}
          name="password"
          rules={[toRule(checkPassword, t)]}
          extra={t('auth:register.passwordHint')}
        >
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Form.Item label={t('auth:register.role')} name="role">
          <Radio.Group
            options={[
              { label: t('auth:register.roleAdmin'), value: 'ADMIN' },
              { label: t('auth:register.roleFacilitator'), value: 'FASILITATOR' },
            ]}
            optionType="button"
          />
        </Form.Item>
        <Form.Item
          label={t('auth:register.school')}
          name="school_id"
          rules={[{ required: true, message: t('auth:validation.required') }]}
        >
          <Select
            showSearch
            optionFilterProp="label"
            placeholder={t('auth:register.schoolPlaceholder')}
            notFoundContent={schools ? t('common:empty') : <Spin size="small" />}
            options={(schools ?? []).map((item) => ({
              value: item.id,
              label: item.school_name,
            }))}
          />
        </Form.Item>
        {schoolsError ? (
          <Alert
            className="kv-auth__alert"
            type="error"
            showIcon
            message={t('common:errors.INTERNAL')}
            action={
              <Button size="small" onClick={loadSchools}>
                {t('common:actions.retry')}
              </Button>
            }
          />
        ) : null}
        {error ? (
          <Alert
            className="kv-auth__alert"
            type="error"
            showIcon
            message={t(`common:errors.${error}`)}
          />
        ) : null}
        <Button type="primary" htmlType="submit" block loading={loading} className="kv-auth__submit">
          {t('auth:register.submit')}
        </Button>
      </Form>
    </AuthLayout>
  )
}
