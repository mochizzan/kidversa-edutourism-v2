/**
 * cropMath (spec 09 decision 6/7): the PURE crop-transform math behind
 * PhotoEditorModal. The square viewport IS the crop window: whatever the
 * transform shows is what the 512×512 export composes. coverScale/clampZoom/
 * clampPan keep the viewport fully covered (no empty see-through); deg is
 * normalized into [0, 360). The modal has no math of its own — the CSS
 * preview and the canvas export both consume cropTransform, so they can
 * never disagree.
 */

/** Smallest scale that fully covers the square viewport (no dead area). */
export function coverScale(imageW: number, imageH: number, viewW: number, viewH: number): number {
  return Math.max(viewW / imageW, viewH / imageH)
}

/** Zoom band [minZoom, 3×minZoom] (spec: zoom ≤ ~3× the cover fit). */
export function clampZoom(zoom: number, minZoom: number): number {
  return Math.min(Math.max(zoom, minZoom), minZoom * 3)
}

/**
 * Pan clamp for a CENTERED image: the preview centers the image in the
 * viewport and the transform is applied about the image's center, so each
 * axis offset stays within ±(shown − viewport)/2. (Anchoring it to
 * [view − shown, 0] like a top-left layout left the image unable to move in
 * one direction and over-panning in the other, exposing the background.)
 * Rotation swaps the extents, so 90°/270° pan room stays honest.
 */
export function clampPan(
  x: number,
  y: number,
  shownW: number,
  shownH: number,
  viewW: number,
  viewH: number,
  deg = 0,
): { x: number; y: number } {
  const rad = (deg * Math.PI) / 180
  const c = Math.abs(Math.cos(rad))
  const s = Math.abs(Math.sin(rad))
  const halfW = (shownW * c + shownH * s) / 2
  const halfH = (shownW * s + shownH * c) / 2
  const bandX = Math.max(0, halfW - viewW / 2)
  const bandY = Math.max(0, halfH - viewH / 2)
  // Snap FP residue (and -0) to 0: an exact cover fit / right angle must
  // report a clean zero, not 2.8e-14 px of phantom pan room.
  const snap = (v: number): number => (Math.abs(v) < 1e-6 ? 0 : v)
  return {
    x: snap(Math.min(Math.max(x, -bandX), bandX)),
    y: snap(Math.min(Math.max(y, -bandY), bandY)),
  }
}

export interface CropTransform {
  /** Effective (clamped) zoom. */
  scale: number
  /** Effective (clamped) pan, in viewport px. */
  pan: { x: number; y: number }
  /** Image center after the pan (canvas translate target). */
  tx: number
  ty: number
  /** Rotation normalized into [0, 360). */
  deg: number
  /** Preview's CSS transform (element is centered; origin = center). */
  cssTransform: string
}

/**
 * The ONE crop transform: clamps zoom against the cover fit, clamps the pan
 * against the clamped zoom, normalizes the rotation, and reports both the
 * canvas draw targets (tx/ty) and the equivalent CSS transform string.
 */
export function cropTransform(
  imageW: number,
  imageH: number,
  viewW: number,
  viewH: number,
  zoom: number,
  panX: number,
  panY: number,
  deg: number,
): CropTransform {
  const minZoom = coverScale(imageW, imageH, viewW, viewH)
  const scale = clampZoom(zoom, minZoom)
  const norm = ((deg % 360) + 360) % 360
  const pan = clampPan(panX, panY, imageW * scale, imageH * scale, viewW, viewH, norm)
  const fmt = (n: number): string => `${Math.round(n * 100) / 100}`
  return {
    scale,
    pan,
    tx: viewW / 2 + pan.x,
    ty: viewH / 2 + pan.y,
    deg: norm,
    cssTransform: `translate(${fmt(pan.x)}px, ${fmt(pan.y)}px) rotate(${fmt(norm)}deg) scale(${fmt(scale)})`,
  }
}
