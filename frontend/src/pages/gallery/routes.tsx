/** Gallery routes: the child directory (`/sessions/:sid/gallery`) and the
 * dedicated per-child gallery (`/sessions/:sid/gallery/children/:cid`) — all
 * roles (server scopes FAS to own/reduced groups, 02§2b). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
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
]
