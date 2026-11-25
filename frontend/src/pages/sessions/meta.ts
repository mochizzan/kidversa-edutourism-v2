/**
 * ONE session-meta rule set (004f): name/location/date required, end > start
 * when both set — consumed by BOTH the create wizard's `validateStep` and the
 * draft editor's `SessionMetaModal.save` (predicates are shared, form flow is
 * not). Returns session-namespace i18n keys; callers translate.
 */
export interface SessionMetaInput {
  name: string
  date: string
  location: string
  startTime: string
  endTime: string
}

export type SessionMetaErrorKey =
  | 'form.errors.name'
  | 'form.errors.date'
  | 'form.errors.location'
  | 'form.errors.endTime'

export function validateSessionMeta(
  input: SessionMetaInput,
): Partial<Record<'name' | 'date' | 'location' | 'endTime', SessionMetaErrorKey>> {
  const errors: Partial<Record<'name' | 'date' | 'location' | 'endTime', SessionMetaErrorKey>> = {}
  if (!input.name.trim()) errors.name = 'form.errors.name'
  if (!input.date) errors.date = 'form.errors.date'
  if (!input.location.trim()) errors.location = 'form.errors.location'
  if (input.startTime && input.endTime && input.endTime <= input.startTime) {
    errors.endTime = 'form.errors.endTime'
  }
  return errors
}

