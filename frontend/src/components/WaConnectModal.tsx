/**
 * OpenWA connect modal (design 2026-11-09 §7.2) — SA-only.
 *
 * Two choices: Open OpenWA (the external dashboard at host:8003) or Connect
 * Directly (start the configured session and pair in-app). Connect Directly
 * renders OpenWA's QR and polls `GET /api/wa/session` SEQUENTIALLY — wait for
 * each response, then 2 s — because one poll can take several seconds. The
 * loop exists only while the modal is open and the session is not `ready`;
 * Back/close/unmount abort the in-flight request and clear the timer. A poll
 * that omits the QR keeps the last code on screen so it does not flicker
 * mid-scan; the QR clears when the session becomes ready.
 */
import { useCallback, useEffect, useState } from 'react'
import { Alert, Button, Modal, Spin } from 'antd'
import { useTranslation } from 'react-i18next'
import { api, errorCode } from '../lib/api'

export interface WaSessionState {
  connected: boolean
  status: string
  session?: string
  phone?: string
  qr_code?: string
  last_error?: string
  checked_at?: string
}

export interface WaConnectModalProps {
  open: boolean
  onClose: () => void
}

const POLL_MS = 2000

const STATUS_KEYS: Record<string, string> = {
  ready: 'wa.statusReady',
  qr_ready: 'wa.statusQrReady',
  initializing: 'wa.statusInitializing',
  authenticating: 'wa.statusAuthenticating',
  disconnected: 'wa.statusDisconnected',
  failed: 'wa.statusFailed',
  action_required: 'wa.statusFailed',
  unreachable: 'wa.statusUnreachable',
  missing: 'wa.statusMissing',
}

const RETRYABLE = new Set([
  'unreachable',
  'missing',
  'failed',
  'action_required',
  'disconnected',
])

export default function WaConnectModal({ open, onClose }: WaConnectModalProps) {
  const { t } = useTranslation('common')
  const [view, setView] = useState<'choice' | 'connect'>('choice')
  const [state, setState] = useState<WaSessionState | null>(null)
  const [error, setError] = useState<string | null>(null)

  // Every fresh open starts at the choice view with no stale QR/status.
  useEffect(() => {
    if (!open) return
    setView('choice')
    setState(null)
    setError(null)
  }, [open])

  const connect = useCallback(async (retry = false) => {
    setView('connect')
    setError(null)
    try {
      const next = await api.post<WaSessionState>(
        '/api/wa/session/start',
        retry ? { retry: true } : {},
      )
      setState(next)
    } catch (cause) {
      setError(errorCode(cause))
    }
  }, [])

  const hasState = state !== null
  const connected = state?.connected ?? false

  useEffect(() => {
    if (!open || view !== 'connect' || !hasState || connected) return
    let cancelled = false
    let timer: ReturnType<typeof setTimeout> | undefined
    const controller = new AbortController()

    const poll = async () => {
      try {
        const next = await api.get<WaSessionState>('/api/wa/session', undefined, {
          signal: controller.signal,
        })
        if (cancelled) return
        // Keep the last QR when a transient poll returns none.
        setState((prev) => ({ ...next, qr_code: next.qr_code ?? prev?.qr_code }))
        if (!next.connected) timer = setTimeout(poll, POLL_MS)
      } catch {
        if (cancelled) return
        timer = setTimeout(poll, POLL_MS)
      }
    }

    timer = setTimeout(poll, POLL_MS)
    return () => {
      cancelled = true
      controller.abort()
      if (timer) clearTimeout(timer)
    }
  }, [open, view, hasState, connected])

  const openOpenWA = () => {
    window.open(
      `${window.location.protocol}//${window.location.hostname}:8003`,
      '_blank',
      'noopener,noreferrer',
    )
    onClose()
  }

  const statusKey = state ? (STATUS_KEYS[state.status] ?? 'wa.statusDisconnected') : 'wa.statusInitializing'
  const busy = state?.status === 'initializing' || state?.status === 'authenticating'
  const qr = !connected ? state?.qr_code : undefined
  const retryable = error !== null || (state !== null && RETRYABLE.has(state.status))

  return (
    <Modal
      className="kv-wa-modal"
      open={open}
      title={t('wa.connectTitle')}
      width={460}
      footer={null}
      onCancel={onClose}
    >
      {view === 'choice' ? (
        <div className="kv-wa-choice">
          <p>{t('wa.connectIntro')}</p>
          <Button block onClick={openOpenWA}>
            {t('wa.openOpenwa')}
          </Button>
          <p className="kv-meta">{t('wa.openOpenwaDesc')}</p>
          <Button block type="primary" onClick={() => void connect()}>
            {t('wa.connectDirectly')}
          </Button>
          <p className="kv-meta">{t('wa.connectDirectlyDesc')}</p>
        </div>
      ) : (
        <div className="kv-wa-status">
          <div className="kv-wa-status__line" role="status" aria-live="polite">
            {busy ? <Spin size="small" /> : null}
            <span>{t(statusKey)}</span>
          </div>

          {error ? <Alert type="error" showIcon message={t(`errors.${error}`)} /> : null}

          {qr ? (
            <div className="kv-wa-status__qr">
              <h4>{t('wa.scanTitle')}</h4>
              <img className="kv-wa-qr" src={qr} alt={t('wa.qrAlt')} />
              <p className="kv-meta">{t('wa.scanHint')}</p>
            </div>
          ) : null}

          {connected ? (
            <p>
              <span className="kv-meta">{t('wa.phone')}</span> <span>{state?.phone}</span>
            </p>
          ) : null}

          {state?.last_error ? <p className="kv-meta">{state.last_error}</p> : null}

          <div className="kv-wa-status__actions">
            <Button onClick={() => setView('choice')}>{t('wa.back')}</Button>
            {retryable ? (
              <Button type="primary" onClick={() => void connect(true)}>
                {t('wa.retry')}
              </Button>
            ) : null}
            <Button onClick={onClose}>{t('wa.close')}</Button>
          </div>
        </div>
      )}
    </Modal>
  )
}
