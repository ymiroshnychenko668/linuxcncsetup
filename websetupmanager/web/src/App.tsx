import { useCallback, useEffect, useRef, useState } from 'react'
import {
  ApiError,
  clearApiSession,
  clearRecentSetups,
  clearCurrentSetup,
  deleteRecentSetup,
  getCapabilities,
  getAuthSession,
  getCurrentSetup,
  getReadiness,
  getSetup,
  getUIState,
  listSetups,
  listRecentSetups,
  logout,
  putUIState,
  setUnauthorizedHandler,
  touchRecentSetup,
  type Capabilities,
  type AuthSession,
  type Readiness,
  type SetupQuery,
} from './api'
import type { CurrentSetup, RecentSetup, Setup, SetupStatus, SetupSummary } from './domain'
import { stableClientID } from './clientState'
import { CreateSetupDialog } from './components/CreateSetupDialog'
import { CurrentSetupPanel } from './components/CurrentSetupPanel'
import { ImportWizard } from './components/ImportWizard'
import { LoginView } from './components/LoginView'
import { RecentSetupsPanel } from './components/RecentSetupsPanel'
import { ConfirmOperationDialog } from './components/SetupOperationDialogs'
import { SetupDetail } from './components/SetupDetail'
import { SetupLibrary, type LibraryFilters } from './components/SetupLibrary'
import { errorMessage } from './ui'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'guest'; message?: string }
  | { kind: 'ready'; capabilities: Capabilities; session: AuthSession }
  | { kind: 'unavailable'; message: string }

const initialFilters: LibraryFilters = {
  query: '', status: 'active', sheet: 'any', current: 'any', sort: 'updated_desc',
}

function restoredFilters(value: Record<string, unknown>): LibraryFilters {
  const status = ['active', 'draft', 'ready', 'attention', 'archived', 'all'].includes(String(value.status))
    ? value.status as LibraryFilters['status'] : initialFilters.status
  const sheet = ['any', 'yes', 'no'].includes(String(value.sheet))
    ? value.sheet as LibraryFilters['sheet'] : initialFilters.sheet
  const current = ['any', 'yes', 'no'].includes(String(value.current))
    ? value.current as LibraryFilters['current'] : initialFilters.current
  const sort = ['updated_desc', 'updated_asc', 'name_asc', 'name_desc', 'recent_desc'].includes(String(value.sort))
    ? value.sort as LibraryFilters['sort'] : initialFilters.sort
  return { query: typeof value.query === 'string' ? value.query.slice(0, 500) : '', status, sheet, current, sort }
}

function queryFor(filters: LibraryFilters, cursor?: string): SetupQuery {
  let statuses: SetupStatus[] | undefined
  if (filters.status === 'all') statuses = ['draft', 'ready', 'attention', 'archived']
  else if (filters.status !== 'active') statuses = [filters.status]
  return {
    query: filters.query || undefined,
    statuses,
    hasSetupSheet: filters.sheet === 'any' ? undefined : filters.sheet === 'yes',
    current: filters.current === 'any' ? undefined : filters.current === 'yes',
    sort: filters.sort,
    cursor,
    limit: 24,
  }
}

function LoadingLibrary() {
  return <section className="state-panel auth-loading" aria-busy="true" aria-labelledby="loading-title"><span className="spinner" aria-hidden="true" /><div><h2 id="loading-title">Проверяем защищённую сессию</h2><p role="status">Подготавливаем Web Setup Manager и управляемое хранилище…</p></div></section>
}

function BackendUnavailable({ message, onRetry }: { message: string; onRetry: () => void }) {
  return <section className="state-panel state-panel--error" role="alert" aria-labelledby="offline-title"><span className="state-icon" aria-hidden="true">!</span><div><p className="eyebrow">Связь прервана</p><h2 id="offline-title">Локальный Backend недоступен</h2><p>{message} Состояние интерфейса не будет сброшено.</p><button className="button button--primary" type="button" onClick={onRetry}>Повторить подключение</button></div></section>
}

