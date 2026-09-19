import { useEffect, useRef, useState } from 'react'
import { Wand2 } from 'lucide-react'
import { TopBar } from '../components/TopBar'
import { PresetsPanel } from '../components/PresetsPanel'
import { VPSDomainsPanel } from '../components/VPSDomainsPanel'
import { AutorouteStatsPanel } from '../components/AutorouteStatsPanel'
import { InfoTip } from '../components/InfoTip'
import { usePoll } from '../hooks/usePoll'
import {
  fetchDomains,
  addDomain,
  fetchServices,
  saveServices,
  fetchScanStatus,
  fetchMonitor,
  fetchPinVPSJob,
  type ZService,
  type PinVPSJob,
  serviceAutoLocal,
  fetchDPIReadiness,
} from '../lib/api'

function SectionHead({ title, count, hint }: { title: string; count?: number; hint?: string }) {
  return (
    <div className="mb-3.5 flex items-center justify-between">
      <h2 className="m-0 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
        {title}
        {hint && <InfoTip text={hint} />}
      </h2>
      {count !== undefined && <span className="font-mono text-[11px] text-text-muted">{count}</span>}
    </div>
  )
}

// quic_fallback — не список доменов, а спец-обработчик "прочего" UDP/443
// QUIC-трафика для сайтов, которых нет в остальных сервисах. 0 доменов —
// правильно и всегда так будет.
const serviceHints: Record<string, string> = {
  quic_fallback: 'Не список доменов — обрабатывает весь остальной QUIC-трафик (UDP/443), не попавший в другие сервисы. 0 доменов здесь — норма, не баг.',
}

// T-режимы-и-готовность (2026-09-18, семантика владельца): dpi = локальный
// DPI-обход, vps = всё через туннель (DPI-подложка проверяется ночью,
// без переключений), direct = без обхода, auto = мозг управляет миксом
// dpi/vps/direct с двойной подложкой и гистерезисом.
// VPS ПЕРВОЙ: раньше (старый toggle) vps была второй кнопкой — смена порядка
// без предупреждения заставила владельца кликнуть по привычке Auto вместо VPS
// (живой инцидент 2026-09-18 21:55). Привычное место — святое.
const modes = [
  { value: 'vps', label: 'VPS' },
  { value: 'dpi', label: 'DPI' },
  { value: 'auto', label: 'Auto' },
  { value: 'direct', label: 'Direct' },
]

// Порог для "Авто": если хотя бы половина доменов сервиса проходит через
// zapret (по результатам blockcheck) — оставляем zapret на весь сервис,
// иначе форсируем vps. Per-domain роутинг внутри одного сервиса архитектурно
// не поддержан (zapret-services.json хранит один mode на сервис целиком) —
// сознательно не стали городить это ради авто-режима, majority vote проще
// и достаточно для решения "этот сервис в целом легко обходится или нет".
const AUTO_ZAPRET_THRESHOLD = 0.5

function ModeToggle({
  value,
  onChange,
  onAuto,
  autoBusy,
}: {
  value: string
  onChange: (v: string) => void
  onAuto: () => void
  autoBusy: boolean
}) {
  const current = value === 'zapret' ? 'dpi' : (value || 'auto')
  return (
    <div className="flex gap-0.5 rounded-lg border border-border p-0.5">
      {modes.map((m) => (
        <button
          key={m.value}
          onClick={() => onChange(m.value)}
          className={`rounded-md px-2 py-1 font-mono text-[10.5px] transition-colors ${
            current === m.value ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted'
          }`}
        >
          {m.label}
        </button>
      ))}
      <button
        onClick={onAuto}
        disabled={autoBusy}
        title="Прогнать домены через blockcheck и подобрать zapret/vps автоматически"
        className="flex items-center gap-1 rounded-md px-2 py-1 font-mono text-[10.5px] text-text-muted transition-colors hover:text-text disabled:opacity-40"
      >
        <Wand2 size={11} strokeWidth={2} className={autoBusy ? 'animate-pulse' : ''} />
        {autoBusy ? '…' : 'auto'}
      </button>
    </div>
  )
}

// Относительное время для пометки "подобрано автоматически" — тот же
// формат, что и в остальном интерфейсе (fmtAgo-подобный, но локальный:
// отдельного общего lib/format.ts в этом репо нет).
function fmtAgo(iso: string) {
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'только что'
  if (s < 3600) return `${Math.floor(s / 60)} мин назад`
  if (s < 86400) return `${Math.floor(s / 3600)} ч назад`
  return `${Math.floor(s / 86400)} дн назад`
}

