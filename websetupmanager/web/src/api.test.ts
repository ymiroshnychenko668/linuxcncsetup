import { afterEach, describe, expect, it, vi } from 'vitest'
import {
	activateExplicitAuthSession,
	acceptExplicitAuthSession,
  ApiError,
  apiRequest,
  clearApiSession,
  getCapabilities,
	getAuthSession,
	getImport,
  checkSetupName,
  getReadiness,
  getUIState,
  listRecentSetups,
	listActiveJobs,
	preflightImport,
  putUIState,
  setCsrfToken,
	setUnauthorizedHandler,
	login,
	logout,
	logoutSessionIfCurrent,
	quarantineExplicitAuthSession,
	reconcileStaleAuthSession,
	uploadSetupSheet,
} from './api'

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
}

class AuthMemoryCache {
  readonly values = new Map<string, Response>()

  match(request: RequestInfo | URL): Promise<Response | undefined> {
    const key = request instanceof Request ? request.url : String(request)
    return Promise.resolve(this.values.get(key)?.clone())
  }

  put(request: RequestInfo | URL, response: Response): Promise<void> {
    const key = request instanceof Request ? request.url : String(request)
    this.values.set(key, response.clone())
    return Promise.resolve()
  }

  keys(): Promise<readonly Request[]> {
    return Promise.resolve([...this.values.keys()].map((url) => new Request(url)))
  }

  delete(request: RequestInfo | URL): Promise<boolean> {
    const key = request instanceof Request ? request.url : String(request)
    return Promise.resolve(this.values.delete(key))
  }
}

function installSerialAuthWebLocks(): () => void {
  const original = Object.getOwnPropertyDescriptor(navigator, 'locks')
  let tail = Promise.resolve()
  Object.defineProperty(navigator, 'locks', {
    configurable: true,
    value: {
      request: vi.fn(async (_name: string, _options: LockOptions, operation: () => Promise<unknown>) => {
        let release!: () => void
        const previous = tail
        tail = new Promise<void>((resolve) => { release = resolve })
        await previous
        try {
          return await operation()
        } finally {
          release()
        }
      }),
    },
  })
  return () => {
    if (original) Object.defineProperty(navigator, 'locks', original)
    else Reflect.deleteProperty(navigator, 'locks')
  }
}

afterEach(() => {
  clearApiSession()
  setUnauthorizedHandler(undefined)
  try {
    window.localStorage.removeItem('web-setup-manager.stale-auth-session.v1')
    for (let index = window.localStorage.length - 1; index >= 0; index -= 1) {
      const key = window.localStorage.key(index)
      if (key?.startsWith('web-setup-manager.stale-auth-session.v2.')) window.localStorage.removeItem(key)
    }
  } catch { /* denied */ }
  vi.unstubAllGlobals()
})

