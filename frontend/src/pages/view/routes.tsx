/**
 * Public view routes (spec 01§5 / FL-15): `/view/report|gallery|consent` —
 * unauthenticated, outside the AppLayout auth shell. Wired into `router.tsx`
 * by the I2 integration step (wave agents never edit the router).
 */
import type { RouteObject } from 'react-router'
import ConsentPage from './ConsentPage'
import GalleryPage from './GalleryPage'
import ReportPage from './ReportPage'

export const viewRoutes: RouteObject[] = [
  { path: '/view/report', element: <ReportPage /> },
  { path: '/view/gallery', element: <GalleryPage /> },
  { path: '/view/consent', element: <ConsentPage /> },
]
