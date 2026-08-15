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
    api.setUnauthorizedHandler(undefined)
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

    await api.login('operator', 'system-password', true)
    await api.createSession('diagnostics')

    const [, loginInit] = fetchMock.mock.calls[0]
    const [, createInit] = fetchMock.mock.calls[1]
    expect(loginInit).toMatchObject({ credentials: 'same-origin' })
    expect(loginInit?.body).toBe(JSON.stringify({
      username: 'operator',
      password: 'system-password',
      rememberMe: true,
    }))
    expect(createInit).toMatchObject({ credentials: 'same-origin', method: 'POST' })
    expect(new Headers(createInit?.headers).get('X-CSRF-Token')).toBe('csrf-secret')
    expect(localStorage.length).toBe(0)
  })

  it('refreshes a replaced browser session and retries a CSRF-rejected mutation once', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true,
        user: { username: 'operator' },
        csrfToken: 'csrf-from-this-tab',
      }))
      .mockResolvedValueOnce(jsonResponse({
        error: { code: 'csrf_rejected', message: 'The request could not be verified.' },
      }, 403))
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true,
        user: { username: 'operator' },
        csrfToken: 'csrf-from-current-cookie',
      }))
      .mockResolvedValueOnce(jsonResponse({
        session: {
          id: 'setup-id',
          name: 'setup',
          attached: false,
          windows: 1,
          terminalConnected: false,
        },
      }, 201))

    await api.login('operator', 'password')
    await expect(api.createSession('setup')).resolves.toMatchObject({ id: 'setup-id', name: 'setup' })

    expect(fetchMock.mock.calls.map(([path]) => path)).toEqual([
      '/api/auth/login',
      '/api/sessions',
      '/api/auth/session',
      '/api/sessions',
    ])
    expect(new Headers(fetchMock.mock.calls[1][1]?.headers).get('X-CSRF-Token')).toBe('csrf-from-this-tab')
    expect(fetchMock.mock.calls[2][1]).toMatchObject({ cache: 'no-store', credentials: 'same-origin' })
    expect(new Headers(fetchMock.mock.calls[2][1]?.headers).has('X-CSRF-Token')).toBe(false)
    expect(fetchMock.mock.calls[3][1]).toMatchObject({
      method: 'POST',
      body: JSON.stringify({ name: 'setup' }),
    })
    expect(new Headers(fetchMock.mock.calls[3][1]?.headers).get('X-CSRF-Token')).toBe('csrf-from-current-cookie')
  })

  it('does not loop when refreshing a CSRF-rejected request fails authentication', async () => {
    const fetchMock = vi.mocked(fetch)
    const unauthorized = vi.fn()
    api.setUnauthorizedHandler(unauthorized)
    fetchMock
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true,
        user: { username: 'operator' },
        csrfToken: 'stale-csrf',
      }))
      .mockResolvedValueOnce(jsonResponse({
        error: { code: 'csrf_rejected', message: 'The request could not be verified.' },
      }, 403))
      .mockResolvedValueOnce(jsonResponse({
        error: { code: 'unauthorized', message: 'Authentication is required.' },
      }, 401))

    await api.login('operator', 'password')
    await expect(api.createSession('setup')).rejects.toMatchObject({ status: 401, code: 'unauthorized' })
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(unauthorized).toHaveBeenCalledTimes(1)

    await expect(api.createSession('setup')).rejects.toMatchObject({ status: 401, code: 'session_expired' })
    expect(fetchMock).toHaveBeenCalledTimes(3)
  })

  it('does not refresh or retry an unrelated forbidden mutation', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true,
        user: { username: 'operator' },
        csrfToken: 'csrf-secret',
      }))
      .mockResolvedValueOnce(jsonResponse({
        error: { code: 'origin_rejected', message: 'The request origin is not allowed.' },
      }, 403))

    await api.login('operator', 'password')
    await expect(api.createSession('setup')).rejects.toMatchObject({ status: 403, code: 'origin_rejected' })
    expect(fetchMock).toHaveBeenCalledTimes(2)
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

  it('takes or discards tmux selections with CSRF-protected one-shot mutations and does not store them', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true,
        user: { username: 'operator' },
        csrfToken: 'csrf-secret',
      }))
      .mockResolvedValueOnce(jsonResponse({ text: 'selected text\nрядок' }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await api.login('operator', 'password')
    await expect(api.takeLatestSelection('session/id')).resolves.toBe('selected text\nрядок')
    await expect(api.discardSelections('session/id')).resolves.toBeUndefined()
    expect(fetchMock.mock.calls[1][0]).toBe('/api/sessions/session%2Fid/clipboard')
    expect(fetchMock.mock.calls[1][1]).toMatchObject({
      method: 'POST',
      cache: 'no-store',
      credentials: 'same-origin',
    })
    expect(new Headers(fetchMock.mock.calls[1][1]?.headers).get('X-CSRF-Token')).toBe('csrf-secret')
    expect(fetchMock.mock.calls[2][0]).toBe('/api/sessions/session%2Fid/clipboard')
    expect(fetchMock.mock.calls[2][1]).toMatchObject({ method: 'DELETE', credentials: 'same-origin' })
    expect(new Headers(fetchMock.mock.calls[2][1]?.headers).get('X-CSRF-Token')).toBe('csrf-secret')
    expect(localStorage.length).toBe(0)
  })

  it('uses authenticated Code Server APIs and accepts only its exact same-origin proxy path', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock
      .mockResolvedValueOnce(jsonResponse({
        authenticated: true,
        user: { username: 'operator' },
        csrfToken: 'csrf-secret',
      }))
      .mockResolvedValueOnce(jsonResponse({
        codeServer: {
          id: 'code-project',
          name: 'project',
          folderPath: '/home/operator/project',
          url: '/code/code-project/',
        },
        reused: false,
      }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))

    await api.login('operator', 'password')
    await expect(api.launchCodeServer('/home/operator/project')).resolves.toMatchObject({
      codeServer: { id: 'code-project', url: '/code/code-project/' },
      reused: false,
    })
    await api.shutdownCodeServer('code/project')

    expect(fetchMock.mock.calls[1][0]).toBe('/api/code-servers')
    expect(fetchMock.mock.calls[1][1]).toMatchObject({
      method: 'POST',
      body: JSON.stringify({ folderPath: '/home/operator/project' }),
    })
    expect(new Headers(fetchMock.mock.calls[1][1]?.headers).get('X-CSRF-Token')).toBe('csrf-secret')
    expect(fetchMock.mock.calls[2][0]).toBe('/api/code-servers/code%2Fproject')
  })

  it('rejects a Code Server URL that does not match its returned instance id', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock.mockResolvedValueOnce(jsonResponse({
      codeServers: [{
        id: 'code-project',
        name: 'project',
        folderPath: '/home/operator/project',
        url: '/code/another-instance/',
      }],
    }))

    await expect(api.getCodeServers()).rejects.toMatchObject({ code: 'invalid_response' })
  })

  it('encodes directory paths and validates canonical absolute listings', async () => {
    const fetchMock = vi.mocked(fetch)
    fetchMock.mockResolvedValueOnce(jsonResponse({
      path: '/home/operator/My Project',
      parentPath: '/home/operator',
      directories: [{ name: 'src', path: '/home/operator/My Project/src' }],
      truncated: false,
    }))

    await expect(api.getDirectories('/home/operator/My Project')).resolves.toMatchObject({
      path: '/home/operator/My Project',
      directories: [{ name: 'src' }],
    })
    expect(fetchMock.mock.calls[0][0]).toBe('/api/directories?path=%2Fhome%2Foperator%2FMy%20Project')
  })
})
