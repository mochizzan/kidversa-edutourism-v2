/**
 * Session create wizard (F-08 / FL-07), ADMIN/SA only.
 *
 * Step 1: eligible program picker — ineligible options are disabled WITH the
 * reason (`PROGRAM_INCOMPLETE` also handled server-side on submit).
 * Step 2: meta — name, date, location (required); date accepts any
 * past/present/future value (format-only, backdate allowed); blank start/end = 24 h, both-set must
 * satisfy end > start (client check, server re-validates → VALIDATION).
 * Step 3: groups[] OPTIONAL — name + active facilitator + active children with
 * search; options chosen in another group are disabled (client dedup; server
 * still answers CHILD_DUPLICATE). After create-with-groups succeeds, the
 * detail page runs the copy-data flow for the added children (F-15).
 *
 * Refreshes: both pickers refetch on SSE `program.changed` / `child.changed`
 * (02§2c routing table) — no polling.
 */
import { useRef, useState } from 'react'
import { Alert, App as AntApp, Button, Card, DatePicker, Form, Input, Select, Space, Steps, TimePicker, Typography } from 'antd'
import { ArrowLeftOutlined, ArrowRightOutlined, DeleteOutlined, PlusOutlined } from '@ant-design/icons'
import { useNavigate } from 'react-router'
import { useTranslation } from 'react-i18next'
import PageHeader from '../../components/PageHeader'
import { errorCode, api, ApiError } from '../../lib/api'
import dayjs from '../../lib/dayjs'
import { useListLoader } from '../../lib/list'
import type { ChildRow } from '../../lib/session-contracts'
import { MAX_NOTE, MAX_SHORT_TEXT } from '../../lib/validation'
import { ChildPicker, OwnerPicker } from './controls'
import { validateSessionMeta } from './meta'

interface ProgramRow {
  id: number
  name: string
  eligible: boolean
}

interface GroupDraft {
  key: number
  name: string
  owner: number | null
  childIds: number[]
}

