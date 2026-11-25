/**
 * Draft-only snapshot editor shared by the session detail page and the session
 * list's inline Edit action. The row shape carries every editable field, so
 * the list never needs a detail fetch before opening the form.
 */
import { useState } from 'react'
import { Alert, DatePicker, Form, Input, Modal, Space, TimePicker } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, ApiError, errorCode } from '../../lib/api'
import dayjs from '../../lib/dayjs'
import { validateSessionMeta } from './meta'

/** Fields the PATCH endpoint accepts, compatible with SessionListItem + Detail.session. */
export interface SessionMetaSource {
  id: number
  name: string
  description?: string | null
  date: string
  start_time: string | null
  end_time: string | null
  location: string
}

export interface SessionMetaModalProps {
  session: SessionMetaSource
  onClose: () => void
  /** PATCH succeeded; the owner closes the modal, toasts and refreshes. */
  onSaved: () => void
}

export default function SessionMetaModal({ session, onClose, onSaved }: SessionMetaModalProps) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const [name, setName] = useState(session.name)
  const [description, setDescription] = useState(session.description ?? '')
  const [date, setDate] = useState(session.date)
  const [startTime, setStartTime] = useState(session.start_time?.slice(0, 5) ?? '')
  const [endTime, setEndTime] = useState(session.end_time?.slice(0, 5) ?? '')
  const [location, setLocation] = useState(session.location)
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)

  const save = async () => {
    const next: Record<string, string> = {}
    for (const [field, key] of Object.entries(
      validateSessionMeta({ name, date, location, startTime, endTime }),
    )) {
      if (key) next[field] = t(key)
    }
    setErrors(next)
    if (Object.keys(next).length > 0) return

    setBusy(true)
    setError(null)
    try {
      await api.patch(`/api/sessions/${session.id}`, {
        name: name.trim(),
        description: description.trim() || undefined,
        date,
        start_time: startTime ? `${startTime}:00` : undefined,
        end_time: endTime ? `${endTime}:00` : undefined,
        location: location.trim(),
      })
      onSaved()
    } catch (failure: unknown) {
      setError(failure)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open
      title={t('detail.editTitle')}
      okText={tc('actions.save')}
      cancelText={tc('actions.cancel')}
      confirmLoading={busy}
      maskClosable={!busy}
      onOk={save}
      onCancel={onClose}
    >
      <Form layout="vertical">
        <Form.Item
          label={t('list.name')}
          validateStatus={errors.name ? 'error' : undefined}
          help={errors.name}
        >
          <Input value={name} onChange={(event) => setName(event.target.value)} />
        </Form.Item>
        <Form.Item label={t('form.description')}>
          <Input.TextArea
            rows={3}
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Form.Item>
        <Form.Item
          label={t('list.date')}
          validateStatus={errors.date ? 'error' : undefined}
          help={errors.date}
        >
          <DatePicker
            value={date ? dayjs(date, 'YYYY-MM-DD') : null}
            onChange={(value) => setDate(value ? value.format('YYYY-MM-DD') : '')}
            format="YYYY-MM-DD"
            style={{ width: '100%' }}
          />
        </Form.Item>
        <Space size="middle">
          <Form.Item label={t('form.startTime')} style={{ marginBottom: 0 }}>
            <TimePicker
              value={startTime ? dayjs(startTime, 'HH:mm') : null}
              onChange={(value) => setStartTime(value ? value.format('HH:mm') : '')}
              format="HH:mm"
              style={{ width: 120 }}
            />
          </Form.Item>
          <Form.Item
            label={t('form.endTime')}
            style={{ marginBottom: 0 }}
            validateStatus={errors.endTime ? 'error' : undefined}
            help={errors.endTime}
          >
            <TimePicker
              value={endTime ? dayjs(endTime, 'HH:mm') : null}
              onChange={(value) => setEndTime(value ? value.format('HH:mm') : '')}
              format="HH:mm"
              style={{ width: 120 }}
            />
          </Form.Item>
        </Space>
        <Form.Item
          label={t('list.location')}
          validateStatus={errors.location ? 'error' : undefined}
          help={errors.location}
        >
          <Input value={location} onChange={(event) => setLocation(event.target.value)} />
        </Form.Item>
        {error ? (
          <Alert
            type="error"
            showIcon
            message={
              error instanceof ApiError && error.code === 'SESSION_LOCKED'
                ? t('detail.locked')
                : tc(`errors.${errorCode(error)}`)
            }
          />
        ) : null}
      </Form>
    </Modal>
  )
}
