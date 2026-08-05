import { useState } from 'react'
import { usePoll } from '../hooks/usePoll'
import {
  fetchGameMode,
  setGameMode,
  fetchVPSMode,
  setVPSMode,
  fetchAdguard,
} from '../lib/api'

const gameModes = ['off', 'tcp', 'udp', 'both'] as const
const vpsModes = ['off', 'on'] as const

function Pills<T extends string>({
  options,
  value,
  onChange,
  disabled,
}: {
  options: readonly T[]
  value: T
  onChange: (v: T) => void
  disabled?: boolean
}) {
  return (
    <div className="flex gap-0.5 rounded-lg border border-border p-0.5">
      {options.map((o) => (
        <button
          key={o}
          disabled={disabled}
          onClick={() => onChange(o)}
          className={`rounded-md px-2.5 py-1 font-mono text-[11px] transition-colors disabled:opacity-40 ${
            value === o ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted'
          }`}
        >
          {o}
        </button>
      ))}
    </div>
  )
}

export function ModesPanel() {
  const { data: gm } = usePoll(fetchGameMode, 8000)
  const { data: vm } = usePoll(fetchVPSMode, 8000)
  const { data: ag } = usePoll(fetchAdguard, 8000)
  const [busy, setBusy] = useState(false)

  async function onGameMode(mode: (typeof gameModes)[number]) {
    setBusy(true)
    try {
      await setGameMode(mode)
    } finally {
      setBusy(false)
    }
  }

  async function onVPSMode(mode: (typeof vpsModes)[number]) {
    setBusy(true)
    try {
      await setVPSMode(mode === 'on' ? 'on' : 'off')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-2 text-[10.5px] uppercase tracking-wide text-text-muted">Режимы</div>

      <div className="flex items-center justify-between border-b border-border py-2.5 text-[12.5px]">
        <span className="text-text-muted">Game mode</span>
        <Pills options={gameModes} value={(gm?.mode as (typeof gameModes)[number]) || 'off'} onChange={onGameMode} disabled={busy} />
      </div>

      <div className="flex items-center justify-between border-b border-border py-2.5 text-[12.5px]">
        <span className="text-text-muted">VPS-режим (весь трафик)</span>
        <Pills options={vpsModes} value={vm?.mode === 'on' ? 'on' : 'off'} onChange={onVPSMode} disabled={busy} />
      </div>

      <div className="flex items-center justify-between py-2.5 text-[12.5px]">
        <span className="text-text-muted">AdGuard Home</span>
        {ag?.available ? (
          <span className="font-mono text-[11px] text-text-secondary">
            {ag.stats?.num_blocked_filtering ?? 0} заблокировано / {ag.stats?.num_dns_queries ?? 0} запросов
          </span>
        ) : (
          <span className="font-mono text-[11px] text-text-muted">недоступен</span>
        )}
      </div>
    </div>
  )
}
