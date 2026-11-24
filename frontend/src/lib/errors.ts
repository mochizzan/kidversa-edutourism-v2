/**
 * Cross-cutting error handling (004d — moved out of pages/masters/shared.tsx
 * so every page group sees it): `errors.<CODE>` toasts via antd's App context,
 * branching on the envelope code (lib/api `errorCode`), never on HTTP status.
 */
import { useCallback } from 'react'
import { App as AntApp } from 'antd'
import { useTranslation } from 'react-i18next'
import { errorCode } from './api'

export function useErrorToast(): (error: unknown) => void {
  const { message } = AntApp.useApp()
  const { t } = useTranslation('common')
  return useCallback(
    (error: unknown) => {
      message.error(t(`errors.${errorCode(error)}`))
    },
    [message, t],
  )
}
