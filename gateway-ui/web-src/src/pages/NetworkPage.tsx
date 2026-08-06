import { TopBar } from '../components/TopBar'
import { usePoll } from '../hooks/usePoll'
import { fetchNetwork, fetchConnection } from '../lib/api'

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

function fmtBytes(n: number) {
  if (n < 1024) return `${n} B`
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KB`
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MB`
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GB`
}

export function NetworkPage() {
  const { data, error } = usePoll(fetchNetwork, 5000)
  const { data: conn } = usePoll(fetchConnection, 10000)

  return (
    <div>
      <TopBar title="Network" subtitle="интерфейсы, маршрутизация, DNS" live={!error} />

      <div className="mb-(--section-gap) grid grid-cols-[repeat(auto-fit,minmax(var(--grid-min),1fr))] gap-(--grid-gap)">
        <Panel title="Routing">
          <Field label="LAN IP" value={data?.lan_ip || '—'} />
          <Field label="Default route" value={data?.default_route || '—'} />
        </Panel>
        <Panel title="DNS">
          {data?.dns_servers.length ? (
            data.dns_servers.map((d) => <Field key={d} label="Nameserver" value={d} />)
          ) : (
            <div className="py-1 text-[12.5px] text-text-muted">не настроено</div>
          )}
        </Panel>
        <Panel title="Подключение (VPS)">
          <Field label="Настроено" value={conn?.configured ? 'да' : 'нет'} />
          <Field label="Адрес" value={conn?.addr || '—'} />
          <Field label="SNI" value={conn?.sni || '—'} />
        </Panel>
      </div>

      <div className="mb-3.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">Interfaces</div>
      <div className="overflow-x-auto rounded-[--card-radius] border border-border">
        <table className="w-full min-w-[720px] text-[12px]">
          <thead>
            <tr className="border-b border-border text-left text-text-muted">
              <th className="p-3 font-medium">Interface</th>
              <th className="p-3 font-medium">Status</th>
              <th className="p-3 font-medium">Speed</th>
              <th className="p-3 font-medium">RX</th>
              <th className="p-3 font-medium">TX</th>
              <th className="p-3 font-medium">Errors</th>
              <th className="p-3 font-medium">Dropped</th>
            </tr>
          </thead>
          <tbody>
            {data?.interfaces.map((i) => (
              <tr key={i.name} className="border-b border-border last:border-b-0">
                <td className="p-3 font-mono">{i.name}</td>
                <td className="p-3">
                  <span className={`flex items-center gap-1.5 font-mono text-[11px] ${i.up ? 'text-success' : 'text-text-muted'}`}>
                    <span className={`h-[7px] w-[7px] rounded-full ${i.up ? 'bg-success' : 'bg-text-muted'}`} />
                    {i.up ? 'up' : 'down'}
                  </span>
                </td>
                <td className="p-3 font-mono tabular-nums">{i.speed_mbps ? `${i.speed_mbps} Mbps` : '—'}</td>
                <td className="p-3 font-mono tabular-nums">{fmtBytes(i.rx_bytes)}</td>
                <td className="p-3 font-mono tabular-nums">{fmtBytes(i.tx_bytes)}</td>
                <td className="p-3 font-mono tabular-nums">
                  {i.rx_errors + i.tx_errors > 0 ? <span className="text-warning">{i.rx_errors + i.tx_errors}</span> : '0'}
                </td>
                <td className="p-3 font-mono tabular-nums">
                  {i.rx_dropped + i.tx_dropped > 0 ? <span className="text-warning">{i.rx_dropped + i.tx_dropped}</span> : '0'}
                </td>
              </tr>
            ))}
            {!data && (
              <tr>
                <td className="p-3 text-text-muted" colSpan={7}>
                  загрузка…
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}
