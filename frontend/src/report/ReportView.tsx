/**
 * A4 fixed report view (spec F-12 / F-12.1, FL-13).
 *
 * Contract (frozen, contracts file): DEFAULT export `ReportView({ data })`
 * rendering the root `<div id="report-a4" class="report-a4">` that
 * `export.ts` grabs. PLAIN CSS only — no antd inside the A4 node (spec, not
 * laziness); labels are hard-coded Indonesian because report content is
 * ID-only regardless of UI language.
 *
 * Single-page scale: the content node is measured against the 277mm sheet and
 * uniformly scaled down when it exceeds one page; an overflow notice pill then
 * appears inside the page (it is part of the export, per acceptance). No
 * auto-pagination.
 */
import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import './report.css'
import type { ReportData } from './types'

/** 1↔BB, 2↔MB, 3↔BSH, 4↔BSB (05-DATA). */
const STAR_LABELS = ['', 'BB', 'MB', 'BSH', 'BSB']

const EM_DASH = '—'

function text(value: string | null | undefined): string {
  return value || EM_DASH
}

interface ImageProps {
  src: string
  alt: string
}

/**
 * Honest image gate: a placeholder covers the box until `onLoad` (never a
 * half-rendered file), a 10s timeout or error switches to a retry affordance
 * that re-requests the same `/uploads/…` URL with a cache-buster.
 */
function ReportImage({ src, alt }: ImageProps) {
  const [nonce, setNonce] = useState(0)
  // State is keyed by the requested URL, so a src/retry change re-opens the
  // gate without any state reset inside an effect.
  const [readyUrl, setReadyUrl] = useState<string | null>(null)
  const [failedUrl, setFailedUrl] = useState<string | null>(null)

  const url = nonce === 0 ? src : `${src}${src.includes('?') ? '&' : '?'}r=${nonce}`
  const ready = readyUrl === url
  const failed = !ready && failedUrl === url

  useEffect(() => {
    const timer = window.setTimeout(() => setFailedUrl(url), 10000)
    return () => window.clearTimeout(timer)
  }, [url])

  return (
    <span className={`ra-img${ready ? ' is-ready' : ''}`}>
      <img src={url} alt={alt} onLoad={() => setReadyUrl(url)} onError={() => setFailedUrl(url)} />
      {!ready && (
        <span className="ra-img__ph">
          {!failed && 'Memuat…'}
          {failed && (
            <button type="button" onClick={() => setNonce((n) => n + 1)}>
              Coba lagi
            </button>
          )}
        </span>
      )}
    </span>
  )
}

