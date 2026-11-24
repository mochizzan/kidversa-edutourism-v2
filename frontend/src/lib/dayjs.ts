/**
 * Shared dayjs bootstrap for antd's date/time pickers: loads the Indonesian
 * locale files (weekday/month names in the calendar panel) and the
 * `customParseFormat` plugin needed for `dayjs(value, 'YYYY-MM-DD')`
 * parsing (rc-picker extends the same plugins internally, but be explicit
 * so the app works regardless of module import order).
 *
 * Re-exports the dayjs instance so every consumer automatically carries
 * this setup, and exposes `setDayjsLocale` to follow the app language
 * (id/en) next to ConfigProvider's widget locale.
 */
import dayjsBase from 'dayjs'
import customParseFormat from 'dayjs/plugin/customParseFormat'
import 'dayjs/locale/id'

dayjsBase.extend(customParseFormat)

/** Map the app language (id/en — `kv-lang`) onto dayjs's global locale. */
export function setDayjsLocale(lang: string): void {
  dayjsBase.locale(lang.startsWith('en') ? 'en' : 'id')
}

export default dayjsBase
