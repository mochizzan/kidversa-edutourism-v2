/**
 * One status vocabulary for every list.
 *
 * `StatusChip` owns the session lifecycle (Draft / Opened / Finished) — a
 * three-state machine with its own colours. Everything else a table needs to
 * say about a row (active / inactive, approved / rejected, eligible / not
 * ready) is a *tone*, not a lifecycle: `positive`, `neutral`, `attention` or
 * `info`. Before this component those rows either reused the lifecycle classes
 * (`kv-status--finished` meant "active") or fell back to antd's default blue
 * Tag, so the same concept looked different on every page.
 *
 * Rules kept from DESIGN: the label always carries the meaning (never colour
 * alone), the pill is full-radius, and the accent stays quiet.
 */
import type { ReactNode } from 'react'
import { Tag } from 'antd'

export type StatusTone = 'neutral' | 'positive' | 'attention' | 'info'

export interface StatusPillProps {
  /** Semantic weight; `neutral` when a row has nothing to flag. */
  tone?: StatusTone
  /** Native tooltip for a pill whose label needs the longer explanation. */
  title?: string
  children: ReactNode
}

export default function StatusPill({ tone = 'neutral', title, children }: StatusPillProps) {
  return (
    <Tag className={`kv-status kv-status--${tone}`} title={title}>
      {children}
    </Tag>
  )
}
