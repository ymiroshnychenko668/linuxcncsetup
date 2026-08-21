import { useEffect, useMemo, useRef, useState, type CSSProperties, type KeyboardEvent } from 'react'
import type { CatalogFolder, CatalogSetup, CatalogSnapshot } from '../api'
import { ChevronIcon, FolderIcon, ProgramIcon, SheetIcon } from './CatalogIcons'

type TreeNode =
  | { key: 'root'; kind: 'root'; level: 1; parentKey?: undefined }
  | { key: string; kind: 'folder'; level: number; parentKey: string; folder: CatalogFolder }
  | { key: string; kind: 'setup'; level: number; parentKey: string; setup: CatalogSetup }

interface Props {
  catalog: CatalogSnapshot
  query: string
  expandedFolderIds: ReadonlySet<string>
  activeFolderId?: string
  selectedSetupId?: string
  onExpandedChange: (folderId: string, expanded: boolean) => void
  onActivateRoot: () => void
  onActivateFolder: (folder: CatalogFolder) => void
  onActivateSetup: (setup: CatalogSetup) => void
  onRenameFolder: (folder: CatalogFolder) => void
  onRenameSetup: (setup: CatalogSetup) => void
  onDeleteFolder: (folder: CatalogFolder) => void
  onDeleteSetup: (setup: CatalogSetup) => void
}

const ROOT_KEY = 'root'

function normalizedSearch(value: string): string {
  return value.trim().toLocaleLowerCase('ru')
}

function setupMatches(setup: CatalogSetup, query: string): boolean {
  return [
    setup.name,
    setup.description,
    setup.program?.displayName,
    setup.setupSheet?.displayName,
    setup.programRelativePath,
    setup.setupSheetRelativePath,
  ].some((value) => value?.toLocaleLowerCase('ru').includes(query))
}

function folderKey(id: string): string { return `folder:${id}` }
function setupKey(id: string): string { return `setup:${id}` }

// Exported for deterministic tree-model tests; this module otherwise exports
// the component consumed by the workbench.
// eslint-disable-next-line react-refresh/only-export-components
export function flattenCatalogTree(
  catalog: CatalogSnapshot,
  expandedFolderIds: ReadonlySet<string>,
  rawQuery: string,
): TreeNode[] {
  const query = normalizedSearch(rawQuery)
  const foldersByID = new Map(catalog.folders.map((folder) => [folder.folderId, folder]))
  const childFolders = new Map<string | undefined, CatalogFolder[]>()
  const childSetups = new Map<string | undefined, CatalogSetup[]>()
  const compare = (left: { name: string }, right: { name: string }) => left.name.localeCompare(right.name, 'ru', { numeric: true })

  for (const folder of catalog.folders) {
    const parent = folder.parentFolderId && foldersByID.has(folder.parentFolderId)
      ? folder.parentFolderId
      : undefined
    childFolders.set(parent, [...(childFolders.get(parent) ?? []), folder])
  }
  for (const setup of catalog.setups) {
    const parent = setup.folderId && foldersByID.has(setup.folderId) ? setup.folderId : undefined
    childSetups.set(parent, [...(childSetups.get(parent) ?? []), setup])
  }
  childFolders.forEach((items) => items.sort(compare))
  childSetups.forEach((items) => items.sort(compare))

  // Track structurally rooted folders independently from what is currently
  // expanded. Otherwise collapsed descendants would be mistaken for orphaned
  // folders and rendered a second time at the root.
  const rootedFolderIds = new Set<string>()
  const markRooted = (parentId: string | undefined) => {
    for (const folder of childFolders.get(parentId) ?? []) {
      if (rootedFolderIds.has(folder.folderId)) continue
      rootedFolderIds.add(folder.folderId)
      markRooted(folder.folderId)
    }
  }
  markRooted(undefined)

  const matchingSetups = new Set<string>()
  const matchingFolders = new Set<string>()
  const includeFolderAndAncestors = (folderId?: string) => {
    const seen = new Set<string>()
    let current = folderId
    while (current && !seen.has(current)) {
      seen.add(current)
      matchingFolders.add(current)
      current = foldersByID.get(current)?.parentFolderId
    }
  }
  if (query) {
    for (const folder of catalog.folders) {
      if (`${folder.name}\n${folder.relativePath}`.toLocaleLowerCase('ru').includes(query)) {
        includeFolderAndAncestors(folder.folderId)
      }
    }
    for (const setup of catalog.setups) {
      if (setupMatches(setup, query)) {
        matchingSetups.add(setup.setupId)
        includeFolderAndAncestors(setup.folderId)
      }
    }
  }

  const nodes: TreeNode[] = [{ key: ROOT_KEY, kind: 'root', level: 1 }]
  const visited = new Set<string>()
  const appendChildren = (parentId: string | undefined, parentKey: string, level: number) => {
    for (const folder of childFolders.get(parentId) ?? []) {
      if (visited.has(folder.folderId)) continue
      visited.add(folder.folderId)
      if (query && !matchingFolders.has(folder.folderId)) continue
      const key = folderKey(folder.folderId)
      nodes.push({ key, kind: 'folder', level, parentKey, folder })
      if (query || expandedFolderIds.has(folder.folderId)) appendChildren(folder.folderId, key, level + 1)
    }
    for (const setup of childSetups.get(parentId) ?? []) {
      if (query && !matchingSetups.has(setup.setupId)) continue
      nodes.push({ key: setupKey(setup.setupId), kind: 'setup', level, parentKey, setup })
    }
  }
  appendChildren(undefined, ROOT_KEY, 2)

  // Malformed ancestry must not make otherwise valid catalog entries vanish.
  // Backend validation remains authoritative; this fallback is display-only.
  for (const folder of catalog.folders) {
    if (rootedFolderIds.has(folder.folderId) || visited.has(folder.folderId) || query && !matchingFolders.has(folder.folderId)) continue
    visited.add(folder.folderId)
    const key = folderKey(folder.folderId)
    nodes.push({ key, kind: 'folder', level: 2, parentKey: ROOT_KEY, folder })
    if (query || expandedFolderIds.has(folder.folderId)) appendChildren(folder.folderId, key, 3)
  }
  return nodes
}

