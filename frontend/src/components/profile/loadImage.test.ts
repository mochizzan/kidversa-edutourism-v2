/**
 * loadImage seam (spec 09): the REAL module (no mocks here) — an object URL
 * resolves on the image's load event and rejects on its error event. jsdom
 * never decodes image resources, so the lifecycle is driven through the src
 * setter seam: assign → capture the element → dispatch the terminal event.
 */
import { afterEach, describe, expect, it } from 'vitest'
import loadImage from './loadImage'

const realSrc = Object.getOwnPropertyDescriptor(HTMLImageElement.prototype, 'src')
let capture: HTMLImageElement | null = null

function stealSrc(): void {
  Object.defineProperty(HTMLImageElement.prototype, 'src', {
    set(this: unknown) {
      // jsdom: no decode — hold the element until the terminal event.
      capture = this as HTMLImageElement
    },
    configurable: true,
  })
}

afterEach(() => {
  if (realSrc?.set) {
    Object.defineProperty(HTMLImageElement.prototype, 'src', realSrc)
  }
  capture = null
})

describe('loadImage', () => {
  it('resolves the image element when the load event fires', async () => {
    stealSrc()
    const promise = loadImage('blob:ok')
    expect(capture).not.toBeNull()

    capture?.dispatchEvent(new Event('load'))
    await expect(promise).resolves.toBe(capture)
  })

  it('rejects when the error event fires (undecodable source)', async () => {
    stealSrc()
    const promise = loadImage('blob:bad')
    expect(capture).not.toBeNull()

    capture?.dispatchEvent(new Event('error'))
    await expect(promise).rejects.toThrow('image decode failed')
  })
})
