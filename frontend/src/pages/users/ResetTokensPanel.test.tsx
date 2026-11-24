/**
 * Reset-token dashboard (F-01 / FL-03): masked paged codes with the local 1s
 * TokenCountdown, audited one-time reveal + copy, revoke via Popconfirm,
 * mail-failed / revoked tags, error retry, empty state and the reset.changed
 * SSE refetch contract (StreamGate session).
 */
import { describe, expect, it, vi } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import StreamGate from '../../components/StreamGate'
import ResetTokensPanel from './ResetTokensPanel'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { fail, makeLoginData, ok, paged, sseChannel } from '../../test/handlers'
import { expectToast } from '../../test/toast'

interface TokenRowSpec {
  id?: number
  code_masked?: string
  expires_at?: string
  revoked?: boolean
  mail_failed?: boolean
  user?: { id: number; name: string; email: string }
}

function tokenRow(overrides: TokenRowSpec = {}) {
  return {
    id: 1,
    code_masked: 'a1b2…z9y8',
    expires_at: new Date(Date.now() + 90_000).toISOString(),
    seconds_left: 90,
    revoked: false,
    mail_failed: false,
    user: { id: 9, name: 'Budi Santoso', email: 'budi@kidversa.id' },
    ...overrides,
  }
}

function listTokens(rows: unknown[], total = rows.length, seen?: URL[]) {
  return http.get('/api/reset-tokens', ({ request }) => {
    const url = new URL(request.url)
    seen?.push(url)
    return paged(rows, {
      page: Number(url.searchParams.get('page') ?? '1'),
      per_page: Number(url.searchParams.get('per_page') ?? '20'),
      total,
    })
  })
}

function rowEl(name: string): HTMLElement {
  const el = screen.getByText(name).closest('tr')
  if (!el) throw new Error(`no table row contains ${name}`)
  return el as HTMLElement
}

async function until(condition: () => boolean, step = 5, limit = 4000): Promise<void> {
  for (let i = 0; i < limit && !condition(); i += 1) {
    await act(async () => {
      await vi.advanceTimersByTimeAsync(step)
    })
  }
  expect(condition()).toBe(true)
}

async function advance(ms: number): Promise<void> {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms)
  })
}

