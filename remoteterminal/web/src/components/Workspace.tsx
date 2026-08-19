import {
  useCallback,
  useEffect,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type KeyboardEvent,
} from 'react'
import {
  ApiError,
  api,
  type CodeServerInstance,
  type LaunchCodeServerResult,
  type TerminalSession,
} from '../api'
import {
  AlertIcon,
  CodeServerIcon,
  EditIcon,
  KeyboardIcon,
  LogOutIcon,
  MenuIcon,
  PlusIcon,
  RefreshIcon,
  TerminalIcon,
  TrashIcon,
  XIcon,
} from '../icons'
import { CodeServerPanel, type CodeServerState } from './CodeServerPanel'
import { CreateSessionModal } from './CreateSessionModal'
import { DeleteSessionModal } from './DeleteSessionModal'
import { LaunchCodeServerModal } from './LaunchCodeServerModal'
import { RenameTabModal } from './RenameTabModal'
import { ShutdownCodeServerModal } from './ShutdownCodeServerModal'
import { TerminalPanel, type TerminalState } from './TerminalPanel'

interface WorkspaceProps {
  machineName: string
  username: string
  onLogout: (message?: string) => void
}

type WorkspaceTab =
  | { kind: 'terminal'; id: string }
  | { kind: 'codeServer'; id: string }

interface StoredWorkspaceV3 {
  openTabs: WorkspaceTab[]
  selectedTab: WorkspaceTab | null
  tabNames: Record<string, string>
  sidebarCompact: boolean
}

interface StoredWorkspaceV2 {
  openTabs: WorkspaceTab[]
  selectedTab: WorkspaceTab | null
}

interface StoredWorkspaceV1 {
  openIds: string[]
  selectedId: string | null
}

type OpenWorkspaceItem =
  | { tab: Extract<WorkspaceTab, { kind: 'terminal' }>; session: TerminalSession; codeServer: null }
  | { tab: Extract<WorkspaceTab, { kind: 'codeServer' }>; session: null; codeServer: CodeServerInstance }

function storageKey(username: string): string {
  return `remoteterminal.workspace.v3:${username}`
}

function legacyV2StorageKey(username: string): string {
  return `remoteterminal.workspace.v2:${username}`
}

function legacyV1StorageKey(username: string): string {
  return `remoteterminal.workspace.v1:${username}`
}

function isWorkspaceTab(value: unknown): value is WorkspaceTab {
  if (!value || typeof value !== 'object') return false
  const candidate = value as Partial<WorkspaceTab>
  return (candidate.kind === 'terminal' || candidate.kind === 'codeServer')
    && typeof candidate.id === 'string'
    && candidate.id.length > 0
}

function tabKey(tab: WorkspaceTab): string {
  return `${tab.kind}:${tab.id}`
}

function sameTab(left: WorkspaceTab | null, right: WorkspaceTab | null): boolean {
  if (!left || !right) return left === right
  return left.kind === right.kind && left.id === right.id
}

