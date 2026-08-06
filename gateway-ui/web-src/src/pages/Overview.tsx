import { TopBar } from '../components/TopBar'
import { EngineCard } from '../components/EngineCard'
import { ModesPanel } from '../components/ModesPanel'
import { MissionTimeline } from '../components/MissionTimeline'
import { HealthPanel } from '../components/HealthPanel'
import { usePoll } from '../hooks/usePoll'
import { fetchStatus, fetchConnection, fetchZapretVersion, fetchCiadpiVersion, fetchZapret2Version } from '../lib/api'

function SectionHead({ title }: { title: string }) {
  return (
    <div className="mb-3.5 flex items-center justify-between">
      <h2 className="m-0 text-[11px] font-medium uppercase tracking-wider text-text-muted">{title}</h2>
    </div>
  )
}

function statusVariant(v: string): 'online' | 'offline' | 'degraded' {
  if (v === 'active') return 'online'
  if (v === 'failed') return 'degraded'
  return 'offline'
}

const dotClass: Record<string, string> = {
  online: 'bg-success',
  degraded: 'bg-warning',
  offline: 'bg-text-muted',
}

// "Running"/"Failed"/"Stopped" — та же формулировка, что и у gmp-server
// Dashboard (ServiceRow там же): сырые systemd-статусы (active/failed/…)
// на двух похожих панелях одного шлюза читались как расхождение.
const stateLabel: Record<string, string> = {
  online: 'Running',
  degraded: 'Failed',
  offline: 'Stopped',
}

function ServiceRow({ name, state }: { name: string; state: string }) {
  const variant = statusVariant(state)
  return (
    <div className="flex items-center justify-between border-b border-border py-2.5 text-[12.5px] last:border-b-0">
      <div className="flex items-center gap-2.5">
        <span className={`h-[7px] w-[7px] rounded-full ${dotClass[variant]}`} />
        <span>{name}</span>
      </div>
      <span className="font-mono text-[11px] text-text-muted">{stateLabel[variant]}</span>
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

function Panel({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-2 text-[10.5px] uppercase tracking-wide text-text-muted">{title}</div>
      {children}
    </div>
  )
}

export function Overview() {
  const { data: status, error: statusError } = usePoll(fetchStatus, 3000)
  const { data: conn } = usePoll(fetchConnection, 10000)

  return (
    <div>
      <TopBar title="Shattl Gateway" subtitle="локальная панель шлюза" live={!statusError} />

      <div className="mb-(--section-gap)">
        <SectionHead title="Состояние сервисов" />
        <div className="grid grid-cols-[repeat(auto-fit,minmax(var(--grid-min),1fr))] gap-(--grid-gap)">
          <Panel title="Демоны">
            {status ? (
              Object.entries(status.services).map(([name, state]) => <ServiceRow key={name} name={name} state={state} />)
            ) : (
              <div className="text-[12.5px] text-text-muted">загрузка…</div>
            )}
          </Panel>
          <Panel title="Подключение (VPS)">
            <Field label="Настроено" value={conn?.configured ? 'да' : 'нет'} />
            <Field label="Адрес" value={conn?.addr || '—'} />
            <Field label="Порт gRPC" value={conn?.port_grpc || '—'} />
            <Field label="SNI" value={conn?.sni || '—'} />
          </Panel>
          <ModesPanel />
          <HealthPanel />
        </div>
      </div>

      <div className="mb-(--section-gap)">
        <SectionHead title="Хронология" />
        <MissionTimeline />
      </div>

      <div>
        <SectionHead title="Движки обхода" />
        <div className="grid grid-cols-[repeat(auto-fit,minmax(var(--grid-min),1fr))] gap-(--grid-gap)">
          <EngineCard engine="zapret" label="zapret" fetcher={fetchZapretVersion} index={0} />
          <EngineCard engine="ciadpi" label="ciadpi" fetcher={fetchCiadpiVersion} index={1} />
          <EngineCard engine="zapret2" label="zapret2" fetcher={fetchZapret2Version} index={2} />
        </div>
      </div>
    </div>
  )
}
