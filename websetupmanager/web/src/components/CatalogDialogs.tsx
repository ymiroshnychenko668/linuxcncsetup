import { useMemo, useRef, useState, type FormEvent } from 'react'
import {
  ApiError,
  createCatalogFolder,
  createCatalogSetup,
  newIdempotencyKey,
  putCatalogComponent,
  updateCatalogFolder,
  updateCatalogSetup,
  type CatalogComponent,
  type CatalogDestination,
  type CatalogFolder,
  type CatalogSetup,
} from '../api'
import { errorMessage, formatBytes } from '../ui'
import { Modal } from './Modal'

function folderLabel(folder: CatalogFolder): string {
  return folder.relativePath || folder.name
}

function joinDisplayPath(...parts: Array<string | undefined>): string {
  return parts.filter((part): part is string => Boolean(part)).join('/').replaceAll('//', '/')
}

function catalogMutationError(reason: unknown): string {
  if (reason instanceof ApiError && (reason.status === 409 || reason.code.includes('CONFLICT'))) {
    return `Конфликт изменений: элемент уже обновлён в другом окне. Выбранный файл и введённые поля сохранены; обновите каталог и повторите действие. ${reason.message}`
  }
  return errorMessage(reason)
}

function useMutationKey() {
  const state = useRef({ key: newIdempotencyKey(), attempted: false })
  return {
    forAttempt: () => {
      state.current.attempted = true
      return state.current.key
    },
    requestChanged: () => {
      if (!state.current.attempted) return
      state.current = { key: newIdempotencyKey(), attempted: false }
    },
  }
}

function selectedFolder(folders: CatalogFolder[], folderId?: string): CatalogFolder | undefined {
  return folders.find((folder) => folder.folderId === folderId)
}

interface FolderSelectProps {
  id: string
  folders: CatalogFolder[]
  value?: string
  rootLabel: string
  disabled?: boolean
  excludedIds?: ReadonlySet<string>
  onChange: (folderId?: string) => void
}

function FolderSelect({ id, folders, value, rootLabel, disabled, excludedIds, onChange }: FolderSelectProps) {
  return (
    <select id={id} value={value ?? ''} disabled={disabled} onChange={(event) => onChange(event.target.value || undefined)}>
      <option value="">{rootLabel}</option>
      {[...folders]
        .filter((folder) => !excludedIds?.has(folder.folderId))
        .sort((left, right) => left.relativePath.localeCompare(right.relativePath, 'ru', { numeric: true }))
        .map((folder) => <option key={folder.folderId} value={folder.folderId}>{folderLabel(folder)}</option>)}
    </select>
  )
}

interface UploadSetupDialogProps {
  folders: CatalogFolder[]
  destination: CatalogDestination
  gcodeExtensions: string[]
  initialFolderId?: string
  title?: string
  onClose: () => void
  onSaved: (setup: CatalogSetup) => void
}

function basenameWithoutExtension(name: string): string {
  const index = name.lastIndexOf('.')
  return (index > 0 ? name.slice(0, index) : name).slice(0, 200)
}

