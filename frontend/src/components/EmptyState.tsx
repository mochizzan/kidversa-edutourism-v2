/**
 * Empty state (system-states rule): never a blank table. Every empty surface
 * shows a small illustration, one line of explanation and — where the user can
 * act — the action that fills it. The illustration is decorative and inherits
 * the border palette, so it stays quiet next to real data.
 */
import type { ReactNode } from 'react'
import './empty-state.css'

export interface EmptyStateProps {
  title: ReactNode
  description?: ReactNode
  action?: ReactNode
  /** `table` keeps the block compact inside a data grid; `card` gives it air. */
  variant?: 'table' | 'card'
}

export default function EmptyState({
  title,
  description,
  action,
  variant = 'table',
}: EmptyStateProps) {
  return (
    <div className={`kv-empty kv-empty--${variant}`}>
      <svg
        className="kv-empty__art"
        viewBox="0 0 96 64"
        width="96"
        height="64"
        fill="none"
        aria-hidden="true"
        focusable="false"
      >
        <rect x="14" y="12" width="68" height="40" rx="7" stroke="currentColor" strokeWidth="1.5" />
        <path d="M14 26h68" stroke="currentColor" strokeWidth="1.5" />
        <path d="M26 20h16" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
        <path
          d="M26 36h30M26 44h20"
          stroke="currentColor"
          strokeWidth="1.5"
          strokeLinecap="round"
          opacity="0.55"
        />
        <circle cx="70" cy="40" r="9" fill="var(--kv-canvas)" stroke="currentColor" strokeWidth="1.5" />
        <path d="M67 40h6M70 37v6" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" />
      </svg>
      <p className="kv-empty__title">{title}</p>
      {description ? <p className="kv-empty__desc">{description}</p> : null}
      {action ? <div className="kv-empty__action">{action}</div> : null}
    </div>
  )
}
