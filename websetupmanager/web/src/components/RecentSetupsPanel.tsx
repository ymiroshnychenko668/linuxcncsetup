import type { RecentSetup } from '../domain'
import { formatDate, statusLabels } from '../ui'

interface Props {
  items: RecentSetup[]
  loading: boolean
  error?: string
  onOpen: (item: RecentSetup) => void
  onDelete: (setupId: string) => void
  onClear: () => void
  onRetry: () => void
}

export function RecentSetupsPanel({ items, loading, error, onOpen, onDelete, onClear, onRetry }: Props) {
  if (loading) return <section className="recent-panel" aria-busy="true"><p role="status">Загружаем недавние сетапы…</p></section>
  if (error) return <section className="recent-panel recent-panel--error" role="alert"><p>Недавние сетапы недоступны: {error}</p><button className="button button--quiet" type="button" onClick={onRetry}>Повторить</button></section>
  if (items.length === 0) return null
  return (
    <section className="recent-panel" aria-labelledby="recent-title">
      <div className="section-heading"><div><p className="eyebrow">История работы</p><h2 id="recent-title">Недавние сетапы</h2></div><button className="button button--quiet" type="button" onClick={onClear}>Очистить список</button></div>
      <ul className="recent-list">
        {items.map((item) => <li key={item.setupId}>
          <button className="recent-list__open" type="button" onClick={() => onOpen(item)}>
            <strong>{item.setupName}</strong>
            <span>{statusLabels[item.setupStatus]} · {formatDate(item.lastOpenedAt)}{item.lastLine ? ` · строка ${item.lastLine}` : ''}</span>
          </button>
          <button className="icon-button" type="button" aria-label={`Удалить ${item.setupName} из недавних`} onClick={() => onDelete(item.setupId)}>×</button>
        </li>)}
      </ul>
    </section>
  )
}
