/**
 * Route inventory (F-F01): every page group owns its routes.tsx and router.tsx
 * spreads all 17 group arrays into its public/authenticated slots. Fully
 * deterministic — fails when a group's routes.tsx is missing (or an
 * unexpected one appears), when a group module stops exporting its single
 * `<group>Routes` RouteObject array, when router.tsx drops a group
 * import/spread (or reintroduces the removed aggregates), or when a moved
 * authenticated path changes.
 */
// Router-first evaluation: router.tsx ↔ pages/<group>/routes.tsx form a
// circular import (routes import RequireRole; router spreads the arrays at
// module init). Entering the cycle from a group file leaves that group's
// array export unset when router spreads it, so the router must be the entry.
import '../router'
import { describe, expect, it } from 'vitest'
import type { RouteObject } from 'react-router'
import routerSource from '../router.tsx?raw'

const EXPECTED_GROUPS = [
  'activities',
  'assessment',
  'auth',
  'children',
  'consent',
  'dashboard',
  'frames',
  'gallery',
  'missions',
  'profile',
  'programs',
  'reports',
  'sessions',
  'schools',
  'topics',
  'users',
  'view',
] as const

/** The 14 new F-F01 groups — auth/view/sessions predate this fix. */
const NEW_GROUPS = EXPECTED_GROUPS.filter(
  (group) => group !== 'auth' && group !== 'view' && group !== 'sessions',
)

/** The 19 paths router.tsx spreads into the authenticated shell (set compare). */
const SHELL_PATHS = [
  '/',
  '/schools',
  '/users',
  '/profile',
  '/programs',
  '/topics',
  '/activities',
  '/missions',
  '/frames',
  '/children',
  '/sessions',
  '/sessions/new',
  '/sessions/:sid',
  '/sessions/:sid/gallery',
  '/sessions/:sid/reports',
  '/assessment/:sid/:cid/:stid',
  '/reports',
  '/consent',
  '/consent/:sid',
] as const

// NOT `{ eager: true }`: Vite hoists an eager glob's generated imports above
// every source import, entering the router ↔ group-routes circular import from
// the group side (router.tsx then spreads a still-initializing group array —
// observed: `TypeError: activitiesRoutes is not iterable` at module load).
// The static `import '../router'` above evaluates the cycle router-first, and
// these thunks are invoked only afterwards, over all keys (deterministic).
const routeGlob = import.meta.glob('./*/routes.tsx') as Record<
  string,
  () => Promise<Record<string, unknown>>
>

const routeModules = Object.fromEntries(
  await Promise.all(
    Object.entries(routeGlob).map(async ([key, load]) => [key, await load()] as const),
  ),
) as Record<string, Record<string, unknown>>

/** Exactly one array export per group module — its `<group>Routes`. */
function routesOf(group: string): RouteObject[] {
  const mod = routeModules[`./${group}/routes.tsx`]
  expect(mod, `missing ./pages/${group}/routes.tsx`).toBeDefined()
  const arrays = Object.entries(mod ?? {}).filter(([, value]) => Array.isArray(value))
  expect(arrays, `${group}/routes.tsx must export exactly one route array`).toHaveLength(1)
  const [name, routes] = arrays[0] as [string, unknown[]]
  // `sessions` pre-dates F-F01 and keeps the historic `sessionRoutes` name.
  expect(name, `${group}/routes.tsx export name`).toBe(
    group === 'sessions' ? 'sessionRoutes' : `${group}Routes`,
  )
  return routes as RouteObject[]
}

describe('page-group route inventory (F-F01)', () => {
  it('loads exactly one routes.tsx per expected group (17)', () => {
    // `'./activities/routes.tsx'` → `'activities'`.
    const loaded = Object.keys(routeModules)
      .map((key) => key.slice(2, key.lastIndexOf('/')))
      .sort()
    expect(loaded).toEqual([...EXPECTED_GROUPS].sort())
  })

  it('every group module exports its <group>Routes RouteObject array', () => {
    for (const group of EXPECTED_GROUPS) {
      const routes = routesOf(group)
      expect(routes.length, `${group}/routes.tsx routes`).toBeGreaterThan(0)
      for (const route of routes) {
        expect(typeof route.path, `${group} route path`).toBe('string')
        expect(route.element, `${group} route element`).toBeDefined()
      }
    }
  })

  it('router.tsx imports and spreads all 14 new groups; removed aggregates absent', () => {
    for (const group of NEW_GROUPS) {
      expect(routerSource, `router.tsx must import ./pages/${group}/routes`).toContain(
        `./pages/${group}/routes`,
      )
      expect(routerSource, `router.tsx must spread ...${group}Routes`).toContain(
        `...${group}Routes`,
      )
    }
    expect(routerSource).not.toContain('./pages/admin/routes')
    expect(routerSource).not.toContain('./pages/masters/routes')
  })

  it('authenticated group modules still declare the original 19 paths', () => {
    const collected = [...NEW_GROUPS, 'sessions'].flatMap((group) =>
      routesOf(group).map((route) => route.path),
    )
    expect([...collected].sort()).toEqual([...SHELL_PATHS].sort())
  })
})
