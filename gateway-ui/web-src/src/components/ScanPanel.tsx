import { useState } from 'react'
import { usePoll } from '../hooks/usePoll'
import { fetchScanStatus, startScan, stopScan } from '../lib/api'

export function ScanPanel() {
  const { data } = usePoll(fetchScanStatus, 3000)
  const [domains, setDomains] = useState('')
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState<string | null>(null)

  const running = data?.running ?? false

  async function onStart() {
    const list = domains
      .split(/[\s,]+/)
      .map((d) => d.trim())
      .filter(Boolean)
    if (list.length === 0) return
    setBusy(true)
    setMsg(null)
    try {
      const res = await startScan(list)
      if (res.error) setMsg('✗ ' + res.error)
      else setMsg('✓ запущено')
    } catch (e) {
      setMsg('✗ ' + (e instanceof Error ? e.message : String(e)))
    } finally {
      setBusy(false)
    }
  }

  async function onStop() {
    setBusy(true)
    try {
      await stopScan()
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-3 flex items-center justify-between">
        <span className="text-[10.5px] uppercase tracking-wide text-text-muted">Поиск стратегии (blockcheck)</span>
        {running && (
          <span className="rounded-md bg-warning/[.12] px-2 py-0.5 font-mono text-[10px] uppercase tracking-wide text-warning">
            идёт
          </span>
        )}
      </div>

      {!data?.precondition_ok && (
        <div className="mb-3 text-[12px] text-text-muted">{data?.precondition || 'проверка предусловий…'}</div>
      )}

      <div className="mb-3 flex gap-2">
        <input
          value={domains}
          onChange={(e) => setDomains(e.target.value)}
          placeholder="domain1.com, domain2.com"
          disabled={running}
          className="h-9 flex-1 rounded-md border border-border bg-surface-raised px-3 text-[13px] outline-none focus:border-border-strong disabled:opacity-40"
        />
        {running ? (
          <button
            onClick={onStop}
            disabled={busy}
            className="rounded-md border border-border-strong bg-surface-raised px-4 text-[13px] font-medium text-danger disabled:opacity-40"
          >
            Остановить
          </button>
        ) : (
          <button
            onClick={onStart}
            disabled={busy || !data?.can_start}
            className="rounded-md border border-border-strong bg-surface-raised px-4 text-[13px] font-medium disabled:opacity-40"
          >
            Запустить
          </button>
        )}
      </div>
      {msg && <div className="mb-2 text-[11px] text-text-muted">{msg}</div>}
      {data?.working && <div className="mb-2 font-mono text-[11.5px] text-text-secondary">{data.working}</div>}
      {data?.log_tail && (
        <pre className="max-h-[180px] overflow-auto rounded-md border border-border bg-bg p-2.5 font-mono text-[11px] leading-relaxed text-text-muted">
          {data.log_tail}
        </pre>
      )}
    </div>
  )
}
