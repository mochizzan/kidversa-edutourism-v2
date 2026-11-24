/**
 * `masters/shared` hooks beyond the pure helpers covered by shared.test.ts:
 * usePagedList (cursor + error + SSE same-params refetch contract, 02§3
 * decision 25), the best-effort picker option hooks and the errors.<CODE>
 * toast.
 */
import type { ReactNode } from 'react'
import { describe, expect, it } from 'vitest'
import { act, renderHook, waitFor } from '@testing-library/react'
import { App as AntApp } from 'antd'
import { ApiError } from '../../lib/api'
import { emitSignal, refetchEvents } from '../../lib/stream'
import { server } from '../../test/setup'
import { fail } from '../../test/handlers'
import {
  failFirstThenList,
  pagedList,
  programRow,
  recordList,
  topicRow,
} from '../../test/helpers.cov-master'
import { useErrorToast } from '../../lib/errors'
import {
  usePagedList,
  useProgramOptions,
  useTopicOptions,
} from './shared'

function antdWrapper({ children }: { children: ReactNode }) {
  return <AntApp>{children}</AntApp>
}

describe('usePagedList', () => {
  it('loads rows/meta into the pager state under the initial sort', async () => {
    const list = pagedList('/api/programs', [programRow(1, 'Alpha')], 45)
    server.use(list.handler)
    const { result } = renderHook(() =>
      usePagedList<{ id: number; name: string }>('/api/programs', 'program.changed', {
        sort: 'id',
        dir: 'asc',
      }),
    )
    expect(result.current.loading).toBe(true)

    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(result.current.rows).toHaveLength(1)
    expect(result.current.rows[0]).toMatchObject({ id: 1, name: 'Alpha' })
    expect(result.current.total).toBe(45)
    expect(result.current.page).toBe(1)
    expect(result.current.perPage).toBe(20)
    expect(result.current.error).toBeNull()
    expect(result.current.filters).toEqual({ sort: 'id', dir: 'asc' })
    expect(list.seen[0].searchParams.get('page')).toBe('1')
    expect(list.seen[0].searchParams.get('per_page')).toBe('20')
    expect(list.seen[0].searchParams.get('sort')).toBe('id')
  })

  it('setPage and setPerPage re-request with only the cursor changed', async () => {
    const list = pagedList('/api/programs', [], 45)
    server.use(list.handler)
    const { result } = renderHook(() =>
      usePagedList('/api/programs', 'program.changed', { sort: 'id', dir: 'asc' }),
    )
    await waitFor(() => expect(result.current.loading).toBe(false))
    const first = list.seen[0].search

    act(() => result.current.setPage(3))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('3'))
    expect(list.seen.at(-1)?.searchParams.get('per_page')).toBe('20')
    expect(list.seen.at(-1)?.search.replace('page=3', 'page=1')).toBe(first)

    act(() => result.current.setPerPage(50))
    expect(result.current.loading).toBe(true)
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(list.seen.at(-1)?.searchParams.get('per_page')).toBe('50')
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('3') // Pager rewinds, not the hook
  })

  it('filter patches merge into the query and rewind page to 1 (02§3)', async () => {
    const list = pagedList('/api/programs', [], 45)
    server.use(list.handler)
    const { result } = renderHook(() =>
      usePagedList('/api/programs', 'program.changed', { sort: 'id', dir: 'asc' }),
    )
    await waitFor(() => expect(result.current.loading).toBe(false))

    act(() => result.current.setPage(2))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('page')).toBe('2'))

    act(() => result.current.setFilters({ q: 'batik' }))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.get('q')).toBe('batik'))
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1') // filter rewinds
    expect(list.seen.at(-1)?.searchParams.get('sort')).toBe('id') // sort survives the patch
    expect(result.current.filters).toEqual({ sort: 'id', dir: 'asc', q: 'batik' })

    act(() => result.current.setFilters({ q: undefined }))
    await waitFor(() => expect(list.seen.at(-1)?.searchParams.has('q')).toBe(false))
    expect(list.seen.at(-1)?.searchParams.get('page')).toBe('1')
  })

  it('the SSE refetch re-requests the SAME query and unregisters on unmount', async () => {
    const list = pagedList('/api/programs', [], 45)
    server.use(list.handler)
    const { result, unmount } = renderHook(() =>
      usePagedList('/api/programs', 'program.changed', { sort: 'id', dir: 'asc', q: 'batik' }),
    )
    await waitFor(() => expect(result.current.loading).toBe(false))
    expect(refetchEvents()).toContain('program.changed')
    const before = (list.seen.at(-1) as URL).search
    const count = list.seen.length

    act(() => emitSignal('program.changed', { rev: 1 }))
    await waitFor(() => expect(list.seen).toHaveLength(count + 1))
    expect((list.seen.at(-1) as URL).search).toBe(before)

    unmount()
    expect(refetchEvents()).not.toContain('program.changed')
    act(() => emitSignal('program.changed', { rev: 2 }))
    await act(async () => Promise.resolve())
    expect(list.seen).toHaveLength(count + 1) // closed page never refetches
  })

  it('surfaces errors.<CODE>, clears it on retry and keeps the same cursor', async () => {
    const list = failFirstThenList('/api/programs', 'DB_UNAVAILABLE', [programRow(1, 'Alpha')])
    server.use(list.handler)
    const { result } = renderHook(() =>
      usePagedList('/api/programs', 'program.changed', { sort: 'id', dir: 'asc' }),
    )

    await waitFor(() => expect(result.current.error).toBe('DB_UNAVAILABLE'))
    expect(result.current.loading).toBe(false)
    expect(result.current.rows).toEqual([])

    act(() => result.current.refetch())
    await waitFor(() => expect(result.current.error).toBeNull())
    expect(result.current.rows).toHaveLength(1)
    expect(list.seen[1].search).toBe(list.seen[0].search) // retry = identical query
  })
})

