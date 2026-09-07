import { useState } from 'react'
import { usePoll } from '../hooks/usePoll'
import { fetchPresets } from '../lib/api'
import { InfoTip } from './InfoTip'

export function PresetsPanel() {
  const { data } = usePoll(fetchPresets, 10000)
  const presets = data?.presets ?? []
  const trusted = presets.filter((p) => p.trusted).length

  const [nameFilter, setNameFilter] = useState('')
  const [engineFilter, setEngineFilter] = useState('')
  const [trustedFilter, setTrustedFilter] = useState<'' | 'trusted' | 'custom'>('')

  const engines = Array.from(new Set(presets.map((p) => p.engine))).sort()

  const filtered = presets.filter((p) => {
    if (nameFilter.trim() && !p.name.toLowerCase().includes(nameFilter.trim().toLowerCase())) return false
    if (engineFilter && p.engine !== engineFilter) return false
    if (trustedFilter === 'trusted' && !p.trusted) return false
    if (trustedFilter === 'custom' && p.trusted) return false
    return true
  })

  return (
    <div>
      <div className="mb-3.5 flex flex-wrap items-center justify-between gap-2">
        <span className="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
          Пресеты zapret/ciadpi
          <InfoTip text="Комбинации параметров DPI-обхода, найденные автоматически ('мозгом') или поиском стратегии — trusted означает подтверждена успешными применениями, score — рейтинг эффективности." />
        </span>
        <span className="font-mono text-[11px] text-text-muted">
          {filtered.length}/{presets.length} · {trusted} доверенных
        </span>
      </div>
      <div className="mb-2 flex flex-wrap items-center gap-1.5">
        <input
          value={nameFilter}
          onChange={(e) => setNameFilter(e.target.value)}
          placeholder="фильтр по имени…"
          className="h-7 w-44 rounded-md border border-border bg-surface-raised px-2 font-mono text-[11px] outline-none focus:border-border-strong"
        />
        <select
          value={engineFilter}
          onChange={(e) => setEngineFilter(e.target.value)}
          className="h-7 rounded-md border border-border bg-surface-raised px-2 font-mono text-[11px] outline-none focus:border-border-strong"
        >
          <option value="">все движки</option>
          {engines.map((e) => (
            <option key={e} value={e}>
              {e}
            </option>
          ))}
        </select>
        <div className="flex gap-0.5 rounded-lg border border-border p-0.5">
          {(['', 'trusted', 'custom'] as const).map((v) => (
            <button
              key={v}
              onClick={() => setTrustedFilter(v)}
              className={`rounded-md px-2 py-1 font-mono text-[10.5px] transition-colors ${
                trustedFilter === v ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted'
              }`}
            >
              {v === '' ? 'все' : v}
            </button>
          ))}
        </div>
      </div>
      <div className="max-h-[360px] overflow-y-auto rounded-[--card-radius] border border-border">
        {filtered.map((p) => (
          <div
            key={p.id}
            className="grid grid-cols-[1fr_auto_auto_auto] items-center gap-4 border-t border-border bg-surface p-(--card-pad) text-[12px] first:border-t-0"
          >
            <span className="truncate font-mono">{p.name}</span>
            <span className="text-text-muted">{p.engine}/{p.proto}</span>
            <span className={p.trusted ? 'text-success' : 'text-text-muted'}>{p.trusted ? 'trusted' : 'custom'}</span>
            <span className="whitespace-nowrap font-mono text-[11px] text-text-muted">score {p.score.toFixed(0)}</span>
          </div>
        ))}
        {filtered.length === 0 && <div className="p-7 text-center text-[12.5px] text-text-muted">пусто</div>}
      </div>
    </div>
  )
}
