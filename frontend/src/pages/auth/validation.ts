/**
 * Client-side validation for the F2 page group (auth, profile, schools,
 * users forms). Every check mirrors ONE server rule set (backend
 * `validate.go`) — there is exactly one copy of each rule:
 *
 * - email: non-empty, RFC-5322-subset format, ≤320 chars
 * - password: non-empty, ≥8 chars, letters + digits, ≤72 bytes
 * - WhatsApp: DERIVED from `lib/validation` `normalizeWa` (004c — the
 *   digit/prefix/length rule lives there once); this check only classifies
 *   the failure into `validation.required|waInvalid|waDigits`
 *
 * A check returns an `auth:validation.*` i18n key (null = valid) so forms
 * translate at render time; `lib/validation` `toRule` adapts a check into an
 * antd Form rule.
 */
import { MAX_PERSON_NAME, normalizeWa, type Check } from '../../lib/validation'

const EMAIL_RE = /^[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}$/

export const checkRequired: Check = (value) =>
  value.trim() === '' ? 'validation.required' : null

export const checkName: Check = (value) => {
  const v = value.trim()
  if (v === '') return 'validation.required'
  if (v.length > MAX_PERSON_NAME) return 'validation.nameTooLong'
  return null
}

export const checkEmail: Check = (value) => {
  const v = value.trim()
  if (v === '') return 'validation.required'
  if (v.length > 320 || !EMAIL_RE.test(v)) return 'validation.invalidEmail'
  return null
}

export const checkPassword: Check = (value) => {
  if (value === '') return 'validation.required'
  if (value.length < 8 || !/[A-Za-z]/.test(value) || !/[0-9]/.test(value)) {
    return 'validation.passwordRule'
  }
  if (new TextEncoder().encode(value).length > 72) return 'validation.passwordTooLong'
  return null
}

export const checkCode: Check = (value) =>
  value.trim() === '' ? 'validation.required' : null

/** Classifies normalizeWa's null into required / bad-format / bad-prefix keys. */
export const checkWa: Check = (value) => {
  if (normalizeWa(value) !== null) return null
  const digits = value.replace(/[^0-9]/g, '')
  if (digits === '') return 'validation.required'
  return /^\d+$/.test(value.trim()) ? 'validation.waInvalid' : 'validation.waDigits'
}
