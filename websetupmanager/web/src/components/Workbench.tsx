import { useCallback, useEffect, useRef, useState, type CSSProperties, type KeyboardEvent as ReactKeyboardEvent, type PointerEvent as ReactPointerEvent } from 'react'
import {
  catalogContentURL,
  createCatalogSetup,
  deleteCatalogComponent,
  deleteCatalogFolder,
  deleteCatalogSetup,
  getCatalog,
  newIdempotencyKey,
  putCatalogComponent,
  ApiError,
  type Capabilities,
  type CatalogComponent,
  type CatalogFolder,
  type CatalogSetup,
  type CatalogSnapshot,
  type Readiness,
} from '../api'
import type { Artifact, Setup } from '../domain'
import { errorMessage, formatBytes } from '../ui'
import { CatalogTree } from './CatalogTree'
import {
  ConfirmCatalogDialog,
  FolderDialog,
  SetupPropertiesDialog,
} from './CatalogDialogs'
import {
  CloseIcon,
  EditIcon,
  FolderIcon,
  LogOutIcon,
  MenuIcon,
  PlusIcon,
  RefreshIcon,
  SearchIcon,
  SheetIcon,
  TrashIcon,
  UploadIcon,
} from './CatalogIcons'
import { GCodePreview } from './GCodePreview'
import { SetupSheetViewer } from './SetupSheetViewer'

interface Props {
  capabilities: Capabilities
  username?: string
  loginRequired: boolean
  networkOffline: boolean
  readiness: Readiness
  loggingOut: boolean
  logoutError?: string
  onLogout: () => void
  onRetryReadiness: () => void
}

type DialogState =
  | { kind: 'folder'; folder?: CatalogFolder }
  | { kind: 'setup'; setup: CatalogSetup }
  | { kind: 'delete-folder'; folder: CatalogFolder }
  | { kind: 'delete-setup'; setup: CatalogSetup }
  | { kind: 'delete-component'; setup: CatalogSetup; component: CatalogComponent }

interface SetupUploadIntent {
  folderId?: string
  program: File
  sheet?: File
  createKey: string
  programKey: string
  sheetKey?: string
  setup?: CatalogSetup
}

interface ComponentUploadIntent {
  setup: CatalogSetup
  component: CatalogComponent
  file: File
  key: string
}

function legacyArtifact(setup: CatalogSetup, artifact: NonNullable<CatalogSetup['program']>, role: Artifact['role']): Artifact {
  return {
    artifactId: artifact.artifactId,
    setupId: setup.setupId,
    role,
    displayName: artifact.displayName,
    mediaType: artifact.mediaType,
    byteSize: artifact.byteSize,
    version: artifact.version,
    position: 0,
    primary: role === 'program',
    state: 'available',
    createdAt: setup.updatedAt,
    updatedAt: setup.updatedAt,
  }
}

function legacySetup(setup: CatalogSetup): Setup {
  const artifacts: Artifact[] = []
  if (setup.program) artifacts.push(legacyArtifact(setup, setup.program, 'program'))
  if (setup.setupSheet) artifacts.push(legacyArtifact(setup, setup.setupSheet, 'setup_sheet'))
  return {
    setupId: setup.setupId,
    libraryId: 'linuxcnc-catalog',
    name: setup.name,
    description: setup.description,
    status: 'draft',
    revision: setup.revision,
    source: 'created',
    artifacts,
    createdAt: setup.updatedAt,
    updatedAt: setup.updatedAt,
  }
}

function fileExtension(name: string): string {
  const index = name.lastIndexOf('.')
  return index < 0 ? '' : name.slice(index).toLocaleLowerCase('en')
}

function setupNameForProgram(name: string): string {
  const index = name.lastIndexOf('.')
  return (index > 0 ? name.slice(0, index) : name).slice(0, 200)
}

function classifySetupFiles(files: File[], gcodeExtensions: string[]): { program: File; sheet?: File } {
  const allowedPrograms = new Set(gcodeExtensions.map((extension) => extension.toLocaleLowerCase('en')))
  const programs = files.filter((file) => allowedPrograms.has(fileExtension(file.name)))
  const sheets = files.filter((file) => ['.pdf', '.html', '.htm'].includes(fileExtension(file.name)))
  const supported = new Set([...programs, ...sheets])
  if (files.length === 0) throw new Error('Выберите G-code файл.')
  if (files.some((file) => !supported.has(file))) throw new Error('Можно выбрать только один G-code и одну PDF/HTML Setup Sheet.')
  if (programs.length !== 1) throw new Error(programs.length === 0 ? 'Для нового сетапа нужен один G-code файл.' : 'Выберите только один G-code файл.')
  if (sheets.length > 1) throw new Error('Выберите не более одной Setup Sheet.')
  return { program: programs[0], sheet: sheets[0] }
}

function canSafelyReplayUpload(error: unknown, signal: AbortSignal): boolean {
  if (signal.aborted || (error instanceof DOMException && error.name === 'AbortError')) return true
  if (error instanceof TypeError) return true
  return error instanceof ApiError && (
    error.retryable
    || error.code === 'NETWORK_ERROR'
    || error.code === 'INVALID_RESPONSE'
  )
}

function upsertSetup(snapshot: CatalogSnapshot, setup: CatalogSetup): CatalogSnapshot {
  const exists = snapshot.setups.some((item) => item.setupId === setup.setupId)
  return {
    ...snapshot,
    setups: exists
      ? snapshot.setups.map((item) => item.setupId === setup.setupId ? setup : item)
      : [...snapshot.setups, setup],
  }
}

