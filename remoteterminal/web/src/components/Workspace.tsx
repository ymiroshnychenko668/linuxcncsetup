import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type KeyboardEvent,
} from 'react'
import { ApiError, api, type TerminalSession } from '../api'
import {
  AlertIcon,
  KeyboardIcon,
  LogOutIcon,
  MenuIcon,
  PlusIcon,
  RefreshIcon,
  TerminalIcon,
  TrashIcon,
  XIcon,
} from '../icons'
import { CreateSessionModal } from './CreateSessionModal'
import { DeleteSessionModal } from './DeleteSessionModal'
import { Modal } from './Modal'
import { TerminalPanel, type TerminalState } from './TerminalPanel'

interface WorkspaceProps {
  machineName: string
  username: string
  onLogout: (message?: string) => void
}

interface StoredWorkspace {
  openIds: string[]
  selectedId: string | null
}

function storageKey(username: string): string {
  return `remoteterminal.workspace.v1:${username}`
}

function readStoredWorkspace(username: string): StoredWorkspace {
  try {
    const parsed = JSON.parse(localStorage.getItem(storageKey(username)) ?? 'null') as Partial<StoredWorkspace> | null
    return {
      openIds: Array.isArray(parsed?.openIds)
        ? parsed.openIds.filter((id): id is string => typeof id === 'string')
        : [],
      selectedId: typeof parsed?.selectedId === 'string' ? parsed.selectedId : null,
    }
  } catch {
    return { openIds: [], selectedId: null }
  }
}

function connectionLabel(state: TerminalState | undefined): string {
  if (state === 'connected') return 'Connected'
  if (state === 'error') return 'Connection interrupted'
  if (state === 'connecting') return 'Connecting'
  return 'Ready to open'
}

