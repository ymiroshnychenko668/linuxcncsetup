import { useMemo, useRef, useState } from 'react'
import { ApiError, cancelJob, deleteArtifact, getSetup, newIdempotencyKey, uploadPrograms, waitForJob } from '../api'
import type { Artifact, Job, Setup } from '../domain'
import { formatBytes, isNetworkError } from '../ui'
import { Modal } from './Modal'
import { OperationError } from './SetupOperationDialogs'

interface ProgramFile {
  file: File
  displayName: string
}

function nameKey(value: string): string {
  return value.normalize('NFC').toLocaleLowerCase('en-US')
}

export function MultiProgramUploadDialog({
  setup,
  files,
  onClose,
  onChanged,
  onReload,
}: {
  setup: Setup
  files: File[]
  onClose: () => void
  onChanged: (setup: Setup) => void
  onReload: () => Promise<void>
}) {
  const [programs, setPrograms] = useState<ProgramFile[]>(() => files.map((file) => ({ file, displayName: file.name })))
  const [pending, setPending] = useState(false)
  const [progress, setProgress] = useState({ loaded: 0, total: files.reduce((sum, file) => sum + file.size, 0) })
  const [error, setError] = useState<unknown>()
  const [job, setJob] = useState<Job>()
  const [key, setKey] = useState(newIdempotencyKey)
  const controller = useRef<AbortController>()
  const duplicates = useMemo(() => {
    const seen = new Set<string>()
    const repeated = new Set<string>()
    for (const program of programs) {
      const normalized = nameKey(program.displayName)
      if (seen.has(normalized)) repeated.add(normalized)
      seen.add(normalized)
    }
    return repeated
  }, [programs])
  const invalid = programs.some((item) => item.displayName === '' || item.displayName.includes('/') || item.displayName.includes('\\'))

  const upload = async () => {
    const active = new AbortController()
    controller.current = active
    setPending(true)
    setError(undefined)
    try {
      const handle = await uploadPrograms(setup, programs, key, {
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
        ? new Error('Загрузка отменена. Ни одна новая программа не опубликована.') : reason)
      if (!isNetworkError(reason)) setKey(newIdempotencyKey())
    } finally {
      controller.current = undefined
      setPending(false)
    }
  }

	const cancelUpload = async () => {
		if (job && !['succeeded', 'failed', 'cancelled', 'conflict'].includes(job.state)) {
			try { setJob(await cancelJob(job.jobId)) } catch { /* request abort still cooperatively stops the reader */ }
		}
		controller.current?.abort(new DOMException('Upload cancelled', 'AbortError'))
	}

  return <Modal
    title={`Добавить программы: ${programs.length}`}
    description={`Все файлы публикуются одной атомарной операцией в revision ${setup.revision}. Browser filename не используется без подтверждения.`}
    onClose={onClose} closeDisabled={pending}
    footer={<><button className="button button--quiet" type="button" onClick={pending ? () => void cancelUpload() : onClose}>{pending ? 'Отменить job' : 'Отмена'}</button><button className="button button--primary" type="button" disabled={pending || invalid || duplicates.size > 0} onClick={() => void upload()}>{pending ? 'Передаём…' : 'Добавить атомарно'}</button></>}
  >
    <div className="stack-form">
      {programs.map((program, index) => <label key={`${program.file.name}-${program.file.lastModified}-${index}`}><span>Basename · {formatBytes(program.file.size)}</span><input aria-label={`Basename ${program.file.name}`} value={program.displayName} disabled={pending} onChange={(event) => setPrograms((current) => current.map((item, itemIndex) => itemIndex === index ? { ...item, displayName: event.target.value } : item))} />{duplicates.has(nameKey(program.displayName)) ? <small className="field-error">Совпадает с другой программой без учёта регистра/Unicode.</small> : null}</label>)}
    </div>
    {pending ? <div className="import-progress" role="status"><span>Передаём комплект программ… {job ? `Job ${job.jobId.slice(0, 8)} · ${job.state}` : 'создаём job…'}</span><progress max={Math.max(job?.progress.totalBytes ?? 0, progress.total, 1)} value={Math.max(job?.progress.completedBytes ?? 0, progress.loaded)} /><small>{formatBytes(Math.max(job?.progress.completedBytes ?? 0, progress.loaded))} из {formatBytes(Math.max(job?.progress.totalBytes ?? 0, progress.total))} · {job?.progress.completedItems ?? 0} из {job?.progress.totalItems ?? programs.length} файлов</small></div> : null}
    {error ? <OperationError error={error} onReload={onReload} onReloaded={() => { setError(undefined); setKey(newIdempotencyKey()) }} /> : null}
  </Modal>
}

export function DeleteProgramDialog({
  setup,
  artifact,
  onClose,
  onChanged,
  onReload,
}: {
  setup: Setup
  artifact: Artifact
  onClose: () => void
  onChanged: (setup: Setup) => void
  onReload: () => Promise<void>
}) {
  const alternatives = setup.artifacts.filter((item) => item.role === 'program' && item.artifactId !== artifact.artifactId)
  const last = alternatives.length === 0
  const [primaryChoice, setPrimaryChoice] = useState('')
  const [confirmLast, setConfirmLast] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState<unknown>()
  const [key, setKey] = useState(newIdempotencyKey)
  const allowed = (!artifact.primary || last || primaryChoice !== '') && (!last || confirmLast)

  const remove = async () => {
    setPending(true)
    setError(undefined)
    try {
      const changed = await deleteArtifact(setup, artifact, key, {
        replacementPrimaryArtifactId: artifact.primary && primaryChoice !== '__none' ? primaryChoice : undefined,
        leavePrimaryUnassigned: artifact.primary && primaryChoice === '__none',
        confirmDeleteLastProgram: last,
      })
      onChanged(changed)
      onClose()
    } catch (reason) {
      setError(reason)
      if (!isNetworkError(reason)) setKey(newIdempotencyKey())
    } finally {
      setPending(false)
    }
  }

  return <Modal
    title="Удалить программу из сетапа"
    description={`${artifact.displayName} будет удалена из revision ${setup.revision}. Управляемый объект удалится сборщиком мусора только когда ссылок не останется.`}
    onClose={onClose} closeDisabled={pending}
    footer={<><button className="button button--quiet" type="button" disabled={pending} onClick={onClose}>Отмена</button><button className="button button--danger" type="button" disabled={pending || !allowed} onClick={() => void remove()}>{pending ? 'Удаляем…' : 'Удалить программу'}</button></>}
  >
    {artifact.primary && !last ? <label className="stack-field"><span>После удаления основной программы</span><select value={primaryChoice} onChange={(event) => setPrimaryChoice(event.target.value)}><option value="">Выберите явно…</option>{alternatives.map((item) => <option key={item.artifactId} value={item.artifactId}>Назначить {item.displayName}</option>)}<option value="__none">Оставить без основной программы</option></select></label> : null}
    {last ? <label className="danger-check"><input type="checkbox" checked={confirmLast} onChange={(event) => setConfirmLast(event.target.checked)} /> Подтверждаю удаление последней G-code-программы. Сетап станет неготовым.</label> : null}
    {error ? <OperationError error={error} onReload={onReload} onReloaded={() => { setError(undefined); setKey(newIdempotencyKey()) }} /> : null}
  </Modal>
}
