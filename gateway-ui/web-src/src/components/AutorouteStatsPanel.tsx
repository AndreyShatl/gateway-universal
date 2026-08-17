import { InfoTip } from './InfoTip'
import { usePoll } from '../hooks/usePoll'
import { fetchAutorouteStats } from '../lib/api'

const STATE_LABEL: Record<string, string> = {
  HEALTHY: 'здоровы',
  DEGRADED: 'проблемные',
  FAILED: 'не работают напрямую',
  UNKNOWN: 'не проверялись',
}

const STATE_COLOR: Record<string, string> = {
  HEALTHY: 'text-success',
  DEGRADED: 'text-text-muted',
  FAILED: 'text-danger',
  UNKNOWN: 'text-text-muted',
}

export function AutorouteStatsPanel() {
  const { data } = usePoll(fetchAutorouteStats, 15000)
  if (!data || data.total === 0) return null

  return (
    <div className="mb-(--section-gap) rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-3 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
        IP-автообход
        <InfoTip text="Отдельный механизм от доменных сервисов выше — учитывает IP/CIDR/порт напрямую (нужен, например, играм: они часто подключаются к серверам по IP, без домена). LEARNED — обнаружено автоматически по реальному трафику; STATIC — закреплено вручную, никогда не снимается сам по себе." />
      </div>
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2 font-mono text-[11.5px]">
        <span className="text-text-secondary">{data.total} записей</span>
        <span className="text-text-muted">
          learned <span className="text-text-secondary">{data.learned}</span> · static{' '}
          <span className="text-text-secondary">{data.static}</span>
        </span>
        {data.port_scoped > 0 && (
          <span className="text-text-muted">
            игровые (по порту) <span className="text-text-secondary">{data.port_scoped}</span>
          </span>
        )}
        <span className="flex items-center gap-3">
          {(['HEALTHY', 'DEGRADED', 'FAILED', 'UNKNOWN'] as const)
            .filter((k) => data.states[k] > 0)
            .map((k) => (
              <span key={k} className={STATE_COLOR[k]}>
                {data.states[k]} {STATE_LABEL[k]}
              </span>
            ))}
        </span>
      </div>
    </div>
  )
}
