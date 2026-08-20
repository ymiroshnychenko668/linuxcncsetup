import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  ApiError,
  apiRequest,
  clearApiSession,
  getCapabilities,
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
})
