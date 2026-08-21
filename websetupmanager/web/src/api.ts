import type { Artifact, ArtifactRole, CurrentSetup, ImportArtifact, ImportSession, Job, RecentSetup, Setup, SetupStatus, SetupSummary, UIState } from './domain'

export interface ApiErrorPayload {
  code?: unknown
  message?: unknown
  request_id?: unknown
  requestId?: unknown
  details?: unknown
  retryable?: unknown
}

export interface Capabilities {
  libraryId?: string
  libraryAlias: string
  csrfToken?: string
  gcodeExtensions: string[]
  requireSetupSheetForReady: boolean
  features: Readonly<Record<string, boolean>>
}

export interface AuthenticatedUser {
  username: string
}

export interface AuthSession {
  authenticated: boolean
  loginRequired: boolean
  user: AuthenticatedUser | null
  csrfToken?: string
}

export class ApiError extends Error {
  readonly status: number
  readonly code: string
  readonly requestId?: string
  readonly details?: unknown
  readonly retryable: boolean

  constructor({
    message,
    status,
    code,
    requestId,
    details,
    retryable = false,
    cause,
  }: {
    message: string
    status: number
    code: string
    requestId?: string
    details?: unknown
    retryable?: boolean
    cause?: unknown
  }) {
    super(message, { cause })
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.requestId = requestId
    this.details = details
    this.retryable = retryable
  }
}
type JsonRecord = Record<string, unknown>

let csrfToken: string | undefined
let authenticatedSession = false
let unauthorizedHandler: (() => void) | undefined

interface ApiRequestOptions {
  csrf?: boolean
  suppressUnauthorized?: boolean
  csrfRetryAttempted?: boolean
}

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}

function requiredString(value: unknown, field: string): string {
  const result = asNonEmptyString(value)
  if (!result) throw invalidResponse(`Missing ${field}.`)
  return result
}

function requiredNumber(value: unknown, field: string): number {
  if (typeof value !== 'number' || !Number.isSafeInteger(value) || value < 0) {
    throw invalidResponse(`Invalid ${field}.`)
  }
  return value
}

function invalidResponse(message = 'The service returned an invalid response.'): ApiError {
  return new ApiError({ message, status: 0, code: 'INVALID_RESPONSE' })
}

function asNonEmptyString(value: unknown): string | undefined {
  return typeof value === 'string' && value.trim() !== '' ? value : undefined
}

function asOptionalPositiveNumber(value: unknown): number | undefined {
  return typeof value === 'number' && Number.isFinite(value) && value >= 0
    ? value
    : undefined
}

function requestIdFrom(payload: ApiErrorPayload, response: Response): string | undefined {
  return asNonEmptyString(payload.request_id)
    ?? asNonEmptyString(payload.requestId)
    ?? asNonEmptyString(response.headers.get('x-request-id'))
}

function apiPath(path: string): string {
  if (!path.startsWith('/api/v1/') || path.includes('\\')) {
    throw new ApiError({
      message: 'API address is outside the supported same-origin namespace.',
      status: 0,
      code: 'INVALID_API_PATH',
    })
  }

  const url = new URL(path, window.location.origin)
  if (url.origin !== window.location.origin || !url.pathname.startsWith('/api/v1/')) {
    throw new ApiError({
      message: 'API address is outside the supported same-origin namespace.',
      status: 0,
      code: 'INVALID_API_PATH',
    })
  }

  return `${url.pathname}${url.search}`
}

async function responseBody(response: Response): Promise<unknown> {
  if (response.status === 204 || response.headers.get('content-length') === '0') {
    return undefined
  }

  const text = await response.text()
  if (text === '') return undefined

  try {
    return JSON.parse(text) as unknown
  } catch (cause) {
    throw new ApiError({
      message: response.ok
        ? 'The service returned an invalid response.'
        : 'The service request failed.',
      status: response.status,
      code: 'INVALID_RESPONSE',
      requestId: asNonEmptyString(response.headers.get('x-request-id')),
      retryable: response.status >= 500,
      cause,
    })
  }
}

function errorFromResponse(response: Response, body: unknown): ApiError {
  const root = isRecord(body) ? body : {}
  const nested = isRecord(root.error) ? root.error : root
  const payload = nested as ApiErrorPayload

  return new ApiError({
    message: asNonEmptyString(payload.message) ?? 'The service request failed.',
    status: response.status,
    code: asNonEmptyString(payload.code) ?? 'HTTP_ERROR',
    requestId: requestIdFrom(payload, response),
    details: payload.details,
    retryable: typeof payload.retryable === 'boolean'
      ? payload.retryable
      : response.status >= 500,
  })
}

function isSafeMethod(method: string): boolean {
  return method === 'GET' || method === 'HEAD' || method === 'OPTIONS'
}

export function setCsrfToken(token: string | undefined): void {
  csrfToken = asNonEmptyString(token)
  if (csrfToken) authenticatedSession = true
}

export function clearApiSession(): void {
  csrfToken = undefined
  authenticatedSession = false
}

export function setUnauthorizedHandler(handler?: () => void): void {
  unauthorizedHandler = handler
}

function expireApiSession(): void {
  const notify = authenticatedSession || csrfToken !== undefined
  clearApiSession()
  if (notify) unauthorizedHandler?.()
}

export async function apiRequest<T>(
  path: string,
  init: RequestInit = {},
  options: ApiRequestOptions = {},
): Promise<T> {
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')

  if (!isSafeMethod(method) && options.csrf !== false) {
    if (!csrfToken) {
      throw new ApiError({
        message: 'The security token is unavailable. Refresh the application and try again.',
        status: 0,
        code: 'CSRF_TOKEN_MISSING',
      })
    }
    headers.set('X-CSRF-Token', csrfToken)
  }

  let response: Response
  try {
    response = await fetch(apiPath(path), {
      ...init,
      method,
      headers,
      credentials: 'same-origin',
      cache: 'no-store',
    })
  } catch (cause) {
    if (cause instanceof ApiError) throw cause
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause
    throw new ApiError({
      message: 'The local Setup Manager service is unavailable.',
      status: 0,
      code: 'NETWORK_ERROR',
      retryable: true,
      cause,
    })
  }

  if (response.status === 401 && !options.suppressUnauthorized) expireApiSession()

  const body = await responseBody(response)
  if (!response.ok) {
    const failure = errorFromResponse(response, body)
    if (
      response.status === 403
      && failure.code === 'CSRF_REJECTED'
      && options.csrf !== false
      && !options.csrfRetryAttempted
    ) {
      const session = await getAuthSession(init.signal ?? undefined)
      if (!session.authenticated) throw failure
      return apiRequest<T>(path, init, { ...options, csrfRetryAttempted: true })
    }
    throw failure
  }
  return body as T
}