function setupLabel(setup: CatalogSetup): string {
  return setup.name || setup.program?.displayName || 'Без названия'
}

export function CatalogTree({
  catalog,
  query,
  expandedFolderIds,
  activeFolderId,
  selectedSetupId,
  onExpandedChange,
  onActivateRoot,
  onActivateFolder,
  onActivateSetup,
  onRenameFolder,
  onRenameSetup,
  onDeleteFolder,
  onDeleteSetup,
}: Props) {
  const nodes = useMemo(
    () => flattenCatalogTree(catalog, expandedFolderIds, query),
    [catalog, expandedFolderIds, query],
  )
  const preferredKey = selectedSetupId
    ? setupKey(selectedSetupId)
    : activeFolderId ? folderKey(activeFolderId) : ROOT_KEY
  const [focusedKey, setFocusedKey] = useState(preferredKey)
  const refs = useRef(new Map<string, HTMLButtonElement>())

  useEffect(() => {
    if (nodes.some((node) => node.key === preferredKey)) setFocusedKey(preferredKey)
  }, [nodes, preferredKey])

  useEffect(() => {
    if (!nodes.some((node) => node.key === focusedKey)) setFocusedKey(nodes[0]?.key ?? ROOT_KEY)
  }, [focusedKey, nodes])

  const focusNode = (key: string) => {
    setFocusedKey(key)
    queueMicrotask(() => refs.current.get(key)?.focus())
  }

  const activate = (node: TreeNode) => {
    if (node.kind === 'root') onActivateRoot()
    else if (node.kind === 'folder') {
      onActivateFolder(node.folder)
      onExpandedChange(node.folder.folderId, !expandedFolderIds.has(node.folder.folderId))
    } else onActivateSetup(node.setup)
  }

  const onKeyDown = (event: KeyboardEvent<HTMLButtonElement>, node: TreeNode, index: number) => {
    let target: string | undefined
    if (event.key === 'ArrowDown') target = nodes[Math.min(nodes.length - 1, index + 1)]?.key
    else if (event.key === 'ArrowUp') target = nodes[Math.max(0, index - 1)]?.key
    else if (event.key === 'Home') target = nodes[0]?.key
    else if (event.key === 'End') target = nodes[nodes.length - 1]?.key
    else if (event.key === 'ArrowRight' && node.kind === 'folder') {
      if (!expandedFolderIds.has(node.folder.folderId)) onExpandedChange(node.folder.folderId, true)
      else if (nodes[index + 1]?.level > node.level) target = nodes[index + 1]?.key
    } else if (event.key === 'ArrowLeft') {
      if (node.kind === 'folder' && expandedFolderIds.has(node.folder.folderId)) {
        onExpandedChange(node.folder.folderId, false)
      } else target = node.parentKey
    } else if (event.key === 'Enter' || event.key === ' ') {
      activate(node)
    } else if (event.key === 'F2') {
      if (node.kind === 'folder') onRenameFolder(node.folder)
      if (node.kind === 'setup') onRenameSetup(node.setup)
    } else if (event.key === 'Delete') {
      if (node.kind === 'folder') onDeleteFolder(node.folder)
      if (node.kind === 'setup') onDeleteSetup(node.setup)
    } else return
    event.preventDefault()
    if (target) focusNode(target)
  }

  return (
    <div className="catalog-tree" role="tree" aria-label="Сетапы по каталогам">
      {nodes.map((node, index) => {
        const selected = node.kind === 'root'
          ? !selectedSetupId && !activeFolderId
          : node.kind === 'folder'
            ? !selectedSetupId && node.folder.folderId === activeFolderId
            : node.setup.setupId === selectedSetupId
        const expanded = node.kind === 'root' || node.kind === 'folder' && expandedFolderIds.has(node.folder.folderId)
        const hasChildren = node.kind === 'root' || node.kind === 'folder'
        const label = node.kind === 'root' ? catalog.destination.rootLabel
          : node.kind === 'folder' ? node.folder.name : setupLabel(node.setup)
        return (
          <button
            ref={(element) => { if (element) refs.current.set(node.key, element); else refs.current.delete(node.key) }}
            key={node.key}
            className={`catalog-tree__row catalog-tree__row--${node.kind}${selected ? ' catalog-tree__row--selected' : ''}`}
            type="button"
            role="treeitem"
            aria-level={node.level}
            aria-selected={selected}
            aria-expanded={hasChildren ? expanded : undefined}
            tabIndex={node.key === focusedKey ? 0 : -1}
            title={node.kind === 'root' ? catalog.destination.rootDisplay
              : node.kind === 'folder' ? node.folder.relativePath
                : node.setup.programRelativePath ?? `${node.setup.name} — программа не загружена`}
            style={{ '--tree-level': node.level } as CSSProperties}
            onFocus={() => setFocusedKey(node.key)}
            onKeyDown={(event) => onKeyDown(event, node, index)}
            onClick={() => activate(node)}
          >
            <span className="catalog-tree__twist" aria-hidden="true">
              {hasChildren ? <ChevronIcon direction={expanded ? 'down' : 'right'} /> : null}
            </span>
            <span className="catalog-tree__kind" aria-hidden="true">
              {node.kind === 'setup' ? <ProgramIcon /> : <FolderIcon />}
            </span>
            <span className="catalog-tree__label">{label}</span>
            {node.kind === 'setup' ? (
              <span className="catalog-tree__badges" aria-label={`${node.setup.program ? 'G-code загружен' : 'G-code отсутствует'}; ${node.setup.setupSheet ? 'Setup Sheet загружена' : 'Setup Sheet отсутствует'}`}>
                {!node.setup.program ? <span className="catalog-tree__empty-badge">пусто</span> : null}
                {node.setup.setupSheet ? <SheetIcon /> : null}
              </span>
            ) : null}
          </button>
        )
      })}
      {nodes.length === 1 && query ? <p className="catalog-tree__empty" role="status">Совпадений нет.</p> : null}
    </div>
  )
}
