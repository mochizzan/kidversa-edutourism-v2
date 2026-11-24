/**
 * i18n bootstrap (spec F-14 / 01§5): ID default, EN switch persisted in
 * `kv-lang`, both language resources load for every namespace.
 */
import { describe, expect, it, vi } from 'vitest'
import i18n, { NAMESPACES } from './i18n'

describe('i18n language switching', () => {
  it('starts on ID with all seven namespaces in both languages', async () => {
    await i18n.changeLanguage('id')
    expect(i18n.language).toBe('id')
    for (const ns of NAMESPACES) {
      expect(i18n.hasResourceBundle('id', ns)).toBe(true)
      expect(i18n.hasResourceBundle('en', ns)).toBe(true)
    }
    expect(i18n.t('auth:login.title')).toBe('Masuk')
  })

  it('switches to EN and persists kv-lang', async () => {
    await i18n.changeLanguage('en')
    expect(i18n.t('auth:login.title')).toBe('Sign in')
    expect(localStorage.getItem('kv-lang')).toBe('en')

    await i18n.changeLanguage('id')
    expect(i18n.t('auth:login.title')).toBe('Masuk')
    expect(localStorage.getItem('kv-lang')).toBe('id')
  })

  it('boots in the persisted language (kv-lang read at module init)', async () => {
    // Module-init branch: `lng` is computed ONCE at import from kv-lang, so a
    // static import cannot re-observe it — resetModules + re-import is the
    // only way to exercise the stored-'en' and fallback-'id' boot paths.
    localStorage.setItem('kv-lang', 'en')
    vi.resetModules()
    const fresh = await import('./i18n')
    await fresh.default.changeLanguage(fresh.default.language)
    expect(fresh.default.language).toBe('en')
    expect(fresh.default.t('auth:login.title')).toBe('Sign in')

    // Anything other than an exact 'en' falls back to the ID default.
    localStorage.setItem('kv-lang', 'fr')
    vi.resetModules()
    const fallback = await import('./i18n')
    await fallback.default.changeLanguage(fallback.default.language)
    expect(fallback.default.language).toBe('id')
    expect(fallback.default.t('auth:login.title')).toBe('Masuk')
  })
})