export function newIdempotencyKey(): string {
  if (typeof crypto.randomUUID === 'function') return crypto.randomUUID()
  const bytes = new Uint8Array(16)
  crypto.getRandomValues(bytes)
  return Array.from(bytes, (value) => value.toString(16).padStart(2, '0')).join('')
}

export interface UploadOptions {
  signal?: AbortSignal
  onProgress?: (loaded: number, total: number) => void
}

function xhrUpload<T>(
  path: string,
  method: 'POST' | 'PUT',
  body: XMLHttpRequestBodyInit,
  totalBytes: number,
  key: string,
  normalize: (value: unknown) => T,
  options: UploadOptions = {},
  contentType?: string,
  extraHeaders: Readonly<Record<string, string>> = {},
): Promise<T> {
  if (!csrfToken) {
    return Promise.reject(new ApiError({
      message: 'The security token is unavailable. Refresh the application and try again.',
      status: 0,
      code: 'CSRF_TOKEN_MISSING',
    }))
  }
  const token = csrfToken
  if (options.signal?.aborted) {
    return Promise.reject(options.signal.reason instanceof Error
      ? options.signal.reason
      : new DOMException('Operation aborted', 'AbortError'))
  }
  return new Promise<T>((resolve, reject) => {
    const request = new XMLHttpRequest()
    let settled = false
    const finish = (callback: () => void) => {
      if (settled) return
      settled = true
      options.signal?.removeEventListener('abort', abort)
      callback()
    }
    const abort = () => request.abort()
    request.open(method, apiPath(path))
    request.withCredentials = true
    request.setRequestHeader('Accept', 'application/json')
    if (contentType) request.setRequestHeader('Content-Type', contentType)
    request.setRequestHeader('X-CSRF-Token', token)
    request.setRequestHeader('Idempotency-Key', key)
    Object.entries(extraHeaders).forEach(([name, value]) => request.setRequestHeader(name, value))
    request.upload.addEventListener('progress', (event) => {
      options.onProgress?.(Math.min(event.loaded, totalBytes), totalBytes)
    })
    request.addEventListener('load', () => {
      if (request.status === 401) expireApiSession()
      let body: unknown
      try { body = request.responseText === '' ? undefined : JSON.parse(request.responseText) as unknown }
      catch (cause) {
        finish(() => reject(new ApiError({
          message: 'The service returned an invalid response.', status: request.status,
          code: 'INVALID_RESPONSE', cause,
        })))
        return
      }
      if (request.status < 200 || request.status >= 300) {
        const response = new Response(request.responseText, {
          status: request.status,
          headers: { 'X-Request-ID': request.getResponseHeader('X-Request-ID') ?? '' },
        })
        finish(() => reject(errorFromResponse(response, body)))
        return
      }
      try {
        const normalized = normalize(body)
        finish(() => resolve(normalized))
      } catch (cause) {
        finish(() => reject(cause instanceof Error
          ? cause
          : new ApiError({
            message: 'The service response could not be normalized.', status: request.status,
            code: 'INVALID_RESPONSE', cause,
          })))
      }
    })
    request.addEventListener('error', () => finish(() => reject(new ApiError({
      message: 'The local Setup Manager service is unavailable.', status: 0,
      code: 'NETWORK_ERROR', retryable: true,
    }))))
    request.addEventListener('abort', () => finish(() => reject(
      options.signal?.reason instanceof Error
        ? options.signal.reason
        : new DOMException('Operation aborted', 'AbortError'),
    )))
    options.signal?.addEventListener('abort', abort, { once: true })
    request.send(body)
  })
}

export interface CatalogDestination {
  rootLabel: string
  rootDisplay: string
}

export interface CatalogFolder {
  folderId: string
  parentFolderId?: string
  name: string
  relativePath: string
  revision: number
}

export interface CatalogArtifact {
  artifactId: string
  displayName: string
  mediaType: string
  byteSize: number
  version: string
  relativePath: string
}

export interface CatalogSetup {
  setupId: string
  folderId?: string
  name: string
  description?: string
  revision: number
  program: CatalogArtifact | null
  setupSheet: CatalogArtifact | null
  programRelativePath?: string
  setupSheetRelativePath?: string
  updatedAt: string
}

export interface CatalogSnapshot {
  destination: CatalogDestination
  generation: string
  folders: CatalogFolder[]
  setups: CatalogSetup[]
}

async function jsonMutation<T>(path: string, method: string, body: unknown, key = newIdempotencyKey(), signal?: AbortSignal): Promise<T> {
  return apiRequest<T>(path, {
    method, signal, body: JSON.stringify(body),
    headers: { 'Content-Type': 'application/json', 'Idempotency-Key': key },
  })
}

function normalizeArtifact(value: unknown): Artifact {
  if (!isRecord(value)) throw invalidResponse()
  const role = value.role
  const state = value.state
  if (role !== 'program' && role !== 'setup_sheet') throw invalidResponse('Invalid artifact role.')
  if (!['available', 'missing', 'changed', 'corrupt', 'unavailable'].includes(String(state))) throw invalidResponse('Invalid artifact state.')
  return {
    artifactId: requiredString(value.artifactId, 'artifactId'),
    setupId: requiredString(value.setupId, 'setupId'), role,
    displayName: requiredString(value.displayName, 'displayName'),
    mediaType: requiredString(value.mediaType, 'mediaType'),
    byteSize: requiredNumber(value.byteSize, 'byteSize'),
    version: requiredString(value.version, 'version'),
    position: requiredNumber(value.position, 'position'),
    primary: value.primary === true,
    state: state as Artifact['state'],
    createdAt: requiredString(value.createdAt, 'createdAt'),
    updatedAt: requiredString(value.updatedAt, 'updatedAt'),
  }
}

