/**
 * Right-hand promo panel for the public auth card.
 *
 * The panel cycles three route-aligned slides (login / register / forgot),
 * each backed by a Kidversa-palette illustration. Automation stops on hover,
 * focus, or `prefers-reduced-motion`; the dot controls always work.
 */
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'

const SLIDES = [
  { key: 'login', src: '/auth/login.svg' },
  { key: 'register', src: '/auth/register.svg' },
  { key: 'forgot', src: '/auth/forgot.svg' },
] as const

const ROTATE_MS = 7000

export type PromoSlide = (typeof SLIDES)[number]['key']

export interface AuthPromoProps {
  /** Slide shown on mount — aligned with the current route. */
  initial: PromoSlide
}

/** Four-point sparkle used to flank the tagline. */
function Sparkle() {
  return (
    <svg className="kv-auth__spark" viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path
        d="M12 1.6c.5 4.2 1.8 6.6 4.2 8.1 2.4 1.5 4.6 1.8 6.2 1.9-1.6.1-3.8.4-6.2 1.9-2.4 1.5-3.7 3.9-4.2 8.1-.5-4.2-1.8-6.6-4.2-8.1-2.4-1.5-4.6-1.8-6.2-1.9 1.6-.1 3.8-.4 6.2-1.9C10.2 8.2 11.5 5.8 12 1.6Z"
        fill="currentColor"
      />
    </svg>
  )
}

export default function AuthPromo({ initial }: AuthPromoProps) {
  const { t } = useTranslation('auth')
  const [index, setIndex] = useState(() =>
    Math.max(
      0,
      SLIDES.findIndex((slide) => slide.key === initial),
    ),
  )
  const [paused, setPaused] = useState(false)

  useEffect(() => {
    if (paused) return
    if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return
    const timer = window.setInterval(() => {
      setIndex((current) => (current + 1) % SLIDES.length)
    }, ROTATE_MS)
    return () => window.clearInterval(timer)
  }, [paused])

  const current = SLIDES[index]

  return (
    <div
      className="kv-auth__promo-frame"
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
      onFocus={() => setPaused(true)}
      onBlur={() => setPaused(false)}
    >
      <svg
        className="kv-auth__waves"
        viewBox="0 0 520 300"
        preserveAspectRatio="none"
        aria-hidden="true"
      >
        <path d="M-40 40 C 80 150, 200 150, 320 40 S 520 150, 640 40" />
        <path d="M-40 92 C 80 202, 200 202, 320 92 S 520 202, 640 92" />
        <path d="M-40 -12 C 80 98, 200 98, 320 -12 S 520 98, 640 -12" />
      </svg>

      <div key={current.key} className="kv-auth__slide">
        <div className="kv-auth__promo-copy">
          <h2>{t(`promo.slides.${current.key}.title`)}</h2>
          <p>{t(`promo.slides.${current.key}.body`)}</p>
          <p className="kv-auth__tagline">
            <Sparkle />
            <span>{t(`promo.slides.${current.key}.tag`)}</span>
            <Sparkle />
          </p>
        </div>

        <div className="kv-auth__dots" role="group" aria-label={t('promo.label')}>
          {SLIDES.map((slide, dotIndex) => (
            <button
              key={slide.key}
              type="button"
              className={`kv-auth__dot${dotIndex === index ? ' is-active' : ''}`}
              aria-pressed={dotIndex === index}
              aria-label={t('promo.show', { n: dotIndex + 1 })}
              onClick={() => setIndex(dotIndex)}
            />
          ))}
        </div>

        <div className="kv-auth__art">
          <img src={current.src} alt="" width={800} height={640} decoding="async" />
        </div>
      </div>
    </div>
  )
}