function upsertFolder(snapshot: CatalogSnapshot, folder: CatalogFolder): CatalogSnapshot {
  const exists = snapshot.folders.some((item) => item.folderId === folder.folderId)
  return {
    ...snapshot,
    folders: exists
      ? snapshot.folders.map((item) => item.folderId === folder.folderId ? folder : item)
      : [...snapshot.folders, folder],
  }
}

function folderForSetup(catalog: CatalogSnapshot, setup?: CatalogSetup): CatalogFolder | undefined {
  return setup?.folderId ? catalog.folders.find((folder) => folder.folderId === setup.folderId) : undefined
}

function displayDestination(catalog: CatalogSnapshot, setup: CatalogSetup): string {
  const relative = setup.programRelativePath
    ?? setup.setupSheetRelativePath
    ?? folderForSetup(catalog, setup)?.relativePath
  return `${catalog.destination.rootDisplay}${relative ? `/${relative}` : ''}`
}

export function Workbench({
  capabilities,
  username,
  loginRequired,
  networkOffline,
  readiness,
  loggingOut,
  logoutError,
  onLogout,
  onRetryReadiness,
}: Props) {
  const [catalog, setCatalog] = useState<CatalogSnapshot>()
  const [loading, setLoading] = useState(true)
  const [refreshing, setRefreshing] = useState(false)
  const [loadError, setLoadError] = useState<string>()
  const [query, setQuery] = useState('')
  const [selectedSetupId, setSelectedSetupId] = useState<string>()
  const [activeFolderId, setActiveFolderId] = useState<string>()
  const [selectedComponent, setSelectedComponent] = useState<CatalogComponent>('program')
  const [expandedFolderIds, setExpandedFolderIds] = useState<Set<string>>(() => new Set())
  const [dialog, setDialog] = useState<DialogState>()
  const [explorerOpen, setExplorerOpen] = useState(false)
  const [actionError, setActionError] = useState<string>()
  const [lineBySetup, setLineBySetup] = useState<Record<string, number>>({})
  const [explorerWidth, setExplorerWidth] = useState(320)
  const [destinationNotice, setDestinationNotice] = useState<string>()
  const [uploadStatus, setUploadStatus] = useState<{ label: string; loaded: number; total: number }>()
  const [uploading, setUploading] = useState(false)
  const [retryUpload, setRetryUpload] = useState<{ kind: 'setup' | 'component'; label: string }>()
  const workbenchRef = useRef<HTMLDivElement>(null)
  const explorerRef = useRef<HTMLElement>(null)
  const explorerToggleRef = useRef<HTMLButtonElement>(null)
  const explorerCloseRef = useRef<HTMLButtonElement>(null)
  const addSetupInputRef = useRef<HTMLInputElement>(null)
  const programInputRef = useRef<HTMLInputElement>(null)
  const sheetInputRef = useRef<HTMLInputElement>(null)
  const pickerReturnRef = useRef<HTMLElement | null>(null)
  const selectedSetupRef = useRef<string>()
  const programTabRef = useRef<HTMLButtonElement>(null)
  const sheetTabRef = useRef<HTMLButtonElement>(null)
  const resizeCleanupRef = useRef<() => void>()
  const setupUploadIntentRef = useRef<SetupUploadIntent>()
  const componentUploadIntentRef = useRef<ComponentUploadIntent>()
  const uploadControllerRef = useRef<AbortController>()

  const load = useCallback(async (signal?: AbortSignal, quiet = false) => {
    if (quiet) setRefreshing(true)
    else setLoading(true)
    setLoadError(undefined)
    try {
      const snapshot = await getCatalog(signal)
      setCatalog(snapshot)
      setSelectedSetupId((selected) => {
        if (selected && snapshot.setups.some((setup) => setup.setupId === selected)) return selected
        return snapshot.setups[0]?.setupId
      })
    } catch (reason) {
      if (reason instanceof DOMException && reason.name === 'AbortError') return
      setLoadError(errorMessage(reason))
    } finally {
      if (!signal?.aborted) {
        setLoading(false)
        setRefreshing(false)
      }
    }
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    void load(controller.signal)
    return () => controller.abort()
  }, [load])

  const selectedSetup = catalog?.setups.find((setup) => setup.setupId === selectedSetupId)
  const selectedFolder = catalog?.folders.find((folder) => folder.folderId === activeFolderId)
  const setupFolder = catalog && folderForSetup(catalog, selectedSetup)
  const effectiveFolderId = selectedSetup?.folderId ?? activeFolderId

  useEffect(() => {
    if (!selectedSetup) return
    if (selectedSetupRef.current !== selectedSetup.setupId) {
      selectedSetupRef.current = selectedSetup.setupId
      setSelectedComponent(selectedSetup.program ? 'program' : selectedSetup.setupSheet ? 'setup-sheet' : 'program')
    } else if (selectedComponent === 'setup-sheet' && !selectedSetup.setupSheet) {
      setSelectedComponent('program')
    }
  }, [selectedComponent, selectedSetup])

  useEffect(() => {
    if (!selectedSetup?.folderId || !catalog) return
    setExpandedFolderIds((current) => {
      const next = new Set(current)
      const seen = new Set<string>()
      let id: string | undefined = selectedSetup.folderId
      while (id && !seen.has(id)) {
        seen.add(id)
        next.add(id)
        id = catalog.folders.find((folder) => folder.folderId === id)?.parentFolderId
      }
      return next
    })
  }, [catalog, selectedSetup?.folderId])

  useEffect(() => {
    if (!explorerOpen) return
    const mobile = window.matchMedia?.('(max-width: 800px)').matches ?? false
    const inertTargets = mobile
      ? Array.from(workbenchRef.current?.querySelectorAll<HTMLElement>('.workbench-titlebar, .workbench-notice, .workbench-upload-status, .catalog-resizer, .catalog-editor, .workbench-statusbar, .destination-toast') ?? [])
      : []
    const previouslyInert = inertTargets.map((element) => element.hasAttribute('inert'))
    inertTargets.forEach((element) => element.setAttribute('inert', ''))
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        setExplorerOpen(false)
        window.setTimeout(() => explorerToggleRef.current?.focus(), 0)
        return
      }
      if (!mobile || event.key !== 'Tab') return
      const explorer = explorerRef.current
      const focusable = Array.from(explorer?.querySelectorAll<HTMLElement>('button:not(:disabled):not([tabindex="-1"]), input:not(:disabled):not([tabindex="-1"]), [tabindex]:not([tabindex="-1"])') ?? [])
      if (focusable.length === 0) return
      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      if (event.shiftKey && (document.activeElement === first || !explorer?.contains(document.activeElement))) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && (document.activeElement === last || !explorer?.contains(document.activeElement))) {
        event.preventDefault()
        first.focus()
      }
    }
    window.addEventListener('keydown', handleKeyDown, true)
    queueMicrotask(() => explorerCloseRef.current?.focus())
    return () => {
      window.removeEventListener('keydown', handleKeyDown, true)
      inertTargets.forEach((element, index) => {
        if (!previouslyInert[index]) element.removeAttribute('inert')
      })
    }
  }, [explorerOpen])

  useEffect(() => {
    if (dialog) setExplorerOpen(false)
  }, [dialog])

  useEffect(() => {
    if (!destinationNotice) return
    const timeout = window.setTimeout(() => setDestinationNotice(undefined), 8000)
    return () => window.clearTimeout(timeout)
  }, [destinationNotice])

  useEffect(() => () => {
    resizeCleanupRef.current?.()
    uploadControllerRef.current?.abort()
  }, [])

  const savedSetup = (setup: CatalogSetup) => {
    setCatalog((current) => current ? upsertSetup(current, setup) : current)
    setSelectedSetupId(setup.setupId)
    setActiveFolderId(undefined)
    setActionError(undefined)
    if (catalog) setDestinationNotice(`Сохранено в LinuxCNC: ${displayDestination(catalog, setup)}`)
  }

  const savedFolder = (folder: CatalogFolder) => {
    setCatalog((current) => current ? upsertFolder(current, folder) : current)
    setActiveFolderId(folder.folderId)
    setSelectedSetupId(undefined)
    setExpandedFolderIds((current) => new Set(current).add(folder.folderId))
    setActionError(undefined)
    void load(undefined, true)
  }

  const deleteSetup = async (setup: CatalogSetup, key: string) => {
    await deleteCatalogSetup(setup, key)
    setCatalog((current) => current ? { ...current, setups: current.setups.filter((item) => item.setupId !== setup.setupId) } : current)
    setSelectedSetupId(undefined)
    setSelectedComponent('program')
  }

  const deleteFolder = async (folder: CatalogFolder, key: string) => {
    await deleteCatalogFolder(folder, key)
    setCatalog((current) => current ? { ...current, folders: current.folders.filter((item) => item.folderId !== folder.folderId) } : current)
    setActiveFolderId(undefined)
  }

  const deleteComponent = async (setup: CatalogSetup, component: CatalogComponent, key: string) => {
    const saved = await deleteCatalogComponent(setup, component, key)
    savedSetup(saved)
    if (component === 'setup-sheet') setSelectedComponent('program')
  }

  const selectSetup = (setup: CatalogSetup) => {
    const returnToEditor = explorerOpen
    selectedSetupRef.current = setup.setupId
    setSelectedSetupId(setup.setupId)
    const component = setup.program ? 'program' : setup.setupSheet ? 'setup-sheet' : 'program'
    setSelectedComponent(component)
    setActiveFolderId(undefined)
    setExplorerOpen(false)
    if (returnToEditor) window.setTimeout(() => (component === 'program' ? programTabRef : sheetTabRef).current?.focus(), 0)
  }

  const selectSetupSheet = (setup: CatalogSetup) => {
    const returnToEditor = explorerOpen
    selectedSetupRef.current = setup.setupId
    setSelectedSetupId(setup.setupId)
    setSelectedComponent('setup-sheet')
    setActiveFolderId(undefined)
    setExplorerOpen(false)
    if (returnToEditor) window.setTimeout(() => sheetTabRef.current?.focus(), 0)
  }

  const selectEditorTab = (component: CatalogComponent, moveFocus = false) => {
    setSelectedComponent(component)
    if (moveFocus) queueMicrotask(() => (component === 'program' ? programTabRef : sheetTabRef).current?.focus())
  }

  const editorTabKeyDown = (event: ReactKeyboardEvent<HTMLButtonElement>) => {
    if (!selectedSetup?.setupSheet) return
    let target: CatalogComponent | undefined
    if (event.key === 'ArrowLeft' || event.key === 'Home') target = 'program'
    else if (event.key === 'ArrowRight' || event.key === 'End') target = 'setup-sheet'
    if (!target) return
    event.preventDefault()
    selectEditorTab(target, true)
  }

  const restorePickerFocus = (fallback?: CatalogComponent) => {
    const target = pickerReturnRef.current
    pickerReturnRef.current = null
    window.setTimeout(() => {
      if (target?.isConnected) target.focus()
      else if (fallback) (fallback === 'program' ? programTabRef : sheetTabRef).current?.focus()
    }, 0)
  }

  useEffect(() => {
    const listeners: Array<[HTMLInputElement | null, EventListener]> = [
      [addSetupInputRef.current, () => restorePickerFocus()],
      [programInputRef.current, () => restorePickerFocus('program')],
      [sheetInputRef.current, () => restorePickerFocus('setup-sheet')],
    ]
    listeners.forEach(([input, listener]) => input?.addEventListener('cancel', listener))
    return () => listeners.forEach(([input, listener]) => input?.removeEventListener('cancel', listener))
  })

  const runComponentUpload = async (intent: ComponentUploadIntent) => {
    if (uploading || uploadControllerRef.current) return
    const controller = new AbortController()
    uploadControllerRef.current = controller
    setUploading(true)
    setRetryUpload(undefined)
    setActionError(undefined)
    const label = intent.component === 'program' ? `Загружаем ${intent.file.name}` : `Прикрепляем ${intent.file.name}`
    setUploadStatus({ label, loaded: 0, total: intent.file.size })
    try {
      const saved = await putCatalogComponent(intent.setup, intent.component, intent.file, intent.key, {
        signal: controller.signal,
        onProgress: (loaded, total) => setUploadStatus({
          label, loaded, total,
        }),
      })
      savedSetup(saved)
      setSelectedComponent(intent.component)
      if (catalog) setDestinationNotice(`Сохранено в LinuxCNC: ${displayDestination(catalog, saved)}`)
      if (componentUploadIntentRef.current === intent) componentUploadIntentRef.current = undefined
    } catch (reason) {
      const replayable = canSafelyReplayUpload(reason, controller.signal)
      setActionError(controller.signal.aborted ? 'Загрузка отменена. Её можно безопасно повторить с тем же запросом.' : errorMessage(reason))
      if (replayable) {
        setRetryUpload({ kind: 'component', label: intent.file.name })
      } else {
        if (componentUploadIntentRef.current === intent) componentUploadIntentRef.current = undefined
        setRetryUpload(undefined)
      }
    } finally {
      if (uploadControllerRef.current === controller) uploadControllerRef.current = undefined
      setUploading(false)
      setUploadStatus(undefined)
      restorePickerFocus(intent.component)
    }
  }

  const uploadComponent = (setup: CatalogSetup, component: CatalogComponent, file: File) => {
    if (uploading || uploadControllerRef.current) return
    const intent = { setup, component, file, key: newIdempotencyKey() }
    componentUploadIntentRef.current = intent
    return runComponentUpload(intent)
  }

  const runSetupUpload = async (intent: SetupUploadIntent) => {
    if (!catalog || uploading || uploadControllerRef.current) return
    const controller = new AbortController()
    uploadControllerRef.current = controller
    setUploading(true)
    setRetryUpload(undefined)
    setActionError(undefined)
    let current = intent.setup
    try {
      if (!current) {
        setUploadStatus({ label: 'Создаём запись G-code…', loaded: 0, total: intent.program.size + (intent.sheet?.size ?? 0) })
        current = await createCatalogSetup({
          folderId: intent.folderId,
          name: setupNameForProgram(intent.program.name),
        }, intent.createKey, controller.signal)
        intent.setup = current
        setCatalog((snapshot) => snapshot ? upsertSetup(snapshot, current!) : snapshot)
        setSelectedSetupId(current.setupId)
        setSelectedComponent('program')
      }
      if (!current.program) {
        setUploadStatus({ label: `Загружаем ${intent.program.name}`, loaded: 0, total: intent.program.size })
        current = await putCatalogComponent(current, 'program', intent.program, intent.programKey, {
          signal: controller.signal,
          onProgress: (loaded, total) => setUploadStatus({ label: `Загружаем ${intent.program.name}`, loaded, total }),
        })
        intent.setup = current
        savedSetup(current)
      }
      if (intent.sheet && !current.setupSheet) {
        if (!intent.sheetKey) throw new Error('UPLOAD_INTENT_INVALID')
        setUploadStatus({ label: `Прикрепляем ${intent.sheet.name}`, loaded: 0, total: intent.sheet.size })
        current = await putCatalogComponent(current, 'setup-sheet', intent.sheet, intent.sheetKey, {
          signal: controller.signal,
          onProgress: (loaded, total) => setUploadStatus({ label: `Прикрепляем ${intent.sheet!.name}`, loaded, total }),
        })
        intent.setup = current
        savedSetup(current)
      }
      setSelectedComponent('program')
      setDestinationNotice(`Сохранено в LinuxCNC: ${displayDestination(catalog, current)}`)
      if (setupUploadIntentRef.current === intent) setupUploadIntentRef.current = undefined
    } catch (reason) {
      if (current) setCatalog((snapshot) => snapshot ? upsertSetup(snapshot, current!) : snapshot)
      const replayable = canSafelyReplayUpload(reason, controller.signal)
      setActionError(controller.signal.aborted ? 'Загрузка отменена. Её можно безопасно продолжить с незавершённого шага.' : errorMessage(reason))
      if (replayable) {
        setRetryUpload({ kind: 'setup', label: intent.sheet ? `${intent.program.name} + ${intent.sheet.name}` : intent.program.name })
      } else {
        if (setupUploadIntentRef.current === intent) setupUploadIntentRef.current = undefined
        setRetryUpload(undefined)
      }
    } finally {
      if (uploadControllerRef.current === controller) uploadControllerRef.current = undefined
      setUploading(false)
      setUploadStatus(undefined)
      restorePickerFocus('program')
    }
  }

  const addSetupFiles = (files: File[]) => {
    if (!catalog || uploading || uploadControllerRef.current) return
    setupUploadIntentRef.current = undefined
    setRetryUpload(undefined)
    let selected: { program: File; sheet?: File }
    try {
      selected = classifySetupFiles(files, capabilities.gcodeExtensions)
    } catch (reason) {
      setActionError(errorMessage(reason))
      restorePickerFocus()
      return
    }
    const intent = {
      folderId: effectiveFolderId,
      program: selected.program,
      sheet: selected.sheet,
      createKey: newIdempotencyKey(),
      programKey: newIdempotencyKey(),
      sheetKey: selected.sheet ? newIdempotencyKey() : undefined,
    }
    setupUploadIntentRef.current = intent
    return runSetupUpload(intent)
  }

  const retryPendingUpload = () => {
    if (retryUpload?.kind === 'setup' && setupUploadIntentRef.current) {
      void runSetupUpload(setupUploadIntentRef.current)
    } else if (retryUpload?.kind === 'component' && componentUploadIntentRef.current) {
      void runComponentUpload(componentUploadIntentRef.current)
    }
  }

  const startResize = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    event.preventDefault()
    resizeCleanupRef.current?.()
    const startX = event.clientX
    const startWidth = explorerWidth
    const move = (moveEvent: PointerEvent) => {
      setExplorerWidth(Math.max(260, Math.min(480, startWidth + moveEvent.clientX - startX)))
    }
    const stop = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', stop)
      window.removeEventListener('pointercancel', stop)
      document.body.classList.remove('catalog-resizing')
      resizeCleanupRef.current = undefined
    }
    resizeCleanupRef.current = stop
    document.body.classList.add('catalog-resizing')
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', stop)
    window.addEventListener('pointercancel', stop)
  }

  const openDeleteFolder = (folder: CatalogFolder) => setDialog({ kind: 'delete-folder', folder })
  const openDeleteSetup = (setup: CatalogSetup) => setDialog({ kind: 'delete-setup', setup })
  const programArtifact = selectedSetup?.program ? legacyArtifact(selectedSetup, selectedSetup.program, 'program') : undefined
  const sheetArtifact = selectedSetup?.setupSheet ? legacyArtifact(selectedSetup, selectedSetup.setupSheet, 'setup_sheet') : undefined
  const previewSetup = selectedSetup ? legacySetup(selectedSetup) : undefined
  const activeRelativePath = (selectedComponent === 'setup-sheet'
    ? selectedSetup?.setupSheetRelativePath
    : selectedSetup?.programRelativePath)
    ?? selectedSetup?.programRelativePath
    ?? selectedSetup?.setupSheetRelativePath
    ?? setupFolder?.relativePath
  const destinationText = catalog
    ? `${catalog.destination.rootDisplay}${activeRelativePath ? `/${activeRelativePath}` : ''}`
    : 'LinuxCNC PROGRAM_PREFIX'

  return (
    <div ref={workbenchRef} className="catalog-workbench" style={{ '--catalog-explorer-width': `${explorerWidth}px` } as CSSProperties}>
      <a className="skip-link" href="#catalog-editor">К просмотру G-code</a>
      <header className="workbench-titlebar">
        <div className="workbench-brand" aria-label="Web Setup Manager">
          <span className="workbench-brand__mark" aria-hidden="true">WS</span>
          <span><strong>Web Setup Manager</strong><small>LinuxCNC catalog</small></span>
        </div>
        <div className="workbench-machine-path" title={catalog?.destination.rootDisplay}>
          <FolderIcon /><span>{catalog?.destination.rootDisplay ?? 'Подключаем каталог LinuxCNC…'}</span>
        </div>
        <div className="workbench-titlebar__actions">
          <button className="workbench-button workbench-button--primary" type="button" disabled={!catalog || uploading} onClick={(event) => { pickerReturnRef.current = event.currentTarget; addSetupInputRef.current?.click() }}><PlusIcon />{uploading ? 'Загружаем…' : 'Добавить'}</button>
          <button ref={explorerToggleRef} className="workbench-icon-button workbench-explorer-toggle" type="button" aria-label="Открыть дерево файлов" aria-expanded={explorerOpen} onClick={() => setExplorerOpen(true)}><MenuIcon /></button>
          {loginRequired ? <div className="workbench-user"><span aria-hidden="true">{(username?.[0] ?? 'U').toUpperCase()}</span><strong>{username}</strong></div> : <span className="workbench-local-mode">Локальный режим</span>}
          {loginRequired ? <button className="workbench-icon-button" type="button" title="Выйти" aria-label="Выйти" disabled={loggingOut} onClick={onLogout}>{loggingOut ? <span className="spinner spinner--small" aria-hidden="true" /> : <LogOutIcon />}</button> : null}
        </div>
      </header>

      <input
        ref={addSetupInputRef}
        className="visually-hidden"
        type="file"
        multiple
        accept={[...capabilities.gcodeExtensions, '.pdf', '.html', '.htm'].join(',')}
        aria-label="Файлы нового сетапа"
        tabIndex={-1}
        disabled={!catalog || uploading}
        onChange={(event) => {
          const files = Array.from(event.currentTarget.files ?? [])
          event.currentTarget.value = ''
          void addSetupFiles(files)
        }}
      />
      <input
        ref={programInputRef}
        className="visually-hidden"
        type="file"
        accept={capabilities.gcodeExtensions.join(',')}
        aria-label="Выбрать G-code"
        tabIndex={-1}
        disabled={!selectedSetup || uploading}
        onChange={(event) => {
          const file = event.currentTarget.files?.[0]
          event.currentTarget.value = ''
          if (selectedSetup && file) void uploadComponent(selectedSetup, 'program', file)
        }}
      />
      <input
        ref={sheetInputRef}
        className="visually-hidden"
        type="file"
        accept=".pdf,.html,.htm"
        aria-label="Выбрать Setup Sheet"
        tabIndex={-1}
        disabled={!selectedSetup || uploading}
        onChange={(event) => {
          const file = event.currentTarget.files?.[0]
          event.currentTarget.value = ''
          if (selectedSetup && file) void uploadComponent(selectedSetup, 'setup-sheet', file)
        }}
      />

      {networkOffline ? <div className="workbench-notice" role="status">Нет соединения с Web Setup Manager. Открытый просмотр сохранён.</div> : null}
      {!readiness.ok ? <div className="workbench-notice workbench-notice--error" role="alert"><span>{readiness.message ?? 'Backend или каталог LinuxCNC временно недоступен.'}</span><button type="button" onClick={onRetryReadiness}>Повторить</button></div> : null}
      {logoutError ? <div className="workbench-notice workbench-notice--error" role="alert">Не удалось выйти: {logoutError}</div> : null}
      {uploadStatus ? <div className="workbench-upload-status" role="status"><span>{uploadStatus.label}</span>{uploadStatus.total > 0 ? <progress max={uploadStatus.total} value={Math.min(uploadStatus.loaded, uploadStatus.total)} /> : null}<button type="button" onClick={() => uploadControllerRef.current?.abort()}>Отменить</button></div> : null}

      <main className="catalog-workspace">
        <button className={`catalog-explorer-scrim${explorerOpen ? ' catalog-explorer-scrim--visible' : ''}`} type="button" aria-label="Закрыть дерево файлов по нажатию вне панели" tabIndex={explorerOpen ? 0 : -1} onClick={() => { setExplorerOpen(false); window.setTimeout(() => explorerToggleRef.current?.focus(), 0) }} />
        <aside ref={explorerRef} className={`catalog-explorer${explorerOpen ? ' catalog-explorer--open' : ''}`} aria-label="Файлы сетапов">
          <header className="catalog-explorer__header">
            <strong>ФАЙЛЫ</strong>
            <div>
              <button className="workbench-icon-button" type="button" title="Новый каталог" aria-label="Новый каталог" disabled={!catalog} onClick={() => setDialog({ kind: 'folder' })}><FolderIcon /></button>
              <button className="workbench-icon-button" type="button" title="Добавить G-code и необязательную Setup Sheet" aria-label="Добавить сетап" disabled={!catalog || uploading} onClick={(event) => { pickerReturnRef.current = event.currentTarget; addSetupInputRef.current?.click() }}><PlusIcon /></button>
              <button className="workbench-icon-button" type="button" title="Обновить каталог" aria-label="Обновить каталог" disabled={refreshing} onClick={() => void load(undefined, true)}>{refreshing ? <span className="spinner spinner--small" aria-hidden="true" /> : <RefreshIcon />}</button>
              <button ref={explorerCloseRef} className="workbench-icon-button catalog-explorer__close" type="button" aria-label="Закрыть дерево файлов" onClick={() => { setExplorerOpen(false); window.setTimeout(() => explorerToggleRef.current?.focus(), 0) }}><CloseIcon /></button>
            </div>
          </header>
          <label className="catalog-search" htmlFor="catalog-search-input"><SearchIcon /><span className="visually-hidden">Поиск файлов</span><input id="catalog-search-input" type="search" value={query} placeholder="Поиск G-code и Setup Sheet" onChange={(event) => setQuery(event.target.value)} />{query ? <button type="button" aria-label="Очистить поиск" onClick={() => setQuery('')}><CloseIcon /></button> : null}</label>

          <div className="catalog-context-actions" aria-label="Действия выбранного элемента">
            <span>{selectedSetup ? selectedSetup.name : selectedFolder?.relativePath ?? catalog?.destination.rootLabel ?? 'LinuxCNC'}</span>
            <div>
              {selectedSetup ? <>
                <button type="button" title="Свойства" aria-label="Свойства сетапа" onClick={() => setDialog({ kind: 'setup', setup: selectedSetup })}><EditIcon /></button>
                <button type="button" title="Удалить" aria-label="Удалить сетап" onClick={() => openDeleteSetup(selectedSetup)}><TrashIcon /></button>
              </> : selectedFolder ? <>
                <button type="button" title="Переименовать или переместить" aria-label="Переименовать или переместить каталог" onClick={() => setDialog({ kind: 'folder', folder: selectedFolder })}><EditIcon /></button>
                <button type="button" title="Удалить пустой каталог" aria-label="Удалить каталог" onClick={() => openDeleteFolder(selectedFolder)}><TrashIcon /></button>
              </> : null}
            </div>
          </div>

          <div className="catalog-explorer__tree">
            {catalog ? <CatalogTree
              catalog={catalog}
              query={query}
              expandedFolderIds={expandedFolderIds}
              activeFolderId={activeFolderId}
              selectedSetupId={selectedSetupId}
              selectedComponent={selectedComponent}
              onExpandedChange={(folderId, expanded) => setExpandedFolderIds((current) => {
                const next = new Set(current)
                if (expanded) next.add(folderId); else next.delete(folderId)
                return next
              })}
              onActivateRoot={() => { setActiveFolderId(undefined); setSelectedSetupId(undefined) }}
              onActivateFolder={(folder) => { setActiveFolderId(folder.folderId); setSelectedSetupId(undefined) }}
              onActivateSetup={selectSetup}
              onActivateSetupSheet={selectSetupSheet}
              onRenameFolder={(folder) => setDialog({ kind: 'folder', folder })}
              onRenameSetup={(setup) => setDialog({ kind: 'setup', setup })}
              onDeleteFolder={openDeleteFolder}
              onDeleteSetup={openDeleteSetup}
              onDeleteSetupSheet={(setup) => setDialog({ kind: 'delete-component', setup, component: 'setup-sheet' })}
            /> : <div className="catalog-tree-loading" role="status">Загружаем…</div>}
          </div>
        </aside>

        <div
          className="catalog-resizer"
          role="separator"
          aria-label="Изменить ширину дерева файлов"
          aria-orientation="vertical"
          aria-valuemin={260}
          aria-valuemax={480}
          aria-valuenow={explorerWidth}
          tabIndex={0}
          onPointerDown={startResize}
          onKeyDown={(event) => {
            if (event.key === 'ArrowLeft') setExplorerWidth((width) => Math.max(260, width - 16))
            else if (event.key === 'ArrowRight') setExplorerWidth((width) => Math.min(480, width + 16))
            else if (event.key === 'Home') setExplorerWidth(260)
            else if (event.key === 'End') setExplorerWidth(480)
            else return
            event.preventDefault()
          }}
        />

        <section id="catalog-editor" className="catalog-editor" aria-label="Просмотр файла" tabIndex={-1}>
          <div className="editor-tabs">
            <div className="editor-tablist" role="tablist" aria-label="Файлы выбранного сетапа">
              <button
                ref={programTabRef}
                id="editor-tab-program"
                className={`editor-tab${selectedComponent === 'program' ? ' editor-tab--active' : ''}`}
                type="button"
                role="tab"
                aria-selected={selectedComponent === 'program'}
                aria-controls="editor-file-panel"
                tabIndex={selectedComponent === 'program' ? 0 : -1}
                disabled={!selectedSetup}
                onClick={() => selectEditorTab('program')}
                onKeyDown={editorTabKeyDown}
              >
                <span className="editor-tab__dot" aria-hidden="true" />
                {selectedSetup?.program?.displayName ?? selectedSetup?.name ?? 'G-code'}
              </button>
              {selectedSetup?.setupSheet ? <button
                ref={sheetTabRef}
                id="editor-tab-setup-sheet"
                className={`editor-tab${selectedComponent === 'setup-sheet' ? ' editor-tab--active' : ''}`}
                type="button"
                role="tab"
                aria-selected={selectedComponent === 'setup-sheet'}
                aria-controls="editor-file-panel"
                tabIndex={selectedComponent === 'setup-sheet' ? 0 : -1}
                onClick={() => selectEditorTab('setup-sheet')}
                onKeyDown={editorTabKeyDown}
              ><SheetIcon />{selectedSetup.setupSheet.displayName}</button> : null}
            </div>
            <div className="editor-tabs__actions">
              {selectedSetup ? <>
                {selectedComponent === 'program' ? <button className="editor-file-action" type="button" disabled={uploading} onClick={(event) => { pickerReturnRef.current = event.currentTarget; programInputRef.current?.click() }}>{selectedSetup.program ? 'Заменить G-code' : 'Добавить G-code'}</button> : null}
                {selectedComponent === 'setup-sheet' ? <button className="editor-file-action" type="button" disabled={uploading} onClick={(event) => { pickerReturnRef.current = event.currentTarget; sheetInputRef.current?.click() }}>Заменить Sheet</button> : null}
                {!selectedSetup.setupSheet ? <button className="editor-file-action editor-file-action--attach" type="button" disabled={uploading || !selectedSetup.program} onClick={(event) => { pickerReturnRef.current = event.currentTarget; sheetInputRef.current?.click() }}><PlusIcon />Setup Sheet</button> : null}
                {selectedComponent === 'setup-sheet' && selectedSetup.setupSheet ? <button className="workbench-icon-button" type="button" title="Отсоединить Setup Sheet" aria-label="Удалить Setup Sheet" onClick={() => setDialog({ kind: 'delete-component', setup: selectedSetup, component: 'setup-sheet' })}><TrashIcon /></button> : null}
                <button className="workbench-icon-button" type="button" title="Свойства и каталог" aria-label="Свойства и каталог сетапа" onClick={() => setDialog({ kind: 'setup', setup: selectedSetup })}><EditIcon /></button>
                <button className="workbench-icon-button" type="button" title="Удалить G-code и Setup Sheet" aria-label="Удалить сетап" onClick={() => openDeleteSetup(selectedSetup)}><TrashIcon /></button>
              </> : null}
            </div>
          </div>

          {actionError ? <div className="editor-inline-error" role="alert"><span>{actionError}</span><div>{retryUpload ? <button type="button" disabled={uploading} onClick={retryPendingUpload}>Повторить: {retryUpload.label}</button> : null}<button type="button" onClick={() => { setActionError(undefined); setRetryUpload(undefined) }}>Закрыть</button></div></div> : null}
          {loadError ? <div className="editor-inline-error" role="alert"><span>Каталог не удалось загрузить: {loadError}</span><button type="button" onClick={() => void load()}>Повторить</button></div> : null}

          <div
            id="editor-file-panel"
            className="editor-surface"
            role="tabpanel"
            aria-labelledby={selectedComponent === 'setup-sheet' ? 'editor-tab-setup-sheet' : 'editor-tab-program'}
          >
            {loading && !catalog ? <div className="workbench-state" aria-busy="true"><span className="spinner" aria-hidden="true" /><p role="status">Открываем каталог программ LinuxCNC…</p></div> : null}
            {!loading && catalog && catalog.setups.length === 0 ? <div className="workbench-state"><UploadIcon width={42} height={42} /><h1>Добавьте первый G-code</h1><p>Можно выбрать G-code отдельно или сразу вместе с одной PDF/HTML Setup Sheet.</p><div><button className="workbench-button" type="button" onClick={(event) => { pickerReturnRef.current = event.currentTarget; addSetupInputRef.current?.click() }}><PlusIcon />Добавить сетап</button><button className="workbench-button" type="button" onClick={() => setDialog({ kind: 'folder' })}><FolderIcon />Создать каталог</button></div></div> : null}
            {!loading && catalog && catalog.setups.length > 0 && !selectedSetup ? <div className="workbench-state"><h1>Выберите G-code слева</h1><p>Setup Sheet, если она прикреплена, находится дочерней строкой под G-code.</p></div> : null}
            {selectedSetup && selectedComponent === 'program' && !programArtifact ? <div className="workbench-state workbench-state--incomplete"><span className="incomplete-icon" aria-hidden="true">{'{}'}</span><h1>Нужен G-code</h1><p>Эта запись не завершена. Добавьте программу; существующая Setup Sheet останется прикреплённой.</p><div><button className="workbench-button" type="button" disabled={uploading} onClick={(event) => { pickerReturnRef.current = event.currentTarget; programInputRef.current?.click() }}><UploadIcon />Добавить G-code</button></div></div> : null}
            {selectedSetup && selectedComponent === 'program' && programArtifact && previewSetup ? <GCodePreview
              key={`${programArtifact.artifactId}:${programArtifact.version}`}
              compact
              setup={previewSetup}
              artifact={programArtifact}
              contentUrl={catalogContentURL(selectedSetup.setupId, 'program')}
              initialLine={lineBySetup[selectedSetup.setupId] ?? 1}
              onLineChanged={(line) => setLineBySetup((current) => ({ ...current, [selectedSetup.setupId]: line }))}
              onArtifactChanged={() => void load(undefined, true)}
            /> : null}
            {selectedSetup && selectedComponent === 'setup-sheet' && sheetArtifact && previewSetup ? <SetupSheetViewer
              inline
              setup={previewSetup}
              artifact={sheetArtifact}
              contentUrl={catalogContentURL(selectedSetup.setupId, 'setup-sheet')}
              onReplace={(trigger) => {
                pickerReturnRef.current = trigger ?? sheetTabRef.current
                sheetInputRef.current?.click()
              }}
            /> : null}
          </div>
        </section>
      </main>

      <footer className="workbench-statusbar">
        <span className={`status-connection${readiness.ok ? ' status-connection--ok' : ''}`}><i aria-hidden="true" />{readiness.ok ? 'LinuxCNC каталог подключён' : 'Каталог недоступен'}</span>
        <code title={destinationText}>{destinationText}</code>
        {selectedSetup ? <span>rev {selectedSetup.revision}{selectedSetup.program ? ` · ${formatBytes(selectedSetup.program.byteSize)}` : ' · без G-code'}</span> : null}
      </footer>
      {destinationNotice ? <div className="destination-toast" role="status"><span>{destinationNotice}</span><button type="button" aria-label="Закрыть уведомление" onClick={() => setDestinationNotice(undefined)}><CloseIcon /></button></div> : null}

      {catalog && dialog?.kind === 'folder' ? <FolderDialog folders={catalog.folders} destination={catalog.destination} initialParentFolderId={activeFolderId} folder={dialog.folder} onClose={() => setDialog(undefined)} onSaved={savedFolder} /> : null}
      {catalog && dialog?.kind === 'setup' ? <SetupPropertiesDialog setup={dialog.setup} folders={catalog.folders} destination={catalog.destination} onClose={() => setDialog(undefined)} onSaved={savedSetup} /> : null}
      {dialog?.kind === 'delete-folder' ? <ConfirmCatalogDialog title="Удалить каталог" description={`Каталог «${dialog.folder.relativePath}» будет удалён, только если он пуст. Сетапы не удаляются вместе с каталогом.`} confirmLabel="Удалить пустой каталог" onClose={() => setDialog(undefined)} onConfirm={(key) => deleteFolder(dialog.folder, key)} /> : null}
      {dialog?.kind === 'delete-setup' ? <ConfirmCatalogDialog title="Удалить сетап" description={`«${dialog.setup.name}» и его G-code/Setup Sheet будут удалены из каталога LinuxCNC.`} confirmLabel="Удалить сетап" onClose={() => setDialog(undefined)} onConfirm={(key) => deleteSetup(dialog.setup, key)} /> : null}
      {dialog?.kind === 'delete-component' ? <ConfirmCatalogDialog title={dialog.component === 'program' ? 'Удалить G-code' : 'Удалить Setup Sheet'} description="Сетап останется в каталоге как неполный, второй файл не изменится." confirmLabel="Удалить файл" onClose={() => setDialog(undefined)} onConfirm={(key) => deleteComponent(dialog.setup, dialog.component, key)} /> : null}
    </div>
  )
}
