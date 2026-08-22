import { useEffect, useMemo, useRef, useState } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import Checkbox from '@mui/material/Checkbox'
import FormControlLabel from '@mui/material/FormControlLabel'
import TextField from '@mui/material/TextField'
import {
	cancelJob,
  cancelImport,
  commitImport,
  excludeImportArtifact,
  getImport,
	getJob,
  newIdempotencyKey,
	preflightImport,
  startImport,
  uploadImportArtifact,
  type Capabilities,
	type ImportPreflightResult,
} from '../api'
import type { ImportArtifact, ImportSession, Job, Setup } from '../domain'
import { errorMessage, formatBytes, isNetworkError } from '../ui'
import { Modal } from './Modal'

interface Props {
  capabilities: Capabilities
  onClose: () => void
  onImported: (setup: Setup) => void
}

interface SelectedFile {
  id: string
  file: File
  basename: string
  included: boolean
  role: 'program' | 'setup_sheet'
  uploaded?: ImportArtifact
  uploadKey: string
}

interface UploadProgress {
  label: string
  loaded: number
  total: number
}

function proposedRole(file: File, extensions: string[]): SelectedFile['role'] {
  const lower = file.name.toLocaleLowerCase()
  if (lower.endsWith('.pdf') || lower.endsWith('.html') || lower.endsWith('.htm')) return 'setup_sheet'
  if (extensions.some((extension) => lower.endsWith(extension.toLocaleLowerCase()))) return 'program'
  return 'program'
}

function sameConfirmedName(left: string, right: string): boolean {
  return left.normalize('NFC') === right.normalize('NFC')
}

function validBasename(value: string): boolean {
  return value.trim() === value && value !== '' && value !== '.' && value !== '..'
    && !value.includes('/') && !value.includes('\\') && !/\p{Cc}/u.test(value)
}

