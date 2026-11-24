/**
 * Profile (F-14 + spec 09): own name + change-password for every role, and
 * the profile photo card — the entry button opens the SELF-CONTAINED
 * PhotoEditorModal (picker/crop/compose live inside it); the page only
 * uploads the composed File via uploadWithProgress (multipart + idempotency
 * key + live progress), DELETEs photoless-again, updates the session user,
 * and toasts. The modal closes AFTER the server answer; a failure keeps it
 * open for a retry with the code on the card's section alert. Password
 * change succeeds → toast + forms reset; the CURRENT tab keeps its session
 * (the server revokes the other refresh rows, other devices re-login).
 */
import { useState } from 'react'
import {
  App as AntApp,
  Alert,
  Avatar,
  Button,
  Card,
  Col,
  Form,
  Input,
  Row,
  Typography,
} from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errorCode, ApiError } from '../../lib/api'
import { useAuth, type User } from '../../lib/auth'
import { avatarUrl } from '../../lib/avatar'
import PageHeader from '../../components/PageHeader'
import PhotoEditorModal from '../../components/profile/PhotoEditorModal'
import { toRule } from '../../lib/validation'
import { checkName, checkPassword } from '../auth/validation'
import '../admin/admin.css'

interface NameForm {
  name: string
}

interface PasswordForm {
  current_password: string
  new_password: string
  confirm: string
}

