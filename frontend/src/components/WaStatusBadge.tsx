/**
 * WhatsApp connection badge (spec F-13 / FL-14) for the SA/ADMIN dashboard —
 * hidden for FAS. EVENT-DRIVEN: renders `wa.changed {connected}` from the SSE
 * stream with NO polling, seeded once on mount by `GET /api/wa/status` (the
 * server only publishes transitions, so without the seed a stable link state
 * would never reach the badge); the same endpoint is reused on demand when the
 * detail popover opens. Only an SA may click through to the connect modal
 * (design 2026-11-09); ADMIN keeps the badge and its popover but the click is
 * inert. Status is never colour-only: the text always names the state.
 */
import { useEffect, useState } from 'react'
import { Badge, Button, Popover } from 'antd'
import { useTranslation } from 'react-i18next'
import { api } from '../lib/api'
import { useAuth } from '../lib/auth'
import { registerSignal } from '../lib/stream'
import WaConnectModal from './WaConnectModal'

interface WaStatus {
  connected: boolean
  session?: string
  checked_at?: string
}

export default function WaStatusBadge() {
  const { user, school } = useAuth()
  const { t } = useTranslation('common')
  const [connected, setConnected] = useState<boolean | null>(null)
  // null = not fetched yet; 'error' = the on-demand status call itself failed.
  const [detail, setDetail] = useState<WaStatus | 'error' | null>(null)
  const [connectOpen, setConnectOpen] = useState(false)
  const isSA = user?.role === 'SA'

  useEffect(
    () =>
      registerSignal<{ connected: boolean }>('wa.changed', (data) => {
        setConnected(Boolean(data.connected))
      }),
    [],
  )

  // The server publishes `wa.changed` only on a link-state TRANSITION, so a
  // stable link never seeds this badge and it would sit on "Checking…" until
  // the user happened to hover. Seed it once per session/school — no polling;
  // SSE stays authoritative for every later transition.
  useEffect(() => {
    if (!user || user.role === 'FASILITATOR') return
    let cancelled = false
    api.get<WaStatus>('/api/wa/status').then(
      (data) => {
        // A transition that landed while the seed was in flight wins.
        if (!cancelled) setConnected((prev) => prev ?? data.connected)
      },
      () => {
        // Best-effort decoration: a hover retries through the detail fetch.
      },
    )
    return () => {
      cancelled = true
    }
  }, [user, school])

  if (user?.role === 'FASILITATOR') return null

  const openDetail = (open: boolean) => {
    // A successful response is cached for the popover's lifetime; a failed one
    // is not — reopening must be able to recover from a transient error.
    if (!open || (detail !== null && detail !== 'error')) return
    if (detail === 'error') setDetail(null)
    api
      .get<WaStatus>('/api/wa/status')
      .then(setDetail)
      .catch(() => setDetail('error'))
  }

  // The SSE `wa.changed` value is authoritative once it arrives; until then the
  // on-demand detail call fills the unknown state so the badge never keeps
  // saying "Checking…" while the popover already reports a real status.
  const resolved =
    connected ?? (detail !== null && detail !== 'error' ? detail.connected : null)

  const popoverBody = () => {
    if (detail === null) return t('wa.loadingDetail')
    if (detail === 'error') return t('errors.INTERNAL')
    return (
      <div className="kv-wa-detail">
        <div>{resolved ? t('wa.connected') : t('wa.disconnected')}</div>
        {detail.session ? <div className="kv-meta">{detail.session}</div> : null}
        {detail.checked_at ? (
          <div className="kv-meta">{new Date(detail.checked_at).toLocaleString()}</div>
        ) : null}
      </div>
    )
  }

  const stateClass = resolved === null ? 'is-unknown' : resolved ? 'is-on' : 'is-off'
  const label =
    resolved === null ? t('wa.checking') : resolved ? t('wa.connected') : t('wa.disconnected')

  const dotStatus = resolved === null ? 'default' : resolved ? ('success' as const) : ('error' as const)

  return (
    <>
      <Popover
        content={popoverBody()}
        trigger={['hover', 'focus']}
        onOpenChange={openDetail}
      >
        <Button
          className={`kv-wa ${stateClass}${isSA ? '' : ' is-locked'}`}
          aria-label={`${t('wa.title')}: ${label}`}
          aria-disabled={!isSA}
          title={isSA ? undefined : t('wa.adminOnly')}
          onClick={() => {
            if (isSA) setConnectOpen(true)
          }}
        >
          <Badge className="kv-wa__badge" status={dotStatus} />
          <span>{label}</span>
        </Button>
      </Popover>
      <WaConnectModal open={connectOpen} onClose={() => setConnectOpen(false)} />
    </>
  )
}
