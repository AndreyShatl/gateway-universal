import { useDensity, useTheme, type Density } from '../hooks/useSettings'
import { useHostname } from '../hooks/useHostname'
import { InfoTip } from './InfoTip'
import { withBase } from '../lib/basePath'

const densities: { value: Density; label: string }[] = [
  { value: 'compact', label: 'Compact' },
  { value: 'comfortable', label: 'Comfortable' },
  { value: 'spacious', label: 'Spacious' },
]

export function TopBar({
  title,
  subtitle,
  live,
  hint,
}: {
  title: string
  subtitle?: string
  live?: boolean
  hint?: string
}) {
  const [theme, setTheme] = useTheme()
  const [density, setDensity] = useDensity()
  const hostname = useHostname()

  return (
    <div className="mb-7 flex flex-wrap items-baseline justify-between gap-6">
      <div>
        <h1 className="m-0 flex items-center gap-2 text-[19px] font-medium tracking-tight">
          {title}
          {hint && <InfoTip text={hint} />}
          {hostname && (
            <span
              className="rounded-md border border-border px-1.5 py-0.5 font-mono text-[10.5px] font-normal text-text-muted"
              title="Имя этого шлюза — не перепутать с другой открытой вкладкой"
            >
              {hostname}
            </span>
          )}
        </h1>
        {subtitle && <div className="mt-0.5 font-mono text-[12.5px] text-text-muted">{subtitle}</div>}
      </div>

      <div className="flex items-center gap-3.5">
        {live !== undefined && (
          <div className="flex items-center gap-1.5 rounded-full border border-border px-3 py-1 font-mono text-[11px] text-text-muted">
            <span
              className={`h-1.5 w-1.5 rounded-full ${live ? 'bg-success shadow-[0_0_0_3px_var(--success-dim)]' : 'bg-text-muted'}`}
              style={live ? { animation: 'shattl-pulse 2s infinite' } : undefined}
            />
            {live ? 'live' : 'reconnecting'}
          </div>
        )}

        <div className="flex gap-0.5 rounded-lg border border-border p-0.5">
          {densities.map((d) => (
            <button
              key={d.value}
              onClick={() => setDensity(d.value)}
              className={`rounded-md px-2.5 py-1 text-xs transition-colors ${
                density === d.value ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted'
              }`}
            >
              {d.label}
            </button>
          ))}
        </div>

        <div className="flex gap-0.5 rounded-lg border border-border p-0.5">
          <button
            onClick={() => setTheme('dark')}
            className={`rounded-md px-2.5 py-1 text-xs transition-colors ${
              theme === 'dark' ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted'
            }`}
          >
            Dark
          </button>
          <button
            onClick={() => setTheme('light')}
            className={`rounded-md px-2.5 py-1 text-xs transition-colors ${
              theme === 'light' ? 'border border-border-strong bg-surface-raised text-text' : 'text-text-muted'
            }`}
          >
            Light
          </button>
        </div>

        <a href={withBase('/logout')} className="font-mono text-[11px] text-text-muted hover:text-text-secondary">
          logout
        </a>
      </div>
    </div>
  )
}
