export interface AuthenticatedUser {
  username: string
}

export interface AppConfig {
  machineName: string
}

export interface AuthSession {
  authenticated: boolean
  user: AuthenticatedUser | null
  csrfToken?: string
}

export interface TerminalSession {
  id: string
  name: string
  attached: boolean
  windows: number
  createdAt?: string
  terminalConnected: boolean
}

export interface ConnectResult {
  session: TerminalSession
  terminalUrl: string
}

interface ErrorResponse {
  error?: {
    code?: string
    message?: string
  }
  message?: string
}

interface SessionsResponse {
  sessions: TerminalSession[]
}

interface SessionResponse {
  session: TerminalSession
}

interface ConnectResponse {
  session?: TerminalSession
  terminalUrl: string
}

interface ClipboardResponse {
  text?: string
}

interface AuthResponse {
  authenticated?: boolean
  user?: AuthenticatedUser | null
  username?: string
  csrfToken?: string
}

interface ConfigResponse {
  machineName?: string
}

interface RequestOptions {
  csrf?: boolean
  suppressUnauthorized?: boolean
}

export class ApiError extends Error {
  readonly status: number
  readonly code?: string

  constructor(message: string, status: number, code?: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

let csrfToken: string | undefined
let unauthorizedHandler: (() => void) | undefined

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError'
}

function normalizeAuthSession(response: AuthResponse): AuthSession {
  const username = response.user?.username ?? response.username
  const authenticated = response.authenticated ?? Boolean(username)

  if (!authenticated) {
    return { authenticated: false, user: null }
  }

  if (!username || !response.csrfToken) {
    throw new ApiError('The service returned an invalid authentication response.', 0, 'invalid_response')
  }

  return {
    authenticated: true,
    user: { username },
    csrfToken: response.csrfToken,
  }
}

function normalizeTerminalUrl(value: string): string {
  let url: URL
  try {
    url = new URL(value, window.location.origin)
  } catch {
    throw new ApiError('The service returned an invalid terminal address.', 0, 'invalid_response')
  }

  if (url.origin !== window.location.origin || !url.pathname.startsWith('/terminal/')) {
    throw new ApiError('The service returned an unsafe terminal address.', 0, 'invalid_response')
  }

  return `${url.pathname}${url.search}${url.hash}`
}

async function request<T>(path: string, init: RequestInit = {}, options: RequestOptions = {}): Promise<T> {
  const headers = new Headers(init.headers)
  headers.set('Accept', 'application/json')

  if (init.body !== undefined) {
    headers.set('Content-Type', 'application/json')
  }

  if (options.csrf) {
    if (!csrfToken) {
      throw new ApiError('Your sign-in session is no longer available.', 401, 'session_expired')
    }
    headers.set('X-CSRF-Token', csrfToken)
  }

  let response: Response
  try {
    response = await fetch(path, {
      ...init,
      headers,
      credentials: 'same-origin',
    })
  } catch (error) {
    if (isAbortError(error)) throw error
    throw new ApiError('Cannot reach the remote terminal service.', 0, 'network_error')
  }

  if (!response.ok) {
    let detail: ErrorResponse | undefined
    try {
      detail = (await response.json()) as ErrorResponse
    } catch {
      // Reverse proxies can return an empty or non-JSON response.
    }

    if (response.status === 401 && !options.suppressUnauthorized) {
      csrfToken = undefined
      unauthorizedHandler?.()
    }

    throw new ApiError(
      detail?.error?.message ?? detail?.message ?? `Request failed (${response.status}).`,
      response.status,
      detail?.error?.code,
    )
  }

  if (response.status === 204) return undefined as T

  try {
    return (await response.json()) as T
  } catch {
    throw new ApiError('The service returned an invalid response.', response.status, 'invalid_response')
  }
}

function adoptAuthSession(response: AuthResponse): AuthSession {
  const session = normalizeAuthSession(response)
  csrfToken = session.csrfToken
  return session
}

export const api = {
  setUnauthorizedHandler(handler?: () => void) {
    unauthorizedHandler = handler
  },

  clearAuthentication() {
    csrfToken = undefined
  },

  getConfig: (signal?: AbortSignal) =>
    request<ConfigResponse>('/api/config', { signal, cache: 'no-store' }).then((response): AppConfig => {
      const machineName = response.machineName?.trim()
      if (!machineName) {
        throw new ApiError('The service returned an invalid machine configuration.', 0, 'invalid_response')
      }
      return { machineName }
    }),

  getAuthSession: (signal?: AbortSignal) =>
    request<AuthResponse>(
      '/api/auth/session',
      { signal, cache: 'no-store' },
      { suppressUnauthorized: true },
    ).then(adoptAuthSession),

  login: (username: string, password: string, signal?: AbortSignal) =>
    request<AuthResponse>(
      '/api/auth/login',
      {
        method: 'POST',
        body: JSON.stringify({ username, password }),
        signal,
      },
      { suppressUnauthorized: true },
    ).then(adoptAuthSession),

  logout: () =>
    request<void>('/api/auth/logout', { method: 'POST' }, { csrf: true }).then(() => {
      csrfToken = undefined
    }),

  getSessions: (signal?: AbortSignal) =>
    request<SessionsResponse>('/api/sessions', { signal, cache: 'no-store' }).then(
      (response) => response.sessions,
    ),

  createSession: (name: string) =>
    request<SessionResponse>(
      '/api/sessions',
      {
        method: 'POST',
        body: JSON.stringify({ name }),
      },
      { csrf: true },
    ).then((response) => response.session),

  connectSession: (id: string, signal?: AbortSignal) =>
    request<ConnectResponse>(
      `/api/sessions/${encodeURIComponent(id)}/connect`,
      { method: 'POST', signal },
      { csrf: true },
    ).then((response): ConnectResult => {
      const session = response.session
      if (!session) {
        throw new ApiError('The service did not identify the connected session.', 0, 'invalid_response')
      }
      return { session, terminalUrl: normalizeTerminalUrl(response.terminalUrl) }
    }),

  getLatestSelection: (id: string, signal?: AbortSignal) =>
    request<ClipboardResponse>(
      `/api/sessions/${encodeURIComponent(id)}/clipboard`,
      { signal, cache: 'no-store' },
    ).then((response) => {
      if (typeof response.text !== 'string' || response.text.length === 0) {
        throw new ApiError('The service returned an invalid terminal selection.', 0, 'invalid_response')
      }
      return response.text
    }),

  deleteSession: (id: string) =>
    request<void>(`/api/sessions/${encodeURIComponent(id)}`, { method: 'DELETE' }, { csrf: true }),
}
