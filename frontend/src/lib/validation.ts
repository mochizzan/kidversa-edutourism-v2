/**
 * ONE client-side rule core (004c): the WhatsApp normalizer (05 §4, mirrors
 * backend/internal/validate.go normalizeWA), the children age parser (1–10
 * direct, 11–15 confirm, else invalid), the badge both-or-neither predicate,
 * the antd rule adapter (`toRule`/`toValueRule`) and — Pembaruan 2026-11-10
 * (spec 09) — the ONE shared upload gate (jpg/jpeg/png ≤50 MB) used by every
 * upload surface (masters badges/frames, gallery photos, profile photos).
 */
import type { RuleObject } from 'antd/es/form'

/**
 * Field-length caps (F05-07 named constants) mirror the backend's
 * validate.go (maxPersonNameLen / maxShortTextLen / maxNoteLen) and the
 * 05 §2 DDL widths; the byte caps mirror 05 §5. Every `maxLength` usage
 * imports from here — no bare numeric maxLength literals.
 */
export const MAX_PERSON_NAME = 160
export const MAX_SHORT_TEXT = 200
export const MAX_NOTE = 2000
export const MAX_RESET_CODE = 8
/** ConsentPage input cap (server allows 160 — respondent_name DDL). */
export const MAX_RESPONDENT_NAME = 120
/**
 * ONE upload rule (Pembaruan 2026-10-11, tightened 2026-11-10 by spec 09):
 * every upload ≤50 MB, JPG/JPEG/PNG only — WebP is retired from NEW uploads
 * (existing stored WebP files keep serving). Validated CLIENT-SIDE before
 * the request is sent; the server enforces the same whitelist by decoding
 * the real image bytes (09-PROFILE-PHOTOS decision 2).
 */
export const MAX_UPLOAD_BYTES = 50 * 1024 * 1024

/**
 * `Upload accept` attribute + the MIME list (incl. the nonstandard
 * `image/jpg`) AND the file extensions; ICO/WebP/GIF never reach the picker.
 */
export const IMAGE_ACCEPT = 'image/jpeg,image/jpg,image/png,.jpg,.jpeg,.png'

const IMAGE_TYPES = ['image/png', 'image/jpeg', 'image/jpg']

/**
 * The ONE upload gate: JPG/JPEG/PNG, non-empty, ≤50 MB. Accepted when EITHER
 * the MIME type or the file extension matches (a .jpg served as
 * application/octet-stream is still acceptable), anything else
 * (incl. WebP/GIF/ICO) rejected before send — used by every upload surface.
 */
export function isAcceptedImage(file: File): boolean {
  const typeOk =
    IMAGE_TYPES.includes(file.type.toLowerCase()) || /\.(png|jpe?g)$/i.test(file.name)
  return typeOk && file.size > 0 && file.size <= MAX_UPLOAD_BYTES
}

/** A check over a string form value → i18n key (null = valid). */
export type Check = (value: string) => string | null

/** A check over any form value (multi-select arrays, undefined) → i18n key. */
export type ValueCheck = (value: unknown) => string | null

/** 05 §4: digits only → 0→62, 8→62, 62 keep, else null; local part 8–15 digits. */
export function normalizeWa(raw: string): string | null {
  let digits = ''
  for (const ch of raw) {
    if (ch >= '0' && ch <= '9') digits += ch
  }
  let normalized: string
  if (digits.startsWith('0')) normalized = '62' + digits.slice(1)
  else if (digits.startsWith('8')) normalized = '62' + digits
  else if (digits.startsWith('62')) normalized = digits
  else return null
  const localLength = normalized.length - 2
  if (localLength < 8 || localLength > 15) return null
  return `+${normalized}`
}

export type AgeResult =
  | { ok: true; age: number; needConfirm: boolean }
  | { ok: false }

/** Integer 1–10 → save; 11–15 → confirm modal; anything else → invalid. */
export function parseAge(raw: string): AgeResult {
  const text = String(raw).trim()
  if (!/^\d+$/.test(text)) return { ok: false }
  const age = Number(text)
  if (age >= 1 && age <= 10) return { ok: true, age, needConfirm: false }
  if (age >= 11 && age <= 15) return { ok: true, age, needConfirm: true }
  return { ok: false }
}

/** Badge both-or-neither (05 §2 CHECK): name and image set or empty together. */
export function badgePairValid(name: string, hasImage: boolean): boolean {
  return (name.trim() === '') === !hasImage
}

/** Adapt a check into an antd Form rule that rejects with the translated key. */
export function toRule(check: Check, t: (key: string) => string): RuleObject {
  return toValueRule((value) => check(typeof value === 'string' ? value : ''), t)
}

/** Same ceremony for value-shape checks (arrays/undefined pass through as-is). */
export function toValueRule(check: ValueCheck, t: (key: string) => string): RuleObject {
  return {
    validator: (_rule, value) => {
      const key = check(value)
      return key ? Promise.reject(new Error(t(key))) : Promise.resolve()
    },
  }
}
