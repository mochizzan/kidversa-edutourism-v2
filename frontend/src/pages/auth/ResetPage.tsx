/** Reset (FL-03): email + code + new password; surfaces RESET_EXPIRED /
 * RESET_USED / VALIDATION from the envelope as an inline alert. RESET_USED
 * disambiguates through ONE auto-login with the NEW password (06§3
 * re-login-disambiguation): the password may already have changed, and a
 * session proves the reset worked — the done screen shows instead of a
 * misleading "code used" error. */
import { useState } from 'react'
import { Alert, Button, Form, Input } from 'antd'
import { Link } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { useAuth, type LoginData } from '../../lib/auth'
import AuthLayout from './AuthLayout'
import { MAX_RESET_CODE, toRule } from '../../lib/validation'
import { checkCode, checkEmail, checkPassword } from './validation'

interface ResetForm {
  email: string
  code: string
  new_password: string
}

export default function ResetPage() {
  const { t } = useTranslation(['auth', 'common'])
  const { setSession } = useAuth()
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [done, setDone] = useState(false)

  const onFinish = async (values: ResetForm & { confirm: string }) => {
    setError(null)
    setLoading(true)
    try {
      await api.post('/api/auth/reset', {
        email: values.email.trim().toLowerCase(),
        code: values.code.trim(),
        new_password: values.new_password,
      })
      setDone(true)
    } catch (cause) {
      const code = errorCode(cause)
      if (code === 'RESET_USED') {
        // Re-login disambiguation (06§3): the password may already be changed
        // — log in with the NEW password; a session proves the reset worked.
        try {
          const data = await api.post<LoginData>('/api/auth/login', {
            email: values.email.trim().toLowerCase(),
            password: values.new_password,
          })
          setSession(data)
          setDone(true)
          return
        } catch {
          // Login refused (INVALID_CREDENTIALS) → the code really was used:
          // fall through and show RESET_USED as before.
        }
      }
      setError(code)
      setLoading(false)
    }
  }

  return (
    <AuthLayout
      title={t('auth:reset.title')}
      slide="forgot"
      footer={<Link to="/login">{t('auth:login.submit')}</Link>}
    >
      {done ? (
        <div className="kv-auth__success">
          <Alert type="success" showIcon message={t('auth:reset.success')} />
          <p>
            <Link to="/login">{t('auth:login.submit')}</Link>
          </p>
        </div>
      ) : (
        <Form<ResetForm & { confirm: string }>
          layout="vertical"
          requiredMark={false}
          onFinish={onFinish}
        >
          <Form.Item
            label={t('auth:reset.email')}
            name="email"
            rules={[toRule(checkEmail, t)]}
          >
            <Input type="email" autoComplete="email" autoFocus placeholder="nama@contoh.id" />
          </Form.Item>
          <Form.Item
            label={t('auth:reset.code')}
            name="code"
            rules={[toRule(checkCode, t)]}
            extra={t('auth:reset.codeHint')}
          >
            <Input autoComplete="one-time-code" maxLength={MAX_RESET_CODE} />
          </Form.Item>
          <Form.Item
            label={t('auth:reset.newPassword')}
            name="new_password"
            rules={[toRule(checkPassword, t)]}
            extra={t('auth:register.passwordHint')}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item
            label={t('auth:reset.confirmPassword')}
            name="confirm"
            dependencies={['new_password']}
            rules={[
              { required: true, message: t('auth:validation.required') },
              ({ getFieldValue }) =>
                toRule(
                  (value) =>
                    value && value !== getFieldValue('new_password')
                      ? 'auth:validation.passwordMismatch'
                      : null,
                  t,
                ),
            ]}
          >
            <Input.Password autoComplete="new-password" />
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
            {t('auth:reset.submit')}
          </Button>
        </Form>
      )}
    </AuthLayout>
  )
}
