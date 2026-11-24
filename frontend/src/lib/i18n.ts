/**
 * i18next bootstrap (spec F-14, 01§5): local JSON only, lng 'id', fallback 'id',
 * persisted in localStorage `kv-lang`, namespaces
 * common/menu/auth/master/session/assessment/report — both languages.
 *
 * JSON modules are loaded through import.meta.glob (no resolveJsonModule in
 * tsconfig; Vite bundles the files eagerly). Layout: src/locales/<lang>/<ns>.json.
 */
import i18n, { type Resource, type ResourceKey } from 'i18next'
import { initReactI18next } from 'react-i18next'

export const NAMESPACES = [
  'common',
  'menu',
  'auth',
  'master',
  'session',
  'assessment',
  'report',
] as const

const localeFiles = import.meta.glob('../locales/*/*.json', {
  eager: true,
  import: 'default',
}) as Record<string, ResourceKey>

const resources: Resource = {}
for (const [path, json] of Object.entries(localeFiles)) {
  const match = /\/locales\/(id|en)\/(\w+)\.json$/.exec(path)
  if (!match) continue
  const [, lang, ns] = match
  if (!(NAMESPACES as readonly string[]).includes(ns)) continue
  resources[lang] ??= {}
  resources[lang][ns] = json
}

const stored = localStorage.getItem('kv-lang')
const lng = stored === 'en' ? 'en' : 'id'

void i18n.use(initReactI18next).init({
  resources,
  lng,
  fallbackLng: 'id',
  defaultNS: 'common',
  ns: [...NAMESPACES],
  interpolation: { escapeValue: false },
})

i18n.on('languageChanged', (lang) => {
  localStorage.setItem('kv-lang', lang)
})

export default i18n
