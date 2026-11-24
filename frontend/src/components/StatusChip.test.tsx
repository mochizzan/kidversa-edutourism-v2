/**
 * Session status chip (spec F-08): Draft/Opened/Finished render translated
 * TEXT next to the colour (DESIGN rule: never colour alone) with the
 * kv-status class hook.
 */
import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import StatusChip from './StatusChip'

describe('StatusChip rendering', () => {
  it.each([
    ['DRAFT', 'Draf', 'kv-status--draft'],
    ['OPENED', 'Dibuka', 'kv-status--opened'],
    ['FINISHED', 'Selesai', 'kv-status--finished'],
  ] as const)('shows %s as "%s" with the %s hook', (status, label, hook) => {
    const { container } = render(<StatusChip status={status} />)
    expect(screen.getByText(label)).toBeInTheDocument()
    expect(container.querySelector(`.${hook}`)).not.toBeNull()
  })
})
