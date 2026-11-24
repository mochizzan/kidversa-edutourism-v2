/**
 * Session context for Kidversa (spec F-01, 02§3, 02§4).
 *
 * - Access token lives in MEMORY only (lib/api module var) — set here.
 * - Active school is persisted in localStorage `kv-school` (X-School-ID source)
 *   and mirrored in api.ts for SCHOOL_MISMATCH recovery (spec 06 E7).
 * - localStorage `kv-session` marks "this browser had a login" so a reload can
 *   restore via POST /api/auth/refresh (cookie) without a pointless refresh
 *   round-trip for anonymous visitors. It is written by `setSession` unless the
 *   caller passes `remember: false` — the login form's Remember Me toggle:
 *   off means the session lives in memory only, so a return visit is
 *   anonymous again.
 * - SA default school = the latest created ACTIVE school chosen by the server
 *   and returned in the login/refresh payload; switching uses
 *   POST /api/auth/switch-school (same access token, new X-School-ID).
 */
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import { api, setAccessToken, setSessionExpiredHandler, setSessionSchool } from './api'
import { registerSignal, type SignalPayload } from './stream'

export type Role = 'SA' | 'ADMIN' | 'FASILITATOR'
export type UserDecision = 'PENDING' | 'APPROVED' | 'REJECTED'

/**
 * Pinned user JSON (W1 contract). Users-list rows share this shape.
 * `photo_url` (spec 09) is carried ONLY on the auth-shaped payloads (login,
 * refresh-restore, /me, profile PATCH, photo POST/DELETE) — the backend
 * omits the key when no photo is set; absent = fall back to the initials
 * avatar. Users-list rows (`userItem`) keep the legacy shape.
 */
export interface User {
  id: number
  name: string
  email: string
  role: Role
  decision: UserDecision
  is_active: boolean
  school_id: number | null
  wa_number: string | null
  photo_url?: string
}

export interface School {
  id: number
  school_name: string
  country: string
  city: string | null
  is_active: boolean
}

/**
 * Additive block carried by POST /api/users/:id/impersonate and, while the
 * session is impersonated, by POST /api/auth/refresh (authoritative) and
 * GET /api/auth/me (best-effort). Optional everywhere: its absence means the
 * session is NOT impersonated.
 */
export interface Impersonation {
  actor_id: number
  actor_name: string
  /** Role of the acting user at switch time. */
  actor_role: Role
}

/** Payload of POST /api/auth/login and /api/auth/refresh (pinned W1 contract). */
export interface LoginData {
  access: string
  user: User
  /** W1: null only when no ACTIVE school exists (edge) — the app then runs without X-School-ID. */
  school: School | null
  /** Present only while the session was started through impersonation. */
  impersonation?: Impersonation
}

/**
 * Options when adopting a session (spec F-01 / FL-02): the login form's
 * Remember Me toggle gates persistence through this.
 */
export interface SessionOptions {
  /** false → memory only: the kv-session boot-restore marker is not written. */
  remember?: boolean
}

/** Decision data consumed by the router: RequireRole computes it, Protected renders it. */
export type AuthDecision =
  | { status: 'allow' }
  | { status: 'anon' }
  | { status: 'forbidden'; redirectTo: string }

/** Canonical access rule (spec 02§3 — RequireRole is the single guard). */
export function requireAuth(user: User | null, roles?: readonly Role[]): AuthDecision {
  if (!user) return { status: 'anon' }
  if (roles && !roles.includes(user.role)) {
    // FAS lands on its own sessions; everyone else on the dashboard.
    return { status: 'forbidden', redirectTo: user.role === 'FASILITATOR' ? '/sessions' : '/' }
  }
  return { status: 'allow' }
}

