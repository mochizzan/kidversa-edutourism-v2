/**
 * F4 shared wire types and cross-slice helpers (004b — re-filed out of
 * pages/sessions/shared.ts so gallery/assessment/consent/reports no longer
 * depend on a "sessions" page file for contracts).
 *
 * Shapes follow docs/superskill/task/2026-10-09-contracts-shapes.md exactly.
 */
import type { TFunction } from 'i18next'
import type { SessionStatus } from '../components/StatusChip'

export type ConsentStatus = 'PENDING' | 'SETUJU' | 'DITOLAK'

export interface SessionListItem {
  id: number
  program_id: number
  program_name: string
  name: string
  description: string
  date: string
  start_time: string | null
  end_time: string | null
  location: string
  status: SessionStatus
  group_count: number
  member_count: number
  created_at: string
}

export interface SessionActivity {
  said: number
  activity_id: number
  name: string
  indicator: string
  sort_order: number
}

export interface SessionTopic {
  stid: number
  topic_id: number
  name: string
  sort_order: number
  activities: SessionActivity[]
}

export interface SessionMember {
  child_id: number
  name: string
  age: number
  school: string
  /** Consent status for this child in this session (member rows may predate the send). */
  consent: string
}

/** Shared keys of both group shapes (FAS wire pin F-B17). */
export interface SessionGroupBase {
  gid: number
  name: string
  member_count: number
  owner?: { id: number; name: string } | null
  owner_missing?: boolean
  /** Consent tallies {PENDING, SETUJU, DITOLAK} — absent on FAS reduced groups. */
  consent?: Record<string, number>
  /** Absent on FAS reduced foreign groups. */
  members?: SessionMember[]
}

/** Own group: full shape, unchanged — never carries the reduced foreign `status`. */
export interface SessionOwnGroup extends SessionGroupBase {
  own: true
  status?: never
}

/** FAS reduced foreign group: {gid, name, own:false, member_count, status}. */
export interface SessionForeignGroup extends SessionGroupBase {
  own: false
  /** Parent session's status verbatim (session_groups has no status column). */
  status: SessionStatus
}

/** Discriminated by `own`: foreign groups require `status`, own groups forbid it. */
export type SessionGroup = SessionOwnGroup | SessionForeignGroup

export interface SessionDetail {
  session: {
    id: number
    program_id: number
    program_name: string
    name: string
    description: string
    date: string
    start_time: string | null
    end_time: string | null
    location: string
    status: SessionStatus
    created_by: number
    created_at: string
  }
  topics: SessionTopic[]
  groups: SessionGroup[]
  /**
   * Wipe-scope counts for the typed delete dialog (contract amendment
   * 2026-10-09, pinned on B2). Optional: treated as absent-while-B2-lands
   * (dialog then falls back to detail/matrix-derived counts).
   */
  scope?: {
    topics: number
    groups: number
    members: number
    assessments: number
    photos: number
    narratives: number
    reports_sent: number
    links_report: number
    links_gallery: number
    links_consent: number
  }
}

export interface CopyCandidate {
  session_id: number
  session_name: string
  session_date: string
  status: SessionStatus
  counts: { assessments: number; narratives: number; missions: number; photos: number }
}

export interface CopyCounts {
  assessments: number
  narratives: number
  missions: number
  photos: number
}

export interface CopyResult {
  copied: CopyCounts
  skipped: CopyCounts
}

export interface ConsentItem {
  child_id: number
  child_name: string
  group_name: string
  status: ConsentStatus
  respondent_name: string | null
  responded_at: string | null
  consent_url?: string | null
}

export interface ReportsRow {
  child_id: number
  child_name: string
  session_topic_id: number
  topic_name: string
  complete: boolean
  narrative: boolean
  missions: number
  candidate_photo: boolean | null
  report_url: string | null
  gallery_url: string | null
}

export interface AssessmentRow {
  said: number
  stars: number | null
  note: string | null
  assessor_id: number | null
  updated_at: string
}

export interface PhotoItem {
  id: number
  url: string
  is_candidate: boolean
  created_at: string
  mime: string
  file_size: number
  /** Durable row whose file is gone on disk (E29 item flag, never an envelope). */
  missing?: boolean
}

export interface NarrativeItem {
  text: string
  model: string
  generated_at: string
}

export interface PickedMission {
  mission_id: number
  name: string
}

export interface PublicLink {
  kind: 'REPORT' | 'GALLERY'
  url: string
  token: string
  created_at: string
}

export interface Readiness {
  complete: boolean
  missing: { said: number; name: string }[]
  badges?: { topic: boolean; program: boolean }
}

export interface ChildRow {
  id: number
  name: string
  age: number
  age_confirmed: boolean
  school: string
  mother_name: string
  mother_wa: string
  father_name: string
  father_wa: string
  guardian_name: string
  is_active: boolean
  created_at: string
}

export interface FacilitatorRow {
  id: number
  name: string
  email: string
  role: string
  decision: string
  is_active: boolean
  school_id: number | null
}

/** Per-child-per-topic prefix P (binding, 02§2b). */
export function pPrefix(sid: number | string, stid: number, cid: number): string {
  return `/api/sessions/${sid}/topics/${stid}/children/${cid}`
}

/** RFC3339 → locale date-time; empty-safe. */
export function fmtDateTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const date = new Date(iso)
  return Number.isNaN(date.getTime()) ? iso : date.toLocaleString()
}

/** Consent status → translated label (kept lockstep with ConsentTag). */
export function consentStatusLabel(t: TFunction, status: string): string {
  if (status === 'SETUJU') return t('consent.approved')
  if (status === 'DITOLAK') return t('consent.rejected')
  return t('consent.pending')
}

/** Best-effort clipboard copy with an "already copied" feedback contract. */
export async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text)
    return true
  } catch {
    return false
  }
}
