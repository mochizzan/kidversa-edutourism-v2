/**
 * Shared list-filter tray.
 *
 * Why this exists: list pages used to render a bare `Space wrap` of controls
 * whose placeholder doubled as the label. Once a value was chosen the
 * placeholder disappeared, so the control's meaning disappeared with it, and
 * "Search" never said what it searched. Here every control keeps a visible
 * label above it, and the tray can explain the search scope, show how many
 * filters are on, and offer one clear action.
 */
import type { ReactNode } from 'react'
import { Button } from 'antd'
import { useTranslation } from 'react-i18next'

export interface FilterBarProps {
  children: ReactNode
  /** How many filters are currently applied. 0 hides the clear action. */
  activeCount?: number
  /** Resets every filter in this bar (search text + selects). */
  onClear?: () => void
  /** One terse line under the row: search scope or a cascade rule. */
  hint?: ReactNode
}

export default function FilterBar({ children, activeCount = 0, onClear, hint }: FilterBarProps) {
  const { t } = useTranslation('common')
  const showClear = activeCount > 0 && onClear !== undefined
  return (
    <section className="kv-filterbar kv-list-filters" aria-label={t('filters.region')}>
      <div className="kv-filterbar__row">
        {children}
        {showClear ? (
          <Button type="link" size="small" className="kv-filterbar__clear" onClick={onClear}>
            {t('filters.clear', { count: activeCount })}
          </Button>
        ) : null}
      </div>
      {hint ? <p className="kv-filterbar__hint">{hint}</p> : null}
    </section>
  )
}

export interface FilterFieldProps {
  /** Always-visible label; the control's placeholder is only an example. */
  label: string
  /** Rendered with the control so screen readers announce the same name. */
  htmlFor?: string
  /** Fixed control width in px; omit to let the field size itself. */
  width?: number
  children: ReactNode
}

export function FilterField({ label, htmlFor, width = 220, children }: FilterFieldProps) {
  return (
    <div className="kv-filter-field" style={{ width }}>
      <label className="kv-filter-field__label" htmlFor={htmlFor}>
        {label}
      </label>
      {children}
    </div>
  )
}