function Workspace({ capabilities }: { capabilities: Capabilities }) {
  const [clientId] = useState(stableClientID)
  const [stateRestored, setStateRestored] = useState(false)
  const [filters, setFilters] = useState(initialFilters)
  const [items, setItems] = useState<SetupSummary[]>([])
  const [nextCursor, setNextCursor] = useState<string>()
  const [loading, setLoading] = useState(true)
  const [loadingMore, setLoadingMore] = useState(false)
  const [libraryError, setLibraryError] = useState<string>()
  const [libraryRefresh, setLibraryRefresh] = useState(0)
  const [current, setCurrent] = useState<CurrentSetup | null>(null)
  const [currentSetup, setCurrentSetupState] = useState<Setup>()
  const [currentLoading, setCurrentLoading] = useState(true)
  const [currentError, setCurrentError] = useState<string>()
  const [view, setView] = useState<'library' | 'detail'>('library')
  const [requestedSetupId, setRequestedSetupId] = useState<string>()
  const [selectedSetup, setSelectedSetup] = useState<Setup>()
  const [detailLoading, setDetailLoading] = useState(false)
  const [detailError, setDetailError] = useState<string>()
  const [createOpen, setCreateOpen] = useState(false)
  const [importOpen, setImportOpen] = useState(false)
  const [clearOpen, setClearOpen] = useState(false)
  const [selectedArtifactId, setSelectedArtifactId] = useState<string>()
  const [selectedLine, setSelectedLine] = useState<number>()
  const [recent, setRecent] = useState<RecentSetup[]>([])
  const [recentLoading, setRecentLoading] = useState(true)
  const [recentError, setRecentError] = useState<string>()
  const [recentRefresh, setRecentRefresh] = useState(0)

  useEffect(() => {
    if (!stateRestored) return
    const controller = new AbortController()
    setLoading(true)
    setLibraryError(undefined)
    void listSetups(queryFor(filters), controller.signal).then(
      (page) => { setItems(page.items); setNextCursor(page.nextCursor); setLoading(false) },
      (reason: unknown) => {
        if (reason instanceof DOMException && reason.name === 'AbortError') return
        setLibraryError(errorMessage(reason)); setLoading(false)
      },
    )
    return () => controller.abort()
  }, [filters, libraryRefresh, stateRestored])

  const loadCurrent = useCallback(async () => {
    setCurrentLoading(true)
    setCurrentError(undefined)
    try {
      const selection = await getCurrentSetup()
      setCurrent(selection)
      if (selection) {
        try {
          setCurrentSetupState(await getSetup(selection.setupId))
        } catch (reason) {
          setCurrentSetupState((loaded) => loaded?.setupId === selection.setupId ? loaded : undefined)
          setCurrentError(errorMessage(reason))
        }
      }
      else setCurrentSetupState(undefined)
    } catch (reason) {
      setCurrentError(errorMessage(reason))
    } finally {
      setCurrentLoading(false)
    }
  }, [])

  const openSetup = useCallback(async (setupId: string, artifactId?: string, line?: number) => {
    setRequestedSetupId(setupId)
    setView('detail')
    setDetailLoading(true)
    setDetailError(undefined)
    try {
      const loaded = await getSetup(setupId)
      setSelectedSetup(loaded)
      const artifact = artifactId && loaded.artifacts.some((item) => item.artifactId === artifactId)
        ? artifactId
        : loaded.artifacts.find((item) => item.role === 'program')?.artifactId
      setSelectedArtifactId(artifact)
      setSelectedLine(artifact && line && line > 0 ? line : undefined)
      void touchRecentSetup(setupId, artifact).then(() => setRecentRefresh((value) => value + 1), () => undefined)
    }
    catch (reason) { setSelectedSetup(undefined); setDetailError(errorMessage(reason)) }
    finally { setDetailLoading(false) }
  }, [])

  useEffect(() => { void loadCurrent() }, [loadCurrent])

  useEffect(() => {
    const controller = new AbortController()
    void getUIState(clientId, controller.signal).then(async (saved) => {
      setFilters(restoredFilters(saved.filters))
      if (saved.screen === 'detail' && saved.selectedSetupId) {
        await openSetup(
          saved.selectedSetupId,
          saved.selectedArtifactId,
          typeof saved.view.line === 'number' ? saved.view.line : undefined,
        )
      }
    }, () => undefined).finally(() => setStateRestored(true))
    return () => controller.abort()
  }, [clientId, openSetup])

  useEffect(() => {
    const showLibrary = () => setView('library')
    window.addEventListener('wsm:library', showLibrary)
    return () => window.removeEventListener('wsm:library', showLibrary)
  }, [])

  useEffect(() => {
    if (!stateRestored) return
    const timeout = window.setTimeout(() => {
      void putUIState({
        clientId,
        screen: view,
        selectedSetupId: view === 'detail' ? selectedSetup?.setupId ?? requestedSetupId : undefined,
        selectedArtifactId: view === 'detail' ? selectedArtifactId : undefined,
        filters: { ...filters },
        view: selectedLine ? { line: selectedLine } : {},
      }).catch(() => undefined)
    }, 350)
    return () => window.clearTimeout(timeout)
  }, [clientId, filters, requestedSetupId, selectedArtifactId, selectedLine, selectedSetup?.setupId, stateRestored, view])

  useEffect(() => {
    if (!stateRestored || view !== 'detail' || !selectedSetup || !selectedArtifactId) return
    const timeout = window.setTimeout(() => {
      void touchRecentSetup(selectedSetup.setupId, selectedArtifactId, selectedLine ?? 0)
        .then(() => setRecentRefresh((value) => value + 1), () => undefined)
    }, 900)
    return () => window.clearTimeout(timeout)
  }, [selectedArtifactId, selectedLine, selectedSetup, stateRestored, view])

  useEffect(() => {
    if (!stateRestored) return
    const controller = new AbortController()
    setRecentLoading(true)
    setRecentError(undefined)
    void listRecentSetups(controller.signal).then(
      (items) => { setRecent(items); setRecentLoading(false) },
      (reason: unknown) => {
        if (reason instanceof DOMException && reason.name === 'AbortError') return
        setRecentError(errorMessage(reason)); setRecentLoading(false)
      },
    )
    return () => controller.abort()
  }, [recentRefresh, stateRestored])

  const reloadSelected = useCallback(async () => {
    if (!selectedSetup) return
    const fresh = await getSetup(selectedSetup.setupId)
    setSelectedSetup(fresh)
    setLibraryRefresh((value) => value + 1)
    if (current?.setupId === fresh.setupId) setCurrentSetupState(fresh)
  }, [current?.setupId, selectedSetup])

  const changed = (setup: Setup) => {
    setSelectedSetup(setup)
    if (selectedArtifactId && !setup.artifacts.some((item) => item.artifactId === selectedArtifactId)) {
      setSelectedArtifactId(undefined)
      setSelectedLine(undefined)
    }
    if (current?.setupId === setup.setupId) setCurrentSetupState(setup)
    setLibraryRefresh((value) => value + 1)
  }

  const loadMore = async () => {
    if (!nextCursor || loadingMore) return
    setLoadingMore(true)
    setLibraryError(undefined)
    try {
      const page = await listSetups(queryFor(filters, nextCursor))
      setItems((currentItems) => {
        const existing = new Set(currentItems.map((item) => item.setupId))
        return [...currentItems, ...page.items.filter((item) => !existing.has(item.setupId))]
      })
      setNextCursor(page.nextCursor)
    } catch (reason) { setLibraryError(errorMessage(reason)) }
    finally { setLoadingMore(false) }
  }

  const created = (setup: Setup) => {
    setCreateOpen(false)
    setImportOpen(false)
    changed(setup)
    setView('detail')
  }

  const openSetupFromUI = useCallback((setupId: string) => {
    void openSetup(setupId)
  }, [openSetup])

  return (
    <>
      <CurrentSetupPanel
        current={current} setup={currentSetup} loading={currentLoading} error={currentError}
        onOpen={openSetupFromUI} onClear={() => setClearOpen(true)} onRetry={() => void loadCurrent()}
      />
      {view === 'library' ? <RecentSetupsPanel
        items={recent} loading={recentLoading} error={recentError}
        onOpen={(item) => void openSetup(item.setupId, item.lastArtifactId, item.lastLine)}
        onDelete={(id) => { void deleteRecentSetup(id).then(() => setRecentRefresh((value) => value + 1), (reason: unknown) => setRecentError(errorMessage(reason))) }}
        onClear={() => { void clearRecentSetups().then(() => setRecentRefresh((value) => value + 1), (reason: unknown) => setRecentError(errorMessage(reason))) }}
        onRetry={() => setRecentRefresh((value) => value + 1)}
      /> : null}
      {view === 'library' ? <SetupLibrary
        alias={capabilities.libraryAlias} items={items} filters={filters} loading={loading} loadingMore={loadingMore}
        error={libraryError} nextCursor={nextCursor} onFiltersChange={setFilters}
        onResetFilters={() => setFilters(initialFilters)}
        onRetry={() => setLibraryRefresh((value) => value + 1)} onLoadMore={() => void loadMore()}
        onOpen={openSetupFromUI} onCreate={() => setCreateOpen(true)} onImport={() => setImportOpen(true)}
      /> : null}
      {view === 'detail' && detailLoading ? <section className="state-panel" aria-busy="true"><span className="spinner" aria-hidden="true" /><p role="status">Загружаем карточку сетапа…</p></section> : null}
      {view === 'detail' && detailError ? <section className="state-panel state-panel--error" role="alert"><div><h2>Карточку не удалось загрузить</h2><p>{detailError}</p><div className="form-actions"><button className="button button--quiet" type="button" onClick={() => setView('library')}>К библиотеке</button><button className="button button--primary" type="button" onClick={() => { if (requestedSetupId) void openSetup(requestedSetupId) }}>Повторить</button></div></div></section> : null}
      {view === 'detail' && !detailLoading && !detailError && selectedSetup ? <SetupDetail
        setup={selectedSetup} current={current} onBack={() => setView('library')} onChanged={changed}
        onReload={reloadSelected} onOpenSetup={openSetupFromUI}
        onCurrentChanged={async () => { await loadCurrent(); setLibraryRefresh((value) => value + 1) }}
        onDeleted={() => { setSelectedSetup(undefined); setView('library'); setLibraryRefresh((value) => value + 1) }}
        selectedArtifactId={selectedArtifactId} initialLine={selectedLine}
        onSelectedArtifact={(artifactId, line) => { setSelectedArtifactId(artifactId); setSelectedLine(line) }}
      /> : null}
      {createOpen ? <CreateSetupDialog onClose={() => setCreateOpen(false)} onCreated={created} /> : null}
      {importOpen ? <ImportWizard capabilities={capabilities} onClose={() => setImportOpen(false)} onImported={created} /> : null}
      {clearOpen ? <ConfirmOperationDialog
        title="Снять выбор текущего сетапа" description="Закрепление будет очищено. Файлы и карточка сетапа останутся в библиотеке."
        confirmLabel="Снять выбор" onClose={() => setClearOpen(false)}
        onReload={loadCurrent}
        onConfirm={async (key) => {
          if (!current) throw new Error('Текущий сетап уже изменён.')
          await clearCurrentSetup(current, key)
          await loadCurrent()
          setLibraryRefresh((value) => value + 1)
        }}
      /> : null}
    </>
  )
}