export default function ProfilePage() {
  const { t } = useTranslation(['auth', 'common'])
  const { user, school, setUser } = useAuth()
  const { message } = AntApp.useApp()
  const [nameForm] = Form.useForm<NameForm>()
  const [passwordForm] = Form.useForm<PasswordForm>()
  const [nameLoading, setNameLoading] = useState(false)
  const [passwordLoading, setPasswordLoading] = useState(false)
  const [nameError, setNameError] = useState<string | null>(null)
  const [passwordError, setPasswordError] = useState<string | null>(null)
  const [photoOpen, setPhotoOpen] = useState(false)
  const [uploading, setUploading] = useState(false)
  const [progress, setProgress] = useState(0)
  const [photoError, setPhotoError] = useState<string | null>(null)

  const openEditor = () => {
    setPhotoError(null)
    setPhotoOpen(true)
  }

  // The editor hands over the composed 512×512 JPEG; uploadWithProgress gives
  // the live 0–100 bar and the honest processing state inside the modal. The
  // modal closes only after the server answers; (data){user} refreshes the
  // session identity (avatar in the rail follows automatically).
  const confirmPhoto = (file: File) => {
    setUploading(true)
    setProgress(0)
    setPhotoError(null)
    const form = new FormData()
    form.append('file', file)
    api
      .uploadWithProgress<{ user: User }>(
        '/api/auth/profile-photo',
        form,
        crypto.randomUUID(),
        (percent) => setProgress(percent),
      )
      .then(
        (data) => {
          setUploading(false)
          setUser(data.user)
          setPhotoOpen(false)
          message.success(t('auth:profile.photoSaved'))
        },
        (cause: unknown) => {
          setUploading(false)
          setProgress(0)
          setPhotoError(errorCode(cause))
        },
      )
  }

  // Only fires for an existing photo (the editor gates that); the idempotent
  // server makes a photo-less DELETE a harmless no-op.
  const deletePhoto = () => {
    api.del<{ user: User }>('/api/auth/profile-photo').then(
      (data) => {
        setUser(data.user)
        setPhotoOpen(false)
        message.success(t('auth:profile.photoDeleted'))
      },
      (cause: unknown) => {
        setPhotoError(errorCode(cause))
      },
    )
  }

  const saveName = (values: NameForm) => {
    setNameLoading(true)
    setNameError(null)
    api.patch<{ user: User }>('/api/auth/profile', { name: values.name.trim() }).then(
      (data) => {
        setUser(data.user)
        setNameLoading(false)
        message.success(t('auth:profile.nameSaved'))
      },
      (cause: unknown) => {
        setNameLoading(false)
        setNameError(errorCode(cause))
      },
    )
  }

  const changePassword = (values: PasswordForm) => {
    setPasswordLoading(true)
    setPasswordError(null)
    api
      .post('/api/auth/change-password', {
        current_password: values.current_password,
        new_password: values.new_password,
      })
      .then(
        () => {
          setPasswordLoading(false)
          passwordForm.resetFields()
          message.success(t('auth:profile.passwordSaved'))
        },
        (cause: unknown) => {
          setPasswordLoading(false)
          if (cause instanceof ApiError && cause.code === 'INVALID_CREDENTIALS') {
            passwordForm.setFields([
              { name: 'current_password', errors: [t('common:errors.INVALID_CREDENTIALS')] },
            ])
          } else {
            setPasswordError(errorCode(cause))
          }
        },
      )
  }

  return (
    <>
      <PageHeader
        title={t('auth:profile.title')}
        description={
          user
            ? `${user.email} · ${t(`common:role.${user.role}`)}${school ? ` · ${school.school_name}` : ''}`
            : undefined
        }
      />

      <Row gutter={[16, 16]} align="top">
        <Col xs={24} sm={12}>
          <Card
            title={
              <Typography.Title level={2} style={{ margin: 0 }}>
                {t('auth:profile.photoTitle')}
              </Typography.Title>
            }
          >
            {photoError ? (
              <Alert
                className="kv-list-error"
                type="error"
                showIcon
                message={t(`common:errors.${photoError}`)}
              />
            ) : null}
            <div className="kv-profile-photo">
              <Avatar size={64} src={user?.photo_url ?? avatarUrl(user?.name ?? '?')}>
                {user?.name.trim().charAt(0).toUpperCase() || '?'}
              </Avatar>
              <Button onClick={openEditor}>{t('auth:profile.photoChange')}</Button>
            </div>
            <PhotoEditorModal
              open={photoOpen}
              hasPhoto={!!user?.photo_url}
              uploading={uploading}
              progress={progress}
              onClose={() => setPhotoOpen(false)}
              onConfirm={confirmPhoto}
              onDelete={deletePhoto}
            />
          </Card>
        </Col>

        <Col xs={24} sm={12}>
          <Card
            title={
              <Typography.Title level={2} style={{ margin: 0 }}>
                {t('auth:profile.nameSection')}
              </Typography.Title>
            }
          >
            <Form<NameForm>
              form={nameForm}
              layout="vertical"
              requiredMark={false}
              initialValues={{ name: user?.name ?? '' }}
              onFinish={saveName}
            >
              <Form.Item label={t('auth:profile.nameLabel')} name="name" rules={[toRule(checkName, t)]}>
                <Input autoComplete="name" />
              </Form.Item>
              {nameError ? (
                <Alert
                  className="kv-list-error"
                  type="error"
                  showIcon
                  message={t(`common:errors.${nameError}`)}
                />
              ) : null}
              <Button type="primary" htmlType="submit" loading={nameLoading}>
                {t('common:actions.save')}
              </Button>
            </Form>
          </Card>
        </Col>

        <Col xs={24} sm={12}>
          <Card
            title={
              <Typography.Title level={2} style={{ margin: 0 }}>
                {t('auth:profile.passwordSection')}
              </Typography.Title>
            }
          >
            <Form<PasswordForm>
              form={passwordForm}
              layout="vertical"
              requiredMark={false}
              onFinish={changePassword}
            >
              <Form.Item
                label={t('auth:profile.currentPassword')}
                name="current_password"
                rules={[toRule(checkPassword, t)]}
              >
                <Input.Password autoComplete="current-password" />
              </Form.Item>
              <Form.Item
                label={t('auth:profile.newPassword')}
                name="new_password"
                rules={[toRule(checkPassword, t)]}
                extra={t('auth:register.passwordHint')}
              >
                <Input.Password autoComplete="new-password" />
              </Form.Item>
              <Form.Item
                label={t('auth:profile.confirmPassword')}
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
              {passwordError ? (
                <Alert
                  className="kv-list-error"
                  type="error"
                  showIcon
                  message={t(`common:errors.${passwordError}`)}
                />
              ) : null}
              <Button type="primary" htmlType="submit" loading={passwordLoading}>
                {t('common:actions.save')}
              </Button>
            </Form>
          </Card>
        </Col>
      </Row>
    </>
  )
}
