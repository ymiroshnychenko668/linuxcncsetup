import type { CurrentSetup, Setup } from '../domain'
import { formatDate, statusLabels } from '../ui'

interface Props {
  current: CurrentSetup | null
  setup?: Setup
  loading: boolean
  error?: string
  onOpen: (setupId: string) => void
  onClear: () => void
  onRetry: () => void
}

export function CurrentSetupPanel({ current, setup, loading, error, onOpen, onClear, onRetry }: Props) {
  const needsAttention = Boolean(current && setup && (
    setup.status !== 'ready' || setup.revision !== current.revisionSelected
  ))
  return (
    <section className={`current-setup${needsAttention ? ' current-setup--attention' : ''}`} aria-labelledby="current-setup-title">
      <div className="current-setup__marker" aria-hidden="true" />
      <div className="current-setup__copy">
        <p className="eyebrow">Текущий сетап</p>
        <h2 id="current-setup-title">{loading ? 'Загружаем…' : setup?.name ?? (current ? 'Текущий сетап недоступен' : 'Не выбран')}</h2>
        {error ? <p className="form-error" role="alert">{error} Выбор не изменён.</p> : null}
        {current && setup ? (
          <p>
            Revision при выборе: {current.revisionSelected} · выбран {formatDate(current.selectedAt)} · сейчас {statusLabels[setup.status]}.
            {needsAttention ? ' Текущий сетап изменился или требует внимания; выбор сохранён, исполнение заблокировано.' : ''}
          </p>
        ) : (
          <p>После проверки готовый сетап можно явно закрепить здесь. Выбор ничего не запускает, не копирует и не исполняет.</p>
        )}
      </div>
      <div className="current-setup__actions">
        {error ? <button className="button button--quiet" type="button" onClick={onRetry}>Повторить загрузку текущего</button> : null}
        <button className="button button--quiet" type="button" disabled={!current} onClick={() => current && onOpen(current.setupId)}>
          Открыть текущий
        </button>
        {current ? <button className="button button--quiet" type="button" onClick={onClear}>Снять выбор</button> : null}
      </div>
    </section>
  )
}
