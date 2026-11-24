import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

/**
 * Integration suite (spec 01§10.2): same jsdom environment + setup, but ONLY
 * `*.integration.test.tsx` files (full component flows against MSW).
 */
export default defineConfig({
  plugins: [react()],
  test: {
    environment: 'jsdom',
    setupFiles: ['src/test/setup.ts'],
    include: ['src/**/*.integration.test.tsx'],
    exclude: ['**/node_modules/**'],
    css: false,
    testTimeout: 60000,
  },
})
