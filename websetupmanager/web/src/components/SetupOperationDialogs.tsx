import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import {
	ApiError,
	cancelJob,
	getSetup,
  mutateProgram,
  newIdempotencyKey,
  permanentDelete,
  setupAction,
  updateSetup,
  type UploadOptions,
	type UploadJobHandle,
	waitForJob,
} from '../api'
import type { Artifact, Job, Setup } from '../domain'
import { errorMessage, formatBytes, isNetworkError, isRevisionConflict } from '../ui'
import { Modal } from './Modal'

interface ErrorNoticeProps {
  error: unknown
  onReload?: () => Promise<void>
  onReloaded?: () => void
}

export function OperationError({ error, onReload, onReloaded }: ErrorNoticeProps) {
  const [reloading, setReloading] = useState(false)
  const conflict = isRevisionConflict(error)
  return (
    <div className={`form-error${conflict ? ' conflict-notice' : ''}`} role="alert">
      <strong>{conflict ? 'Карточка уже изменилась' : 'Операция не завершена'}</strong>
      <p>{errorMessage(error)}{conflict ? ' Введённые данные сохранены.' : ''}</p>
      {conflict && onReload ? (
        <button
          className="button button--quiet"
          type="button"
          disabled={reloading}
          onClick={() => {
            setReloading(true)
            void onReload().then(onReloaded).finally(() => setReloading(false))
          }}
        >
          {reloading ? 'Обновляем…' : 'Загрузить актуальную revision'}
        </button>
      ) : null}
    </div>
  )
}

interface MetadataProps {
  setup: Setup
  onChanged: (setup: Setup) => void
  onReload: () => Promise<void>
}

export function MetadataEditor({ setup, onChanged, onReload }: MetadataProps) {
  const [editing, setEditing] = useState(false)
  const [name, setName] = useState(setup.name)
  const [description, setDescription] = useState(setup.description ?? '')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  const [key, setKey] = useState(newIdempotencyKey)

  useEffect(() => {
    if (!editing) {
      setName(setup.name)
      setDescription(setup.description ?? '')
    }
  }, [editing, setup.description, setup.name, setup.revision])

  const reset = () => {
    setName(setup.name)
    setDescription(setup.description ?? '')
    setError(undefined)
    setEditing(false)
    setKey(newIdempotencyKey())
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setPending(true)
    setError(undefined)
    try {
      const changed = await updateSetup(setup.setupId, setup.revision, name, description, key)
      setEditing(false)
      setKey(newIdempotencyKey())
      onChanged(changed)
    } catch (reason) {
      setError(reason)
      if (!isNetworkError(reason)) setKey(newIdempotencyKey())
    } finally {
      setPending(false)
    }
  }

  if (!editing) {
    return (
      <section className="detail-section metadata-section" aria-labelledby="metadata-title">
        <div className="section-heading">
          <div><p className="eyebrow">Карточка</p><h2 id="metadata-title">Описание</h2></div>
          {setup.status !== 'archived' ? <button className="button button--quiet" type="button" onClick={() => setEditing(true)}>Изменить метаданные</button> : null}
        </div>
        <p className={setup.description ? '' : 'muted-copy'}>{setup.description || 'Описание не добавлено.'}</p>
      </section>
    )
  }

  return (
    <section className="detail-section metadata-section" aria-labelledby="metadata-edit-title">
      <div className="section-heading"><div><p className="eyebrow">Карточка</p><h2 id="metadata-edit-title">Изменение метаданных</h2></div></div>
      <form className="stack-form" onSubmit={(event) => void submit(event)}>
        <label><span>Название</span><input value={name} required maxLength={200} onChange={(event) => setName(event.target.value)} /></label>
        <label><span>Описание</span><textarea rows={4} value={description} onChange={(event) => setDescription(event.target.value)} /></label>
        {error ? <OperationError error={error} onReload={onReload} onReloaded={() => { setError(undefined); setKey(newIdempotencyKey()) }} /> : null}
        <div className="form-actions">
          <button className="button button--quiet" type="button" onClick={reset} disabled={pending}>Отмена</button>
          <button className="button button--primary" type="submit" disabled={pending || name.trim() === ''}>{pending ? 'Сохраняем…' : 'Сохранить revision'}</button>
        </div>
      </form>
    </section>
  )
}

