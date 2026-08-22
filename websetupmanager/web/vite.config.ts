import react from '@vitejs/plugin-react'
import { defineConfig } from 'vitest/config'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    rollupOptions: {
      output: {
        manualChunks(moduleId) {
          const id = moduleId.replace(/\\/g, '/')
          if (/\/node_modules\/(?:@mui|@emotion|react(?:-dom|-is)?|scheduler)\//.test(id)) {
            return 'ui-vendor'
          }
          return undefined
        },
      },
    },
  },
  server: {
    host: '127.0.0.1',
    proxy: {
      '/api': 'http://127.0.0.1:8080',
      '/healthz': 'http://127.0.0.1:8080',
      '/readyz': 'http://127.0.0.1:8080',
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    css: true,
    restoreMocks: true,
    clearMocks: true,
  },
})
