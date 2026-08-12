import { useCallback, useEffect, useState } from 'react'

export type Theme = 'dark' | 'light'
export type Density = 'compact' | 'comfortable' | 'spacious'

function usePersistedAttr<T extends string>(key: string, attr: string, fallback: T) {
  const [value, setValue] = useState<T>(() => (localStorage.getItem(key) as T) || fallback)

  useEffect(() => {
    if (value === fallback && !localStorage.getItem(key)) {
      document.documentElement.removeAttribute(attr)
    } else {
      document.documentElement.setAttribute(attr, value)
    }
  }, [value, attr, fallback, key])

  const set = useCallback(
    (v: T) => {
      localStorage.setItem(key, v)
      setValue(v)
    },
    [key],
  )

  return [value, set] as const
}

export function useTheme() {
  return usePersistedAttr<Theme>('shattl-theme', 'data-theme', 'dark')
}

export function useDensity() {
  return usePersistedAttr<Density>('shattl-density', 'data-density', 'comfortable')
}

// useAdvancedMode — ТЗ п.12 "Advanced Diagnostics": по умолчанию скрыт,
// пользователь сам включает, чтобы увидеть внутренний состав Traffic
// Engine (Zapret/CIADPI/zapret2/xray) вместо одной агрегированной строки.
const ADVANCED_MODE_KEY = 'shattl-advanced-mode'

export function useAdvancedMode() {
  const [value, setValue] = useState<boolean>(() => localStorage.getItem(ADVANCED_MODE_KEY) === '1')

  const set = useCallback((v: boolean) => {
    localStorage.setItem(ADVANCED_MODE_KEY, v ? '1' : '0')
    setValue(v)
  }, [])

  return [value, set] as const
}
