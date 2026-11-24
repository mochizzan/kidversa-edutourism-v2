/**
 * Session create wizard (FL-07): ineligible programs are disabled WITH the
 * reason; end≤start is blocked client-side without any request;
 * CHILD_DUPLICATE from the server renders inline; PROGRAM_INCOMPLETE jumps
 * back to the program step; group drafts validate name+owner before any
 * request; the create POST carries the auto X-Idempotency-Key, omits empty
 * groups, and hands the added children to the detail page as copy state;
 * cross-group child dedup disables already-chosen options.
 */
import { describe, expect, it } from 'vitest'
import { act, fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent, { type UserEvent } from '@testing-library/user-event'
import { http, HttpResponse } from 'msw'
import { useLocation, useParams, type RouteObject } from 'react-router'
import SessionCreatePage from './SessionCreatePage'
import { emitSignal } from '../../lib/stream'
import { renderApp } from '../../test/render'
import { server } from '../../test/setup'
import { makeLoginData, makeUser, paged } from '../../test/handlers'
import { chooseOption, openDropdown, sessionChildRow } from '../../test/helpers.cov-session'

interface CreateCapture {
  calls: number
  headers: Headers | null
  body: Record<string, unknown> | null
}

function DetailMarker() {
  const { sid } = useParams()
  const location = useLocation()
  const state = location.state as { copyChildren?: Array<{ cid: number; name: string }> } | null
  return (
    <div>
      SESSION DETAIL {sid}
      {state?.copyChildren ? (
        <span>{`COPY:${state.copyChildren.map((entry) => `${entry.cid}:${entry.name}`).join(',')}`}</span>
      ) : null}
    </div>
  )
}

const ROUTES: RouteObject[] = [
  { path: '/sessions/new', element: <SessionCreatePage /> },
  { path: '/sessions/:sid', element: <DetailMarker /> },
]

/** Fresh Response per request — a Response body is single-consumption. */
function usersHandler() {
  return http.get('/api/users', () =>
    paged(
      [
        {
          id: 7,
          name: 'Dewi Lestari',
          email: 'dewi@kidversa.id',
          role: 'FASILITATOR',
          decision: 'APPROVED',
          is_active: true,
          school_id: 1,
        },
      ],
      { page: 1, per_page: 1000, total: 1 },
    ),
  )
}

function baseHandlers(capture: CreateCapture, post: () => Response) {
  return [
    http.get('/api/programs', () =>
      paged(
        [
          { id: 1, name: 'Eksplorasi Kota', eligible: true },
          { id: 2, name: 'Program Kosong', eligible: false },
        ],
        { page: 1, per_page: 1000, total: 2 },
      ),
    ),
    http.get('/api/children', () => paged([sessionChildRow(2, 'Aisyah Putri'), sessionChildRow(3, 'Budi Santoso')], { page: 1, per_page: 1000, total: 2 })),
    usersHandler(),
    http.post('/api/sessions', async ({ request }) => {
      capture.calls += 1
      capture.headers = new Headers(request.headers)
      capture.body = (await request.json()) as Record<string, unknown>
      return post()
    }),
  ]
}

async function pickProgram(user: UserEvent, name = 'Eksplorasi Kota'): Promise<void> {
  await user.click(await screen.findByRole('combobox'))
  await user.click(await screen.findByText(name))
  await user.click(screen.getByRole('button', { name: /Lanjut/ }))
}

/** Step "Detail": meta fields in DOM order (name, description, location). */
async function fillMeta(user: UserEvent): Promise<void> {
  await user.type(screen.getByPlaceholderText('mis. Kelas Batik'), 'Kelas Batik')
  await user.type(screen.getByPlaceholderText('mis. Museum Tekstil'), 'Museum Tekstil')
  // antd DatePicker (rc-picker 4 mask input): a synthesized `change` event is
  // a deliberate no-op — the value commits only through real keydowns, and
  // Enter submits the pending selection to the controlled state.
  await user.type(document.querySelector('.ant-picker-input > input') as Element, '2026-11-15{Enter}')
}

function capture(): CreateCapture {
  return { calls: 0, headers: null, body: null }
}

describe('form session wizard program step', () => {
  it('disables the ineligible program WITH the reason — picking it never advances', async () => {
    const cap = capture()
    server.use(...baseHandlers(cap, () => HttpResponse.json({ data: { session_id: 42 } })))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await user.click(await screen.findByRole('combobox'))
    const reason = await screen.findByText('Program Kosong — Program belum lengkap (topik/kegiatan).')
    // Disabled option: clicking it must NOT select it.
    await user.click(reason)
    expect(reason).toBeInTheDocument()
    // Still on step 0: the picker prompt is visible and Lanjut cannot proceed.
    expect(screen.getByText('Pilih program yang akan dijalani sesi ini.')).toBeInTheDocument()
    expect(cap.calls).toBe(0)
  })

  it('PROGRAM_INCOMPLETE from the server jumps back to the program step', async () => {
    const cap = capture()
    server.use(
      ...baseHandlers(cap, () =>
        HttpResponse.json({
          error: { code: 'PROGRAM_INCOMPLETE', message: 'Program belum lengkap: setiap topik butuh minimal satu kegiatan.' },
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)
    await fillMeta(user)
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))
    await user.click(screen.getByRole('button', { name: 'Buat sesi' }))

    await screen.findByText(
      'Program belum lengkap: setiap topik butuh minimal satu kegiatan.',
    )
    // Back on step 0.
    expect(screen.getByText('Pilih program yang akan dijalani sesi ini.')).toBeInTheDocument()
    expect(cap.calls).toBe(1)
  })
})

describe('form session wizard meta validation', () => {
  it('blocks end<=start without any request', async () => {
    const cap = capture()
    server.use(...baseHandlers(cap, () => HttpResponse.json({ data: { session_id: 42 } })))
    const user = userEvent.setup()
    const view = renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)
    await fillMeta(user)
    // Picker inputs in DOM order: date (0), start (1), end (2).
    const [, startInput, endInput] = view.container.querySelectorAll(
      '.ant-picker-input > input',
    )
    await user.type(startInput, '10:00{Enter}')
    await user.type(endInput, '09:00{Enter}')
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))

    await screen.findByText('Waktu selesai harus setelah waktu mulai.')
    expect(cap.calls).toBe(0)
    // Still on the meta step.
    expect(screen.queryByRole('button', { name: 'Buat sesi' })).toBeNull()
  })
})