function normalizeCatalogArtifact(value: unknown, field: string): CatalogArtifact {
  if (!isRecord(value)) throw invalidResponse(`Invalid catalog ${field}.`)
  return {
    artifactId: requiredString(value.artifactId, `${field}.artifactId`),
    displayName: requiredString(value.displayName, `${field}.displayName`),
    mediaType: requiredString(value.mediaType, `${field}.mediaType`),
    byteSize: requiredNumber(value.byteSize, `${field}.byteSize`),
    version: requiredString(value.version, `${field}.version`),
    relativePath: requiredString(value.relativePath, `${field}.relativePath`),
  }
}

function normalizeCatalogFolder(value: unknown): CatalogFolder {
  if (!isRecord(value)) throw invalidResponse('Invalid catalog folder.')
  return {
    folderId: requiredString(value.folderId, 'folderId'),
    parentFolderId: asNonEmptyString(value.parentFolderId),
    name: requiredString(value.name, 'folder name'),
    relativePath: requiredString(value.relativePath, 'folder relativePath'),
    revision: requiredNumber(value.revision, 'folder revision'),
  }
}

function normalizeCatalogSetup(value: unknown): CatalogSetup {
  if (!isRecord(value)) throw invalidResponse('Invalid catalog setup.')
  const program = value.program === null || value.program === undefined
    ? null
    : normalizeCatalogArtifact(value.program, 'program')
  const setupSheet = value.setupSheet === null || value.setupSheet === undefined
    ? null
    : normalizeCatalogArtifact(value.setupSheet, 'setupSheet')
  return {
    setupId: requiredString(value.setupId, 'setupId'),
    folderId: asNonEmptyString(value.folderId),
    name: requiredString(value.name, 'setup name'),
    description: typeof value.description === 'string' ? value.description : undefined,
    revision: requiredNumber(value.revision, 'setup revision'),
    program,
    setupSheet,
    programRelativePath: asNonEmptyString(value.programRelativePath) ?? program?.relativePath,
    setupSheetRelativePath: asNonEmptyString(value.setupSheetRelativePath) ?? setupSheet?.relativePath,
    updatedAt: requiredString(value.updatedAt, 'updatedAt'),
  }
}

function normalizeSetup(value: unknown): Setup {
  if (!isRecord(value)) throw invalidResponse()
  const status = String(value.status) as SetupStatus
  if (!['draft', 'ready', 'attention', 'archived'].includes(status)) throw invalidResponse('Invalid setup status.')
  if (!Array.isArray(value.artifacts)) throw invalidResponse('Invalid setup artifacts.')
  const source = value.source
  if (source !== 'created' && source !== 'imported' && source !== 'duplicated') throw invalidResponse('Invalid setup source.')
  return {
    setupId: requiredString(value.setupId, 'setupId'),
    libraryId: requiredString(value.libraryId, 'libraryId'),
    name: requiredString(value.name, 'name'),
    description: typeof value.description === 'string' ? value.description : undefined,
    status,
    archivedFromStatus: typeof value.archivedFromStatus === 'string' ? value.archivedFromStatus as SetupStatus : undefined,
    revision: requiredNumber(value.revision, 'revision'), source,
    sourceSetupId: asNonEmptyString(value.sourceSetupId),
    importSessionId: asNonEmptyString(value.importSessionId),
    artifacts: value.artifacts.map(normalizeArtifact),
    notReadyReasons: normalizeStringList(value.notReadyReasons),
    createdAt: requiredString(value.createdAt, 'createdAt'),
    updatedAt: requiredString(value.updatedAt, 'updatedAt'),
  }
}

function normalizeSummary(value: unknown): SetupSummary {
  if (!isRecord(value)) throw invalidResponse()
  const status = String(value.status) as SetupStatus
  if (!['draft', 'ready', 'attention', 'archived'].includes(status)) throw invalidResponse('Invalid setup status.')
  return {
    setupId: requiredString(value.setupId, 'setupId'), name: requiredString(value.name, 'name'),
    description: typeof value.description === 'string' ? value.description : undefined,
    status, revision: requiredNumber(value.revision, 'revision'),
    programCount: requiredNumber(value.programCount, 'programCount'),
    hasSetupSheet: value.hasSetupSheet === true, isCurrent: value.isCurrent === true,
    notReadyReasons: normalizeStringList(value.notReadyReasons),
    createdAt: requiredString(value.createdAt, 'createdAt'),
    updatedAt: requiredString(value.updatedAt, 'updatedAt'),
    lastOpenedAt: asNonEmptyString(value.lastOpenedAt),
  }
}

function normalizeJob(value: unknown): Job {
  if (!isRecord(value) || !isRecord(value.progress)) throw invalidResponse('Invalid job response.')
  const state = String(value.state)
  if (!['queued', 'running', 'cancelling', 'succeeded', 'failed', 'cancelled', 'conflict'].includes(state)) throw invalidResponse('Invalid job state.')
  return {
    jobId: requiredString(value.jobId, 'jobId'), kind: requiredString(value.kind, 'kind'),
    setupId: asNonEmptyString(value.setupId), state: state as Job['state'],
    progress: {
      completedBytes: requiredNumber(value.progress.completedBytes ?? 0, 'completedBytes'),
      totalBytes: typeof value.progress.totalBytes === 'number' ? value.progress.totalBytes : undefined,
      completedItems: requiredNumber(value.progress.completedItems ?? 0, 'completedItems'),
      totalItems: typeof value.progress.totalItems === 'number' ? value.progress.totalItems : undefined,
    },
    errorCode: asNonEmptyString(value.errorCode), result: value.result,
    createdAt: requiredString(value.createdAt, 'createdAt'),
    startedAt: asNonEmptyString(value.startedAt), completedAt: asNonEmptyString(value.completedAt),
  }
}

function normalizeImportArtifact(value: unknown): ImportArtifact {
  if (!isRecord(value)) throw invalidResponse('Invalid import artifact.')
  const role = value.role
  const state = String(value.state)
  if (role !== 'program' && role !== 'setup_sheet') throw invalidResponse('Invalid import artifact role.')
  if (!['pending', 'uploading', 'staged', 'excluded', 'published', 'failed'].includes(state)) throw invalidResponse('Invalid import artifact state.')
  return {
    importArtifactId: requiredString(value.importArtifactId, 'importArtifactId'),
    artifactId: asNonEmptyString(value.artifactId), role,
    displayName: requiredString(value.displayName, 'displayName'),
    byteSize: requiredNumber(value.byteSize, 'byteSize'),
    bytes: requiredNumber(value.bytes, 'bytes'), state: state as ImportArtifact['state'],
    errorCode: asNonEmptyString(value.errorCode),
  }
}