export function App() {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [networkOffline, setNetworkOffline] = useState(() => !navigator.onLine)
  const [attempt, setAttempt] = useState(0)
  const [readiness, setReadiness] = useState<Readiness>({ ok: true })
  const [readinessAttempt, setReadinessAttempt] = useState(0)
  const [loggingOut, setLoggingOut] = useState(false)
  const [logoutError, setLogoutError] = useState<string>()
  const mainRef = useRef<HTMLElement>(null)
  const focusWorkspace = useRef(false)
  const retry = useCallback(() => { setState({ kind: 'loading' }); setAttempt((value) => value + 1) }, [])

  useEffect(() => {
    setUnauthorizedHandler(() => {
      setLoggingOut(false)
      setLogoutError(undefined)
      setReadiness({ ok: true })
      setState({ kind: 'guest', message: 'Сессия истекла. Войдите снова, чтобы продолжить.' })
    })
    return () => setUnauthorizedHandler(undefined)
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    const load = async () => {
      try {
        const session = await getAuthSession(controller.signal)
        if (!session.authenticated) {
          setState({ kind: 'guest' })
          return
        }
        const capabilities = await getCapabilities(controller.signal)
        setState({ kind: 'ready', capabilities, session })
      } catch (error) {
        if (error instanceof DOMException && error.name === 'AbortError') return
        if (error instanceof ApiError && error.status === 401) {
          clearApiSession()
          setState({ kind: 'guest' })
          return
        }
        setState({ kind: 'unavailable', message: error instanceof ApiError ? error.message : 'Не удалось получить конфигурацию приложения.' })
      }
    }
    void load()
    return () => controller.abort()
  }, [attempt])

  const onAuthenticated = useCallback((session: AuthSession) => {
    focusWorkspace.current = true
    setLogoutError(undefined)
    setState({ kind: 'loading' })
    void getCapabilities().then(
      (capabilities) => setState({ kind: 'ready', capabilities, session }),
      (error: unknown) => {
        if (error instanceof DOMException && error.name === 'AbortError') return
        if (error instanceof ApiError && error.status === 401) {
          clearApiSession()
          setState({ kind: 'guest', message: 'Сессия истекла. Войдите снова, чтобы продолжить.' })
          return
        }
        setState({ kind: 'unavailable', message: error instanceof ApiError ? error.message : 'Не удалось получить конфигурацию приложения.' })
      },
    )
  }, [])

  useEffect(() => {
    if (state.kind !== 'ready' || !focusWorkspace.current) return
    focusWorkspace.current = false
    mainRef.current?.focus()
  }, [state.kind])

  useEffect(() => {
    const onOnline = () => { setNetworkOffline(false); if (state.kind === 'unavailable') retry() }
    const onOffline = () => setNetworkOffline(true)
    window.addEventListener('online', onOnline); window.addEventListener('offline', onOffline)
    return () => { window.removeEventListener('online', onOnline); window.removeEventListener('offline', onOffline) }
  }, [retry, state.kind])

  useEffect(() => {
    if (state.kind !== 'ready') return
    const controller = new AbortController()
    const check = () => { void getReadiness(controller.signal).then(setReadiness, (reason: unknown) => {
      if (reason instanceof DOMException && reason.name === 'AbortError') return
      setReadiness({ ok: false, code: 'BACKEND_UNAVAILABLE', message: errorMessage(reason) })
    }) }
    check()
    const interval = window.setInterval(check, 5000)
    return () => { controller.abort(); window.clearInterval(interval) }
  }, [readinessAttempt, state.kind])

  const signOut = async () => {
    if (loggingOut) return
    setLoggingOut(true)
    setLogoutError(undefined)
    try {
      await logout()
      setReadiness({ ok: true })
      setState({ kind: 'guest', message: 'Вы вышли из Web Setup Manager.' })
    } catch (reason) {
      if (!(reason instanceof ApiError && reason.status === 401)) {
        setLogoutError(errorMessage(reason))
      }
    } finally {
      setLoggingOut(false)
    }
  }

  return <div className="app-shell">
    <a className="skip-link" href="#main-content">К основному содержимому</a>
    <header className="app-header">
      {state.kind === 'ready' ? <button className="brand" type="button" aria-label="Web Setup Manager — библиотека" onClick={() => window.dispatchEvent(new Event('wsm:library'))}><span className="brand__mark" aria-hidden="true">WS</span><span><strong>Web Setup Manager</strong><small>Технологические комплекты станка</small></span></button> : <div className="brand"><span className="brand__mark" aria-hidden="true">WS</span><span><strong>Web Setup Manager</strong><small>Технологические комплекты станка</small></span></div>}
      {state.kind === 'ready' && state.session.loginRequired ? <div className="auth-session" aria-label={`Выполнен вход: ${state.session.user?.username ?? ''}`}><span><small>Пользователь</small><strong>{state.session.user?.username}</strong></span><button className="button auth-session__logout" type="button" disabled={loggingOut} aria-busy={loggingOut} onClick={() => void signOut()}>{loggingOut ? <span className="spinner spinner--small" aria-hidden="true" /> : null}{loggingOut ? 'Выходим…' : 'Выйти'}</button></div> : <div className="service-state" aria-label="Состояние приложения"><span className="service-state__dot" aria-hidden="true" />{state.kind === 'guest' ? 'Требуется вход' : state.kind === 'loading' ? 'Проверка сессии' : state.kind === 'unavailable' ? 'Сервис недоступен' : 'Локальный режим'}</div>}
    </header>
    {networkOffline ? <div className="network-notice" role="status">{state.kind === 'ready' && !state.session.loginRequired ? 'Внешняя сеть недоступна. Setup Manager продолжает работать с локальным Backend.' : 'Сеть недоступна. Проверьте соединение с Backend Web Setup Manager.'}</div> : null}
    {!readiness.ok && state.kind === 'ready' ? <div className="critical-notice" role="alert"><div><strong>{readiness.code === 'STORAGE_UNAVAILABLE' ? 'Управляемое хранилище недоступно' : 'Локальный сервис временно не готов'}</strong><span>{readiness.code === 'STORAGE_UNAVAILABLE' ? ' Физическое хранилище недоступно; карточки и фильтры сохранены, публикация файлов заблокирована.' : ` ${readiness.message ?? ''} Интерфейс и несохранённые поля не сброшены.`}</span></div><button className="button button--quiet" type="button" onClick={() => setReadinessAttempt((value) => value + 1)}>Проверить снова</button></div> : null}
    {logoutError && state.kind === 'ready' ? <div className="critical-notice" role="alert"><div><strong>Не удалось выйти</strong><span> {logoutError} Рабочая область остаётся открытой.</span></div></div> : null}
    <main ref={mainRef} id="main-content" className={`main-content ${state.kind === 'ready' ? '' : 'main-content--auth'}`} tabIndex={-1}>
      {state.kind === 'loading' ? <LoadingLibrary /> : null}
      {state.kind === 'unavailable' ? <BackendUnavailable message={state.message} onRetry={retry} /> : null}
      {state.kind === 'guest' ? <LoginView message={state.message} onAuthenticated={onAuthenticated} /> : null}
      {state.kind === 'ready' ? <Workspace capabilities={state.capabilities} /> : null}
    </main>
  </div>
}