export function ImportWizard({ capabilities, onClose, onImported }: Props) {
  const pickerRef = useRef<HTMLInputElement>(null)
  const controllerRef = useRef<AbortController>()
  const sessionRef = useRef<ImportSession>()
  const cancelRequested = useRef(false)
  const closeIssued = useRef(false)
  const startKey = useRef(newIdempotencyKey())
  const commitKey = useRef(newIdempotencyKey())
  const partialCommitKey = useRef(newIdempotencyKey())
  const cancelKey = useRef(newIdempotencyKey())
  const [step, setStep] = useState<1 | 2>(1)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [files, setFiles] = useState<SelectedFile[]>([])
  const [primaryID, setPrimaryID] = useState<string>()
  const [session, setSession] = useState<ImportSession>()
  const [pending, setPending] = useState(false)
  const [cancelling, setCancelling] = useState(false)
  const [failed, setFailed] = useState(false)
  const [progress, setProgress] = useState<UploadProgress>()
  const [error, setError] = useState<string>()
  const [job, setJob] = useState<Job>()
	const [preflight, setPreflight] = useState<ImportPreflightResult>()
	const [preflightFresh, setPreflightFresh] = useState(false)
	const [preflightPending, setPreflightPending] = useState(false)
  const included = useMemo(() => files.filter((item) => item.included), [files])
  const programs = included.filter((item) => item.role === 'program')
  const sheetCount = included.filter((item) => item.role === 'setup_sheet').length
  const invalidNames = included.filter((item) => !validBasename(item.basename))
	const includedIDs = useMemo(() => new Set(included.map((item) => item.id)), [included])
	const activeCollisionGroups = useMemo(() => (preflight?.collisions ?? [])
		.map((group) => group.clientIds.filter((id) => includedIDs.has(id)))
		.filter((ids) => ids.length > 1), [includedIDs, preflight])
	const conflictIDs = useMemo(() => new Set(activeCollisionGroups.flat()), [activeCollisionGroups])
	const preflightErrors = useMemo(() => new Map((preflight?.items ?? []).filter((item) => item.errorCode).map((item) => [item.clientId, item.errorCode!])), [preflight])
	const conflicts = included.filter((item) => conflictIDs.has(item.id))
  const staged = files.filter((item) => item.included && item.uploaded?.state === 'staged')
  const stagedPrograms = staged.filter((item) => item.role === 'program')
  const canPublish = preflightFresh && sheetCount <= 1 && programs.length > 0 && invalidNames.length === 0 && conflicts.length === 0 && preflightErrors.size === 0

	useEffect(() => {
		if (!session?.jobId || session.state !== 'staging') return
		let stopped = false
		const poll = async () => {
			try {
				const snapshot = await getJob(session.jobId!)
				if (!stopped) setJob(snapshot)
			} catch { /* session polling/recovery remains authoritative */ }
		}
		void poll()
		const timer = window.setInterval(() => void poll(), 350)
		return () => { stopped = true; window.clearInterval(timer) }
	}, [session?.jobId, session?.state])

  const choose = (selected: FileList | null) => {
    if (!selected) return
    const chosen = Array.from(selected, (file, index) => ({
      id: `${file.name}-${file.size}-${file.lastModified}-${index}`,
      file,
      basename: file.name,
      included: true,
      role: proposedRole(file, capabilities.gcodeExtensions),
      uploadKey: newIdempotencyKey(),
    }))
    setFiles(chosen)
		setPreflight(undefined)
		setPreflightFresh(false)
    const firstProgram = chosen.find((item) => item.role === 'program')
    setPrimaryID(firstProgram?.id)
  }

	const updateFiles = (updater: (current: SelectedFile[]) => SelectedFile[]) => {
		setFiles(updater)
		setPreflightFresh(false)
	}

	const checkNames = async (advance = false): Promise<ImportPreflightResult | undefined> => {
		const selected = files.filter((item) => item.included)
		if (selected.length === 0) return undefined
		setPreflightPending(true)
		setError(undefined)
		try {
			const checked = await preflightImport(selected.map((item) => ({ clientId: item.id, role: item.role, displayName: item.basename })))
			setPreflight(checked)
			setPreflightFresh(true)
			const canonical = new Map(checked.items.filter((item) => item.displayName).map((item) => [item.clientId, item.displayName!]))
			setFiles((current) => current.map((item) => canonical.has(item.id) ? { ...item, basename: canonical.get(item.id)! } : item))
			if (advance) setStep(2)
			return checked
		} catch (reason) {
			setError(errorMessage(reason))
			return undefined
		} finally {
			setPreflightPending(false)
		}
	}

  const finishClose = () => {
    if (closeIssued.current) return
    closeIssued.current = true
    onClose()
  }

  const cancelAndClose = async () => {
    cancelRequested.current = true
    controllerRef.current?.abort(new DOMException('Import cancelled', 'AbortError'))
    const active = sessionRef.current
    if (!active && pending) {
      setCancelling(true)
      setProgress({ label: 'Ожидаем staging-сессию, чтобы безопасно отменить её…', loaded: 0, total: 0 })
      return
    }
    setCancelling(true)
    if (active && !['succeeded', 'cancelled'].includes(active.state)) {
		try {
			if (active.jobId) setJob(await cancelJob(active.jobId, cancelKey.current))
			else await cancelImport(active.importSessionId, cancelKey.current)
		} catch { /* restart cleanup reclaims staging */ }
    }
    finishClose()
  }

  const applySnapshot = (snapshot: ImportSession) => {
    setSession(snapshot)
    sessionRef.current = snapshot
    setFiles((current) => current.map((item) => {
      if (item.uploaded) return item
      const remote = snapshot.artifacts.find((artifact) => artifact.state === 'staged'
		&& artifact.role === item.role && sameConfirmedName(artifact.displayName, item.basename))
      return remote ? { ...item, uploaded: remote } : item
    }))
  }

  const cleanFailedReservations = async (active: ImportSession) => {
    const snapshot = await getImport(active.importSessionId)
    applySnapshot(snapshot)
		const failedNames = snapshot.artifacts.filter((item) => item.state === 'failed').map((item) => ({ role: item.role, displayName: item.displayName }))
    for (const artifact of snapshot.artifacts.filter((item) => item.state === 'failed')) {
      const updated = await excludeImportArtifact(active.importSessionId, artifact.importArtifactId)
      setSession(updated)
      sessionRef.current = updated
    }
		if (failedNames.length > 0) setFiles((current) => current.map((item) => !item.uploaded && failedNames.some((failed) => failed.role === item.role && sameConfirmedName(failed.displayName, item.basename)) ? { ...item, uploadKey: newIdempotencyKey() } : item))
    return sessionRef.current ?? snapshot
  }

  const publish = async () => {
    if (pending || !canPublish) return
    setPending(true)
    setFailed(false)
    setError(undefined)
    cancelRequested.current = false
    try {
      let active = sessionRef.current ?? session
      let reconciled: ImportSession | undefined
      if (!active) {
        setProgress({ label: 'Создаём staging-сессию…', loaded: 0, total: included.reduce((sum, item) => sum + item.file.size, 0) })
        active = await startImport(name.trim(), description, startKey.current)
        setSession(active)
        sessionRef.current = active
      } else {
        reconciled = await cleanFailedReservations(active)
      }
      if (cancelRequested.current) {
        try { await cancelImport(active.importSessionId, cancelKey.current) } catch { /* restart recovery remains authoritative */ }
        finishClose()
        return
      }
      const controller = new AbortController()
      controllerRef.current = controller
      const uploaded: ImportArtifact[] = []
      const total = included.reduce((sum, item) => sum + item.file.size, 0)
      let completedBytes = 0
      let primaryArtifactID: string | undefined
      for (let index = 0; index < included.length; index += 1) {
        const selected = included[index]
        let artifact = selected.uploaded ?? reconciled?.artifacts.find((item) => item.state === 'staged'
		  && item.role === selected.role && sameConfirmedName(item.displayName, selected.basename))
        if (!artifact) {
          artifact = await uploadImportArtifact(
            active.importSessionId,
            selected.file,
            selected.role,
            selected.basename,
            reconciled ? newIdempotencyKey() : selected.uploadKey,
            {
              signal: controller.signal,
              onProgress: (loaded) => setProgress({
                label: `Загружаем ${index + 1} из ${included.length}: ${selected.basename}`,
                loaded: completedBytes + loaded,
                total,
              }),
            },
          )
          setFiles((current) => current.map((item) => item.id === selected.id ? { ...item, uploaded: artifact } : item))
        }
        uploaded.push(artifact)
        completedBytes += selected.file.size
        setProgress({ label: `Загружено ${index + 1} из ${included.length}`, loaded: completedBytes, total })
        if (selected.id === primaryID) primaryArtifactID = artifact.importArtifactId
      }
      setProgress({ label: 'Атомарно публикуем комплект…', loaded: total, total })
      const created = await commitImport(active.importSessionId, uploaded, primaryArtifactID, false, commitKey.current)
      setProgress({ label: 'Комплект опубликован.', loaded: total, total })
      onImported(created)
    } catch (reason) {
      if (cancelRequested.current || reason instanceof DOMException && reason.name === 'AbortError') return
      if (!sessionRef.current && !isNetworkError(reason)) startKey.current = newIdempotencyKey()
      if (sessionRef.current && !isNetworkError(reason)) commitKey.current = newIdempotencyKey()
      setFailed(true)
      setError(errorMessage(reason))
      setProgress((current) => ({ label: 'Загрузка остановлена. Staged-файлы не опубликованы.', loaded: current?.loaded ?? 0, total: current?.total ?? 0 }))
      const active = sessionRef.current
      if (active) void getImport(active.importSessionId).then(applySnapshot, () => undefined)
		void checkNames()
    } finally {
      controllerRef.current = undefined
      setPending(false)
    }
  }

  const savePartialDraft = async () => {
    const active = sessionRef.current
    if (!active || stagedPrograms.length === 0 || pending) return
    setPending(true)
    setError(undefined)
    try {
      await cleanFailedReservations(active)
      const stagedArtifacts = staged.flatMap((item) => item.uploaded ? [item.uploaded] : [])
      const selectedPrimary = staged.find((item) => item.id === primaryID)?.uploaded?.importArtifactId
        ?? stagedPrograms[0].uploaded?.importArtifactId
      const draft = await commitImport(active.importSessionId, stagedArtifacts, selectedPrimary, true, partialCommitKey.current)
      onImported(draft)
    } catch (reason) {
      setError(errorMessage(reason))
      if (!isNetworkError(reason)) partialCommitKey.current = newIdempotencyKey()
    } finally {
      setPending(false)
    }
  }

	const excludeFile = async (id: string) => {
		const selected = files.find((item) => item.id === id)
		if (selected?.uploaded && sessionRef.current) {
			setPending(true)
			try {
				const updated = await excludeImportArtifact(sessionRef.current.importSessionId, selected.uploaded.importArtifactId)
				setSession(updated)
				sessionRef.current = updated
			} catch (reason) {
				setError(errorMessage(reason))
				return
			} finally {
				setPending(false)
			}
		}
		updateFiles((current) => current.map((item) => item.id === id ? { ...item, included: false, uploaded: undefined } : item))
	}

	const keepOnlyCollision = async (id: string) => {
		const group = activeCollisionGroups.find((ids) => ids.includes(id)) ?? []
		for (const otherID of group) {
			if (otherID !== id) await excludeFile(otherID)
		}
	}

  return (
    <Modal
      title="Импорт комплекта"
      description={`Шаг ${step} из 2. Несколько файлов публикуются атомарно как один Setup.`}
      onClose={() => void cancelAndClose()}
      closeDisabled={cancelling}
      className="import-modal"
      footer={(
        <>
          {step === 2 ? <Button className="button button--quiet" variant="outlined" type="button" disabled={pending || Boolean(session)} onClick={() => setStep(1)}>Назад</Button> : null}
          <Button className="button button--quiet" variant="outlined" type="button" disabled={cancelling} onClick={() => void cancelAndClose()}>{cancelling ? 'Отменяем…' : pending ? 'Отменить загрузку' : 'Отмена'}</Button>
          {step === 1 ? (
				<Button className="button button--primary" variant="contained" type="button" disabled={preflightPending || name.trim() === '' || files.length === 0} onClick={() => void checkNames(true)}>{preflightPending ? 'Проверяем Unicode…' : 'Проверить роли'}</Button>
          ) : (
				preflightFresh ? <Button className="button button--primary" variant="contained" type="button" disabled={pending || !canPublish} onClick={() => void publish()}>{pending ? 'Импортируем…' : failed ? 'Повторить загрузку' : 'Опубликовать комплект'}</Button>
					: <Button className="button button--primary" variant="contained" type="button" disabled={pending || preflightPending} onClick={() => void checkNames()}>{preflightPending ? 'Проверяем Unicode…' : 'Проверить имена повторно'}</Button>
          )}
        </>
      )}
    >
      {step === 1 ? (
        <div className="stack-form">
          <label><span>Название сетапа</span><TextField value={name} fullWidth size="small" slotProps={{ htmlInput: { maxLength: 200 } }} onChange={(event) => setName(event.target.value)} required /></label>
          <label><span>Описание</span><TextField multiline rows={3} value={description} fullWidth size="small" onChange={(event) => setDescription(event.target.value)} /></label>
          <input ref={pickerRef} className="visually-hidden" type="file" tabIndex={-1} aria-hidden="true" multiple accept={[...capabilities.gcodeExtensions, '.pdf', '.html', '.htm'].join(',')} onChange={(event) => { choose(event.target.files); event.target.value = '' }} />
          <Button className="file-drop" variant="outlined" type="button" onClick={() => pickerRef.current?.click()}><strong>{files.length > 0 ? `Выбрано файлов: ${files.length}` : 'Выбрать несколько файлов'}</strong><span>G-code, PDF или автономная HTML Setup Sheet</span></Button>
        </div>
      ) : (
        <div>
          <div className="wizard-summary"><strong>{name}</strong><span>{description || 'Без описания'}</span></div>
          <ul className="import-file-list">
            {files.map((item) => {
				  const duplicate = item.included && conflictIDs.has(item.id)
              return <li key={item.id} className={duplicate ? 'import-file-list__conflict' : ''}>
				<FormControlLabel className="toggle" sx={{ margin: 0 }} control={<Checkbox size="small" sx={{ padding: '2px' }} checked={item.included} disabled={pending || Boolean(item.uploaded)} onChange={(event) => updateFiles((current) => current.map((entry) => entry.id === item.id ? { ...entry, included: event.target.checked } : entry))} />} label="Включить" />
				<span><label><small>Подтверждённое basename</small><TextField value={item.basename} fullWidth size="small" disabled={pending || Boolean(item.uploaded)} slotProps={{ htmlInput: { 'aria-label': `Basename ${item.file.name}` } }} onChange={(event) => updateFiles((current) => current.map((entry) => entry.id === item.id ? { ...entry, basename: event.target.value } : entry))} /></label><small>{formatBytes(item.file.size)}{item.uploaded ? ' · загружен в staging' : ''}</small></span>
				<select aria-label={`Роль ${item.file.name}`} value={item.role} disabled={pending || Boolean(item.uploaded)} onChange={(event) => updateFiles((current) => current.map((entry) => entry.id === item.id ? { ...entry, role: event.target.value as SelectedFile['role'] } : entry))}><option value="program">G-code программа</option><option value="setup_sheet">Общая Setup Sheet</option></select>
                {item.included && item.role === 'program' ? <label className="toggle"><input type="radio" name="primary-program" checked={primaryID === item.id} disabled={pending || Boolean(session)} onChange={() => setPrimaryID(item.id)} /> Основная</label> : null}
				{duplicate ? <div className="import-conflict-actions"><span>Backend обнаружил совпадение Unicode basename.</span><Button variant="outlined" type="button" disabled={pending} onClick={() => void keepOnlyCollision(item.id)}>Оставить только этот файл</Button><Button variant="outlined" type="button" disabled={pending} onClick={() => void excludeFile(item.id)}>Исключить</Button></div> : null}
				{item.included && item.role === 'setup_sheet' && sheetCount > 1 && !session ? <div className="import-conflict-actions"><span>В комплекте допустима одна Setup Sheet.</span><Button variant="outlined" type="button" onClick={() => updateFiles((current) => current.map((entry) => entry.role === 'setup_sheet' && entry.id !== item.id ? { ...entry, included: false } : entry))}>Оставить эту Setup Sheet</Button></div> : null}
              </li>
            })}
          </ul>
			  {conflicts.length > 0 ? <Alert className="form-error" severity="error" role="alert">Backend обнаружил совпадающие Unicode basename: переименуйте, оставьте один или исключите файл.</Alert> : null}
			  {preflightErrors.size > 0 ? <Alert className="form-error" severity="error" role="alert">Backend отклонил одно или несколько имён: {Array.from(preflightErrors.values()).join(', ')}.</Alert> : null}
          {sheetCount > 1 ? <Alert className="form-error" severity="error" role="alert">В сетапе может быть только одна Setup Sheet.</Alert> : null}
          {programs.length === 0 ? <Alert className="form-error" severity="error" role="alert">В комплекте должна быть хотя бы одна G-code-программа.</Alert> : null}
          {invalidNames.length > 0 ? <Alert className="form-error" severity="error" role="alert">Basename не может быть пустым, путём, «.» или «..».</Alert> : null}
          {error ? <Alert className="form-error" severity="error" role="alert">{error}</Alert> : null}
          {failed && stagedPrograms.length > 0 ? <div className="partial-import"><p>Успешно staged: {staged.length}. Можно повторить остальные файлы или сохранить staged-часть как draft.</p><Button className="button button--quiet" variant="outlined" type="button" disabled={pending} onClick={() => void savePartialDraft()}>Сохранить staged как draft</Button></div> : null}
          {progress ? <div className="import-progress" role="status"><span>{progress.label}{session?.jobId ? ` · Job ${session.jobId.slice(0, 8)} · ${job?.state ?? 'running'}` : ''}</span>{progress.total > 0 ? <><progress max={progress.total} value={Math.min(Math.max(job?.progress.completedBytes ?? 0, progress.loaded), progress.total)} /><small>{formatBytes(Math.max(job?.progress.completedBytes ?? 0, progress.loaded))} из {formatBytes(progress.total)}</small></> : null}</div> : <p className="form-hint">До commit файлы находятся в staging и не видны в библиотеке.</p>}
        </div>
      )}
    </Modal>
  )
}