export default function SessionCreatePage() {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const navigate = useNavigate()

  const [step, setStep] = useState(0)
  const [programId, setProgramId] = useState<number | null>(null)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [date, setDate] = useState('')
  const [startTime, setStartTime] = useState('')
  const [endTime, setEndTime] = useState('')
  const [location, setLocation] = useState('')
  const [groups, setGroups] = useState<GroupDraft[]>([])
  const [metaErrors, setMetaErrors] = useState<Record<string, string>>({})
  const [groupErrors, setGroupErrors] = useState<Record<number, string>>({})
  const [submitError, setSubmitError] = useState<unknown>(null)
  const [submitting, setSubmitting] = useState(false)
  const groupKeyRef = useRef(1)

  const { rows: programs, loading: programsLoading } = useListLoader<ProgramRow>(
    '/api/programs',
    {
      page: 1,
      per_page: 1000,
    },
    ['program.changed'],
  )
  const { rows: children } = useListLoader<ChildRow>(
    '/api/children',
    {
      is_active: true,
      page: 1,
      per_page: 1000,
    },
    ['child.changed'],
  )

  function validateStep(index: number): boolean {
    if (index === 0) return programId !== null
    const next: Record<string, string> = {}
    for (const [field, key] of Object.entries(
      validateSessionMeta({ name, date, location, startTime, endTime }),
    )) {
      if (key) next[field] = t(key)
    }
    setMetaErrors(next)
    return Object.keys(next).length === 0
  }

  const advance = () => {
    if (!validateStep(step)) return
    setStep((current) => current + 1)
  }

  const updateGroup = (key: number, patch: Partial<GroupDraft>) =>
    setGroups((list) => list.map((group) => (group.key === key ? { ...group, ...patch } : group)))

  async function submit() {
    const errors: Record<number, string> = {}
    for (const group of groups) {
      if (!group.name.trim()) errors[group.key] = t('form.errors.groupName')
      else if (group.owner === null) errors[group.key] = t('form.errors.owner')
    }
    setGroupErrors(errors)
    if (Object.keys(errors).length > 0) return

    setSubmitting(true)
    setSubmitError(null)
    try {
      const created = await api.post<{ session_id: number }>('/api/sessions', {
        program_id: programId,
        name: name.trim(),
        description: description.trim() || undefined,
        date,
        start_time: startTime ? `${startTime}:00` : undefined,
        end_time: endTime ? `${endTime}:00` : undefined,
        location: location.trim(),
        groups:
          groups.length > 0
            ? groups.map((group) => ({
                name: group.name.trim(),
                facilitator_id: group.owner,
                child_ids: group.childIds,
              }))
            : undefined,
      })
      message.success(t('form.createDone'))
      const copyChildren = groups
        .flatMap((group) => group.childIds)
        .filter((childId, index, all) => all.indexOf(childId) === index)
        .map((childId) => ({
          cid: childId,
          name: children.find((child) => child.id === childId)?.name ?? '',
        }))
      navigate(
        `/sessions/${created.session_id}`,
        copyChildren.length > 0 ? { state: { copyChildren } } : undefined,
      )
    } catch (error: unknown) {
      setSubmitError(error)
      if (error instanceof ApiError && error.code === 'PROGRAM_INCOMPLETE') setStep(0)
    } finally {
      setSubmitting(false)
    }
  }

  const submitCode = errorCode(submitError)
  const submitMessage =
    submitError instanceof ApiError && submitError.message && submitError.message !== submitCode
      ? submitError.message
      : tc(`errors.${submitCode}`)

  return (
    <>
      <PageHeader title={t('wizard.title')} />
      <div className="kv-wizard">
        <Steps
          size="small"
          current={step}
          className="kv-wizard__steps"
          onChange={(next) => {
            if (next < step) setStep(next)
            else if (next === step + 1) advance()
          }}
          items={[
            { title: t('wizard.stepProgram') },
            { title: t('wizard.stepDetail') },
            { title: t('wizard.stepGroups') },
          ]}
        />

        {step === 0 ? (
          <Space direction="vertical" style={{ width: '100%' }}>
          <Typography.Text type="secondary">{t('wizard.pickProgram')}</Typography.Text>
          <Select
            showSearch
            style={{ width: '100%' }}
            value={programId ?? undefined}
            onChange={setProgramId}
            loading={programsLoading}
            placeholder={t('form.programPlaceholder')}
            optionFilterProp="label"
            options={programs.map((program) => ({
              value: program.id,
              label: program.eligible
                ? program.name
                : `${program.name} — ${t('form.programIneligible')}`,
              disabled: !program.eligible,
            }))}
          />
          {!programsLoading && programs.length === 0 ? (
            <Alert type="warning" showIcon message={t('wizard.noPrograms')} />
          ) : null}
        </Space>
      ) : null}

      {step === 1 ? (
        <Form layout="vertical" style={{ width: '100%' }}>
          <Form.Item
            label={t('list.name')}
            validateStatus={metaErrors.name ? 'error' : undefined}
            help={metaErrors.name}
          >
            <Input
              value={name}
              maxLength={MAX_SHORT_TEXT}
              showCount
              placeholder={t('form.namePlaceholder')}
              onChange={(event) => setName(event.target.value)}
            />
          </Form.Item>
          <Form.Item label={t('form.description')} extra={t('form.descriptionHint')}>
            <Input.TextArea
              rows={3}
              maxLength={MAX_NOTE}
              showCount
              value={description}
              placeholder={t('form.descriptionPlaceholder')}
              onChange={(event) => setDescription(event.target.value)}
            />
          </Form.Item>
          <Form.Item
            label={t('list.date')}
            validateStatus={metaErrors.date ? 'error' : undefined}
            help={metaErrors.date}
          >
            <DatePicker
              value={date ? dayjs(date, 'YYYY-MM-DD') : null}
              onChange={(value) => setDate(value ? value.format('YYYY-MM-DD') : '')}
              format="YYYY-MM-DD"
              style={{ width: '100%' }}
            />
          </Form.Item>
          <Space size="middle" wrap>
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
              validateStatus={metaErrors.endTime ? 'error' : undefined}
              help={metaErrors.endTime}
            >
              <TimePicker
                value={endTime ? dayjs(endTime, 'HH:mm') : null}
                onChange={(value) => setEndTime(value ? value.format('HH:mm') : '')}
                format="HH:mm"
                style={{ width: 120 }}
              />
            </Form.Item>
          </Space>
          <Typography.Text type="secondary" style={{ display: 'block', margin: '0 0 16px' }}>
            {t('form.endTimeHint')}
          </Typography.Text>
          <Form.Item
            label={t('list.location')}
            validateStatus={metaErrors.location ? 'error' : undefined}
            help={metaErrors.location}
          >
            <Input
              value={location}
              placeholder={t('form.locationPlaceholder')}
              onChange={(event) => setLocation(event.target.value)}
            />
          </Form.Item>
        </Form>
      ) : null}

      {step === 2 ? (
        <div style={{ width: '100%' }}>
          <Typography.Text type="secondary" style={{ display: 'block' }}>{t('form.childrenHint')}</Typography.Text>
          {groups.map((group) => {
            const excludeIds = groups
              .filter((other) => other.key !== group.key)
              .flatMap((other) => other.childIds)
            return (
              <Card key={group.key} style={{ marginBottom: 12 }}>
                <Space direction="vertical" style={{ width: '100%' }}>
                  <div className="kv-field">
                    <span className="kv-field-label">{t('form.groupName')}</span>
                    <Input
                      placeholder={t('form.groupNamePlaceholder')}
                      value={group.name}
                      onChange={(event) => updateGroup(group.key, { name: event.target.value })}
                    />
                  </div>
                  <div className="kv-field">
                    <span className="kv-field-label">{t('form.owner')}</span>
                    <OwnerPicker
                      value={group.owner}
                      onChange={(owner) => updateGroup(group.key, { owner })}
                    />
                  </div>
                  <div className="kv-field">
                    <span className="kv-field-label">{t('form.children')}</span>
                    <ChildPicker
                      rows={children}
                      value={group.childIds}
                      excludeIds={excludeIds}
                      onChange={(childIds) => updateGroup(group.key, { childIds })}
                    />
                  </div>
                  {groupErrors[group.key] ? (
                    <Alert type="error" showIcon message={groupErrors[group.key]} />
                  ) : null}
                  <Button
                    danger
                    size="small"
                    icon={<DeleteOutlined />}
                    onClick={() => setGroups((list) => list.filter((other) => other.key !== group.key))}
                  >
                    {tc('actions.delete')}
                  </Button>
                </Space>
              </Card>
            )
          })}
          <Button
            icon={<PlusOutlined />}
            onClick={() => {
              const key = groupKeyRef.current++
              setGroups((list) => [
                ...list,
                { key, name: '', owner: null, childIds: [] },
              ])
              setGroupErrors({})
            }}
          >
            {t('form.addGroup')}
          </Button>
        </div>
      ) : null}

        {submitError ? <Alert style={{ marginBottom: 20 }} type="error" showIcon message={submitMessage} /> : null}

        <div className="kv-wizard__actions">
          <div className="kv-wizard__actions-start">
            {step > 0 ? (
              <Button icon={<ArrowLeftOutlined />} onClick={() => setStep((current) => current - 1)} disabled={submitting}>
                {tc('actions.back')}
              </Button>
            ) : null}
          </div>
          <div className="kv-wizard__actions-forward">
            {step < 2 ? (
              <Button type="primary" icon={<ArrowRightOutlined />} onClick={advance}>
                {tc('actions.next')}
              </Button>
            ) : (
              <Button type="primary" loading={submitting} onClick={submit}>
                {t('form.create')}
              </Button>
            )}
            <Button onClick={() => navigate('/sessions')} disabled={submitting}>
              {tc('actions.cancel')}
            </Button>
          </div>
        </div>
      </div>
    </>
  )
}
