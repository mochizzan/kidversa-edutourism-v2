/**
 * ConfirmDialog typed-text gate (02§3 destructive wipes): the confirm
 * button stays disabled until the EXACT text is typed; a fresh open never
 * carries the previous typed confirmation.
 */
import { describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import ConfirmDialog from './ConfirmDialog'

describe('ConfirmDialog typed-text enable gate', () => {
  it('without typedText the confirm button is immediately usable', async () => {
    const onConfirm = vi.fn()
    const onCancel = vi.fn()
    const user = userEvent.setup()
    render(
      <ConfirmDialog
        open
        title="Hapus sesi ini?"
        onConfirm={onConfirm}
        onCancel={onCancel}
      />,
    )

    const confirm = screen.getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeEnabled()
    await user.click(confirm)
    expect(onConfirm).toHaveBeenCalledTimes(1)
    expect(onCancel).not.toHaveBeenCalled()
  })

  it('with typedText the confirm button unlocks only on the exact string', async () => {
    const onConfirm = vi.fn()
    const user = userEvent.setup()
    render(
      <ConfirmDialog
        open
        title="Hapus anak?"
        typedText="Budi Santoso"
        danger
        onConfirm={onConfirm}
        onCancel={() => {}}
      />,
    )

    const dialog = screen.getByRole('dialog')
    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    expect(confirm).toBeDisabled()

    const input = within(dialog).getByRole('textbox')
    await user.type(input, 'Budi Santoso')
    expect(confirm).toBeEnabled()

    await user.click(confirm)
    expect(onConfirm).toHaveBeenCalledTimes(1)
  })

  it('a wrong or partial text keeps confirmation locked', async () => {
    const onConfirm = vi.fn()
    const user = userEvent.setup()
    render(
      <ConfirmDialog
        open
        title="Hapus anak?"
        typedText="HAPUS"
        onConfirm={onConfirm}
        onCancel={() => {}}
      />,
    )

    const dialog = screen.getByRole('dialog')
    const confirm = within(dialog).getByRole('button', { name: 'Konfirmasi' })
    await user.type(within(dialog).getByRole('textbox'), 'HAPU')
    expect(confirm).toBeDisabled()

    await user.click(confirm)
    expect(onConfirm).not.toHaveBeenCalled()
  })

  it('reopening starts from a clean typed confirmation', async () => {
    const onConfirm = vi.fn()
    const user = userEvent.setup()
    const props = {
      title: 'Hapus sekolah?',
      typedText: 'Bandung',
      onConfirm,
      onCancel: () => {},
    }
    const view = render(<ConfirmDialog {...props} open />)

    const input = screen.getByRole('textbox')
    await user.type(input, 'Bandung')
    expect(screen.getByRole('button', { name: 'Konfirmasi' })).toBeEnabled()

    view.rerender(<ConfirmDialog {...props} open={false} />)
    view.rerender(<ConfirmDialog {...props} open />)

    const freshInput = screen.getByRole('textbox')
    expect(freshInput).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Konfirmasi' })).toBeDisabled()
  })
})
