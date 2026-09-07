import { usePoll } from '../hooks/usePoll'
import { TopBar } from '../components/TopBar'
import { AnimatedNumber } from '../components/AnimatedNumber'
import {
  fetchDailyDigest,
  fetchRouteState,
  fetchShadowReport,
  type DailyDigestResponse,
  type RouteStateResponse,
  type ShadowReportResponse,
} from '../lib/api'

function fmtTime(s: string | null | undefined): string {
  if (!s) return '—'
  try {
    return new Date(s).toLocaleString('ru-RU', { hour: '2-digit', minute: '2-digit', day: '2-digit', month: '2-digit' })
  } catch {
    return s
  }
}

// Раздел 28 схемы: пользователь не должен видеть кухню — только состояние и
// «мозг оптимизирует сам». Распределение здесь — по НАЗНАЧЕНИЯМ (доменам),
// не по байтам (учёт трафика — отдельная задача ТЗ).
function RouteDistribution({ totals }: { totals: NonNullable<RouteStateResponse['totals']> }) {
  const local = totals.local_zapret + totals.local_ciadpi + totals.local_zapret2
  const vps = totals.vps_auto_domain + totals.vps_static
  const sum = local + vps || 1
  const seg = [
    { label: 'LOCAL (DPI-обход)', n: local, color: 'bg-emerald-500' },
    { label: 'VPS (туннель)', n: vps, color: 'bg-sky-500' },
  ]
  return (
    <div>
      <div className="mb-3 flex h-3 overflow-hidden rounded-full bg-surface-raised">
        {seg.map((s) => (
          <div key={s.label} className={s.color} style={{ width: `${(s.n / sum) * 100}%` }} title={`${s.label}: ${s.n}`} />
        ))}
      </div>
      <div className="flex flex-wrap gap-x-5 gap-y-1 text-[12px] text-text-muted">
        {seg.map((s) => (
          <span key={s.label} className="flex items-center gap-1.5">
            <span className={`inline-block h-2 w-2 rounded-full ${s.color}`} />
            {s.label}: <b className="text-text-secondary">{s.n}</b> ({Math.round((s.n / sum) * 100)}%)
          </span>
        ))}
        <span className="text-text-muted/70">
          + {totals.vps_auto_ip} IP-записей автообхода{totals.conflicts > 0 ? ` · ⚠ конфликтов двойного учёта: ${totals.conflicts}` : ''}
        </span>
      </div>
    </div>
  )
}

function DigestCard({ d }: { d: DailyDigestResponse }) {
  const sys = d.system ?? ({} as DailyDigestResponse['system'])
  const brainOk = brainAlive(d)
  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-3 flex items-center justify-between">
        <span className="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
          Дайджест за {d.window}
        </span>
        <span className="font-mono text-[11px] text-text-muted">{fmtTime(d.generated)}</span>
      </div>
      <div className="mb-4 grid grid-cols-2 gap-3 md:grid-cols-4">
        <div>
          <div className="text-[11px] text-text-muted">Пробы мозга</div>
          <div className="text-lg font-semibold"><AnimatedNumber value={d.brain?.probes ?? 0} /></div>
          <div className="text-[11px] text-text-muted">
            {(d.brain?.success ?? 0)} ок / {(d.brain?.fail ?? 0)} провал · {d.brain?.domains ?? 0} доменов
          </div>
        </div>
        <div>
          <div className="text-[11px] text-text-muted">Тень: R1 персист.</div>
          <div className={`text-lg font-semibold ${d.shadow?.r1_still ? 'text-amber-400' : 'text-emerald-400'}`}>
            {d.shadow?.r1_still ?? 0}
          </div>
          <div className="text-[11px] text-text-muted">долечено {d.shadow?.r1_healed ?? 0} · открыто {d.shadow?.r1_open ?? 0}</div>
        </div>
        <div>
          <div className="text-[11px] text-text-muted">R2: VPS→LOCAL</div>
          <div className="text-lg font-semibold"><AnimatedNumber value={d.shadow?.r2_moved ?? 0} /></div>
          <div className="text-[11px] text-text-muted">остались на VPS: {d.shadow?.r2_still ?? 0}</div>
        </div>
        <div>
          <div className="text-[11px] text-text-muted">Система</div>
          <div className="text-lg font-semibold">
            <span className={sys.disk_avail_pct && sys.disk_avail_pct < 15 ? 'text-red-400' : ''}>{sys.disk_avail_pct ?? '—'}%</span>
            <span className="ml-1 text-xs font-normal text-text-muted">диск своб.</span>
          </div>
          <div className="text-[11px] text-text-muted">
            память {sys.mem_avail_mb ?? '—'}МБ · очередь {sys.queue_len ?? 0}
          </div>
        </div>
      </div>
      <div className="flex flex-wrap gap-2 text-[11px]">
        <span className={`rounded-md border px-2 py-0.5 ${brainOk ? 'border-emerald-500/40 text-emerald-400' : 'border-red-500/40 text-red-400'}`}>
          {brainOk ? '● мозг жив' : '● мозг: проблема'}
        </span>
        {sys.failed_units && sys.failed_units.length > 0 && (
          <span className="rounded-md border border-red-500/40 px-2 py-0.5 text-red-400">failed: {sys.failed_units.join(', ')}</span>
        )}
      </div>
      {d.notes && d.notes.length > 0 && (
        <div className="mt-3 space-y-1">
          {d.notes.map((n, i) => (
            <div key={i} className="rounded-md border border-amber-500/30 bg-amber-500/5 px-2.5 py-1.5 text-[12px] text-amber-300">
              ⚠ {n}
            </div>
          ))}
        </div>
      )}
      {d.hint && <div className="mt-2 text-[12px] text-text-muted">{d.hint}</div>}
    </div>
  )
}

