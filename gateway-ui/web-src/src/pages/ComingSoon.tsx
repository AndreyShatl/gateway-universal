import { TopBar } from '../components/TopBar'
import { withBase } from '../lib/basePath'

export function ComingSoon({ title }: { title: string }) {
  return (
    <div>
      <TopBar title={title} />
      <div className="rounded-[--card-radius] border border-border bg-surface p-(--card-pad) text-[12.5px] text-text-secondary">
        Этот раздел ещё не перенесён на новый интерфейс — пока доступен на{' '}
        <a href={withBase('/legacy')} className="text-accent hover:underline">
          старой панели
        </a>
        .
      </div>
    </div>
  )
}