export function UploadSetupDialog({
  folders,
  destination,
  gcodeExtensions,
  initialFolderId,
  title = 'Загрузить сетап',
  onClose,
  onSaved,
}: UploadSetupDialogProps) {
  const [folderId, setFolderId] = useState(initialFolderId)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [program, setProgram] = useState<File>()
  const [sheet, setSheet] = useState<File>()
  const [created, setCreated] = useState<CatalogSetup>()
  const [pending, setPending] = useState(false)
  const [progress, setProgress] = useState<{ label: string; loaded: number; total: number }>()
  const [error, setError] = useState<string>()
  const nameTouched = useRef(false)
  const createKey = useMutationKey()
  const programKey = useMutationKey()
  const sheetKey = useMutationKey()
  const folder = selectedFolder(folders, folderId)
  const folderPath = folder?.relativePath
  const programPath = program ? joinDisplayPath(destination.rootDisplay, folderPath, program.name) : undefined
  const sheetPath = sheet ? joinDisplayPath(destination.rootDisplay, folderPath, sheet.name) : undefined

  const chooseProgram = (file?: File) => {
    programKey.requestChanged()
    setProgram(file)
    if (file && !nameTouched.current && name.trim() === '') setName(basenameWithoutExtension(file.name))
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!name.trim() || pending) return
    setPending(true)
    setError(undefined)
    let current = created
    try {
      if (!current) {
        setProgress({ label: 'Создаём сетап…', loaded: 0, total: (program?.size ?? 0) + (sheet?.size ?? 0) })
        current = await createCatalogSetup({ folderId, name: name.trim(), description }, createKey.forAttempt())
        setCreated(current)
        onSaved(current)
      }
      if (program && !current.program) {
        setProgress({ label: `Загружаем G-code: ${program.name}`, loaded: 0, total: program.size })
        current = await putCatalogComponent(current, 'program', program, programKey.forAttempt(), {
          onProgress: (loaded, total) => setProgress({ label: `Загружаем G-code: ${program.name}`, loaded, total }),
        })
        setCreated(current)
        onSaved(current)
      }
      if (sheet && !current.setupSheet) {
        setProgress({ label: `Загружаем Setup Sheet: ${sheet.name}`, loaded: 0, total: sheet.size })
        current = await putCatalogComponent(current, 'setup-sheet', sheet, sheetKey.forAttempt(), {
          onProgress: (loaded, total) => setProgress({ label: `Загружаем Setup Sheet: ${sheet.name}`, loaded, total }),
        })
        setCreated(current)
        onSaved(current)
      }
      setProgress({ label: 'Сетап сохранён в каталоге LinuxCNC.', loaded: 1, total: 1 })
      onClose()
    } catch (reason) {
      setError(catalogMutationError(reason))
      if (current) onSaved(current)
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal
      title={title}
      description="Один сетап содержит не более одной G-code-программы и одной Setup Sheet. Любой из файлов можно добавить позже."
      onClose={onClose}
      closeDisabled={pending}
      className="catalog-modal catalog-modal--upload"
      footer={(
        <>
          <button className="button button--quiet" type="button" disabled={pending} onClick={onClose}>Отмена</button>
          <button className="button button--primary" type="submit" form="catalog-upload-form" disabled={pending || !name.trim()}>
            {pending ? 'Сохраняем…' : program || sheet ? 'Создать и загрузить' : 'Создать неполный сетап'}
          </button>
        </>
      )}
    >
      <form id="catalog-upload-form" className="catalog-form" onSubmit={(event) => void submit(event)}>
        <label htmlFor="catalog-upload-folder"><span>Каталог LinuxCNC</span>
          <FolderSelect id="catalog-upload-folder" folders={folders} value={folderId} rootLabel={destination.rootLabel} disabled={pending || Boolean(created)} onChange={(value) => { createKey.requestChanged(); setFolderId(value) }} />
        </label>
        <label htmlFor="catalog-upload-name"><span>Название сетапа</span><input id="catalog-upload-name" value={name} maxLength={200} disabled={pending || Boolean(created)} autoFocus required onChange={(event) => { createKey.requestChanged(); nameTouched.current = true; setName(event.target.value) }} /></label>
        <label htmlFor="catalog-upload-description"><span>Описание <small>необязательно</small></span><textarea id="catalog-upload-description" rows={2} value={description} disabled={pending || Boolean(created)} onChange={(event) => { createKey.requestChanged(); setDescription(event.target.value) }} /></label>
        <div className="catalog-file-grid">
          <label className="catalog-file-field" htmlFor="catalog-upload-program">
            <span>G-code программа</span>
            <input id="catalog-upload-program" aria-label="G-code программа" type="file" accept={gcodeExtensions.join(',')} disabled={pending || Boolean(created?.program)} onChange={(event) => chooseProgram(event.target.files?.[0])} />
            <small>{program ? `${program.name} · ${formatBytes(program.size)}` : 'Можно оставить пустым'}</small>
          </label>
          <label className="catalog-file-field" htmlFor="catalog-upload-sheet">
            <span>Setup Sheet</span>
            <input id="catalog-upload-sheet" aria-label="Setup Sheet" type="file" accept=".pdf,.html,.htm" disabled={pending || Boolean(created?.setupSheet)} onChange={(event) => { sheetKey.requestChanged(); setSheet(event.target.files?.[0]) }} />
            <small>{sheet ? `${sheet.name} · ${formatBytes(sheet.size)}` : 'PDF или автономный HTML; необязательно'}</small>
          </label>
        </div>
        <section className="destination-preview" aria-labelledby="destination-preview-title">
          <strong id="destination-preview-title">Куда попадут файлы</strong>
          <code>{programPath ?? joinDisplayPath(destination.rootDisplay, folderPath)}</code>
          {sheetPath ? <code>{sheetPath}</code> : null}
          <small>Этот каталог доступен LinuxCNC через {destination.rootLabel}.</small>
        </section>
        {created && error ? <p className="catalog-partial-note" role="status">Сетап уже создан и остаётся доступным как неполный. Повтор продолжит с незагруженного файла.</p> : null}
        {progress ? <div className="catalog-upload-progress" role="status"><span>{progress.label}</span>{progress.total > 0 ? <progress max={progress.total} value={Math.min(progress.loaded, progress.total)} /> : null}</div> : null}
        {error ? <p className="form-error" role="alert">{error}</p> : null}
      </form>
    </Modal>
  )
}

