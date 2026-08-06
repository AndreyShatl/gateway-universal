import { TopBar } from '../components/TopBar'
import { usePoll } from '../hooks/usePoll'
import { fetchMonitor } from '../lib/api'
import { AnimatedNumber } from '../components/AnimatedNumber'
import { ScanPanel } from '../components/ScanPanel'

function MetricCell({ label, value, suffix }: { label: string; value?: number; suffix?: string }) {
  return (
    <div className="border-r border-border p-(--card-pad) last:border-r-0">
      <div className="mb-2 text-[10.5px] uppercase tracking-wide text-text-muted">{label}</div>
      <div className="font-mono text-[19px] font-medium tracking-tight tabular-nums">
        <AnimatedNumber value={value} decimals={suffix === 'MB' ? 1 : 0} />
        {suffix && <span className="ml-0.5 text-xs font-normal text-text-muted">{suffix}</span>}
      </div>
    </div>
  )
}

const confColor: Record<number, string> = {
  0: 'text-text-muted',
  30: 'text-warning',
  70: 'text-accent',
  100: 'text-success',
}

export function MonitorPage() {
  const { data, error } = usePoll(fetchMonitor, 5000)

  const groupsByEngine = (data?.brain_groups ?? []).reduce<Record<string, NonNullable<typeof data>['brain_groups']>>(
    (acc, g) => {
      ;(acc[g.engine] ??= []).push(g)
      return acc
    },
    {},
  )

  const schedule = [...(data?.reeval_schedule ?? [])].sort((a, b) => a.confidence - b.confidence)

  return (
    <div>
      <TopBar title="Monitor" subtitle="мозг: группы, домены, confidence-гистерезис" live={!error} />

      <div className="mb-(--section-gap) grid grid-cols-2 overflow-hidden rounded-[--card-radius] border border-border bg-surface md:grid-cols-4">
        <MetricCell label="Группы" value={data?.brain_totals.groups} />
        <MetricCell label="Домены" value={data?.brain_totals.domains} />
        <MetricCell label="Демоны" value={data?.brain_totals.daemons} />
        <MetricCell label="Память" value={data?.brain_totals.memory_mb} suffix="MB" />
      </div>

      <div className="mb-(--section-gap)">
        <div className="mb-3.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">Группы по движку</div>
        <div className="grid grid-cols-[repeat(auto-fit,minmax(var(--grid-min),1fr))] gap-(--grid-gap)">
          {Object.entries(groupsByEngine).map(([engine, groups]) => (
            <div key={engine} className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
              <div className="mb-3 flex items-center justify-between">
                <span className="text-sm font-medium">{engine}</span>
                <span className="font-mono text-[11px] text-text-muted">{groups?.length ?? 0} групп</span>
              </div>
              {groups?.map((g) => (
                <div key={g.group_id} className="flex justify-between border-b border-border py-2 text-[12px] last:border-b-0">
                  <span className="font-mono text-text-secondary">{g.group_id}</span>
                  <span className="font-mono text-text-muted">{g.count} доменов</span>
                </div>
              ))}
            </div>
          ))}
          {Object.keys(groupsByEngine).length === 0 && (
            <div className="text-[12.5px] text-text-muted">нет активных групп</div>
          )}
        </div>
      </div>

      <div>
        <div className="mb-3.5 flex items-center justify-between">
          <span className="text-[11px] font-medium uppercase tracking-wider text-text-muted">Confidence-гистерезис</span>
          <span className="font-mono text-[11px] text-text-muted">{schedule.length} доменов</span>
        </div>
        <div className="max-h-[420px] overflow-y-auto rounded-[--card-radius] border border-border">
          {schedule.map((e) => (
            <div
              key={e.domain}
              className="grid grid-cols-[1fr_auto_auto_auto] items-center gap-4 border-t border-border bg-surface p-(--card-pad) text-[12px] first:border-t-0"
            >
              <span className="truncate font-mono">{e.domain}</span>
              <span className="text-text-muted">{e.engine}</span>
              <span className={`font-mono ${confColor[e.confidence] ?? 'text-text-muted'}`}>{e.confidence}</span>
              <span className="whitespace-nowrap font-mono text-[11px] text-text-muted">{e.next_reeval_at || '—'}</span>
            </div>
          ))}
          {schedule.length === 0 && <div className="p-7 text-center text-[12.5px] text-text-muted">пусто</div>}
        </div>
      </div>

      <div className="mt-(--section-gap)">
        <ScanPanel />
      </div>
    </div>
  )
}
