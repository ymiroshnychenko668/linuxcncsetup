import { useCallback, useEffect, useState } from 'react'
import { ApiError, api, type AuthSession } from './api'
import { LoginView } from './components/LoginView'
import { Workspace } from './components/Workspace'
import { RefreshIcon, TerminalIcon } from './icons'

type AppState =
  | { kind: 'loading' }
  | { kind: 'guest'; message?: string }
  | { kind: 'authenticated'; username: string }
  | { kind: 'unavailable'; message: string }

export default function App() {
  const [state, setState] = useState<AppState>({ kind: 'loading' })
  const [machineName, setMachineName] = useState('')

  const checkSession = useCallback(async (signal?: AbortSignal) => {
    setState({ kind: 'loading' })
    try {
      const configuration = await api.getConfig(signal)
      setMachineName(configuration.machineName)
      const session = await api.getAuthSession(signal)
      if (session.authenticated && session.user) {
        setState({ kind: 'authenticated', username: session.user.username })
      } else {
        setState({ kind: 'guest' })
      }
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return
      if (cause instanceof ApiError && cause.status === 401) {
        api.clearAuthentication()
        setState({ kind: 'guest' })
      } else {
        setState({
          kind: 'unavailable',
          message: cause instanceof Error ? cause.message : 'The remote terminal service is unavailable.',
        })
      }
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void checkSession(controller.signal)
    return () => controller.abort()
  }, [checkSession])

  useEffect(() => {
    api.setUnauthorizedHandler(() => {
      setState({ kind: 'guest', message: 'Your session expired. Sign in again to continue.' })
    })
    return () => api.setUnauthorizedHandler(undefined)
  }, [])

  const onAuthenticated = (session: AuthSession) => {
    if (session.user) setState({ kind: 'authenticated', username: session.user.username })
  }

  if (state.kind === 'loading') {
    return (
      <main className="boot-page" aria-label="Loading Remote Terminal">
        <span className="boot-logo"><TerminalIcon width={28} height={28} /></span>
        <span className="spinner spinner--large" aria-hidden="true" />
        <p>Checking secure session…</p>
      </main>
    )
  }

  if (state.kind === 'unavailable') {
    return (
      <main className="boot-page">
        <span className="boot-logo"><TerminalIcon width={28} height={28} /></span>
        <h1>Service unavailable</h1>
        <p>{state.message}</p>
        <button className="button button--primary" type="button" onClick={() => void checkSession()}>
          <RefreshIcon /> Try again
        </button>
      </main>
    )
  }

  if (state.kind === 'guest') {
    return <LoginView machineName={machineName} onAuthenticated={onAuthenticated} message={state.message} />
  }

  return (
    <Workspace
      machineName={machineName}
      username={state.username}
      onLogout={(message) => {
        api.clearAuthentication()
        setState({ kind: 'guest', message })
      }}
    />
  )
}
