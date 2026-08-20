import { useEffect, useMemo, useRef, useState } from 'react'
import {
	ApiError,
  cancelJob,
  deleteArtifact,
	listActiveJobs,
  mutateProgram,
  setCurrentSetup,
  setupAction,
  uploadProgram,
  uploadSetupSheet,
  waitForJob,
} from '../api'
import type { CurrentSetup, Job, Setup, ValidationIssue } from '../domain'
import { asJob, errorMessage, formatBytes, formatDate, resultSetupId, sourceLabels, statusLabels } from '../ui'
import { GCodePreview } from './GCodePreview'
import {
  ConfirmOperationDialog,
  DuplicateSetupDialog,
  FileOperationDialog,
  MetadataEditor,
  PermanentDeleteDialog,
  RenameProgramDialog,
} from './SetupOperationDialogs'
import { SetupSheetViewer } from './SetupSheetViewer'
import { DeleteProgramDialog, MultiProgramUploadDialog } from './ProgramOperationDialogs'

type ConfirmKind = 'validate' | 'current' | 'archive' | 'restore' | 'primary' | 'delete-artifact'
type FileIntent =
  | { kind: 'replace-program'; file: File; artifactId: string }
  | { kind: 'put-sheet'; file: File; artifactId?: string }

interface Props {
  setup: Setup
  current: CurrentSetup | null
  onBack: () => void
  onChanged: (setup: Setup) => void
  onReload: () => Promise<void>
  onOpenSetup: (setupId: string) => void
  onCurrentChanged: () => Promise<void>
  onDeleted: () => void
  selectedArtifactId?: string
  initialLine?: number
  onSelectedArtifact: (artifactId: string, line?: number) => void
}

function jobIssues(job: Job): ValidationIssue[] {
  if (typeof job.result !== 'object' || job.result === null) return []
  const issues = (job.result as Record<string, unknown>).issues
  if (!Array.isArray(issues)) return []
  return issues.flatMap((value) => {
    if (typeof value !== 'object' || value === null) return []
    const issue = value as Record<string, unknown>
    if (typeof issue.code !== 'string' || typeof issue.message !== 'string') return []
    return [{
      code: issue.code,
      severity: issue.severity === 'warning' ? 'warning' as const : 'error' as const,
      message: issue.message,
      artifactId: typeof issue.artifactId === 'string' ? issue.artifactId : undefined,
      action: typeof issue.action === 'string' ? issue.action : undefined,
    }]
  })
}

function JobNotice({ job, setup, onCancel, onReload }: { job: Job; setup: Setup; onCancel: () => void; onReload: () => Promise<void> }) {
  const active = ['queued', 'running', 'cancelling'].includes(job.state)
  const itemPercent = job.progress.totalItems
    ? Math.round((job.progress.completedItems / job.progress.totalItems) * 100)
    : undefined
  const bytePercent = job.progress.totalBytes
    ? Math.round((job.progress.completedBytes / job.progress.totalBytes) * 100)
    : undefined
  const completed = bytePercent ?? itemPercent
  const issues = jobIssues(job)
	const labels: Record<string, string> = {
		validate: 'Проверка сетапа', duplicate: 'Дублирование сетапа',
		addPrograms: 'Добавление программ', replaceProgram: 'Загрузка файла сетапа',
		updateSetupSheet: 'Загрузка Setup Sheet', restore: 'Восстановление сетапа',
	}
  return (
    <section className={`job-notice job-notice--${job.state}`} aria-live="polite" aria-busy={active || undefined}>
      <div>
        <strong>{labels[job.kind] ?? 'Фоновая операция сетапа'}</strong>
        <p>
          {active ? `Выполняется${completed === undefined ? '…' : ` · ${completed}%`}` : null}
          {job.state === 'succeeded' ? 'Завершено.' : null}
          {job.state === 'failed' ? `Не завершено: ${job.errorCode ?? 'ошибка операции'}.` : null}
          {job.state === 'conflict' ? 'Сетап изменился во время операции. Обновите карточку.' : null}
          {job.state === 'cancelled' ? 'Операция отменена.' : null}
        </p>
      </div>
      {active ? <button className="button button--quiet" type="button" onClick={onCancel}>Отменить job</button> : null}
      {job.state === 'conflict' ? <button className="button button--quiet" type="button" onClick={() => void onReload()}>Загрузить актуальную revision</button> : null}
      {!active && issues.length > 0 ? <ul className="validation-issues">
        {issues.map((issue, index) => {
          const artifact = setup.artifacts.find((item) => item.artifactId === issue.artifactId)
          return <li key={`${issue.code}-${issue.artifactId ?? 'setup'}-${index}`}>
            <strong>{artifact?.displayName ?? 'Весь сетап'} · {issue.severity === 'warning' ? 'Предупреждение' : 'Ошибка'}</strong>
            <span>{issue.message}</span>
            {issue.action ? <small>Действие: {issue.action}</small> : null}
          </li>
        })}
      </ul> : null}
    </section>
  )
}

