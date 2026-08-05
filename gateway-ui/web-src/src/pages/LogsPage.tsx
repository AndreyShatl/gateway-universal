import { useState } from 'react'
import { TopBar } from '../components/TopBar'
import { usePoll } from '../hooks/usePoll'
import { fetchLogs, LOGGABLE_SERVICES } from '../lib/api'

export function LogsPage() {
  const [service, setService] = useState<string>(LOGGABLE_SERVICES[0])
  const [lines, setLines] = useState(100)
  const { data, error } = usePoll(() => fetchLogs(service, lines), 4000, [service, lines])

  return (
    <div>
      <TopBar title="Logs" subtitle="journalctl по сервисам шлюза" live={!error} />

      <div className="mb-(--grid-gap) flex flex-wrap items-center gap-2.5">
        <select
          value={service}
          onChange={(e) => setService(e.target.value)}
          className="h-9 rounded-md border border-border bg-surface-raised px-3 text-[13px] outline-none focus:border-border-strong"
        >
          {LOGGABLE_SERVICES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <div className="flex gap-0.5 rounded-lg border border-border p-0.5">
          {[50, 100, 200].map((n) => (
            <button
              key={n}
              onClick={() => setLines(n)}
              className={`rounded-md px-2.5 py-1.5 font-mono text-[11px] transition-colors ${
                lines === n ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted'
              }`}
            >
              {n}
            </button>
          ))}
        </div>
      </div>

      <pre className="max-h-[560px] overflow-auto rounded-[--card-radius] border border-border bg-surface p-(--card-pad) font-mono text-[11.5px] leading-relaxed text-text-secondary">
        {data?.log || 'загрузка…'}
      </pre>
    </div>
  )
}
