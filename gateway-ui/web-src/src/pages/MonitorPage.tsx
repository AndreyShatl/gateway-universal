import { useState } from 'react'
import { TopBar } from '../components/TopBar'
import { usePoll } from '../hooks/usePoll'
import { fetchMonitor, fetchNightlyProgress, triggerNightly } from '../lib/api'
import { AnimatedNumber } from '../components/AnimatedNumber'
import { ScanPanel } from '../components/ScanPanel'
import { InfoTip } from '../components/InfoTip'

// п.5 ТЗ: "Полная проверка" — ручной запуск того же самого прохода, что и
// ночной таймер (04:00), кнопкой, когда пользователю самому хочется
// перепроверить домены "прямо сейчас" (например, подозревает, что DPI
// сегодня ослаб перед работой).
function FullCheckPanel() {
  const { data: progress } = usePoll(fetchNightlyProgress, 3000)
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)

  async function onTrigger() {
    setBusy(true)
    setMsg(null)
    try {
      const res = await triggerNightly()
      if (res.error) setMsg('✗ ' + res.error)
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  const running = progress?.running ?? false
  const pct = progress && progress.total > 0 ? Math.round((progress.done / progress.total) * 100) : 0

  return (
    <div className="mb-(--section-gap) rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-3 flex items-center justify-between">
        <div className="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
          Полная проверка
          <InfoTip text="Прогоняет все управляемые домены (курируемые сервисы + автообход) через тот же цикл переоценки, что и ночной таймер в 04:00 — заново подбирает рабочие стратегии по каждому. Не трогает домены, добавленные вручную сверх списков." />
        </div>
        <button
          onClick={onTrigger}
          disabled={busy || running}
          className="rounded-md border border-border-strong bg-surface-raised px-3 py-1 text-xs font-medium disabled:opacity-40"
        >
          {running ? 'Выполняется…' : busy ? 'Запуск…' : 'Запустить'}
        </button>
      </div>
      {msg && <div className="mb-2 text-[11px] text-text-muted">{msg}</div>}
      {running && progress && (
        <>
          <div className="mb-2 h-1.5 overflow-hidden rounded-full bg-surface-raised">
            <div className="h-full bg-accent transition-[width]" style={{ width: `${pct}%` }} />
          </div>
          <div className="mb-2 font-mono text-[11px] text-text-muted">
            {progress.done}/{progress.total} ({pct}%), осталось {progress.remaining}
          </div>
        </>
      )}
      {progress && progress.feed.length > 0 && (
        <div className="max-h-32 overflow-y-auto rounded-md border border-border bg-surface-raised p-2 font-mono text-[10.5px] leading-relaxed text-text-muted">
          {progress.feed.map((line, i) => (
            <div key={i}>{line}</div>
          ))}
        </div>
      )}
    </div>
  )
}

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

  const scheduleAll = [...(data?.reeval_schedule ?? [])].sort((a, b) => a.confidence - b.confidence)
  const [domainFilter, setDomainFilter] = useState('')
  const [engineFilter, setEngineFilter] = useState('')
  const [confFilter, setConfFilter] = useState<number | ''>('')
  const scheduleEngines = Array.from(new Set(scheduleAll.map((e) => e.engine))).sort()
  const schedule = scheduleAll.filter((e) => {
    if (domainFilter.trim() && !e.domain.toLowerCase().includes(domainFilter.trim().toLowerCase())) return false
    if (engineFilter && e.engine !== engineFilter) return false
    if (confFilter !== '' && e.confidence !== confFilter) return false
    return true
  })

  return (
    <div>
      <TopBar
        title="Monitor"
        subtitle="мозг: группы, домены, confidence-гистерезис"
        live={!error}
        hint="'Мозг' — автоматическая система подбора и перепроверки стратегий обхода. Confidence-гистерезис ниже показывает, насколько уверенно подобрана стратегия по каждому домену и когда его перепроверят снова."
      />

      <div className="mb-(--section-gap) grid grid-cols-2 overflow-hidden rounded-[--card-radius] border border-border bg-surface md:grid-cols-4">
        <MetricCell label="Группы" value={data?.brain_totals.groups} />
        <MetricCell label="Домены" value={data?.brain_totals.domains} />
        <MetricCell label="Демоны" value={data?.brain_totals.daemons} />
        <MetricCell label="Память" value={data?.brain_totals.memory_mb} suffix="MB" />
      </div>

      <FullCheckPanel />

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
          <span className="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
            Confidence-гистерезис
            <InfoTip text="Confidence 0/30/70/100 — насколько уверенно домену подобрана стратегия: чем выше, тем реже перепроверка (экономит ресурсы на стабильных доменах, быстрее реагирует на нестабильные)." />
          </span>
          <span className="font-mono text-[11px] text-text-muted">
            {schedule.length}/{scheduleAll.length} доменов
          </span>
        </div>
        <div className="mb-2 flex flex-wrap items-center gap-1.5">
          <input
            value={domainFilter}
            onChange={(e) => setDomainFilter(e.target.value)}
            placeholder="фильтр по домену…"
            className="h-7 w-44 rounded-md border border-border bg-surface-raised px-2 font-mono text-[11px] outline-none focus:border-border-strong"
          />
          <select
            value={engineFilter}
            onChange={(e) => setEngineFilter(e.target.value)}
            className="h-7 rounded-md border border-border bg-surface-raised px-2 font-mono text-[11px] outline-none focus:border-border-strong"
          >
            <option value="">все движки</option>
            {scheduleEngines.map((e) => (
              <option key={e} value={e}>
                {e}
              </option>
            ))}
          </select>
          <div className="flex gap-0.5 rounded-lg border border-border p-0.5">
            {(['', 0, 30, 70, 100] as const).map((c) => (
              <button
                key={c}
                onClick={() => setConfFilter(c)}
                className={`rounded-md px-2 py-1 font-mono text-[10.5px] transition-colors ${
                  confFilter === c ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted'
                }`}
              >
                {c === '' ? 'все' : c}
              </button>
            ))}
          </div>
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
