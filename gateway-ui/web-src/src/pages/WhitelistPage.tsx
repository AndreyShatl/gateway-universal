import { useState } from 'react'
import { motion, AnimatePresence } from 'framer-motion'
import { X } from 'lucide-react'
import { TopBar } from '../components/TopBar'
import { usePoll } from '../hooks/usePoll'
import { fetchWhitelist, addWhitelist, removeWhitelist } from '../lib/api'

const kinds = [
  { value: 'suffix', label: 'suffix' },
  { value: 'exact', label: 'exact' },
]

export function WhitelistPage() {
  const { data, error } = usePoll(fetchWhitelist, 5000)
  const [pattern, setPattern] = useState('')
  const [kind, setKind] = useState('suffix')
  const [note, setNote] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)

  const list = data?.whitelist ?? []

  async function onAdd() {
    if (!pattern.trim()) return
    setBusy(true)
    setMsg(null)
    try {
      const res = await addWhitelist(pattern.trim(), kind, note.trim())
      if (res.error) setMsg('✗ ' + res.error)
      else {
        setMsg('✓ добавлено')
        setPattern('')
        setNote('')
      }
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  async function onRemove(id: number) {
    try {
      await removeWhitelist(id)
    } catch {
      /* список обновится на следующем polling-тике вне зависимости от исхода */
    }
  }

  return (
    <div>
      <TopBar title="Whitelist" subtitle="домены, никогда не идущие в обход" live={!error} />

      <div className="mb-(--section-gap) rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
        <div className="mb-3 flex flex-wrap gap-2">
          <input
            value={pattern}
            onChange={(e) => setPattern(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && onAdd()}
            placeholder="example.com"
            className="h-9 flex-1 min-w-[180px] rounded-md border border-border bg-surface-raised px-3 text-[13px] outline-none focus:border-border-strong"
          />
          <div className="flex gap-0.5 rounded-lg border border-border p-0.5">
            {kinds.map((k) => (
              <button
                key={k.value}
                onClick={() => setKind(k.value)}
                className={`rounded-md px-2.5 py-1.5 font-mono text-[11px] transition-colors ${
                  kind === k.value ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted'
                }`}
              >
                {k.label}
              </button>
            ))}
          </div>
          <input
            value={note}
            onChange={(e) => setNote(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && onAdd()}
            placeholder="заметка (необязательно)"
            className="h-9 flex-1 min-w-[160px] rounded-md border border-border bg-surface-raised px-3 text-[13px] outline-none focus:border-border-strong"
          />
          <button
            onClick={onAdd}
            disabled={busy}
            className="rounded-md border border-border-strong bg-surface-raised px-4 text-[13px] font-medium disabled:opacity-40"
          >
            Добавить
          </button>
        </div>
        {msg && <div className="text-[11px] text-text-muted">{msg}</div>}
      </div>

      <div className="overflow-hidden rounded-[--card-radius] border border-border">
        <AnimatePresence initial={false}>
          {list.map((e) => (
            <motion.div
              key={e.id}
              initial={{ opacity: 0 }}
              animate={{ opacity: 1 }}
              exit={{ opacity: 0 }}
              className="flex items-center justify-between gap-3 border-t border-border bg-surface p-(--card-pad) first:border-t-0"
            >
              <div className="flex min-w-0 items-center gap-3">
                <span className="rounded-md border border-border bg-surface-raised px-2 py-0.5 font-mono text-[10px] uppercase tracking-wide text-text-muted">
                  {e.kind}
                </span>
                <span className="truncate font-mono text-[12.5px]">{e.pattern}</span>
                {e.note && <span className="truncate text-[12px] text-text-muted">— {e.note}</span>}
              </div>
              <div className="flex shrink-0 items-center gap-3">
                <span className="font-mono text-[11px] text-text-muted">{e.source}</span>
                <button onClick={() => onRemove(e.id)} className="text-text-muted hover:text-danger">
                  <X size={14} strokeWidth={2} />
                </button>
              </div>
            </motion.div>
          ))}
        </AnimatePresence>
        {list.length === 0 && (
          <div className="p-7 text-center text-[12.5px] text-text-muted">whitelist пуст</div>
        )}
      </div>
    </div>
  )
}
