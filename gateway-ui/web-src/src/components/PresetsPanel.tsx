import { usePoll } from '../hooks/usePoll'
import { fetchPresets } from '../lib/api'

export function PresetsPanel() {
  const { data } = usePoll(fetchPresets, 10000)
  const presets = data?.presets ?? []
  const trusted = presets.filter((p) => p.trusted).length

  return (
    <div>
      <div className="mb-3.5 flex items-center justify-between">
        <span className="text-[11px] font-medium uppercase tracking-wider text-text-muted">Пресеты zapret/ciadpi</span>
        <span className="font-mono text-[11px] text-text-muted">
          {presets.length} всего · {trusted} доверенных
        </span>
      </div>
      <div className="max-h-[360px] overflow-y-auto rounded-[--card-radius] border border-border">
        {presets.map((p) => (
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
        {presets.length === 0 && <div className="p-7 text-center text-[12.5px] text-text-muted">пусто</div>}
      </div>
    </div>
  )
}
