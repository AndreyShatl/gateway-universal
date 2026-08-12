import { Wrench } from 'lucide-react'
import { InfoTip } from './InfoTip'
import { usePoll } from '../hooks/usePoll'
import { useAdvancedMode } from '../hooks/useSettings'
import { fetchEngineStatus, type EngineComponents } from '../lib/api'

// TrafficEngineCard — ТЗ "Traffic Engine Orchestrator" п.11-14: пользователь
// видит "Traffic Engine ● Healthy", а не список из xray/zapret/ciadpi/
// zapret2 по отдельности. Внутренний состав — только в Advanced Mode
// (тумблер тут же, состояние в localStorage — см. useAdvancedMode).
const dotClass: Record<string, string> = {
  healthy: 'bg-success',
  degraded: 'bg-warning',
  failed: 'bg-danger',
}
const statusLabel: Record<string, string> = {
  healthy: 'Healthy',
  degraded: 'Degraded',
  failed: 'Failed',
}

// ciadpi/zapret2 — не демоны (динамические per-домен группы), "false" здесь
// означает "движок не установлен", а не "остановлен" — формулировка другая,
// чтобы не пугать пользователя красным на пустом месте.
const componentLabels: { key: keyof EngineComponents; label: string; installedOnly?: boolean }[] = [
  { key: 'zapret', label: 'Zapret' },
  { key: 'xray', label: 'VPS-туннель (xray)' },
  { key: 'ciadpi', label: 'CIADPI', installedOnly: true },
  { key: 'zapret2', label: 'Zapret2', installedOnly: true },
  { key: 'brain', label: 'Мозг (маршрутизация)' },
]

export function TrafficEngineCard() {
  const { data: engine } = usePoll(fetchEngineStatus, 5000)
  const [advanced, setAdvanced] = useAdvancedMode()

  const status = engine?.status || 'degraded'

  return (
    <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad)">
      <div className="mb-2 flex items-center justify-between">
        <div className="flex items-center gap-1.5 text-[10.5px] uppercase tracking-wide text-text-muted">
          Traffic Engine
          <InfoTip text="Единая обёртка над всеми механизмами обхода (Zapret/Zapret2/CIADPI/VPS-туннель) и логикой их выбора («мозг»). Обычно не важно, какой конкретно движок сейчас работает — важно, что обход в целом исправен." />
        </div>
        <button
          onClick={() => setAdvanced(!advanced)}
          title="Показать/скрыть внутренний состав"
          className={`flex items-center gap-1 rounded-md border px-2 py-0.5 font-mono text-[10.5px] transition-colors ${
            advanced ? 'border-border-strong bg-surface-raised text-text' : 'border-border text-text-muted hover:text-text-secondary'
          }`}
        >
          <Wrench size={10.5} strokeWidth={2} /> advanced
        </button>
      </div>

      <div className="flex items-center gap-2.5 border-b border-border py-2.5 text-[12.5px]">
        <span className={`h-[7px] w-[7px] rounded-full ${dotClass[status]}`} />
        <span>Shattl Bypass</span>
        <span className="ml-auto font-mono text-[11px] text-text-muted">{statusLabel[status]}</span>
      </div>
      <div className="pt-2 text-[11.5px] text-text-muted">{engine?.detail || 'загрузка…'}</div>

      {advanced && engine && (
        <div className="mt-3 border-t border-border pt-3">
          <div className="mb-2 text-[10px] uppercase tracking-wide text-text-muted">Внутренние компоненты</div>
          {componentLabels.map(({ key, label, installedOnly }) => {
            const ok = engine.components[key]
            return (
              <div key={key} className="flex items-center justify-between border-b border-border py-2 text-[12px] last:border-b-0">
                <span className="text-text-secondary">{label}</span>
                <span className={`flex items-center gap-1.5 font-mono text-[10.5px] ${ok ? 'text-success' : 'text-text-muted'}`}>
                  <span className={`h-[6px] w-[6px] rounded-full ${ok ? 'bg-success' : 'bg-text-muted'}`} />
                  {ok ? (installedOnly ? 'установлен' : 'активен') : installedOnly ? 'не установлен' : 'не активен'}
                </span>
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}