function ServiceRow({
  svc,
  onModeChange,
  onAuto,
  autoBusy,
  bypassedDomains,
  readyDomains,
}: {
  svc: ZService
  onModeChange: (id: string, mode: string) => void
  onAuto: (svc: ZService) => void
  autoBusy: boolean
  bypassedDomains: Set<string>
  readyDomains: Set<string>
}) {
  // п.15 ТЗ: badge сервиса — это mode из zapret-services.json, а не то, что
  // реально происходит по доменам. Домен из vps-сервиса может уже успешно
  // обходиться через ciadpi/zapret2 индивидуально (brain сам так решил по
  // per-domain стратегиям) — тогда badge "vps" вводит в заблуждение. Считаем
  // фактическое пересечение с активными brain-группами и показываем как
  // подсказку, не трогая сам механизм хранения одного mode на сервис целиком.
  const bypassedCount = svc.mode === 'vps' ? svc.domains.filter((d) => bypassedDomains.has(d)).length : 0
  return (
    <div className="border-b border-border py-3 text-[12.5px] last:border-b-0">
      <div className="flex items-center justify-between">
        <div>
          <div className="flex items-center gap-1.5 font-medium">
            {svc.name}
            {serviceHints[svc.id] && <InfoTip text={serviceHints[svc.id]} />}
            {svc.auto_at && (
              <span
                className="flex items-center gap-1 rounded-md bg-[--accent-dim] px-1.5 py-0.5 font-mono text-[9.5px] uppercase tracking-wide text-accent"
                title={`Режим подобран кнопкой auto ${new Date(svc.auto_at).toLocaleString()}`}
              >
                <Wand2 size={9} strokeWidth={2} />
                auto {fmtAgo(svc.auto_at)}
              </span>
            )}
          </div>
          <div className="font-mono text-[11px] text-text-muted">{svc.domains.length} domains</div>
        </div>
        <div className="flex items-center gap-2">
          <span className="font-mono text-[10.5px] text-text-muted" title="Готовые DPI-стратегии по ночному кэшу (мгновенное переключение доступно только для них)">DPI готово: {svc.domains.filter((d) => readyDomains.has(d)).length}/{svc.domains.length}</span>
          <ModeToggle value={svc.mode} onChange={(mode) => onModeChange(svc.id, mode)} onAuto={() => onAuto(svc)} autoBusy={autoBusy} />
        </div>
      </div>
      {bypassedCount > 0 && (
        <div className="mt-2 flex items-center gap-1.5 text-[11px] text-text-muted">
          <InfoTip text="Brain индивидуально подобрал рабочую zapret/zapret2/ciadpi-стратегию для части доменов этого сервиса, хотя у самого сервиса режим 'vps'. Реальный трафик по ним уже идёт в обход VPS — можно понизить режим сервиса на zapret, если это устраивает по остальным доменам." />
          {bypassedCount}/{svc.domains.length} доменов уже реально обходится (не через VPS)
        </div>
      )}
    </div>
  )
}

