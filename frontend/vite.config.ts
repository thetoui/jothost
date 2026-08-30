import { defineConfig } from 'vitest/config';
import react from '@vitejs/plugin-react';
import { fileURLToPath, URL } from 'node:url';

// The dev server runs inside a Linux container; 0.0.0.0 makes it reachable
// from the Windows host, and /api is proxied to the Go API so the browser
// never needs a second origin.
export default defineConfig({
  plugins: [react()],
  resolve: {
    alias: {
      '@': fileURLToPath(new URL('./src', import.meta.url)),
    },
  },
  server: {
    host: '0.0.0.0',
    port: 5173,
    strictPort: true,
    // Vite rejects requests whose Host header it does not recognise. The dev
    // server sits behind the Nginx container, so its service name must be
    // allowed explicitly rather than disabling the check entirely.
    allowedHosts: ['localhost', '127.0.0.1', 'nginx', 'frontend'],
    watch: {
      // Bind-mounted Windows volumes do not deliver inotify events.
      usePolling: true,
      interval: 300,
    },
    proxy: {
      '/api': {
        target: process.env.VITE_API_PROXY_TARGET ?? 'http://api:8080',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: false,
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    css: false,
    // The default 5s is not enough headroom once the whole suite runs in
    // parallel inside a container; see src/test/setup.ts for the same reasoning
    // applied to Testing Library's own waits.
    testTimeout: 20_000,
  },
});
