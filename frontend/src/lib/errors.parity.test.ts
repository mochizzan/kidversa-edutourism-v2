/**
 * Error-code catalogue parity (finding 005b): `common.json` in BOTH
 * locales must expose `errors.<CODE>` for every code of the pinned
 * 06 §1 taxonomy — and nothing outside it. Codes are consumed by
 * `errorCode()` / `errors.${code}` lookups with no type support, so a
 * renamed or dropped translation key breaks the UI while every test
 * stays green; this file closes that hole.
 */
import { describe, expect, it } from 'vitest'
import { PINNED_ERROR_CODES } from '../test/catalogues'
import type { ResourceKey } from 'i18next'

const commonModules = import.meta.glob('../locales/*/common.json', {
  eager: true,
  import: 'default',
}) as Record<string, ResourceKey>

function errorKeys(lang: string): string[] {
  const mod = commonModules[`../locales/${lang}/common.json`]
  if (typeof mod !== 'object' || mod === null) {
    throw new Error(`locale module missing: ../locales/${lang}/common.json`)
  }
  const errors = (mod as Record<string, ResourceKey>).errors
  if (typeof errors !== 'object' || errors === null) {
    throw new Error(`common.json "${lang}" has no errors block`)
  }
  return Object.keys(errors)
}

describe('error-code catalogue parity (06 §1, 33 codes)', () => {
  it('pins exactly 33 codes', () => {
    expect(PINNED_ERROR_CODES).toHaveLength(34)
    expect(new Set(PINNED_ERROR_CODES).size, 'pinned codes must be unique').toBe(34)
  })

  it.each(['id', 'en'])(
    'common.json (%s) errors block equals the pinned taxonomy in both directions',
    (lang) => {
      const keys = new Set(errorKeys(lang))
      const missing = PINNED_ERROR_CODES.filter((code) => !keys.has(code))
      const unpinned = [...keys].filter(
        (key) => !PINNED_ERROR_CODES.includes(key),
      )
      expect(missing, `${lang}: errors.<CODE> missing for pinned codes`).toEqual([])
      expect(unpinned, `${lang}: errors block carries keys outside 06 §1`).toEqual([])
    },
  )
})
