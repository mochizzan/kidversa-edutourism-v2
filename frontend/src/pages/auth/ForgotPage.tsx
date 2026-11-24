/** Forgot (FL-03): always answers {sent:true}; the public page shows only
 * "check email / ask admin" — NO countdown (the list endpoint is auth-only). */
import { useState } from 'react'
import { Alert, Button, Form, Input } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import AuthLayout from './AuthLayout'
import { toRule } from '../../lib/validation'
import { checkEmail } from './validation'

export default function ForgotPage() {
  const { t } = useTranslation(['auth', 'common'])
  const [sent, setSent] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  const onFinish = async (values: { email: string }) => {
    setError(null)
    setLoading(true)
    try {
      await api.post<{ sent: true }>('/api/auth/forgot', {
        email: values.email.trim().toLowerCase(),
      })
      setSent(true)
    } catch (cause) {
      setError(errorCode(cause))
      setLoading(false)
    }
  }

  return (
    <AuthLayout
      title={t('auth:forgot.title')}
      subtitle={t('auth:forgot.subtitle')}
      slide="forgot"
      footer={<Link to="/login">{t('auth:login.submit')}</Link>}
    >
      {sent ? (
        <div className="kv-auth__success">
          <Alert type="success" showIcon message={t('auth:forgot.success')} />
          <p>
            <Link to="/reset">{t('auth:forgot.goToReset')}</Link>
          </p>
        </div>
      ) : (
        <Form<{ email: string }> layout="vertical" requiredMark={false} onFinish={onFinish}>
          <Form.Item
            label={t('auth:forgot.email')}
            name="email"
            rules={[toRule(checkEmail, t)]}
          >
            <Input type="email" autoComplete="email" autoFocus placeholder="nama@contoh.id" />
          </Form.Item>
          {error ? (
            <Alert
              className="kv-auth__alert"
              type="error"
              showIcon
              message={t(`common:errors.${error}`)}
            />
          ) : null}
          <Button type="primary" htmlType="submit" block loading={loading} className="kv-auth__submit">
            {t('auth:forgot.submit')}
          </Button>
        </Form>
      )}
    </AuthLayout>
  )
}