describe('form session wizard groups step', () => {
  it('validates group name then owner before any request', async () => {
    const cap = capture()
    server.use(...baseHandlers(cap, () => HttpResponse.json({ data: { session_id: 42 } })))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)
    await fillMeta(user)
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))
    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    await user.click(screen.getByRole('button', { name: 'Buat sesi' }))
    // Name takes precedence (owner is only validated once the name is set).
    await screen.findByText('Nama kelompok wajib diisi.')
    expect(screen.queryByText('Pilih fasilitator kelompok.')).toBeNull()

    await user.type(screen.getByPlaceholderText('mis. Kelompok Merah'), 'Kelompok Merah')
    await user.click(screen.getByRole('button', { name: 'Buat sesi' }))
    await screen.findByText('Pilih fasilitator kelompok.')
    expect(cap.calls).toBe(0)
  })

  it('creates with groups: contract payload, idem header, copyChildren state', async () => {
    const cap = capture()
    server.use(...baseHandlers(cap, () => HttpResponse.json({ data: { session_id: 42 } })))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)
    await fillMeta(user)
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))

    // Group draft: name + active facilitator + one child.
    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    await user.type(screen.getByPlaceholderText('mis. Kelompok Merah'), 'Kelompok Merah')
    const [ownerPicker, childPicker] = screen.getAllByRole('combobox')
    await openDropdown(ownerPicker)
    await chooseOption('Dewi Lestari · dewi@kidversa.id')
    await openDropdown(childPicker)
    await chooseOption('Aisyah Putri · SD Cendekia')
    await user.keyboard('{Escape}')

    await user.click(screen.getByRole('button', { name: 'Buat sesi' }))

    await screen.findByText('SESSION DETAIL 42')
    expect(cap.calls).toBe(1)
    expect(cap.body).toEqual({
      program_id: 1,
      name: 'Kelas Batik',
      date: '2026-11-15',
      location: 'Museum Tekstil',
      groups: [{ name: 'Kelompok Merah', facilitator_id: 7, child_ids: [2] }],
    })
    expect(cap.headers?.get('X-Idempotency-Key')).toMatch(/^[0-9a-f-]{36}$/)
    // Children added at create are handed to the detail page for the copy flow.
    await screen.findByText('COPY:2:Aisyah Putri')
  })

  it('CHILD_DUPLICATE from the server renders inline without leaving the step', async () => {
    const cap = capture()
    server.use(
      ...baseHandlers(cap, () =>
        HttpResponse.json({
          error: {
            code: 'CHILD_DUPLICATE',
            message: 'Anak sudah berada di kelompok lain pada sesi ini.',
          },
        }),
      ),
    )
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)
    await fillMeta(user)
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))
    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    await user.type(screen.getByPlaceholderText('mis. Kelompok Merah'), 'Kelompok Merah')
    const [ownerPicker, childPicker] = screen.getAllByRole('combobox')
    await openDropdown(ownerPicker)
    await chooseOption('Dewi Lestari · dewi@kidversa.id')
    await openDropdown(childPicker)
    await chooseOption('Aisyah Putri · SD Cendekia')
    await user.keyboard('{Escape}')

    await user.click(screen.getByRole('button', { name: 'Buat sesi' }))

    await screen.findByText('Anak sudah berada di kelompok lain pada sesi ini.')
    expect(cap.calls).toBe(1)
    // Still on the groups step.
    expect(screen.getByRole('button', { name: /Tambah kelompok/ })).toBeInTheDocument()
  })

  it('client dedup: a child chosen in one group is disabled in the next', async () => {
    const cap = capture()
    server.use(...baseHandlers(cap, () => HttpResponse.json({ data: { session_id: 42 } })))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)
    await fillMeta(user)
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))

    // Group 1 takes Aisyah.
    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    const pickers1 = screen.getAllByRole('combobox')
    await openDropdown(pickers1[1])
    await chooseOption('Aisyah Putri · SD Cendekia')
    await user.keyboard('{Escape}')
    const group1Picker = pickers1[1].closest('.ant-select') as HTMLElement
    expect(group1Picker.querySelectorAll('.ant-select-selection-item')).toHaveLength(1)

    // Group 2: Aisyah's option is disabled (client-side CHILD_DUPLICATE dedup).
    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    const pickers2 = screen.getAllByRole('combobox')
    await openDropdown(pickers2[3])
    const aisyahOption = await chooseOption('Aisyah Putri · SD Cendekia')
    // antd marks the blocked option; clicking it must not select anything.
    expect(aisyahOption).toHaveClass('ant-select-item-option-disabled')
    fireEvent.click(aisyahOption)
    const group2Picker = pickers2[3].closest('.ant-select') as HTMLElement
    expect(group2Picker.querySelectorAll('.ant-select-selection-item')).toHaveLength(0)
    // Budi remains selectable.
    await chooseOption('Budi Santoso · SD Cendekia')
    expect(group2Picker.querySelectorAll('.ant-select-selection-item')).toHaveLength(1)
    expect(cap.calls).toBe(0)
  })

  it('deleting a group draft removes its card; back/cancel steer the wizard', async () => {
    const cap = capture()
    server.use(...baseHandlers(cap, () => HttpResponse.json({ data: { session_id: 42 } })))
    const user = userEvent.setup()
    renderApp(null, {
      routes: [...ROUTES, { path: '/sessions', element: <div>SESSIONS LIST</div> }],
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)
    await fillMeta(user)
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))

    // Two drafts, one gets deleted — the other's card stays. (Icon prefixes
    // join the accessible name, so match by suffix.)
    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    await user.type(screen.getByPlaceholderText('mis. Kelompok Merah'), 'Kelompok Merah')
    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    expect(screen.getAllByPlaceholderText('mis. Kelompok Merah')).toHaveLength(2)
    await user.click(screen.getAllByRole('button', { name: /Hapus/ })[1])
    expect(screen.getAllByPlaceholderText('mis. Kelompok Merah')).toHaveLength(1)
    expect(screen.getByPlaceholderText('mis. Kelompok Merah')).toHaveValue('Kelompok Merah')

    // Back to the meta step, forward again — the surviving draft persists.
    await user.click(screen.getByRole('button', { name: /Kembali/ }))
    expect(screen.queryAllByPlaceholderText('mis. Kelompok Merah')).toHaveLength(0)
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))
    expect(screen.getByPlaceholderText('mis. Kelompok Merah')).toHaveValue('Kelompok Merah')

    // Cancel leaves the wizard without any request.
    await user.click(screen.getByRole('button', { name: /Batal/ }))
    await screen.findByText('SESSIONS LIST')
    expect(cap.calls).toBe(0)
  })

  it('an empty program catalogue warns instead of a dead Select', async () => {
    server.use(
      http.get('/api/programs', () => paged([], { page: 1, per_page: 20, total: 0 })),
    )
    renderApp(<SessionCreatePage />, { route: '/sessions/new', session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }) })

    await screen.findByText('Belum ada program sama sekali.')
  })

  it('the Steps header can jump BACK to a finished step', async () => {
    const cap = capture()
    server.use(...baseHandlers(cap, () => HttpResponse.json({ data: { session_id: 42 } })))
    const user = userEvent.setup()
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await pickProgram(user)
    await fillMeta(user)
    await user.click(screen.getByRole('button', { name: /Lanjut/ }))
    // Groups start empty — add one draft so the step has content to return to.
    await user.click(screen.getByRole('button', { name: /Tambah kelompok/ }))
    expect(screen.getByPlaceholderText('mis. Kelompok Merah')).toBeInTheDocument()

    // Clicking the earlier "Program" step marker rewinds (no revalidation).
    await user.click(screen.getByText('Program'))
    expect(screen.getByText('Pilih program yang akan dijalani sesi ini.')).toBeInTheDocument()
    expect(cap.calls).toBe(0)
  })
})

