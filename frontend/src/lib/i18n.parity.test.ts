/**
 * Locale key parity (finding 005a): the `id` and `en` trees must expose
 * the IDENTICAL nested key set across all 7 namespaces — a translation
 * added/removed on one side fails here before it reaches a runtime
 * `t('ns:key')` miss. Values are deliberately NEVER compared: the two
 * languages translate differently.
 */
import { describe, expect, it } from 'vitest'
import type { ResourceKey } from 'i18next'

const localeModules = import.meta.glob('../locales/*/*.json', {
  eager: true,
  import: 'default',
}) as Record<string, ResourceKey>

const LANGS = ['id', 'en'] as const
const NAMESPACES = [
  'auth',
  'common',
  'master',
  'menu',
  'report',
  'session',
  'assessment',
] as const

function localeTree(lang: string, ns: string): ResourceKey {
  const mod: ResourceKey | undefined = localeModules[`../locales/${lang}/${ns}.json`]
  if (mod === undefined) {
    throw new Error(`locale module missing: ../locales/${lang}/${ns}.json`)
  }
  return mod
}

/** Dotted leaf paths only; arrays are opaque leaves (never value-compared). */
function flattenKeys(node: ResourceKey, prefix: string, out: Set<string>): void {
  if (typeof node !== 'object' || node === null || Array.isArray(node)) {
    if (prefix !== '') out.add(prefix)
    return
  }
  for (const [key, child] of Object.entries(node)) {
    flattenKeys(child, prefix === '' ? key : `${prefix}.${key}`, out)
  }
}

describe('i18n locale parity', () => {
  it('ships every namespace module for both languages', () => {
    for (const lang of LANGS) {
      for (const ns of NAMESPACES) {
        expect(
          localeModules,
          `locale module missing: ../locales/${lang}/${ns}.json (namespace "${ns}", lang "${lang}")`,
        ).toHaveProperty([`../locales/${lang}/${ns}.json`])
      }
    }
  })

  describe.each(NAMESPACES)('namespace "%s"', (ns) => {
    it('id and en expose the identical nested key set', () => {
      const idKeys = new Set<string>()
      flattenKeys(localeTree('id', ns), '', idKeys)
      const enKeys = new Set<string>()
      flattenKeys(localeTree('en', ns), '', enKeys)
      expect(idKeys.size, `namespace "${ns}": the id tree must not be empty`).toBeGreaterThan(0)
      expect(enKeys.size, `namespace "${ns}": the en tree must not be empty`).toBeGreaterThan(0)

      const missingInEn = [...idKeys].filter((key) => !enKeys.has(key)).sort()
      const missingInId = [...enKeys].filter((key) => !idKeys.has(key)).sort()
      expect(
        missingInEn,
        `namespace "${ns}": keys present in id but missing in en`,
      ).toEqual([])
      expect(
        missingInId,
        `namespace "${ns}": keys present in en but missing in id`,
      ).toEqual([])
    })
  })
})
