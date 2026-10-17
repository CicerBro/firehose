import { defineConfig } from 'vite';

// The Go server embeds web/dist. In `make dev`, Vite proxies /api (incl. WebSocket) to it.
export default defineConfig({
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    // Strict CSP (script-src/style-src 'self'): never inline anything into index.html.
    assetsInlineLimit: 0,
    modulePreload: { polyfill: false },
  },
  worker: { format: 'es' },
  server: {
    port: 5173,
    proxy: {
      '/api': { target: 'http://127.0.0.1:8080', ws: true },
      '/healthz': 'http://127.0.0.1:8080',
    },
  },
});