function normalizeImportSession(value: unknown): ImportSession {
  if (!isRecord(value) || !Array.isArray(value.artifacts)) throw invalidResponse('Invalid import session.')
  const state = String(value.state)
  if (!['staging', 'committing', 'succeeded', 'draft_saved', 'failed', 'cancelled', 'conflict'].includes(state)) throw invalidResponse('Invalid import state.')
  return {
    importSessionId: requiredString(value.importSessionId, 'importSessionId'),
		jobId: asNonEmptyString(value.jobId),
    name: requiredString(value.name, 'name'),
    description: typeof value.description === 'string' ? value.description : undefined,
    state: state as ImportSession['state'], artifacts: value.artifacts.map(normalizeImportArtifact),
    bytes: requiredNumber(value.bytes, 'bytes'), setupId: asNonEmptyString(value.setupId),
    errorCode: asNonEmptyString(value.errorCode), expiresAt: requiredString(value.expiresAt, 'expiresAt'),
    createdAt: requiredString(value.createdAt, 'createdAt'), updatedAt: requiredString(value.updatedAt, 'updatedAt'),
  }
}

export async function getCatalog(signal?: AbortSignal): Promise<CatalogSnapshot> {
  const value = await apiRequest<unknown>('/api/v1/catalog', { signal })
  if (!isRecord(value) || !isRecord(value.destination)
    || !Array.isArray(value.folders) || !Array.isArray(value.setups)) {
    throw invalidResponse('Invalid catalog response.')
  }
  return {
    destination: {
      rootLabel: requiredString(value.destination.rootLabel, 'destination.rootLabel'),
      rootDisplay: requiredString(value.destination.rootDisplay, 'destination.rootDisplay'),
    },
    generation: requiredString(value.generation, 'catalog generation'),
    folders: value.folders.map(normalizeCatalogFolder),
    setups: value.setups.map(normalizeCatalogSetup),
  }
}

export async function createCatalogFolder(
  parentFolderId: string | undefined,
  name: string,
  key = newIdempotencyKey(),
): Promise<CatalogFolder> {
  return normalizeCatalogFolder(await jsonMutation<unknown>('/api/v1/catalog/folders', 'POST', {
    parentFolderId: parentFolderId ?? null,
    name,
  }, key))
}

export async function updateCatalogFolder(
  folder: CatalogFolder,
  changes: { name?: string; parentFolderId?: string | null },
  key = newIdempotencyKey(),
): Promise<CatalogFolder> {
  return normalizeCatalogFolder(await jsonMutation<unknown>(
    `/api/v1/catalog/folders/${encodeURIComponent(folder.folderId)}`,
    'PATCH',
    { expectedRevision: folder.revision, ...changes },
    key,
  ))
}

export async function deleteCatalogFolder(
  folder: CatalogFolder,
  key = newIdempotencyKey(),
): Promise<void> {
  const query = new URLSearchParams({ expectedRevision: String(folder.revision) })
  await apiRequest<unknown>(`/api/v1/catalog/folders/${encodeURIComponent(folder.folderId)}?${query}`, {
    method: 'DELETE', headers: { 'Idempotency-Key': key },
  })
}

export async function createCatalogSetup(
  input: { folderId?: string; name: string; description?: string },
  key = newIdempotencyKey(),
  signal?: AbortSignal,
): Promise<CatalogSetup> {
  return normalizeCatalogSetup(await jsonMutation<unknown>('/api/v1/catalog/setups', 'POST', {
    folderId: input.folderId ?? null,
    name: input.name,
    description: input.description ?? '',
  }, key, signal))
}

export async function updateCatalogSetup(
  setup: CatalogSetup,
  changes: { name?: string; description?: string; folderId?: string | null },
  key = newIdempotencyKey(),
): Promise<CatalogSetup> {
  return normalizeCatalogSetup(await jsonMutation<unknown>(
    `/api/v1/catalog/setups/${encodeURIComponent(setup.setupId)}`,
    'PATCH',
    { expectedRevision: setup.revision, ...changes },
    key,
  ))
}

export async function deleteCatalogSetup(
  setup: CatalogSetup,
  key = newIdempotencyKey(),
): Promise<void> {
  const query = new URLSearchParams({ expectedRevision: String(setup.revision) })
  await apiRequest<unknown>(`/api/v1/catalog/setups/${encodeURIComponent(setup.setupId)}?${query}`, {
    method: 'DELETE', headers: { 'Idempotency-Key': key },
  })
}

export type CatalogComponent = 'program' | 'setup-sheet'

export function catalogContentURL(setupId: string, component: CatalogComponent): string {
  return `/api/v1/catalog/setups/${encodeURIComponent(setupId)}/${component}/content`
}

export function putCatalogComponent(
  setup: CatalogSetup,
  component: CatalogComponent,
  file: File,
  key = newIdempotencyKey(),
  options: UploadOptions = {},
): Promise<CatalogSetup> {
  const query = new URLSearchParams({ expectedRevision: String(setup.revision) })
  const existing = component === 'program' ? setup.program : setup.setupSheet
  const filePrecondition: Readonly<Record<string, string>> = existing
    ? { 'If-Match': `"${existing.version}"` }
    : { 'If-None-Match': '*' }
  return xhrUpload(
    `/api/v1/catalog/setups/${encodeURIComponent(setup.setupId)}/${component}?${query}`,
    'PUT', file, file.size, key, normalizeCatalogSetup, options,
    file.type || 'application/octet-stream',
    { 'X-File-Name': encodeURIComponent(file.name), ...filePrecondition },
  )
}

export async function deleteCatalogComponent(
  setup: CatalogSetup,
  component: CatalogComponent,
  key = newIdempotencyKey(),
): Promise<CatalogSetup> {
  const query = new URLSearchParams({ expectedRevision: String(setup.revision) })
  const existing = component === 'program' ? setup.program : setup.setupSheet
  if (!existing) {
    throw new ApiError({
      message: 'The setup component is no longer present.',
      status: 409,
      code: 'ARTIFACT_NOT_FOUND',
    })
  }
  return normalizeCatalogSetup(await apiRequest<unknown>(
    `/api/v1/catalog/setups/${encodeURIComponent(setup.setupId)}/${component}?${query}`,
    { method: 'DELETE', headers: {
      'Idempotency-Key': key,
      'If-Match': `"${existing.version}"`,
    } },
  ))
}

