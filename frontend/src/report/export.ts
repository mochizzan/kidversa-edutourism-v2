/**
 * Client-only report export (spec F-12 / F-12.1, FL-13): PNG via
 * `toPng(node, {cacheBust:true})`, PDF via `jsPDF({orientation:'p', unit:'mm',
 * format:'a4'})` + `addImage` + `save`. NEVER `doc.html()`. The server is not
 * involved. Filename sanitisation lives ONLY here (single source): ASCII
 * transliteration where possible → `[^A-Za-z0-9-_]+`→`_` → trim `_` → 40
 * chars → empty falls back to `anak`/`topik`.
 */
import { toPng } from 'html-to-image'
import { jsPDF } from 'jspdf'

function filePart(value: string, fallback: string): string {
  const ascii = value.normalize('NFKD').replace(/[\u0300-\u036f]/g, '')
  const part = ascii
    .replace(/[^A-Za-z0-9-_]+/g, '_')
    .replace(/^_+|_+$/g, '')
    .slice(0, 40)
    .replace(/^_+|_+$/g, '')
  return part === '' ? fallback : part
}

export async function exportReport(
  kind: 'png' | 'pdf',
  opts: { childName: string; topicName: string; sessionDate: string },
): Promise<void> {
  const node = document.getElementById('report-a4')
  if (!node) throw new Error('report node #report-a4 not found')
  const png = await toPng(node, { cacheBust: true })
  const date = opts.sessionDate.replace(/\D/g, '').slice(0, 8)
  const name = `Rapor-${filePart(opts.childName, 'anak')}-${filePart(opts.topicName, 'topik')}-${date}.${kind}`

  if (kind === 'pdf') {
    // The captured node is exactly 210×297mm — full-bleed fit on the A4 page.
    const doc = new jsPDF({ orientation: 'p', unit: 'mm', format: 'a4' })
    doc.addImage(png, 'PNG', 0, 0, 210, 297)
    doc.save(name)
    return
  }

  const anchor = document.createElement('a')
  anchor.href = png
  anchor.download = name
  document.body.appendChild(anchor)
  anchor.click()
  anchor.remove()
}
