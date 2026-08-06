import { useState } from 'react'
import { X } from 'lucide-react'
import { TopBar } from '../components/TopBar'
import { InfoTip } from '../components/InfoTip'
import { usePoll } from '../hooks/usePoll'
import {
  fetchRouterIP,
  setRouterIP,
  fetchConnections,
  addConnection,
  activateConnection,
  deleteConnection,
  fetchGMPStatus,
  type SavedConnection,
} from '../lib/api'

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

function ConnectionRow({ conn, onActivate, onDelete, busy }: { conn: SavedConnection; onActivate: (id: string) => void; onDelete: (id: string) => void; busy: boolean }) {
  return (
    <div className="flex items-center justify-between gap-3 border-b border-border py-2.5 text-[12.5px] last:border-b-0">
      <div className="min-w-0">
        <div className="flex items-center gap-1.5">
          <span className="truncate font-medium">{conn.name}</span>
          {conn.active && (
            <span className="rounded-md bg-success/[.12] px-1.5 py-0.5 font-mono text-[9.5px] uppercase tracking-wide text-success">
              активно
            </span>
          )}
        </div>
        <div className="truncate font-mono text-[11px] text-text-muted">
          {conn.addr}:{conn.port_grpc}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {!conn.active && (
          <button
            onClick={() => onActivate(conn.id)}
            disabled={busy}
            className="rounded-md border border-border px-2.5 py-1 text-[11.5px] text-text-secondary hover:border-border-strong hover:text-text disabled:opacity-40"
          >
            Переключить
          </button>
        )}
        <button onClick={() => onDelete(conn.id)} disabled={busy} className="text-text-muted hover:text-danger disabled:opacity-40">
          <X size={14} strokeWidth={2} />
        </button>
      </div>
    </div>
  )
}

function VPSConnectionPanel() {
  const { data } = usePoll(fetchConnections, 5000)
  const [link, setLink] = useState('')
  const [name, setName] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)

  const connections = data?.connections ?? []

  async function onAdd() {
    if (!link.trim()) return
    setBusy(true)
    setMsg(null)
    try {
      const res = await addConnection(link.trim(), name.trim())
      if (res.error) setMsg('✗ ' + res.error)
      else {
        setMsg('✓ добавлено')
        setLink('')
        setName('')
        
      }
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  async function onActivate(id: string) {
    setBusy(true)
    setMsg(null)
    try {
      const res = await activateConnection(id)
      if (res.error) setMsg('✗ ' + res.error)
      else {
        setMsg('✓ переключено на ' + (res.addr || ''))
        
      }
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  async function onDelete(id: string) {
    setBusy(true)
    try {
      await deleteConnection(id)
      
    } finally {
      setBusy(false)
    }
  }

  return (
    <Panel
      title="Подключения к VPS"
      hint="Можно сохранить несколько VPS и переключаться между ними в один клик — например свой и запасной. Вставьте vless://-ссылку из панели 3x-ui (Reality, gRPC или tcp/vision), чтобы добавить новое."
    >
      {connections.length > 0 && (
        <div className="mb-3">
          {connections.map((c) => (
            <ConnectionRow key={c.id} conn={c} onActivate={onActivate} onDelete={onDelete} busy={busy} />
          ))}
        </div>
      )}
      <div className="mb-3 flex flex-wrap gap-2">
        <input
          value={name}
          onChange={(e) => setName(e.target.value)}
          placeholder="имя (необязательно)"
          className="h-9 w-[160px] rounded-md border border-border bg-surface-raised px-3 text-[13px] outline-none focus:border-border-strong"
        />
        <input
          value={link}
          onChange={(e) => setLink(e.target.value)}
          onKeyDown={(e) => e.key === 'Enter' && onAdd()}
          placeholder="vless://uuid@host:port?security=reality&..."
          className="h-9 min-w-[220px] flex-1 rounded-md border border-border bg-surface-raised px-3 font-mono text-[12.5px] outline-none focus:border-border-strong"
        />
        <button
          onClick={onAdd}
          disabled={busy}
          className="rounded-md border border-border-strong bg-surface-raised px-4 text-[13px] font-medium disabled:opacity-40"
        >
          {busy ? 'Применяю…' : 'Добавить'}
        </button>
      </div>
      {msg && <div className="text-[11px] text-text-muted">{msg}</div>}
    </Panel>
  )
}

function GMPStatusPanel() {
  const { data } = usePoll(fetchGMPStatus, 15000)

  return (
    <Panel
      title="Подключение к GMP"
      hint="Это НЕ то же самое, что подключение к VPS выше. GMP — удалённый мониторинг: если этот шлюз зарегистрирован, его метрики видны на чужом Dashboard (у того, кто выдал токен при установке). Отдельный механизм от xray-туннеля."
    >
      {!data?.installed ? (
        <div className="text-[12.5px] text-text-muted">gmp-agent не установлен на этом шлюзе.</div>
      ) : (
        <>
          <div className="flex justify-between border-b border-border py-2.5 text-[12.5px] last:border-b-0">
            <span className="text-text-muted">Статус</span>
            <span className={`flex items-center gap-1.5 font-mono text-[11.5px] ${data.registered ? 'text-success' : 'text-text-muted'}`}>
              <span className={`h-[7px] w-[7px] rounded-full ${data.registered ? 'bg-success' : 'bg-text-muted'}`} />
              {data.registered ? 'Зарегистрирован' : 'Не зарегистрирован'}
            </span>
          </div>
          <div className="flex justify-between border-b border-border py-2.5 text-[12.5px] last:border-b-0">
            <span className="text-text-muted">Сервер</span>
            <span className="font-mono">{data.server_url || '—'}</span>
          </div>
          <div className="flex justify-between border-b border-border py-2.5 text-[12.5px] last:border-b-0">
            <span className="text-text-muted">Gateway ID</span>
            <span className="max-w-[220px] truncate font-mono text-[11px]" title={data.gateway_id}>
              {data.gateway_id || '—'}
            </span>
          </div>
        </>
      )}
    </Panel>
  )
}

export function SettingsPage() {
  return (
    <div>
      <TopBar title="Settings" />
      <VPSConnectionPanel />
      <GMPStatusPanel />
      <RouterIPPanel />
      <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad) text-[12.5px] text-text-secondary">
        Тема и плотность интерфейса — в верхней панели на каждой странице.
      </div>
    </div>
  )
}