function brainAlive(d: DailyDigestResponse): boolean {
  const sys = d.system
  if (!sys) return false
  const probesOk = (d.brain?.probes ?? 0) > 0
  return Boolean(sys.brain_worker_active) && probesOk
}

function ShadowCard({ rep }: { rep: ShadowReportResponse }) {
  const still = (rep.r1 ?? []).filter((x) => x.outcome === 'still_open')
  const healed = (rep.r1 ?? []).length - still.length
  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-3 flex items-center justify-between">
        <span className="flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
          Сверка тени с реальностью
        </span>
        <span className="font-mono text-[11px] text-text-muted">{fmtTime(rep.generated)}</span>
      </div>
      {rep.hint && <div className="text-[12px] text-text-muted">{rep.hint}</div>}
      <div className="mb-3 grid grid-cols-2 gap-3 text-[12px] md:grid-cols-4">
        <div><div className="text-lg font-semibold text-amber-400">{still.length}</div>R1 персистентные (движок не покрывает IP)</div>
        <div><div className="text-lg font-semibold text-emerald-400">{healed}</div>R1 долечено refresh-ips</div>
        <div><div className="text-lg font-semibold text-emerald-400">{(rep.r2 ?? []).filter((x) => x.outcome === 'moved_to_local').length}</div>R2 переехали на LOCAL</div>
        <div><div className="text-lg font-semibold text-sky-400">{(rep.r2 ?? []).filter((x) => x.outcome === 'still_vps').length}</div>R2 остались на VPS</div>
      </div>
      {still.length > 0 && (
        <div className="mb-3">
          <div className="mb-1.5 text-[11px] uppercase tracking-wider text-text-muted">Персистентные R1</div>
          <div className="flex flex-wrap gap-1.5">
            {still.map((x) => (
              <span key={x.domain} className="rounded-md border border-amber-500/30 bg-amber-500/5 px-2 py-0.5 font-mono text-[11px] text-amber-300">
                {x.domain}
                {x.days_open > 0 ? ` · ${x.days_open}д` : ''}
              </span>
            ))}
          </div>
        </div>
      )}
      {(rep.r2 ?? []).length > 0 && (
        <div className="overflow-x-auto">
          <div className="mb-1.5 text-[11px] uppercase tracking-wider text-text-muted">R2 — исходы кандидатов «VPS → LOCAL»</div>
          <table className="w-full text-[12px]">
            <tbody>
              {(rep.r2 ?? []).map((x) => (
                <tr key={x.domain} className="border-b border-border last:border-b-0">
                  <td className="py-1.5 font-mono text-text-secondary">{x.domain}</td>
                  <td className="py-1.5">
                    <span className={`rounded px-1.5 py-0.5 text-[11px] ${
                      x.outcome === 'moved_to_local' ? 'bg-emerald-500/15 text-emerald-400'
                      : x.outcome === 'still_vps' ? 'bg-sky-500/15 text-sky-400'
                      : 'bg-surface-raised text-text-muted'}`}>
                      {x.outcome}
                    </span>
                  </td>
                  <td className="py-1.5 text-text-muted">{x.route_now}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {(rep.notes ?? []).map((n, i) => (
        <div key={i} className="mt-2 text-[11.5px] text-text-muted">· {n}</div>
      ))}
    </div>
  )
}

export function ObservePage() {
  const { data: digest } = usePoll(fetchDailyDigest, 60_000)
  const { data: state } = usePoll(fetchRouteState, 60_000)
  const { data: shadow } = usePoll(fetchShadowReport, 120_000)

  return (
    <div>
      <TopBar
        title="Observe"
        subtitle="Shattl Brain · тень и сверка (Stage 5)"
        live={Boolean(digest)}
        hint="Теневой мозг наблюдает за всеми назначениями и сверяет свои решения с реальными действиями боевого мозга. Распределение — по доменам, не по трафику."
      />
      <div className="mb-(--section-gap)">
        {digest ? <DigestCard d={digest} /> : (
          <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad) text-[12.5px] text-text-muted">
            Дайджест ещё не создан — первый сформируется в 05:30 (или обновите позже).
          </div>
        )}
      </div>
      {state?.totals && (
        <div className="mb-(--section-gap) rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
          <div className="mb-3 flex items-center justify-between">
            <span className="text-[11px] font-medium uppercase tracking-wider text-text-muted">Маршруты назначений</span>
            <span className="font-mono text-[11px] text-text-muted">снимок {fmtTime(state.generated)}</span>
          </div>
          <RouteDistribution totals={state.totals} />
          {state.hint && <div className="mt-2 text-[12px] text-text-muted">{state.hint}</div>}
        </div>
      )}
      {shadow && !shadow.hint && <ShadowCard rep={shadow} />}
      {shadow?.hint && (
        <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad) text-[12.5px] text-text-muted">
          {shadow.hint}
        </div>
      )}
    </div>
  )
}