export interface AuthContextValue {
  user: User | null
  school: School | null
  /** Non-null only while an admin is "signed in as" another user. */
  impersonation: Impersonation | null
  /** false until the boot refresh restore has settled (prevents a premature /login redirect). */
  ready: boolean
  /** Adopt a session: login response or boot refresh restore. `remember: false` = memory only. */
  setSession(data: LoginData, options?: SessionOptions): void
  /** Replace the cached user after a profile change. */
  setUser(user: User): void
  logout(): Promise<void>
  switchSchool(schoolId: number): Promise<void>
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function useAuth(): AuthContextValue {
  const value = useContext(AuthContext)
  if (!value) throw new Error('useAuth() must be used inside <AuthProvider>')
  return value
}

const KV_SESSION = 'kv-session'

/**
 * One restore promise per tab: React StrictMode mounts the provider effect
 * twice, and two CONCURRENT refreshes race the single-use jti (the loser would
 * be forced to re-login on every dev reload).
 */
let restoreInFlight: Promise<LoginData> | null = null

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null)
  const [school, setSchool] = useState<School | null>(null)
  const [impersonation, setImpersonation] = useState<Impersonation | null>(null)
  // Anonymous visitors are ready immediately; a previous login awaits the restore.
  const [ready, setReady] = useState(() => localStorage.getItem(KV_SESSION) === null)

  /**
   * Monotonic session epoch (impersonation race guard). A boot restore that
   * started BEFORE an impersonation switch must not adopt the old identity
   * afterwards: it captures the epoch and drops its result when the epoch has
   * moved on (setSession/clearLocal both bump it).
   */
  const sessionEpoch = useRef(0)

  const setSession = useCallback((data: LoginData, options?: SessionOptions) => {
    sessionEpoch.current += 1
    setAccessToken(data.access)
    // The kv-session marker is what the boot effect (below) restores on a
    // reload. With Remember Me off the session lives in memory only — write no
    // marker (and drop a stale one from an earlier remember-me login), so a
    // return visit boots anonymous instead of auto-restoring.
    if (options?.remember === false) localStorage.removeItem(KV_SESSION)
    else localStorage.setItem(KV_SESSION, '1')
    if (data.school) {
      localStorage.setItem('kv-school', String(data.school.id))
      setSessionSchool(data.school.id)
    } else {
      localStorage.removeItem('kv-school') // never keep a stale school header
      setSessionSchool(null)
    }
    setUser(data.user)
    setSchool(data.school)
    setImpersonation(data.impersonation ?? null)
    setReady(true)
  }, [])

  const clearLocal = useCallback(() => {
    sessionEpoch.current += 1
    setAccessToken(null)
    setSessionSchool(null)
    localStorage.removeItem('kv-school')
    localStorage.removeItem(KV_SESSION)
    setUser(null)
    setSchool(null)
    setImpersonation(null)
    setReady(true)
  }, [])

  /**
   * One in-flight bootstrap restore per tab, shared by the boot effect and
   * the school.changed context reload below — the SAME load path
   * (POST /api/auth/refresh), no invented endpoint.
   */
  const startRestore = useCallback((): Promise<LoginData> => {
    restoreInFlight ??= api.post<LoginData>('/api/auth/refresh').finally(() => {
      restoreInFlight = null
    })
    return restoreInFlight
  }, [])

  // Restore the session on reload: memory token is gone, refresh cookie remains.
  useEffect(() => {
    if (localStorage.getItem(KV_SESSION) === null) return
    const restore = startRestore()
    const epoch = sessionEpoch.current
    let cancelled = false
    restore.then(
      (data) => {
        // A stale restore (an impersonation switch happened meanwhile) must
        // never overwrite the new identity with the old one.
        if (!cancelled && sessionEpoch.current === epoch) setSession(data)
      },
      () => {
        // Dead/missing refresh cookie → anonymous (spec 06: re-login, no loop).
        if (!cancelled && sessionEpoch.current === epoch) clearLocal()
      },
    )
    return () => {
      cancelled = true
    }
  }, [startRestore, setSession, clearLocal])

  // api.ts reports unrecoverable auth (refresh INVALID_TOKEN / ACCOUNT_INACTIVE).
  useEffect(() => {
    setSessionExpiredHandler(clearLocal)
    return () => setSessionExpiredHandler(null)
  }, [clearLocal])

  /**
   * Context reload (spec 02§2c, `school.changed` on the OWN school): re-run
   * the bootstrap load path. A background failure keeps the running context —
   * dead refresh cookies still clear through the session-expired handler.
   */
  const reloadContext = useCallback(() => {
    if (localStorage.getItem(KV_SESSION) === null) return
    const epoch = sessionEpoch.current
    startRestore().then(
      (data) => {
        if (sessionEpoch.current === epoch) setSession(data)
      },
      () => {
        // Transient failure: keep the current user/school (no logout on a blip).
      },
    )
  }, [startRestore, setSession])

  // Latest-state refs (useEventRefetch pattern): the listener registered once
  // below always sees the CURRENT session school and reload fn without
  // re-registration on a plain render.
  const schoolIdRef = useRef<number | null>(null)
  const reloadRef = useRef<() => void>(() => {})
  useEffect(() => {
    schoolIdRef.current = school?.id ?? null
    reloadRef.current = reloadContext
  })

  const hasSchool = school !== null
  useEffect(() => {
    if (!hasSchool) return // no session → nothing to reload for
    // Signal listener (payload passed through; the refetch wrappers drop it):
    // reload the context ONLY when frame.school_id is this session's school.
    return registerSignal<SignalPayload>('school.changed', (data) => {
      const incoming = Number(data.school_id)
      const current = schoolIdRef.current
      if (current === null || !Number.isFinite(incoming) || incoming !== current) return
      reloadRef.current()
    })
  }, [hasSchool])

  const logout = useCallback(async () => {
    try {
      await api.post('/api/auth/logout')
    } catch {
      // Best-effort server revoke; local sign-out must succeed regardless.
    }
    clearLocal()
  }, [clearLocal])

  const switchSchool = useCallback(async (schoolId: number) => {
    const data = await api.post<{ school: School }>('/api/auth/switch-school', {
      school_id: schoolId,
    })
    localStorage.setItem('kv-school', String(data.school.id))
    setSessionSchool(data.school.id)
    setSchool(data.school) // StreamGate keys on school.id → stream closes + reopens
  }, [])

  const value = useMemo<AuthContextValue>(
    () => ({
      user,
      school,
      impersonation,
      ready,
      setSession,
      setUser,
      logout,
      switchSchool,
    }),
    [user, school, impersonation, ready, setSession, logout, switchSchool],
  )

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}