interface FolderDialogProps {
  folders: CatalogFolder[]
  destination: CatalogDestination
  initialParentFolderId?: string
  folder?: CatalogFolder
  onClose: () => void
  onSaved: (folder: CatalogFolder) => void
}

function descendantFolderIDs(folder: CatalogFolder, folders: CatalogFolder[]): Set<string> {
  const result = new Set([folder.folderId])
  let changed = true
  while (changed) {
    changed = false
    for (const candidate of folders) {
      if (candidate.parentFolderId && result.has(candidate.parentFolderId) && !result.has(candidate.folderId)) {
        result.add(candidate.folderId)
        changed = true
      }
    }
  }
  return result
}

export function FolderDialog({ folders, destination, initialParentFolderId, folder, onClose, onSaved }: FolderDialogProps) {
  const [name, setName] = useState(folder?.name ?? '')
  const [parentFolderId, setParentFolderId] = useState(folder?.parentFolderId ?? initialParentFolderId)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const key = useMutationKey()
  const excluded = useMemo(() => folder ? descendantFolderIDs(folder, folders) : undefined, [folder, folders])
  const parent = selectedFolder(folders, parentFolderId)
  const preview = joinDisplayPath(destination.rootDisplay, parent?.relativePath, name.trim() || 'Новый каталог')

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!name.trim() || pending) return
    setPending(true)
    setError(undefined)
    try {
      const saved = folder
        ? await updateCatalogFolder(folder, { name: name.trim(), parentFolderId: parentFolderId ?? null }, key.forAttempt())
        : await createCatalogFolder(parentFolderId, name.trim(), key.forAttempt())
      onSaved(saved)
      onClose()
    } catch (reason) {
      setError(catalogMutationError(reason))
    } finally { setPending(false) }
  }

  return <Modal
    title={folder ? 'Переименовать или переместить каталог' : 'Новый каталог'}
    description="Каталог создаётся только внутри фиксированного каталога программ LinuxCNC."
    onClose={onClose}
    closeDisabled={pending}
    className="catalog-modal"
    footer={<><button className="button button--quiet" type="button" disabled={pending} onClick={onClose}>Отмена</button><button className="button button--primary" type="submit" form="catalog-folder-form" disabled={pending || !name.trim()}>{folder ? 'Сохранить' : 'Создать каталог'}</button></>}
  >
    <form id="catalog-folder-form" className="catalog-form" onSubmit={(event) => void submit(event)}>
      <label htmlFor="catalog-folder-name"><span>Название</span><input id="catalog-folder-name" value={name} maxLength={200} autoFocus required onChange={(event) => { key.requestChanged(); setName(event.target.value) }} /></label>
      <label htmlFor="catalog-folder-parent"><span>Родительский каталог</span><FolderSelect id="catalog-folder-parent" folders={folders} value={parentFolderId} rootLabel={destination.rootLabel} excludedIds={excluded} onChange={(value) => { key.requestChanged(); setParentFolderId(value) }} /></label>
      <section className="destination-preview"><strong>Итоговый каталог</strong><code>{preview}</code></section>
      {error ? <p className="form-error" role="alert">{error}</p> : null}
    </form>
  </Modal>
}

