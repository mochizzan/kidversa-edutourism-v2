/**
 * Canonical ReportView JSON mirror (contracts file `2026-10-09-contracts-shapes.md`,
 * "Report" section — backend B3 emits EXACTLY this shape). Nullable variants
 * follow the JSON annotations 1:1: `narrative.text`/`narrative.generated_at`
 * nullable, `group` nullable, `badges.*` null when undefined, `candidate_photo`
 * null when unset.
 */

export interface ReportData {
  session: { id: number; name: string; date: string; location: string; status: string }
  program: { name: string }
  topic: { stid: number; name: string }
  child: {
    id: number
    name: string
    age: number
    school: string
    mother_name: string
    father_name: string
    guardian_name: string
  }
  group: { name: string } | null
  attestation: { name: string; role: string }
  narrative: { text: string | null; generated_at: string | null }
  outcomes: Array<{ said: number; activity_name: string; indicator: string; stars: number; note: string }>
  missions: Array<{ mission_id: number; name: string }>
  badges: {
    topic: { name: string; image_url: string } | null
    program: { name: string; image_url: string } | null
  }
  candidate_photo: { url: string } | null
  complete: boolean
  missing: Array<{ said: number; name: string }>
}