export interface SetupQuery {
  query?: string
  statuses?: SetupStatus[]
  hasSetupSheet?: boolean
  current?: boolean
  sort?: string
  cursor?: string
  limit?: number
}

export async function listSetups(options: SetupQuery = {}, signal?: AbortSignal): Promise<{ items: SetupSummary[]; nextCursor?: string }> {
  const query = new URLSearchParams()
  if (options.query) query.set('q', options.query)
  options.statuses?.forEach((status) => query.append('status', status))
  if (options.hasSetupSheet !== undefined) query.set('hasSetupSheet', String(options.hasSetupSheet))
  if (options.current !== undefined) query.set('current', String(options.current))
  if (options.sort) query.set('sort', options.sort)
  if (options.cursor) query.set('cursor', options.cursor)
  if (options.limit) query.set('limit', String(options.limit))
  const body = await apiRequest<unknown>(`/api/v1/setups${query.size > 0 ? `?${query}` : ''}`, { signal })
  if (!isRecord(body) || !Array.isArray(body.items)) throw invalidResponse()
  return { items: body.items.map(normalizeSummary), nextCursor: asNonEmptyString(body.nextCursor) }
}

export interface SetupNameMatch {
  setupId: string
  name: string
}

export async function checkSetupName(name: string, signal?: AbortSignal): Promise<SetupNameMatch | undefined> {
  const query = new URLSearchParams({ name })
  const value = await apiRequest<unknown>(`/api/v1/setups/name-check?${query}`, { signal })
  if (!isRecord(value)) throw invalidResponse()
  if (value.match === null) return undefined
  if (!isRecord(value.match)) throw invalidResponse('Invalid setup name match.')
  return {
    setupId: requiredString(value.match.setupId, 'setupId'),
    name: requiredString(value.match.name, 'name'),
  }
}

export async function getSetup(setupId: string, signal?: AbortSignal): Promise<Setup> {
  return normalizeSetup(await apiRequest<unknown>(`/api/v1/setups/${encodeURIComponent(setupId)}`, { signal }))
}

export async function createSetup(name: string, description: string, key = newIdempotencyKey()): Promise<Setup> {
  return normalizeSetup(await jsonMutation<unknown>('/api/v1/setups', 'POST', { name, description }, key))
}

export async function updateSetup(setupId: string, revision: number, name: string, description: string, key = newIdempotencyKey()): Promise<Setup> {
  return normalizeSetup(await jsonMutation<unknown>(`/api/v1/setups/${encodeURIComponent(setupId)}`, 'PATCH', {
    expectedRevision: revision, name, description,
  }, key))
}

export async function getCurrentSetup(signal?: AbortSignal): Promise<CurrentSetup | null> {
  const value = await apiRequest<unknown>('/api/v1/current-setup', { signal })
  if (value === null) return null
  if (!isRecord(value)) throw invalidResponse()
  return {
    libraryId: requiredString(value.libraryId, 'libraryId'), setupId: requiredString(value.setupId, 'setupId'),
    revisionSelected: requiredNumber(value.revisionSelected, 'revisionSelected'),
    selectedAt: requiredString(value.selectedAt, 'selectedAt'),
  }
}

export async function setCurrentSetup(setupId: string, revision: number, previous: CurrentSetup | null, key = newIdempotencyKey()): Promise<CurrentSetup> {
  const value = await jsonMutation<unknown>('/api/v1/current-setup', 'PUT', {
    setupId, expectedRevision: revision,
    expectedCurrentSetupId: previous?.setupId ?? '',
    expectedCurrentRevision: previous?.revisionSelected ?? 0,
    confirmed: true,
  }, key)
  if (!isRecord(value)) throw invalidResponse()
  return {
    libraryId: requiredString(value.libraryId, 'libraryId'), setupId: requiredString(value.setupId, 'setupId'),
    revisionSelected: requiredNumber(value.revisionSelected, 'revisionSelected'), selectedAt: requiredString(value.selectedAt, 'selectedAt'),
  }
}

export async function clearCurrentSetup(current: CurrentSetup, key = newIdempotencyKey()): Promise<void> {
  await jsonMutation<unknown>('/api/v1/current-setup', 'PUT', {
    setupId: '', expectedRevision: current.revisionSelected,
    expectedCurrentSetupId: current.setupId,
    expectedCurrentRevision: current.revisionSelected,
    confirmed: true,
  }, key)
}

export async function setupAction(setupId: string, action: 'validate' | 'duplicate' | 'archive' | 'restore' | 'delete-plan', revision: number, extra: Record<string, unknown> = {}, key = newIdempotencyKey()): Promise<unknown> {
  return jsonMutation<unknown>(`/api/v1/setups/${encodeURIComponent(setupId)}/${action}`, 'POST', {
    expectedRevision: revision, ...extra,
  }, key)
}

export async function getJob(jobId: string, signal?: AbortSignal): Promise<Job> {
  return normalizeJob(await apiRequest<unknown>(`/api/v1/jobs/${encodeURIComponent(jobId)}`, { signal }))
}

export async function listJobs(limit = 50, signal?: AbortSignal): Promise<Job[]> {
  const query = new URLSearchParams({ limit: String(limit) })
  const value = await apiRequest<unknown>(`/api/v1/jobs?${query}`, { signal })
  if (!isRecord(value) || !Array.isArray(value.items)) throw invalidResponse('Invalid job list response.')
  return value.items.map(normalizeJob)
}

export async function listActiveJobs(setupId: string, signal?: AbortSignal): Promise<Job[]> {
  const query = new URLSearchParams({ setupId, active: 'true' })
  const value = await apiRequest<unknown>(`/api/v1/jobs?${query}`, { signal })
  if (!isRecord(value) || !Array.isArray(value.items)) throw invalidResponse('Invalid active job list response.')
  return value.items.map(normalizeJob)
}

export async function cancelJob(jobId: string, key = newIdempotencyKey()): Promise<Job> {
  return normalizeJob(await apiRequest<unknown>(`/api/v1/jobs/${encodeURIComponent(jobId)}`, {
    method: 'DELETE', headers: { 'Idempotency-Key': key },
  }))
}

