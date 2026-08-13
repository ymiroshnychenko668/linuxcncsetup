import { useEffect, useRef, useState } from 'react'
import type { CodeServerInstance } from '../api'
import { AlertIcon, CodeServerIcon, RefreshIcon } from '../icons'

export type CodeServerState = 'loading' | 'ready' | 'error'

interface CodeServerPanelProps {
  codeServer: CodeServerInstance
  active: boolean
  reloadKey: number
  onStateChange: (id: string, state: CodeServerState) => void
  onReload: (id: string) => void
}

export function CodeServerPanel({ codeServer, active, reloadKey, onStateChange, onReload }: CodeServerPanelProps) {
  const [loadState, setLoadState] = useState({
    codeServerId: codeServer.id,
    reloadKey,
    state: 'loading' as CodeServerState,
  })
  const frameRef = useRef<HTMLIFrameElement>(null)
  const state = loadState.codeServerId === codeServer.id && loadState.reloadKey === reloadKey
    ? loadState.state
    : 'loading'

  useEffect(() => {
    setLoadState({ codeServerId: codeServer.id, reloadKey, state: 'loading' })
    onStateChange(codeServer.id, 'loading')
  }, [codeServer.id, reloadKey])

  const updateState = (next: CodeServerState) => {
    setLoadState({ codeServerId: codeServer.id, reloadKey, state: next })
    onStateChange(codeServer.id, next)
  }

  const inspectLoadedFrame = () => {
    try {
      const document = frameRef.current?.contentDocument
      if (!document) {
        updateState('error')
        return
      }

      const contentType = document.contentType.toLowerCase()
      const loadedURL = new URL(document.URL)
      const expectedURL = new URL(codeServer.url, window.location.origin)
      const expectedHTML = contentType === 'text/html' || contentType === 'application/xhtml+xml'
      const expectedPath = loadedURL.pathname.startsWith(expectedURL.pathname)
      if (loadedURL.origin !== window.location.origin || !expectedHTML || !expectedPath) {
        updateState('error')
        return
      }
      updateState('ready')
    } catch {
      // The configured route is deliberately same-origin. A document that
      // cannot be inspected is therefore not the expected editor response.
      updateState('error')
    }
  }

  return (
    <section
      id={`panel-codeServer-${codeServer.id}`}
      className="terminal-panel code-server-panel"
      role="tabpanel"
      aria-labelledby={`tab-codeServer-${codeServer.id}`}
      aria-hidden={!active}
      hidden={!active}
      tabIndex={0}
    >
      <iframe
        ref={frameRef}
        key={reloadKey}
        className="terminal-frame code-server-frame"
        src={codeServer.url}
        title={`${codeServer.name} Code Server`}
        aria-hidden={state !== 'ready' || !active ? true : undefined}
        tabIndex={state === 'ready' && active ? 0 : -1}
        allow="clipboard-read; clipboard-write"
        onLoad={inspectLoadedFrame}
        onError={() => updateState('error')}
      />

      {state === 'loading' ? (
        <div className="terminal-status code-server-status" role="status">
          <span className="terminal-status__icon code-server-status__icon terminal-status__icon--pulse"><CodeServerIcon /></span>
          <h2>Opening {codeServer.name}</h2>
          <p>Loading the persistent editor for {codeServer.folderPath}…</p>
          <button className="button button--secondary" type="button" onClick={() => onReload(codeServer.id)}>
            <RefreshIcon /> Reload editor
          </button>
        </div>
      ) : null}

      {state === 'error' ? (
        <div className="terminal-status" role="alert">
          <span className="terminal-status__icon terminal-status__icon--error"><AlertIcon /></span>
          <h2>Code Server could not be loaded</h2>
          <p>The editor is still running. Reload this browser view to try again.</p>
          <button className="button button--code-primary" type="button" onClick={() => onReload(codeServer.id)}>
            <RefreshIcon /> Reload editor
          </button>
        </div>
      ) : null}
    </section>
  )
}
