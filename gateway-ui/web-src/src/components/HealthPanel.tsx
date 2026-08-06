import { usePoll } from '../hooks/usePoll'
import { fetchHostMetrics } from '../lib/api'
import { InfoTip } from './InfoTip'

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between border-b border-border py-2.5 text-[12.5px] last:border-b-0">
      <span className="text-text-muted">{label}</span>
      <span className="font-mono tabular-nums">{value}</span>
    </div>
  )
}

// Тот же формат, что и gmp-server/web-src/src/lib/format.ts fmtUptime.
function fmtUptime(s?: number) {
  if (s === undefined) return '—'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  if (d > 0) return `${d}d ${h}h`
  const m = Math.floor((s % 3600) / 60)
  return `${h}h ${m}m`
}

// Форматы ниже намеренно 1:1 повторяют gmp-server/web-src/src/lib/format.ts
// (fmtUsedOfTotal/fmtUsedOfTotalGB/fmtLoad3/fmtCapacity) — Dashboard и эта
// панель показывают ОДНИ И ТЕ ЖЕ метрики одного и того же шлюза, разное
// форматирование одного числа в двух местах выглядело как расхождение.
function fmtMB(mb?: number) {
  if (mb === undefined) return '—'
  if (mb >= 1024) return `${(mb / 1024).toFixed(1)} GB`
  return `${Math.round(mb)} MB`
}

function fmtUsedOfTotalMB(pct?: number, totalMB?: number) {
  if (pct === undefined) return '—'
  if (!totalMB) return `${Math.round(pct)}%`
  return `${fmtMB((pct / 100) * totalMB)} / ${fmtMB(totalMB)}`
}

function fmtUsedOfTotalGB(pct?: number, totalGB?: number) {
  if (pct === undefined) return '—'
  if (!totalGB) return `${Math.round(pct)}%`
  return `${((pct / 100) * totalGB).toFixed(1)} / ${totalGB.toFixed(1)} GB`
}

function fmtLoad3(l1?: number, l5?: number, l15?: number) {
  if (l1 === undefined) return '—'
  return [l1, l5 ?? 0, l15 ?? 0].map((v) => v.toFixed(2)).join(' ')
}

function fmtCapacity(cores?: number, mhz?: number) {
  if (!cores && !mhz) return ''
  const parts: string[] = []
  if (cores) parts.push(`${cores} core${cores > 1 ? 's' : ''}`)
  if (mhz) parts.push(`${(mhz / 1000).toFixed(1)}GHz`)
  return parts.join(' @ ')
}

export function HealthPanel() {
  const { data } = usePoll(fetchHostMetrics, 5000)

  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-2 flex items-center justify-between">
        <div className="flex items-center gap-1.5 text-[10.5px] uppercase tracking-wide text-text-muted">
          Health
          <InfoTip text="Аппаратные метрики самого шлюза (не VPS) — собираются локально, читаются напрямую из /proc, не зависят от других сервисов." />
        </div>
        <span className="font-mono text-[10.5px] text-text-muted">{fmtCapacity(data?.cpu_cores, data?.cpu_mhz)}</span>
      </div>
      <Field label="CPU" value={data ? `${Math.round(data.cpu_pct)}%` : '—'} />
      <Field label="RAM" value={fmtUsedOfTotalMB(data?.memory_pct, data?.mem_total_mb)} />
      <Field label="Swap" value={fmtUsedOfTotalMB(data?.swap_pct, data?.swap_total_mb)} />
      <Field label="Disk" value={fmtUsedOfTotalGB(data?.disk_pct, data?.disk_total_gb)} />
      <Field label="Load" value={fmtLoad3(data?.load_avg_1, data?.load_avg_5, data?.load_avg_15)} />
      <Field label="Temperature" value={data?.cpu_temp_c ? `${Math.round(data.cpu_temp_c)}°C` : '—'} />
      <Field label="Uptime" value={fmtUptime(data?.uptime_s)} />
    </div>
  )
}
