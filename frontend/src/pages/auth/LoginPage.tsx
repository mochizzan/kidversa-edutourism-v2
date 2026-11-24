/** Login (F-01 / FL-02): email + password → setSession → deep-link target. */
import { useState } from 'react'
import { Alert, Button, Form, Input, Switch } from 'antd'
import { Link, useLocation, useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { useAuth, type LoginData } from '../../lib/auth'
import AuthLayout from './AuthLayout'
import { toRule } from '../../lib/validation'
import { checkEmail, checkRequired } from './validation'

interface LoginForm {
  email: string
  password: string
}

const REMEMBER_KEY = 'kv-remember-email'

/** Best-effort read: private mode / blocked storage must not break sign-in. */
function readRememberedEmail(): string {
  try {
    return localStorage.getItem(REMEMBER_KEY) ?? ''
  } catch {
    return ''
  }
}

function rememberEmail(email: string | null) {
  try {
    if (email) localStorage.setItem(REMEMBER_KEY, email)
    else localStorage.removeItem(REMEMBER_KEY)
  } catch {
    // Storage unavailable — the login itself still succeeds.
  }
}

export default function LoginPage() {
  const { t } = useTranslation(['auth', 'common'])
  const { setSession } = useAuth()
  const navigate = useNavigate()
  const location = useLocation()
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)
  const [remember, setRemember] = useState(() => readRememberedEmail() !== '')
  const [rememberedEmail] = useState(readRememberedEmail)

  const from = (location.state as { from?: string } | null)?.from ?? '/'

  const onFinish = async (values: LoginForm) => {
    setError(null)
    setLoading(true)
    const email = values.email.trim().toLowerCase()
    try {
      const data = await api.post<LoginData>('/api/auth/login', {
        email,
        password: values.password,
      })
      rememberEmail(remember ? email : null)
      setSession(data)
      navigate(from, { replace: true })
    } catch (cause) {
      setError(errorCode(cause))
      setLoading(false)
    }
  }

  return (
    <AuthLayout
      title={t('auth:login.title')}
      subtitle={t('auth:login.subtitle')}
      activeTab="login"
      slide="login"
    >
      <Form<LoginForm>
        layout="vertical"
        requiredMark={false}
        initialValues={{ email: rememberedEmail }}
        onFinish={onFinish}
      >
        <Form.Item label={t('auth:login.email')} name="email" rules={[toRule(checkEmail, t)]}>
          <Input type="email" autoComplete="email" autoFocus placeholder="nama@contoh.id" />
        </Form.Item>
        <Form.Item
          label={t('auth:login.password')}
          name="password"
          rules={[toRule(checkRequired, t)]}
        >
          <Input.Password autoComplete="current-password" />
        </Form.Item>
        {error ? (
          <Alert
            className="kv-auth__alert"
            type="error"
            showIcon
            message={t(`common:errors.${error}`)}
          />
        ) : null}
        <div className="kv-auth__utility">
          <span className="kv-auth__remember">
            <Switch
              size="small"
              checked={remember}
              onChange={setRemember}
              aria-label={t('auth:login.remember')}
            />
            <span>{t('auth:login.remember')}</span>
          </span>
          <Link to="/forgot">{t('auth:login.forgot')}</Link>
        </div>
        <Button type="primary" htmlType="submit" block loading={loading} className="kv-auth__submit">
          {t('auth:login.submit')}
        </Button>
      </Form>
    </AuthLayout>
  )
}
