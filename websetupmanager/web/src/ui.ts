import { ApiError } from './api'
import type { Job, SetupStatus } from './domain'

export const statusLabels: Record<SetupStatus, string> = {
  draft: 'Черновик',
  ready: 'Готов',
  attention: 'Требует внимания',
  archived: 'В архиве',
}

export const sourceLabels = {
  created: 'Создан вручную',
  imported: 'Импортирован',
  duplicated: 'Дубликат',
} as const

const dateFormatter = new Intl.DateTimeFormat('ru', {
  dateStyle: 'medium',
  timeStyle: 'short',
})

export function formatDate(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? '—' : dateFormatter.format(date)
}

export function formatBytes(value: number): string {
  if (!Number.isFinite(value) || value < 0) return '—'
  if (value < 1024) return `${value.toLocaleString('ru')} Б`
  const units = ['КиБ', 'МиБ', 'ГиБ', 'ТиБ']
  let scaled = value
  let unit = -1
  do {
    scaled /= 1024
    unit += 1
  } while (scaled >= 1024 && unit < units.length - 1)
  return `${scaled.toLocaleString('ru', { maximumFractionDigits: scaled >= 10 ? 1 : 2 })} ${units[unit]}`
}

export function errorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.message
  if (error instanceof Error) return error.message
  return 'Операцию не удалось завершить.'
}

export function isRevisionConflict(error: unknown): boolean {
  return error instanceof ApiError && (
    error.code === 'REVISION_CONFLICT'
    || error.code === 'ARTIFACT_CHANGED'
    || error.code === 'CURRENT_SETUP_CONFLICT'
  )
}

export function isNetworkError(error: unknown): boolean {
  return error instanceof ApiError && (error.code === 'NETWORK_ERROR' || error.status === 0)
}

export function asJob(value: unknown): Job {
  if (typeof value !== 'object' || value === null) throw new Error('Backend не вернул job.')
  const record = value as Record<string, unknown>
  const progress = typeof record.progress === 'object' && record.progress !== null
    ? record.progress as Record<string, unknown>
    : {}
  const state = String(record.state)
  if (
    typeof record.jobId !== 'string'
    || typeof record.kind !== 'string'
    || !['queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled', 'conflict'].includes(state)
  ) {
    throw new Error('Backend вернул некорректный job.')
  }
  return {
    jobId: record.jobId,
    kind: record.kind,
    setupId: typeof record.setupId === 'string' ? record.setupId : undefined,
    state: state as Job['state'],
    progress: {
      completedBytes: typeof progress.completedBytes === 'number' ? progress.completedBytes : 0,
      totalBytes: typeof progress.totalBytes === 'number' ? progress.totalBytes : undefined,
      completedItems: typeof progress.completedItems === 'number' ? progress.completedItems : 0,
      totalItems: typeof progress.totalItems === 'number' ? progress.totalItems : undefined,
    },
    errorCode: typeof record.errorCode === 'string' ? record.errorCode : undefined,
    result: record.result,
    createdAt: typeof record.createdAt === 'string' ? record.createdAt : new Date().toISOString(),
    startedAt: typeof record.startedAt === 'string' ? record.startedAt : undefined,
    completedAt: typeof record.completedAt === 'string' ? record.completedAt : undefined,
  }
}

export function resultSetupId(job: Job): string | undefined {
  if (typeof job.result !== 'object' || job.result === null) return undefined
  const value = (job.result as Record<string, unknown>).setupId
  return typeof value === 'string' ? value : undefined
}
