import { useState } from 'react'
import { TopBar } from '../components/TopBar'
import { InfoTip } from '../components/InfoTip'
import { usePoll } from '../hooks/usePoll'
import { fetchEngineStatus, fetchEngineSnapshots, type EngineSnapshot } from '../lib/api'

function fmtAgo(iso: string) {
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'только что'
  if (s < 3600) return `${Math.floor(s / 60)} мин назад`
  if (s < 86400) return `${Math.floor(s / 3600)} ч назад`
  return `${Math.floor(s / 86400)} дн назад`
}

const statusColor: Record<string, string> = {
  healthy: 'text-success',
  degraded: 'text-warning',
  failed: 'text-danger',
}

function SnapshotRow({ snap }: { snap: EngineSnapshot }) {
  const [open, setOpen] = useState(false)
  const entries = Object.entries(snap.data ?? {})
  return (
    <div className="border-b border-border py-3 text-[12.5px] last:border-b-0">
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="rounded-md border border-border bg-surface-raised px-1.5 py-0.5 font-mono text-[10.5px] text-text-muted">
              {snap.component}
            </span>
            <span className="truncate">{snap.reason}</span>
          </div>
          <div className="mt-0.5 font-mono text-[11px] text-text-muted">
            {new Date(snap.at).toLocaleString()} · {fmtAgo(snap.at)}
          </div>
        </div>
        {entries.length > 0 && (
          <button
            onClick={() => setOpen((v) => !v)}
            className="shrink-0 rounded-md border border-border px-2 py-1 font-mono text-[10.5px] text-text-muted hover:text-text"
          >
            {open ? 'скрыть' : `данные (${entries.length})`}
          </button>
        )}
      </div>
      {open && entries.length > 0 && (
        <div className="mt-2 space-y-1 rounded-md border border-border bg-surface-raised p-2">
          {entries.map(([k, v]) => (
            <div key={k} className="flex gap-2 font-mono text-[11px]">
              <span className="shrink-0 text-text-muted">{k}:</span>
              <span className="truncate">{v}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}

export function DiagnosticsPage() {
  const { data: status, error: statusError } = usePoll(fetchEngineStatus, 5000)
  const { data: snapshots, error: snapError } = usePoll(fetchEngineSnapshots, 5000)

  return (
    <div>
      <TopBar
        title="Diagnostics"
        subtitle="что оркестратор реально делал — статус движка и история операций"
        live={!statusError && !snapError}
        hint="Advanced Diagnostics — сырой снапшот здоровья Traffic Engine и журнал backup/apply/health-check операций оркестратора (см. controlCore в engine.go). Полезно, когда нужно понять, что именно произошло при переключении VPS-режима, рестарте ядра и т.п., без копания в systemd-логах вручную."
      />

      <div className="mb-(--section-gap)">
        <div className="mb-3.5 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
          Engine status (сырой)
          <InfoTip text="То же самое здоровье, что и агрегированная карточка Traffic Engine на Overview — здесь без сборки в человекочитаемый текст, компонент за компонентом." />
        </div>
        <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
          {!status && <div className="text-[12.5px] text-text-muted">загрузка…</div>}
          {status && (
            <>
              <div className="mb-3 flex items-center justify-between border-b border-border pb-3 text-[13px]">
                <span className="font-medium">{status.engine}</span>
                <span className={`font-mono text-[11.5px] uppercase ${statusColor[status.status] ?? 'text-text-muted'}`}>
                  {status.status}
                </span>
              </div>
              {status.detail && <div className="mb-3 text-[12px] text-text-muted">{status.detail}</div>}
              <div className="grid grid-cols-[repeat(auto-fit,minmax(100px,1fr))] gap-2 font-mono text-[11px]">
                {Object.entries(status.components).map(([k, v]) => (
                  <div key={k} className="flex items-center justify-between rounded-md border border-border bg-surface-raised px-2 py-1.5">
                    <span className="text-text-muted">{k}</span>
                    <span className={v ? 'text-success' : 'text-text-muted'}>{v ? 'да' : 'нет'}</span>
                  </div>
                ))}
              </div>
            </>
          )}
        </div>
      </div>

      <div>
        <div className="mb-3.5 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
          Snapshots оркестратора ({snapshots?.length ?? 0})
          <InfoTip text="backup → apply → health-check вокруг потенциально опасных операций (переключение VPS-режима, control zapret/xray). Живут только в памяти процесса — не переживают рестарт gateway-ui, это история ТЕКУЩЕЙ сессии, не персистентный лог." />
        </div>
        <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
          {snapshots && snapshots.length === 0 && (
            <div className="text-[12.5px] text-text-muted">пока пусто — снапшот появится при следующей операции оркестратора (смена VPS-режима, control ядра и т.п.)</div>
          )}
          {!snapshots && <div className="text-[12.5px] text-text-muted">загрузка…</div>}
          {snapshots?.map((s) => <SnapshotRow key={s.id} snap={s} />)}
        </div>
      </div>
    </div>
  )
}
