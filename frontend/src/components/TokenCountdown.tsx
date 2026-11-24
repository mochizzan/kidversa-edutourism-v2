/**
 * Reset-dashboard countdown (spec F-01 / 02§2c): a 1s LOCAL tick from
 * `expires_at` — no polling; the row list refetches on `reset.changed`.
 * Renders mm:ss with tabular numerals.
 */
import { useEffect, useState } from 'react'

export interface TokenCountdownProps {
  /** ISO timestamp of the reset-code expiry. */
  expiresAt: string
}

export default function TokenCountdown({ expiresAt }: TokenCountdownProps) {
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(timer)
  }, [])

  const remaining = Math.max(0, Math.floor((Date.parse(expiresAt) - now) / 1000))
  const minutes = String(Math.floor(remaining / 60)).padStart(2, '0')
  const seconds = String(remaining % 60).padStart(2, '0')
  return <span className="kv-num">{minutes}:{seconds}</span>
}
