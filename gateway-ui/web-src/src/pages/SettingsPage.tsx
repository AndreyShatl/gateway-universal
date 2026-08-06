import { TopBar } from '../components/TopBar'

export function SettingsPage() {
  return (
    <div>
      <TopBar title="Settings" />
      <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad) text-[12.5px] text-text-secondary">
        Тема и плотность интерфейса — в верхней панели на каждой странице.
      </div>
    </div>
  )
}
