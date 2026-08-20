import { useCallback, useEffect, useState } from 'react'
import { ApiError, getCapabilities, type Capabilities } from './api'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'ready'; capabilities: Capabilities }
  | { kind: 'unavailable'; message: string }

function LoadingLibrary() {
  return (
    <section className="state-panel" aria-busy="true" aria-labelledby="loading-title">
      <span className="spinner" aria-hidden="true" />
      <div>
        <h2 id="loading-title">Загружаем библиотеку сетапов</h2>
        <p role="status">Проверяем локальный Backend и управляемое хранилище…</p>
      </div>
    </section>
  )
}
function BackendUnavailable({ message, onRetry }: { message: string; onRetry: () => void }) {
  return (
    <section className="state-panel state-panel--error" role="alert" aria-labelledby="offline-title">
      <span className="state-icon" aria-hidden="true">!</span>
      <div>
        <p className="eyebrow">Связь прервана</p>
        <h2 id="offline-title">Локальный Backend недоступен</h2>
        <p>{message} Состояние интерфейса не будет сброшено.</p>
        <button className="button button--primary" type="button" onClick={onRetry}>
          Повторить подключение
        </button>
      </div>
    </section>
  )
}

function LibraryShell({ capabilities }: { capabilities: Capabilities }) {
  return (
    <>
      <section className="current-setup" aria-labelledby="current-setup-title">
        <div className="current-setup__marker" aria-hidden="true" />
        <div className="current-setup__copy">
          <p className="eyebrow">Текущий сетап</p>
          <h2 id="current-setup-title">Не выбран</h2>
          <p>
            После проверки готовый сетап можно явно закрепить здесь. Выбор ничего не
            запускает и не исполняет.
          </p>
        </div>
        <button className="button button--quiet" type="button" disabled>
          Открыть текущий
        </button>
      </section>

      <section className="library" aria-labelledby="library-title">
        <div className="library__heading">
          <div>
            <p className="eyebrow">Локальная библиотека</p>
            <h1 id="library-title">{capabilities.libraryAlias}</h1>
          </div>
          <div className="library__actions" aria-label="Действия библиотеки">
            <button className="button button--quiet" type="button" disabled>
              Создать сетап
            </button>
            <button className="button button--primary" type="button" disabled>
              Импортировать комплект
            </button>
          </div>
        </div>

        <form className="library-tools" role="search" onSubmit={(event) => event.preventDefault()}>
          <label className="search-field">
            <span>Поиск сетапов</span>
            <input
              type="search"
              placeholder="Название, описание или программа"
              disabled
            />
          </label>
          <label>
            <span>Статус</span>
            <select disabled defaultValue="active">
              <option value="active">Активные</option>
            </select>
          </label>
          <label>
            <span>Сортировка</span>
            <select disabled defaultValue="updated">
              <option value="updated">Недавно изменённые</option>
            </select>
          </label>
        </form>

        <div className="empty-library">
          <div className="empty-library__illustration" aria-hidden="true">
            <span />
            <span />
            <span />
          </div>
          <p className="eyebrow">Библиотека готова</p>
          <h2>Здесь появятся технологические сетапы</h2>
          <p>
            Каждый сетап объединяет G-code-программы, одну Setup Sheet, метаданные,
            revision и состояние готовности.
          </p>
        </div>
      </section>
    </>
  )
}

export function App() {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [networkOffline, setNetworkOffline] = useState(() => !navigator.onLine)
  const [attempt, setAttempt] = useState(0)

  const retry = useCallback(() => {
    setState({ kind: 'loading' })
    setAttempt((value) => value + 1)
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void getCapabilities(controller.signal).then(
      (capabilities) => setState({ kind: 'ready', capabilities }),
      (error: unknown) => {
        if (error instanceof DOMException && error.name === 'AbortError') return
        const message = error instanceof ApiError
          ? error.message
          : 'Не удалось получить конфигурацию приложения.'
        setState({ kind: 'unavailable', message })
      },
    )
    return () => controller.abort()
  }, [attempt])

  useEffect(() => {
    const onOnline = () => {
      setNetworkOffline(false)
      if (state.kind === 'unavailable') retry()
    }
    const onOffline = () => setNetworkOffline(true)
    window.addEventListener('online', onOnline)
    window.addEventListener('offline', onOffline)
    return () => {
      window.removeEventListener('online', onOnline)
      window.removeEventListener('offline', onOffline)
    }
  }, [retry, state.kind])

  return (
    <div className="app-shell">
      <a className="skip-link" href="#main-content">К основному содержимому</a>
      <header className="app-header">
        <a className="brand" href="/" aria-label="Web Setup Manager — библиотека">
          <span className="brand__mark" aria-hidden="true">WS</span>
          <span>
            <strong>Web Setup Manager</strong>
            <small>Технологические комплекты станка</small>
          </span>
        </a>
        <div className="service-state" aria-label="Состояние приложения">
          <span className="service-state__dot" aria-hidden="true" />
          Локальный режим
        </div>
      </header>

      {networkOffline ? (
        <div className="network-notice" role="status">
          Внешняя сеть недоступна. Setup Manager продолжает работать с локальным Backend.
        </div>
      ) : null}

      <main id="main-content" className="main-content" tabIndex={-1}>
        {state.kind === 'loading' ? <LoadingLibrary /> : null}
        {state.kind === 'unavailable' ? (
          <BackendUnavailable message={state.message} onRetry={retry} />
        ) : null}
        {state.kind === 'ready' ? (
          <LibraryShell capabilities={state.capabilities} />
        ) : null}
      </main>
    </div>
  )
}
