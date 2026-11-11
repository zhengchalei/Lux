import { defineConfig } from 'vite'
import vue from '@vitejs/plugin-vue'

// vite.config.ts builds the Vue workspace consumed by the embedded MyGo server.
// Asset paths remain absolute because the application serves one local origin,
// including its champion portraits and typed backend calls, without remote fonts.
// The development proxy terminates same-origin browser requests at Vite and forwards them to the explicitly configured loopback preview service.
export default defineConfig({
  plugins: [vue()],
  server: { port: 5173, strictPort: true, proxy: {
    '/api': { target: 'http://127.0.0.1:19373', changeOrigin: true, headers: { Origin: 'http://127.0.0.1:19373' } },
    '/portraits': { target: 'http://127.0.0.1:19373', changeOrigin: true },
  } },
  build: { target: 'es2022', sourcemap: false },
})