describe('token list', () => {
  it('renders user identity, masked code, mail-failed/status tags and a countdown cell', async () => {
    server.use(
      listTokens([
        tokenRow(),
        tokenRow({
          id: 2,
          code_masked: 'c3d4…x7w6',
          revoked: true,
          mail_failed: true,
          user: { id: 10, name: 'Siti Aminah', email: 'siti@kidversa.id' },
        }),
      ]),
    )
    renderApp(<ResetTokensPanel />)

    await screen.findByText('Budi Santoso')
    const activeRow = rowEl('Budi Santoso')
    expect(within(activeRow).getByText('budi@kidversa.id')).toBeInTheDocument()
    expect(within(activeRow).getByText('a1b2…z9y8')).toBeInTheDocument() // masked code
    expect(within(activeRow).getByText('Aktif')).toBeInTheDocument()
    expect(within(activeRow).queryByText('Email gagal')).toBeNull()
    expect(within(activeRow).getByText(/^\d{2}:\d{2}$/)).toBeInTheDocument() // countdown cell

    const revokedRow = rowEl('Siti Aminah')
    expect(within(revokedRow).getByText('c3d4…x7w6')).toBeInTheDocument()
    expect(within(revokedRow).getByText('Email gagal')).toBeInTheDocument()
    expect(within(revokedRow).getByText('Dicabut')).toBeInTheDocument()
    expect(within(revokedRow).getByText('siti@kidversa.id')).toBeInTheDocument()
  })

  it('ticks mm:ss from expires_at and clamps expired rows to 00:00', async () => {
    vi.useFakeTimers({
      toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval', 'Date'],
    })
    // +999ms keeps floor(remaining/1000) at 90s for the sub-second fixture→mount drift.
    const soon = new Date(Date.now() + 90_999).toISOString()
    const expired = new Date(Date.now() - 60_000).toISOString()
    server.use(
      listTokens([
        tokenRow({ id: 1, expires_at: soon }),
        tokenRow({
          id: 2,
          expires_at: expired,
          user: { id: 10, name: 'Siti Aminah', email: 'siti@kidversa.id' },
        }),
      ]),
    )
    renderApp(<ResetTokensPanel />)

    await until(() => screen.queryByText('Budi Santoso') !== null)
    expect(within(rowEl('Budi Santoso')).getByText('01:30')).toBeInTheDocument()
    expect(within(rowEl('Siti Aminah')).getByText('00:00')).toBeInTheDocument()

    await advance(30_000)
    expect(within(rowEl('Budi Santoso')).getByText('01:00')).toBeInTheDocument()
    expect(within(rowEl('Siti Aminah')).getByText('00:00')).toBeInTheDocument() // still clamped
  })

  it('renders the translated list error and recovers through Coba lagi', async () => {
    let broken = true
    const seen: URL[] = []
    server.use(
      http.get('/api/reset-tokens', ({ request }) => {
        seen.push(new URL(request.url))
        if (broken) return fail('DB_UNAVAILABLE')
        return paged([tokenRow()], { page: 1, per_page: 20, total: 1 })
      }),
    )
    const user = userEvent.setup()
    renderApp(<ResetTokensPanel />)

    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Layanan sedang sibuk. Coba lagi.')

    broken = false
    await user.click(within(alert).getByRole('button', { name: 'Coba lagi' }))
    await screen.findByText('Budi Santoso')
    expect(seen).toHaveLength(2)
    expect(screen.queryByRole('alert')).toBeNull()
  })

  it('renders the empty state when the server returns no rows', async () => {
    server.use(listTokens([]))
    renderApp(<ResetTokensPanel />)

    await screen.findByText('Tidak ada data')
  })
})

describe('reveal and copy', () => {
  it('reveals the one-time code behind an audit note and copies it to the clipboard', async () => {
    let reveals = 0
    server.use(
      listTokens([tokenRow()]),
      http.post('/api/reset-tokens/1/reveal', () => {
        reveals += 1
        return ok({ code_plain: 'KX9M2PLQ' })
      }),
    )
    const user = userEvent.setup()
    // userEvent.setup() installs its own clipboard mock — override it afterwards.
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(window.navigator, 'clipboard', { value: { writeText }, configurable: true })
    renderApp(<ResetTokensPanel />)

    await screen.findByText('Budi Santoso')
    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Tampilkan' }))

    await screen.findByText('Kode hanya ditampilkan sekali dan tercatat dalam audit.')
    expect(reveals).toBe(1)
    // getByLabelText('Kode reset') would also match the dialog via aria-labelledby.
    expect(screen.getByRole('textbox', { name: 'Kode reset' })).toHaveValue('KX9M2PLQ')

    await user.click(screen.getByRole('button', { name: 'Salin' }))
    await screen.findByText('Tersalin')
    expect(writeText).toHaveBeenCalledWith('KX9M2PLQ')
  })

  it('toasts the translated error when the reveal is refused', async () => {
    let reveals = 0
    server.use(
      listTokens([tokenRow()]),
      http.post('/api/reset-tokens/1/reveal', () => {
        reveals += 1
        return fail('FORBIDDEN_ROLE')
      }),
    )
    const user = userEvent.setup()
    renderApp(<ResetTokensPanel />)

    await screen.findByText('Budi Santoso')
    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Tampilkan' }))

    await screen.findByText('Anda tidak punya akses untuk tindakan ini.')
    expect(reveals).toBe(1)
    expect(screen.queryByRole('dialog')).toBeNull()
  })

  it('disables reveal and revoke for a revoked code', async () => {
    server.use(listTokens([tokenRow({ revoked: true })]))
    renderApp(<ResetTokensPanel />)

    await screen.findByText('Budi Santoso')
    const actions = within(rowEl('Budi Santoso'))
    expect(actions.getByRole('button', { name: 'Tampilkan' })).toBeDisabled()
    expect(actions.getByRole('button', { name: 'Cabut' })).toBeDisabled()
  })
})

