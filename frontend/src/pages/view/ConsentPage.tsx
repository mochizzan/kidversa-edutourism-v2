/**
 * Public consent view (spec F-10, FL-15): `/view/consent?token=` — no auth.
 * Fetches `GET /public/consent?token=`; renders the session/child summary plus
 * the respondent form (name required, Setuju/Tolak). First-wins: once
 * `status !== 'PENDING'` the recorded decision is shown and the buttons are
 * disabled — `POST /public/consent/respond` (api.post attaches the
 * idempotency key) succeeds identically for `already:true`. A page-level
 * public stream renders `consent.ack {decision}` without a refresh;
 * `link.revoked` is terminal and closes the stream. Inline error codes.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useSearchParams } from 'react-router'
import { useTranslation } from 'react-i18next'
import { Alert, Button, Card, Input, Result, Space, Typography } from 'antd'
import PageHeader from '../../components/PageHeader'
import PageSkeleton from '../../components/PageSkeleton'
import { api, ApiError } from '../../lib/api'
import { usePublicStream } from '../../lib/list'
import { MAX_RESPONDENT_NAME } from '../../lib/validation'

interface ConsentData {
  session: { name: string; date: string; location: string }
  child: { name: string; school: string }
  status: string
  respondent_name: string | null
  responded_at: string | null
}

export default function ConsentPage() {
  const { t } = useTranslation()
  const [params] = useSearchParams()
  const token = params.get('token')

  const [data, setData] = useState<ConsentData | null>(null)
  const [error, setError] = useState<ApiError | null>(null)
  const [invalid, setInvalid] = useState(false)
  const [respondent, setRespondent] = useState('')
  const [formError, setFormError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState<'SETUJU' | 'DITOLAK' | null>(null)
  const revokedRef = useRef(false)

  const load = useCallback(async () => {
    try {
      const consent = await api.get<ConsentData>('/public/consent', { token })
      if (revokedRef.current) return
      setData(consent)
      setError(null)
    } catch (e) {
      if (revokedRef.current) return
      const err = e instanceof ApiError ? e : new ApiError('INTERNAL', String(e))
      if (err.code === 'TOKEN_INVALID') {
        revokedRef.current = true
        setInvalid(true)
      } else {
        setError(err)
      }
    }
  }, [token])

  useEffect(() => {
    if (token) void load()
  }, [token, load])

  usePublicStream(
    invalid ? null : token,
    {
      'consent.ack': (payload) => {
        const decision = payload.decision
        if (decision === 'SETUJU' || decision === 'DITOLAK') {
          setData((prev) => (prev ? { ...prev, status: decision } : prev)) // render, no refresh
        }
      },
    },
    () => {
      revokedRef.current = true
      setInvalid(true) // flips the token arg → the hook closes the stream
    },
  )

  const respond = async (decision: 'SETUJU' | 'DITOLAK') => {
    const name = respondent.trim()
    if (name === '') {
      setFormError(t('report:public.consentNameRequired'))
      return
    }
    setSubmitting(decision)
    setFormError(null)
    try {
      const result = await api.post<{ decision: string; already?: boolean }>('/public/consent/respond', {
        token,
        decision,
        respondent_name: name,
      })
      setData((prev) =>
        prev
          ? { ...prev, status: result.decision, respondent_name: result.already ? prev.respondent_name : name }
          : prev,
      )
    } catch (e) {
      setFormError(
        e instanceof ApiError ? t(`errors.${e.code}`, { defaultValue: e.message }) : t('errors.INTERNAL'),
      )
    } finally {
      setSubmitting(null)
    }
  }

  if (!token || invalid) {
    return <Result status="404" title={t('report:public.invalidLink')} />
  }

  if (!data) {
    if (error) {
      return (
        <Result
          status="error"
          title={t(`errors.${error.code}`, { defaultValue: error.message })}
          extra={
            <Button onClick={() => { setError(null); void load() }}>{t('actions.retry')}</Button>
          }
        />
      )
    }
    return <PageSkeleton variant="document" />
  }

  const decided = data.status !== 'PENDING'
  const decisionLabel =
    data.status === 'SETUJU'
      ? t('status.approved')
      : data.status === 'DITOLAK'
        ? t('status.rejected')
        : t('status.pending')

  return (
    <div className="kv-content" style={{ maxWidth: 640, margin: '0 auto' }}>
      <PageHeader
        title={t('report:public.consentTitle')}
        description={`${data.session.name} · ${data.session.date}`}
      />
      <Card style={{ marginBottom: 16 }} title={t('report:public.sessionInfo')}>
        <div>
          <Typography.Text type="secondary">{t('report:public.date')}: </Typography.Text>
          {data.session.date}
        </div>
        <div>
          <Typography.Text type="secondary">{t('report:public.location')}: </Typography.Text>
          {data.session.location || '—'}
        </div>
        <div style={{ marginTop: 8 }}>
          <Typography.Text type="secondary">{t('report:public.child')}: </Typography.Text>
          {data.child.name}
        </div>
        <div>
          <Typography.Text type="secondary">{t('report:public.school')}: </Typography.Text>
          {data.child.school || '—'}
        </div>
      </Card>

      <Card title={t('report:public.consentQuestion')}>

        {decided && (
          <Alert
            type="info"
            showIcon
            style={{ marginBottom: 12 }}
            message={t('report:public.consentAlready', { decision: decisionLabel })}
            description={
              <>
                {data.respondent_name && (
                  <div>
                    {t('report:public.consentName')}: {data.respondent_name}
                  </div>
                )}
                {data.responded_at && (
                  <Typography.Text type="secondary">{data.responded_at}</Typography.Text>
                )}
              </>
            }
          />
        )}

        <Space direction="vertical" size={12} style={{ width: '100%' }}>
          <Input
            value={respondent}
            onChange={(e) => { setRespondent(e.target.value); setFormError(null) }}
            placeholder={t('report:public.consentNamePlaceholder')}
            aria-label={t('report:public.consentName')}
            disabled={decided || submitting !== null}
            maxLength={MAX_RESPONDENT_NAME}
          />
          {formError && <Alert type="error" showIcon message={formError} />}
          <Space size={8}>
            <Button
              type="primary"
              disabled={decided || submitting !== null}
              loading={submitting === 'SETUJU'}
              onClick={() => void respond('SETUJU')}
            >
              {t('report:public.consentApprove')}
            </Button>
            <Button
              danger
              disabled={decided || submitting !== null}
              loading={submitting === 'DITOLAK'}
              onClick={() => void respond('DITOLAK')}
            >
              {t('report:public.consentReject')}
            </Button>
          </Space>
        </Space>
      </Card>
    </div>
  )
}
