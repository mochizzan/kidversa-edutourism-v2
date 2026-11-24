/**
 * Confirmation feedback for data changes (004e): one vocabulary for every
 * mutation, so the toast that follows an action names the same thing the
 * button that caused it named.
 *
 * - `created` / `saved` / `deleted` / `changed` → antd `message.success`
 *   (the app-wide transient convention; default antd timing, no overrides).
 * - `heavy` → antd `notification.success` for actions that finish after real
 *   server work and whose outcome deserves a title + body: batch report send,
 *   AI narrative generation, copying a session's data.
 *
 * Copy lives in `common.feedback.*`; the record name is interpolated, so a
 * confirmation always says WHICH row changed. Failures never come through
 * here — `lib/errors.ts` `useErrorToast` is the single error path, and modal
 * forms keep their inline `Alert`.
 */
import { useCallback, useMemo } from 'react'
import { App as AntApp } from 'antd'
import { useTranslation } from 'react-i18next'

/** Record kinds that can be created/saved/deleted from the UI. */
export type FeedbackEntity =
  | 'program'
  | 'topic'
  | 'activity'
  | 'mission'
  | 'frame'
  | 'child'
  | 'user'
  | 'school'
  | 'session'
  | 'group'
  | 'photo'
  | 'reset'

/** Binary lifecycle flips that read as "verb the record" instead of "saved". */
export type FeedbackState = 'activated' | 'deactivated' | 'approved' | 'rejected' | 'revoked'

type FeedbackCopyKey = 'created' | 'saved' | 'deleted' | FeedbackState

export interface Feedback {
  /** A new record now exists. */
  created(entity: FeedbackEntity, name: string): void
  /** An existing record was edited and kept. */
  saved(entity: FeedbackEntity, name: string): void
  /** A record is gone. */
  deleted(entity: FeedbackEntity, name: string): void
  /** A record moved between two lifecycle states. */
  changed(entity: FeedbackEntity, name: string, state: FeedbackState): void
  /** Tiered card for multi-second work with a title and a short result body. */
  heavy(title: string, body: string): void
}

export function useFeedback(): Feedback {
  const { message, notification } = AntApp.useApp()
  const { t } = useTranslation('common')

  const say = useCallback(
    (key: FeedbackCopyKey, entity: FeedbackEntity, name: string) => {
      message.success(
        t(`feedback.${key}`, { entity: t(`feedback.entity.${entity}`), name }),
      )
    },
    [message, t],
  )

  return useMemo<Feedback>(
    () => ({
      created: (entity, name) => say('created', entity, name),
      saved: (entity, name) => say('saved', entity, name),
      deleted: (entity, name) => say('deleted', entity, name),
      changed: (entity, name, state) => say(state, entity, name),
      heavy: (title, body) => {
        notification.success({ message: title, description: body })
      },
    }),
    [say, notification],
  )
}
