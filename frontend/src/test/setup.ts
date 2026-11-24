/**
 * Vitest setup shared by BOTH configs (unit + integration).
 *
 * - MSW: one `setupServer` per test file; lifecycle on before/after-all;
 *   unhandled requests are bypassed so nothing ever reaches a real network.
 *   Tests get the instance via `import { server } from './setup'`.
 * - React Testing Library cleanup + jest-dom matchers.
 * - jsdom gaps that antd/React/report code rely on: matchMedia,
 *   ResizeObserver, rAF, scrollIntoView, crypto.randomUUID — each only
 *   installed when missing.
 * - Node's fetch rejects relative URLs (`/api/…`), so after MSW patches
 *   fetch we wrap it (outermost) to resolve them against http://localhost.
 * - Per-test hygiene: handlers reset, mocks/timers restored, storage cleared,
 *   i18n back to ID, api module session state nulled.
 */
import '@testing-library/jest-dom/vitest'
import { cleanup } from '@testing-library/react'
import { afterAll, afterEach, beforeAll, beforeEach, vi } from 'vitest'
import { setupServer } from 'msw/node'
import i18n from '../lib/i18n'
import { setAccessToken, setSessionExpiredHandler, setSessionSchool } from '../lib/api'

export const server = setupServer()

/* ---------- jsdom gap-fillers (only when missing) ---------- */

if (typeof window.matchMedia !== 'function') {
  window.matchMedia = (query: string) =>
    ({
      matches: false,
      media: query,
      onchange: null,
      addListener: () => {},
      removeListener: () => {},
      addEventListener: () => {},
      removeEventListener: () => {},
      dispatchEvent: () => false,
    }) as unknown as MediaQueryList
}

if (typeof globalThis.ResizeObserver !== 'function') {
  globalThis.ResizeObserver = class {
    observe(): void {}
    unobserve(): void {}
    disconnect(): void {}
  }
}

if (typeof globalThis.requestAnimationFrame !== 'function') {
  globalThis.requestAnimationFrame = (callback: FrameRequestCallback): number =>
    setTimeout(() => callback(performance.now()), 16)
  globalThis.cancelAnimationFrame = (handle: number): void => clearTimeout(handle)
}

if (typeof Element.prototype.scrollIntoView !== 'function') {
  Element.prototype.scrollIntoView = () => {}
}

if (typeof globalThis.crypto?.randomUUID !== 'function') {
  const bytes = new Uint8Array(16)
  globalThis.crypto.getRandomValues(bytes)
  bytes[6] = (bytes[6] & 0x0f) | 0x40
  bytes[8] = (bytes[8] & 0x3f) | 0x80
  const hex = [...bytes].map((b) => b.toString(16).padStart(2, '0')).join('')
  globalThis.crypto.randomUUID = (): `${string}-${string}-${string}-${string}-${string}` =>
    `${hex.slice(0, 8)}-${hex.slice(8, 12)}-${hex.slice(12, 16)}-${hex.slice(16, 20)}-${hex.slice(20)}` as `${string}-${string}-${string}-${string}-${string}`
}

/* ---------- MSW lifecycle ---------- */

// jsdom has no CSS transition/animation engine: rc-motion (antd modals,
// dropdowns) waits for transitionend/animationend events that never fire, so
// leave phases would never unmount. Synthesize the end events whenever an
// element enters an active motion class.
const MOTION_ACTIVE = /-(?:enter|leave|appear)-active/
const MOTION_END_EVENTS = [
  'transitionend',
  'TransitionEnd',
  'webkitTransitionEnd',
  'oTransitionEnd',
  'animationend',
  'AnimationEnd',
  'webkitAnimationEnd',
  'oAnimationEnd',
]
const motionObserver = new MutationObserver((mutations) => {
  for (const mutation of mutations) {
    if (mutation.type !== 'attributes') continue
    const element = mutation.target
    if (!(element instanceof Element) || !MOTION_ACTIVE.test(element.className)) continue
    // Two shots: rc-motion attaches its listeners in passive effects, whose
    // scheduling vs. this macrotask is not guaranteed.
    for (const delay of [0, 60]) {
      setTimeout(() => {
        for (const type of MOTION_END_EVENTS) {
          element.dispatchEvent(new Event(type, { bubbles: true }))
        }
      }, delay)
    }
  }
})

beforeAll(() => {
  motionObserver.observe(document, { attributes: true, subtree: true, attributeFilter: ['class'] })
})

afterAll(() => {
  motionObserver.disconnect()
})

beforeAll(() => {
  // 'error' = no test can ever reach a real network: a missed handler fails
  // loudly instead of silently bypassing to the socket.
  server.listen({ onUnhandledRequest: 'error' })
  // Outermost wrapper (applied AFTER listen): relative `/api/...` URLs from
  // the app become absolute (jsdom's origin) before MSW/node fetch see them —
  // Node's fetch rejects relative URLs and MSW resolves handler paths against
  // the same origin, so both must agree.
  const patched = globalThis.fetch
  globalThis.fetch = ((input: RequestInfo | URL, init?: RequestInit) =>
    typeof input === 'string' && input.startsWith('/')
      ? patched(new URL(input, window.location.origin).toString(), init)
      : patched(input, init)) as typeof fetch
})

afterAll(() => {
  server.close()
})

beforeEach(async () => {
  await i18n.changeLanguage('id') // a previous test may have switched to EN
  localStorage.clear()
})

afterEach(() => {
  server.resetHandlers()
  cleanup()
  vi.unstubAllGlobals()
  vi.restoreAllMocks()
  vi.useRealTimers()
  setAccessToken(null)
  setSessionSchool(null)
  setSessionExpiredHandler(null)
})
