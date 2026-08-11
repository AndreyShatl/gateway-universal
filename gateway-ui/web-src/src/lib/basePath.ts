// basePath — T-shattl-tunnel (2026-08-12): gateway-ui отдаётся и напрямую
// с шлюза (http://LAN_IP:8088/, basePath=""), и через дашборд-прокси
// (https://.../gw/<uuid>/, basePath="/gw/<uuid>"). Один и тот же билд
// должен работать в обоих случаях — префикс вычисляем один раз из
// текущего URL при загрузке страницы, а не зашиваем в билд.
const GATEWAY_PROXY_RE = /^\/gw\/[^/]+/

function detectBasePath(): string {
  const m = window.location.pathname.match(GATEWAY_PROXY_RE)
  return m ? m[0] : ''
}

export const basePath = detectBasePath()

// withBase — добавить префикс к абсолютному пути ("/api/x" -> "/gw/<id>/api/x").
// Не трогает уже-абсолютные URL с протоколом (на всякий случай, для будущих
// вызовов) и не дублирует префикс, если он уже есть.
export function withBase(path: string): string {
  if (!basePath || path.startsWith(basePath)) return path
  return basePath + path
}
