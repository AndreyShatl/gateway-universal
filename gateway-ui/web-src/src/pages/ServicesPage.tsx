import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { TopBar } from '../components/TopBar'
import { usePoll } from '../hooks/usePoll'
import { fetchServicesDetail, restartService, stopService, type ServiceDetail } from '../lib/api'

// Тот же формат, что и gmp-server/web-src/src/lib/format.ts fmtUptime.
function fmtUptime(s: number) {
  if (!s && s !== 0) return '—'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  if (d > 0) return `${d}d ${h}h`
  const m = Math.floor((s % 3600) / 60)
  return `${h}h ${m}m`
}

const stateDot: Record<string, string> = {
  active: 'bg-success',
  failed: 'bg-danger',
}

// Та же формулировка, что и Overview/gmp-server Dashboard — не сырой
// systemd-статус (active/failed/inactive).
const stateLabel: Record<string, string> = {
  active: 'Running',
  failed: 'Failed',
}
function fmtState(state: string) {
  return stateLabel[state] ?? 'Stopped'
}

function ServiceCard({ svc }: { svc: ServiceDetail }) {
  const [busy, setBusy] = useState<'restart' | 'stop' | null>(null)
  const navigate = useNavigate()

  async function run(action: 'restart' | 'stop') {
    setBusy(action)
    try {
      // следующий тик usePoll (раз в 5с) подхватит новое состояние сам,
      // отдельный триггер обновления не нужен
      await (action === 'restart' ? restartService(svc.name) : stopService(svc.name))
    } finally {
      setBusy(null)
    }
  }

  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-3 flex items-center justify-between">
        <span className="text-sm font-medium">{svc.name}</span>
        <span className={`flex items-center gap-1.5 font-mono text-[11px] ${svc.state === 'active' ? 'text-success' : 'text-danger'}`}>
          <span className={`h-[7px] w-[7px] rounded-full ${stateDot[svc.state] ?? 'bg-text-muted'}`} />
          {fmtState(svc.state)}
        </span>
      </div>
      <div className="mb-3 grid grid-cols-3 gap-2 text-[11.5px]">
        <div>
          <div className="text-text-muted">Uptime</div>
          <div className="font-mono tabular-nums">{fmtUptime(svc.uptime_s)}</div>
        </div>
        <div>
          <div className="text-text-muted">CPU</div>
          <div className="font-mono tabular-nums">{svc.cpu_pct.toFixed(1)}%</div>
        </div>
        <div>
          <div className="text-text-muted">RAM</div>
          <div className="font-mono tabular-nums">{svc.memory_mb.toFixed(0)} MB</div>
        </div>
      </div>
      <div className="flex gap-2">
        <button
          onClick={() => run('restart')}
          disabled={busy !== null}
          className="flex-1 rounded-md border border-border px-2.5 py-1.5 text-[11.5px] text-text-secondary hover:border-border-strong hover:text-text disabled:opacity-50"
        >
          {busy === 'restart' ? 'Restarting…' : 'Restart'}
        </button>
        {svc.stoppable && (
          <button
            onClick={() => run('stop')}
            disabled={busy !== null}
            className="flex-1 rounded-md border border-border px-2.5 py-1.5 text-[11.5px] text-text-secondary hover:border-border-strong hover:text-text disabled:opacity-50"
          >
            {busy === 'stop' ? 'Stopping…' : 'Stop'}
          </button>
        )}
        {svc.loggable && (
          <button
            onClick={() => navigate(`/logs?service=${encodeURIComponent(svc.name)}`)}
            className="flex-1 rounded-md border border-border px-2.5 py-1.5 text-[11.5px] text-text-secondary hover:border-border-strong hover:text-text"
          >
            Logs
          </button>
        )}
      </div>
    </div>
  )
}

export function ServicesPage() {
  const { data, error } = usePoll(fetchServicesDetail, 5000)

  return (
    <div>
      <TopBar title="Services" subtitle="управление демонами шлюза" live={!error} />
      <div className="grid grid-cols-[repeat(auto-fit,minmax(var(--grid-min),1fr))] gap-(--grid-gap)">
        {data?.map((svc) => <ServiceCard key={svc.name} svc={svc} />)}
        {!data && <div className="text-[12.5px] text-text-muted">загрузка…</div>}
      </div>
    </div>
  )
}
