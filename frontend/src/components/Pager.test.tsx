/**
 * Pager (02§3 decision 25): server-side paging controls — total-driven
 * summary from meta.total, 20/50/100/1000 page-size options, page change
 * vs. page-size change (size change resets to page 1).
 */
import { describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import Pager from './Pager'

describe('Pager total-driven summary', () => {
  it('renders the range from meta.total', () => {
    render(<Pager page={1} perPage={20} total={345} onPage={() => {}} onPerPage={() => {}} />)
    expect(screen.getByText('Menampilkan 1–20 dari 345')).toBeInTheDocument()
  })
})

describe('Pager page navigation', () => {
  it('reports the clicked page', async () => {
    const onPage = vi.fn()
    const user = userEvent.setup()
    render(<Pager page={1} perPage={20} total={345} onPage={onPage} onPerPage={() => {}} />)

    await user.click(screen.getByTitle('2'))
    expect(onPage).toHaveBeenCalledWith(2)
  })
})

describe('Pager page-size options', () => {
  it('offers 20/50/100/1000 and a size change resets to page 1', async () => {
    const onPage = vi.fn()
    const onPerPage = vi.fn()
    const user = userEvent.setup()
    render(<Pager page={3} perPage={20} total={345} onPage={onPage} onPerPage={onPerPage} />)

    // antd's size changer is a Select of "<size> / page" options.
    await user.click(screen.getByRole('combobox', { name: 'Page Size' }))
    const listbox = within(await screen.findByRole('listbox'))
    for (const size of [20, 50, 100, 1000]) {
      expect(listbox.getByRole('option', { name: `${size} / page` })).toBeInTheDocument()
    }

    await user.click(listbox.getByRole('option', { name: '50 / page' }))
    expect(onPerPage).toHaveBeenCalledWith(50)
    expect(onPage).toHaveBeenCalledWith(1) // size change rewinds to page 1
  })
})
