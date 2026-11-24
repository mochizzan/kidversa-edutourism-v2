/**
 * Star rating input, values 1–4 with the spec labels BB / MB / BSH / BSB
 * (05-DATA: 1↔BB, 2↔MB, 3↔BSH, 4↔BSB — acronyms are identical in both
 * languages). antd Rate (count=4): each star keeps its `N — LABEL`
 * accessible name (never colour alone) and role=radio/aria-checked via
 * characterRender, so screen readers and tests see labelled radios.
 * allowClear is disabled: clicking the current value re-reports it — the
 * scale is 1–4 only, there is no "0" state.
 */
import { cloneElement, type ReactElement, type ReactNode } from 'react'
import { Rate } from 'antd'
import type { RateProps } from 'antd'
import { useTranslation } from 'react-i18next'

export interface StarInputProps {
  /** Current value 1–4, or null/undefined when unassessed. */
  value?: number | null
  onChange?: (value: number) => void
  disabled?: boolean
}

export default function StarInput({ value, onChange, disabled = false }: StarInputProps) {
  const { t } = useTranslation('assessment')
  // Label every star with its value + spec acronym and expose the disabled
  // state (antd Rate renders role="radio" divs without names by default).
  const characterRender: RateProps['characterRender'] = (origin, props) => {
    const star = (props.index ?? 0) + 1
    const li = origin as ReactElement<{ children?: ReactNode }>
    const radio = li.props.children as ReactElement<Record<string, unknown>>
    return cloneElement(li, {
      children: cloneElement(radio, {
        'aria-label': `${star} — ${t(`stars.${star}`)}`,
        ...(disabled ? { 'aria-disabled': 'true' } : {}),
      }),
    })
  }
  return (
    <div className="kv-stars" role="radiogroup" aria-label={t('stars.groupLabel')}>
      <Rate
        count={4}
        value={value ?? 0}
        allowClear={false}
        disabled={disabled}
        characterRender={characterRender}
        onChange={(next) => onChange?.(next)}
      />
    </div>
  )
}