describe('form session wizard SSE refetch (02§2c)', () => {
  it('program.changed refetches the program select; child.changed the children list', async () => {
    let programCalls = 0
    let childCalls = 0
    server.use(
      http.get('/api/programs', () => {
        programCalls += 1
        return paged(
          [{ id: 1, name: 'Eksplorasi Kota', eligible: true }],
          { page: 1, per_page: 1000, total: 1 },
        )
      }),
      http.get('/api/children', () => {
        childCalls += 1
        return paged([sessionChildRow(2, 'Aisyah Putri')], { page: 1, per_page: 1000, total: 1 })
      }),
    )
    renderApp(null, {
      routes: ROUTES,
      route: '/sessions/new',
      session: makeLoginData({ user: makeUser({ role: 'ADMIN' }) }),
    })

    await waitFor(() => expect(programCalls).toBe(1))
    await waitFor(() => expect(childCalls).toBe(1))

    // Each signal refetches ONLY its own table's list.
    act(() => emitSignal('program.changed', { rev: 1 }))
    await waitFor(() => expect(programCalls).toBe(2))
    expect(childCalls).toBe(1)

    act(() => emitSignal('child.changed', { rev: 2 }))
    await waitFor(() => expect(childCalls).toBe(2))
    expect(programCalls).toBe(2)
  })
})
