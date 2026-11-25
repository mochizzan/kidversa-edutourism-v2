/**
 * Shared child-gallery plumbing (F-10 / FL-10): the session/member/consent
 * context both gallery surfaces need, the route builders that keep the two
 * pages pointing at each other, and the program-linked frame picker used by
 * the capture studio.
 *
 * The gallery (`/sessions/:sid/gallery/children/:cid`) and the camera
 * (`…/capture`) are two faces of the same workspace: the gallery owns photos,
 * the camera owns the viewfinder. Keeping the gate here means both surfaces
 * lock identically when consent is not SETUJU.
 */
import { useCallback, useEffect, useRef, useState } from 'react'
import { useNavigate, useParams, useSearchParams } from 'react-router'
import { api } from '../../lib/api'
import { useCrumbName } from '../../lib/crumbs'
import { useEventRefetch } from '../../lib/list'
import {
  pPrefix,
  type SessionDetail,
  type SessionGroup,
  type SessionMember,
} from '../../lib/session-contracts'
import { useSessionDetail } from '../sessions/shared'

/** A frame the session's program can apply as a capture overlay. */
export interface FrameOption {
  id: number
  name: string
  image_url: string
}

export interface ChildGalleryContext {
  sid: string | undefined
  cid: number
  detail: SessionDetail | null
  detailError: unknown
  deleted: boolean
  reloadDetail: () => void
  member: SessionMember | null
  group: SessionGroup | null
  /** Effective topic: `?stid=` when it still exists, else the first topic. */
  activeStid: number | null
  consent: string
  unlocked: boolean
  /** Per-child-per-topic API prefix P; null until the workspace is usable. */
  prefix: string | null
  changeTopic: (next: number) => void
}

export function childGalleryPath(sid: string | number, cid: number): string {
  return `/sessions/${sid}/gallery/children/${cid}`
}

export function childCapturePath(
  sid: string | number,
  cid: number,
  stid?: number | null,
): string {
  const base = `${childGalleryPath(sid, cid)}/capture`
  return stid === null || stid === undefined ? base : `${base}?stid=${stid}`
}

/**
 * Resolves the child workspace from the route. The `?stid=` query is the
 * single source of truth for the topic, so gallery → camera navigation keeps
 * the scope without an extra fetch.
 */
export function useChildGallery(): ChildGalleryContext {
  const { sid, cid: cidParam } = useParams()
  const navigate = useNavigate()
  const [searchParams, setSearchParams] = useSearchParams()
  const { detail, error: detailError, deleted, reload: reloadDetail } = useSessionDetail(sid)

  const cid = Number(cidParam)

  // `consent.changed` already refetches inside useSessionDetail, so both
  // gallery surfaces unlock/relock live without a second subscription here.

  useCrumbName('sid', detail?.session.name)

  const found =
    detail?.groups
      .flatMap((group) =>
        group.own
          ? (group.members ?? []).map((member) => ({ member, group }))
          : [],
      )
      .find((entry) => entry.member.child_id === cid) ?? null
  const member = found?.member ?? null
  const group = found?.group ?? null

  useCrumbName('cid', member?.name)

  useEffect(() => {
    if (deleted) navigate('/sessions', { replace: true })
  }, [deleted, navigate])

  const requestedStid = Number(searchParams.get('stid')) || null
  // A stale `?stid=` falls back to the session's first topic.
  const activeStid =
    requestedStid !== null && detail?.topics.some((topic) => topic.stid === requestedStid)
      ? requestedStid
      : detail?.topics[0]?.stid ?? null

  const consent = member?.consent ?? ''
  const unlocked = consent === 'SETUJU'

  const prefix =
    sid !== undefined && activeStid !== null && member !== null && unlocked
      ? pPrefix(sid, activeStid, cid)
      : null

  const changeTopic = useCallback(
    (next: number) => {
      setSearchParams({ stid: String(next) }, { replace: true })
    },
    [setSearchParams],
  )

  return {
    sid,
    cid,
    detail,
    detailError,
    deleted,
    reloadDetail,
    member,
    group,
    activeStid,
    consent,
    unlocked,
    prefix,
    changeTopic,
  }
}

export interface ProgramFramesState {
  frames: FrameOption[]
  loading: boolean
  error: unknown
  /** Re-run the fetch (the capture rail's retry action). */
  reload: () => void
}

/**
 * Frames linked to the session's program (`GET /api/frames/options`). Frame
 * decoration is optional, so a failed fetch never blocks capture — the studio
 * simply offers "no frame". `frame.changed` refetches the picker.
 */
export function useProgramFrames(
  programId: number | null,
  enabled: boolean,
): ProgramFramesState {
  const [frames, setFrames] = useState<FrameOption[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const request = useRef(0)

  const load = useCallback(() => {
    const current = ++request.current
    if (!enabled || programId === null) {
      setFrames([])
      setLoading(false)
      setError(null)
      return
    }
    setLoading(true)
    api.get<FrameOption[]>('/api/frames/options', { program_id: programId }).then(
      (data) => {
        if (current !== request.current) return
        setFrames(data)
        setError(null)
        setLoading(false)
      },
      (failure: unknown) => {
        if (current !== request.current) return
        setFrames([])
        setError(failure)
        setLoading(false)
      },
    )
  }, [enabled, programId])

  useEffect(() => {
    load()
  }, [load])

  useEventRefetch(['frame.changed'], load)

  return { frames, loading, error, reload: load }
}