export async function waitForJob(job: Job, signal?: AbortSignal): Promise<Job> {
	let current = job
	while (!['succeeded', 'failed', 'cancelled', 'conflict'].includes(current.state)) {
		if (signal?.aborted) throw signal.reason instanceof Error ? signal.reason : new DOMException('Operation aborted', 'AbortError')
		await new Promise<void>((resolve, reject) => {
			const onAbort = () => {
				window.clearTimeout(timeout)
				reject(signal?.reason instanceof Error ? signal.reason : new DOMException('Operation aborted', 'AbortError'))
			}
			const timeout = window.setTimeout(() => {
				signal?.removeEventListener('abort', onAbort)
				resolve()
			}, 250)
			signal?.addEventListener('abort', onAbort, { once: true })
		})
    current = await getJob(current.jobId, signal)
  }
  return current
}

export interface UploadJobHandle {
  job: Job
  transfer: Promise<Job>
}

async function prepareUploadJob(
  setup: Setup,
  operation: 'addPrograms' | 'replaceProgram' | 'putSetupSheet',
  items: Array<{ displayName: string; size: number }>,
  key: string,
  artifact?: Artifact,
): Promise<Job> {
  return normalizeJob(await jsonMutation<unknown>(`/api/v1/setups/${encodeURIComponent(setup.setupId)}/upload-jobs`, 'POST', {
    operation, expectedRevision: setup.revision,
    artifactId: artifact?.artifactId ?? '', expectedVersion: artifact?.version ?? '', items,
  }, key))
}

function runPreparedUpload(job: Job, body: XMLHttpRequestBodyInit, bytes: number, key: string, options: UploadOptions, contentType?: string): Promise<Job> {
  if (['succeeded', 'failed', 'cancelled', 'conflict'].includes(job.state)) return Promise.resolve(job)
  return xhrUpload(`/api/v1/jobs/${encodeURIComponent(job.jobId)}/upload`, 'POST', body, bytes, key, normalizeJob, options, contentType)
}

export async function uploadProgram(setup: Setup, file: File, replace?: Artifact, key = newIdempotencyKey(), options: UploadOptions = {}): Promise<UploadJobHandle> {
  if (replace) {
    const job = await prepareUploadJob(setup, 'replaceProgram', [{ displayName: replace.displayName, size: file.size }], key, replace)
    return { job, transfer: runPreparedUpload(job, file, file.size, key, options, 'application/octet-stream') }
  }
  return uploadPrograms(setup, [{ file, displayName: file.name }], key, options)
}

export async function uploadSetupSheet(setup: Setup, file: File, current?: Artifact, key = newIdempotencyKey(), options: UploadOptions = {}): Promise<UploadJobHandle> {
  const job = await prepareUploadJob(setup, 'putSetupSheet', [{ displayName: file.name, size: file.size }], key, current)
  return { job, transfer: runPreparedUpload(job, file, file.size, key, options, 'application/octet-stream') }
}

export interface ProgramUpload {
  file: File
  displayName: string
}

export async function uploadPrograms(setup: Setup, programs: ProgramUpload[], key = newIdempotencyKey(), options: UploadOptions = {}): Promise<UploadJobHandle> {
  const job = await prepareUploadJob(setup, 'addPrograms', programs.map((item) => ({ displayName: item.displayName, size: item.file.size })), key)
  const form = new FormData()
  form.append('manifest', JSON.stringify({ programs: programs.map((item) => ({ displayName: item.displayName, size: item.file.size })) }))
  for (const program of programs) form.append('program', program.file)
  const total = programs.reduce((sum, item) => sum + item.file.size, 0)
  return { job, transfer: runPreparedUpload(job, form, total, key, options) }
}

export async function mutateProgram(setup: Setup, artifact: Artifact, operation: { displayName: string } | { primary: true }, key = newIdempotencyKey()): Promise<Setup> {
  return normalizeSetup(await jsonMutation<unknown>(`/api/v1/setups/${encodeURIComponent(setup.setupId)}/programs/${encodeURIComponent(artifact.artifactId)}`, 'PATCH', {
    expectedRevision: setup.revision, expectedVersion: artifact.version, ...operation,
  }, key))
}

export interface DeleteArtifactOptions {
  replacementPrimaryArtifactId?: string
  leavePrimaryUnassigned?: boolean
  confirmDeleteLastProgram?: boolean
}

export async function deleteArtifact(setup: Setup, artifact: Artifact, key = newIdempotencyKey(), options: DeleteArtifactOptions = {}): Promise<Setup> {
  const query = new URLSearchParams({ expectedRevision: String(setup.revision), expectedVersion: artifact.version })
  if (options.replacementPrimaryArtifactId) query.set('replacementPrimaryArtifactId', options.replacementPrimaryArtifactId)
  if (options.leavePrimaryUnassigned) query.set('leavePrimaryUnassigned', 'true')
  if (options.confirmDeleteLastProgram) query.set('confirmDeleteLastProgram', 'true')
  const path = artifact.role === 'setup_sheet'
    ? `/api/v1/setups/${encodeURIComponent(setup.setupId)}/setup-sheet?${query}`
    : `/api/v1/setups/${encodeURIComponent(setup.setupId)}/programs/${encodeURIComponent(artifact.artifactId)}?${query}`
  return normalizeSetup(await apiRequest<unknown>(path, { method: 'DELETE', headers: { 'Idempotency-Key': key } }))
}

export async function permanentDelete(setup: Setup, exactName: string, confirmationToken: string, key = newIdempotencyKey()): Promise<void> {
  await jsonMutation<unknown>(`/api/v1/setups/${encodeURIComponent(setup.setupId)}`, 'DELETE', {
    expectedRevision: setup.revision, exactName, confirmationToken,
  }, key)
}

export async function startImport(name: string, description: string, key = newIdempotencyKey()): Promise<ImportSession> {
  return normalizeImportSession(await jsonMutation<unknown>('/api/v1/setup-imports', 'POST', { name, description }, key))
}

export interface ImportPreflightItem {
  clientId: string
  role: ArtifactRole
  displayName: string
}

export interface ImportPreflightResult {
  items: Array<{ clientId: string; displayName?: string; errorCode?: string }>
  collisions: Array<{ clientIds: string[] }>
}