interface SetupPropertiesDialogProps {
  setup: CatalogSetup
  folders: CatalogFolder[]
  destination: CatalogDestination
  onClose: () => void
  onSaved: (setup: CatalogSetup) => void
}

export function SetupPropertiesDialog({ setup, folders, destination, onClose, onSaved }: SetupPropertiesDialogProps) {
  const [name, setName] = useState(setup.name)
  const [description, setDescription] = useState(setup.description ?? '')
  const [folderId, setFolderId] = useState(setup.folderId)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const key = useMutationKey()
  const folder = selectedFolder(folders, folderId)
  const filename = setup.program?.displayName ?? setup.setupSheet?.displayName ?? setup.name

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!name.trim() || pending) return
    setPending(true)
    setError(undefined)
    try {
      const saved = await updateCatalogSetup(setup, {
        name: name.trim(), description, folderId: folderId ?? null,
      }, key.forAttempt())
      onSaved(saved)
      onClose()
    } catch (reason) {
      setError(catalogMutationError(reason))
    } finally { setPending(false) }
  }

  return <Modal
    title="Свойства сетапа"
    description="Название относится к сетапу; G-code и Setup Sheet остаются его единственными файлами."
    onClose={onClose}
    closeDisabled={pending}
    className="catalog-modal"
    footer={<><button className="button button--quiet" type="button" disabled={pending} onClick={onClose}>Отмена</button><button className="button button--primary" type="submit" form="catalog-setup-form" disabled={pending || !name.trim()}>Сохранить</button></>}
  >
    <form id="catalog-setup-form" className="catalog-form" onSubmit={(event) => void submit(event)}>
      <label htmlFor="catalog-setup-name"><span>Название</span><input id="catalog-setup-name" value={name} maxLength={200} autoFocus required onChange={(event) => { key.requestChanged(); setName(event.target.value) }} /></label>
      <label htmlFor="catalog-setup-description"><span>Описание</span><textarea id="catalog-setup-description" rows={3} value={description} onChange={(event) => { key.requestChanged(); setDescription(event.target.value) }} /></label>
      <label htmlFor="catalog-setup-folder"><span>Каталог LinuxCNC</span><FolderSelect id="catalog-setup-folder" folders={folders} value={folderId} rootLabel={destination.rootLabel} onChange={(value) => { key.requestChanged(); setFolderId(value) }} /></label>
      <section className="destination-preview"><strong>Расположение</strong><code>{joinDisplayPath(destination.rootDisplay, folder?.relativePath, filename)}</code></section>
      {error ? <p className="form-error" role="alert">{error}</p> : null}
    </form>
  </Modal>
}

interface ComponentUploadDialogProps {
  setup: CatalogSetup
  component: CatalogComponent
  destination: CatalogDestination
  folderRelativePath?: string
  gcodeExtensions: string[]
  onClose: () => void
  onSaved: (setup: CatalogSetup) => void
}