describe('revoke', () => {
  it('confirms through the popconfirm, revokes and refetches the list', async () => {
    const seen: URL[] = []
    let revokes = 0
    server.use(
      listTokens([tokenRow()], 1, seen),
      http.post('/api/reset-tokens/1/revoke', () => {
        revokes += 1
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<ResetTokensPanel />)

    await screen.findByText('Budi Santoso')
    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Cabut' }))

    const titleEl = await screen.findByText('Cabut kode reset untuk Budi Santoso?')
    const popover = titleEl.closest('.ant-popover')
    if (!popover) throw new Error('revoke popconfirm was not rendered')
    const pop = within(popover as HTMLElement)
    expect(pop.getByRole('button', { name: 'OK' })).toBeInTheDocument() // antd default (en_US)
    expect(pop.getByRole('button', { name: 'Cancel' })).toBeInTheDocument()
    await user.click(pop.getByRole('button', { name: 'OK' }))

    await waitFor(() => expect(revokes).toBe(1))
    await waitFor(() => expect(seen).toHaveLength(2)) // list refetched exactly once
    await expectToast('Kode reset “Budi Santoso” dicabut')
  })

  it('toasts the translated error without refetching when the revoke fails', async () => {
    const seen: URL[] = []
    let revokes = 0
    server.use(
      listTokens([tokenRow()], 1, seen),
      http.post('/api/reset-tokens/1/revoke', () => {
        revokes += 1
        return fail('FORBIDDEN_ROLE')
      }),
    )
    const user = userEvent.setup()
    renderApp(<ResetTokensPanel />)

    await screen.findByText('Budi Santoso')
    await user.click(within(rowEl('Budi Santoso')).getByRole('button', { name: 'Cabut' }))

    const titleEl = await screen.findByText('Cabut kode reset untuk Budi Santoso?')
    const popover = titleEl.closest('.ant-popover')
    if (!popover) throw new Error('revoke popconfirm was not rendered')
    await user.click(within(popover as HTMLElement).getByRole('button', { name: 'OK' }))

    await screen.findByText('Anda tidak punya akses untuk tindakan ini.')
    expect(revokes).toBe(1)
    expect(seen).toHaveLength(1) // a failed revoke never refetches
  })
})

describe('sse refetch', () => {
  it('one debounced reset.changed refetch with the identical query', async () => {
    // Only the debounce clock may be faked: MSW + undici need their real
    // macrotasks (sessions integration pattern).
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    const seen: URL[] = []
    const channel = sseChannel('/api/stream')
    server.use(channel.handler, listTokens([tokenRow()], 1, seen))
    renderApp(
      <>
        <StreamGate />
        <ResetTokensPanel />
      </>,
      { session: makeLoginData() },
    )

    await until(() => channel.handshakes === 1)
    await until(() => screen.queryByText('Budi Santoso') !== null)

    const before = seen.at(-1)?.search
    const count = seen.length
    channel.push('event: reset.changed\ndata: {"rev":1}\n\n')
    channel.push('event: reset.changed\ndata: {"rev":2}\n\n')
    await advance(300)
    await advance(0)
    expect(seen).toHaveLength(count + 1)
    expect(seen.at(-1)?.search).toBe(before)

    // rev 1 again — already seen, the gate drops it without a refetch.
    channel.push('event: reset.changed\ndata: {"rev":1}\n\n')
    await advance(300)
    await advance(0)
    expect(seen).toHaveLength(count + 1)
  })
})
