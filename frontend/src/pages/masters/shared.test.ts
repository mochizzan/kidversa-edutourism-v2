/**
 * Children form rules (spec F-07 / FL-06): parseAge 1–10 direct,
 * 11–15 confirm, else invalid; WA normalisation mirrors the server
 * (05 §4); badge both-or-neither; shared image pre-check jpg/jpeg/png ≤50MB
 * (spec 09, Pembaruan 2026-11-10).
 */
import { describe, expect, it } from 'vitest'
import { badgePairValid, normalizeWa, parseAge } from '../../lib/validation'
import { isAcceptedImage } from './shared'

describe('form children age rules', () => {
  it('accepts integer ages 1–10 without confirmation', () => {
    expect(parseAge('1')).toEqual({ ok: true, age: 1, needConfirm: false })
    expect(parseAge(' 10 ')).toEqual({ ok: true, age: 10, needConfirm: false })
  })

  it('accepts 11–15 only through the confirm flow', () => {
    expect(parseAge('11')).toEqual({ ok: true, age: 11, needConfirm: true })
    expect(parseAge('13')).toEqual({ ok: true, age: 13, needConfirm: true })
    expect(parseAge('15')).toEqual({ ok: true, age: 15, needConfirm: true })
  })

  it('rejects 0, 16 and non-integers', () => {
    expect(parseAge('0')).toEqual({ ok: false })
    expect(parseAge('16')).toEqual({ ok: false })
    expect(parseAge('-3')).toEqual({ ok: false })
    expect(parseAge('4.5')).toEqual({ ok: false })
    expect(parseAge('tiga')).toEqual({ ok: false })
  })
})

describe('form whatsapp normalisation', () => {
  it('maps 0/8/62 prefixes to +62 like the server', () => {
    expect(normalizeWa('081234567890')).toBe('+6281234567890')
    expect(normalizeWa('81234567890')).toBe('+6281234567890')
    expect(normalizeWa('6281234567890')).toBe('+6281234567890')
    expect(normalizeWa('+62 812-3456-789')).toBe('+628123456789')
  })

  it('rejects unusable numbers instead of guessing', () => {
    expect(normalizeWa('')).toBeNull()
    expect(normalizeWa('12345')).toBeNull()
    expect(normalizeWa('0812')).toBeNull() // local part too short
    expect(normalizeWa('12345678901234567')).toBeNull() // local part too long
  })
})

describe('form badge and image rules', () => {
  it('badge name and image must be set or empty together', () => {
    expect(badgePairValid('', false)).toBe(true)
    expect(badgePairValid('Badge Emas', true)).toBe(true)
    expect(badgePairValid('Badge Emas', false)).toBe(false)
    expect(badgePairValid('', true)).toBe(false)
  })

  it('image pre-check (shared gate, 2026-11-10) accepts jpg/jpeg/png ≤50MB and rejects the rest', () => {
    const png = new File(['x'], 'badge.png', { type: 'image/png' })
    const jpeg = new File(['x'], 'photo.jpg', { type: 'image/jpeg' })
    const webp = new File(['x'], 'frame.webp', { type: 'image/webp' })
    const ico = new File(['x'], 'icon.ico', { type: 'image/vnd.microsoft.icon' })
    const gif = new File(['x'], 'anim.gif', { type: 'image/gif' })
    // Valid extension with an odd MIME (e.g. served as a binary stream) is
    // still a valid image — the extension alone must pass it.
    const oddMime = new File(['x'], 'foto.jpg', { type: 'application/octet-stream' })
    // Case-insensitive extension fallback when the browser reports no MIME type.
    const upperExt = new File(['x'], 'PHOTO.PNG')
    const exactlyMax = new File(['x'], 'limit.png', { type: 'image/png' })
    Object.defineProperty(exactlyMax, 'size', { value: 50 * 1024 * 1024 })
    const oversized = new File(['x'], 'big.png', { type: 'image/png' })
    Object.defineProperty(oversized, 'size', { value: 51 * 1024 * 1024 })
    const empty = new File([''], 'nothing.png', { type: 'image/png' })

    expect(isAcceptedImage(png)).toBe(true)
    expect(isAcceptedImage(jpeg)).toBe(true)
    expect(isAcceptedImage(webp)).toBe(false) // WebP retired from new uploads
    expect(isAcceptedImage(upperExt)).toBe(true) // case-insensitive extension fallback
    expect(isAcceptedImage(ico)).toBe(false)
    expect(isAcceptedImage(gif)).toBe(false)
    expect(isAcceptedImage(oddMime)).toBe(true) // valid extension, odd MIME
    expect(isAcceptedImage(exactlyMax)).toBe(true) // boundary: 50 MB is allowed
    expect(isAcceptedImage(oversized)).toBe(false)
    expect(isAcceptedImage(empty)).toBe(false) // zero bytes
  })
})
