/**
 * Shared shell for the four public auth pages: compact form on the left, a
 * route-aligned promo carousel on the right. Beneath 920px the card folds into
 * one column and the promo becomes a short strip under the form.
 */
import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from 'react-router'
import AuthPromo, { type PromoSlide } from './AuthPromo'
import './auth.css'

export interface AuthLayoutProps {
  title: string
  subtitle?: ReactNode
  children: ReactNode
  /** Secondary route link shown above the language switch. */
  footer?: ReactNode
  /** Highlights the matching auth tab; omit for forgot / reset sub-flows. */
  activeTab?: 'login' | 'register'
  /** Promo slide to open on, aligned with the current route. */
  slide?: PromoSlide
}

export default function AuthLayout({
  title,
  subtitle,
  children,
  footer,
  activeTab,
  slide = 'login',
}: AuthLayoutProps) {
  const { t, i18n } = useTranslation('auth')
  const language = i18n.language.startsWith('en') ? 'en' : 'id'

  return (
    <div className="kv-auth">
      <div className="kv-auth__ground" aria-hidden="true">
        <span className="kv-auth__block kv-auth__block--a" />
        <span className="kv-auth__block kv-auth__block--b" />
        <span className="kv-auth__block kv-auth__block--c" />
      </div>

      <main className="kv-auth__stage">
        <div className="kv-auth__card">
          <section className="kv-auth__panel">
            <header className="kv-auth__head">
              <Link to="/" className="kv-auth__brand" aria-label="Kidversa">
                <span className="kv-auth__logo">
                  <img src="/logo.png" alt="" />
                </span>
                <span className="kv-auth__wordmark">Kidversa</span>
              </Link>
              <h1 className="kv-auth__title">{title}</h1>
              {subtitle ? <p className="kv-auth__subtitle">{subtitle}</p> : null}
            </header>

            {activeTab ? (
              <nav className="kv-auth__tabs" aria-label={t('shell.tabs')}>
                <Link
                  to="/login"
                  className={`kv-auth__tab${activeTab === 'login' ? ' is-active' : ''}`}
                  aria-current={activeTab === 'login' ? 'page' : undefined}
                >
                  {t('login.submit')}
                </Link>
                <Link
                  to="/register"
                  className={`kv-auth__tab${activeTab === 'register' ? ' is-active' : ''}`}
                  aria-current={activeTab === 'register' ? 'page' : undefined}
                >
                  {t('login.registerLink')}
                </Link>
              </nav>
            ) : null}

            <div className="kv-auth__body">{children}</div>

            {footer ? <div className="kv-auth__footer">{footer}</div> : null}

            <div className="kv-auth__lang">
              <span className="kv-auth__lang-label">{t('shell.language')}</span>
              <div className="kv-auth__lang-switch" role="group" aria-label={t('shell.language')}>
                <button
                  type="button"
                  className="kv-auth__lang-btn"
                  aria-pressed={language === 'id'}
                  onClick={() => void i18n.changeLanguage('id')}
                >
                  Indonesia
                </button>
                <button
                  type="button"
                  className="kv-auth__lang-btn"
                  aria-pressed={language === 'en'}
                  onClick={() => void i18n.changeLanguage('en')}
                >
                  English
                </button>
              </div>
            </div>
          </section>

          <aside className="kv-auth__promo">
            <AuthPromo initial={slide} />
          </aside>
        </div>
      </main>
    </div>
  )
}
