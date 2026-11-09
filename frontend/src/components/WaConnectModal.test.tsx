/**
 * WaConnectModal (design 2026-11-09 §7.2): SA-only connect flow. The choice
 * view offers OpenWA-in-a-new-tab or in-app QR pairing; Connect Directly
 * starts the session, renders OpenWA's QR, and polls sequentially (2 s after
 * each response) until the session reports `ready`.
 */
import { useState } from 'react'
import { describe, expect, it, vi } from 'vitest'
import { act, fireEvent, screen, within } from '@testing-library/react'
import { http } from 'msw'
import WaConnectModal from './WaConnectModal'
import { renderApp } from '../test/render'
import { server } from '../test/setup'
import { makeLoginData, makeUser, ok } from '../test/handlers'

function saSession() {
  return makeLoginData({ user: makeUser({ role: 'SA' }) })
}

const QR_A = 'data:image/png;base64,AAA'
const QR_B = 'data:image/png;base64,BBB'

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

function dialog(): HTMLElement {
  return screen.getByRole('dialog')
}

function Harness() {
  const [open, setOpen] = useState(true)
  return <WaConnectModal open={open} onClose={() => setOpen(false)} />
}

describe('WaConnectModal choice view', () => {
  it('shows both choices', () => {
    renderApp(<WaConnectModal open onClose={() => {}} />, { session: saSession() })

    expect(within(dialog()).getByRole('button', { name: 'Buka OpenWA' })).toBeInTheDocument()
    expect(
      within(dialog()).getByRole('button', { name: 'Hubungkan Langsung' }),
    ).toBeInTheDocument()
  })

  it('Open OpenWA opens :8003 and closes', () => {
    const open = vi.spyOn(window, 'open').mockReturnValue(null)
    const onClose = vi.fn()
    renderApp(<WaConnectModal open onClose={onClose} />, { session: saSession() })

    fireEvent.click(within(dialog()).getByRole('button', { name: 'Buka OpenWA' }))

    expect(open).toHaveBeenCalledWith(
      'http://localhost:8003',
      '_blank',
      'noopener,noreferrer',
    )
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('WaConnectModal connect view', () => {
  it('Connect Directly starts the session and shows the QR', async () => {
    server.use(
      http.post('/api/wa/session/start', () =>
        ok({ connected: false, status: 'qr_ready', qr_code: QR_A }),
      ),
    )
    renderApp(<WaConnectModal open onClose={() => {}} />, { session: saSession() })

    fireEvent.click(within(dialog()).getByRole('button', { name: 'Hubungkan Langsung' }))

    const img = await screen.findByRole('img', { name: 'Kode QR WhatsApp' })
    expect(img).toHaveAttribute('src', QR_A)
  })

  it('polls, refreshes the QR, then shows connected + phone', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    let gets = 0
    server.use(
      http.post('/api/wa/session/start', () =>
        ok({ connected: false, status: 'qr_ready', qr_code: QR_A }),
      ),
      http.get('/api/wa/session', () => {
        gets += 1
        return gets === 1
          ? ok({ connected: false, status: 'qr_ready', qr_code: QR_B })
          : ok({ connected: true, status: 'ready', phone: '628123' })
      }),
    )
    renderApp(<WaConnectModal open onClose={() => {}} />, { session: saSession() })

    fireEvent.click(within(dialog()).getByRole('button', { name: 'Hubungkan Langsung' }))
    await until(() => screen.queryByRole('img', { name: 'Kode QR WhatsApp' }) !== null)
    expect(screen.getByRole('img', { name: 'Kode QR WhatsApp' })).toHaveAttribute('src', QR_A)

    await advance(2000)
    await until(
      () =>
        screen.queryByRole('img', { name: 'Kode QR WhatsApp' })?.getAttribute('src') === QR_B,
    )
    expect(gets).toBe(1)

    await advance(2000)
    await until(() => screen.queryByText('Terhubung') !== null)
    expect(screen.getByText('628123')).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: 'Kode QR WhatsApp' })).toBeNull()

    await advance(6000)
    expect(gets).toBe(2)
  })

  it('already ready shows connected + phone with no QR and no polling', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    let gets = 0
    server.use(
      http.post('/api/wa/session/start', () =>
        ok({ connected: true, status: 'ready', phone: '628123' }),
      ),
      http.get('/api/wa/session', () => {
        gets += 1
        return ok({ connected: true, status: 'ready' })
      }),
    )
    renderApp(<WaConnectModal open onClose={() => {}} />, { session: saSession() })

    fireEvent.click(within(dialog()).getByRole('button', { name: 'Hubungkan Langsung' }))
    await until(() => screen.queryByText('Terhubung') !== null)
    expect(screen.getByText('628123')).toBeInTheDocument()
    expect(screen.queryByRole('img', { name: 'Kode QR WhatsApp' })).toBeNull()

    await advance(5000)
    expect(gets).toBe(0)
  })

  it('unreachable shows the error and Retry', async () => {
    server.use(
      http.post('/api/wa/session/start', () => ok({ connected: false, status: 'unreachable' })),
    )
    renderApp(<WaConnectModal open onClose={() => {}} />, { session: saSession() })

    fireEvent.click(within(dialog()).getByRole('button', { name: 'Hubungkan Langsung' }))

    expect(await screen.findByText('OpenWA tidak dapat dijangkau')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Coba lagi' })).toBeInTheDocument()
  })

  it('missing shows the session-not-found copy and Retry', async () => {
    server.use(
      http.post('/api/wa/session/start', () => ok({ connected: false, status: 'missing' })),
    )
    renderApp(<WaConnectModal open onClose={() => {}} />, { session: saSession() })

    fireEvent.click(within(dialog()).getByRole('button', { name: 'Hubungkan Langsung' }))

    expect(await screen.findByText('Sesi OpenWA tidak ditemukan')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Coba lagi' })).toBeInTheDocument()
  })

  it('closing stops polling', async () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'setInterval', 'clearInterval'] })
    let gets = 0
    server.use(
      http.post('/api/wa/session/start', () =>
        ok({ connected: false, status: 'qr_ready', qr_code: QR_A }),
      ),
      http.get('/api/wa/session', () => {
        gets += 1
        return ok({ connected: false, status: 'qr_ready', qr_code: QR_A })
      }),
    )
    renderApp(<Harness />, { session: saSession() })

    fireEvent.click(within(dialog()).getByRole('button', { name: 'Hubungkan Langsung' }))
    await until(() => screen.queryByRole('img', { name: 'Kode QR WhatsApp' }) !== null)
    await advance(2000)
    await until(() => gets === 1)

    fireEvent.click(screen.getByRole('button', { name: 'Tutup' }))
    await advance(10000)
    expect(gets).toBe(1)
  })

  it('Back returns to the choice view', async () => {
    server.use(
      http.post('/api/wa/session/start', () =>
        ok({ connected: false, status: 'qr_ready', qr_code: QR_A }),
      ),
    )
    renderApp(<WaConnectModal open onClose={() => {}} />, { session: saSession() })

    fireEvent.click(within(dialog()).getByRole('button', { name: 'Hubungkan Langsung' }))
    await screen.findByRole('img', { name: 'Kode QR WhatsApp' })

    fireEvent.click(screen.getByRole('button', { name: 'Kembali' }))

    expect(screen.getByRole('button', { name: 'Buka OpenWA' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Hubungkan Langsung' })).toBeInTheDocument()
  })
})
