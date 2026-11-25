/** Gallery routes: the child directory (`/sessions/:sid/gallery`), the
 * dedicated per-child gallery (`/sessions/:sid/gallery/children/:cid`) and its
 * camera studio (`…/capture`) — all roles (server scopes FAS to own/reduced
 * groups, 02§2b). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import CapturePage from '../gallery/CapturePage'
import ChildGalleryPage from '../gallery/ChildGalleryPage'
import GalleryPage from '../gallery/GalleryPage'

export const galleryRoutes: RouteObject[] = [
  { path: '/sessions/:sid/gallery', element: <RequireRole><GalleryPage /></RequireRole> },
  {
    path: '/sessions/:sid/gallery/children/:cid',
    element: (
      <RequireRole>
        <ChildGalleryPage />
      </RequireRole>
    ),
  },
  {
    path: '/sessions/:sid/gallery/children/:cid/capture',
    element: (
      <RequireRole>
        <CapturePage />
      </RequireRole>
    ),
  },
]
