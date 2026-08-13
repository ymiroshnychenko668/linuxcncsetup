import { useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react'
import { ApiError, api, type TerminalSession } from '../api'
import { AlertIcon, RefreshIcon, TerminalIcon } from '../icons'

export type TerminalState = 'connecting' | 'connected' | 'error'

interface TtydTerminal {
  fit?: () => void
  focus?: () => void
  refresh?: (start: number, end: number) => void
  rows?: number
  textarea?: HTMLTextAreaElement
}

type TtydWindow = Window & { term?: TtydTerminal }

const REFIT_RETRY_DELAYS_MS = [50, 100, 200, 400, 800, 1600] as const

interface TerminalPanelProps {
  session: TerminalSession
  active: boolean
  focusRequestKey: number
  reconnectKey: number
  onStateChange: (id: string, state: TerminalState) => void
  onSessionChange: (session: TerminalSession) => void
}

export function TerminalPanel({ session, active, focusRequestKey, reconnectKey, onStateChange, onSessionChange }: TerminalPanelProps) {
  const [state, setState] = useState<TerminalState>('connecting')
  const [terminalUrl, setTerminalUrl] = useState<string | null>(null)
  const [frameKey, setFrameKey] = useState(0)
  const [error, setError] = useState<string | null>(null)
  const attemptRef = useRef(0)
  const panelRef = useRef<HTMLElement>(null)
  const frameRef = useRef<HTMLIFrameElement>(null)
  const activeRef = useRef(active)
  const animationFrameRef = useRef<number | null>(null)
  const retryTimeoutRef = useRef<number | null>(null)
  const focusRequestRef = useRef(0)
  const focusedRequestRef = useRef(0)
  const focusOwnerRef = useRef<Element | null>(null)
  const focusAnimationFrameRef = useRef<number | null>(null)
  const focusRetryTimeoutRef = useRef<number | null>(null)

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

  const cancelScheduledRefit = useCallback(() => {
    if (animationFrameRef.current !== null) {
      window.cancelAnimationFrame(animationFrameRef.current)
      animationFrameRef.current = null
    }
    if (retryTimeoutRef.current !== null) {
      window.clearTimeout(retryTimeoutRef.current)
      retryTimeoutRef.current = null
    }
  }, [])

  const refitTerminal = useCallback(() => {
    try {
      const terminal = (frameRef.current?.contentWindow as TtydWindow | null)?.term
      if (typeof terminal?.fit !== 'function') return false

      terminal.fit()
      const rows = terminal.rows
      if (typeof terminal.refresh === 'function' && Number.isInteger(rows) && rows !== undefined && rows > 0) {
        terminal.refresh(0, rows - 1)
      }
      return true
    } catch {
      // The iframe can still be navigating when its load event fires. A later
      // bounded retry will run once the same-origin ttyd terminal is ready.
      return false
    }
  }, [])

  const scheduleTerminalRefit = useCallback(function scheduleTerminalRefit(attempt = 0) {
    if (!activeRef.current) return

    cancelScheduledRefit()
    animationFrameRef.current = window.requestAnimationFrame(() => {
      animationFrameRef.current = null
      if (!activeRef.current || refitTerminal()) return

      const retryDelay = REFIT_RETRY_DELAYS_MS[attempt]
      if (retryDelay === undefined) return
      retryTimeoutRef.current = window.setTimeout(() => {
        retryTimeoutRef.current = null
        scheduleTerminalRefit(attempt + 1)
      }, retryDelay)
    })
  }, [cancelScheduledRefit, refitTerminal])

  const cancelScheduledFocus = useCallback(() => {
    if (focusAnimationFrameRef.current !== null) {
      window.cancelAnimationFrame(focusAnimationFrameRef.current)
      focusAnimationFrameRef.current = null
    }
    if (focusRetryTimeoutRef.current !== null) {
      window.clearTimeout(focusRetryTimeoutRef.current)
      focusRetryTimeoutRef.current = null
    }
  }, [])

  const focusTerminal = useCallback(() => {
    const frame = frameRef.current
    if (!frame) return false

    try {
      const terminal = (frame.contentWindow as TtydWindow | null)?.term
      if (typeof terminal?.focus === 'function') {
        terminal.focus()
        const textarea = terminal.textarea
        if (textarea?.isConnected && textarea.ownerDocument.activeElement === textarea && document.activeElement === frame) {
          return true
        }
      }
    } catch {
      // The iframe may still be navigating. Focusing the frame remains a safe
      // fallback while bounded retries wait for the same-origin xterm object.
    }
    frame.focus()
    return false
  }, [])

  const scheduleTerminalFocus = useCallback(function scheduleTerminalFocus(attempt = 0) {
    const request = focusRequestRef.current
    if (!activeRef.current || request <= focusedRequestRef.current) return

    cancelScheduledFocus()
    focusAnimationFrameRef.current = window.requestAnimationFrame(() => {
      focusAnimationFrameRef.current = null
      if (!activeRef.current || request !== focusRequestRef.current) return

      const frame = frameRef.current
      const activeElement = document.activeElement
      const ownsFocus = activeElement === focusOwnerRef.current || activeElement === frame
      if (!ownsFocus) {
        // The request started from an explicit tab/open action. If the user
        // has moved on while ttyd was loading, consume it instead of stealing
        // focus back from their new control.
        focusedRequestRef.current = request
        return
      }
      if (focusTerminal()) {
        focusedRequestRef.current = request
        return
      }
      const retryDelay = REFIT_RETRY_DELAYS_MS[attempt]
      if (retryDelay === undefined) return
      focusRetryTimeoutRef.current = window.setTimeout(() => {
        focusRetryTimeoutRef.current = null
        scheduleTerminalFocus(attempt + 1)
      }, retryDelay)
    })
  }, [cancelScheduledFocus, focusTerminal])

  useEffect(() => {
    void connect()
    return () => {
      attemptRef.current += 1
    }
  }, [connect, reconnectKey])

  useLayoutEffect(() => {
    activeRef.current = active
    if (!active) {
      cancelScheduledRefit()
      return
    }

    scheduleTerminalRefit()
    return cancelScheduledRefit
  }, [active, cancelScheduledRefit, scheduleTerminalRefit])

  useLayoutEffect(() => {
    const changed = focusRequestRef.current !== focusRequestKey
    focusRequestRef.current = focusRequestKey
    if (changed) focusOwnerRef.current = document.activeElement
    if (!active) {
      cancelScheduledFocus()
      // A focus intent belongs to the activation that created it. Do not
      // replay it after a later keyboard-only tab activation.
      focusedRequestRef.current = Math.max(focusedRequestRef.current, focusRequestKey)
      return
    }

    scheduleTerminalFocus()
    return cancelScheduledFocus
  }, [active, cancelScheduledFocus, focusRequestKey, scheduleTerminalFocus])

  useEffect(() => {
    if (!active || typeof ResizeObserver === 'undefined' || !panelRef.current) return

    const observer = new ResizeObserver(() => scheduleTerminalRefit())
    observer.observe(panelRef.current)
    return () => observer.disconnect()
  }, [active, scheduleTerminalRefit])

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
      ref={panelRef}
      id={`panel-terminal-${session.id}`}
      className="terminal-panel"
      role="tabpanel"
      aria-labelledby={`tab-terminal-${session.id}`}
      aria-hidden={!active}
      hidden={!active}
      tabIndex={0}
    >
      {terminalUrl ? (
        <iframe
          ref={frameRef}
          key={frameKey}
          className="terminal-frame"
          src={terminalUrl}
          title={`${session.name} terminal`}
          allow="clipboard-read; clipboard-write"
          onLoad={() => {
            updateState('connected')
            scheduleTerminalRefit()
            scheduleTerminalFocus()
          }}
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
