import { useState } from 'react'
import { ChevronDown } from 'lucide-react'
import { TopBar } from '../components/TopBar'
import { EngineCard } from '../components/EngineCard'
import { usePoll } from '../hooks/usePoll'
import { fetchMonitor, fetchZapretVersion, fetchCiadpiVersion, fetchZapret2Version, fetchTimeline } from '../lib/api'

function fmtBuildDate(unixSec: string) {
  const n = Number(unixSec)
  if (!n) return '—'
  return new Date(n * 1000).toLocaleString()
}

function UpdateHistory() {
  const { data } = usePoll(fetchTimeline, 15000)
  const [open, setOpen] = useState(false)
  const updates = (data ?? []).filter((ev) => ev.kind === 'engine.updated')

  return (
    <div className="mb-(--section-gap) rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <button
        onClick={() => setOpen((o) => !o)}
        className="flex w-full items-center justify-between text-left"
      >
        <span className="text-[10.5px] uppercase tracking-wide text-text-muted">
          История обновлений {updates.length > 0 && `(${updates.length})`}
        </span>
        <ChevronDown size={14} strokeWidth={2} className={`text-text-muted transition-transform ${open ? 'rotate-180' : ''}`} />
      </button>
      {open && (
        <div className="mt-3">
          {updates.length === 0 ? (
            <div className="text-[12px] text-text-muted">
              Пока пусто — записи появляются начиная с этого обновления панели (задним числом не восстановить).
            </div>
          ) : (
            updates
              .slice()
              .reverse()
              .map((ev, i) => (
                <div key={i} className="flex justify-between border-b border-border py-2 text-[12px] last:border-b-0">
                  <span className="font-mono">{ev.message}</span>
                  <span className="whitespace-nowrap font-mono text-text-muted">
                    {new Date(ev.at).toLocaleString()}
                  </span>
                </div>
              ))
          )}
        </div>
      )}
    </div>
  )
}

export function UpdatesPage() {
  const { data, error } = usePoll(fetchMonitor, 15000)

  return (
    <div>
      <TopBar
        title="Updates"
        subtitle="версии движков и сборки шлюза"
        live={!error}
        hint="Движки zapret/ciadpi/zapret2 обновляются автоматически по воскресеньям (ночной таймер) — кнопка ниже нужна только для немедленного обновления, не ждать до воскресенья."
      />

      <div className="mb-(--section-gap) rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
        <div className="mb-2 text-[10.5px] uppercase tracking-wide text-text-muted">Shattl Gateway</div>
        <div className="flex justify-between border-b border-border py-2.5 text-[12.5px] last:border-b-0">
          <span className="text-text-muted">Build</span>
          <span className="font-mono tabular-nums">{fmtBuildDate(data?.ver || '')}</span>
        </div>
      </div>

      <div className="mb-3.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">Движки обхода</div>
      <div className="mb-(--section-gap) grid grid-cols-[repeat(auto-fit,minmax(var(--grid-min),1fr))] gap-(--grid-gap)">
        <EngineCard engine="zapret" label="zapret" fetcher={fetchZapretVersion} index={0} />
        <EngineCard engine="ciadpi" label="ciadpi" fetcher={fetchCiadpiVersion} index={1} />
        <EngineCard engine="zapret2" label="zapret2" fetcher={fetchZapret2Version} index={2} />
      </div>

      <div className="mb-(--section-gap) rounded-[--card-radius] border border-border bg-surface p-(--card-pad) text-[12px] text-text-muted">
        Обновление движков всегда подтягивает последний коммит апстрима (git fetch + rebuild) —
        отдельного шага "проверить, потом установить" и отката на произвольную старую версию нет.
        Если сборка после обновления не удаётся, скрипт автоматически откатывается на предыдущий
        коммит сам — вручную откатывать не требуется.
      </div>

      <UpdateHistory />
    </div>
  )
}
