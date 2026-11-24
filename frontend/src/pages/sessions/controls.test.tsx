/**
 * Shared F4 controls: ChildPicker's search + CHILD_DUPLICATE exclusion rules
 * (excluded only while NOT already chosen — a selected id stays clearable),
 * OwnerPicker's active-facilitator fetch narrowed to the ACTIVE school with
 * a clear → null, and ConsentTag's label-always-rendered rule with the
 * unknown-status fallback style.
 */
import { describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import { ChildPicker, ConsentTag, FormError, OwnerPicker } from './controls'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeSchool, makeUser, paged } from '../../test/handlers'
import { chooseOption, openDropdown, sessionChildRow } from '../../test/helpers.cov-session'
import type { ChildRow } from '../../lib/session-contracts'

function pickerInput(): HTMLElement {
  return document.querySelector('.ant-select .ant-select-selection-search-input') as HTMLElement
}

describe('child picker', () => {
  const rows: ChildRow[] = [
    sessionChildRow(2, 'Aisyah Putri'),
    sessionChildRow(3, 'Budi Santoso'),
    sessionChildRow(4, 'Citra Ayu'),
  ]

  it('searches by name/school and reports the picked ids upward', async () => {
    const onChange = vi.fn()
    renderApp(
      <ChildPicker rows={rows} value={[]} onChange={onChange} />,
    )
    const user = userEvent.setup()

    await openDropdown(pickerInput())
    await chooseOption('Budi Santoso · SD Cendekia')

    // antd multiple-Select passes (value, option, options).
    expect(onChange).toHaveBeenCalledTimes(1)
    expect(onChange.mock.calls[0][0]).toEqual([3])

    // filterOption: a school-only query still narrows the list.
    await user.type(pickerInput(), 'SD Cendekia')
    const options = screen.getAllByRole('option')
    expect(options).toHaveLength(2) // Aisyah + Budi share the school
    expect(screen.queryByRole('option', { name: /Citra Ayu/ })).toBeNull()
  })

  it('disables excluded children only while they are not already selected', async () => {
    renderApp(
      // 3 excluded and already chosen → stays enabled (must remain clearable);
      // 4 excluded and unchosen → blocked; 2 free.
      <ChildPicker rows={rows} value={[3]} onChange={vi.fn()} excludeIds={[3, 4]} />,
    )

    await openDropdown(pickerInput())
    // Disabled options leave the a11y tree; read the flag off the DOM nodes.
    const disabledOf = (name: string) =>
      Array.from(document.querySelectorAll('.ant-select-item-option')).find((option) =>
        option.textContent?.includes(`${name} ·`),
      )?.classList.contains('ant-select-item-option-disabled')
    expect(disabledOf('Budi Santoso')).toBe(false) // excluded but selected → clearable
    expect(disabledOf('Citra Ayu')).toBe(true) // excluded + unchosen → blocked
    expect(disabledOf('Aisyah Putri')).toBe(false) // never excluded
  })

  it('renders disabled when the parent passes disabled', async () => {
    renderApp(<ChildPicker rows={rows} value={[]} onChange={vi.fn()} disabled />)
    const select = document.querySelector('.ant-select') as HTMLElement
    expect(select.classList.contains('ant-select-disabled')).toBe(true)
    await userEvent.setup().click(pickerInput())
    expect(screen.queryByRole('option')).toBeNull()
  })
})

describe('owner picker', () => {
  function usersHandler() {
    return http.get('/api/users', ({ request }) => {
      const url = new URL(request.url)
      expect(url.searchParams.get('role')).toBe('FASILITATOR')
      expect(url.searchParams.get('is_active')).toBe('true')
      return paged(
        [
          { id: 7, name: 'Dewi Lestari', email: 'dewi@kidversa.id', role: 'FASILITATOR', decision: 'APPROVED', is_active: true, school_id: 1 },
          { id: 8, name: 'Rina Luar', email: 'rina@other.id', role: 'FASILITATOR', decision: 'APPROVED', is_active: true, school_id: 9 },
        ],
        { page: 1, per_page: 1000, total: 2 },
      )
    })
  }

  it('lists only active facilitators of the ACTIVE school and selects one', async () => {
    server.use(usersHandler())
    const onChange = vi.fn()
    renderApp(<OwnerPicker value={null} onChange={onChange} />, {
      session: makeLoginData({ user: makeUser(), school: makeSchool({ id: 1 }) }),
    })

    await openDropdown(pickerInput())
    await waitFor(() => expect(screen.getByRole('option', { name: /Dewi Lestari/ })).toBeInTheDocument())
    expect(screen.queryByRole('option', { name: /Rina Luar/ })).toBeNull()

    await chooseOption('Dewi Lestari · dewi@kidversa.id')
    expect(onChange).toHaveBeenCalledWith(7)
  })

  it('clearing a selected owner reports null', async () => {
    server.use(usersHandler())
    const onChange = vi.fn()
    renderApp(<OwnerPicker value={7} onChange={onChange} />, {
      session: makeLoginData({ user: makeUser(), school: makeSchool({ id: 1 }) }),
    })

    await screen.findByText('Dewi Lestari · dewi@kidversa.id')
    const clear = document.querySelector('.ant-select-clear') as HTMLElement
    await userEvent.setup().click(clear)
    expect(onChange).toHaveBeenCalledWith(null)
  })

  it('typing filters the owner list by label', async () => {
    server.use(usersHandler())
    renderApp(<OwnerPicker value={null} onChange={vi.fn()} />, {
      session: makeLoginData({ user: makeUser(), school: makeSchool({ id: 1 }) }),
    })

    const user = userEvent.setup()
    await openDropdown(pickerInput())
    await waitFor(() => expect(screen.getByRole('option', { name: /Dewi Lestari/ })).toBeInTheDocument())

    await user.type(pickerInput(), 'dewi@')
    // filterOption matches the label substring — the list narrows to one row.
    expect(screen.getAllByRole('option')).toHaveLength(1)
    expect(screen.getByRole('option', { name: /Dewi Lestari/ })).toBeInTheDocument()
  })
})

describe('consent tag and form error', () => {
  it('renders a translated label for every status, unknown included', async () => {
    const { container } = renderApp(
      <>
        <ConsentTag status="SETUJU" />
        <ConsentTag status="DITOLAK" />
        <ConsentTag status="PENDING" />
        <ConsentTag status="WEIRD" />
      </>,
    )

    expect(screen.getByText('Disetujui')).toBeInTheDocument()
    expect(screen.getByText('Ditolak')).toBeInTheDocument()
    // PENDING + the unknown status both fall back to the pending label.
    expect(screen.getAllByText('Menunggu')).toHaveLength(2)
    // Unknown status falls back to the neutral tone — never unstyled.
    expect(container.querySelectorAll('.kv-status')).toHaveLength(4)
    const fallback = screen.getAllByText('Menunggu').at(-1) as HTMLElement
    expect(fallback.closest('.kv-status')).toHaveClass('kv-status--neutral')
  })

  it('FormError renders its message text', () => {
    renderApp(<FormError>Wajib diisi.</FormError>)
    expect(screen.getByText('Wajib diisi.')).toBeInTheDocument()
  })
})