export default function ReportView({ data }: { data: ReportData }) {
  const sheetRef = useRef<HTMLDivElement>(null)
  const contentRef = useRef<HTMLDivElement>(null)
  const [fit, setFit] = useState(1)

  // Scale-to-fit: uniform shrink when the natural content exceeds the 277mm
  // sheet; transform never changes layout size, so measurement cannot loop.
  useLayoutEffect(() => {
    const content = contentRef.current
    const sheet = sheetRef.current
    if (!content || !sheet) return
    const update = () => {
      const available = sheet.clientHeight
      const natural = content.offsetHeight
      setFit(natural <= available + 1 ? 1 : available / natural)
    }
    update()
    const observer = new ResizeObserver(update)
    observer.observe(content)
    return () => observer.disconnect()
  }, [])

  const overflow = fit < 1

  return (
    <div id="report-a4" className="report-a4">
      {overflow && (
        <div className="report-a4__overflow">Konten melebihi satu halaman A4 — tampilan diperkecil.</div>
      )}
      <div className="report-a4__sheet" ref={sheetRef}>
        <div
          className="report-a4__content"
          ref={contentRef}
          style={overflow ? { transform: `scale(${fit})` } : undefined}
        >
          <header className="ra-head">
            <div className="ra-logo">Kidversa EduTourism</div>
            <div className="ra-head__main">
              <div className="ra-title">Rapor Kegiatan</div>
              <div className="ra-sub">{data.session.name}</div>
              <div className="ra-meta">
                {data.session.date} · {text(data.session.location)} · Program {data.program.name}
              </div>
            </div>
            <div className="ra-head__topic">
              Topik
              <b>{data.topic.name}</b>
            </div>
          </header>

          {!data.complete && (
            <div className="ra-banner">
              Rapor belum lengkap — {data.missing.length} kegiatan belum dinilai
              {data.missing.length > 0 ? `: ${data.missing.map((m) => m.name).join(', ')}` : '.'}
            </div>
          )}

          <section className="ra-section">
            <h2 className="ra-h">Profil Anak</h2>
            <div className="ra-profile">
              <div>
                <span className="ra-field__label">Nama</span>
                {data.child.name}
              </div>
              <div>
                <span className="ra-field__label">Usia</span>
                {data.child.age} tahun
              </div>
              <div>
                <span className="ra-field__label">Sekolah</span>
                {text(data.child.school)}
              </div>
              <div>
                <span className="ra-field__label">Ibu</span>
                {text(data.child.mother_name)}
              </div>
              <div>
                <span className="ra-field__label">Ayah</span>
                {text(data.child.father_name)}
              </div>
              <div>
                <span className="ra-field__label">Wali</span>
                {text(data.child.guardian_name)}
              </div>
              {data.group && (
                <div>
                  <span className="ra-field__label">Kelompok</span>
                  {data.group.name}
                </div>
              )}
            </div>
          </section>

          <section className="ra-section">
            <h2 className="ra-h">Ringkasan Narasi AI</h2>
            <p className="ra-narrative">{text(data.narrative?.text)}</p>
          </section>

          <section className="ra-section">
            <h2 className="ra-h">Misi</h2>
            {data.missions.length > 0 ? (
              <ol className="ra-missions">
                {data.missions.map((mission) => (
                  <li key={mission.mission_id}>
                    <b>{mission.name}</b>
                  </li>
                ))}
              </ol>
            ) : (
              <p className="ra-narrative">{EM_DASH}</p>
            )}
          </section>

          <section className="ra-section">
            <h2 className="ra-h">Hasil Kegiatan</h2>
            <table className="ra-table">
              <thead>
                <tr>
                  <th>Kegiatan</th>
                  <th className="ra-table__star">Bintang</th>
                  <th>Catatan</th>
                </tr>
              </thead>
              <tbody>
                {data.outcomes.map((outcome) => (
                  <tr key={outcome.said}>
                    <td>
                      {outcome.activity_name}
                      <span className="ra-indicator">{text(outcome.indicator)}</span>
                    </td>
                    <td className="ra-table__star">
                      {outcome.stars >= 1 && outcome.stars <= 4 ? (
                        <span className="ra-star">{STAR_LABELS[outcome.stars]}</span>
                      ) : (
                        EM_DASH
                      )}
                    </td>
                    <td>
                      <span className="ra-note">{text(outcome.note)}</span>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </section>

          {(data.badges.topic || data.badges.program) && (
            <section className="ra-section">
              <h2 className="ra-h">Badge</h2>
              <div className="ra-badges">
                {data.badges.topic && (
                  <div className="ra-badge">
                    {data.badges.topic.image_url && (
                      <ReportImage src={data.badges.topic.image_url} alt={`Badge ${data.badges.topic.name}`} />
                    )}
                    {data.badges.topic.name}
                  </div>
                )}
                {data.badges.program && (
                  <div className="ra-badge">
                    {data.badges.program.image_url && (
                      <ReportImage src={data.badges.program.image_url} alt={`Badge ${data.badges.program.name}`} />
                    )}
                    {data.badges.program.name}
                  </div>
                )}
              </div>
            </section>
          )}

          {data.candidate_photo && (
            <section className="ra-section">
              <h2 className="ra-h">Foto Utama</h2>
              <div className="ra-photos">
                <div className="ra-photo">
                  <ReportImage src={data.candidate_photo.url} alt={`Foto utama ${data.child.name}`} />
                  <div className="ra-photo__caption">{data.child.name}</div>
                </div>
              </div>
            </section>
          )}

          <footer className="ra-sign">
            <div>
              <div className="ra-sign__label">Ditetapkan oleh</div>
              <div className="ra-sign__name">{data.attestation.name}</div>
              <div className="ra-sign__role">{data.attestation.role}</div>
            </div>
          </footer>
        </div>
      </div>
    </div>
  )
}
