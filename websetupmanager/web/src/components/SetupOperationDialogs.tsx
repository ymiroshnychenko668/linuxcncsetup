import { useEffect, useRef, useState, type FormEvent, type ReactNode } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import CircularProgress from '@mui/material/CircularProgress'
import TextField from '@mui/material/TextField'
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
    <Alert
      className={`form-error${conflict ? ' conflict-notice' : ''}`}
      severity={conflict ? 'warning' : 'error'}
      variant="standard"
      icon={false}
      role="alert"
      sx={{ '& .MuiAlert-message': { width: '100%', padding: 0 } }}
    >
      <strong>{conflict ? 'Карточка уже изменилась' : 'Операция не завершена'}</strong>
      <p>{errorMessage(error)}{conflict ? ' Введённые данные сохранены.' : ''}</p>
      {conflict && onReload ? (
        <Button
          component="button"
          className="button button--quiet"
          type="button"
          disabled={reloading}
          onClick={() => {
            setReloading(true)
            void onReload().then(onReloaded).finally(() => setReloading(false))
          }}
          startIcon={reloading ? <CircularProgress color="inherit" size="1rem" aria-hidden="true" /> : undefined}
        >
          {reloading ? 'Обновляем…' : 'Загрузить актуальную revision'}
        </Button>
      ) : null}
    </Alert>
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
          {setup.status !== 'archived' ? <Button component="button" className="button button--quiet" type="button" onClick={() => setEditing(true)}>Изменить метаданные</Button> : null}
        </div>
        <p className={setup.description ? '' : 'muted-copy'}>{setup.description || 'Описание не добавлено.'}</p>
      </section>
    )
  }

  return (
    <section className="detail-section metadata-section" aria-labelledby="metadata-edit-title">
      <div className="section-heading"><div><p className="eyebrow">Карточка</p><h2 id="metadata-edit-title">Изменение метаданных</h2></div></div>
      <form className="stack-form" onSubmit={(event) => void submit(event)}>
        <TextField label="Название" value={name} required fullWidth slotProps={{ htmlInput: { maxLength: 200 } }} onChange={(event) => setName(event.target.value)} />
        <TextField label="Описание" value={description} fullWidth multiline rows={4} onChange={(event) => setDescription(event.target.value)} />
        {error ? <OperationError error={error} onReload={onReload} onReloaded={() => { setError(undefined); setKey(newIdempotencyKey()) }} /> : null}
        <div className="form-actions">
          <Button component="button" className="button button--quiet" type="button" onClick={reset} disabled={pending}>Отмена</Button>
          <Button component="button" className="button button--primary" type="submit" disabled={pending || name.trim() === ''} startIcon={pending ? <CircularProgress color="inherit" size="1rem" aria-hidden="true" /> : undefined}>{pending ? 'Сохраняем…' : 'Сохранить revision'}</Button>
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
          <Button component="button" className="button button--quiet" type="button" onClick={onClose} disabled={pending}>Отмена</Button>
          <Button component="button" className={`button ${danger ? 'button--danger' : 'button--primary'}`} type="button" onClick={() => void confirm()} disabled={pending} startIcon={pending ? <CircularProgress color="inherit" size="1rem" aria-hidden="true" /> : undefined}>
            {pending ? 'Выполняем…' : confirmLabel}
          </Button>
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
          <Button component="button" className="button button--quiet" type="button" onClick={onClose} disabled={pending}>Отмена</Button>
          <Button component="button" className="button button--primary" type="submit" form="rename-program-form" disabled={pending || name.trim() === ''} startIcon={pending ? <CircularProgress color="inherit" size="1rem" aria-hidden="true" /> : undefined}>Переименовать</Button>
        </>
      )}
    >
      <form id="rename-program-form" className="stack-form" onSubmit={(event) => void submit(event)}>
        <TextField label="Новое basename" inputRef={inputRef} value={name} onChange={(event) => setName(event.target.value)} required fullWidth />
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
          <Button component="button" className="button button--quiet" type="button" onClick={pending ? () => void cancelUpload() : onClose}>{pending ? 'Отменить job' : 'Отмена'}</Button>
          <Button component="button" className="button button--primary" type="button" onClick={() => void confirm()} disabled={pending} startIcon={pending ? <CircularProgress color="inherit" size="1rem" aria-hidden="true" /> : undefined}>{pending ? 'Передаём…' : 'Подтвердить'}</Button>
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
          <Button component="button" className="button button--quiet" type="button" onClick={onClose} disabled={pending}>Отмена</Button>
          <Button component="button" className="button button--primary" type="submit" form="duplicate-setup-form" disabled={pending || name.trim() === ''} startIcon={pending ? <CircularProgress color="inherit" size="1rem" aria-hidden="true" /> : undefined}>Запустить дублирование</Button>
        </>
      )}
    >
      <form id="duplicate-setup-form" className="stack-form" onSubmit={(event) => void submit(event)}>
        <TextField label="Название копии" inputRef={inputRef} value={name} required fullWidth onChange={(event) => setName(event.target.value)} />
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
          <Button component="button" className="button button--quiet" type="button" onClick={onClose} disabled={pending}>Отмена</Button>
          <Button
            component="button"
            className="button button--danger"
            type="button"
            disabled={pending || !plan || exactName !== plan.exactName}
            onClick={() => void remove()}
            startIcon={pending ? <CircularProgress color="inherit" size="1rem" aria-hidden="true" /> : undefined}
          >
            {pending ? 'Выполняем…' : 'Удалить окончательно'}
          </Button>
        </>
      )}
    >
      {pending && !plan ? <div role="status"><CircularProgress color="inherit" size="1rem" aria-hidden="true" /> <span>Готовим точный план удаления…</span></div> : null}
      {plan ? (
        <>
          <dl className="operation-summary">
            <div><dt>Программы</dt><dd>{plan.programCount}</dd></div>
            <div><dt>Setup Sheet</dt><dd>{plan.hasSetupSheet ? 'Есть' : 'Нет'}</dd></div>
            <div><dt>Уникальный объём</dt><dd>{formatBytes(plan.uniqueBytes)}</dd></div>
          </dl>
          <TextField
            className="danger-confirmation"
            label={<>Введите точное название: <strong>{plan.exactName}</strong></>}
            value={exactName}
            onChange={(event) => setExactName(event.target.value)}
            autoComplete="off"
            fullWidth
          />
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
