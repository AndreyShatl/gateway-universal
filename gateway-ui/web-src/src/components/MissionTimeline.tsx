import { usePoll } from '../hooks/usePoll'
import { fetchTimeline } from '../lib/api'
import { InfoTip } from './InfoTip'

const kindDot: Record<string, string> = {
  'vpn.up': 'bg-success',
  'vpn.down': 'bg-danger',
  'dns.up': 'bg-success',
  'dns.down': 'bg-danger',
  'service.restart': 'bg-accent',
  'config.updated': 'bg-accent',
  'system.boot': 'bg-text-muted',
}

function fmtWhen(iso: string) {
  const d = new Date(iso)
  const now = new Date()
  const sameDay = d.toDateString() === now.toDateString()
  const time = d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
  if (sameDay) return time
  const yesterday = new Date(now)
  yesterday.setDate(now.getDate() - 1)
  if (d.toDateString() === yesterday.toDateString()) return `Yesterday ${time}`
  return `${d.toLocaleDateString([], { day: '2-digit', month: '2-digit' })} ${time}`
}

export function MissionTimeline() {
  const { data } = usePoll(fetchTimeline, 15000)

  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-2 flex items-center gap-1.5 text-[10.5px] uppercase tracking-wide text-text-muted">
        Mission Timeline
        <InfoTip text="Локальная лента событий шлюза: загрузка, рестарты сервисов, изменения конфигурации, обрывы/восстановления VPN и DNS. Хранится на самом шлюзе, не зависит от интернета." />
      </div>
      {!data || data.length === 0 ? (
        <div className="py-2 text-[12.5px] text-text-muted">пока пусто</div>
      ) : (
        <div className="max-h-[280px] overflow-y-auto">
          {data.map((ev, i) => (
            <div key={i} className="flex items-center gap-2.5 border-b border-border py-2 text-[12.5px] last:border-b-0">
              <span className={`h-[7px] w-[7px] shrink-0 rounded-full ${kindDot[ev.kind] ?? 'bg-text-muted'}`} />
              <span className="w-[92px] shrink-0 font-mono text-[11px] text-text-muted">{fmtWhen(ev.at)}</span>
              <span className="truncate">{ev.message}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