describe('picker option hooks', () => {
  it('useProgramOptions maps rows to {value, label}', async () => {
    const list = pagedList('/api/programs', [programRow(1, 'Alpha'), programRow(2, 'Beta')])
    server.use(list.handler)
    const { result } = renderHook(() => useProgramOptions())

    await waitFor(() =>
      expect(result.current).toEqual([
        { value: 1, label: 'Alpha' },
        { value: 2, label: 'Beta' },
      ]),
    )
    expect(list.seen[0].searchParams.get('per_page')).toBe('1000')
    expect(list.seen[0].searchParams.get('sort')).toBe('name')
  })

  it('a failed options fetch leaves the picker empty (best-effort)', async () => {
    const list = recordList('/api/programs', () => fail('DB_UNAVAILABLE'))
    server.use(list.handler)
    const { result } = renderHook(() => useProgramOptions())

    await waitFor(() => expect(list.seen).toHaveLength(1))
    expect(result.current).toEqual([])
  })

  it('useTopicOptions carries the program scope and maps grouping fields', async () => {
    const list = pagedList('/api/topics', [
      topicRow(2, 'Batik Nusantara', { program_id: 7, program_name: 'Eksplorasi Kota' }),
    ])
    server.use(list.handler)
    const { result } = renderHook(() => useTopicOptions(7))

    await waitFor(() =>
      expect(result.current).toEqual([
        {
          value: 2,
          label: 'Batik Nusantara',
          program_id: 7,
          program_name: 'Eksplorasi Kota',
        },
      ]),
    )
    expect(list.seen[0].searchParams.get('program_id')).toBe('7')
  })

  it('useTopicOptions without a program omits program_id and tolerates failure', async () => {
    const list = recordList('/api/topics', () => fail('DB_UNAVAILABLE'))
    server.use(list.handler)
    const { result } = renderHook(() => useTopicOptions())

    await waitFor(() => expect(list.seen).toHaveLength(1))
    expect(list.seen[0].searchParams.has('program_id')).toBe(false)
    expect(result.current).toEqual([]) // failure leaves the picker empty
  })
})

describe('useErrorToast', () => {
  it('renders errors.<CODE> for ApiError and the INTERNAL message otherwise', async () => {
    const { result } = renderHook(() => useErrorToast(), { wrapper: antdWrapper })

    act(() => result.current(new ApiError('RATE_LIMITED', 'slow down')))
    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Terlalu banyak permintaan. Tunggu sebentar lalu coba lagi.',
      ),
    )

    act(() => result.current(new Error('socket hang up')))
    await waitFor(() =>
      expect(document.querySelector('.ant-message')?.textContent).toContain(
        'Terjadi kesalahan tak terduga. Coba lagi.',
      ),
    )
  })
})
