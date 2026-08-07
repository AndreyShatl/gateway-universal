import { useState } from 'react'

// Скрывает чувствительное значение (WAN IP) размытием до клика — по
// фидбегу 2026-08-07: "чтобы когда делаешь скриншот не спалить свой IP".
export function BlurReveal({ value }: { value: string }) {
  const [shown, setShown] = useState(false)
  if (shown || value === '—') {
    return <span onClick={() => setShown(false)}>{value}</span>
  }
  return (
    <span
      onClick={() => setShown(true)}
      title="Показать"
      className="cursor-pointer select-none blur-[5px] transition-[filter] hover:blur-[3px]"
    >
      {value}
    </span>
  )
}
