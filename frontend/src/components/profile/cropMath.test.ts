/**
 * cropMath (spec 09 decision 6/7): the PURE crop-transform math behind
 * PhotoEditorModal — cover-fit scale, zoom clamp (fit → ~3×), pan clamp (no
 * empty see-through inside the square viewport), 0–360° rotation, and the
 * single transform source shared by the CSS preview and the 512×512 canvas
 * export. The modal has no math of its own; the preview and the export can
 * never disagree.
 */
import { describe, expect, it } from 'vitest'
import {
  clampPan,
  clampZoom,
  coverScale,
  cropTransform,
} from './cropMath'

describe('coverScale', () => {
  it('picks the larger ratio so the viewport is fully covered', () => {
    // 1600×900 portrait-cropped into a 400×400 square: 900 side rules.
    expect(coverScale(1600, 900, 400, 400)).toBeCloseTo(400 / 900)
    // Same for a portrait image rotated against the square.
    expect(coverScale(600, 1200, 300, 300)).toBeCloseTo(300 / 600)
    // An exact square 1:1-equals the viewport scales to exactly 1.
    expect(coverScale(400, 400, 400, 400)).toBe(1)
  })
})

describe('clampZoom', () => {
  const min = 0.5

  it('keeps the zoom within [minZoom, 3×minZoom]', () => {
    expect(clampZoom(0.4, min)).toBe(0.5) // under cover → pinned to cover
    expect(clampZoom(0.9, min)).toBe(0.9) // inside the band → unchanged
    expect(clampZoom(2.0, min)).toBe(1.5) // above the band → pinned to 3×
  })

  it('pins the upper bound at exactly 3×min', () => {
    expect(clampZoom(99, min)).toBe(1.5)
    expect(clampZoom(1.5, min)).toBe(1.5)
  })
})

describe('clampPan', () => {
  it('keeps the shown image covering the viewport with symmetric room per axis', () => {
    // Shown 800×800 in a 400×400 viewport: ±200 around the centered origin,
    // so BOTH drag directions have the same room (the old top-left band
    // [view − shown, 0] blocked one direction and over-panned the other).
    expect(clampPan(0, 0, 800, 800, 400, 400)).toEqual({ x: 0, y: 0 })
    expect(clampPan(-400, 400, 800, 800, 400, 400)).toEqual({ x: -200, y: 200 })
    expect(clampPan(100, -500, 800, 800, 400, 400)).toEqual({ x: 100, y: -200 })
  })

  it('locks the pan at 0 when the shown size equals the viewport (cover fit)', () => {
    expect(clampPan(-9, 12, 400, 400, 400, 400)).toEqual({ x: 0, y: 0 })
  })

  it('swaps the pan room when the image is rotated 90°', () => {
    // Shown 800×400 in a 400² viewport: X band ±200, Y band 0 at 0°.
    expect(clampPan(999, 999, 800, 400, 400, 400, 0)).toEqual({ x: 200, y: 0 })
    // Rotated 90°/270° the extents swap: the room moves to the Y axis.
    const r90 = clampPan(999, 999, 800, 400, 400, 400, 90)
    expect(r90.x).toBe(0)
    expect(r90.y).toBeCloseTo(200)
    const r270 = clampPan(-999, -999, 800, 400, 400, 400, 270)
    expect(r270.x).toBe(0)
    expect(r270.y).toBeCloseTo(-200)
  })
})

describe('cropTransform', () => {
  const img = { w: 1600, h: 900 }
  const view = { w: 400, h: 400 }

  it('centers the shown image at the requested pan within the viewport', () => {
    const t = cropTransform(img.w, img.h, view.w, view.h, 0.3, -10, 20, 0)
    // zoom 0.3 is under cover for 1600×900 in 400² → clamped up to cover.
    expect(t.scale).toBeCloseTo(400 / 900)
    // shown ≈ 711×400; the pan clamps to the shown/viewport band.
    expect(t.tx).toBeCloseTo(view.w / 2 + t.pan.x)
    expect(t.ty).toBeCloseTo(view.h / 2 + t.pan.y)
  })

  it('normalizes the rotation into [0, 360)', () => {
    expect(cropTransform(img.w, img.h, view.w, view.h, 1, 0, 0, 370).deg).toBe(10)
    expect(cropTransform(img.w, img.h, view.w, view.h, 1, 0, 0, -30).deg).toBe(330)
    expect(cropTransform(img.w, img.h, view.w, view.h, 1, 0, 0, 360).deg).toBe(0)
  })

  it('emits one CSS transform driving the preview (translate → rotate → scale)', () => {
    const t = cropTransform(img.w, img.h, view.w, view.h, 1, -10, 20, 90)
    expect(t.cssTransform).toMatch(/^translate\(-?\d+(\.\d+)?px, -?\d+(\.\d+)?px\) rotate\(\d+(\.\d+)?deg\) scale\(\d+(\.\d+)?\)$/)
  })

  it('locks the pan at 0 for a square image exactly at cover', () => {
    // 1600×1600 square at cover covers the viewport EXACTLY → no pan room.
    const t = cropTransform(1600, 1600, 400, 400, 0.1, -50, 50, 0)
    expect(t.pan).toEqual({ x: 0, y: 0 })
  })
})
