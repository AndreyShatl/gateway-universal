import { useState } from 'react'
import { InfoTip } from './InfoTip'
import { usePoll } from '../hooks/usePoll'
import { fetchVPSDomains, type VPSDomainEntry } from '../lib/api'

function SectionHead({ title, count, hint }: { title: string; count?: number; hint?: string }) {
  return (
    <div className="mb-3.5 flex items-center justify-between">
      <h2 className="m-0 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
        {title}
        {hint && <InfoTip text={hint} />}
      </h2>
      {count !== undefined && <span className="font-mono text-[11px] text-text-muted">{count}</span>}
    </div>
  )
}

type SortKey = 'domain' | 'route' | 'group_id' | 'last_active'

function fmtAgo(iso?: string) {
  if (!iso) return '—'
  const s = Math.floor((Date.now() - new Date(iso).getTime()) / 1000)
  if (s < 60) return 'только что'
  if (s < 3600) return `${Math.floor(s / 60)} мин назад`
  if (s < 86400) return `${Math.floor(s / 3600)} ч назад`
  return `${Math.floor(s / 86400)} дн назад`
}

function sortEntries(entries: VPSDomainEntry[], key: SortKey, dir: 1 | -1): VPSDomainEntry[] {
  const withFallback = (v: string | undefined) => v ?? ''
  return [...entries].sort((a, b) => {
    let av: string, bv: string
    switch (key) {
      case 'route':
        av = a.route
        bv = b.route
        break
      case 'group_id':
        av = withFallback(a.group_id)
        bv = withFallback(b.group_id)
        break
      case 'last_active':
        av = withFallback(a.last_active)
        bv = withFallback(b.last_active)
        break
      default:
        av = a.domain
        bv = b.domain
    }
    return av < bv ? -1 * dir : av > bv ? 1 * dir : 0
  })
}

function SortButton({
  label,
  active,
  dir,
  onClick,
}: {
  label: string
  active: boolean
  dir: 1 | -1
  onClick: () => void
}) {
  return (
    <button
      onClick={onClick}
      className={`flex items-center gap-1 rounded-md px-2 py-1 font-mono text-[10.5px] transition-colors ${
        active ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted hover:text-text-secondary'
      }`}
    >
      {label}
      {active && <span>{dir === 1 ? '↑' : '↓'}</span>}
    </button>
  )
}

function DomainList({ entries }: { entries: VPSDomainEntry[] }) {
  const [sortKey, setSortKey] = useState<SortKey>('domain')
  const [dir, setDir] = useState<1 | -1>(1)
  const [filter, setFilter] = useState('')

  function toggleSort(key: SortKey) {
    if (key === sortKey) setDir((d) => (d === 1 ? -1 : 1))
    else {
      setSortKey(key)
      setDir(1)
    }
  }

  const filtered = filter.trim() ? entries.filter((e) => e.domain.toLowerCase().includes(filter.trim().toLowerCase())) : entries
  const sorted = sortEntries(filtered, sortKey, dir)
  const vpsCount = entries.filter((e) => e.route === 'vps').length

  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <div className="font-mono text-[11px] text-text-muted">
          {entries.length} доменов · {vpsCount} на VPS · {entries.length - vpsCount} на DPI-обходе
          {filter.trim() && ` · найдено ${filtered.length}`}
        </div>
        <div className="flex flex-wrap items-center gap-1">
          <input
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
            placeholder="фильтр по домену…"
            className="h-7 w-40 rounded-md border border-border bg-surface-raised px-2 font-mono text-[11px] outline-none focus:border-border-strong"
          />
          <SortButton label="домен" active={sortKey === 'domain'} dir={dir} onClick={() => toggleSort('domain')} />
          <SortButton label="маршрут" active={sortKey === 'route'} dir={dir} onClick={() => toggleSort('route')} />
          <SortButton label="группа" active={sortKey === 'group_id'} dir={dir} onClick={() => toggleSort('group_id')} />
          <SortButton
            label="обновлено"
            active={sortKey === 'last_active'}
            dir={dir}
            onClick={() => toggleSort('last_active')}
          />
        </div>
      </div>
      <div className="max-h-[420px] overflow-auto rounded-[--card-radius] border border-border bg-surface font-mono text-[11.5px]">
        {sorted.length === 0 && <div className="p-(--card-pad) text-text-muted">пусто</div>}
        {sorted.map((e) => (
          <div
            key={e.domain}
            className="flex items-center justify-between gap-3 border-b border-border px-(--card-pad) py-2 last:border-b-0"
          >
            <span className="truncate text-text-secondary">{e.domain}</span>
            <div className="flex shrink-0 items-center gap-3 text-[11px] text-text-muted">
              {e.route === 'dpi' ? (
                <>
                  <span className="text-success">{e.engine}</span>
                  <span>{e.group_id}</span>
                  <span>{fmtAgo(e.last_active)}</span>
                </>
              ) : (
                <span className="text-accent">VPS</span>
              )}
            </div>
          </div>
        ))}
      </div>
    </div>
  )
}

export function VPSDomainsPanel() {
  const { data } = usePoll(fetchVPSDomains, 15000)
  if (!data) return null

  const groups: { title: string; hint?: string; entries: VPSDomainEntry[] }[] = [
    { title: 'Discord', entries: data.discord },
    { title: 'Instagram', entries: data.instagram },
    { title: 'YouTube', entries: data.youtube },
    {
      title: 'Остальные домены',
      hint: 'Курируемые категории (соцсети, стриминг, игры, разработка и т.д. — см. xray/domains/*.txt), кроме уже показанных выше Discord/Instagram/YouTube и кроме AI-сервисов (те никогда не идут в обход намеренно). Как и везде на этой странице — не только VPS, колонка "маршрут" показывает фактическое состояние каждого домена.',
      entries: data.other,
    },
  ].filter((g) => g.entries.length > 0)

  if (groups.length === 0) return null

  const total = groups.reduce((n, g) => n + g.entries.length, 0)

  return (
    <div className="mb-(--section-gap)">
      <SectionHead
        title="Домены через VPS"
        count={total}
        hint="Все домены из курируемых списков (Discord/Instagram/YouTube/остальные, кроме AI-сервисов — те никогда не идут в обход) с фактическим текущим маршрутом. 'VPS' — статический baseline-путь; 'zapret/ciadpi/zapret2' — домен сбежал на локальный DPI-обход (см. brain-apply.sh escape). Список для анализа — сколько уже переведено, сколько ещё нет."
      />
      <div className="space-y-(--section-gap)">
        {groups.map((g) => (
          <div key={g.title} className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
            <div className="mb-3 flex items-center gap-1.5 text-[11px] font-medium uppercase tracking-wider text-text-muted">
              {g.title}
              {g.hint && <InfoTip text={g.hint} />}
            </div>
            <DomainList entries={g.entries} />
          </div>
        ))}
      </div>
    </div>
  )
}