export async function preflightImport(items: ImportPreflightItem[], signal?: AbortSignal): Promise<ImportPreflightResult> {
  const value = await apiRequest<unknown>('/api/v1/setup-imports/preflight', {
    method: 'POST', signal, body: JSON.stringify({ items }), headers: { 'Content-Type': 'application/json' },
  })
  if (!isRecord(value) || !Array.isArray(value.items) || !Array.isArray(value.collisions)) throw invalidResponse('Invalid import preflight response.')
  return {
    items: value.items.map((item) => {
      if (!isRecord(item)) throw invalidResponse('Invalid import preflight item.')
      return {
        clientId: requiredString(item.clientId, 'clientId'),
        displayName: asNonEmptyString(item.displayName), errorCode: asNonEmptyString(item.errorCode),
      }
    }),
    collisions: value.collisions.map((collision) => {
      if (!isRecord(collision) || !Array.isArray(collision.clientIds)) throw invalidResponse('Invalid import collision.')
      return { clientIds: collision.clientIds.map((id) => requiredString(id, 'clientId')) }
    }),
  }
}

export async function getImport(sessionId: string, signal?: AbortSignal): Promise<ImportSession> {
  return normalizeImportSession(await apiRequest<unknown>(`/api/v1/setup-imports/${encodeURIComponent(sessionId)}`, { signal }))
}

export function uploadImportArtifact(
  sessionId: string,
  file: File,
  role: ArtifactRole,
  displayName: string,
  key = newIdempotencyKey(),
  options: UploadOptions = {},
): Promise<ImportArtifact> {
  const query = new URLSearchParams({ role, name: displayName })
  return xhrUpload(`/api/v1/setup-imports/${encodeURIComponent(sessionId)}/artifacts?${query}`, 'POST', file, file.size, key, normalizeImportArtifact, options, 'application/octet-stream')
}

export async function commitImport(sessionId: string, artifacts: ImportArtifact[], primaryArtifactId?: string, savePartialDraft = false, key = newIdempotencyKey()): Promise<Setup> {
  return normalizeSetup(await jsonMutation<unknown>(`/api/v1/setup-imports/${encodeURIComponent(sessionId)}/commit`, 'POST', {
    expectedArtifactIds: artifacts.filter((item) => item.state === 'staged').map((item) => item.importArtifactId),
    primaryArtifactId: primaryArtifactId ?? '', savePartialDraft,
  }, key))
}

export async function cancelImport(sessionId: string, key = newIdempotencyKey()): Promise<ImportSession> {
  return normalizeImportSession(await apiRequest<unknown>(`/api/v1/setup-imports/${encodeURIComponent(sessionId)}`, {
    method: 'DELETE', headers: { 'Idempotency-Key': key },
  }))
}

export async function excludeImportArtifact(sessionId: string, artifactId: string, key = newIdempotencyKey()): Promise<ImportSession> {
  return normalizeImportSession(await apiRequest<unknown>(`/api/v1/setup-imports/${encodeURIComponent(sessionId)}/artifacts/${encodeURIComponent(artifactId)}`, {
    method: 'DELETE', headers: { 'Idempotency-Key': key },
  }))
}

function normalizeRecentSetup(value: unknown): RecentSetup {
  if (!isRecord(value)) throw invalidResponse('Invalid recent setup.')
  const status = String(value.setupStatus)
  if (!['draft', 'ready', 'attention', 'archived'].includes(status)) throw invalidResponse('Invalid recent setup status.')
  return {
    libraryId: requiredString(value.libraryId, 'libraryId'),
    setupId: requiredString(value.setupId, 'setupId'),
    setupName: requiredString(value.setupName, 'setupName'),
    setupStatus: status as SetupStatus,
    lastArtifactId: asNonEmptyString(value.lastArtifactId),
    lastLine: typeof value.lastLine === 'number' && Number.isSafeInteger(value.lastLine) && value.lastLine >= 0 ? value.lastLine : undefined,
    lastOpenedAt: requiredString(value.lastOpenedAt, 'lastOpenedAt'),
  }
}

export async function listRecentSetups(signal?: AbortSignal): Promise<RecentSetup[]> {
  const value = await apiRequest<unknown>('/api/v1/recent-setups', { signal })
  if (!isRecord(value) || !Array.isArray(value.items)) throw invalidResponse('Invalid recent setup list.')
  return value.items.map(normalizeRecentSetup)
}

export async function touchRecentSetup(setupId: string, artifactId = '', line = 0, key = newIdempotencyKey()): Promise<void> {
  await jsonMutation<unknown>(`/api/v1/recent-setups/${encodeURIComponent(setupId)}`, 'PUT', { artifactId, line }, key)
}

export async function deleteRecentSetup(setupId: string, key = newIdempotencyKey()): Promise<void> {
  await apiRequest<unknown>(`/api/v1/recent-setups/${encodeURIComponent(setupId)}`, {
    method: 'DELETE', headers: { 'Idempotency-Key': key },
  })
}

export async function clearRecentSetups(key = newIdempotencyKey()): Promise<void> {
  await apiRequest<unknown>('/api/v1/recent-setups', {
    method: 'DELETE', headers: { 'Idempotency-Key': key },
  })
}

function normalizeUIState(value: unknown): UIState {
  if (!isRecord(value)) throw invalidResponse('Invalid UI state.')
  const screen = value.screen
  if (screen !== 'library' && screen !== 'detail') throw invalidResponse('Invalid UI screen.')
  return {
    clientId: requiredString(value.clientId, 'clientId'),
    screen,
    selectedSetupId: asNonEmptyString(value.selectedSetupId),
    selectedArtifactId: asNonEmptyString(value.selectedArtifactId),
    filters: isRecord(value.filters) ? value.filters : {},
    view: isRecord(value.view) ? value.view : {},
    updatedAt: asNonEmptyString(value.updatedAt),
  }
}

export async function getUIState(clientId: string, signal?: AbortSignal): Promise<UIState> {
  const query = new URLSearchParams({ clientId })
  return normalizeUIState(await apiRequest<unknown>(`/api/v1/ui-state?${query}`, { signal }))
}

export async function putUIState(state: Omit<UIState, 'updatedAt'>, key = newIdempotencyKey()): Promise<UIState> {
  return normalizeUIState(await jsonMutation<unknown>('/api/v1/ui-state', 'PUT', state, key))
}

