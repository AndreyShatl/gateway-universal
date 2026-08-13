import { useEffect, useRef, useState } from 'react'
import { Wand2 } from 'lucide-react'
import { TopBar } from '../components/TopBar'
import { PresetsPanel } from '../components/PresetsPanel'
import { InfoTip } from '../components/InfoTip'
import { usePoll } from '../hooks/usePoll'
import { fetchDomains, addDomain, fetchServices, saveServices, startScan, fetchScanStatus, fetchMonitor, type ZService } from '../lib/api'

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

const modes = [
  { value: 'zapret', label: 'zapret' },
  { value: 'vps', label: 'vps' },
  { value: 'direct', label: 'direct' },
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
  const current = value || 'zapret'
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
}: {
  svc: ZService
  onModeChange: (id: string, mode: string) => void
  onAuto: (svc: ZService) => void
  autoBusy: boolean
  bypassedDomains: Set<string>
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
        <ModeToggle value={svc.mode} onChange={(mode) => onModeChange(svc.id, mode)} onAuto={() => onAuto(svc)} autoBusy={autoBusy} />
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
  const bypassedDomains = new Set((monitorData?.brain_groups ?? []).flatMap((g) => g.domains))
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)
  const [localServices, setLocalServices] = useState<ZService[] | null>(null)
  const [saving, setSaving] = useState(false)
  const [autoServiceId, setAutoServiceId] = useState<string | null>(null)
  const autoCancelled = useRef(false)

  const services = localServices ?? servicesData?.services ?? []

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
  async function onAuto(svc: ZService) {
    if (svc.domains.length === 0) {
      setMsg('✗ у сервиса нет доменов для проверки')
      return
    }
    setMsg(null)
    autoCancelled.current = false
    try {
      await startScan(svc.domains, 'quick', `auto:${svc.id}`)
      setAutoServiceId(svc.id)
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
      }
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setSaving(false)
    }
  }

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
            <InfoTip text="Курируемые группы доменов с готовыми стратегиями обхода. Режим на каждую: zapret (свой DPI-обход), vps (форс через VPS-туннель) или direct (без обхода вообще). Кнопка auto прогоняет все домены сервиса через blockcheck и сама подбирает zapret/vps по большинству — решение всё равно нужно подтвердить кнопкой «Сохранить и применить»." />
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
        <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
          {services.length === 0 && <div className="text-[12.5px] text-text-muted">загрузка…</div>}
          {services.map((svc) => (
            <ServiceRow
              key={svc.id}
              svc={svc}
              onModeChange={onModeChange}
              onAuto={onAuto}
              autoBusy={autoServiceId === svc.id}
              bypassedDomains={bypassedDomains}
            />
          ))}
        </div>
      </div>

      {(() => {
        const vpsServices = services.filter((s) => s.mode === 'vps' && s.domains.length > 0)
        const vpsDomainCount = vpsServices.reduce((n, s) => n + s.domains.length, 0)
        if (vpsServices.length === 0) return null
        return (
          <div className="mb-(--section-gap)">
            <SectionHead
              title="Только через VPS"
              count={vpsDomainCount}
              hint="Сервисы в режиме vps идут всем своим трафиком через VPS-туннель, а не через локальный zapret-обход — обычно потому что zapret их не пробивает."
            />
            <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
              {vpsServices.map((svc) => (
                <div key={svc.id} className="border-b border-border py-3 last:border-b-0">
                  <div className="mb-1.5 font-medium">{svc.name}</div>
                  <div className="flex flex-wrap gap-1.5">
                    {svc.domains.map((d) => (
                      <span
                        key={d}
                        className="rounded-md border border-border bg-surface-raised px-2 py-0.5 font-mono text-[11px] text-text-muted"
                      >
                        {d}
                      </span>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          </div>
        )
      })()}

      <PresetsPanel />
    </div>
  )
}
