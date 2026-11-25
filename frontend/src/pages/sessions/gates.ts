/**
 * Open-gate failure helpers shared by the session detail page and the session
 * list's inline Start action. The server attaches the offending groups under
 * `error.groups` for VALIDATION (groups without children) and
 * GROUP_WITHOUT_FACILITATOR (groups without an active owner).
 */
import { ApiError } from '../../lib/api'

/** One offending group from an open-gate failure (`error.groups`, B11). */
export interface GateGroup {
  name: string
}

/** Read the offending group names from an envelope failure; malformed → []. */
export function gateGroupsOf(failure: unknown): GateGroup[] {
  if (!(failure instanceof ApiError)) return []
  const raw = failure.details?.groups
  if (!Array.isArray(raw)) return []
  return raw.flatMap((entry) => {
    if (typeof entry !== 'object' || entry === null) return []
    const name = (entry as Record<string, unknown>).name
    return typeof name === 'string' && name.trim() !== '' ? [{ name }] : []
  })
}
