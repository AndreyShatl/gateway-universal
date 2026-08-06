import { useState } from 'react'
import { Info } from 'lucide-react'

// Компактная подсказка (i) — hover на десктопе, click на тач-устройствах
// (по фидбегу 2026-08-06: "непонятно что делает конкретный функционал").
// Не занимает места, пока не нужна.
export function InfoTip({ text }: { text: string }) {
  const [open, setOpen] = useState(false)

  return (
    <span
      className="relative inline-flex"
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
    >
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          setOpen((o) => !o)
        }}
        className="flex h-4 w-4 items-center justify-center rounded-full text-text-muted hover:text-text-secondary"
        aria-label="Подсказка"
      >
        <Info size={12} strokeWidth={1.8} />
      </button>
      {open && (
        <div className="absolute left-1/2 top-[calc(100%+6px)] z-20 w-[240px] -translate-x-1/2 rounded-md border border-border-strong bg-surface-raised p-2.5 text-[11px] leading-snug text-text-secondary shadow-lg">
          {text}
        </div>
      )}
    </span>
  )
}
