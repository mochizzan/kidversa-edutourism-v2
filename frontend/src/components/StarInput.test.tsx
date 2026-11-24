/**
 * StarInput (05-DATA 1↔BB 2↔MB 3↔BSH 4↔BSB): antd Rate with count=4 —
 * each star keeps its `N — LABEL` accessible name and role=radio (via
 * characterRender), click reports the star value, selection and disabled
 * states are exposed via aria-checked/aria-disabled, and there is NO
 * clear-to-zero (allowClear disabled: the scale is 1–4 only).
 */
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import StarInput from './StarInput'

describe('StarInput interaction', () => {
  it('renders a radiogroup labelled with the BB/MB/BSH/BSB values', () => {
    render(<StarInput value={2} />)
    const group = screen.getByRole('radiogroup', { name: 'Penilaian bintang 1–4' })
    expect(group).toBeInTheDocument()
    for (const label of ['1 — BB', '2 — MB', '3 — BSH', '4 — BSB']) {
      expect(screen.getByRole('radio', { name: label })).toBeInTheDocument()
    }
    expect(screen.getByRole('radio', { name: '2 — MB' })).toBeChecked()
    expect(screen.getByRole('radio', { name: '4 — BSB' })).not.toBeChecked()
  })

  it('clicking a star reports its value', async () => {
    const onChange = vi.fn()
    const user = userEvent.setup()
    render(<StarInput value={null} onChange={onChange} />)

    await user.click(screen.getByRole('radio', { name: '3 — BSH' }))
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange).toHaveBeenCalledWith(3)
  })

  it('re-reports the current value on re-click instead of clearing to zero', async () => {
    const onChange = vi.fn()
    const user = userEvent.setup()
    render(<StarInput value={2} onChange={onChange} />)

    await user.click(screen.getByRole('radio', { name: '2 — MB' }))
    expect(onChange).toHaveBeenCalledWith(2) // never 0 — allowClear is disabled
  })

  it('reflects the current value through aria-checked', () => {
    render(<StarInput value={2} onChange={() => {}} />)
    expect(screen.getByRole('radio', { name: '2 — MB' })).toBeChecked()
    expect(screen.getByRole('radio', { name: '3 — BSH' })).not.toBeChecked()
  })

  it('disabled input ignores clicks', async () => {
    const onChange = vi.fn()
    const user = userEvent.setup()
    render(<StarInput value={1} onChange={onChange} disabled />)

    const star4 = screen.getByRole('radio', { name: '4 — BSB' })
    expect(star4).toHaveAttribute('aria-disabled', 'true')
    expect(star4).toHaveAttribute('tabindex', '-1')
    await user.click(star4)
    expect(onChange).not.toHaveBeenCalled()
  })
})
