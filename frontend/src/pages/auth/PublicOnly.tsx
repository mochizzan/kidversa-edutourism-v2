/** Bounces an already-authenticated session out of the public auth pages. */
import type { ReactNode } from 'react'
import { Navigate } from 'react-router'
import { useAuth } from '../../lib/auth'

export default function PublicOnly({ children }: { children: ReactNode }) {
  const { user, ready } = useAuth()
  if (ready && user) return <Navigate to="/" replace />
  return <>{children}</>
}
