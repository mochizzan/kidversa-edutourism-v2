/**
 * Thin renderer of the RequireRole decision (spec 02§3): RequireRole in
 * router.tsx computes the decision from auth state, this component only
 * renders it — no duplicate guard logic lives here.
 */
import type { ReactNode } from 'react'
import { Flex, Spin } from 'antd'
import { Navigate, useLocation } from 'react-router'
import type { AuthDecision } from '../lib/auth'

export interface ProtectedProps {
  decision: AuthDecision
  /** false while the boot refresh restore is still settling. */
  ready: boolean
  children: ReactNode
}

export default function Protected({ decision, ready, children }: ProtectedProps) {
  const location = useLocation()
  if (!ready) {
    return (
      <Flex align="center" justify="center" style={{ minHeight: '60vh' }}>
        <Spin size="large" />
      </Flex>
    )
  }
  if (decision.status === 'anon') {
    return (
      <Navigate
        to="/login"
        replace
        state={{ from: `${location.pathname}${location.search}` }}
      />
    )
  }
  if (decision.status === 'forbidden') {
    return <Navigate to={decision.redirectTo} replace />
  }
  return <>{children}</>
}