interface ConfirmProps {
  title: string
  description: string
  confirmLabel: string
  children?: ReactNode
  danger?: boolean
  onClose: () => void
  onConfirm: (key: string) => Promise<void>
  onReload?: () => Promise<void>
}

export function ConfirmOperationDialog({
  title,
  description,
  confirmLabel,
  children,
  danger,
  onClose,
  onConfirm,
  onReload,
}: ConfirmProps) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  const [key, setKey] = useState(newIdempotencyKey)
  const confirm = async () => {
    setPending(true)
    setError(undefined)
    try {
      await onConfirm(key)
      onClose()
    } catch (reason) {
      setError(reason)
      if (!isNetworkError(reason)) setKey(newIdempotencyKey())
    } finally {
      setPending(false)
    }
  }
  return (
    <Modal
      title={title}
      description={description}
      onClose={onClose}
      closeDisabled={pending}
      footer={(
        <>
          <button className="button button--quiet" type="button" onClick={onClose} disabled={pending}>Отмена</button>
          <button className={`button ${danger ? 'button--danger' : 'button--primary'}`} type="button" onClick={() => void confirm()} disabled={pending}>
            {pending ? 'Выполняем…' : confirmLabel}
          </button>
        </>
      )}
    >
      {children}
      {error ? <OperationError error={error} onReload={onReload} onReloaded={() => { setError(undefined); setKey(newIdempotencyKey()) }} /> : null}
    </Modal>
  )
}

interface RenameProps {
  setup: Setup
  artifact: Artifact
  onClose: () => void
  onChanged: (setup: Setup) => void
  onReload: () => Promise<void>
}

export function RenameProgramDialog({ setup, artifact, onClose, onChanged, onReload }: RenameProps) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState(artifact.displayName)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  const [key, setKey] = useState(newIdempotencyKey)
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setPending(true)
    setError(undefined)
    try {
      const changed = await mutateProgram(setup, artifact, { displayName: name }, key)
      onChanged(changed)
      onClose()
    } catch (reason) {
      setError(reason)
      if (!isNetworkError(reason)) setKey(newIdempotencyKey())
    } finally {
      setPending(false)
    }
  }
  return (
    <Modal
      title="Переименовать программу"
      description={`${setup.name} · revision ${setup.revision} · содержимое и artifact ID не меняются.`}
      onClose={onClose}
      closeDisabled={pending}
      initialFocusRef={inputRef}
      footer={(
        <>
          <button className="button button--quiet" type="button" onClick={onClose} disabled={pending}>Отмена</button>
          <button className="button button--primary" type="submit" form="rename-program-form" disabled={pending || name.trim() === ''}>Переименовать</button>
        </>
      )}
    >
      <form id="rename-program-form" className="stack-form" onSubmit={(event) => void submit(event)}>
        <label><span>Новое basename</span><input ref={inputRef} value={name} onChange={(event) => setName(event.target.value)} required /></label>
        {error ? <OperationError error={error} onReload={onReload} onReloaded={() => { setError(undefined); setKey(newIdempotencyKey()) }} /> : null}
      </form>
    </Modal>
  )
}

interface FileProps {
  title: string
  description: string
  setup: Setup
  file: File
  artifact?: Artifact
  onClose: () => void
  onConfirm: (key: string, options: UploadOptions) => Promise<UploadJobHandle>
  onChanged: (setup: Setup) => void
  onReload: () => Promise<void>
}

