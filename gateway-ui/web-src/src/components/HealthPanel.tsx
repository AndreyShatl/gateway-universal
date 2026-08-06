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

// Горизонтальные полоски-прогрессбары (ТЗ 2026-08-06, второй заход — первая
// версия была вертикальной по ядрам, попросили горизонтальные и "в сторону
// процентов" для CPU/RAM/Swap/Disk).
function barColor(pct: number) {
  if (pct >= 85) return 'bg-danger'
  if (pct >= 60) return 'bg-warning'
  return 'bg-accent'
}

function Bar({ pct }: { pct: number }) {
  const p = Math.max(0, Math.min(100, pct))
  return (
    <div className="h-[5px] overflow-hidden rounded-full bg-border">
      <div className={`h-full rounded-full transition-[width] duration-500 ${barColor(p)}`} style={{ width: `${p}%` }} />
    </div>
  )
}

function MetricBar({ label, pct, value }: { label: string; pct?: number; value: string }) {
  return (
    <div className="border-b border-border py-2.5 text-[12.5px] last:border-b-0">
      <div className="mb-1.5 flex justify-between">
        <span className="text-text-muted">{label}</span>
        <span className="font-mono tabular-nums">{value}</span>
      </div>
      <Bar pct={pct ?? 0} />
    </div>
  )
}

// CPU — отдельная полоска на каждый поток (не одна общая), по числу
// элементов per_core_pct с бэкенда.
function CPUBars({ label, cores, value }: { label: string; cores?: number[] | null; value: string }) {
  return (
    <div className="border-b border-border py-2.5 text-[12.5px] last:border-b-0">
      <div className="mb-1.5 flex justify-between">
        <span className="text-text-muted">{label}</span>
        <span className="font-mono tabular-nums">{value}</span>
      </div>
      <div className="space-y-1">
        {cores && cores.length > 0 ? (
          cores.map((pct, i) => <Bar key={i} pct={pct} />)
        ) : (
          <Bar pct={0} />
        )}
      </div>
    </div>
  )
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
      <CPUBars label="CPU" cores={data?.per_core_pct} value={data ? `${Math.round(data.cpu_pct)}%` : '—'} />
      <MetricBar label="RAM" pct={data?.memory_pct} value={fmtUsedOfTotalMB(data?.memory_pct, data?.mem_total_mb)} />
      <MetricBar label="Swap" pct={data?.swap_pct} value={fmtUsedOfTotalMB(data?.swap_pct, data?.swap_total_mb)} />
      <MetricBar label="Disk" pct={data?.disk_pct} value={fmtUsedOfTotalGB(data?.disk_pct, data?.disk_total_gb)} />
      <Field label="Load" value={fmtLoad3(data?.load_avg_1, data?.load_avg_5, data?.load_avg_15)} />
      <Field label="Temperature" value={data?.cpu_temp_c ? `${Math.round(data.cpu_temp_c)}°C` : '—'} />
      <Field label="Uptime" value={fmtUptime(data?.uptime_s)} />
    </div>
  )
}
