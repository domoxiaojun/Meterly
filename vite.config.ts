import { fileURLToPath, URL } from 'node:url'
import { cpSync, mkdirSync } from 'node:fs'
import { defineConfig } from 'vite'
import react, { reactCompilerPreset } from '@vitejs/plugin-react'
import babel from '@rolldown/plugin-babel'
import tailwindcss from '@tailwindcss/vite'

const proxy = { '/api': { target: 'http://127.0.0.1:18788', changeOrigin: false }, '/healthz': { target: 'http://127.0.0.1:18788' } }
export default defineConfig({
  root: 'frontend',
  plugins: [react(), babel({ presets: [reactCompilerPreset()] }), tailwindcss(), {
    name: 'meterly-classic-management',
    apply: 'build',
    closeBundle() {
      const legacy = fileURLToPath(new URL('./web/dist/legacy/', import.meta.url))
      mkdirSync(legacy, { recursive: true })
      for (const file of ['index.html', 'app.js', 'style.css', 'chevron-down.svg', 'vendor', 'modules']) {
        cpSync(fileURLToPath(new URL(`./web/${file}`, import.meta.url)), `${legacy}/${file}`, { recursive: true })
      }
    },
  }],
  resolve: { alias: { '@': fileURLToPath(new URL('./frontend/src', import.meta.url)) } },
  server: { host: '127.0.0.1', port: 5173, strictPort: true, proxy },
  preview: { host: '127.0.0.1', port: 4173, strictPort: true, proxy },
  build: { outDir: '../web/dist', emptyOutDir: true },
})
