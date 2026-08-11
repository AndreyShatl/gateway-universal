import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

// https://vite.dev/config/
export default defineConfig({
  // Относительные пути к ассетам (T-shattl-tunnel, 2026-08-12): gateway-ui
  // отдаётся и напрямую с шлюза (http://LAN_IP:8088/), и через дашборд-прокси
  // (https://.../gw/<id>/) — с абсолютными путями (/assets/...) браузер при
  // проксировании запрашивал бы их с корня ДАШБОРДА (совпадение по имени
  // пути), подсовывая вместо gateway-ui интерфейс самого дашборда.
  base: './',
  plugins: [react(), tailwindcss()],
  server: {
    proxy: {
      '/api': 'http://127.0.0.1:8088',
      '/ws': { target: 'ws://127.0.0.1:8088', ws: true },
    },
  },
  build: {
    outDir: '../static/dist',
    emptyOutDir: true,
  },
})
