/** Gallery route: `/sessions/:sid/gallery` — all roles (server scopes FAS to
 * own/reduced groups, 02§2b). */
import type { RouteObject } from 'react-router'
import { RequireRole } from '../../router'
import GalleryPage from '../gallery/GalleryPage'

export const galleryRoutes: RouteObject[] = [
  { path: '/sessions/:sid/gallery', element: <RequireRole><GalleryPage /></RequireRole> },
]
