import { useCallback, useEffect, useRef, useState } from 'react'
import { ApiError, api, type TerminalSession } from '../api'
import { AlertIcon, RefreshIcon, TerminalIcon } from '../icons'

export type TerminalState = 'connecting' | 'connected' | 'error'

interface TerminalPanelProps {
  session: TerminalSession
  active: boolean
  reconnectKey: number
  onStateChange: (id: string, state: TerminalState) => void
  onSessionChange: (session: TerminalSession) => void
}

export function TerminalPanel({ session, active, reconnectKey, onStateChange, onSessionChange }: TerminalPanelProps) {
  const [state, setState] = useState<TerminalState>('connecting')
  const [terminalUrl, setTerminalUrl] = useState<string | null>(null)
  const [frameKey, setFrameKey] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const attemptRef = useRef(0)

  const updateState = useCallback((next: TerminalState) => {
    setState(next)
    onStateChange(session.id, next)
  }, [onStateChange, session.id])

  const connect = useCallback(async () => {
    const attempt = ++attemptRef.current
    updateState('connecting')
    setError(null)
    setTerminalUrl(null)

    try {
      const result = await api.connectSession(session.id)
      if (attempt !== attemptRef.current) return
      onSessionChange(result.session)
      setFrameKey(attempt)
      setTerminalUrl(result.terminalUrl)
    } catch (cause) {
      if (attempt !== attemptRef.current) return
      setError(cause instanceof ApiError ? cause.message : 'The terminal could not be opened.')
      updateState('error')
    }
  }, [onSessionChange, session.id, updateState])

  useEffect(() => {
    void connect()
    return () => {
      attemptRef.current += 1
    }
  }, [connect, reconnectKey])

  useEffect(() => {
    const onOffline = () => {
      setError('This browser is offline. Your tmux session is still running.')
      updateState('error')
    }
    const onOnline = () => {
      if (state === 'error') void connect()
    }
    window.addEventListener('offline', onOffline)
    window.addEventListener('online', onOnline)
    return () => {
      window.removeEventListener('offline', onOffline)
      window.removeEventListener('online', onOnline)
    }
  }, [connect, state, updateState])

  return (
    <section
      id={`panel-${session.id}`}
      className="terminal-panel"
      role="tabpanel"
      aria-labelledby={`tab-${session.id}`}
      aria-hidden={!active}
      hidden={!active}
      tabIndex={0}
    >
      {terminalUrl ? (
        <iframe
          key={frameKey}
          className="terminal-frame"
          src={terminalUrl}
          title={`${session.name} terminal`}
          allow="clipboard-read; clipboard-write"
          onLoad={() => updateState('connected')}
          onError={() => {
            setError('The terminal connection was interrupted. Your tmux session is still running.')
            updateState('error')
          }}
        />
      ) : null}

      {state === 'connecting' ? (
        <div className="terminal-status" role="status">
          <span className="terminal-status__icon terminal-status__icon--pulse"><TerminalIcon /></span>
          <h2>Connecting to {session.name}</h2>
          <p>Attaching securely to the persistent tmux session…</p>
        </div>
      ) : null}

      {state === 'error' ? (
        <div className="terminal-status" role="alert">
          <span className="terminal-status__icon terminal-status__icon--error"><AlertIcon /></span>
          <h2>Connection interrupted</h2>
          <p>{error}</p>
          <button className="button button--primary" type="button" onClick={() => void connect()}>
            <RefreshIcon /> Reconnect
          </button>
        </div>
      ) : null}
    </section>
  )
}
