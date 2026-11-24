/**
 * Session-meta rule set (004f): the ONE predicate behind the create wizard's
 * validateStep and the draft editor's save — required name/date/location
 * (whitespace-only counts as empty) and end > start only when BOTH times are
 * set. Callers translate the returned keys.
 */
import { describe, expect, it } from 'vitest'
import { validateSessionMeta, type SessionMetaInput } from './meta'

const complete: SessionMetaInput = {
  name: 'Kelas Batik',
  date: '2026-10-09',
  location: 'Museum Tekstil',
  startTime: '09:00',
  endTime: '10:00',
}

describe('validateSessionMeta', () => {
  it('accepts a complete meta and a meta without times', () => {
    expect(validateSessionMeta(complete)).toEqual({})
    expect(validateSessionMeta({ ...complete, startTime: '', endTime: '' })).toEqual({})
  })

  it('flags each required field; whitespace-only name/location count as empty', () => {
    expect(
      validateSessionMeta({ name: '   ', date: '', location: '\t', startTime: '', endTime: '' }),
    ).toEqual({
      name: 'form.errors.name',
      date: 'form.errors.date',
      location: 'form.errors.location',
    })
  })

  it('flags end<=start only when both times are set', () => {
    expect(validateSessionMeta({ ...complete, endTime: '08:00' })).toEqual({
      endTime: 'form.errors.endTime',
    })
    // Equal bounds are still a violation (end must EXCEED start).
    expect(validateSessionMeta({ ...complete, endTime: complete.startTime })).toEqual({
      endTime: 'form.errors.endTime',
    })
    // Half a pair never compares — no time error.
    expect(validateSessionMeta({ ...complete, endTime: '' })).toEqual({})
    expect(validateSessionMeta({ ...complete, startTime: '' })).toEqual({})
  })

  it('reports multiple independent errors at once', () => {
    expect(
      validateSessionMeta({ name: '', date: '2026-10-09', location: '', startTime: '10:00', endTime: '09:00' }),
    ).toEqual({
      name: 'form.errors.name',
      location: 'form.errors.location',
      endTime: 'form.errors.endTime',
    })
  })
})
