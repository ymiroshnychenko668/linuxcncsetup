import { useCallback, useEffect, useRef, useState, type ReactNode } from 'react'
import {
	activateExplicitAuthSession,
	acceptExplicitAuthSession,
  ApiError,
  clearApiSession,
  getAuthSession,
  getCapabilities,
  getReadiness,
  logout,
	logoutSessionIfCurrent,
	quarantineExplicitAuthSession,
	reconcileStaleAuthSession,
  setUnauthorizedHandler,
  type AuthSession,
  type Capabilities,
  type Readiness,
} from './api'
import { LoginView } from './components/LoginView'
import { Workbench } from './components/Workbench'
import {
	allowGCodeCacheScope,
	blockGCodeCacheScope,
	captureDurableGCodeCacheAuthGeneration,
	captureGCodeCacheAuthGeneration,
	clearGCodeCacheScope,
} from './gcodeCache'
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

function gcodeCacheScope(session: AuthSession, capabilities: Capabilities): string {
	return `${session.user?.username ? `user:${session.user.username}` : 'local'}:${capabilities.libraryId ?? capabilities.libraryAlias}`
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
  const cacheScopeRef = useRef<string>()
	const authAttemptGenerationRef = useRef(0)
	const authContinuationControllerRef = useRef<AbortController>()
  const retry = useCallback(() => {
		authAttemptGenerationRef.current += 1
		authContinuationControllerRef.current?.abort()
		setState({ kind: 'loading' })
		setAttempt((value) => value + 1)
	}, [])

  useEffect(() => {
    setUnauthorizedHandler(() => {
		authAttemptGenerationRef.current += 1
		authContinuationControllerRef.current?.abort()
      const scope = cacheScopeRef.current
      if (scope) {
	        void blockGCodeCacheScope(scope).then((token) => clearGCodeCacheScope(scope, token))
      }
      setLoggingOut(false)
      setLogoutError(undefined)
      setReadiness({ ok: true })
      setState({ kind: 'guest', message: 'Сессия истекла. Войдите снова, чтобы продолжить.' })
    })
    return () => setUnauthorizedHandler(undefined)
  }, [])

  useEffect(() => {
    cacheScopeRef.current = state.kind === 'ready'
      ? `${state.session.user?.username ? `user:${state.session.user.username}` : 'local'}:${state.capabilities.libraryId ?? state.capabilities.libraryAlias}`
      : undefined
  }, [state])

  useEffect(() => {
		const controller = new AbortController()
		const generation = authAttemptGenerationRef.current + 1
		authAttemptGenerationRef.current = generation
		const current = () => !controller.signal.aborted && authAttemptGenerationRef.current === generation
    const load = async () => {
      try {
				const cacheAuthGeneration = await captureDurableGCodeCacheAuthGeneration()
				if (!current()) return
        const session = await getAuthSession(controller.signal)
				if (!current()) return
        if (!session.authenticated) {
          setState({ kind: 'guest' })
          return
        }
				if (!await reconcileStaleAuthSession(session, controller.signal)) {
					if (!current()) return
					clearApiSession()
					setState({ kind: 'guest', message: 'Предыдущий вход был отменён выходом. Войдите снова.' })
					return
				}
        const capabilities = await getCapabilities(controller.signal)
				if (!current()) return
				const scope = gcodeCacheScope(session, capabilities)
				controller.signal.throwIfAborted()
				const allowed = await allowGCodeCacheScope(scope, undefined, cacheAuthGeneration)
				if (!current()) {
					if (allowed) void blockGCodeCacheScope(scope).then((token) => clearGCodeCacheScope(scope, token))
					return
				}
				if (!allowed) {
					clearApiSession()
					setState({ kind: 'guest', message: 'Сессия изменилась во время входа. Войдите снова.' })
					return
				}
        setState({ kind: 'ready', capabilities, session })
      } catch (error) {
				if (!current()) return
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

  const onAuthenticated = useCallback((session: AuthSession, cacheAuthGeneration = captureGCodeCacheAuthGeneration()) => {
		authContinuationControllerRef.current?.abort()
		const controller = new AbortController()
		authContinuationControllerRef.current = controller
		const generation = authAttemptGenerationRef.current + 1
		authAttemptGenerationRef.current = generation
		const current = () => !controller.signal.aborted && authAttemptGenerationRef.current === generation
		const revokeStaleLogin = async () => {
			await logoutSessionIfCurrent(session.csrfToken)
		}
    focusWorkspace.current = true
    setLogoutError(undefined)
    setState({ kind: 'loading' })
		void (async () => {
			const proof = await quarantineExplicitAuthSession(session)
			if (!proof) {
				await revokeStaleLogin()
				if (current()) setState({ kind: 'guest', message: 'Браузер не смог надёжно защитить новую сессию. Повторите вход.' })
				return
			}
			if (!await activateExplicitAuthSession(session, proof)) {
				await revokeStaleLogin()
				if (current()) setState({ kind: 'guest', message: 'Браузер не смог подтвердить защищённую сессию. Повторите вход.' })
				return
			}
			if (!current()) {
				await revokeStaleLogin()
				return
			}
			const capabilities = await getCapabilities(controller.signal)
				if (!current()) {
					await revokeStaleLogin()
					return
				}
				const scope = gcodeCacheScope(session, capabilities)
				const allowed = await allowGCodeCacheScope(scope, undefined, cacheAuthGeneration)
				if (!current()) {
					if (allowed) void blockGCodeCacheScope(scope).then((token) => clearGCodeCacheScope(scope, token))
					await revokeStaleLogin()
					return
				}
				if (!allowed) {
					await revokeStaleLogin()
					setState({ kind: 'guest', message: 'Сессия изменилась во время входа. Войдите снова.' })
					return
				}
				await acceptExplicitAuthSession(session, proof)
				if (!current()) {
					void blockGCodeCacheScope(scope).then((token) => clearGCodeCacheScope(scope, token))
					await revokeStaleLogin()
					return
				}
        setState({ kind: 'ready', capabilities, session })
		})().catch(async (error: unknown) => {
				if (!current()) {
					await revokeStaleLogin()
					return
				}
			await revokeStaleLogin()
			if (!current()) return
        if (error instanceof DOMException && error.name === 'AbortError') return
        if (error instanceof ApiError && error.status === 401) {
          clearApiSession()
          setState({ kind: 'guest', message: 'Сессия истекла. Войдите снова, чтобы продолжить.' })
          return
        }
        setState({ kind: 'unavailable', message: error instanceof ApiError ? error.message : 'Не удалось получить конфигурацию приложения.' })
			})
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

  const signOut = async (): Promise<boolean> => {
    if (loggingOut) return false
		authAttemptGenerationRef.current += 1
		authContinuationControllerRef.current?.abort()
		const scope = state.kind === 'ready' ? gcodeCacheScope(state.session, state.capabilities) : cacheScopeRef.current
    setLoggingOut(true)
    setLogoutError(undefined)
		const blockToken = scope ? await blockGCodeCacheScope(scope) : undefined
		if (scope && !blockToken) {
			setLogoutError('Браузер не смог надёжно закрыть локальный кэш. Повторите выход или закройте доступ к этому устройству.')
			setLoggingOut(false)
			return false
		}
		const cacheAuthGeneration = captureGCodeCacheAuthGeneration()
    try {
      await logout()
			if (scope) await clearGCodeCacheScope(scope, blockToken)
      setReadiness({ ok: true })
      setState({ kind: 'guest', message: 'Вы вышли из Web Setup Manager.' })
      return true
    } catch (reason) {
      if (reason instanceof ApiError && reason.status === 401) {
				if (scope) await clearGCodeCacheScope(scope, blockToken)
        setReadiness({ ok: true })
        setState({ kind: 'guest', message: 'Вы вышли из Web Setup Manager.' })
        return true
      }
			if (scope && !await allowGCodeCacheScope(scope, blockToken, cacheAuthGeneration)) {
				clearApiSession()
				setReadiness({ ok: true })
				setState({ kind: 'guest', message: 'Сессия была завершена в другой операции.' })
				return true
			}
      setLogoutError(errorMessage(reason))
      return false
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
      onLogout={signOut}
      onRetryReadiness={() => setReadinessAttempt((value) => value + 1)}
    />
  }

  const label = state.kind === 'guest' ? 'Требуется вход'
    : state.kind === 'loading' ? 'Проверка сессии' : 'Сервис недоступен'
  return <AuthShell stateLabel={label}>
    {state.kind === 'loading' ? <LoadingWorkspace /> : null}
    {state.kind === 'unavailable' ? <BackendUnavailable message={state.message} onRetry={retry} /> : null}
    {state.kind === 'guest' ? <LoginView message={state.message} onAuthenticated={onAuthenticated} captureAuthGeneration={captureDurableGCodeCacheAuthGeneration} /> : null}
  </AuthShell>
}
