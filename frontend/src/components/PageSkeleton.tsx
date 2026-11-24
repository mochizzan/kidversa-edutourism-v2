/**
 * Page-level loading placeholder. Full-screen spinners are replaced by blocks
 * shaped like the page that is arriving, so the frame never jumps and the wait
 * reads as progress rather than a stall.
 *
 * - `page` — header + two stacked cards (detail screens).
 * - `list` — stacked rows inside an existing card body.
 * - `grid` — photo tiles (galleries).
 * - `document` — a single wide sheet (report / consent).
 */
import { useTranslation } from 'react-i18next'
import './page-skeleton.css'

export interface PageSkeletonProps {
  variant?: 'page' | 'list' | 'grid' | 'document'
  rows?: number
}

export default function PageSkeleton({ variant = 'page', rows = 3 }: PageSkeletonProps) {
  const { t } = useTranslation('common')

  return (
    <div
      className={`kv-page-skeleton kv-page-skeleton--${variant}`}
      role="status"
      aria-live="polite"
      aria-busy="true"
    >
      <span className="kv-sr-only">{t('actions.loading')}</span>

      {variant === 'page' ? (
        <>
          <div className="kv-page-skeleton__head" aria-hidden="true">
            <span className="kv-skel" style={{ width: 180, height: 16 }} />
            <span className="kv-skel" style={{ width: 280, height: 9 }} />
          </div>
          {Array.from({ length: 2 }, (_, card) => (
            <div className="kv-page-skeleton__card" key={card} aria-hidden="true">
              <span className="kv-skel" style={{ width: 120, height: 10 }} />
              {Array.from({ length: rows }, (_, row) => (
                <span className="kv-skel" key={row} style={{ height: 10, width: `${88 - row * 9}%` }} />
              ))}
            </div>
          ))}
        </>
      ) : null}

      {variant === 'list' ? (
        <div className="kv-page-skeleton__rows" aria-hidden="true">
          {Array.from({ length: rows }, (_, index) => (
            <div className="kv-page-skeleton__row" key={index}>
              <span className="kv-skel" style={{ width: 160, height: 10 }} />
              <span className="kv-skel" style={{ flex: 1, height: 8 }} />
            </div>
          ))}
        </div>
      ) : null}

      {variant === 'grid' ? (
        <div className="kv-page-skeleton__grid" aria-hidden="true">
          {Array.from({ length: 6 }, (_, index) => (
            <span className="kv-skel kv-page-skeleton__tile" key={index} />
          ))}
        </div>
      ) : null}

      {variant === 'document' ? (
        <div className="kv-page-skeleton__sheet" aria-hidden="true">
          <span className="kv-skel" style={{ width: 220, height: 16 }} />
          <span className="kv-skel" style={{ width: 320, height: 9 }} />
          <span className="kv-skel" style={{ width: '100%', height: 1 }} />
          <span className="kv-skel" style={{ width: '92%', height: 10 }} />
          <span className="kv-skel" style={{ width: '84%', height: 10 }} />
          <span className="kv-skel" style={{ width: '88%', height: 10 }} />
          <span className="kv-skel" style={{ width: '76%', height: 10 }} />
        </div>
      ) : null}
    </div>
  )
}