function normalizeFeatures(value: unknown): Readonly<Record<string, boolean>> {
  if (!isRecord(value)) return {}
  return Object.fromEntries(
    Object.entries(value).filter((entry): entry is [string, boolean] => (
      typeof entry[1] === 'boolean'
    )),
  )
}

function normalizeStringList(value: unknown): string[] {
  if (!Array.isArray(value)) return []
  return value.filter((item): item is string => typeof item === 'string')
}

function normalizeAuthSession(body: unknown): AuthSession {
  const envelope = isRecord(body) ? body : {}
  const root = isRecord(envelope.data) ? envelope.data : envelope
  if (typeof root.authenticated !== 'boolean') throw invalidResponse('Invalid authentication session.')

  const rawLoginRequired = root.loginRequired ?? root.login_required
  if (typeof rawLoginRequired !== 'boolean') throw invalidResponse('Invalid authentication session.')
  if (root.user === undefined) throw invalidResponse('Invalid authentication session user.')

  let user: AuthenticatedUser | null = null
  if (root.user !== null) {
    if (!isRecord(root.user)) throw invalidResponse('Invalid authentication session user.')
    user = { username: requiredString(root.user.username, 'authentication username') }
  }

  const token = asNonEmptyString(root.csrfToken)
    ?? asNonEmptyString(root.csrf_token)
    ?? asNonEmptyString(envelope.csrfToken)
    ?? asNonEmptyString(envelope.csrf_token)

  if (!root.authenticated) {
    if (!rawLoginRequired || user !== null || token) throw invalidResponse('Invalid guest authentication session.')
    return { authenticated: false, loginRequired: true, user: null }
  }
  if (!token || (rawLoginRequired && user === null)) {
    throw invalidResponse('Invalid authenticated session.')
  }

  return {
    authenticated: true,
    loginRequired: rawLoginRequired,
    user,
    csrfToken: token,
  }
}

function adoptAuthSession(body: unknown): AuthSession {
  const session = normalizeAuthSession(body)
  const notifyExpired = authenticatedSession && !session.authenticated
  csrfToken = session.csrfToken
  authenticatedSession = session.authenticated
  if (notifyExpired) unauthorizedHandler?.()
  return session
}

export async function getAuthSession(signal?: AbortSignal): Promise<AuthSession> {
  const response = await apiRequest<unknown>(
    '/api/v1/auth/session',
    { signal },
    { suppressUnauthorized: true },
  )
  return adoptAuthSession(response)
}

export async function login(
  username: string,
  password: string,
  rememberMe: boolean,
  signal?: AbortSignal,
): Promise<AuthSession> {
  const response = await apiRequest<unknown>(
    '/api/v1/auth/login',
    {
      method: 'POST',
      signal,
      body: JSON.stringify({ username, password, rememberMe }),
      headers: { 'Content-Type': 'application/json' },
    },
    { csrf: false, suppressUnauthorized: true },
  )
  return adoptAuthSession(response)
}

export async function logout(signal?: AbortSignal): Promise<void> {
  await apiRequest<void>('/api/v1/auth/logout', { method: 'POST', signal })
  clearApiSession()
}

function normalizeCapabilities(body: unknown): Capabilities {
  const envelope = isRecord(body) ? body : {}
  const root = isRecord(envelope.data) ? envelope.data : envelope
  const library = isRecord(root.library) ? root.library : {}

  const libraryAlias = asNonEmptyString(root.library_alias)
    ?? asNonEmptyString(root.libraryAlias)
    ?? asNonEmptyString(library.alias)
  if (!libraryAlias) {
    throw new ApiError({
      message: 'The service returned invalid capabilities.',
      status: 0,
      code: 'INVALID_RESPONSE',
    })
  }

  const token = asNonEmptyString(root.csrf_token)
    ?? asNonEmptyString(root.csrfToken)
    ?? asNonEmptyString(envelope.csrf_token)
    ?? asNonEmptyString(envelope.csrfToken)

  const requiredSheet = root.require_setup_sheet_for_ready
    ?? root.requireSetupSheetForReady

  return {
    libraryId: asNonEmptyString(root.library_id)
      ?? asNonEmptyString(root.libraryId)
      ?? asNonEmptyString(library.id),
    libraryAlias,
    csrfToken: token,
    gcodeExtensions: normalizeStringList(
      root.gcode_extensions ?? root.gcodeExtensions,
    ),
    requireSetupSheetForReady: typeof requiredSheet === 'boolean'
      ? requiredSheet
      : false,
    features: normalizeFeatures(root.features ?? root.capabilities),
  }
}

export async function getCapabilities(signal?: AbortSignal): Promise<Capabilities> {
  const response = await apiRequest<unknown>('/api/v1/capabilities', { signal })
  const capabilities = normalizeCapabilities(response)
  // The authenticated session is authoritative. Capabilities may retain a
  // compatibility token for implicit-local clients, but must never rotate a
  // cookie principal to an unrelated token.
  if (!authenticatedSession && capabilities.csrfToken) setCsrfToken(capabilities.csrfToken)
  return capabilities
}

export interface Readiness {
  ok: boolean
  code?: string
  message?: string
}

export async function getReadiness(signal?: AbortSignal): Promise<Readiness> {
  let response: Response
  try {
    response = await fetch('/readyz', { signal, credentials: 'same-origin', cache: 'no-store', headers: { Accept: 'application/json' } })
  } catch (cause) {
    if (cause instanceof DOMException && cause.name === 'AbortError') throw cause
    return { ok: false, code: 'BACKEND_UNAVAILABLE', message: 'Локальный Backend недоступен.' }
  }
  if (response.ok) return { ok: true }
  let value: unknown
  try { value = await response.json() as unknown } catch { value = undefined }
  const root = isRecord(value) ? value : {}
  const nested = isRecord(root.error) ? root.error : root
  return {
    ok: false,
    code: asNonEmptyString(nested.code) ?? 'BACKEND_UNAVAILABLE',
    message: asNonEmptyString(nested.message) ?? 'Локальный Backend не готов.',
  }
}

export interface PaginationHints {
  total?: number
  nextCursor?: string
}

export function normalizePaginationHints(value: unknown): PaginationHints {
  const root = isRecord(value) ? value : {}
  return {
    total: asOptionalPositiveNumber(root.total),
    nextCursor: asNonEmptyString(root.next_cursor) ?? asNonEmptyString(root.nextCursor),
  }
}