export function DomainsPage() {
  const { data: domainsData } = usePoll(fetchDomains, 5000)
  const { data: servicesData, error: servicesError } = usePoll(fetchServices, 5000)
  const { data: monitorData } = usePoll(fetchMonitor, 10000)
  const { data: readiness } = usePoll(fetchDPIReadiness, 30000)
  const bypassedDomains = new Set((monitorData?.brain_groups ?? []).flatMap((g) => g.domains))
  const readyDomains = new Set((readiness?.entries ?? []).filter((e) => e.ready).map((e) => e.domain))
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)
  const [localServices, setLocalServices] = useState<ZService[] | null>(null)
  const [saving, setSaving] = useState(false)
  const [autoServiceId, setAutoServiceId] = useState<string | null>(null)
  const autoCancelled = useRef(false)
  // T-vps-pin (2026-08-16): id сервисов -> прогресс фоновой работы, стартующей
  // на шлюзе, когда сохранение переводит mode на/с "vps" (чистка старых DPI-
  // групп / постановка на перепроверку, см. zapret.go handleServices).
  const [pinJobs, setPinJobs] = useState<Record<string, PinVPSJob | null>>({})

  const services = localServices ?? servicesData?.services ?? []

  // T-vps-pin-rediscover (2026-08-16): раньше pinJobs узнавался только из
  // ответа на POST /api/zapret/services в момент сохранения — если
  // страница перезагружалась/переоткрывалась, пока фоновая работа на
  // шлюзе ещё шла (перебор десятков доменов может занимать долго), прогресс
  // -бар просто пропадал, хотя сама работа продолжалась нормально. Один раз
  // после первой загрузки списка сервисов — переспрашиваем каждый на
  // предмет активной job'ы, чтобы прогресс-бар появился заново.
  const rediscoveredPinJobs = useRef(false)
  useEffect(() => {
    if (rediscoveredPinJobs.current || !servicesData) return
    rediscoveredPinJobs.current = true
    ;(async () => {
      const results = await Promise.all(
        servicesData.services.map(async (svc) => {
          try {
            const { job } = await fetchPinVPSJob(svc.id)
            return [svc.id, job] as const
          } catch {
            return [svc.id, null] as const
          }
        }),
      )
      const active = Object.fromEntries(results.filter(([, job]) => job !== null))
      if (Object.keys(active).length > 0) {
        setPinJobs((prev) => ({ ...prev, ...active }))
      }
    })()
  }, [servicesData])

  async function onAdd() {
    if (!input.trim()) return
    setBusy(true)
    setMsg(null)
    try {
      const res = await addDomain(input.trim())
      if (res.error) setMsg('✗ ' + res.error)
      else {
        setMsg('✓ добавлено')
        setInput('')
      }
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  function onModeChange(id: string, mode: string) {
    // Ручное переключение — снимаем пометку "подобрано автоматически"
    // (см. auto_at), раз человек сам явно выбрал режим.
    const next = services.map((s) => (s.id === id ? { ...s, mode, auto_at: undefined } : s))
    setLocalServices(next)
  }

  // "Авто": прогоняет домены сервиса через blockcheck (тот же движок, что и
  // ручной "Поиск стратегии" на Monitor), затем сам решает zapret/vps по
  // majority vote (см. AUTO_ZAPRET_THRESHOLD) и подставляет режим — дальше
  // всё равно требуется "Сохранить и применить", ничего не применяется
  // молча за спиной пользователя.
  // T-auto-local (2026-09-17): кнопка «Авто» больше не делает blockcheck
  // majority-vote — она ставит все домены сервиса в очередь мозга: фоновый
  // параллельный поиск (4 воркера), переключение домена на DPI только при
  // подтверждённом обходе, с VPS-подложкой и без разрыва соединений.
  async function onAuto(svc: ZService) {
    if (svc.domains.length === 0) {
      setMsg('✗ у сервиса нет доменов для проверки')
      return
    }
    setMsg(null)
    try {
      const res = await serviceAutoLocal(svc.id)
      setMsg('✓ ' + res.message + ` (поставлено: ${res.enqueued})`)
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    }
  }

  useEffect(() => {
    if (!autoServiceId) return
    const svcId = autoServiceId

    async function poll() {
      let st
      try {
        st = await fetchScanStatus()
      } catch (e) {
        if (autoCancelled.current) return
        setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
        setAutoServiceId(null)
        return
      }
      if (autoCancelled.current) return
      if (st.running) {
        setTimeout(poll, 3000)
        return
      }
      const svc = services.find((s) => s.id === svcId)
      if (svc) {
        const workingDomains = new Set(st.working.map((w) => w.domain))
        const hit = svc.domains.filter((d) => workingDomains.has(d)).length
        const mode = svc.domains.length > 0 && hit / svc.domains.length >= AUTO_ZAPRET_THRESHOLD ? 'zapret' : 'vps'
        const next = services.map((s) => (s.id === svcId ? { ...s, mode, auto_at: new Date().toISOString() } : s))
        setLocalServices(next)
        setMsg(`✓ авто-подбор для «${svc.name}»: ${mode} (${hit}/${svc.domains.length} доменов через zapret) — нажмите «Сохранить и применить»`)
      }
      setAutoServiceId(null)
    }
    poll()
    return () => {
      autoCancelled.current = true
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [autoServiceId])

  async function onSaveServices() {
    if (!localServices) return
    setSaving(true)
    setMsg(null)
    try {
      const res = await saveServices(localServices)
      if (res.error) setMsg('✗ ' + res.error)
      else {
        setMsg('✓ применено')
        setLocalServices(null)
        if (res.pinJobs && res.pinJobs.length > 0) {
          setPinJobs(Object.fromEntries(res.pinJobs.map((id) => [id, { total: 0, done: 0 }])))
        }
      }
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setSaving(false)
    }
  }

  // T-vps-pin: опрос прогресса фоновых job'ов, пока хоть один жив. Каждый
  // сервис — своя работа (чистка/перепроверка), завершается независимо —
  // убираем из отслеживания по одному, не ждём самый долгий, чтобы показать
  // остальные.
  useEffect(() => {
    const ids = Object.keys(pinJobs)
    if (ids.length === 0) return
    const t = setInterval(async () => {
      const results = await Promise.all(
        ids.map(async (id) => {
          try {
            const { job } = await fetchPinVPSJob(id)
            return [id, job] as const
          } catch {
            return [id, null] as const
          }
        }),
      )
      setPinJobs((prev) => {
        const next = { ...prev }
        for (const [id, job] of results) {
          if (job) next[id] = job
          else delete next[id]
        }
        return next
      })
    }, 2000)
    return () => clearInterval(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [Object.keys(pinJobs).join(',')])

  return (
    <div>
      <TopBar title="Domains" subtitle="ручные домены и режимы сервисов" live={!servicesError} />

      <div className="mb-(--section-gap)">
        <SectionHead title="Домены в обход (вручную)" count={domainsData?.domains.length} />
        {domainsData && (
          <div className="mb-3 text-[11.5px] text-text-muted">
            Плюс ещё {domainsData.defaults.length} курируемых доменов уже встроены и работают без ручного
            добавления (Instagram/Discord/YouTube и т.д. — см. вкладку Domains ниже, раздел «Сервисы»); список
            здесь — только для доменов, которых нет ни в курируемых, ни в автообходе.
          </div>
        )}
        <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
          <div className="mb-3 flex gap-2">
            <input
              value={input}
              onChange={(e) => setInput(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && onAdd()}
              placeholder="example.com"
              className="h-9 flex-1 rounded-md border border-border bg-surface-raised px-3 text-[13px] outline-none focus:border-border-strong"
            />
            <button
              onClick={onAdd}
              disabled={busy}
              className="rounded-md border border-border-strong bg-surface-raised px-4 text-[13px] font-medium disabled:opacity-40"
            >
              Добавить
            </button>
          </div>
          {msg && <div className="text-[11px] text-text-muted">{msg}</div>}
        </div>
      </div>

      <div className="mb-(--section-gap)">
        <div className="mb-3.5 flex items-center justify-between">
          <h2 className="m-0 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
            Сервисы ({services.length})
            <InfoTip text="VPS — строго VPS: всё через туннель. DPI — включает DPI-обход (работает или нет — зависит от стратегий). Direct — прямой путь через провайдера. Auto — миксует маршруты из двух подложек (DPI и VPS), приоритет — 100% работоспособность. Кнопка авто — фоновый поиск LOCAL для всех доменов, переключение только при подтверждении." />
          </h2>
          {localServices && (
            <button
              onClick={onSaveServices}
              disabled={saving}
              className="rounded-md border border-border-strong bg-surface-raised px-3 py-1 text-xs font-medium disabled:opacity-40"
            >
              {saving ? 'Применяю…' : 'Сохранить и применить'}
            </button>
          )}
        </div>
        {Object.keys(pinJobs).length > 0 && (
          <div className="mb-3 flex flex-col gap-1 rounded-[--card-radius] border border-border bg-surface p-(--card-pad) font-mono text-[11px] text-text-muted">
            {Object.entries(pinJobs).map(([id, job]) => (
              <div key={id}>
                {id}: {job && job.total > 0 ? `${job.done}/${job.total}` : 'подготовка…'}
              </div>
            ))}
          </div>
        )}
        <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
          {services.length === 0 && <div className="text-[12.5px] text-text-muted">загрузка…</div>}
          {services.map((svc) => (
            <ServiceRow
              key={svc.id}
              svc={svc}
              onModeChange={onModeChange}
              readyDomains={readyDomains}
              onAuto={onAuto}
              autoBusy={autoServiceId === svc.id}
              bypassedDomains={bypassedDomains}
            />
          ))}
        </div>
      </div>

      <VPSDomainsPanel />

      <AutorouteStatsPanel />

      <PresetsPanel />
    </div>
  )
}