export function FileOperationDialog({
  title,
  description,
  setup,
  file,
  artifact,
  onClose,
  onConfirm,
  onChanged,
  onReload,
}: FileProps) {
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  const [key, setKey] = useState(newIdempotencyKey)
  const [progress, setProgress] = useState({ loaded: 0, total: file.size })
  const [job, setJob] = useState<Job>()
  const controller = useRef<AbortController>()
  const confirm = async () => {
    setPending(true)
    setError(undefined)
    setProgress({ loaded: 0, total: file.size })
    const active = new AbortController()
    controller.current = active
    try {
      const handle = await onConfirm(key, {
        signal: active.signal,
        onProgress: (loaded, total) => setProgress({ loaded, total }),
      })
		setJob(handle.job)
		void waitForJob(handle.job, active.signal).then(setJob, () => undefined)
		const terminal = await handle.transfer
		setJob(terminal)
		if (terminal.state !== 'succeeded') throw new ApiError({
			message: `Upload job завершён: ${terminal.errorCode ?? terminal.state}.`,
			status: terminal.state === 'conflict' ? 409 : 422,
			code: terminal.errorCode ?? terminal.state.toUpperCase(),
		})
		const changed = await getSetup(setup.setupId)
      onChanged(changed)
      onClose()
    } catch (reason) {
      setError(reason instanceof DOMException && reason.name === 'AbortError'
        ? new Error('Загрузка отменена. Предыдущий объект не изменён.')
        : reason)
      if (!isNetworkError(reason)) setKey(newIdempotencyKey())
    } finally {
      controller.current = undefined
      setPending(false)
    }
  }
	const cancelUpload = async () => {
		if (job && !['succeeded', 'failed', 'cancelled', 'conflict'].includes(job.state)) {
			try { setJob(await cancelJob(job.jobId)) } catch { /* abort remains the fallback */ }
		}
		controller.current?.abort(new DOMException('Upload cancelled', 'AbortError'))
	}
  return (
    <Modal
      title={title}
      description={description}
      onClose={onClose}
      closeDisabled={pending}
      footer={(
        <>
          <button className="button button--quiet" type="button" onClick={pending ? () => void cancelUpload() : onClose}>{pending ? 'Отменить job' : 'Отмена'}</button>
          <button className="button button--primary" type="button" onClick={() => void confirm()} disabled={pending}>{pending ? 'Передаём…' : 'Подтвердить'}</button>
        </>
      )}
    >
      <dl className="operation-summary">
        <div><dt>Сетап</dt><dd>{setup.name}</dd></div>
        <div><dt>Revision</dt><dd>{setup.revision}</dd></div>
        {artifact ? <div><dt>Заменяется</dt><dd>{artifact.displayName} · версия {artifact.version.slice(0, 12)}…</dd></div> : null}
        <div><dt>Новый файл</dt><dd>{file.name} · {formatBytes(file.size)}</dd></div>
      </dl>
      <p className="form-hint">Предыдущий объект останется видимым до полного успешного завершения операции.</p>
      {pending ? <div className="import-progress" role="status"><span>Передаём {file.name} · {job ? `Job ${job.jobId.slice(0, 8)} · ${job.state}` : 'создаём job…'}</span><progress max={Math.max(job?.progress.totalBytes ?? 0, progress.total, 1)} value={Math.max(job?.progress.completedBytes ?? 0, progress.loaded)} /><small>{formatBytes(Math.max(job?.progress.completedBytes ?? 0, progress.loaded))} из {formatBytes(Math.max(job?.progress.totalBytes ?? 0, progress.total))}</small></div> : null}
      {error ? <OperationError error={error} onReload={onReload} onReloaded={() => { setError(undefined); setKey(newIdempotencyKey()) }} /> : null}
    </Modal>
  )
}

interface DuplicateProps {
  setup: Setup
  onClose: () => void
  onStart: (name: string, key: string) => Promise<void>
  onReload: () => Promise<void>
}

export function DuplicateSetupDialog({ setup, onClose, onStart, onReload }: DuplicateProps) {
  const inputRef = useRef<HTMLInputElement>(null)
  const [name, setName] = useState(`${setup.name} — копия`)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  const [key, setKey] = useState(newIdempotencyKey)
  const submit = async (event: FormEvent) => {
    event.preventDefault()
    setPending(true)
    setError(undefined)
    try {
      await onStart(name, key)
      onClose()
    } catch (reason) {
      setError(reason)
      if (!isNetworkError(reason)) setKey(newIdempotencyKey())
    } finally {
      setPending(false)
    }
  }
  return (
    <Modal
      title="Дублировать сетап"
      description={`Будет создан новый draft с новыми setup_id и artifact_id на основе revision ${setup.revision}.`}
      onClose={onClose}
      closeDisabled={pending}
      initialFocusRef={inputRef}
      footer={(
        <>
          <button className="button button--quiet" type="button" onClick={onClose} disabled={pending}>Отмена</button>
          <button className="button button--primary" type="submit" form="duplicate-setup-form" disabled={pending || name.trim() === ''}>Запустить дублирование</button>
        </>
      )}
    >
      <form id="duplicate-setup-form" className="stack-form" onSubmit={(event) => void submit(event)}>
        <label><span>Название копии</span><input ref={inputRef} value={name} required onChange={(event) => setName(event.target.value)} /></label>
        {error ? <OperationError error={error} onReload={onReload} onReloaded={() => { setError(undefined); setKey(newIdempotencyKey()) }} /> : null}
      </form>
    </Modal>
  )
}

