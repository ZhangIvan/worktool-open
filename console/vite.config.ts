import { defineConfig } from 'vite';
import { fileURLToPath } from 'node:url';
import { consoleProxy, isGoMode } from './mode.ts';
export default defineConfig(({ mode }) => ({
  root: fileURLToPath(new URL('.', import.meta.url)),
  server: { host: '127.0.0.1', port: 5173, strictPort: true, proxy: { '/api': consoleProxy(mode) } },
  preview: { host: '127.0.0.1', port: 5173, strictPort: true },
  build: { outDir: isGoMode(mode) ? 'dist-go' : 'dist', emptyOutDir: true },
}));
