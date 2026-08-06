import { useState } from 'react'
import { motion } from 'framer-motion'
import type { EngineVersion } from '../lib/api'
import { updateEngine } from '../lib/api'
import { usePoll } from '../hooks/usePoll'

export function EngineCard({
  engine,
  label,
  fetcher,
  index,
}: {
  engine: 'zapret' | 'ciadpi' | 'zapret2'
  label: string
  fetcher: () => Promise<EngineVersion>
  index: number
}) {
  const { data } = usePoll(fetcher, 5000)
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)

  async function onUpdate() {
    if (!confirm(`Обновить движок ${label} из апстрима и пересобрать?`)) return
    setBusy(true)
    setMsg(null)
    try {
      const res = await updateEngine(engine)
      if (res.error) setMsg('✗ ' + res.error)
      else setMsg('✓ запущено')
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  return (
    <motion.div
      initial={{ opacity: 0, y: 4 }}
      animate={{ opacity: 1, y: 0 }}
      transition={{ duration: 0.4, delay: Math.min(index * 0.05, 0.3) }}
      className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)"
    >
      <div className="mb-(--metrics-pt) flex items-center justify-between">
        <span className="text-sm font-medium">{label}</span>
        {data?.updating && (
          <span className="rounded-md bg-warning/[.12] px-2 py-0.5 font-mono text-[10px] uppercase tracking-wide text-warning">
            updating
          </span>
        )}
      </div>
      <div className="mb-3 space-y-1 font-mono text-[12px] text-text-secondary">
        <div>commit: {data?.commit || '—'}</div>
        <div className="truncate text-text-muted" title={data?.desc}>
          {data?.desc || '—'}
        </div>
      </div>
      <button
        onClick={onUpdate}
        disabled={busy || data?.updating}
        className="w-full rounded-md border border-border-strong bg-surface-raised px-3 py-1.5 text-xs font-medium disabled:opacity-40"
      >
        {busy ? 'Запускаю…' : 'Обновить движок'}
      </button>
      {msg && <div className="mt-2 text-[11px] text-text-muted">{msg}</div>}
    </motion.div>
  )
}