export function ComponentUploadDialog({ setup, component, destination, folderRelativePath, gcodeExtensions, onClose, onSaved }: ComponentUploadDialogProps) {
  const [file, setFile] = useState<File>()
  const [pending, setPending] = useState(false)
  const [progress, setProgress] = useState(0)
  const [error, setError] = useState<string>()
  const key = useMutationKey()
  const existing = component === 'program' ? setup.program : setup.setupSheet
  const label = component === 'program' ? 'G-code программу' : 'Setup Sheet'

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!file || pending) return
    setPending(true)
    setError(undefined)
    try {
      const saved = await putCatalogComponent(setup, component, file, key.forAttempt(), {
        onProgress: (loaded, total) => setProgress(total > 0 ? loaded / total : 0),
      })
      onSaved(saved)
      onClose()
    } catch (reason) {
      setError(catalogMutationError(reason))
    } finally { setPending(false) }
  }

  return <Modal
    title={`${existing ? 'Заменить' : 'Добавить'} ${label}`}
    description={existing ? `Текущий файл «${existing.displayName}» будет атомарно заменён.` : `${label} станет частью «${setup.name}».`}
    onClose={onClose}
    closeDisabled={pending}
    className="catalog-modal"
    footer={<><button className="button button--quiet" type="button" disabled={pending} onClick={onClose}>Отмена</button><button className="button button--primary" type="submit" form="catalog-component-form" disabled={pending || !file}>{pending ? 'Загружаем…' : existing ? 'Заменить' : 'Загрузить'}</button></>}
  >
    <form id="catalog-component-form" className="catalog-form" onSubmit={(event) => void submit(event)}>
      <label className="catalog-file-field" htmlFor="catalog-component-file"><span>{label}</span><input id="catalog-component-file" aria-label={label} type="file" autoFocus required accept={component === 'program' ? gcodeExtensions.join(',') : '.pdf,.html,.htm'} onChange={(event) => { key.requestChanged(); setFile(event.target.files?.[0]) }} /><small>{file ? `${file.name} · ${formatBytes(file.size)}` : 'Выберите один файл'}</small></label>
      <section className="destination-preview"><strong>Назначение LinuxCNC</strong><code>{joinDisplayPath(destination.rootDisplay, file ? folderRelativePath : undefined, file?.name ?? existing?.relativePath ?? folderRelativePath)}</code></section>
      {pending ? <progress max={1} value={progress} aria-label="Прогресс загрузки" /> : null}
      {error ? <p className="form-error" role="alert">{error}</p> : null}
    </form>
  </Modal>
}

interface ConfirmCatalogDialogProps {
  title: string
  description: string
  confirmLabel: string
  onClose: () => void
  onConfirm: (idempotencyKey: string) => Promise<void>
}

export function ConfirmCatalogDialog({ title, description, confirmLabel, onClose, onConfirm }: ConfirmCatalogDialogProps) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<string>()
  const key = useMutationKey()
  const confirm = async () => {
    if (pending) return
    setPending(true)
    setError(undefined)
    try { await onConfirm(key.forAttempt()); onClose() }
    catch (reason) {
      setError(catalogMutationError(reason))
    }
    finally { setPending(false) }
  }
  return <Modal title={title} description={description} onClose={onClose} closeDisabled={pending} className="catalog-modal" footer={<><button className="button button--quiet" type="button" disabled={pending} onClick={onClose}>Отмена</button><button className="button button--danger" type="button" disabled={pending} onClick={() => void confirm()}>{pending ? 'Выполняется…' : confirmLabel}</button></>}>
    <p className="catalog-danger-note">Операция относится только к выбранному элементу внутри каталога программ LinuxCNC.</p>
    {error ? <p className="form-error" role="alert">{error}</p> : null}
  </Modal>
}
