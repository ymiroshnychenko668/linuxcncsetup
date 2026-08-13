import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
  },
  server: {
    host: true,
    proxy: {
      '/api': 'http://localhost:8080',
      '/terminal': {
        target: 'http://localhost:8080',
        ws: true,
      },
      '/code': {
        target: 'http://localhost:8080',
        ws: true,
      },
    },
  },
  test: {
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    css: true,
    restoreMocks: true,
    testTimeout: 20000,
  },
})
