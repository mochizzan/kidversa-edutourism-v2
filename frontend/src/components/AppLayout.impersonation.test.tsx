/**
 * AppLayout impersonation strip (spec 09§9.2 banner contract, §9.6 item 5).
 *
 * The banner is tied to `impersonation` on the session: a plain sign-in must
 * never show it, an impersonated one must always show it with the target, the
 * target's role and the actor — and Exit ends the session through the real
 * `POST /api/auth/logout`. StreamGate is stubbed (the shell test owns no SSE).
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http } from 'msw'
import AppLayout from './AppLayout'
import { renderApp } from '../test/render'
import { makeLoginData, makeUser, ok } from '../test/handlers'
import type { LoginData } from '../lib/auth'
import { server } from '../test/setup'

vi.mock('./StreamGate', () => ({ default: () => null }))

const IMPERSONATION = { actor_id: 12, actor_name: 'Budi', actor_role: 'ADMIN' } as const

const TARGET = () => makeUser({ role: 'FASILITATOR', name: 'Siti Aminah' })

function renderShell(session: LoginData) {
  return renderApp(null, {
    session,
    routes: [
      {
        element: <AppLayout />,
        children: [{ index: true, element: <div>Konten</div> }],
      },
    ],
  })
}

/** The shell renders nothing until the boot refresh resolves. */
async function shellReady() {
  await screen.findByText('Konten')
}

beforeEach(() => {
  server.use(
    http.get('/api/schools', () => ok([])),
    http.get('/api/users/pending-count', () => ok({ pending: 0 })),
  )
})

describe('impersonation banner', () => {
  it('is absent for a plain session', async () => {
    const { container } = renderShell(
      makeLoginData({ user: TARGET(), impersonation: undefined }),
    )
    await shellReady()

    expect(container.querySelector('.kv-impersonation')).toBeNull()
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
    expect(screen.queryByText(/atas nama/)).not.toBeInTheDocument()
  })

  it('names the target, the target role and the actor while impersonating', async () => {
    renderShell(makeLoginData({ user: TARGET(), impersonation: { ...IMPERSONATION } }))
    await shellReady()

    const banner = screen.getByRole('status')
    expect(banner).toHaveClass('kv-impersonation')
    expect(banner).toHaveAttribute('aria-live', 'polite')
    // ID locale: "Masuk sebagai Siti Aminah (Fasilitator) atas nama Budi."
    expect(banner).toHaveTextContent(
      'Masuk sebagai Siti Aminah (Fasilitator) atas nama Budi.',
    )
    expect(within(banner).getByText(/Siti Aminah/)).toHaveClass('kv-impersonation__text')

    const exit = within(banner).getByRole('button', { name: 'Keluar' })
    expect(exit).toHaveClass('kv-impersonation__exit')
  })

  it('ends the session from Exit and removes the banner', async () => {
    let logoutCalled = false
    server.use(
      http.post('/api/auth/logout', () => {
        logoutCalled = true
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderShell(makeLoginData({ user: TARGET(), impersonation: { ...IMPERSONATION } }))
    await shellReady()

    await user.click(screen.getByRole('button', { name: 'Keluar' }))

    await waitFor(() => expect(screen.queryByRole('status')).not.toBeInTheDocument())
    expect(logoutCalled).toBe(true)
    expect(screen.queryByText('Konten')).not.toBeInTheDocument()
  })
})
