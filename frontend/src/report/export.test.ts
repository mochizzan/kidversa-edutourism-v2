/**
 * Client-only report export (spec F-12 / FL-13): filename sanitisation is
 * the single source of truth here (ASCII transliteration → illegal chars
 * to `_` → trim → 40 chars → anak/topik fallback → YYYYMMDD), PNG downloads
 * through an anchor, PDF through jsPDF A4 full-bleed.
 */
import { beforeEach, describe, expect, it, vi, type Mock } from 'vitest'

const mocks = vi.hoisted(() => ({
  toPng: vi.fn(async () => 'data:image/png;base64,AAA'),
  pdfDocs: [] as Array<{ opts: unknown; addImage: Mock; save: Mock }>,
}))

vi.mock('html-to-image', () => ({ toPng: mocks.toPng }))
vi.mock('jspdf', () => ({
  jsPDF: class {
    opts: unknown
    addImage: Mock
    save: Mock
    constructor(opts: unknown) {
      this.opts = opts
      this.addImage = vi.fn()
      this.save = vi.fn()
      mocks.pdfDocs.push(this)
    }
  },
}))

import { exportReport } from './export'

const NODE_HTML = '<div id="report-a4" class="report-a4">rapor</div>'

function captureAnchorClick() {
  const seen: Array<{ download: string; href: string }> = []
  vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (
    this: HTMLAnchorElement,
  ) {
    seen.push({ download: this.download, href: this.href })
  })
  return seen
}

describe('report export filename sanitisation', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
    mocks.toPng.mockClear()
    mocks.pdfDocs.length = 0
  })

  it('transliterates to ASCII, replaces illegal runs and empties with the fallbacks', async () => {
    document.body.innerHTML = NODE_HTML
    const anchors = captureAnchorClick()

    await exportReport('png', {
      childName: 'Éloïse "Rapunzel" Budi!!!',
      topicName: '   ',
      sessionDate: '2026-10-09',
    })

    expect(anchors).toHaveLength(1)
    expect(anchors[0].download).toBe('Rapor-Eloise_Rapunzel_Budi-topik-20261009.png')
    expect(anchors[0].href).toBe('data:image/png;base64,AAA')
    expect(mocks.toPng).toHaveBeenCalledWith(document.getElementById('report-a4'), {
      cacheBust: true,
    })
    expect(document.body.querySelector('a')).toBeNull() // anchor removed after click
  })

  it('caps every part at 40 characters', async () => {
    document.body.innerHTML = NODE_HTML
    const anchors = captureAnchorClick()

    await exportReport('png', {
      childName: 'B'.repeat(50),
      topicName: 'C'.repeat(45),
      sessionDate: '2026-10-09',
    })

    expect(anchors[0].download).toBe(`Rapor-${'B'.repeat(40)}-${'C'.repeat(40)}-20261009.png`)
  })

  it('falls back to anak/topik for name parts that sanitise to nothing', async () => {
    document.body.innerHTML = NODE_HTML
    const anchors = captureAnchorClick()

    await exportReport('png', {
      childName: '🙂🙂',
      topicName: '###',
      sessionDate: '13/10/2026',
    })

    expect(anchors[0].download).toBe('Rapor-anak-topik-13102026.png')
  })

  it('builds the PDF via jsPDF A4 full-bleed and saves the sanitised name', async () => {
    document.body.innerHTML = NODE_HTML

    await exportReport('pdf', {
      childName: 'Aisyah Putri',
      topicName: 'Batik Nusantara',
      sessionDate: '09-10-2026',
    })

    expect(mocks.pdfDocs).toHaveLength(1)
    const doc = mocks.pdfDocs[0]
    expect(doc.opts).toEqual({ orientation: 'p', unit: 'mm', format: 'a4' })
    expect(doc.addImage).toHaveBeenCalledWith('data:image/png;base64,AAA', 'PNG', 0, 0, 210, 297)
    expect(doc.save).toHaveBeenCalledWith('Rapor-Aisyah_Putri-Batik_Nusantara-09102026.pdf')
    expect(mocks.toPng).toHaveBeenCalledTimes(1)
  })

  it('refuses to export without the #report-a4 node', async () => {
    await expect(
      exportReport('png', { childName: 'Aisyah', topicName: 'Batik', sessionDate: '2026-10-09' }),
    ).rejects.toThrow('report node #report-a4 not found')
    expect(mocks.toPng).not.toHaveBeenCalled()
  })
})
