import { usePoll } from '../hooks/usePoll'
import { fetchHostMetrics } from '../lib/api'

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between border-b border-border py-2.5 text-[12.5px] last:border-b-0">
      <span className="text-text-muted">{label}</span>
      <span className="font-mono tabular-nums">{value}</span>
    </div>
  )
}

function fmtUptime(s?: number) {
  if (!s) return '—'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

function fmtUsedOfTotalMB(pct?: number, totalMB?: number) {
  if (pct === undefined) return '—'
  if (!totalMB) return `${Math.round(pct)}%`
  const usedGB = (pct / 100) * (totalMB / 1024)
  return `${Math.round(pct)}% (${usedGB.toFixed(1)}/${(totalMB / 1024).toFixed(1)} ГБ)`
}

function fmtUsedOfTotalGB(pct?: number, totalGB?: number) {
  if (pct === undefined) return '—'
  if (!totalGB) return `${Math.round(pct)}%`
  return `${Math.round(pct)}% (${((pct / 100) * totalGB).toFixed(1)}/${totalGB.toFixed(1)} ГБ)`
}

function fmtLoad3(l1?: number, l5?: number, l15?: number) {
  if (l1 === undefined) return '—'
  return `${l1.toFixed(2)} / ${(l5 ?? 0).toFixed(2)} / ${(l15 ?? 0).toFixed(2)}`
}

export function HealthPanel() {
  const { data } = usePoll(fetchHostMetrics, 5000)

  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-2 flex items-center justify-between">
        <div className="text-[10.5px] uppercase tracking-wide text-text-muted">Health</div>
        {data?.cpu_cores ? (
          <span className="font-mono text-[10.5px] text-text-muted">
            {data.cpu_cores} ядер · {(data.cpu_mhz / 1000).toFixed(1)} ГГц
          </span>
        ) : null}
      </div>
      <Field label="CPU" value={data ? `${Math.round(data.cpu_pct)}%` : '—'} />
      <Field label="RAM" value={fmtUsedOfTotalMB(data?.memory_pct, data?.mem_total_mb)} />
      <Field label="Swap" value={fmtUsedOfTotalMB(data?.swap_pct, data?.swap_total_mb)} />
      <Field label="Disk" value={fmtUsedOfTotalGB(data?.disk_pct, data?.disk_total_gb)} />
      <Field label="Temperature" value={data?.cpu_temp_c ? `${Math.round(data.cpu_temp_c)}°C` : '—'} />
      <Field label="Load Average" value={fmtLoad3(data?.load_avg_1, data?.load_avg_5, data?.load_avg_15)} />
      <Field label="Uptime" value={fmtUptime(data?.uptime_s)} />
    </div>
  )
}