export function Workspace({ machineName, username, onLogout }: WorkspaceProps) {
  const [sessions, setSessions] = useState<TerminalSession[] | null>(null)
  const [openIds, setOpenIds] = useState<string[]>([])
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [activatedIds, setActivatedIds] = useState<string[]>([])
  const [connectionStates, setConnectionStates] = useState<Record<string, TerminalState>>({})
  const [reconnectKeys, setReconnectKeys] = useState<Record<string, number>>({})
  const [loadError, setLoadError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<TerminalSession | null>(null)
  const [showHelp, setShowHelp] = useState(false)
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [loggingOut, setLoggingOut] = useState(false)
  const [restored, setRestored] = useState(false)

  const loadSessions = useCallback(async (signal?: AbortSignal) => {
    setLoadError(null)
    try {
      const loaded = await api.getSessions(signal)
      setSessions(loaded)
      setOpenIds((current) => current.filter((id) => loaded.some((session) => session.id === id)))
      setSelectedId((current) => current && loaded.some((session) => session.id === current) ? current : null)
      setActivatedIds((current) => current.filter((id) => loaded.some((session) => session.id === id)))
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return
      setLoadError(cause instanceof ApiError ? cause.message : 'Sessions could not be loaded.')
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void loadSessions(controller.signal)
    return () => controller.abort()
  }, [loadSessions])

  useEffect(() => {
    if (!sessions || restored) return
    const stored = readStoredWorkspace(username)
    const validOpenIds = stored.openIds.filter((id) => sessions.some((session) => session.id === id))
    const restoredSelection = stored.selectedId && validOpenIds.includes(stored.selectedId)
      ? stored.selectedId
      : validOpenIds[0] ?? null
    setOpenIds(validOpenIds)
    setSelectedId(restoredSelection)
    setActivatedIds(restoredSelection ? [restoredSelection] : [])
    setRestored(true)
  }, [restored, sessions, username])

  useEffect(() => {
    if (!restored) return
    localStorage.setItem(storageKey(username), JSON.stringify({ openIds, selectedId }))
  }, [openIds, restored, selectedId, username])

  const sessionById = useMemo(
    () => new Map((sessions ?? []).map((session) => [session.id, session])),
    [sessions],
  )
  const openSessions = openIds.flatMap((id) => {
    const session = sessionById.get(id)
    return session ? [session] : []
  })
  const closedSessions = (sessions ?? []).filter((session) => !openIds.includes(session.id))
  const activeSession = selectedId ? sessionById.get(selectedId) ?? null : null

  const activateSession = useCallback((id: string) => {
    setActivatedIds((current) => current.includes(id) ? current : [...current, id])
    setSelectedId(id)
  }, [])

  const openSession = useCallback((id: string) => {
    setOpenIds((current) => current.includes(id) ? current : [...current, id])
    activateSession(id)
    setSidebarOpen(false)
  }, [activateSession])

  const closeTab = useCallback((id: string) => {
    const index = openIds.indexOf(id)
    const remaining = openIds.filter((openId) => openId !== id)
    setOpenIds(remaining)
    setActivatedIds((current) => current.filter((activeId) => activeId !== id))
    if (selectedId === id) {
      const nextId = remaining[Math.min(index, remaining.length - 1)] ?? null
      if (nextId) activateSession(nextId)
      else setSelectedId(null)
    }
    setConnectionStates((current) => {
      const next = { ...current }
      delete next[id]
      return next
    })
  }, [activateSession, openIds, selectedId])

  const handleTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, id: string) => {
    const index = openIds.indexOf(id)
    let targetId: string | undefined
    if (event.key === 'ArrowDown') targetId = openIds[(index + 1) % openIds.length]
    if (event.key === 'ArrowUp') targetId = openIds[(index - 1 + openIds.length) % openIds.length]
    if (event.key === 'Home') targetId = openIds[0]
    if (event.key === 'End') targetId = openIds[openIds.length - 1]
    if (event.key === 'Delete') {
      event.preventDefault()
      closeTab(id)
      return
    }
    if (targetId) {
      event.preventDefault()
      activateSession(targetId)
      document.getElementById(`tab-${targetId}`)?.focus()
    }
  }

  const onCreated = (session: TerminalSession) => {
    setSessions((current) => [...(current ?? []).filter((item) => item.id !== session.id), session])
    setShowCreate(false)
    openSession(session.id)
  }

  const onDeleted = (id: string) => {
    setDeleteTarget(null)
    closeTab(id)
    setSessions((current) => (current ?? []).filter((session) => session.id !== id))
  }

  const onSessionChange = useCallback((updated: TerminalSession) => {
    setSessions((current) => (current ?? []).map((session) => session.id === updated.id ? updated : session))
  }, [])

  const onTerminalStateChange = useCallback((id: string, state: TerminalState) => {
    setConnectionStates((current) => current[id] === state ? current : { ...current, [id]: state })
  }, [])

  const logout = async () => {
    setLoggingOut(true)
    setActionError(null)
    try {
      await api.logout()
      onLogout('You have signed out.')
    } catch (cause) {
      setActionError(cause instanceof ApiError ? cause.message : 'Sign out failed. Please try again.')
      setLoggingOut(false)
    }
  }

  return (
    <div className="workspace">
      <button
        className={`sidebar-scrim ${sidebarOpen ? 'sidebar-scrim--visible' : ''}`}
        type="button"
        aria-label="Close session navigation"
        tabIndex={sidebarOpen ? 0 : -1}
        onClick={() => setSidebarOpen(false)}
      />

      <aside className={`sidebar ${sidebarOpen ? 'sidebar--open' : ''}`} aria-label="Session navigation">
        <div className="sidebar__header">
          <div className="brand">
            <span className="brand__mark"><TerminalIcon /></span>
            <span className="brand__text">
              <span>Remote Terminal</span>
              <small>{machineName}</small>
            </span>
          </div>
          <button className="icon-button sidebar__mobile-close" type="button" aria-label="Close navigation" onClick={() => setSidebarOpen(false)}>
            <XIcon />
          </button>
        </div>

        <button className="button button--new-session" type="button" onClick={() => setShowCreate(true)}>
          <PlusIcon /> New session
        </button>

        <div className="sidebar__scroll">
          <div className="sidebar-section">
            <div className="sidebar-section__heading">
              <span>Open tabs</span>
              <span className="count-badge">{openSessions.length}</span>
            </div>

            {openSessions.length ? (
              <div className="session-tabs" role="tablist" aria-label="Open terminal sessions" aria-orientation="vertical">
                {openSessions.map((session) => {
                  const state = activatedIds.includes(session.id) ? connectionStates[session.id] : undefined
                  const selected = session.id === selectedId
                  return (
                    <div className={`session-tab ${selected ? 'session-tab--selected' : ''}`} key={session.id}>
                      <button
                        id={`tab-${session.id}`}
                        className="session-tab__select"
                        type="button"
                        role="tab"
                        aria-selected={selected}
                        aria-controls={`panel-${session.id}`}
                        tabIndex={selected || selectedId === null ? 0 : -1}
                        onClick={() => {
                          activateSession(session.id)
                          setSidebarOpen(false)
                        }}
                        onKeyDown={(event) => handleTabKeyDown(event, session.id)}
                      >
                        <span className={`status-dot status-dot--${state ?? 'idle'}`} aria-hidden="true" />
                        <span className="session-tab__text">
                          <strong>{session.name}</strong>
                          <small>{connectionLabel(state)}</small>
                        </span>
                      </button>
                      <button
                        className="session-tab__action"
                        type="button"
                        aria-label={`Close ${session.name} browser tab`}
                        title="Close browser tab (session keeps running)"
                        onClick={() => closeTab(session.id)}
                      >
                        <XIcon width={15} height={15} />
                      </button>
                    </div>
                  )
                })}
              </div>
            ) : (
              <p className="sidebar-empty">Open a running session or create a new one.</p>
            )}
          </div>

          <div className="sidebar-section sidebar-section--available">
            <div className="sidebar-section__heading">
              <span>Running sessions</span>
              <span className="count-badge">{closedSessions.length}</span>
            </div>

            {sessions === null ? (
              <div className="session-list-skeleton" aria-label="Loading sessions">
                <span /><span /><span />
              </div>
            ) : closedSessions.length ? (
              <ul className="available-list">
                {closedSessions.map((session) => (
                  <li key={session.id}>
                    <button className="available-session" type="button" onClick={() => openSession(session.id)}>
                      <TerminalIcon width={16} height={16} />
                      <span>{session.name}</span>
                      <span className="available-session__open">Open</span>
                    </button>
                    <button
                      className="available-session__delete"
                      type="button"
                      aria-label={`Delete ${session.name} tmux session`}
                      title="Delete tmux session"
                      onClick={() => setDeleteTarget(session)}
                    >
                      <TrashIcon width={15} height={15} />
                    </button>
                  </li>
                ))}
              </ul>
            ) : (
              <p className="sidebar-empty">No unopened sessions.</p>
            )}
          </div>
        </div>

        <div className="sidebar__footer">
          <div className="user-chip" aria-label={`Signed in as ${username}`}>
            <span className="user-chip__avatar">{username.slice(0, 1).toUpperCase()}</span>
            <span className="user-chip__name">
              <small>Signed in as</small>
              <strong>{username}</strong>
            </span>
          </div>
          <button className="icon-button" type="button" aria-label="Sign out" title="Sign out" onClick={() => void logout()} disabled={loggingOut}>
            {loggingOut ? <span className="spinner" aria-hidden="true" /> : <LogOutIcon />}
          </button>
        </div>
      </aside>

      <main className="workspace-main">
        <header className="topbar">
          <div className="topbar__title">
            <button className="icon-button topbar__menu" type="button" aria-label="Open session navigation" onClick={() => setSidebarOpen(true)}>
              <MenuIcon />
            </button>
            <div>
              <p className="eyebrow">Terminal session</p>
              <h1>{machineName} / {activeSession?.name ?? 'Workspace'}</h1>
            </div>
          </div>
          <div className="topbar__actions">
            <button className="button button--ghost" type="button" onClick={() => setShowHelp(true)}>
              <KeyboardIcon /> <span>Clipboard help</span>
            </button>
            {activeSession ? (
              <button
                className="button button--ghost"
                type="button"
                onClick={() => setReconnectKeys((current) => ({
                  ...current,
                  [activeSession.id]: (current[activeSession.id] ?? 0) + 1,
                }))}
              >
                <RefreshIcon /> <span>Reconnect</span>
              </button>
            ) : null}
            {activeSession ? (
              <button className="button button--ghost button--danger-ghost" type="button" onClick={() => setDeleteTarget(activeSession)}>
                <TrashIcon /> <span>Delete session</span>
              </button>
            ) : null}
          </div>
        </header>

        {loadError ? (
          <div className="workspace-notice notice notice--error" role="alert">
            <AlertIcon />
            <span>{loadError}</span>
            <button className="button button--compact button--secondary" type="button" onClick={() => void loadSessions()}>
              <RefreshIcon /> Retry
            </button>
          </div>
        ) : null}
        {actionError ? (
          <div className="workspace-notice notice notice--error" role="alert">
            <AlertIcon /> <span>{actionError}</span>
            <button className="icon-button" type="button" aria-label="Dismiss error" onClick={() => setActionError(null)}><XIcon /></button>
          </div>
        ) : null}

        <div className="terminal-stage">
          {openSessions.map((session) => activatedIds.includes(session.id) ? (
              <TerminalPanel
                key={session.id}
                session={session}
                active={session.id === selectedId}
                reconnectKey={reconnectKeys[session.id] ?? 0}
                onStateChange={onTerminalStateChange}
                onSessionChange={onSessionChange}
              />
            ) : (
              <section
                key={session.id}
                id={`panel-${session.id}`}
                role="tabpanel"
                aria-labelledby={`tab-${session.id}`}
                hidden
              />
            ))}

          {!activeSession ? (
            <section className="workspace-empty" aria-labelledby="workspace-empty-title">
              <span className="workspace-empty__graphic"><TerminalIcon width={36} height={36} /></span>
              <p className="eyebrow">Persistent shells</p>
              <h2 id="workspace-empty-title">Choose where to pick up</h2>
              <p>
                Open a running tmux session from the sidebar, or create a clean terminal for a new task.
              </p>
              <button className="button button--primary button--large" type="button" onClick={() => setShowCreate(true)}>
                <PlusIcon /> Create a session
              </button>
              <div className="shortcut-note"><KeyboardIcon /> Copy with <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>C</kbd>, paste with <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>V</kbd></div>
            </section>
          ) : null}
        </div>
      </main>

      {showCreate ? <CreateSessionModal onClose={() => setShowCreate(false)} onCreated={onCreated} /> : null}
      {deleteTarget ? <DeleteSessionModal session={deleteTarget} onClose={() => setDeleteTarget(null)} onDeleted={onDeleted} /> : null}
      {showHelp ? (
        <Modal title="Terminal copy and paste" onClose={() => setShowHelp(false)}>
          <div className="shortcut-grid">
            <div><span>Copy selected text</span><kbd>Ctrl</kbd><span>+</span><kbd>Shift</kbd><span>+</span><kbd>C</kbd></div>
            <div><span>Paste clipboard text</span><kbd>Ctrl</kbd><span>+</span><kbd>Shift</kbd><span>+</span><kbd>V</kbd></div>
            <div><span>Alternative paste</span><kbd>Shift</kbd><span>+</span><kbd>Insert</kbd></div>
          </div>
          <p className="modal-note">Hold <kbd>Shift</kbd> while dragging to make a browser text selection when tmux mouse scrolling is active. Your browser may ask for clipboard permission. Text stays between your clipboard and the terminal.</p>
        </Modal>
      ) : null}
    </div>
  )
}
