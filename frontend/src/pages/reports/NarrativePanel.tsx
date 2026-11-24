/**
 * AI narrative block (F-11 / FL-12) for one child+topic (prefix P).
 * Generate/Regenerate → `POST P/narrative` (auto idem key); manual edit →
 * `PATCH P/narrative {text}` (≤2000). NOTHING_TO_NARRATE and AI_FAILED show
 * INLINE (AI_FAILED keeps the old text — the button is the retry affordance).
 * SSE `narrative.changed` refetches (own-200 also refetches), unsaved edits
 * are not clobbered.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { Alert, App as AntApp, Button, Card, Flex, Input, Space, Typography } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../../lib/api'
import { useFeedback } from '../../lib/feedback'
import { useEventRefetch } from '../../lib/list'
import { MAX_NOTE } from '../../lib/validation'
import type { NarrativeItem } from '../../lib/session-contracts'

export default function NarrativePanel({ prefix }: { prefix: string }) {
  const { t } = useTranslation('session')
  const { t: tc } = useTranslation('common')
  const { message } = AntApp.useApp()
  const feedback = useFeedback()
  const [narrative, setNarrative] = useState<NarrativeItem | null>(null)
  const [loaded, setLoaded] = useState(false)
  const [text, setText] = useState('')
  const [busy, setBusy] = useState(false)
  const [inlineError, setInlineError] = useState<string | null>(null)
  const [saveError, setSaveError] = useState<string | null>(null)
  const dirtyRef = useRef(false)

  const load = useCallback(() => {
    api.get<NarrativeItem | null>(`${prefix}/narrative`).then(
      (data) => {
        setNarrative(data)
        setLoaded(true)
        if (!dirtyRef.current) setText(data?.text ?? '')
      },
      () => setLoaded(true),
    )
  }, [prefix])

  useEffect(() => {
    dirtyRef.current = false
    setInlineError(null)
    setSaveError(null)
    setText('')
    load()
  }, [load])

  useEventRefetch(['narrative.changed'], load)

  // Render-safe derived dirty: ref (handler-only) keeps SSE refetches from
  // clobbering unsaved edits; this comparison drives the UI.
  const dirty = (narrative?.text ?? '') !== text

  const showServerError = (failure: unknown, fallbackSetter: (message: string) => void) => {
    const code = errorCode(failure)
    if (code === 'NOTHING_TO_NARRATE' || code === 'AI_FAILED') setInlineError(tc(`errors.${code}`))
    else fallbackSetter(tc(`errors.${code}`))
  }

  const generate = async () => {
    setBusy(true)
    setInlineError(null)
    setSaveError(null)
    try {
      const data = await api.post<NarrativeItem>(`${prefix}/narrative`)
      setNarrative(data)
      setText(data.text)
      dirtyRef.current = false
      // Generation takes seconds and can finish while the facilitator looks
      // elsewhere: a card (not a 3-second toast) is the right weight here.
      feedback.heavy(t('reports.narrativeReady'), t('reports.narrativeReadyBody'))
    } catch (failure: unknown) {
      showServerError(failure, setSaveError) // AI_FAILED: old narrative kept
    } finally {
      setBusy(false)
    }
  }

  const save = async () => {
    setBusy(true)
    setSaveError(null)
    try {
      const data = await api.patch<NarrativeItem>(`${prefix}/narrative`, { text: text.trim() })
      setNarrative(data)
      // Re-sync the editor from the response: the server's stored text (trimmed
      // / normalised) becomes both the value and the dirty baseline.
      setText(data.text)
      dirtyRef.current = false
      message.success(t('reports.narrativeSaved'))
    } catch (failure: unknown) {
      showServerError(failure, setSaveError)
    } finally {
      setBusy(false)
    }
  }

  return (
    <Card style={{ marginBottom: 16 }}>
      <Flex justify="space-between" align="flex-start" gap="middle" wrap style={{ marginBottom: 8 }}>
        <Typography.Title level={2} className="kv-card-title">
          {t('reports.narrative')}
        </Typography.Title>
        <Space size={4}>
          {loaded && narrative === null && !inlineError && !saveError ? (
            <Typography.Text type="secondary">{t('reports.narrativeEmpty')}</Typography.Text>
          ) : null}
          <Button size="small" loading={busy} onClick={() => void generate()}>
            {narrative ? t('reports.narrativeRegenerate') : t('reports.narrativeGenerate')}
          </Button>
        </Space>
      </Flex>
      <Input.TextArea
        rows={5}
        maxLength={MAX_NOTE}
        showCount={{ formatter: ({ count, maxLength }) => `${count}/${maxLength}` }}
        disabled={busy}
        placeholder={t('reports.narrativePlaceholder')}
        value={text}
        onChange={(event) => {
          dirtyRef.current = true
          setText(event.target.value)
        }}
      />
      <Flex justify="flex-end" style={{ marginTop: 8 }}>
        <Button
          size="small"
          type="primary"
          loading={busy}
          disabled={!dirty || text.trim().length === 0}
          onClick={() => void save()}
        >
          {t('reports.narrativeSave')}
        </Button>
      </Flex>
      {inlineError ? (
        <Alert style={{ marginTop: 8 }} type="warning" showIcon message={inlineError} />
      ) : null}
      {saveError ? <Alert style={{ marginTop: 8 }} type="error" showIcon message={saveError} /> : null}
      {narrative && !dirty ? (
        <Typography.Text type="secondary" style={{ display: 'block', marginTop: 8 }}>
          {narrative.model} · {narrative.generated_at}
        </Typography.Text>
      ) : null}
    </Card>
  )
}
