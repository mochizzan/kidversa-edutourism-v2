import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

/**
 * Unit suite (spec 01§10.2): jsdom + RTL co-located tests under src
 * (`*.test.ts` / `*.test.tsx`); `*.integration.test.*` files belong to
 * vitest.integration.config.ts.
 */
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['src/test/setup.ts'],
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
    exclude: ['**/node_modules/**', '**/*.integration.test.*'],
    css: false,
    // jsdom + antd event simulation: real interaction tests (typing into
    // forms, modals) legitimately need more than the 5s default — and up to
    // ~3x that again while v8 coverage instrumentation runs.
    testTimeout: 60000,
    coverage: {
      provider: 'v8',
      include: ['src/**/*.{ts,tsx}'],
      exclude: ['src/test/**'],
      reporter: ['text'],
      // Show fully-covered files too (honest per-file reporting).
      skipFull: false,
      // Spec gate 90/90/90/90 (01§10.2) — the suite must clear these on
      // every run; per-file detail in the text report keeps gaps visible.
      thresholds: {
        statements: 90,
        branches: 90,
        functions: 90,
        lines: 90,
      },
    },
  },
})
