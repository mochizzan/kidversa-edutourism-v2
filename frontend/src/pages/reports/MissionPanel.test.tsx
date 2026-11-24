/**
 * Mission picker (F-12 / FL-13): the picks list + empty state, the lazy
 * picker modal loading the topic bank, the ≤4 client pre-block (5th
 * checkbox disabled), the replace-set PUT contract, the MISSION_LIMIT
 * server refusal inline, and picked missions missing from the bank staying
 * selectable.
 */
import { describe, expect, it } from 'vitest'
import { act, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import MissionPanel from './MissionPanel'
import { emitSignal } from '../../lib/stream'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { ok, paged } from '../../test/handlers'

const P = '/api/sessions/1/topics/3/children/2'

const BANK = [
  { id: 1, name: 'Misi Satu' },
  { id: 2, name: 'Misi Dua' },
  { id: 3, name: 'Misi Tiga' },
  { id: 4, name: 'Misi Empat' },
  { id: 5, name: 'Misi Lima' },
]

function bankHandler(rows = BANK) {
  return http.get('/api/missions', ({ request }) => {
    const url = new URL(request.url)
    expect(url.searchParams.get('topic_id')).toBe('9')
    return paged(rows, { page: 1, per_page: 1000, total: rows.length })
  })
}

describe('mission panel', () => {
  it('shows the empty state until missions are picked', async () => {
    server.use(http.get(`${P}/missions`, () => ok([])))
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    await screen.findByText('Belum ada misi terpilih.')
  })

  it('lists picked missions by name only', async () => {
    server.use(
      http.get(`${P}/missions`, () => ok([{ mission_id: 21, name: 'Misi Alpha' }])),
    )
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    await screen.findByText('Misi Alpha')
    expect(screen.queryByText(/Body alpha/)).toBeNull()
  })

  it('saves a ≤4 selection via the replace-set PUT and refetches', async () => {
    let picks = [{ mission_id: 21, name: 'Misi Alpha' }]
    let putBody: unknown = null
    server.use(
      http.get(`${P}/missions`, () => ok(picks)),
      bankHandler(),
      http.put(`${P}/missions`, async ({ request }) => {
        putBody = await request.json()
        picks = [
          { mission_id: 1, name: 'Misi Satu' },
          { mission_id: 2, name: 'Misi Dua' },
        ]
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    await screen.findByText('Misi Alpha')
    await user.click(screen.getByRole('button', { name: 'Ubah pilihan' }))

    const modal = await screen.findByRole('dialog')
    // Pre-picked mission still present (from the picks list append) and
    // counts toward the cap.
    const checkboxes = within(modal).getAllByRole('checkbox')
    expect(checkboxes).toHaveLength(BANK.length + 1)
    expect(within(modal).getByRole('checkbox', { name: /Misi Alpha/ })).toBeChecked()
    expect(within(modal).getByText('1/4')).toBeInTheDocument()

    await user.click(within(modal).getByRole('checkbox', { name: /Misi Satu/ }))
    expect(within(modal).getByText('2/4')).toBeInTheDocument()

    // Pick two more, then the fifth must be client-blocked.
    await user.click(within(modal).getByRole('checkbox', { name: /Misi Dua/ }))
    await user.click(within(modal).getByRole('checkbox', { name: /Misi Tiga/ }))
    expect(within(modal).getByText('4/4')).toBeInTheDocument()
    const fifth = within(modal).getByRole('checkbox', { name: /Misi Empat/ })
    expect(fifth).toBeDisabled()
    // Already-checked boxes stay togglable.
    expect(within(modal).getByRole('checkbox', { name: /Misi Satu/ })).toBeEnabled()

    await user.click(within(modal).getByRole('button', { name: 'Simpan' }))

    await screen.findByText('Misi disimpan.')
    await waitFor(() =>
      expect(putBody).toEqual({ mission_ids: [21, 1, 2, 3] }),
    )
    // Own-200 refetch renders the new picks. Scoped to the card: the closed
    // antd modal keeps its children mounted (hidden) for the diff.
    const card = screen.getByText('Misi').closest('.ant-card') as HTMLElement
    await waitFor(() => {
      expect(within(card).getAllByText(/Misi Satu/)).toHaveLength(1)
    })
    expect(within(card).queryByText(/Misi Alpha/)).toBeNull()
  })

  it('opening the picker before the picks load seeds an empty selection', async () => {
    const gate = Promise.withResolvers<void>()
    let putBody: unknown = null
    server.use(
      http.get(`${P}/missions`, async () => {
        await gate.promise
        return ok([{ mission_id: 1, name: 'Misi Satu' }])
      }),
      bankHandler(),
      http.put(`${P}/missions`, async ({ request }) => {
        putBody = await request.json()
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    // Picks still in flight (panel shows the spinner) → open the bank anyway.
    await user.click(screen.getByRole('button', { name: 'Ubah pilihan' }))
    const modal = await screen.findByRole('dialog')
    await within(modal).findByText('Misi Satu')
    expect(within(modal).getByRole('checkbox', { name: /Misi Satu/ })).not.toBeChecked()

    await user.click(within(modal).getByRole('checkbox', { name: /Misi Satu/ }))
    await user.click(within(modal).getByRole('button', { name: 'Simpan' }))
    gate.resolve()
    await waitFor(() => expect(putBody).toEqual({ mission_ids: [1] }))
  })

  it('a deselected mission drops out of the replace-set payload', async () => {
    let putBody: unknown = null
    server.use(
      http.get(`${P}/missions`, () =>
        ok([
          { mission_id: 1, name: 'Misi Satu' },
          { mission_id: 2, name: 'Misi Dua' },
        ]),
      ),
      bankHandler(),
      http.put(`${P}/missions`, async ({ request }) => {
        putBody = await request.json()
        return ok({})
      }),
    )
    const user = userEvent.setup()
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    await screen.findByText('Misi Satu')
    await user.click(screen.getByRole('button', { name: 'Ubah pilihan' }))
    const modal = await screen.findByRole('dialog')
    expect(within(modal).getByRole('checkbox', { name: /Misi Satu/ })).toBeChecked()

    // Un-check an already-picked mission → it leaves the set (toggle-off path).
    await user.click(within(modal).getByRole('checkbox', { name: /Misi Satu/ }))
    expect(within(modal).getByRole('checkbox', { name: /Misi Satu/ })).not.toBeChecked()

    await user.click(within(modal).getByRole('button', { name: 'Simpan' }))
    await screen.findByText('Misi disimpan.')
    expect(putBody).toEqual({ mission_ids: [2] })
  })

  it('MISSION_LIMIT from the server renders inline in the modal', async () => {
    server.use(
      http.get(`${P}/missions`, () => ok([])),
      bankHandler(),
      http.put(`${P}/missions`, () =>
        HttpResponse.json({ error: { code: 'MISSION_LIMIT', message: 'too many' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    await screen.findByText('Belum ada misi terpilih.')
    await user.click(screen.getByRole('button', { name: 'Ubah pilihan' }))
    const modal = await screen.findByRole('dialog')
    await within(modal).findByText('Misi Satu')
    await user.click(within(modal).getByRole('checkbox', { name: /Misi Satu/ }))
    await user.click(within(modal).getByRole('button', { name: 'Simpan' }))

    await within(modal).findByText('Maksimal 4 misi per anak per topik.')
  })

  it('a failed picks read renders the translated alert under the list', async () => {
    server.use(
      http.get(`${P}/missions`, () =>
        HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } }),
      ),
    )
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    // No crash: the alert carries the code while the list shows nothing.
    await screen.findByText('Layanan sedang sibuk. Coba lagi.')
  })

  it('a failed bank read renders the alert inside the modal', async () => {
    server.use(
      http.get(`${P}/missions`, () => ok([])),
      http.get('/api/missions', () =>
        HttpResponse.json({ error: { code: 'DB_UNAVAILABLE', message: 'down' } }),
      ),
    )
    const user = userEvent.setup()
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    await screen.findByText('Belum ada misi terpilih.')
    await user.click(screen.getByRole('button', { name: 'Ubah pilihan' }))

    const modal = await screen.findByRole('dialog')
    await within(modal).findByText('Layanan sedang sibuk. Coba lagi.')
  })

  it('mission-pick.changed refetches the picks list', async () => {
    let picksCalls = 0
    server.use(
      http.get(`${P}/missions`, () => {
        picksCalls += 1
        return ok([])
      }),
    )
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    await waitFor(() => expect(picksCalls).toBe(1))
    act(() => emitSignal('mission-pick.changed', { rev: 1 }))
    await waitFor(() => expect(picksCalls).toBeGreaterThan(1))
  })

  it('mission.changed refetches the OPEN bank (selection kept); a closed picker stays lazy', async () => {
    const rowsRef = { rows: [BANK[0]] }
    let bankCalls = 0
    server.use(
      http.get(`${P}/missions`, () => ok([])),
      http.get('/api/missions', ({ request }) => {
        bankCalls += 1
        const url = new URL(request.url)
        expect(url.searchParams.get('topic_id')).toBe('9')
        return paged(rowsRef.rows, { page: 1, per_page: 1000, total: rowsRef.rows.length })
      }),
    )
    const user = userEvent.setup()
    renderApp(<MissionPanel prefix={P} masterTopicId={9} />)

    await screen.findByText('Belum ada misi terpilih.')
    // Picker closed → the bank is lazy: the signal must not fetch it.
    act(() => emitSignal('mission.changed', { rev: 1 }))
    await act(async () => Promise.resolve())
    expect(bankCalls).toBe(0)

    await user.click(screen.getByRole('button', { name: 'Ubah pilihan' }))
    const modal = await screen.findByRole('dialog')
    await within(modal).findByText('Misi Satu')
    expect(bankCalls).toBe(1)
    await user.click(within(modal).getByRole('checkbox', { name: /Misi Satu/ }))

    // A master edit elsewhere → the OPEN picker refetches; the in-progress
    // selection must survive the refresh.
    rowsRef.rows = [BANK[0], BANK[1]]
    act(() => emitSignal('mission.changed', { rev: 2 }))
    await waitFor(() => expect(bankCalls).toBe(2))
    expect(within(modal).getByRole('checkbox', { name: /Misi Dua/ })).toBeInTheDocument()
    expect(within(modal).getByRole('checkbox', { name: /Misi Satu/ })).toBeChecked()

    // Picker closed again → the next signal fetches nothing until reopened.
    await user.click(within(modal).getByRole('button', { name: 'Batal' }))
    act(() => emitSignal('mission.changed', { rev: 3 }))
    await act(async () => Promise.resolve())
    expect(bankCalls).toBe(2)
  })
})
