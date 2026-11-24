/**
 * Shared helpers for the masters + children pages (wave F3, spec F-04..F-07,
 * 02§3 paging rule). Exports ONLY plain hooks/functions (no JSX) so pages can
 * reuse:
 *
 * - `usePagedList` — a thin STATEFUL wrapper over the shared list primitive
 *   (`lib/list` `useListLoader`, 004a): owns page/perPage/filters state
 *   (merging filter/sort patches with a page=1 reset, 02§3) and normalizes
 *   the error to an `errors.<CODE>` string at the boundary; the primitive
 *   owns fetching + the SSE same-params refetch. No polling anywhere: the
 *   only fetch triggers are mount, page/filter/sort change, explicit retry,
 *   and the SSE signal.
 * - option hooks for the filter/form pickers (best-effort: a failed options
 *   fetch leaves the picker empty; the page's own list fetch surfaces errors).
 * - the ONE upload pre-check — since Pembaruan 2026-11-10 (spec 09) it lives
 *   in `lib/validation` and this module re-exports it for the master pages
 *   (program/topic badges, frames). The WA/age/badge rules live in
 *   `lib/validation` (004c) and the `errors.<CODE>` toast in `lib/errors` (004d) — one
 *   implementation each.
 */
import { useCallback, useEffect, useMemo, useState } from 'react'
import { api, errorCode, type ListParams } from '../../lib/api'
import { useListLoader } from '../../lib/list'

/* ---------- list ---------- */

export interface PagedList<T> {
  rows: T[]
  total: number
  page: number
  perPage: number
  loading: boolean
  /** `errors.<CODE>` key from the last failed fetch; null while healthy. */
  error: string | null
  /** Current filter/sort params (read-only; drives controlled inputs). */
  filters: ListParams
  /** Merge a filter/sort patch into the query and reset page to 1 (02§3). */
  setFilters: (patch: ListParams) => void
  setPage: (page: number) => void
  setPerPage: (perPage: number) => void
  /** Re-request with the current params (retry button, post-mutation, SSE). */
  refetch: () => void
}

export function usePagedList<T>(
  path: string,
  event: string,
  initial: ListParams = {},
): PagedList<T> {
  const [params, setParams] = useState<ListParams>(initial)
  const [page, setPage] = useState(1)
  const [perPage, setPerPage] = useState(20)

  const query = useMemo<ListParams>(
    () => ({ ...params, page, per_page: perPage }),
    [params, page, perPage],
  )
  const { rows, meta, error, loading, reload } = useListLoader<T>(path, query, [event])

  const setFilters = useCallback((patch: ListParams) => {
    setParams((prev) => ({ ...prev, ...patch }))
    setPage(1)
  }, [])

  return {
    rows,
    total: meta?.total ?? 0,
    page,
    perPage,
    loading,
    error: error === null ? null : errorCode(error),
    filters: params,
    setFilters,
    setPage,
    setPerPage,
    refetch: reload,
  }
}

/* ---------- picker options ---------- */

export interface NameOption {
  value: number
  label: string
}

export interface TopicOption extends NameOption {
  program_id: number
  program_name: string
}

/** All school programs for filter/form pickers (label = name). */
export function useProgramOptions(): NameOption[] {
  const [options, setOptions] = useState<NameOption[]>([])
  useEffect(() => {
    let cancelled = false
    api
      .list<{ id: number; name: string }>('/api/programs', {
        per_page: 1000,
        sort: 'name',
        dir: 'asc',
      })
      .then(
        (res) => {
          if (!cancelled) setOptions(res.data.map((row) => ({ value: row.id, label: row.name })))
        },
        () => {
          // Best-effort picker; the page's own list fetch surfaces API errors.
        },
      )
    return () => {
      cancelled = true
    }
  }, [])
  return options
}

/** Topics (optionally of one program); carries program_id/program_name for grouping. */
export function useTopicOptions(programId?: number): TopicOption[] {
  const [options, setOptions] = useState<TopicOption[]>([])
  const id = programId ?? null
  useEffect(() => {
    let cancelled = false
    api
      .list<{ id: number; name: string; program_id: number; program_name: string }>(
        '/api/topics',
        { per_page: 1000, sort: 'name', dir: 'asc', program_id: id ?? undefined },
      )
      .then(
        (res) => {
          if (cancelled) return
          setOptions(
            res.data.map((row) => ({
              value: row.id,
              label: row.name,
              program_id: row.program_id,
              program_name: row.program_name,
            })),
          )
        },
        () => {
          // Best-effort picker; the page's own list fetch surfaces API errors.
        },
      )
    return () => {
      cancelled = true
    }
  }, [id])
  return options
}

/* ---------- upload pre-check (shared gate, moved to lib/validation) ---------- */

export { IMAGE_ACCEPT, isAcceptedImage } from '../../lib/validation'
