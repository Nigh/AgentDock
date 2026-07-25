import { defineConfig } from 'vite';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import tailwindcss from '@tailwindcss/vite';

export default defineConfig({
  plugins: [svelte(), tailwindcss()],
  build: {
    // xterm 6.0 ships pre-minified ESM using ES2021 `||=`. Vite's default
    // es2020 target makes esbuild lower that operator during re-minification,
    // which corrupts InputHandler.requestMode ("ReferenceError: i is not
    // defined" on vim's DECRQM query, freezing all terminal output).
    // Production builds only. https://github.com/xtermjs/xterm.js/issues/5800
    target: 'es2022',
    // built assets are embedded into the Go server binary
    outDir: '../server/internal/webui/dist',
    emptyOutDir: true,
  },
  server: {
    proxy: {
      '/api': { target: 'http://localhost:8080', ws: true },
    },
  },
});
