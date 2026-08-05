import { useEffect, useRef, useState } from 'react'
import { useReducedMotion } from 'framer-motion'

/** AnimatedNumber — плавно интерполирует между значениями вместо мгновенного
 * скачка. rAF-based tween, ~350ms ease-out. Уважает prefers-reduced-motion —
 * тогда просто прыгает на новое значение. */
export function AnimatedNumber({
  value,
  decimals = 0,
  suffix = '',
}: {
  value: number | null | undefined
  decimals?: number
  suffix?: string
}) {
  const reduced = useReducedMotion()
  const [display, setDisplay] = useState(value ?? 0)
  const fromRef = useRef(value ?? 0)
  const rafRef = useRef<number>(0)

  useEffect(() => {
    if (value === null || value === undefined) return
    if (reduced) {
      setDisplay(value)
      fromRef.current = value
      return
    }
    const from = fromRef.current
    const to = value
    const duration = 350
    const start = performance.now()

    cancelAnimationFrame(rafRef.current)
    function tick(now: number) {
      const t = Math.min(1, (now - start) / duration)
      const eased = 1 - Math.pow(1 - t, 3)
      setDisplay(from + (to - from) * eased)
      if (t < 1) rafRef.current = requestAnimationFrame(tick)
      else fromRef.current = to
    }
    rafRef.current = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(rafRef.current)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [value, reduced])

  if (value === null || value === undefined) return <>—</>
  return (
    <>
      {display.toFixed(decimals)}
      {suffix}
    </>
  )
}
