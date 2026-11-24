/**
 * Toast / notification assertions for page tests (004e): the feedback helper
 * and the error helper both render through antd's App context, so tests read
 * the mounted DOM rather than mocking the antd instance.
 *
 * - `expectToast(text)` → toast text rendered into `.ant-message`.
 * - `expectNotification(title, body?)` → an `.ant-notification-notice` card.
 *
 * Both `waitFor` because the mutation resolves asynchronously after MSW
 * answers; a test that only looks synchronously would race the toast mount.
 */
import { expect } from 'vitest'
import { waitFor as waitForDom } from '@testing-library/react'

/** Whole text of the message stack (all notices, in mount order). */
export function toastText(): string {
  return document.querySelector('.ant-message')?.textContent ?? ''
}

/** Total mounted message notices — for "one toast, not two" assertions. */
export function toastCount(): number {
  return document.querySelectorAll('.ant-message-notice').length
}

export async function expectToast(text: string | RegExp): Promise<void> {
  await waitForDom(() => {
    const content = toastText()
    if (typeof text === 'string') expect(content).toContain(text)
    else expect(content).toMatch(text)
  })
}

export async function expectNoToast(text: string | RegExp): Promise<void> {
  await waitForDom(() => {
    const content = toastText()
    if (typeof text === 'string') expect(content).not.toContain(text)
    else expect(content).not.toMatch(text)
  })
}

export interface ToastAssertions {
  /** Message stack text. */
  text: string
  /** Number of mounted notices. */
  count: number
}

/** Snapshot of the message stack for custom assertions. */
export function toasts(): ToastAssertions {
  return { text: toastText(), count: toastCount() }
}

/** Whole text of the notification stack (title + description, in mount order). */
export function notificationText(): string {
  return (
    [...document.querySelectorAll('.ant-notification-notice')]
      .map((notice) => notice.textContent ?? '')
      .join('\n') || ''
  )
}

export async function expectNotification(title: string, body?: string): Promise<void> {
  await waitForDom(() => {
    const content = notificationText()
    expect(content).toContain(title)
    if (body !== undefined) expect(content).toContain(body)
  })
}

/**
 * Persistent `<Alert>` banners are separate from toasts: heavy actions keep
 * their inline result AND raise a notification, so a bare `getByText` would
 * match twice. Scope banner assertions here.
 */
export function queryAlert(text: string | RegExp): HTMLElement | null {
  const nodes = [...document.querySelectorAll<HTMLElement>('.ant-alert-message')]
  return (
    nodes.find((node) => {
      const content = node.textContent ?? ''
      return typeof text === 'string' ? content.includes(text) : text.test(content)
    }) ?? null
  )
}

export async function findAlert(text: string | RegExp): Promise<HTMLElement> {
  return waitForDom(() => {
    const match = queryAlert(text)
    if (match === null) throw new Error(`alert not found: ${String(text)}`)
    return match
  })
}
