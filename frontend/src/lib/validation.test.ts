/**
 * The ONE shared upload gate (spec 09-PROFILE-PHOTOS, Pembaruan 2026-11-10):
 * every upload surface funnels through `isAcceptedImage` — JPG/JPEG/PNG only
 * (WebP retired from NEW uploads), non-empty, ≤50 MB by byte count
 * (52,428,800). Mirrors the server's decode-config whitelist (decrypt:
 * the server cross-checks real image bytes; this gate is the client half).
 */
import { describe, expect, it } from 'vitest'
import { IMAGE_ACCEPT, MAX_UPLOAD_BYTES, isAcceptedImage } from './validation'

/** A File whose size the prototype reports as `size` (jsdom payloads lie). */
function fileOfSize(name: string, type: string, size: number): File {
  const file = new File(['x'], name, { type })
  Object.defineProperty(file, 'size', { value: size })
  return file
}

describe('the shared upload gate (isAcceptedImage)', () => {
  it('accepts valid JPG/JPEG/PNG at or below 50 MB', () => {
    expect(isAcceptedImage(new File(['x'], 'badge.png', { type: 'image/png' }))).toBe(true)
    expect(isAcceptedImage(new File(['x'], 'photo.jpg', { type: 'image/jpeg' }))).toBe(true)
    expect(isAcceptedImage(new File(['x'], 'photo.jpeg', { type: 'image/jpeg' }))).toBe(true)
    // The nonstandard image/jpg MIME browsers report for some .jpg files.
    expect(isAcceptedImage(new File(['x'], 'photo.jpg', { type: 'image/jpg' }))).toBe(true)
  })

  it('accepts by extension when the MIME type is unhelpful', () => {
    expect(
      isAcceptedImage(new File(['x'], 'foto.png', { type: 'application/octet-stream' })),
    ).toBe(true)
    expect(isAcceptedImage(new File(['x'], 'PHOTO.PNG'))).toBe(true)
    expect(isAcceptedImage(new File(['x'], 'raw.jpg', { type: '' }))).toBe(true)
  })

  it('rejects WebP — type and extension retirement (2026-11-10)', () => {
    expect(isAcceptedImage(new File(['x'], 'frame.webp', { type: 'image/webp' }))).toBe(false)
    expect(
      isAcceptedImage(new File(['x'], 'lossy.webp', { type: 'application/octet-stream' })),
    ).toBe(false)
  })

  it('rejects GIF, ICO, PDF and unknown types', () => {
    expect(isAcceptedImage(new File(['x'], 'anim.gif', { type: 'image/gif' }))).toBe(false)
    expect(isAcceptedImage(new File(['x'], 'icon.ico', { type: 'image/vnd.microsoft.icon' }))).toBe(false)
    expect(isAcceptedImage(new File(['x'], 'doc.pdf', { type: 'application/pdf' }))).toBe(false)
    expect(isAcceptedImage(new File(['x'], 'mystery.bin'))).toBe(false)
  })

  it('enforces the 50 MB byte limit (52,428,800)', () => {
    expect(MAX_UPLOAD_BYTES).toBe(52428800)
    expect(isAcceptedImage(fileOfSize('limit.png', 'image/png', MAX_UPLOAD_BYTES))).toBe(true)
    expect(isAcceptedImage(fileOfSize('big.png', 'image/png', MAX_UPLOAD_BYTES + 1))).toBe(false)
    expect(isAcceptedImage(fileOfSize('huge.jpg', 'image/jpeg', 51 * 1024 * 1024))).toBe(false)
  })

  it('rejects empty files', () => {
    expect(isAcceptedImage(new File([''], 'blank.png', { type: 'image/png' }))).toBe(false)
  })
})

describe('the shared upload accept list (IMAGE_ACCEPT)', () => {
  it('lists the JPG/JPEG/PNG mimes and extensions — no WebP', () => {
    for (const entry of ['image/jpeg', 'image/jpg', 'image/png', '.jpg', '.jpeg', '.png']) {
      expect(IMAGE_ACCEPT).toContain(entry)
    }
    expect(IMAGE_ACCEPT).not.toContain('webp')
  })
})
