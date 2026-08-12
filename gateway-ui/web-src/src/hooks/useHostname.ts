import { useEffect, useState } from 'react'
import { fetchHostname } from '../lib/api'

// useHostname — T-shattl-multi-gateway (2026-08-12): при нескольких открытых
// вкладках разных шлюзов (обычное дело через дашборд-прокси) не только
// показываем имя в шапке, но и меняем заголовок вкладки браузера — та самая
// путаница обычно ловится взглядом на панель вкладок, не на саму страницу.
export function useHostname() {
  const [hostname, setHostname] = useState('')

  useEffect(() => {
    fetchHostname()
      .then((r) => {
        setHostname(r.hostname)
        if (r.hostname) document.title = `${r.hostname} — Shattl Gateway`
      })
      .catch(() => {
        /* не критично — шапка просто не покажет имя */
      })
  }, [])

  return hostname
}
