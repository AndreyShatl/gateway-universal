import { usePoll } from '../hooks/usePoll'
import { fetchInternetChecks } from '../lib/api'

// Тот же CheckRow, что и на Dashboard gmp-server (GatewayDetail.tsx) —
// панель "Internet" там уже есть для удалённого вида этого же шлюза, тут её
// не было вообще (только фоновый DNS-watcher без визуального вывода).
function CheckRow({ label, ok }: { label: string; ok?: boolean }) {
  return (
    <div className="flex items-center justify-between border-b border-border py-2.5 text-[12.5px] last:border-b-0">
      <span className="text-text-muted">{label}</span>
      <span className={`flex items-center gap-1.5 font-mono text-[11.5px] ${ok ? 'text-success' : 'text-danger'}`}>
        <span className={`h-[7px] w-[7px] rounded-full ${ok ? 'bg-success' : 'bg-danger'}`} />
        {ok ? 'Ok' : 'Down'}
      </span>
    </div>
  )
}

export function InternetPanel() {
  const { data } = usePoll(fetchInternetChecks, 10000)

  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-2 text-[10.5px] uppercase tracking-wide text-text-muted">Internet</div>
      <CheckRow label="DNS" ok={data?.dns_ok} />
      <CheckRow label="HTTPS" ok={data?.https_ok} />
      <CheckRow label="Local gateway reachable" ok={data?.local_gateway_ok} />
      <CheckRow label="VPS reachable" ok={data?.vps_ok} />
    </div>
  )
}
