import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import {
  ApiError,
  clearApiSession,
  getAuthSession,
  getCapabilities,
  getReadiness,
  logout,
  setUnauthorizedHandler,
  type AuthSession,
  type Capabilities,
  type Readiness,
} from './api'
import { LoginView } from './components/LoginView'
import { Workbench } from './components/Workbench'
import { errorMessage } from './ui'

type LoadState =
  | { kind: 'loading' }
  | { kind: 'guest'; message?: string }
  | { kind: 'ready'; capabilities: Capabilities; session: AuthSession }
  | { kind: 'unavailable'; message: string }

function LoadingWorkspace() {
  return <section className="state-panel auth-loading" aria-busy="true" aria-labelledby="loading-title"><span className="spinner" aria-hidden="true" /><div><h2 id="loading-title">Проверяем защищённую сессию</h2><p role="status">Подключаем каталог программ LinuxCNC…</p></div></section>
}

function BackendUnavailable({ message, onRetry }: { message: string; onRetry: () => void }) {
  return <section className="state-panel state-panel--error" role="alert" aria-labelledby="offline-title"><span className="state-icon" aria-hidden="true">!</span><div><p className="eyebrow">Связь прервана</p><h2 id="offline-title">Web Setup Manager недоступен</h2><p>{message} Локальное состояние интерфейса не будет сброшено.</p><button className="button button--primary" type="button" onClick={onRetry}>Повторить подключение</button></div></section>
}

function AuthShell({ children, stateLabel }: { children: ReactNode; stateLabel: string }) {
  return <div className="app-shell app-shell--auth">
    <a className="skip-link" href="#main-content">К основному содержимому</a>
    <header className="app-header">
      <div className="brand"><span className="brand__mark" aria-hidden="true">WS</span><span><strong>Web Setup Manager</strong><small>Каталог программ LinuxCNC</small></span></div>
      <div className="service-state" aria-label="Состояние приложения"><span className="service-state__dot" aria-hidden="true" />{stateLabel}</div>
    </header>
    <main id="main-content" className="main-content main-content--auth" tabIndex={-1}>{children}</main>
  </div>
}

export function App() {
  const [state, setState] = useState<LoadState>({ kind: 'loading' })
  const [networkOffline, setNetworkOffline] = useState(() => !navigator.onLine)
  const [attempt, setAttempt] = useState(0)
  const [readiness, setReadiness] = useState<Readiness>({ ok: true })
  const [readinessAttempt, setReadinessAttempt] = useState(0)
  const [loggingOut, setLoggingOut] = useState(false)
  const [logoutError, setLogoutError] = useState<string>()
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
    document.getElementById('catalog-editor')?.focus()
  }, [state.kind])

  useEffect(() => {
    const onOnline = () => { setNetworkOffline(false); if (state.kind === 'unavailable') retry() }
    const onOffline = () => setNetworkOffline(true)
    window.addEventListener('online', onOnline)
    window.addEventListener('offline', onOffline)
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
      if (!(reason instanceof ApiError && reason.status === 401)) setLogoutError(errorMessage(reason))
    } finally { setLoggingOut(false) }
  }

  if (state.kind === 'ready') {
    return <Workbench
      capabilities={state.capabilities}
      username={state.session.user?.username}
      loginRequired={state.session.loginRequired}
      networkOffline={networkOffline}
      readiness={readiness}
      loggingOut={loggingOut}
      logoutError={logoutError}
      onLogout={() => void signOut()}
      onRetryReadiness={() => setReadinessAttempt((value) => value + 1)}
    />
  }

  const label = state.kind === 'guest' ? 'Требуется вход'
    : state.kind === 'loading' ? 'Проверка сессии' : 'Сервис недоступен'
  return <AuthShell stateLabel={label}>
    {state.kind === 'loading' ? <LoadingWorkspace /> : null}
    {state.kind === 'unavailable' ? <BackendUnavailable message={state.message} onRetry={retry} /> : null}
    {state.kind === 'guest' ? <LoginView message={state.message} onAuthenticated={onAuthenticated} /> : null}
  </AuthShell>
}
