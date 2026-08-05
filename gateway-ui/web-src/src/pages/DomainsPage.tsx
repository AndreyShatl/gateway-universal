import { useState } from 'react'
import { motion, AnimatePresence } from 'framer-motion'
import { X } from 'lucide-react'
import { TopBar } from '../components/TopBar'
import { PresetsPanel } from '../components/PresetsPanel'
import { usePoll } from '../hooks/usePoll'
import { fetchDomains, addDomain, removeDomain, fetchServices, saveServices, type ZService } from '../lib/api'

function SectionHead({ title, count }: { title: string; count?: number }) {
  return (
    <div className="mb-3.5 flex items-center justify-between">
      <h2 className="m-0 text-[11px] font-medium uppercase tracking-wider text-text-muted">{title}</h2>
      {count !== undefined && <span className="font-mono text-[11px] text-text-muted">{count}</span>}
    </div>
  )
}

const modes = [
  { value: 'zapret', label: 'zapret' },
  { value: 'vps', label: 'vps' },
  { value: 'direct', label: 'direct' },
]

function ModeToggle({ value, onChange }: { value: string; onChange: (v: string) => void }) {
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
    </div>
  )
}

function ServiceRow({ svc, onModeChange }: { svc: ZService; onModeChange: (id: string, mode: string) => void }) {
  return (
    <div className="flex items-center justify-between border-b border-border py-3 text-[12.5px] last:border-b-0">
      <div>
        <div className="font-medium">{svc.name}</div>
        <div className="font-mono text-[11px] text-text-muted">{svc.domains.length} domains</div>
      </div>
      <ModeToggle value={svc.mode} onChange={(mode) => onModeChange(svc.id, mode)} />
    </div>
  )
}

export function DomainsPage() {
  const { data: domainsData } = usePoll(fetchDomains, 5000)
  const { data: servicesData, error: servicesError } = usePoll(fetchServices, 5000)
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)
  const [localServices, setLocalServices] = useState<ZService[] | null>(null)
  const [saving, setSaving] = useState(false)

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

  async function onRemove(domain: string) {
    try {
      await removeDomain(domain)
    } catch {
      /* список обновится на следующем polling-тике вне зависимости от исхода */
    }
  }

  function onModeChange(id: string, mode: string) {
    const next = services.map((s) => (s.id === id ? { ...s, mode } : s))
    setLocalServices(next)
  }

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
        <SectionHead title="Домены в обход" count={domainsData?.domains.length} />
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
          {msg && <div className="mb-3 text-[11px] text-text-muted">{msg}</div>}
          <div className="flex flex-wrap gap-1.5">
            <AnimatePresence initial={false}>
              {(domainsData?.domains ?? []).map((d) => (
                <motion.span
                  key={d}
                  initial={{ opacity: 0, scale: 0.9 }}
                  animate={{ opacity: 1, scale: 1 }}
                  exit={{ opacity: 0, scale: 0.9 }}
                  className="flex items-center gap-1.5 rounded-md border border-border bg-surface-raised px-2.5 py-1 font-mono text-[11.5px]"
                >
                  {d}
                  <button onClick={() => onRemove(d)} className="text-text-muted hover:text-danger">
                    <X size={12} strokeWidth={2} />
                  </button>
                </motion.span>
              ))}
            </AnimatePresence>
            {domainsData && domainsData.domains.length === 0 && (
              <span className="text-[12.5px] text-text-muted">нет добавленных вручную доменов</span>
            )}
          </div>
        </div>
      </div>

      <div className="mb-(--section-gap)">
        <div className="mb-3.5 flex items-center justify-between">
          <h2 className="m-0 text-[11px] font-medium uppercase tracking-wider text-text-muted">
            Сервисы ({services.length})
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
            <ServiceRow key={svc.id} svc={svc} onModeChange={onModeChange} />
          ))}
        </div>
      </div>

      <PresetsPanel />
    </div>
  )
}
