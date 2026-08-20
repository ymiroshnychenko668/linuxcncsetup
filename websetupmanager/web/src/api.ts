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

function isRecord(value: unknown): value is JsonRecord {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
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
}

export function clearApiSession(): void {
  csrfToken = undefined
}

export async function apiRequest<T>(
  path: string,
  init: RequestInit = {},
): Promise<T> {
  const method = (init.method ?? 'GET').toUpperCase()
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')

  if (!isSafeMethod(method)) {
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

  const body = await responseBody(response)
  if (!response.ok) throw errorFromResponse(response, body)
  return body as T
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
  setCsrfToken(capabilities.csrfToken)
  return capabilities
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
