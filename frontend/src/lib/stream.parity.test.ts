/**
 * SSE catalogue parity (finding 005c): every event string this app
 * REGISTERES with the stream layer — registerRefetch / registerSignal /
 * useEventRefetch / useListLoader / usePagedList events / usePublicStream
 * handler keys, the StreamGate VALUE_EVENTS set, and the terminal
 * `event ===` comparisons — must be a member of the pinned 02 §2c
 * catalogue (src/test/catalogues.ts). The FE keys handlers on these
 * strings verbatim, so a typo'd event ("program.chnged") silently never
 * fires; this scan catches it at test time.
 *
 * Pragmatic approach (stated choice): the src tree is scanned as raw
 * source via import.meta.glob (?raw) with one precise regex per
 * registration API, test files excluded — richer than pinning known
 * registration sites (which cannot see a typo IN a site) and
 * deterministic (the tree is fixed at test time). A sentinel set guards
 * against the regexes rotting into matching nothing.
 */
import { describe, expect, it } from 'vitest'
import { PINNED_SSE_EVENTS } from '../test/catalogues'

const EVENT_SHAPE = '[a-z][a-z0-9-]*\\.[a-z0-9-]+'

// Production sources only: *.test.* / /src/test/ / *.spec.* are excluded —
// they intentionally register probe events.
const sourceFiles = Object.entries(
  import.meta.glob('/src/**/*.{ts,tsx}', {
    eager: true,
    query: '?raw',
    import: 'default',
  }) as Record<string, string>,
).filter(
  ([path]) =>
    !path.includes('.test.') &&
    !path.includes('.spec.') &&
    !path.startsWith('/src/test/'),
)

function collect(re: RegExp, text: string): string[] {
  const out: string[] = []
  for (const m of text.matchAll(re)) {
    if (m[1] !== undefined) out.push(m[1])
  }
  return out
}

function extractQuoted(captures: string[]): string[] {
  return captures.flatMap((capture) => collect(/'([^']+)'/g, capture))
}

/** One precise extractor per registration surface. */
function registeredEvents(text: string): string[] {
  const found: string[] = []
  // 1) registerRefetch('x') / registerSignal<T>('x') (also across newlines)
  found.push(
    ...collect(
      new RegExp(`register(?:Refetch|Signal)(?:\\s*<[^>]*>)?\\s*\\(\\s*'([^']+)'`, 'g'),
      text,
    ),
  )
  // 2) ['a', …].map((event) => registerRefetch(event, …)) — session shared helper
  found.push(
    ...extractQuoted(
      collect(
        new RegExp(`\\[([^\\]]+)\\]\\s*\\.map\\(\\s*\\(event\\)\\s*=>\\s*registerRefetch`, 'g'),
        text,
      ),
    ),
  )
  // 3) useEventRefetch(['a', 'b'], …)
  found.push(...extractQuoted(collect(/useEventRefetch\(\s*\[([^\]]*)\]/g, text)))
  // 4) useListLoader(path, {…}, ['a', 'b']) — events are the 3rd argument
  found.push(
    ...extractQuoted(
      collect(
        new RegExp(
          `useListLoader(?:<[^>]*>)?\\(\\s*[^,]*,\\s*\\{[^}]*\\}\\s*,\\s*\\[([^\\]]*)\\]`,
          'g',
        ),
        text,
      ),
    ),
  )
  // 5) usePagedList(path, 'a', …) — single event, second argument
  found.push(
    ...collect(
      new RegExp(
        `usePagedList(?:<[^>]*>)?\\(\\s*(?:'[^']*'|\`[^\`]*\`)\\s*,\\s*'([^']+)'`,
        'g',
      ),
      text,
    ),
  )
  // 6) usePublicStream(token, { 'a': handler, … }, …) — handler keys in window
  for (const [region] of text.matchAll(/usePublicStream[\s\S]{0,600}/g)) {
    found.push(...collect(new RegExp(`'(${EVENT_SHAPE})'\\s*:`, 'g'), region))
  }
  // 7) terminal comparisons: event === 'link.revoked' / 'auth.expiring'
  found.push(...collect(new RegExp(`event\\s*===\\s*'(${EVENT_SHAPE})'`, 'g'), text))
  // 8) StreamGate full-value routing set
  found.push(
    ...extractQuoted(collect(/VALUE_EVENTS\s*=\s*new Set\(\[([^\]]*)\]/g, text)),
  )
  return found
}

const foundByFile = sourceFiles.map(
  ([path, text]) => [path, registeredEvents(text)] as const,
)
const foundEvents = new Set(foundByFile.flatMap(([, events]) => events))

// One sentinel per API family: if a regex rots into matching nothing the
// sentinel set fails loudly instead of the suite silently weakening.
const SENTINELS = [
  'program.changed', // usePagedList
  'session.status', // mapped registerRefetch array
  'member.changed', // mapped registerRefetch array
  'consent.changed', // useListLoader
  'school.changed', // useListLoader
  'reset.changed', // useListLoader
  'photo.changed', // useEventRefetch
  'stats.changed', // direct registerRefetch
  'readiness.changed', // direct registerSignal + VALUE_EVENTS
  'consent.ack', // usePublicStream handler key
  'link.revoked', // event === terminal handling
  'auth.expiring', // event === control frame
]

describe('SSE catalogue parity (02 §2c)', () => {
  it('pins exactly 30 events', () => {
    expect(PINNED_SSE_EVENTS).toHaveLength(30)
    expect(new Set(PINNED_SSE_EVENTS).size, 'pinned events must be unique').toBe(30)
  })

  it('scans production sources and sees the critical registration sites', () => {
    expect(sourceFiles.length, 'the raw source glob must match files').toBeGreaterThan(50)
    const seen = [...foundEvents]
    expect(seen.length, 'the scan must extract at least the sentinels').toBeGreaterThanOrEqual(
      SENTINELS.length,
    )
    for (const sentinel of SENTINELS) {
      expect(seen, `sentinel event "${sentinel}" must be seen by the scan`).toContain(sentinel)
    }
  })

  it('every registered event string belongs to the pinned catalogue', () => {
    const offenders = foundByFile.flatMap(([path, events]) =>
      events
        .filter((event) => !PINNED_SSE_EVENTS.includes(event))
        .map((event) => `${event} (${path})`),
    )
    expect([...new Set(offenders)].sort(), 'registered events outside 02 §2c').toEqual([])
  })
})