interface DeletePlan {
  confirmationToken: string
  setupId: string
  revision: number
  exactName: string
  programCount: number
  hasSetupSheet: boolean
  uniqueBytes: number
  expiresAt: string
}

function asDeletePlan(value: unknown): DeletePlan {
  if (typeof value !== 'object' || value === null) throw new Error('Backend не вернул план удаления.')
  const item = value as Record<string, unknown>
  if (
    typeof item.confirmationToken !== 'string'
    || typeof item.exactName !== 'string'
    || typeof item.programCount !== 'number'
    || typeof item.uniqueBytes !== 'number'
  ) throw new Error('План удаления повреждён.')
  return {
    confirmationToken: item.confirmationToken,
    setupId: String(item.setupId),
    revision: Number(item.revision),
    exactName: item.exactName,
    programCount: item.programCount,
    hasSetupSheet: item.hasSetupSheet === true,
    uniqueBytes: item.uniqueBytes,
    expiresAt: String(item.expiresAt),
  }
}

interface PermanentDeleteProps {
  setup: Setup
  onClose: () => void
  onDeleted: () => void
  onReload: () => Promise<void>
}

export function PermanentDeleteDialog({ setup, onClose, onDeleted, onReload }: PermanentDeleteProps) {
  const [plan, setPlan] = useState<DeletePlan>()
  const [exactName, setExactName] = useState('')
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  const [key, setKey] = useState(newIdempotencyKey)

  const loadPlan = async () => {
    setPending(true)
    setError(undefined)
    try {
      setPlan(asDeletePlan(await setupAction(setup.setupId, 'delete-plan', setup.revision)))
    } catch (reason) {
      setError(reason)
    } finally {
      setPending(false)
    }
  }

  useEffect(() => {
    void loadPlan()
    // A fresh dialog owns one plan tied to the revision passed on mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  const remove = async () => {
    if (!plan) return
    setPending(true)
    setError(undefined)
    try {
      await permanentDelete(setup, exactName, plan.confirmationToken, key)
      onDeleted()
    } catch (reason) {
      setError(reason)
      if (!isNetworkError(reason)) setKey(newIdempotencyKey())
    } finally {
      setPending(false)
    }
  }

  return (
    <Modal
      title="Удалить сетап окончательно"
      description="Это отдельное необратимое действие доступно только для архивного сетапа."
      onClose={onClose}
      closeDisabled={pending}
      footer={(
        <>
          <button className="button button--quiet" type="button" onClick={onClose} disabled={pending}>Отмена</button>
          <button
            className="button button--danger"
            type="button"
            disabled={pending || !plan || exactName !== plan.exactName}
            onClick={() => void remove()}
          >
            {pending ? 'Выполняем…' : 'Удалить окончательно'}
          </button>
        </>
      )}
    >
      {pending && !plan ? <p role="status">Готовим точный план удаления…</p> : null}
      {plan ? (
        <>
          <dl className="operation-summary">
            <div><dt>Программы</dt><dd>{plan.programCount}</dd></div>
            <div><dt>Setup Sheet</dt><dd>{plan.hasSetupSheet ? 'Есть' : 'Нет'}</dd></div>
            <div><dt>Уникальный объём</dt><dd>{formatBytes(plan.uniqueBytes)}</dd></div>
          </dl>
          <label className="danger-confirmation">
            <span>Введите точное название: <strong>{plan.exactName}</strong></span>
            <input value={exactName} onChange={(event) => setExactName(event.target.value)} autoComplete="off" />
          </label>
        </>
      ) : null}
      {error ? (
        <OperationError
          error={error}
          onReload={onReload}
          onReloaded={() => { setError(undefined); setPlan(undefined); setKey(newIdempotencyKey()); void loadPlan() }}
        />
      ) : null}
    </Modal>
  )
}
