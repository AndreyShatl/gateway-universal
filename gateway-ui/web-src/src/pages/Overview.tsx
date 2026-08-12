import { TopBar } from '../components/TopBar'
import { ModesPanel } from '../components/ModesPanel'
import { MissionTimeline } from '../components/MissionTimeline'
import { HealthPanel } from '../components/HealthPanel'
import { InternetPanel } from '../components/InternetPanel'
import { TrafficEngineCard } from '../components/TrafficEngineCard'
import { InfoTip } from '../components/InfoTip'
import { usePoll } from '../hooks/usePoll'
import { fetchConnection } from '../lib/api'

function SectionHead({ title }: { title: string }) {
  return (
    <div className="mb-3.5 flex items-center justify-between">
      <h2 className="m-0 text-[11px] font-medium uppercase tracking-wider text-text-muted">{title}</h2>
    </div>
  )
}

function Field({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between border-b border-border py-2.5 text-[12.5px] last:border-b-0">
      <span className="text-text-muted">{label}</span>
      <span className="font-mono tabular-nums">{value}</span>
    </div>
  )
}

function Panel({ title, hint, children }: { title: string; hint?: string; children: React.ReactNode }) {
  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-2 flex items-center gap-1.5 text-[10.5px] uppercase tracking-wide text-text-muted">
        {title}
        {hint && <InfoTip text={hint} />}
      </div>
      {children}
    </div>
  )
}

export function Overview() {
  const { data: conn, error: connError } = usePoll(fetchConnection, 10000)

  return (
    <div>
      <TopBar title="Shattl Gateway" subtitle="локальная панель шлюза" live={!connError} />

      <div className="mb-(--section-gap)">
        <SectionHead title="Состояние сервисов" />
        <div className="grid grid-cols-[repeat(auto-fit,minmax(var(--grid-min),1fr))] gap-(--grid-gap)">
          <TrafficEngineCard />
          <Panel
            title="Подключение (VPS)"
            hint="Параметры VLESS Reality gRPC-туннеля до вашего VPS — тот канал, через который идёт трафик в режиме vps (Instagram/Discord и т.п.) и весь трафик при включённом VPS-режиме."
          >
            <Field label="Настроено" value={conn?.configured ? 'да' : 'нет'} />
            <Field label="Адрес" value={conn?.addr || '—'} />
            <Field label="Порт gRPC" value={conn?.port_grpc || '—'} />
            <Field label="SNI" value={conn?.sni || '—'} />
          </Panel>
          <ModesPanel />
          <HealthPanel />
          <InternetPanel />
        </div>
      </div>

      <div>
        <SectionHead title="Хронология" />
        <MissionTimeline />
      </div>
    </div>
  )
}
