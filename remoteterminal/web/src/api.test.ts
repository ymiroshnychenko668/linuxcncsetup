import { beforeEach, describe, expect, it, vi } from 'vitest'
import { api } from './api'

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

describe('api security contract', () => {
  beforeEach(() => {
    api.clearAuthentication()
    vi.stubGlobal('fetch', vi.fn())
  })

  it('loads the public machine configuration', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock.mockResolvedValueOnce(jsonResponse({ machineName: 'Workshop Mill' }))

    await expect(api.getConfig()).resolves.toEqual({ machineName: 'Workshop Mill' })
    expect(fetchMock).toHaveBeenCalledWith('/api/config', expect.objectContaining({
      cache: 'no-store',
      credentials: 'same-origin',
    }))
  })

  it('keeps the CSRF token in memory and sends it on state-changing calls', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true,
        user: { username: 'operator' },
        csrfToken: 'csrf-secret',
      }))
      .mockResolvedValueOnce(jsonResponse({
        session: {
          id: 'new-id',
          name: 'diagnostics',
          attached: false,
          windows: 1,
          terminalConnected: false,
        },
      }))

    await api.login('operator', 'system-password')
    await api.createSession('diagnostics')

    const [, loginInit] = fetchMock.mock.calls[0]
    const [, createInit] = fetchMock.mock.calls[1]
    expect(loginInit).toMatchObject({ credentials: 'same-origin' })
    expect(createInit).toMatchObject({ credentials: 'same-origin', method: 'POST' })
    expect(new Headers(createInit?.headers).get('X-CSRF-Token')).toBe('csrf-secret')
    expect(localStorage.length).toBe(0)
  })

  it('rejects a terminal URL outside the same-origin proxy path', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true,
        user: { username: 'operator' },
        csrfToken: 'csrf-secret',
      }))
      .mockResolvedValueOnce(jsonResponse({
        session: {
          id: 'session-id',
          name: 'alpha',
          attached: false,
          windows: 1,
          terminalConnected: true,
        },
        terminalUrl: 'https://attacker.invalid/terminal/session-id/',
      }))

    await api.login('operator', 'password')
    await expect(api.connectSession('session-id')).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('loads the latest tmux selection without placing it in browser storage', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock.mockResolvedValueOnce(jsonResponse({ text: 'selected text\nрядок' }))

    await expect(api.getLatestSelection('session/id')).resolves.toBe('selected text\nрядок')
    expect(fetchMock).toHaveBeenCalledWith(
      '/api/sessions/session%2Fid/clipboard',
      expect.objectContaining({ cache: 'no-store', credentials: 'same-origin' }),
    )
    expect(localStorage.length).toBe(0)
  })
})