describe('API client', () => {
  it('normalizes guest, remote and implicit-local authentication sessions', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ authenticated: false, loginRequired: true, user: null }))
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true, loginRequired: true,
        user: { username: 'operator' }, csrfToken: 'remote-csrf',
      }))
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true, loginRequired: false, user: null, csrfToken: 'local-csrf',
      }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(getAuthSession()).resolves.toEqual({
      authenticated: false, loginRequired: true, user: null,
    })
    await expect(getAuthSession()).resolves.toMatchObject({
      authenticated: true, loginRequired: true, user: { username: 'operator' }, csrfToken: 'remote-csrf',
    })
    await expect(getAuthSession()).resolves.toEqual({
      authenticated: true, loginRequired: false, user: null, csrfToken: 'local-csrf',
    })
    expect(fetchMock.mock.calls.every(([, init]) => (init as RequestInit).credentials === 'same-origin')).toBe(true)
  })

  it('posts PAM credentials without CSRF or browser storage and protects logout with session CSRF', async () => {
    const localStorageLength = localStorage.length
    const sessionStorageLength = sessionStorage.length
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true, loginRequired: true,
        user: { username: 'operator' }, csrfToken: 'login-csrf',
      }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(login('operator', 'system-secret', true)).resolves.toMatchObject({
      authenticated: true, user: { username: 'operator' },
    })
    await logout()

    const [, loginInit] = fetchMock.mock.calls[0] as [string, RequestInit]
    const [, logoutInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(loginInit).toMatchObject({ method: 'POST', credentials: 'same-origin' })
    expect(loginInit.body).toBe(JSON.stringify({ username: 'operator', password: 'system-secret', rememberMe: true }))
    expect(new Headers(loginInit.headers).has('X-CSRF-Token')).toBe(false)
    expect(new Headers(logoutInit.headers).get('X-CSRF-Token')).toBe('login-csrf')
    expect(localStorage).toHaveLength(localStorageLength)
    expect(sessionStorage).toHaveLength(sessionStorageLength)
  })

	it('never refreshes and retries a conditional stale-session revocation', async () => {
		setCsrfToken('stale-login-csrf')
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
			error: { code: 'CSRF_REJECTED', message: 'The session token does not match.' },
		}, { status: 403 }))
		vi.stubGlobal('fetch', fetchMock)

		expect(await logoutSessionIfCurrent('stale-login-csrf')).toBe(false)
		expect(fetchMock).toHaveBeenCalledTimes(1)
		const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
		expect(url).toBe('/api/v1/auth/revoke-stale')
		expect(new Headers(init.headers).get('X-CSRF-Token')).toBe('stale-login-csrf')
		expect(await logoutSessionIfCurrent('different-csrf')).toBe(false)
		expect(fetchMock).toHaveBeenCalledTimes(1)
		const fresh = { authenticated: true, loginRequired: true, user: { username: 'operator' }, csrfToken: 'fresh-csrf' } as const
		const proof = await quarantineExplicitAuthSession(fresh)
		expect(proof).toBeDefined()
		await acceptExplicitAuthSession(fresh, proof)
	})

	it('activates only the exact sealed session through the CSRF-bound endpoint', async () => {
		const session = {
			authenticated: true,
			loginRequired: true,
			user: { username: 'operator' },
			csrfToken: 'raw-csrf-must-not-enter-the-cookie',
		} as const
		const proof = await quarantineExplicitAuthSession(session)
		if (!proof) throw new Error('expected a durable quarantine proof')
		const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
		vi.stubGlobal('fetch', fetchMock)
		try {
			expect(await activateExplicitAuthSession(session, proof)).toBe(true)
			expect(fetchMock).toHaveBeenCalledTimes(1)
			const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
			expect(url).toBe('/api/v1/auth/activate')
			expect(new Headers(init.headers).get('X-CSRF-Token')).toBe(session.csrfToken)
		} finally {
			await acceptExplicitAuthSession(session, proof)
		}
	})

	it('keeps an unconfirmed stale login quarantined across a session probe', async () => {
		setCsrfToken('late-login-csrf')
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
			error: { code: 'AUTHENTICATION_UNAVAILABLE', message: 'Try again.' },
		}, { status: 503 }))
		vi.stubGlobal('fetch', fetchMock)
		expect(await logoutSessionIfCurrent('late-login-csrf')).toBe(false)
		expect(await reconcileStaleAuthSession({
			authenticated: true,
			loginRequired: true,
			user: { username: 'operator' },
			csrfToken: 'late-login-csrf',
		})).toBe(false)
		expect(fetchMock).toHaveBeenCalledTimes(2)

		const fresh = {
			authenticated: true,
			loginRequired: true,
			user: { username: 'operator' },
			csrfToken: 'fresh-login-csrf',
		} as const
		const proof = await quarantineExplicitAuthSession(fresh)
		expect(proof).toBeDefined()
		await acceptExplicitAuthSession(fresh, proof)
	})

	it('keeps a stale explicit-login session quarantined in Cache Storage when localStorage is unavailable', async () => {
		const memoryCache = new AuthMemoryCache()
		const restoreLocks = installSerialAuthWebLocks()
		vi.stubGlobal('caches', {
			open: vi.fn().mockResolvedValue(memoryCache),
		})
		const unavailableStorage = {
			getItem: vi.fn(() => { throw new DOMException('Storage denied', 'SecurityError') }),
			setItem: vi.fn(() => { throw new DOMException('Storage denied', 'SecurityError') }),
			removeItem: vi.fn(() => { throw new DOMException('Storage denied', 'SecurityError') }),
		} as unknown as Storage
		const storageDescriptor = Object.getOwnPropertyDescriptor(window, 'localStorage')
		Object.defineProperty(window, 'localStorage', { configurable: true, value: unavailableStorage })

		const staleToken = 'late-login-csrf-must-never-be-persisted'
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
			error: { code: 'AUTHENTICATION_UNAVAILABLE', message: 'Try again.' },
		}, { status: 503 }))
		vi.stubGlobal('fetch', fetchMock)
		try {
			setCsrfToken(staleToken)
			expect(await logoutSessionIfCurrent(staleToken)).toBe(false)

			const persisted = [...memoryCache.values.values()][0]
			if (!persisted) throw new Error('expected a durable quarantine marker')
			const persistedText = await persisted.clone().text()
			expect(persistedText).not.toContain(staleToken)
			const persistedRecord: unknown = JSON.parse(persistedText)
			if (!persistedRecord || typeof persistedRecord !== 'object') throw new Error('invalid durable marker')
			const marker = (persistedRecord as { marker?: unknown }).marker
			expect((persistedRecord as { schema?: unknown }).schema).toBe(1)
			if (typeof marker !== 'string') throw new Error('invalid durable marker')
			expect(marker).toMatch(/^(?:unknown|sha256:[0-9a-f]{64})$/)

			// A new module instance models a tab reload: its realm-local fallback is empty,
			// while the origin's Cache Storage survives.
			vi.resetModules()
			const reloaded = await import('./api')
			reloaded.setCsrfToken(staleToken)
			expect(await reloaded.reconcileStaleAuthSession({
				authenticated: true,
				loginRequired: true,
				user: { username: 'operator' },
				csrfToken: staleToken,
			})).toBe(false)
			expect(fetchMock).toHaveBeenCalledTimes(2)

			const fresh = {
				authenticated: true,
				loginRequired: true,
				user: { username: 'operator' },
				csrfToken: 'rotated-login-csrf',
			} as const
			const proof = await reloaded.quarantineExplicitAuthSession(fresh)
			expect(proof).toBeDefined()
			await reloaded.acceptExplicitAuthSession(fresh, proof)
			expect(memoryCache.values).toHaveLength(0)

			// Also clear the statically imported module's realm-local fallback.
			await acceptExplicitAuthSession(fresh, proof)
			reloaded.clearApiSession()
		} finally {
			restoreLocks()
			if (storageDescriptor) Object.defineProperty(window, 'localStorage', storageDescriptor)
		}
	})

	it('refuses to issue an explicit-login proof when neither durable store can seal it', async () => {
		vi.stubGlobal('caches', { open: vi.fn().mockRejectedValue(new DOMException('Cache denied', 'SecurityError')) })
		const unavailableStorage = {
			getItem: vi.fn(() => { throw new DOMException('Storage denied', 'SecurityError') }),
			setItem: vi.fn(() => { throw new DOMException('Storage denied', 'SecurityError') }),
			removeItem: vi.fn(() => { throw new DOMException('Storage denied', 'SecurityError') }),
		} as unknown as Storage
		const storageDescriptor = Object.getOwnPropertyDescriptor(window, 'localStorage')
		Object.defineProperty(window, 'localStorage', { configurable: true, value: unavailableStorage })
		const session = {
			authenticated: true,
			loginRequired: true,
			user: { username: 'operator' },
			csrfToken: 'unsealed-session',
		} as const
		try {
			await expect(quarantineExplicitAuthSession(session)).resolves.toBeUndefined()
			setCsrfToken(session.csrfToken)
			vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 })))
			await logoutSessionIfCurrent(session.csrfToken)
		} finally {
			if (storageDescriptor) Object.defineProperty(window, 'localStorage', storageDescriptor)
		}
	})

	it('clears the durable stale-session quarantine after confirmed revocation', async () => {
		const memoryCache = new AuthMemoryCache()
		const restoreLocks = installSerialAuthWebLocks()
		vi.stubGlobal('caches', { open: vi.fn().mockResolvedValue(memoryCache) })
		const fetchMock = vi.fn()
			.mockResolvedValueOnce(jsonResponse({
				error: { code: 'AUTHENTICATION_UNAVAILABLE', message: 'Try again.' },
			}, { status: 503 }))
			.mockResolvedValueOnce(new Response(null, { status: 204 }))
		vi.stubGlobal('fetch', fetchMock)

		try {
			setCsrfToken('eventually-revoked-csrf')
			expect(await logoutSessionIfCurrent('eventually-revoked-csrf')).toBe(false)
			expect(memoryCache.values).toHaveLength(1)
			expect(await logoutSessionIfCurrent('eventually-revoked-csrf')).toBe(true)
			expect(memoryCache.values).toHaveLength(0)
		} finally {
			restoreLocks()
		}
	})

	it('removes only stale marker A when revocation A completes after concurrent failure B', async () => {
		const memoryCache = new AuthMemoryCache()
		const restoreLocks = installSerialAuthWebLocks()
		vi.stubGlobal('caches', { open: vi.fn().mockResolvedValue(memoryCache) })
		let finishARevocation!: (response: Response) => void
		const delayedARevocation = new Promise<Response>((resolve) => { finishARevocation = resolve })
		let aCalls = 0
		const unavailable = () => jsonResponse({
			error: { code: 'AUTHENTICATION_UNAVAILABLE', message: 'Try again.' },
		}, { status: 503 })
		const fetchMock = vi.fn((_url: RequestInfo | URL, init?: RequestInit) => {
			const token = new Headers(init?.headers).get('X-CSRF-Token')
			if (token === 'session-a') {
				aCalls += 1
				return Promise.resolve(aCalls === 1 ? unavailable() : delayedARevocation)
			}
			return Promise.resolve(unavailable())
		})
		vi.stubGlobal('fetch', fetchMock)

		try {
			vi.resetModules()
			const tabA = await import('./api')
			vi.resetModules()
			const tabB = await import('./api')
			tabA.setCsrfToken('session-a')
			tabB.setCsrfToken('session-b')

			expect(await tabA.logoutSessionIfCurrent('session-a')).toBe(false)
			const finishingA = tabA.logoutSessionIfCurrent('session-a')
			expect(await tabB.logoutSessionIfCurrent('session-b')).toBe(false)
			finishARevocation(new Response(null, { status: 204 }))
			expect(await finishingA).toBe(true)

			expect(memoryCache.values).toHaveLength(1)
			const remaining = [...memoryCache.values.values()][0]
			if (!remaining) throw new Error('expected marker B to remain durable')
			const remainingText = await remaining.clone().text()
			expect(remainingText).not.toMatch(/session-[ab]/)

			// A fresh realm still rejects B, proving A's late completion did not clear it.
			vi.resetModules()
			const reloaded = await import('./api')
			reloaded.setCsrfToken('session-b')
			expect(await reloaded.reconcileStaleAuthSession({
				authenticated: true,
				loginRequired: true,
				user: { username: 'operator' },
				csrfToken: 'session-b',
			})).toBe(false)

			const fresh = {
				authenticated: true,
				loginRequired: true,
				user: { username: 'operator' },
				csrfToken: 'fresh-session-c',
			} as const
			const proof = await tabB.quarantineExplicitAuthSession(fresh)
			expect(proof).toBeDefined()
			await tabB.acceptExplicitAuthSession(fresh, proof)
			tabA.clearApiSession()
			tabB.clearApiSession()
			reloaded.clearApiSession()
		} finally {
			restoreLocks()
		}
	})

	it('finalizes only the explicit-login snapshot and preserves a later marker B', async () => {
		const memoryCache = new AuthMemoryCache()
		const restoreLocks = installSerialAuthWebLocks()
		vi.stubGlobal('caches', { open: vi.fn().mockResolvedValue(memoryCache) })
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
			error: { code: 'AUTHENTICATION_UNAVAILABLE', message: 'Try again.' },
		}, { status: 503 })))

		try {
			vi.resetModules()
			const tabA = await import('./api')
			vi.resetModules()
			const tabB = await import('./api')
			vi.resetModules()
			const tabC = await import('./api')
			tabA.setCsrfToken('stale-session-a')
			tabB.setCsrfToken('stale-session-b')
			expect(await tabA.logoutSessionIfCurrent('stale-session-a')).toBe(false)

			const freshC = {
				authenticated: true,
				loginRequired: true,
				user: { username: 'operator' },
				csrfToken: 'fresh-session-c',
			} as const
			const proofC = await tabC.quarantineExplicitAuthSession(freshC)
			expect(proofC).toBeDefined()
			expect(memoryCache.values).toHaveLength(2)

			// B is journaled after C captured its proof and must not be swept by finalize(C).
			expect(await tabB.logoutSessionIfCurrent('stale-session-b')).toBe(false)
			expect(memoryCache.values).toHaveLength(3)
			await tabC.acceptExplicitAuthSession(freshC, proofC)
			expect(memoryCache.values).toHaveLength(1)

			vi.resetModules()
			const reloaded = await import('./api')
			reloaded.setCsrfToken('stale-session-b')
			expect(await reloaded.reconcileStaleAuthSession({
				authenticated: true,
				loginRequired: true,
				user: { username: 'operator' },
				csrfToken: 'stale-session-b',
			})).toBe(false)
			tabA.clearApiSession()
			tabB.clearApiSession()
			tabC.clearApiSession()
			reloaded.clearApiSession()
		} finally {
			restoreLocks()
		}
	})

	it('keeps per-fingerprint localStorage markers race-safe without Web Locks or Cache Storage', async () => {
		const locksDescriptor = Object.getOwnPropertyDescriptor(navigator, 'locks')
		Reflect.deleteProperty(navigator, 'locks')
		vi.stubGlobal('caches', undefined)
		let finishARevocation!: (response: Response) => void
		const delayedARevocation = new Promise<Response>((resolve) => { finishARevocation = resolve })
		let aCalls = 0
		const unavailable = () => jsonResponse({
			error: { code: 'AUTHENTICATION_UNAVAILABLE', message: 'Try again.' },
		}, { status: 503 })
		vi.stubGlobal('fetch', vi.fn((_url: RequestInfo | URL, init?: RequestInit) => {
			const token = new Headers(init?.headers).get('X-CSRF-Token')
			if (token === 'local-session-a') {
				aCalls += 1
				return Promise.resolve(aCalls === 1 ? unavailable() : delayedARevocation)
			}
			return Promise.resolve(unavailable())
		}))

		try {
			vi.resetModules()
			const tabA = await import('./api')
			vi.resetModules()
			const tabB = await import('./api')
			tabA.setCsrfToken('local-session-a')
			tabB.setCsrfToken('local-session-b')
			expect(await tabA.logoutSessionIfCurrent('local-session-a')).toBe(false)
			const finishingA = tabA.logoutSessionIfCurrent('local-session-a')
			expect(await tabB.logoutSessionIfCurrent('local-session-b')).toBe(false)
			finishARevocation(new Response(null, { status: 204 }))
			expect(await finishingA).toBe(true)

			const markerKeys = Array.from({ length: localStorage.length }, (_, index) => localStorage.key(index))
				.filter((key): key is string => key?.startsWith('web-setup-manager.stale-auth-session.v2.') === true)
			expect(markerKeys).toHaveLength(1)
			expect(`${markerKeys[0]}:${localStorage.getItem(markerKeys[0])}`).not.toMatch(/local-session-[ab]/)

			vi.resetModules()
			const reloaded = await import('./api')
			reloaded.setCsrfToken('local-session-b')
			expect(await reloaded.reconcileStaleAuthSession({
				authenticated: true,
				loginRequired: true,
				user: { username: 'operator' },
				csrfToken: 'local-session-b',
			})).toBe(false)

			const fresh = {
				authenticated: true,
				loginRequired: true,
				user: { username: 'operator' },
				csrfToken: 'local-session-c',
			} as const
			const proof = await tabB.quarantineExplicitAuthSession(fresh)
			expect(proof).toBeDefined()
			await tabB.acceptExplicitAuthSession(fresh, proof)
			tabA.clearApiSession()
			tabB.clearApiSession()
			reloaded.clearApiSession()
		} finally {
			if (locksDescriptor) Object.defineProperty(navigator, 'locks', locksDescriptor)
		}
	})

  it('keeps session CSRF authoritative when capabilities include a compatibility token', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true, loginRequired: true,
        user: { username: 'operator' }, csrfToken: 'session-csrf',
      }))
      .mockResolvedValueOnce(jsonResponse({
        library_alias: 'Сетапы', csrf_token: 'unrelated-global-token',
        gcode_extensions: ['.ngc'], require_setup_sheet_for_ready: false, features: {},
      }))
      .mockResolvedValueOnce(jsonResponse({ accepted: true }))
    vi.stubGlobal('fetch', fetchMock)

    await getAuthSession()
    await getCapabilities()
    await apiRequest('/api/v1/setups', { method: 'POST', body: '{}' })

    const [, mutationInit] = fetchMock.mock.calls[2] as [string, RequestInit]
    expect(new Headers(mutationInit.headers).get('X-CSRF-Token')).toBe('session-csrf')
  })

  it('expires an authenticated session on fetch 401 and notifies once', async () => {
    const unauthorized = vi.fn()
    setCsrfToken('active-csrf')
    setUnauthorizedHandler(unauthorized)
    vi.stubGlobal('fetch', vi.fn().mockImplementation(() => Promise.resolve(jsonResponse({
      error: { code: 'AUTHENTICATION_REQUIRED', message: 'Authentication is required.' },
    }, { status: 401 }))))

    await expect(apiRequest('/api/v1/setups')).rejects.toMatchObject({ status: 401 })
    await expect(apiRequest('/api/v1/setups')).rejects.toMatchObject({ status: 401 })
    expect(unauthorized).toHaveBeenCalledTimes(1)
    await expect(apiRequest('/api/v1/setups', { method: 'POST' })).rejects.toMatchObject({
      code: 'CSRF_TOKEN_MISSING',
    })
  })

  it('refreshes and retries exactly once only for dedicated CSRF_REJECTED', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true, loginRequired: true,
        user: { username: 'operator' }, csrfToken: 'old-csrf',
      }))
      .mockResolvedValueOnce(jsonResponse({
        error: { code: 'CSRF_REJECTED', message: 'Refresh the request token.' },
      }, { status: 403 }))
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true, loginRequired: true,
        user: { username: 'operator' }, csrfToken: 'new-csrf',
      }))
      .mockResolvedValueOnce(jsonResponse({ accepted: true }))
    vi.stubGlobal('fetch', fetchMock)

    await getAuthSession()
    await expect(apiRequest('/api/v1/setups', { method: 'POST', body: '{}' })).resolves.toEqual({ accepted: true })
    expect(fetchMock).toHaveBeenCalledTimes(4)
    expect(new Headers((fetchMock.mock.calls[1][1] as RequestInit).headers).get('X-CSRF-Token')).toBe('old-csrf')
    expect(new Headers((fetchMock.mock.calls[3][1] as RequestInit).headers).get('X-CSRF-Token')).toBe('new-csrf')
  })

  it('does not refresh a generic forbidden mutation', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      error: { code: 'REQUEST_FORBIDDEN', message: 'Origin rejected.' },
    }, { status: 403 }))
    vi.stubGlobal('fetch', fetchMock)
    setCsrfToken('token')

    await expect(apiRequest('/api/v1/setups', { method: 'POST' })).rejects.toMatchObject({
      status: 403, code: 'REQUEST_FORBIDDEN',
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('expires the same session when a streaming XHR upload receives 401', async () => {
    class FakeXMLHttpRequest extends EventTarget {
      static latest: FakeXMLHttpRequest
      readonly upload = new EventTarget()
      readonly headers = new Map<string, string>()
      status = 0
      responseText = ''
      withCredentials = false

      constructor() {
        super()
        FakeXMLHttpRequest.latest = this
      }

      open(): void {}
      send(): void {}
      abort(): void { this.dispatchEvent(new Event('abort')) }
      setRequestHeader(name: string, value: string): void { this.headers.set(name, value) }
      getResponseHeader(): string | null { return null }
      respond(status: number, body: unknown): void {
        this.status = status
        this.responseText = JSON.stringify(body)
        this.dispatchEvent(new Event('load'))
      }
    }

    const unauthorized = vi.fn()
    setCsrfToken('upload-csrf')
    setUnauthorizedHandler(unauthorized)
    vi.stubGlobal('XMLHttpRequest', FakeXMLHttpRequest)
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
      jobId: 'job-upload', kind: 'put_setup_sheet', setupId: 'setup-1', state: 'running',
      progress: { completedBytes: 0, totalBytes: 1, completedItems: 0, totalItems: 1 },
      createdAt: '2026-08-20T00:00:00Z',
    })))
    const setup: Parameters<typeof uploadSetupSheet>[0] = {
      setupId: 'setup-1', libraryId: 'library-1', name: 'Fixture', description: '',
      status: 'draft', revision: 1, source: 'created', notReadyReasons: [], artifacts: [],
      createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z',
    }

    const upload = await uploadSetupSheet(setup, new File(['x'], 'sheet.pdf'), undefined, 'upload-key')
    FakeXMLHttpRequest.latest.respond(401, {
      error: { code: 'AUTHENTICATION_REQUIRED', message: 'Authentication is required.' },
    })

    await expect(upload.transfer).rejects.toMatchObject({ status: 401 })
    expect(FakeXMLHttpRequest.latest.withCredentials).toBe(true)
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })

  it('uses the backend canonical name check and validates its bounded result', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ match: { setupId: 'setup-sigma', name: 'ς' } }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(checkSetupName('Σ')).resolves.toEqual({ setupId: 'setup-sigma', name: 'ς' })
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/setups/name-check?name=%CE%A3')
  })

  it('normalizes capabilities and reuses its CSRF token from memory', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({
        library_id: 'lib-01',
        library_alias: 'Сетапы станка № 4',
        csrf_token: 'session-token',
        gcode_extensions: ['.ngc', '.tap'],
        require_setup_sheet_for_ready: true,
        features: { imports: true, unknown: 'ignored' },
      }))
      .mockResolvedValueOnce(jsonResponse({ accepted: true }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(getCapabilities()).resolves.toEqual({
      libraryId: 'lib-01',
      libraryAlias: 'Сетапы станка № 4',
      csrfToken: 'session-token',
      gcodeExtensions: ['.ngc', '.tap'],
      requireSetupSheetForReady: true,
      features: { imports: true },
    })
    await apiRequest('/api/v1/setups', { method: 'POST', body: '{}' })

    const [, mutationInit] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(mutationInit.credentials).toBe('same-origin')
    expect(new Headers(mutationInit.headers).get('X-CSRF-Token')).toBe('session-token')
    expect(localStorage).toHaveLength(0)
    expect(sessionStorage).toHaveLength(0)
  })

  it('exposes stable server error fields', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
      error: {
        code: 'REVISION_CONFLICT',
        message: 'Карточка уже изменилась.',
        request_id: 'req-42',
        details: { current_revision: 3 },
        retryable: false,
      },
    }, { status: 409 })))

    const result = apiRequest('/api/v1/setups/setup-1')
    await expect(result).rejects.toMatchObject({
      name: 'ApiError',
      status: 409,
      code: 'REVISION_CONFLICT',
      message: 'Карточка уже изменилась.',
      requestId: 'req-42',
      details: { current_revision: 3 },
      retryable: false,
    })
  })

  it('rejects unsafe API paths before making a request', async () => {
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)

    await expect(apiRequest('https://example.test/api/v1/setups')).rejects.toMatchObject({
      code: 'INVALID_API_PATH',
    })
    await expect(apiRequest('/api/v1/../../secret')).rejects.toBeInstanceOf(ApiError)
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('requires an in-memory CSRF token for mutations', async () => {
    vi.stubGlobal('fetch', vi.fn())
    await expect(apiRequest('/api/v1/setups', { method: 'POST' })).rejects.toMatchObject({
      code: 'CSRF_TOKEN_MISSING',
    })

    setCsrfToken('token')
    clearApiSession()
    await expect(apiRequest('/api/v1/setups', { method: 'DELETE' })).rejects.toMatchObject({
      code: 'CSRF_TOKEN_MISSING',
    })
  })

  it('turns a connection failure into a retryable API error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('connection refused')))
    await expect(getCapabilities()).rejects.toMatchObject({
      code: 'NETWORK_ERROR',
      status: 0,
      retryable: true,
    })
  })

  it('normalizes recent and persisted UI state without path-shaped fields', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse({ items: [{
        libraryId: 'library-1', setupId: 'setup-1', setupName: 'Part', setupStatus: 'draft',
        lastArtifactId: 'artifact-1', lastLine: 12, lastOpenedAt: '2026-08-20T00:00:00Z',
      }] }))
      .mockResolvedValueOnce(jsonResponse({
        clientId: 'web:test', screen: 'detail', selectedSetupId: 'setup-1', selectedArtifactId: 'artifact-1',
        filters: { query: 'part' }, view: { line: 12 }, updatedAt: '2026-08-20T00:00:00Z',
      }))
      .mockResolvedValueOnce(jsonResponse({
        clientId: 'web:test', screen: 'library', filters: {}, view: {}, updatedAt: '2026-08-20T00:01:00Z',
      }))
    vi.stubGlobal('fetch', fetchMock)
    setCsrfToken('token')
    await expect(listRecentSetups()).resolves.toEqual([expect.objectContaining({ setupId: 'setup-1', lastArtifactId: 'artifact-1', lastLine: 12 })])
    await expect(getUIState('web:test')).resolves.toMatchObject({ screen: 'detail', selectedSetupId: 'setup-1', selectedArtifactId: 'artifact-1' })
    await expect(putUIState({ clientId: 'web:test', screen: 'library', filters: {}, view: {} }, 'ui-key')).resolves.toMatchObject({ screen: 'library' })
    const [, uiInit] = fetchMock.mock.calls[2] as [string, RequestInit]
    expect(new Headers(uiInit.headers).get('Idempotency-Key')).toBe('ui-key')
  })

  it('distinguishes managed storage readiness from a network outage', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValueOnce(jsonResponse({
      error: { code: 'STORAGE_UNAVAILABLE', message: 'Managed storage is unavailable.' },
    }, { status: 503 })).mockRejectedValueOnce(new TypeError('connection refused')))
    await expect(getReadiness()).resolves.toMatchObject({ ok: false, code: 'STORAGE_UNAVAILABLE' })
    await expect(getReadiness()).resolves.toMatchObject({ ok: false, code: 'BACKEND_UNAVAILABLE' })
  })

	it('exposes the persistent import job ID for polling and cancellation', async () => {
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
			importSessionId: 'import-1', jobId: 'job-1', name: 'Fixture', state: 'staging',
			artifacts: [], bytes: 0, expiresAt: '2026-08-21T00:00:00Z',
			createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z',
		})))
		await expect(getImport('import-1')).resolves.toMatchObject({ importSessionId: 'import-1', jobId: 'job-1' })
	})

	it('uses the backend Unicode import preflight without exposing canonical keys', async () => {
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
			items: [
				{ clientId: 'a', displayName: 'Straße.ngc' },
				{ clientId: 'b', displayName: 'STRASSE.ngc' },
			],
			collisions: [{ clientIds: ['a', 'b'] }],
		}))
		vi.stubGlobal('fetch', fetchMock)
		setCsrfToken('token')

		const result = await preflightImport([
			{ clientId: 'a', role: 'program', displayName: 'Straße.ngc' },
			{ clientId: 'b', role: 'program', displayName: 'STRASSE.ngc' },
		])
		expect(result).toEqual({
			items: [
				{ clientId: 'a', displayName: 'Straße.ngc', errorCode: undefined },
				{ clientId: 'b', displayName: 'STRASSE.ngc', errorCode: undefined },
			],
			collisions: [{ clientIds: ['a', 'b'] }],
		})
		const [path, init] = fetchMock.mock.calls[0] as [string, RequestInit]
		expect(path).toBe('/api/v1/setup-imports/preflight')
		if (typeof init.body !== 'string') throw new Error('expected JSON request body')
		expect(JSON.parse(init.body)).toEqual({ items: [
			{ clientId: 'a', role: 'program', displayName: 'Straße.ngc' },
			{ clientId: 'b', role: 'program', displayName: 'STRASSE.ngc' },
		] })
		expect(JSON.stringify(result)).not.toMatch(/storage|path|canonicalKey/i)
	})

	it('loads setup-scoped active jobs without a terminal-history page limit', async () => {
		const fetchMock = vi.fn().mockResolvedValue(jsonResponse({ items: [{
			jobId: 'job-active', kind: 'validate', setupId: 'setup-1', state: 'running',
			progress: { completedBytes: 1, completedItems: 0 }, createdAt: '2026-08-20T00:00:00Z',
		}] }))
		vi.stubGlobal('fetch', fetchMock)
		await expect(listActiveJobs('setup-1')).resolves.toEqual([expect.objectContaining({ jobId: 'job-active', state: 'running' })])
		expect(String(fetchMock.mock.calls[0][0])).toContain('setupId=setup-1&active=true')
	})
})
