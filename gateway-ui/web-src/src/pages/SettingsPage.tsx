import { useState } from 'react'
import { TopBar } from '../components/TopBar'
import { InfoTip } from '../components/InfoTip'
import { usePoll } from '../hooks/usePoll'
import { fetchRouterIP, setRouterIP, fetchConnection, setConnectionLink } from '../lib/api'

function Panel({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="mb-(--section-gap) rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-3 flex items-center gap-1.5 text-[10.5px] uppercase tracking-wide text-text-muted">
        {title}
        {hint && <InfoTip text={hint} />}
      </div>
      {children}
    </div>
  )
}

function RouterIPPanel() {
  const { data, error } = usePoll(fetchRouterIP, 15000)
  const [ip, setIp] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)

  const current = data?.router_ip || ''

  async function onSave() {
    if (!ip.trim()) return
    setBusy(true)
    setMsg(null)
    try {
      const res = await setRouterIP(ip.trim())
      if (res.error) setMsg('✗ ' + res.error)
      else {
        setMsg('✓ применено')
        setIp('')
      }
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Panel
      title="IP роутера"
      hint="Домашний роутер не всегда 192.168.1.1 — бывает 192.168.0.1 и другие. Это тот IP, к которому default route должен указывать при загрузке (см. fix-gateway на вкладке Services)."
    >
      {error && <div className="mb-2 text-[11px] text-danger">не удалось прочитать текущее значение</div>}
      <div className="mb-3 flex gap-2">
        <input
          value={ip}
          onChange={(e) => setIp(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && onSave()}
          placeholder={current || '192.168.1.1'}
          className="h-9 flex-1 rounded-md border border-border bg-surface-raised px-3 font-mono text-[13px] outline-none focus:border-border-strong"
        />
        <button
          onClick={onSave}
          disabled={busy}
          className="rounded-md border border-border-strong bg-surface-raised px-4 text-[13px] font-medium disabled:opacity-40"
        >
          {busy ? 'Применяю…' : 'Сохранить'}
        </button>
      </div>
      <div className="flex justify-between text-[12px]">
        <span className="text-text-muted">Текущее значение</span>
        <span className="font-mono">{current || '—'}</span>
      </div>
      {msg && <div className="mt-2 text-[11px] text-text-muted">{msg}</div>}
    </Panel>
  )
}

function VPSConnectionPanel() {
  const { data } = usePoll(fetchConnection, 15000)
  const [link, setLink] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)

  async function onSave() {
    if (!link.trim()) return
    setBusy(true)
    setMsg(null)
    try {
      const res = await setConnectionLink(link.trim())
      if (res.error) setMsg('✗ ' + res.error)
      else {
        setMsg('✓ подключено: ' + (res.addr || ''))
        setLink('')
      }
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Panel
      title="Подключение к своему VPS"
      hint="Вставьте vless://-ссылку из панели 3x-ui (Reality, gRPC или tcp/vision) — шлюз применит её сразу: перегенерирует конфиг xray и перезапустит туннель. Текущее состояние (замаскированное) — на вкладке Network."
    >
      <div className="mb-3 flex gap-2">
        <input
          value={link}
          onChange={(e) => setLink(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && onSave()}
          placeholder="vless://uuid@host:port?security=reality&..."
          className="h-9 flex-1 rounded-md border border-border bg-surface-raised px-3 font-mono text-[12.5px] outline-none focus:border-border-strong"
        />
        <button
          onClick={onSave}
          disabled={busy}
          className="rounded-md border border-border-strong bg-surface-raised px-4 text-[13px] font-medium disabled:opacity-40"
        >
          {busy ? 'Применяю…' : 'Подключить'}
        </button>
      </div>
      <div className="flex justify-between text-[12px]">
        <span className="text-text-muted">Сейчас настроено</span>
        <span className="font-mono">{data?.configured ? data.addr : 'нет'}</span>
      </div>
      {msg && <div className="mt-2 text-[11px] text-text-muted">{msg}</div>}
    </Panel>
  )
}

export function SettingsPage() {
  return (
    <div>
      <TopBar title="Settings" />
      <VPSConnectionPanel />
      <RouterIPPanel />
      <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad) text-[12.5px] text-text-secondary">
        Тема и плотность интерфейса — в верхней панели на каждой странице.
      </div>
    </div>
  )
}
