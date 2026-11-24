/**
 * Settings MODAL (never a page/route — decision 2026-10-11): hosts the
 * language switcher moved out of the header. The switcher is a Select
 * showing the language endonym + its country flag from `country-flag-icons`
 * (no emoji), calling i18n.changeLanguage (persisted `kv-lang`). Language
 * names are endonyms — identical in every UI locale — so they live here as
 * constants rather than locale keys.
 */
import { Modal, Select } from 'antd'
import { ID, US } from 'country-flag-icons/react/3x2'
import { useTranslation } from 'react-i18next'

const LANG_OPTIONS = [
  { value: 'id', label: 'Bahasa Indonesia', Flag: ID },
  { value: 'en', label: 'English', Flag: US },
] as const

export interface SettingsModalProps {
  open: boolean
  onClose: () => void
}

export default function SettingsModal({ open, onClose }: SettingsModalProps) {
  const { t, i18n } = useTranslation()
  const current = i18n.language.startsWith('en') ? 'en' : 'id'

  return (
    <Modal
      className="kv-settings"
      open={open}
      title={t('menu:settings')}
      width={420}
      footer={null}
      onCancel={onClose}
    >
      <div className="kv-settings__row">
        <span className="kv-settings__label">{t('menu:language')}</span>
        <Select
          className="kv-settings__lang"
          aria-label={t('menu:language')}
          value={current}
          options={LANG_OPTIONS.map(({ value, label, Flag }) => ({
            value,
            label: (
              <span className="kv-flag-option">
                <Flag width={18} height={12} />
                {label}
              </span>
            ),
          }))}
          onChange={(code) => void i18n.changeLanguage(code)}
        />
      </div>
    </Modal>
  )
}
