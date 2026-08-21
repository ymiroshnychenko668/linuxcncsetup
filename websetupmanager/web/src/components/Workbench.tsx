import { useCallback, useEffect, useRef, useState, type CSSProperties, type PointerEvent as ReactPointerEvent } from 'react'
import {
  catalogContentURL,
  deleteCatalogComponent,
  deleteCatalogFolder,
  deleteCatalogSetup,
  getCatalog,
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
  ComponentUploadDialog,
  ConfirmCatalogDialog,
  FolderDialog,
  SetupPropertiesDialog,
  UploadSetupDialog,
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
  | { kind: 'upload'; title?: string }
  | { kind: 'folder'; folder?: CatalogFolder }
  | { kind: 'setup'; setup: CatalogSetup }
  | { kind: 'component'; setup: CatalogSetup; component: CatalogComponent }
  | { kind: 'delete-folder'; folder: CatalogFolder }
  | { kind: 'delete-setup'; setup: CatalogSetup }
  | { kind: 'delete-component'; setup: CatalogSetup; component: CatalogComponent }

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

function pathSegments(rootLabel: string, relativePath?: string): string[] {
  return [rootLabel, ...(relativePath?.split('/').filter(Boolean) ?? [])]
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
  const [expandedFolderIds, setExpandedFolderIds] = useState<Set<string>>(() => new Set())
  const [dialog, setDialog] = useState<DialogState>()
  const [sheetOpen, setSheetOpen] = useState(false)
  const [explorerOpen, setExplorerOpen] = useState(false)
  const [actionError, setActionError] = useState<string>()
  const [lineBySetup, setLineBySetup] = useState<Record<string, number>>({})
  const [explorerWidth, setExplorerWidth] = useState(320)
  const [destinationNotice, setDestinationNotice] = useState<string>()
  const explorerToggleRef = useRef<HTMLButtonElement>(null)
  const explorerCloseRef = useRef<HTMLButtonElement>(null)
  const resizeCleanupRef = useRef<() => void>()

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
    const close = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return
      setExplorerOpen(false)
      explorerToggleRef.current?.focus()
    }
    window.addEventListener('keydown', close)
    queueMicrotask(() => explorerCloseRef.current?.focus())
    return () => window.removeEventListener('keydown', close)
  }, [explorerOpen])

  useEffect(() => {
    if (!destinationNotice) return
    const timeout = window.setTimeout(() => setDestinationNotice(undefined), 8000)
    return () => window.clearTimeout(timeout)
  }, [destinationNotice])

  useEffect(() => () => resizeCleanupRef.current?.(), [])

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
    setSheetOpen(false)
  }

  const deleteFolder = async (folder: CatalogFolder, key: string) => {
    await deleteCatalogFolder(folder, key)
    setCatalog((current) => current ? { ...current, folders: current.folders.filter((item) => item.folderId !== folder.folderId) } : current)
    setActiveFolderId(undefined)
  }

  const deleteComponent = async (setup: CatalogSetup, component: CatalogComponent, key: string) => {
    const saved = await deleteCatalogComponent(setup, component, key)
    savedSetup(saved)
    if (component === 'setup-sheet') setSheetOpen(false)
  }

  const selectSetup = (setup: CatalogSetup) => {
    setSelectedSetupId(setup.setupId)
    setActiveFolderId(undefined)
    setSheetOpen(false)
    setExplorerOpen(false)
  }

  const startResize = (event: ReactPointerEvent<HTMLDivElement>) => {
    if (event.button !== 0) return
    event.preventDefault()
    resizeCleanupRef.current?.()
    const startX = event.clientX
    const startWidth = explorerWidth
    const move = (moveEvent: PointerEvent) => {
      setExplorerWidth(Math.max(260, Math.min(480, startWidth + startX - moveEvent.clientX)))
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
  const activeRelativePath = selectedSetup?.programRelativePath
    ?? selectedSetup?.setupSheetRelativePath
    ?? setupFolder?.relativePath
  const destinationText = catalog
    ? `${catalog.destination.rootDisplay}${activeRelativePath ? `/${activeRelativePath}` : ''}`
    : 'LinuxCNC PROGRAM_PREFIX'

  return (
    <div className="catalog-workbench" style={{ '--catalog-explorer-width': `${explorerWidth}px` } as CSSProperties}>
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
          <button className="workbench-button workbench-button--primary" type="button" disabled={!catalog} onClick={() => setDialog({ kind: 'upload' })}><UploadIcon />Загрузить</button>
          <button ref={explorerToggleRef} className="workbench-icon-button workbench-explorer-toggle" type="button" aria-label="Открыть дерево сетапов" aria-expanded={explorerOpen} onClick={() => setExplorerOpen(true)}><MenuIcon /></button>
          {loginRequired ? <div className="workbench-user"><span aria-hidden="true">{(username?.[0] ?? 'U').toUpperCase()}</span><strong>{username}</strong></div> : <span className="workbench-local-mode">Локальный режим</span>}
          {loginRequired ? <button className="workbench-icon-button" type="button" title="Выйти" aria-label="Выйти" disabled={loggingOut} onClick={onLogout}>{loggingOut ? <span className="spinner spinner--small" aria-hidden="true" /> : <LogOutIcon />}</button> : null}
        </div>
      </header>

      {networkOffline ? <div className="workbench-notice" role="status">Нет соединения с Web Setup Manager. Открытый просмотр сохранён.</div> : null}
      {!readiness.ok ? <div className="workbench-notice workbench-notice--error" role="alert"><span>{readiness.message ?? 'Backend или каталог LinuxCNC временно недоступен.'}</span><button type="button" onClick={onRetryReadiness}>Повторить</button></div> : null}
      {logoutError ? <div className="workbench-notice workbench-notice--error" role="alert">Не удалось выйти: {logoutError}</div> : null}

      <main className="catalog-workspace">
        <section id="catalog-editor" className="catalog-editor" aria-label="Просмотр сетапа" tabIndex={-1}>
          <div className="editor-tabs" aria-label="Содержимое сетапа">
            <button className="editor-tab editor-tab--active" type="button" aria-current="page">
              <span className="editor-tab__dot" aria-hidden="true" />
              {selectedSetup?.program?.displayName ?? selectedSetup?.name ?? 'G-code'}
            </button>
            {selectedSetup ? <button className="editor-tab" type="button" onClick={() => selectedSetup.setupSheet ? setSheetOpen(true) : setDialog({ kind: 'component', setup: selectedSetup, component: 'setup-sheet' })}><SheetIcon />{selectedSetup.setupSheet?.displayName ?? 'Добавить Setup Sheet'}</button> : null}
            <div className="editor-tabs__actions">
              {selectedSetup ? <>
                <button className="workbench-icon-button" type="button" title="Свойства и каталог" aria-label="Свойства и каталог сетапа" onClick={() => setDialog({ kind: 'setup', setup: selectedSetup })}><EditIcon /></button>
                <button className="workbench-icon-button" type="button" title="Удалить сетап" aria-label="Удалить сетап" onClick={() => openDeleteSetup(selectedSetup)}><TrashIcon /></button>
              </> : null}
            </div>
          </div>

          <nav className="editor-breadcrumbs" aria-label="Расположение программы LinuxCNC">
            {pathSegments(catalog?.destination.rootLabel ?? 'LinuxCNC', activeRelativePath).map((segment, index) => <span key={`${segment}-${index}`}>{index > 0 ? <b aria-hidden="true">›</b> : null}{segment}</span>)}
          </nav>

          {selectedSetup ? <div className="editor-commandbar" aria-label="Файлы выбранного сетапа">
            <span><strong>G-code</strong>{selectedSetup.program?.displayName ?? 'не загружен'}</span>
            <button type="button" onClick={() => setDialog({ kind: 'component', setup: selectedSetup, component: 'program' })}>{selectedSetup.program ? 'Заменить' : 'Добавить'}</button>
            {selectedSetup.program ? <button type="button" onClick={() => setDialog({ kind: 'delete-component', setup: selectedSetup, component: 'program' })}>Удалить</button> : null}
            <i aria-hidden="true" />
            <span><strong>Setup Sheet</strong>{selectedSetup.setupSheet?.displayName ?? 'не загружена'}</span>
            {selectedSetup.setupSheet ? <button type="button" onClick={() => setSheetOpen(true)}>Открыть</button> : null}
            <button type="button" onClick={() => setDialog({ kind: 'component', setup: selectedSetup, component: 'setup-sheet' })}>{selectedSetup.setupSheet ? 'Заменить' : 'Добавить'}</button>
            {selectedSetup.setupSheet ? <button type="button" onClick={() => setDialog({ kind: 'delete-component', setup: selectedSetup, component: 'setup-sheet' })}>Удалить</button> : null}
          </div> : null}
          {actionError ? <div className="editor-inline-error" role="alert"><span>{actionError}</span><button type="button" onClick={() => setActionError(undefined)}>Закрыть</button></div> : null}
          {loadError ? <div className="editor-inline-error" role="alert"><span>Каталог не удалось загрузить: {loadError}</span><button type="button" onClick={() => void load()}>Повторить</button></div> : null}

          <div className="editor-surface">
            {loading && !catalog ? <div className="workbench-state" aria-busy="true"><span className="spinner" aria-hidden="true" /><p role="status">Открываем каталог программ LinuxCNC…</p></div> : null}
            {!loading && catalog && catalog.setups.length === 0 ? <div className="workbench-state"><UploadIcon width={42} height={42} /><h1>Каталог готов к загрузке</h1><p>Создайте сетап в нужном каталоге. Программу и Setup Sheet можно загружать независимо.</p><div><button className="workbench-button workbench-button--primary" type="button" onClick={() => setDialog({ kind: 'upload' })}><UploadIcon />Загрузить первый сетап</button><button className="workbench-button" type="button" onClick={() => setDialog({ kind: 'folder' })}><FolderIcon />Создать каталог</button></div></div> : null}
            {!loading && catalog && catalog.setups.length > 0 && !selectedSetup ? <div className="workbench-state"><h1>Выберите сетап справа</h1><p>G-code откроется здесь, а точный путь LinuxCNC будет показан в строке состояния.</p></div> : null}
            {selectedSetup && !programArtifact ? <div className="workbench-state workbench-state--incomplete"><span className="incomplete-icon" aria-hidden="true">{'{}'}</span><h1>Программа ещё не загружена</h1><p>Это нормальный неполный сетап. Добавьте G-code сейчас или оставьте только Setup Sheet.</p><div><button className="workbench-button workbench-button--primary" type="button" onClick={() => setDialog({ kind: 'component', setup: selectedSetup, component: 'program' })}><UploadIcon />Добавить G-code</button>{selectedSetup.setupSheet ? <button className="workbench-button" type="button" onClick={() => setSheetOpen(true)}><SheetIcon />Открыть Setup Sheet</button> : null}</div></div> : null}
            {selectedSetup && programArtifact && previewSetup ? <GCodePreview
              compact
              setup={previewSetup}
              artifact={programArtifact}
              contentUrl={catalogContentURL(selectedSetup.setupId, 'program')}
              initialLine={lineBySetup[selectedSetup.setupId] ?? 1}
              onLineChanged={(line) => setLineBySetup((current) => ({ ...current, [selectedSetup.setupId]: line }))}
              onArtifactChanged={() => void load(undefined, true)}
            /> : null}
          </div>
        </section>

        <div
          className="catalog-resizer"
          role="separator"
          aria-label="Изменить ширину дерева сетапов"
          aria-orientation="vertical"
          aria-valuemin={260}
          aria-valuemax={480}
          aria-valuenow={explorerWidth}
          tabIndex={0}
          onPointerDown={startResize}
          onKeyDown={(event) => {
            if (event.key === 'ArrowLeft') setExplorerWidth((width) => Math.min(480, width + 16))
            else if (event.key === 'ArrowRight') setExplorerWidth((width) => Math.max(260, width - 16))
            else if (event.key === 'Home') setExplorerWidth(480)
            else if (event.key === 'End') setExplorerWidth(260)
            else return
            event.preventDefault()
          }}
        />
        <button className={`catalog-explorer-scrim${explorerOpen ? ' catalog-explorer-scrim--visible' : ''}`} type="button" aria-label="Закрыть дерево сетапов по нажатию вне панели" tabIndex={explorerOpen ? 0 : -1} onClick={() => { setExplorerOpen(false); explorerToggleRef.current?.focus() }} />
        <aside className={`catalog-explorer${explorerOpen ? ' catalog-explorer--open' : ''}`} aria-label="Каталог сетапов">
          <header className="catalog-explorer__header">
            <strong>СЕТАПЫ</strong>
            <div>
              <button className="workbench-icon-button" type="button" title="Новый каталог" aria-label="Новый каталог" disabled={!catalog} onClick={() => setDialog({ kind: 'folder' })}><FolderIcon /></button>
              <button className="workbench-icon-button" type="button" title="Неполный сетап" aria-label="Создать сетап" disabled={!catalog} onClick={() => setDialog({ kind: 'upload', title: 'Новый сетап' })}><PlusIcon /></button>
              <button className="workbench-icon-button" type="button" title="Обновить каталог" aria-label="Обновить каталог" disabled={refreshing} onClick={() => void load(undefined, true)}>{refreshing ? <span className="spinner spinner--small" aria-hidden="true" /> : <RefreshIcon />}</button>
              <button ref={explorerCloseRef} className="workbench-icon-button catalog-explorer__close" type="button" aria-label="Закрыть дерево сетапов" onClick={() => { setExplorerOpen(false); explorerToggleRef.current?.focus() }}><CloseIcon /></button>
            </div>
          </header>
          <label className="catalog-search" htmlFor="catalog-search-input"><SearchIcon /><span className="visually-hidden">Поиск сетапов</span><input id="catalog-search-input" type="search" value={query} placeholder="Поиск сетапов и файлов" onChange={(event) => setQuery(event.target.value)} />{query ? <button type="button" aria-label="Очистить поиск" onClick={() => setQuery('')}><CloseIcon /></button> : null}</label>

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
              onExpandedChange={(folderId, expanded) => setExpandedFolderIds((current) => {
                const next = new Set(current)
                if (expanded) next.add(folderId); else next.delete(folderId)
                return next
              })}
              onActivateRoot={() => { setActiveFolderId(undefined); setSelectedSetupId(undefined) }}
              onActivateFolder={(folder) => { setActiveFolderId(folder.folderId); setSelectedSetupId(undefined) }}
              onActivateSetup={selectSetup}
              onRenameFolder={(folder) => setDialog({ kind: 'folder', folder })}
              onRenameSetup={(setup) => setDialog({ kind: 'setup', setup })}
              onDeleteFolder={openDeleteFolder}
              onDeleteSetup={openDeleteSetup}
            /> : <div className="catalog-tree-loading" role="status">Загружаем…</div>}
          </div>
          <footer className="catalog-explorer__destination"><small>КАТАЛОГ LINUXCNC</small><code title={catalog?.destination.rootDisplay}>{catalog?.destination.rootDisplay ?? '—'}</code></footer>
        </aside>
      </main>

      <footer className="workbench-statusbar">
        <span className={`status-connection${readiness.ok ? ' status-connection--ok' : ''}`}><i aria-hidden="true" />{readiness.ok ? 'LinuxCNC каталог подключён' : 'Каталог недоступен'}</span>
        <code title={destinationText}>{destinationText}</code>
        {selectedSetup ? <span>rev {selectedSetup.revision}{selectedSetup.program ? ` · ${formatBytes(selectedSetup.program.byteSize)}` : ' · без G-code'}</span> : null}
      </footer>
      {destinationNotice ? <div className="destination-toast" role="status"><span>{destinationNotice}</span><button type="button" aria-label="Закрыть уведомление" onClick={() => setDestinationNotice(undefined)}><CloseIcon /></button></div> : null}

      {catalog && dialog?.kind === 'upload' ? <UploadSetupDialog folders={catalog.folders} destination={catalog.destination} gcodeExtensions={capabilities.gcodeExtensions} initialFolderId={effectiveFolderId} title={dialog.title} onClose={() => setDialog(undefined)} onSaved={savedSetup} /> : null}
      {catalog && dialog?.kind === 'folder' ? <FolderDialog folders={catalog.folders} destination={catalog.destination} initialParentFolderId={activeFolderId} folder={dialog.folder} onClose={() => setDialog(undefined)} onSaved={savedFolder} /> : null}
      {catalog && dialog?.kind === 'setup' ? <SetupPropertiesDialog setup={dialog.setup} folders={catalog.folders} destination={catalog.destination} onClose={() => setDialog(undefined)} onSaved={savedSetup} /> : null}
      {catalog && dialog?.kind === 'component' ? <ComponentUploadDialog setup={dialog.setup} component={dialog.component} destination={catalog.destination} folderRelativePath={folderForSetup(catalog, dialog.setup)?.relativePath} gcodeExtensions={capabilities.gcodeExtensions} onClose={() => setDialog(undefined)} onSaved={savedSetup} /> : null}
      {dialog?.kind === 'delete-folder' ? <ConfirmCatalogDialog title="Удалить каталог" description={`Каталог «${dialog.folder.relativePath}» будет удалён, только если он пуст. Сетапы не удаляются вместе с каталогом.`} confirmLabel="Удалить пустой каталог" onClose={() => setDialog(undefined)} onConfirm={(key) => deleteFolder(dialog.folder, key)} /> : null}
      {dialog?.kind === 'delete-setup' ? <ConfirmCatalogDialog title="Удалить сетап" description={`«${dialog.setup.name}» и его G-code/Setup Sheet будут удалены из каталога LinuxCNC.`} confirmLabel="Удалить сетап" onClose={() => setDialog(undefined)} onConfirm={(key) => deleteSetup(dialog.setup, key)} /> : null}
      {dialog?.kind === 'delete-component' ? <ConfirmCatalogDialog title={dialog.component === 'program' ? 'Удалить G-code' : 'Удалить Setup Sheet'} description="Сетап останется в каталоге как неполный, второй файл не изменится." confirmLabel="Удалить файл" onClose={() => setDialog(undefined)} onConfirm={(key) => deleteComponent(dialog.setup, dialog.component, key)} /> : null}
      {selectedSetup && previewSetup && sheetArtifact && sheetOpen ? <SetupSheetViewer setup={previewSetup} artifact={sheetArtifact} contentUrl={catalogContentURL(selectedSetup.setupId, 'setup-sheet')} onClose={() => setSheetOpen(false)} onReplace={() => { setSheetOpen(false); setDialog({ kind: 'component', setup: selectedSetup, component: 'setup-sheet' }) }} /> : null}
    </div>
  )
}