function readinessReason(reason: string): string {
  const labels: Record<string, string> = {
    NO_PROGRAMS: 'Добавьте хотя бы одну G-code-программу.',
    NO_PRIMARY_PROGRAM: 'Назначьте основную программу.',
    SETUP_SHEET_REQUIRED: 'Добавьте общую Setup Sheet.',
    VALIDATION_REQUIRED: 'Проверьте текущую revision.',
    ARTIFACT_UNAVAILABLE: 'Один из файлов недоступен или изменился.',
    'setup is archived': 'Сетап находится в архиве.',
    'add at least one G-code program': 'Добавьте хотя бы одну G-code-программу.',
    'add a setup sheet': 'Добавьте общую Setup Sheet.',
    'validate this revision': 'Проверьте текущую revision.',
    'managed content needs attention': 'Управляемые файлы требуют внимания.',
    'one or more managed artifacts changed or became unavailable': 'Один или несколько управляемых файлов изменились или стали недоступны.',
  }
  return labels[reason] ?? reason
}

export function SetupDetail({
  setup,
  current,
  onBack,
  onChanged,
  onReload,
  onOpenSetup,
  onCurrentChanged,
  onDeleted,
  selectedArtifactId,
  initialLine,
  onSelectedArtifact,
}: Props) {
  const programs = useMemo(() => setup.artifacts.filter((item) => item.role === 'program'), [setup.artifacts])
  const sheet = setup.artifacts.find((item) => item.role === 'setup_sheet')
  const selectedProgram = programs.find((item) => item.artifactId === selectedArtifactId) ?? programs[0]
  const [viewerOpen, setViewerOpen] = useState(false)
  const [renameId, setRenameId] = useState<string>()
  const [confirm, setConfirm] = useState<{ kind: ConfirmKind; artifactId?: string }>()
  const [duplicateOpen, setDuplicateOpen] = useState(false)
  const [deleteOpen, setDeleteOpen] = useState(false)
  const [deleteProgramId, setDeleteProgramId] = useState<string>()
  const [programFiles, setProgramFiles] = useState<File[]>()
  const [fileIntent, setFileIntent] = useState<FileIntent>()
  const [job, setJob] = useState<Job>()
  const [jobError, setJobError] = useState<string>()
  const programPicker = useRef<HTMLInputElement>(null)
  const replacementPicker = useRef<HTMLInputElement>(null)
  const sheetPicker = useRef<HTMLInputElement>(null)
  const replaceProgramId = useRef<string>()
  const pollController = useRef<AbortController>()

  useEffect(() => () => pollController.current?.abort(), [])

	useEffect(() => {
		const controller = new AbortController()
		void listActiveJobs(setup.setupId, controller.signal).then((jobs) => {
			const active = jobs.find((item) => ['queued', 'running', 'cancelling'].includes(item.state))
			if (!active) return
			setJob(active)
			void waitForJob(active, controller.signal).then(async (terminal) => {
				setJob(terminal)
				if (terminal.state === 'succeeded' && terminal.kind === 'duplicate') {
					const target = resultSetupId(terminal)
					if (target) onOpenSetup(target)
				} else if (terminal.state === 'succeeded') await onReload()
			}, () => undefined)
		}, () => undefined)
		return () => controller.abort()
	}, [setup.setupId, onReload, onOpenSetup])

  const acceptJob = async (initial: Job, kind: 'validate' | 'duplicate' | 'restore') => {
    pollController.current?.abort()
    const controller = new AbortController()
    pollController.current = controller
    setJob(initial)
    setJobError(undefined)
    try {
      const terminal = await waitForJob(initial, controller.signal)
      setJob(terminal)
      if (terminal.state === 'succeeded') {
        if (kind === 'duplicate') {
          const target = resultSetupId(terminal)
          if (target) onOpenSetup(target)
        } else {
          await onReload()
        }
      }
    } catch (reason) {
      if (!(reason instanceof DOMException && reason.name === 'AbortError')) setJobError(errorMessage(reason))
    }
  }

  const startValidation = async (key: string) => {
    const initial = asJob(await setupAction(setup.setupId, 'validate', setup.revision, {}, key))
    void acceptJob(initial, 'validate')
  }

  const startDuplicate = async (name: string, key: string) => {
    const initial = asJob(await setupAction(setup.setupId, 'duplicate', setup.revision, { name }, key))
		pollController.current?.abort()
		const controller = new AbortController()
		pollController.current = controller
		setJob(initial)
		setJobError(undefined)
		const terminal = await waitForJob(initial, controller.signal)
		setJob(terminal)
		if (terminal.state === 'succeeded') {
			const target = resultSetupId(terminal)
			if (target) onOpenSetup(target)
			return
		}
		throw new ApiError({
			message: `Duplicate job завершён: ${terminal.errorCode ?? terminal.state}.`,
			status: terminal.state === 'conflict' ? 409 : 422,
			code: terminal.errorCode ?? terminal.state.toUpperCase(),
		})
  }

  const cancelActiveJob = async () => {
    if (!job) return
    try {
      setJob(await cancelJob(job.jobId))
    } catch (reason) {
      setJobError(errorMessage(reason))
    }
  }

  const confirmProps = (() => {
    if (!confirm) return undefined
    const artifact = setup.artifacts.find((item) => item.artifactId === confirm.artifactId)
    if (confirm.kind === 'validate') return {
      title: 'Проверить текущую revision',
      description: `Будет проверена revision ${setup.revision}. Проверка не запускает LinuxCNC и не подтверждает безопасность обработки.`,
      confirmLabel: 'Запустить проверку',
      onConfirm: startValidation,
    }
    if (confirm.kind === 'current') return {
      title: 'Выбрать текущий сетап',
      description: `Закрепить «${setup.name}», revision ${setup.revision}. Это только выбор в Setup Manager: G-code не копируется, не исполняется и LinuxCNC не запускается.`,
      confirmLabel: 'Выбрать, не запускать',
      onConfirm: async (key: string) => { await setCurrentSetup(setup.setupId, setup.revision, current, key); await onCurrentChanged() },
    }
    if (confirm.kind === 'archive') return {
      title: 'Архивировать сетап',
      description: 'Сетап исчезнет из рабочего списка, но останется доступен в архиве.',
      confirmLabel: 'Переместить в архив', danger: true,
      onConfirm: async (key: string) => { await setupAction(setup.setupId, 'archive', setup.revision, {}, key); await onReload() },
    }
    if (confirm.kind === 'restore') return {
      title: 'Восстановить сетап',
      description: 'Сетап вернётся в рабочую библиотеку с сохранёнными файлами.',
      confirmLabel: 'Восстановить',
      onConfirm: async (key: string) => {
				const initial = asJob(await setupAction(setup.setupId, 'restore', setup.revision, {}, key))
				void acceptJob(initial, 'restore')
			},
    }
    if (confirm.kind === 'primary' && artifact) return {
      title: 'Назначить основную программу',
      description: `${artifact.displayName} станет основной программой этого сетапа. Ничего не будет исполнено.`,
      confirmLabel: 'Назначить основной',
      onConfirm: async (key: string) => onChanged(await mutateProgram(setup, artifact, { primary: true }, key)),
    }
    if (confirm.kind === 'delete-artifact' && artifact) return {
      title: 'Удалить Setup Sheet',
      description: `${artifact.displayName} будет удалён только из этой карточки.`,
      confirmLabel: 'Удалить', danger: true,
      onConfirm: async (key: string) => onChanged(await deleteArtifact(setup, artifact, key)),
    }
    return undefined
  })()

  const fileArtifact = fileIntent?.kind === 'replace-program'
    ? programs.find((item) => item.artifactId === fileIntent.artifactId)
    : fileIntent?.kind === 'put-sheet' ? sheet : undefined

  return (
    <article className="setup-detail" aria-labelledby="setup-title">
      <button className="back-button" type="button" onClick={onBack}>← К библиотеке</button>
      <header className="detail-hero">
        <div>
          <p className="eyebrow">{sourceLabels[setup.source]} · Revision {setup.revision}</p>
          <div className="title-with-status"><h1 id="setup-title">{setup.name}</h1><span className={`status-badge status-badge--${setup.status}`}>{statusLabels[setup.status]}</span></div>
          <p>Изменён {formatDate(setup.updatedAt)} · ID используется внутри системы и не раскрывает физическое хранилище.</p>
        </div>
        <div className="detail-actions" aria-label="Действия сетапа">
          {setup.status !== 'archived' ? <button className="button button--primary" type="button" onClick={() => setConfirm({ kind: 'validate' })}>Проверить</button> : null}
          {setup.status === 'ready' && current?.setupId !== setup.setupId ? <button className="button button--quiet" type="button" onClick={() => setConfirm({ kind: 'current' })}>Выбрать текущим</button> : null}
          <button className="button button--quiet" type="button" onClick={() => setDuplicateOpen(true)}>Дублировать</button>
          {setup.status === 'archived' ? (
            <><button className="button button--quiet" type="button" onClick={() => setConfirm({ kind: 'restore' })}>Восстановить</button><button className="button button--danger" type="button" onClick={() => setDeleteOpen(true)}>Удалить навсегда</button></>
          ) : <button className="button button--quiet" type="button" disabled={current?.setupId === setup.setupId} title={current?.setupId === setup.setupId ? 'Сначала снимите выбор текущего сетапа' : undefined} onClick={() => setConfirm({ kind: 'archive' })}>В архив</button>}
        </div>
      </header>

      {current?.setupId === setup.setupId ? <p className="current-detail-notice">Текущий сетап · выбрана revision {current.revisionSelected}. Выбор не означает запуск программы.</p> : null}
      {job ? <JobNotice job={job} setup={setup} onCancel={() => void cancelActiveJob()} onReload={onReload} /> : null}
      {jobError ? <p className="form-error" role="alert">{jobError}</p> : null}

      <section className={`readiness readiness--${setup.status}`} aria-labelledby="readiness-title">
        <div><p className="eyebrow">Готовность</p><h2 id="readiness-title">{setup.status === 'ready' ? 'Revision прошла структурную проверку' : 'Что нужно проверить'}</h2></div>
        {setup.notReadyReasons && setup.notReadyReasons.length > 0 ? <ul>{setup.notReadyReasons.map((reason) => <li key={reason}>{readinessReason(reason)}</li>)}</ul> : <p>Нет известных причин неготовности.</p>}
        <p className="readiness__scope">Готовность означает только проверку состава, читаемости и формата. Она не проверяет станок, оснастку, траекторию или безопасность исполнения.</p>
      </section>

      <MetadataEditor setup={setup} onChanged={onChanged} onReload={onReload} />

      <section className="detail-section" aria-labelledby="programs-title">
        <div className="section-heading">
          <div><p className="eyebrow">G-code</p><h2 id="programs-title">Программы <span>{programs.length}</span></h2></div>
          {setup.status !== 'archived' ? <button className="button button--primary" type="button" onClick={() => programPicker.current?.click()}>Добавить программу</button> : null}
        </div>
        <input ref={programPicker} className="visually-hidden" type="file" tabIndex={-1} aria-hidden="true" multiple onChange={(event) => { const files = event.target.files ? Array.from(event.target.files) : []; if (files.length > 0) setProgramFiles(files); event.target.value = '' }} />
        <input ref={replacementPicker} className="visually-hidden" type="file" tabIndex={-1} aria-hidden="true" onChange={(event) => { const file = event.target.files?.[0]; if (file && replaceProgramId.current) setFileIntent({ kind: 'replace-program', file, artifactId: replaceProgramId.current }); event.target.value = '' }} />
        {programs.length === 0 ? <div className="section-empty"><p>В этом сетапе пока нет программ.</p></div> : (
          <ul className="artifact-list">
            {programs.map((artifact) => (
              <li key={artifact.artifactId} className={selectedProgram?.artifactId === artifact.artifactId ? 'artifact-list__selected' : ''}>
                <button className="artifact-list__open" type="button" onClick={() => onSelectedArtifact(artifact.artifactId)}>
                  <strong>{artifact.displayName}</strong><span>{artifact.primary ? 'Основная · ' : ''}{formatBytes(artifact.byteSize)} · {artifact.state}</span>
                </button>
                {setup.status !== 'archived' ? <div className="artifact-list__actions">
                  {!artifact.primary ? <button type="button" onClick={() => setConfirm({ kind: 'primary', artifactId: artifact.artifactId })}>Основная</button> : null}
                  <button type="button" onClick={() => setRenameId(artifact.artifactId)}>Переименовать</button>
                  <button type="button" onClick={() => { replaceProgramId.current = artifact.artifactId; replacementPicker.current?.click() }}>Заменить</button>
                  <button type="button" onClick={() => setDeleteProgramId(artifact.artifactId)}>Удалить</button>
                </div> : null}
              </li>
            ))}
          </ul>
        )}
        {selectedProgram ? <GCodePreview setup={setup} artifact={selectedProgram} initialLine={selectedProgram.artifactId === selectedArtifactId ? initialLine : 1} onLineChanged={(line) => onSelectedArtifact(selectedProgram.artifactId, line)} onOpenSetupSheet={sheet ? () => setViewerOpen(true) : undefined} onArtifactChanged={() => void onReload()} /> : null}
      </section>

      <section className="detail-section" aria-labelledby="sheet-title">
        <div className="section-heading"><div><p className="eyebrow">Общий документ</p><h2 id="sheet-title">Setup Sheet</h2></div></div>
        <input ref={sheetPicker} className="visually-hidden" type="file" tabIndex={-1} aria-hidden="true" accept=".pdf,.html,.htm" onChange={(event) => { const file = event.target.files?.[0]; if (file) setFileIntent({ kind: 'put-sheet', file, artifactId: sheet?.artifactId }); event.target.value = '' }} />
        {sheet ? <div className="sheet-card"><div><strong>{sheet.displayName}</strong><p>{sheet.mediaType} · {formatBytes(sheet.byteSize)} · {sheet.state}</p></div><div className="sheet-card__actions"><button className="button button--primary" type="button" onClick={() => setViewerOpen(true)}>Открыть</button>{setup.status !== 'archived' ? <><button className="button button--quiet" type="button" onClick={() => sheetPicker.current?.click()}>Заменить</button><button className="button button--quiet" type="button" onClick={() => setConfirm({ kind: 'delete-artifact', artifactId: sheet.artifactId })}>Удалить</button></> : null}</div></div> : <div className="section-empty"><p>Setup Sheet не добавлена.</p>{setup.status !== 'archived' ? <button className="button button--primary" type="button" onClick={() => sheetPicker.current?.click()}>Добавить PDF или HTML</button> : null}</div>}
      </section>

      {viewerOpen && sheet ? <SetupSheetViewer setup={setup} artifact={sheet} onClose={() => setViewerOpen(false)} onReplace={() => { setViewerOpen(false); sheetPicker.current?.click() }} /> : null}
      {renameId && programs.find((item) => item.artifactId === renameId) ? <RenameProgramDialog setup={setup} artifact={programs.find((item) => item.artifactId === renameId)!} onClose={() => setRenameId(undefined)} onChanged={onChanged} onReload={onReload} /> : null}
      {confirmProps ? <ConfirmOperationDialog
        {...confirmProps}
        onClose={() => setConfirm(undefined)}
        onReload={confirm?.kind === 'current' ? async () => { await onCurrentChanged(); await onReload() } : onReload}
      /> : null}
      {duplicateOpen ? <DuplicateSetupDialog setup={setup} onClose={() => setDuplicateOpen(false)} onStart={startDuplicate} onReload={onReload} /> : null}
      {deleteOpen ? <PermanentDeleteDialog setup={setup} onClose={() => setDeleteOpen(false)} onDeleted={onDeleted} onReload={onReload} /> : null}
      {programFiles ? <MultiProgramUploadDialog setup={setup} files={programFiles} onClose={() => setProgramFiles(undefined)} onChanged={onChanged} onReload={onReload} /> : null}
      {deleteProgramId && programs.find((item) => item.artifactId === deleteProgramId) ? <DeleteProgramDialog setup={setup} artifact={programs.find((item) => item.artifactId === deleteProgramId)!} onClose={() => setDeleteProgramId(undefined)} onChanged={onChanged} onReload={onReload} /> : null}
      {fileIntent ? <FileOperationDialog
        title={fileIntent.kind === 'replace-program' ? 'Заменить программу' : sheet ? 'Заменить Setup Sheet' : 'Добавить Setup Sheet'}
        description="Файл будет потоково загружен в управляемое хранилище и опубликован атомарно."
        setup={setup} file={fileIntent.file} artifact={fileArtifact} onClose={() => setFileIntent(undefined)} onChanged={onChanged} onReload={onReload}
        onConfirm={(key, options) => fileIntent.kind === 'replace-program' && fileArtifact ? uploadProgram(setup, fileIntent.file, fileArtifact, key, options) : uploadSetupSheet(setup, fileIntent.file, sheet, key, options)}
      /> : null}
    </article>
  )
}