function deduplicateTabs(tabs: WorkspaceTab[]): WorkspaceTab[] {
  const seen = new Set<string>()
  return tabs.filter((tab) => {
    const key = tabKey(tab)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function readTabNames(value: unknown): Record<string, string> {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return {}
  return Object.fromEntries(Object.entries(value).flatMap(([key, name]) => {
    if (
      (!key.startsWith('terminal:') && !key.startsWith('codeServer:'))
      || typeof name !== 'string'
    ) return []
    const normalized = name.trim().slice(0, 48)
    return normalized ? [[key, normalized]] : []
  }))
}

function readStoredWorkspace(username: string): StoredWorkspaceV3 {
  try {
    const parsed = JSON.parse(localStorage.getItem(storageKey(username)) ?? 'null') as Partial<StoredWorkspaceV3> | null
    if (parsed) {
      const openTabs = deduplicateTabs(Array.isArray(parsed.openTabs) ? parsed.openTabs.filter(isWorkspaceTab) : [])
      return {
        openTabs,
        selectedTab: isWorkspaceTab(parsed.selectedTab) ? parsed.selectedTab : null,
        tabNames: readTabNames(parsed.tabNames),
        sidebarCompact: parsed.sidebarCompact === true,
      }
    }
  } catch {
    // Fall through to the previous workspace format.
  }

  try {
    const parsed = JSON.parse(localStorage.getItem(legacyV2StorageKey(username)) ?? 'null') as Partial<StoredWorkspaceV2> | null
    if (parsed) {
      const openTabs = deduplicateTabs(Array.isArray(parsed.openTabs) ? parsed.openTabs.filter(isWorkspaceTab) : [])
      return {
        openTabs,
        selectedTab: isWorkspaceTab(parsed.selectedTab) ? parsed.selectedTab : null,
        tabNames: {},
        sidebarCompact: false,
      }
    }
  } catch {
    // Fall through to the legacy terminal-only workspace.
  }

  try {
    const parsed = JSON.parse(localStorage.getItem(legacyV1StorageKey(username)) ?? 'null') as Partial<StoredWorkspaceV1> | null
    const openIds = Array.isArray(parsed?.openIds)
      ? parsed.openIds.filter((id): id is string => typeof id === 'string' && id.length > 0)
      : []
    return {
      openTabs: deduplicateTabs(openIds.map((id) => ({ kind: 'terminal' as const, id }))),
      selectedTab: typeof parsed?.selectedId === 'string'
        ? { kind: 'terminal', id: parsed.selectedId }
        : null,
      tabNames: {},
      sidebarCompact: false,
    }
  } catch {
    return { openTabs: [], selectedTab: null, tabNames: {}, sidebarCompact: false }
  }
}

function terminalConnectionLabel(state: TerminalState | undefined): string {
  if (state === 'connected') return 'Connected'
  if (state === 'error') return 'Connection interrupted'
  if (state === 'connecting') return 'Connecting'
  return 'Ready to open'
}

function codeServerConnectionLabel(state: CodeServerState | undefined): string {
  if (state === 'ready') return 'Editor ready'
  if (state === 'error') return 'Load interrupted'
  if (state === 'loading') return 'Loading editor'
  return 'Code Server · ready to open'
}

interface InFlightCodeServerList {
  controller: AbortController
  promise: Promise<void>
}

const MOBILE_NAVIGATION_QUERY = '(max-width: 800px)'

function useMobileNavigation(): boolean {
  const [mobile, setMobile] = useState(() => (
    typeof window.matchMedia === 'function'
      ? window.matchMedia(MOBILE_NAVIGATION_QUERY).matches
      : false
  ))

  useEffect(() => {
    if (typeof window.matchMedia !== 'function') return
    const query = window.matchMedia(MOBILE_NAVIGATION_QUERY)
    const update = () => setMobile(query.matches)
    update()
    query.addEventListener('change', update)
    return () => query.removeEventListener('change', update)
  }, [])

  return mobile
}

export function Workspace({ machineName, username, onLogout }: WorkspaceProps) {
  const initialWorkspaceRef = useRef<StoredWorkspaceV3 | null>(null)
  if (initialWorkspaceRef.current === null) {
    initialWorkspaceRef.current = readStoredWorkspace(username)
  }
  const initialWorkspace = initialWorkspaceRef.current
  const storedSelectionIsOpen = initialWorkspace.selectedTab !== null
    && initialWorkspace.openTabs.some((tab) => sameTab(tab, initialWorkspace.selectedTab))
  const initialTerminalSelection = storedSelectionIsOpen && initialWorkspace.selectedTab?.kind === 'terminal'
    ? initialWorkspace.selectedTab
    : initialWorkspace.openTabs.find((tab) => tab.kind === 'terminal') ?? null

  const [sessions, setSessions] = useState<TerminalSession[] | null>(null)
  const [codeServers, setCodeServers] = useState<CodeServerInstance[] | null>(null)
  const [openTabs, setOpenTabs] = useState<WorkspaceTab[]>(initialWorkspace.openTabs)
  const [selectedTab, setSelectedTab] = useState<WorkspaceTab | null>(initialTerminalSelection)
  const [tabNames, setTabNames] = useState<Record<string, string>>(initialWorkspace.tabNames)
  const [sidebarCompact, setSidebarCompact] = useState(initialWorkspace.sidebarCompact)
  const [activatedKeys, setActivatedKeys] = useState<string[]>(
    initialTerminalSelection ? [tabKey(initialTerminalSelection)] : [],
  )
  const [terminalListReady, setTerminalListReady] = useState(false)
  const [codeServerListReady, setCodeServerListReady] = useState(false)
  const [terminalStates, setTerminalStates] = useState<Record<string, TerminalState>>({})
  const [codeServerStates, setCodeServerStates] = useState<Record<string, CodeServerState>>({})
  const [terminalFocusKeys, setTerminalFocusKeys] = useState<Record<string, number>>({})
  const [codeServerReloadKeys, setCodeServerReloadKeys] = useState<Record<string, number>>({})
  const [terminalLoadError, setTerminalLoadError] = useState<string | null>(null)
  const [codeServerLoadError, setCodeServerLoadError] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [showCreate, setShowCreate] = useState(false)
  const [showLaunchCodeServer, setShowLaunchCodeServer] = useState(false)
  const [deleteTarget, setDeleteTarget] = useState<TerminalSession | null>(null)
  const [shutdownTarget, setShutdownTarget] = useState<CodeServerInstance | null>(null)
  const [renameTarget, setRenameTarget] = useState<WorkspaceTab | null>(null)
  const [sidebarOpen, setSidebarOpen] = useState(false)
  const [loggingOut, setLoggingOut] = useState(false)
  const codeServerListGenerationRef = useRef(0)
  const codeServerListRequestRef = useRef<InFlightCodeServerList | null>(null)
  const codeServerListReadyRef = useRef(false)
  const selectionChangedByUserRef = useRef(false)
  const pendingTabFocusRef = useRef<WorkspaceTab | 'workspace-action' | null>(null)
  const sidebarRef = useRef<HTMLElement>(null)
  const sidebarMenuButtonRef = useRef<HTMLButtonElement>(null)
  const sidebarCloseButtonRef = useRef<HTMLButtonElement>(null)
  const newSessionButtonRef = useRef<HTMLButtonElement>(null)
  const mobileNavigation = useMobileNavigation()
  const pendingStoredCodeSelectionRef = useRef<WorkspaceTab | null>(
    storedSelectionIsOpen && initialWorkspace.selectedTab?.kind === 'codeServer'
      ? initialWorkspace.selectedTab
      : null,
  )

  const openExclusiveModal = useCallback((open: () => void) => {
    setShowCreate(false)
    setShowLaunchCodeServer(false)
    setDeleteTarget(null)
    setShutdownTarget(null)
    setRenameTarget(null)
    open()
  }, [])

  const loadSessions = useCallback(async (signal?: AbortSignal) => {
    setTerminalLoadError(null)
    try {
      const loaded = await api.getSessions(signal)
      setSessions(loaded)
      setTerminalListReady(true)
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return
      setTerminalLoadError(cause instanceof ApiError ? cause.message : 'Sessions could not be loaded.')
    }
  }, [])

  const loadCodeServers = useCallback(async (signal?: AbortSignal) => {
    const generation = ++codeServerListGenerationRef.current
    setCodeServerLoadError(null)
    try {
      const loaded = await api.getCodeServers(signal)
      if (generation !== codeServerListGenerationRef.current) return

      const firstSuccessfulList = !codeServerListReadyRef.current
      codeServerListReadyRef.current = true
      setCodeServers(loaded)
      setCodeServerListReady(true)

      if (firstSuccessfulList) {
        const pendingSelection = pendingStoredCodeSelectionRef.current
        pendingStoredCodeSelectionRef.current = null
        if (
          pendingSelection
          && !selectionChangedByUserRef.current
          && loaded.some((codeServer) => codeServer.id === pendingSelection.id)
        ) {
          setSelectedTab(pendingSelection)
          setActivatedKeys((current) => {
            const key = tabKey(pendingSelection)
            return current.includes(key) ? current : [...current, key]
          })
        }
      }
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return
      if (generation === codeServerListGenerationRef.current) {
        setCodeServerLoadError(cause instanceof ApiError ? cause.message : 'Active Code Servers could not be loaded.')
      }
    }
  }, [])

  const cancelCodeServerList = useCallback(() => {
    codeServerListGenerationRef.current += 1
    const request = codeServerListRequestRef.current
    codeServerListRequestRef.current = null
    request?.controller.abort()
  }, [])

  const loadLatestCodeServers = useCallback(() => {
    const current = codeServerListRequestRef.current
    if (current && !current.controller.signal.aborted) return current.promise

    const controller = new AbortController()
    let request: InFlightCodeServerList
    const promise = loadCodeServers(controller.signal).finally(() => {
      if (codeServerListRequestRef.current === request) {
        codeServerListRequestRef.current = null
      }
    })
    request = { controller, promise }
    codeServerListRequestRef.current = request
    return promise
  }, [loadCodeServers])

  useEffect(() => {
    const terminalController = new AbortController()
    void loadSessions(terminalController.signal)
    void loadLatestCodeServers()
    return () => {
      terminalController.abort()
      cancelCodeServerList()
    }
  }, [cancelCodeServerList, loadLatestCodeServers, loadSessions])

  useEffect(() => {
    let interval: number | undefined

    const schedule = () => {
      if (interval !== undefined) window.clearInterval(interval)
      interval = undefined
      if (document.visibilityState === 'visible') {
        interval = window.setInterval(() => void loadLatestCodeServers(), 5000)
      }
    }
    const refreshWhenVisible = () => {
      if (document.visibilityState === 'visible') void loadLatestCodeServers()
    }
    const onVisibilityChange = () => {
      refreshWhenVisible()
      schedule()
    }

    schedule()
    document.addEventListener('visibilitychange', onVisibilityChange)
    window.addEventListener('focus', refreshWhenVisible)
    return () => {
      if (interval !== undefined) window.clearInterval(interval)
      document.removeEventListener('visibilitychange', onVisibilityChange)
      window.removeEventListener('focus', refreshWhenVisible)
    }
  }, [loadLatestCodeServers])

  const sessionById = useMemo(
    () => new Map((sessions ?? []).map((session) => [session.id, session])),
    [sessions],
  )
  const codeServerById = useMemo(
    () => new Map((codeServers ?? []).map((codeServer) => [codeServer.id, codeServer])),
    [codeServers],
  )
  const defaultTabName = useCallback((tab: WorkspaceTab): string => {
    const resourceName = tab.kind === 'terminal'
      ? sessionById.get(tab.id)?.name
      : codeServerById.get(tab.id)?.name
    return resourceName?.trim() || (tab.kind === 'terminal' ? 'Terminal' : 'Code Server')
  }, [codeServerById, sessionById])
  const displayTabName = useCallback((tab: WorkspaceTab): string => (
    tabNames[tabKey(tab)]?.trim() || defaultTabName(tab)
  ), [defaultTabName, tabNames])

  useEffect(() => {
    if (!terminalListReady && !codeServerListReady) return
    const validTabs = openTabs.filter((tab) => {
      if (tab.kind === 'terminal') return !terminalListReady || sessionById.has(tab.id)
      return !codeServerListReady || codeServerById.has(tab.id)
    })
    const validKeys = new Set(validTabs.map(tabKey))
    const selectedStillValid = selectedTab !== null && validKeys.has(tabKey(selectedTab))
    const nextSelection = selectedStillValid
      ? selectedTab
      : validTabs.find((tab) => (
          tab.kind === 'terminal' ? sessionById.has(tab.id) : codeServerById.has(tab.id)
        )) ?? null
    if (validTabs.length === openTabs.length && sameTab(selectedTab, nextSelection)) return

    setOpenTabs(validTabs)
    setActivatedKeys((current) => {
      const next = current.filter((key) => validKeys.has(key))
      if (!nextSelection) return next
      const selectedKey = tabKey(nextSelection)
      return next.includes(selectedKey) ? next : [...next, selectedKey]
    })
    if (!sameTab(selectedTab, nextSelection)) setSelectedTab(nextSelection)
  }, [codeServerById, codeServerListReady, openTabs, selectedTab, sessionById, terminalListReady])

  useEffect(() => {
    if (!terminalListReady || !codeServerListReady) return
    const liveKeys = new Set([
      ...(sessions ?? []).map((session) => tabKey({ kind: 'terminal', id: session.id })),
      ...(codeServers ?? []).map((codeServer) => tabKey({ kind: 'codeServer', id: codeServer.id })),
    ])
    setTabNames((current) => {
      const next = Object.fromEntries(Object.entries(current).filter(([key]) => liveKeys.has(key)))
      return Object.keys(next).length === Object.keys(current).length ? current : next
    })
  }, [codeServerListReady, codeServers, sessions, terminalListReady])

  useEffect(() => {
    if (!terminalListReady || !codeServerListReady) return
    localStorage.setItem(storageKey(username), JSON.stringify({
      openTabs,
      selectedTab,
      tabNames,
      sidebarCompact,
    }))
  }, [codeServerListReady, openTabs, selectedTab, sidebarCompact, tabNames, terminalListReady, username])

  const openItems = openTabs.reduce<OpenWorkspaceItem[]>((items, tab) => {
    if (tab.kind === 'terminal') {
      const session = sessionById.get(tab.id)
      if (session) items.push({ tab, session, codeServer: null })
    } else {
      const codeServer = codeServerById.get(tab.id)
      if (codeServer) items.push({ tab, session: null, codeServer })
    }
    return items
  }, [])
  const openTerminalIds = new Set(openTabs.filter((tab) => tab.kind === 'terminal').map((tab) => tab.id))
  const closedSessions = (sessions ?? []).filter((session) => !openTerminalIds.has(session.id))
  const activeTerminal = selectedTab?.kind === 'terminal' ? sessionById.get(selectedTab.id) ?? null : null
  const activeCodeServer = selectedTab?.kind === 'codeServer' ? codeServerById.get(selectedTab.id) ?? null : null
  const hasActiveItem = activeTerminal !== null || activeCodeServer !== null
  const activeTabName = selectedTab && hasActiveItem ? displayTabName(selectedTab) : 'Workspace'

  const activateTab = useCallback((tab: WorkspaceTab, focusTerminal = false) => {
    selectionChangedByUserRef.current = true
    const key = tabKey(tab)
    setActivatedKeys((current) => current.includes(key) ? current : [...current, key])
    setSelectedTab(tab)
    if (focusTerminal && tab.kind === 'terminal') {
      setTerminalFocusKeys((current) => ({
        ...current,
        [tab.id]: (current[tab.id] ?? 0) + 1,
      }))
    }
  }, [])

  const openTab = useCallback((tab: WorkspaceTab) => {
    setOpenTabs((current) => current.some((item) => sameTab(item, tab)) ? current : [...current, tab])
    activateTab(tab, tab.kind === 'terminal')
    setSidebarOpen(false)
    if (mobileNavigation) sidebarMenuButtonRef.current?.focus()
  }, [activateTab, mobileNavigation])

  const openSession = useCallback((id: string) => openTab({ kind: 'terminal', id }), [openTab])
  const openCodeServer = useCallback((id: string) => openTab({ kind: 'codeServer', id }), [openTab])

  const openSidebar = useCallback(() => {
    setSidebarOpen(true)
  }, [])

  const toggleNavigation = useCallback(() => {
    if (mobileNavigation) {
      openSidebar()
      return
    }
    setSidebarCompact((current) => !current)
  }, [mobileNavigation, openSidebar])

  const closeSidebar = useCallback((restoreFocus: boolean) => {
    setSidebarOpen(false)
    if (restoreFocus) sidebarMenuButtonRef.current?.focus()
  }, [])

  const clearTabName = useCallback((tab: WorkspaceTab) => {
    setTabNames((current) => {
      const key = tabKey(tab)
      if (!(key in current)) return current
      const next = { ...current }
      delete next[key]
      return next
    })
  }, [])

  const requestTabRename = useCallback((tab: WorkspaceTab) => {
    openExclusiveModal(() => setRenameTarget(tab))
  }, [openExclusiveModal])

  const renameTab = useCallback((tab: WorkspaceTab, name: string) => {
    const normalized = name.trim().slice(0, 48)
    if (!normalized) return
    setTabNames((current) => ({ ...current, [tabKey(tab)]: normalized }))
    setRenameTarget(null)
  }, [])

  useEffect(() => {
    if (mobileNavigation && sidebarOpen) sidebarCloseButtonRef.current?.focus()
  }, [mobileNavigation, sidebarOpen])

  useLayoutEffect(() => {
    sidebarRef.current?.toggleAttribute('inert', mobileNavigation && !sidebarOpen)
  }, [mobileNavigation, sidebarOpen])

  useEffect(() => {
    const pendingTab = pendingTabFocusRef.current
    if (!pendingTab) return
    pendingTabFocusRef.current = null

    if (pendingTab === 'workspace-action') {
      if (mobileNavigation) sidebarMenuButtonRef.current?.focus()
      else newSessionButtonRef.current?.focus()
      return
    }
    const target = document.getElementById(`tab-${pendingTab.kind}-${pendingTab.id}`)
    if (target instanceof HTMLElement) {
      target.focus()
      return
    }
    if (mobileNavigation) sidebarMenuButtonRef.current?.focus()
    else newSessionButtonRef.current?.focus()
  }, [mobileNavigation, openTabs, selectedTab])

  const closeTab = useCallback((tab: WorkspaceTab) => {
    selectionChangedByUserRef.current = true
    const index = openTabs.findIndex((item) => sameTab(item, tab))
    const remaining = openTabs.filter((item) => !sameTab(item, tab))
    const nextTab = sameTab(selectedTab, tab)
      ? remaining[Math.min(index, remaining.length - 1)] ?? null
      : selectedTab && remaining.find((item) => sameTab(item, selectedTab))
        ? selectedTab
        : remaining[Math.min(index, remaining.length - 1)] ?? null
    pendingTabFocusRef.current = nextTab ?? 'workspace-action'
    setOpenTabs(remaining)
    setActivatedKeys((current) => current.filter((key) => key !== tabKey(tab)))
    if (sameTab(selectedTab, tab)) {
      if (nextTab) activateTab(nextTab)
      else setSelectedTab(null)
    }

    if (tab.kind === 'terminal') {
      setTerminalStates((current) => {
        const next = { ...current }
        delete next[tab.id]
        return next
      })
    } else {
      setCodeServerStates((current) => {
        const next = { ...current }
        delete next[tab.id]
        return next
      })
    }
  }, [activateTab, openTabs, selectedTab])

  const handleTabKeyDown = (event: KeyboardEvent<HTMLButtonElement>, tab: WorkspaceTab) => {
    const index = openTabs.findIndex((item) => sameTab(item, tab))
    let target: WorkspaceTab | undefined
    if (event.key === 'ArrowDown') target = openTabs[(index + 1) % openTabs.length]
    if (event.key === 'ArrowUp') target = openTabs[(index - 1 + openTabs.length) % openTabs.length]
    if (event.key === 'Home') target = openTabs[0]
    if (event.key === 'End') target = openTabs[openTabs.length - 1]
    if (event.key === 'Delete') {
      event.preventDefault()
      closeTab(tab)
      return
    }
    if (event.key === 'F2') {
      event.preventDefault()
      requestTabRename(tab)
      return
    }
    if (target) {
      event.preventDefault()
      activateTab(target)
      document.getElementById(`tab-${target.kind}-${target.id}`)?.focus()
    }
  }

  const onCreated = (session: TerminalSession) => {
    clearTabName({ kind: 'terminal', id: session.id })
    setSessions((current) => [...(current ?? []).filter((item) => item.id !== session.id), session])
    setShowCreate(false)
    openSession(session.id)
  }

  const onCodeServerLaunched = (result: LaunchCodeServerResult) => {
    cancelCodeServerList()
    setCodeServerLoadError(null)
    setCodeServers((current) => [
      ...(current ?? []).filter((item) => item.id !== result.codeServer.id),
      result.codeServer,
    ])
    if (!result.reused) clearTabName({ kind: 'codeServer', id: result.codeServer.id })
    setShowLaunchCodeServer(false)
    openCodeServer(result.codeServer.id)
    if (result.reused) setActionError(null)
  }

  const onDeleted = (id: string) => {
    setDeleteTarget(null)
    clearTabName({ kind: 'terminal', id })
    closeTab({ kind: 'terminal', id })
    setSessions((current) => (current ?? []).filter((session) => session.id !== id))
  }

  const onCodeServerShutdown = (id: string) => {
    cancelCodeServerList()
    setCodeServerLoadError(null)
    setShutdownTarget(null)
    clearTabName({ kind: 'codeServer', id })
    closeTab({ kind: 'codeServer', id })
    setCodeServers((current) => (current ?? []).filter((codeServer) => codeServer.id !== id))
  }

  const onSessionChange = useCallback((updated: TerminalSession) => {
    setSessions((current) => (current ?? []).map((session) => session.id === updated.id ? updated : session))
  }, [])

  const onTerminalStateChange = useCallback((id: string, state: TerminalState) => {
    setTerminalStates((current) => current[id] === state ? current : { ...current, [id]: state })
  }, [])

  const onCodeServerStateChange = useCallback((id: string, state: CodeServerState) => {
    setCodeServerStates((current) => current[id] === state ? current : { ...current, [id]: state })
  }, [])

  const reloadCodeServer = useCallback((id: string) => {
    setCodeServerReloadKeys((current) => ({ ...current, [id]: (current[id] ?? 0) + 1 }))
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
        aria-hidden={!sidebarOpen}
        tabIndex={sidebarOpen ? 0 : -1}
        onClick={() => closeSidebar(true)}
      />

      <aside
        ref={sidebarRef}
        id="session-navigation"
        className={`sidebar ${sidebarOpen ? 'sidebar--open' : ''} ${sidebarCompact ? 'sidebar--compact' : ''}`}
        aria-label="Session navigation"
        aria-hidden={mobileNavigation && !sidebarOpen ? true : undefined}
      >
        <div className="sidebar__header">
          <div className="brand" title={`Remote Terminal · ${machineName}`}>
            <span className="brand__mark"><TerminalIcon /></span>
            <span className="brand__text">
              <span>Remote Terminal</span>
              <small>{machineName}</small>
            </span>
          </div>
          <button
            ref={sidebarCloseButtonRef}
            className="icon-button sidebar__mobile-close"
            type="button"
            aria-label="Close navigation"
            onClick={() => closeSidebar(true)}
          >
            <XIcon />
          </button>
        </div>

        <div className="sidebar__launch-actions">
          <button ref={newSessionButtonRef} className="button button--new-session" type="button" title="New terminal session" onClick={() => openExclusiveModal(() => setShowCreate(true))}>
            <PlusIcon /> <span className="sidebar__action-label">New terminal session</span>
          </button>
          <button className="button button--new-code-server" type="button" title="Launch Code Server" onClick={() => openExclusiveModal(() => setShowLaunchCodeServer(true))}>
            <CodeServerIcon /> <span className="sidebar__action-label">Launch Code Server</span>
          </button>
        </div>

        <div className="sidebar__scroll">
          <div className="sidebar-section">
            <div className="sidebar-section__heading">
              <span>Open tabs</span>
              <span className="count-badge">{openItems.length}</span>
            </div>

            {openItems.length ? (
              <div className="session-tabs" role="tablist" aria-label="Open terminal sessions and Code Servers" aria-orientation="vertical">
                {openItems.map(({ tab, session, codeServer }) => {
                  const key = tabKey(tab)
                  const selected = sameTab(tab, selectedTab)
                  const state = activatedKeys.includes(key)
                    ? session ? terminalStates[session.id] : codeServerStates[codeServer!.id]
                    : undefined
                  const name = displayTabName(tab)
                  return (
                    <div
                      className={`session-tab ${codeServer ? 'session-tab--code-server' : ''} ${selected ? 'session-tab--selected' : ''}`}
                      key={key}
                    >
                      <button
                        id={`tab-${tab.kind}-${tab.id}`}
                        className="session-tab__select"
                        type="button"
                        role="tab"
                        aria-selected={selected}
                        aria-controls={`panel-${tab.kind}-${tab.id}`}
                        tabIndex={selected || selectedTab === null ? 0 : -1}
                        title={`${name} · ${session
                          ? terminalConnectionLabel(state as TerminalState | undefined)
                          : codeServerConnectionLabel(state as CodeServerState | undefined)}`}
                        onClick={() => {
                          activateTab(tab, tab.kind === 'terminal')
                          setSidebarOpen(false)
                          if (mobileNavigation) sidebarMenuButtonRef.current?.focus()
                        }}
                        onDoubleClick={() => requestTabRename(tab)}
                        onKeyDown={(event) => handleTabKeyDown(event, tab)}
                      >
                        {session ? (
                          <span className="terminal-tab-icon" aria-hidden="true">
                            <TerminalIcon width={16} height={16} />
                            <span className={`status-dot status-dot--${state ?? 'idle'}`} />
                          </span>
                        ) : (
                          <span className={`code-server-tab-icon code-server-tab-icon--${state ?? 'idle'}`} aria-hidden="true"><CodeServerIcon width={16} height={16} /></span>
                        )}
                        <span className="session-tab__text">
                          <strong>{name}</strong>
                          <small>{session
                            ? terminalConnectionLabel(state as TerminalState | undefined)
                            : codeServerConnectionLabel(state as CodeServerState | undefined)}</small>
                        </span>
                      </button>
                      <span className="session-tab__actions">
                        <button
                          className="session-tab__action session-tab__action--rename"
                          type="button"
                          aria-label={`Rename ${name} browser tab`}
                          title="Rename browser tab (F2)"
                          onClick={() => requestTabRename(tab)}
                        >
                          <EditIcon width={14} height={14} />
                        </button>
                        <button
                          className="session-tab__action"
                          type="button"
                          aria-label={`Close ${name} browser tab`}
                          title={`Close browser tab (${session ? 'session' : 'Code Server'} keeps running)`}
                          onClick={() => closeTab(tab)}
                        >
                          <XIcon width={15} height={15} />
                        </button>
                      </span>
                    </div>
                  )
                })}
              </div>
            ) : (
              <p className="sidebar-empty">Open a running terminal or Code Server.</p>
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
                    <button className="available-session" type="button" title={`Open ${session.name || 'terminal'} session`} onClick={() => openSession(session.id)}>
                      <TerminalIcon width={16} height={16} />
                      <span>{session.name}</span>
                      <span className="available-session__open">Open</span>
                    </button>
                    <button
                      className="available-session__delete"
                      type="button"
                      aria-label={`Delete ${session.name} tmux session`}
                      title="Delete tmux session"
                      onClick={() => openExclusiveModal(() => setDeleteTarget(session))}
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

          <div className="sidebar-section sidebar-section--code-servers">
            <div className="sidebar-section__heading">
              <span>Active Code Servers</span>
              <span className="count-badge count-badge--code">{codeServers?.length ?? 0}</span>
            </div>

            {codeServers === null ? (
              <div className="session-list-skeleton" aria-label="Loading Code Servers">
                <span /><span />
              </div>
            ) : codeServers.length ? (
              <ul className="available-list code-server-list">
                {codeServers.map((codeServer) => {
                  const isOpen = openTabs.some((tab) => tab.kind === 'codeServer' && tab.id === codeServer.id)
                  return (
                    <li key={codeServer.id}>
                      <button
                        className="available-session code-server-list__open"
                        type="button"
                        aria-label={`${isOpen ? 'Focus' : 'Open'} ${codeServer.name} Code Server at ${codeServer.folderPath}`}
                        title={`${codeServer.name || 'Code Server'} · ${codeServer.folderPath}`}
                        onClick={() => openCodeServer(codeServer.id)}
                      >
                        <CodeServerIcon width={17} height={17} />
                        <span className="code-server-list__text">
                          <strong>{codeServer.name}</strong>
                          <small>{codeServer.folderPath}</small>
                        </span>
                        <span className="available-session__open">{isOpen ? 'Focus' : 'Open'}</span>
                      </button>
                      <button
                        className="available-session__delete code-server-list__shutdown"
                        type="button"
                        aria-label={`Shut down ${codeServer.name} Code Server`}
                        title="Shut down Code Server"
                        onClick={() => openExclusiveModal(() => setShutdownTarget(codeServer))}
                      >
                        <TrashIcon width={15} height={15} />
                      </button>
                    </li>
                  )
                })}
              </ul>
            ) : (
              <p className="sidebar-empty">No Code Servers are running.</p>
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

      <main className={`workspace-main ${activeCodeServer ? 'workspace-main--code-server' : ''}`}>
        <header className="topbar">
          <div className="topbar__title">
            <button
              ref={sidebarMenuButtonRef}
              className="icon-button topbar__menu"
              type="button"
              aria-label={mobileNavigation
                ? 'Open session navigation'
                : sidebarCompact ? 'Expand session navigation' : 'Compact session navigation'}
              aria-controls="session-navigation"
              aria-expanded={mobileNavigation ? sidebarOpen : !sidebarCompact}
              title={mobileNavigation
                ? 'Open navigation'
                : sidebarCompact ? 'Expand navigation' : 'Compact navigation'}
              onClick={toggleNavigation}
            >
              <MenuIcon />
            </button>
            <div>
              <p className="eyebrow">{activeCodeServer ? 'Code Server' : 'Terminal session'}</p>
              <h1>{machineName} / {activeTabName}</h1>
            </div>
          </div>
        </header>

        {terminalLoadError ? (
          <div className="workspace-notice notice notice--error" role="alert">
            <AlertIcon />
            <span>{terminalLoadError}</span>
            <button className="button button--compact button--secondary" type="button" onClick={() => void loadSessions()}>
              <RefreshIcon /> Retry
            </button>
          </div>
        ) : null}
        {codeServerLoadError ? (
          <div className="workspace-notice notice notice--error" role="alert">
            <AlertIcon />
            <span>{codeServerLoadError}</span>
            <button className="button button--compact button--secondary" type="button" onClick={() => void loadLatestCodeServers()}>
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
          {openItems.map(({ tab, session, codeServer }) => {
            const key = tabKey(tab)
            if (!activatedKeys.includes(key)) {
              return (
                <section
                  key={key}
                  id={`panel-${tab.kind}-${tab.id}`}
                  role="tabpanel"
                  aria-labelledby={`tab-${tab.kind}-${tab.id}`}
                  hidden
                />
              )
            }
            if (session) {
              return (
                <TerminalPanel
                  key={key}
                  session={session}
                  active={sameTab(tab, selectedTab)}
                  focusRequestKey={terminalFocusKeys[session.id] ?? 0}
                  onStateChange={onTerminalStateChange}
                  onSessionChange={onSessionChange}
                />
              )
            }
            return (
              <CodeServerPanel
                key={key}
                codeServer={codeServer!}
                active={sameTab(tab, selectedTab)}
                reloadKey={codeServerReloadKeys[codeServer!.id] ?? 0}
                onStateChange={onCodeServerStateChange}
                onReload={reloadCodeServer}
              />
            )
          })}

          {!hasActiveItem ? (
            <section className="workspace-empty" aria-labelledby="workspace-empty-title">
              <span className="workspace-empty__graphic"><TerminalIcon width={36} height={36} /></span>
              <p className="eyebrow">Persistent remote workspaces</p>
              <h2 id="workspace-empty-title">Choose where to pick up</h2>
              <p>
                Open a running tmux session or Code Server from the sidebar, or start a new workspace.
              </p>
              <div className="workspace-empty__actions">
                <button className="button button--primary button--large" type="button" onClick={() => openExclusiveModal(() => setShowCreate(true))}>
                  <PlusIcon /> Create a terminal session
                </button>
                <button className="button button--code-primary button--large" type="button" onClick={() => openExclusiveModal(() => setShowLaunchCodeServer(true))}>
                  <CodeServerIcon /> Launch Code Server
                </button>
              </div>
              <div className="shortcut-note"><KeyboardIcon /> Copy: hold <kbd>Shift</kbd> (or <kbd>Option</kbd> on macOS), drag over terminal text, and release. The selection is copied automatically.</div>
            </section>
          ) : null}
        </div>
      </main>

      {showCreate ? <CreateSessionModal onClose={() => setShowCreate(false)} onCreated={onCreated} /> : null}
      {showLaunchCodeServer ? <LaunchCodeServerModal onClose={() => setShowLaunchCodeServer(false)} onLaunched={onCodeServerLaunched} /> : null}
      {deleteTarget ? <DeleteSessionModal session={deleteTarget} onClose={() => setDeleteTarget(null)} onDeleted={onDeleted} /> : null}
      {shutdownTarget ? <ShutdownCodeServerModal codeServer={shutdownTarget} onClose={() => setShutdownTarget(null)} onShutdown={onCodeServerShutdown} /> : null}
      {renameTarget ? (
        <RenameTabModal
          currentName={displayTabName(renameTarget)}
          defaultName={defaultTabName(renameTarget)}
          onClose={() => setRenameTarget(null)}
          onRename={(name) => renameTab(renameTarget, name)}
        />
      ) : null}
    </div>
  )
}
