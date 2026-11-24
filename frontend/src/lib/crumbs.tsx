/**
 * Display names for dynamic route segments, shared by the shell's breadcrumb
 * top bar.
 *
 * The bar resolves its trail from the URL alone, so `/sessions/:sid` can only
 * ever read "Detail sesi" on its own. A session-scoped page already holds the
 * session's real name, so it registers that name for its param with
 * `useCrumbName` and the bar prints it instead — one line per page, no extra
 * fetch, no server change.
 *
 * Names are keyed by param NAME (`sid`), not by value: one route renders at a
 * time, and a page clears its entry on unmount so a stale name never leaks into
 * the next detail page.
 *
 * The default context is inert, so a page test that mounts a page on its own
 * (outside the AppLayout shell) keeps working without a provider.
 */
import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react'
import type { ReactNode } from 'react'

export type CrumbNames = Record<string, string | undefined>

interface CrumbNamesValue {
  names: CrumbNames
  setName: (param: string, label: string | undefined) => void
}

const INERT: CrumbNamesValue = { names: {}, setName: () => {} }

const CrumbNamesContext = createContext<CrumbNamesValue>(INERT)

export function CrumbNamesProvider({ children }: { children: ReactNode }) {
  const [names, setNames] = useState<CrumbNames>({})

  const setName = useCallback((param: string, label: string | undefined) => {
    // Same value → same object: a page whose data has not changed yet must not
    // re-render the shell on every one of its own renders.
    setNames((current) => (current[param] === label ? current : { ...current, [param]: label }))
  }, [])

  const value = useMemo(() => ({ names, setName }), [names, setName])

  return <CrumbNamesContext.Provider value={value}>{children}</CrumbNamesContext.Provider>
}

export function useCrumbNames(): CrumbNames {
  return useContext(CrumbNamesContext).names
}

/** Register the human label for a dynamic route param (`sid`, `cid`, …). */
export function useCrumbName(param: string, label: string | undefined): void {
  const { setName } = useContext(CrumbNamesContext)

  useEffect(() => {
    setName(param, label)
  }, [param, label, setName])

  // Leaving the page drops its entry, so the next detail route starts blank
  // instead of inheriting the previous entity's name.
  useEffect(() => {
    return () => setName(param, undefined)
  }, [param, setName])
}
