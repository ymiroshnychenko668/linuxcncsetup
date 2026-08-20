import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  apiRequest,
  clearApiSession,
  getCapabilities,
	getImport,
  checkSetupName,
  getReadiness,
  getUIState,
  listRecentSetups,
	listActiveJobs,
	preflightImport,
  putUIState,
  setCsrfToken,
} from './api'

function jsonResponse(body: unknown, init: ResponseInit = {}): Response {
  return new Response(JSON.stringify(body), {
    status: 200,
    headers: { 'Content-Type': 'application/json' },
    ...init,
  })
}

afterEach(() => {
  clearApiSession()
  vi.unstubAllGlobals()
})

describe('API client', () => {
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
