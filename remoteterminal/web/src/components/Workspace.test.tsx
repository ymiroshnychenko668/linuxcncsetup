import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type CodeServerInstance, type TerminalSession } from '../api'
import { Workspace } from './Workspace'

const alpha: TerminalSession = {
  id: 'session-alpha',
  name: 'alpha',
  attached: false,
  windows: 1,
  terminalConnected: false,
}
const beta: TerminalSession = {
  id: 'session-beta',
  name: 'beta',
  attached: true,
  windows: 2,
  terminalConnected: false,
}

const projectCodeServer: CodeServerInstance = {
  id: 'code-project',
  name: 'project',
  folderPath: '/home/operator/project',
  createdAt: '2026-08-13T10:00:00Z',
  url: '/code/code-project/',
}

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

function installMobileMatchMedia() {
  vi.stubGlobal('matchMedia', vi.fn((query: string) => ({
    matches: query === '(max-width: 800px)',
    media: query,
    onchange: null,
    addListener: vi.fn(),
    removeListener: vi.fn(),
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(() => true),
  })))
}

function installTerminalFocus(frame: HTMLIFrameElement) {
  const terminalDocument = document.implementation.createHTMLDocument('terminal')
  const textarea = terminalDocument.createElement('textarea')
  terminalDocument.body.append(textarea)
  let textareaFocused = false
  Object.defineProperty(terminalDocument, 'activeElement', {
    configurable: true,
    get: () => textareaFocused ? textarea : terminalDocument.body,
  })
  const focus = vi.fn(() => {
    frame.focus()
    textareaFocused = true
  })
  Object.defineProperty(frame.contentWindow, 'term', {
    configurable: true,
    value: { focus, textarea },
  })
  return focus
}

const mocks = vi.hoisted(() => ({
  getSessions: vi.fn(),
  getCodeServers: vi.fn(),
  getDirectories: vi.fn(),
  launchCodeServer: vi.fn(),
  shutdownCodeServer: vi.fn(),
  connectSession: vi.fn(),
  takeLatestSelection: vi.fn(),
  discardSelections: vi.fn(),
  createSession: vi.fn(),
  deleteSession: vi.fn(),
  logout: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, api: { ...actual.api, ...mocks } }
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Workspace', () => {
  beforeEach(() => {
    Object.values(mocks).forEach((mock) => mock.mockReset())
    mocks.getSessions.mockResolvedValue([alpha, beta])
    mocks.getCodeServers.mockResolvedValue([])
    mocks.getDirectories.mockResolvedValue({
      path: '/home/operator',
      parentPath: '/home',
      directories: [{ name: 'project', path: '/home/operator/project' }],
      truncated: false,
    })
    mocks.launchCodeServer.mockResolvedValue({ codeServer: projectCodeServer, reused: false })
    mocks.shutdownCodeServer.mockResolvedValue(undefined)
    mocks.connectSession.mockImplementation(async (id: string) => ({
      session: id === alpha.id ? alpha : beta,
      terminalUrl: `/terminal/${id}/`,
    }))
    mocks.takeLatestSelection.mockResolvedValue('selected terminal text')
    mocks.discardSelections.mockResolvedValue(undefined)
    mocks.deleteSession.mockResolvedValue(undefined)
    mocks.logout.mockResolvedValue(undefined)
  })

  it('opens multiple sessions as accessible vertical tabs and keeps panels mounted', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    await user.click(screen.getByRole('button', { name: /beta.*open/i }))

    const tablist = screen.getByRole('tablist', { name: /open terminal sessions/i })
    expect(tablist).toHaveAttribute('aria-orientation', 'vertical')
    const tabs = within(tablist).getAllByRole('tab')
    expect(tabs).toHaveLength(2)
    expect(screen.getByRole('tab', { name: /beta/i })).toHaveAttribute('aria-selected', 'true')

    await waitFor(() => expect(mocks.connectSession).toHaveBeenCalledTimes(2))
    expect(screen.getByTitle('alpha terminal')).toBeInTheDocument()
    expect(screen.getByTitle('beta terminal')).toBeInTheDocument()
    expect(screen.getByTitle('alpha terminal').closest('[role="tabpanel"]')).toHaveAttribute('hidden')

    tabs[1].focus()
    await user.keyboard('{ArrowUp}')
    expect(screen.getByRole('tab', { name: /alpha/i })).toHaveAttribute('aria-selected', 'true')
  })

  it('moves keyboard focus to the selected successor when Delete closes a tab', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    await user.click(screen.getByRole('button', { name: /beta.*open/i }))
    const betaTab = screen.getByRole('tab', { name: /beta/i })
    betaTab.focus()

    await user.keyboard('{Delete}')

    const alphaTab = screen.getByRole('tab', { name: /alpha/i })
    expect(screen.queryByRole('tab', { name: /beta/i })).not.toBeInTheDocument()
    expect(alphaTab).toHaveAttribute('aria-selected', 'true')
    expect(alphaTab).toHaveFocus()
  })

  it('moves keyboard focus into ttyd for click activation but keeps arrow navigation on tabs', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    const alphaFrame = await screen.findByTitle('alpha terminal') as HTMLIFrameElement
    const focusAlpha = installTerminalFocus(alphaFrame)
    await waitFor(() => expect(focusAlpha).toHaveBeenCalled())

    await user.click(screen.getByRole('button', { name: /beta.*open/i }))
    const betaFrame = await screen.findByTitle('beta terminal') as HTMLIFrameElement
    const focusBeta = installTerminalFocus(betaFrame)
    await waitFor(() => expect(focusBeta).toHaveBeenCalled())

    focusAlpha.mockClear()
    focusBeta.mockClear()
    const alphaTab = screen.getByRole('tab', { name: /alpha/i })
    await user.click(alphaTab)
    await waitFor(() => expect(focusAlpha).toHaveBeenCalledTimes(1))
    expect(document.activeElement).toBe(alphaFrame)
    expect(focusBeta).not.toHaveBeenCalled()

    focusAlpha.mockClear()
    alphaTab.focus()
    await user.keyboard('{ArrowDown}')
    const betaTab = screen.getByRole('tab', { name: /beta/i })
    expect(betaTab).toHaveAttribute('aria-selected', 'true')
    expect(document.activeElement).toBe(betaTab)
    await act(async () => new Promise((resolve) => window.setTimeout(resolve, 30)))
    expect(focusAlpha).not.toHaveBeenCalled()
    expect(focusBeta).not.toHaveBeenCalled()
  })

  it('closing a tab leaves tmux running while deletion is confirmed separately', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    await user.click(screen.getByRole('button', { name: 'Close alpha browser tab' }))

    expect(mocks.deleteSession).not.toHaveBeenCalled()
    expect(screen.queryByRole('tab', { name: /alpha/i })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /alpha.*open/i })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Delete alpha tmux session' }))
    expect(screen.getByRole('dialog', { name: /delete “alpha”/i })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /delete tmux session/i }))

    await waitFor(() => expect(mocks.deleteSession).toHaveBeenCalledWith(alpha.id))
    expect(screen.queryByRole('button', { name: /alpha.*open/i })).not.toBeInTheDocument()
  })

  it('restores non-sensitive open tab order and selection for the signed-in user', async () => {
    localStorage.setItem('remoteterminal.workspace.v1:operator', JSON.stringify({
      openIds: [beta.id, alpha.id],
      selectedId: alpha.id,
    }))

    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)

    const tabs = await screen.findAllByRole('tab')
    expect(tabs.map((tab) => tab.textContent)).toEqual(expect.arrayContaining([
      expect.stringContaining('beta'),
      expect.stringContaining('alpha'),
    ]))
    expect(tabs[0]).toHaveTextContent('beta')
    expect(tabs[1]).toHaveTextContent('alpha')
    expect(screen.getByRole('tab', { name: /alpha/i })).toHaveAttribute('aria-selected', 'true')
    expect(localStorage.getItem('remoteterminal.workspace.v1:operator')).not.toContain('csrf')
  })

  it('connects restored tabs only after first activation and then keeps their panels mounted', async () => {
    localStorage.setItem('remoteterminal.workspace.v1:operator', JSON.stringify({
      openIds: [alpha.id, beta.id],
      selectedId: alpha.id,
    }))

    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await waitFor(() => expect(mocks.connectSession).toHaveBeenCalledTimes(1))
    expect(mocks.connectSession).toHaveBeenLastCalledWith(alpha.id)
    expect(screen.getByTitle('alpha terminal')).toBeInTheDocument()
    expect(screen.queryByTitle('beta terminal')).not.toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /beta/i })).toHaveTextContent('Ready to open')

    await user.click(screen.getByRole('tab', { name: /beta/i }))
    await waitFor(() => expect(mocks.connectSession).toHaveBeenCalledTimes(2))
    expect(mocks.connectSession).toHaveBeenLastCalledWith(beta.id)
    expect(screen.getByTitle('alpha terminal')).toBeInTheDocument()
    expect(screen.getByTitle('beta terminal')).toBeInTheDocument()
    expect(screen.getByTitle('alpha terminal').closest('[role="tabpanel"]')).toHaveAttribute('hidden')

    await user.click(screen.getByRole('tab', { name: /alpha/i }))
    expect(mocks.connectSession).toHaveBeenCalledTimes(2)
    expect(screen.getByTitle('alpha terminal')).toBeInTheDocument()
    expect(screen.getByTitle('beta terminal')).toBeInTheDocument()
    expect(screen.getByTitle('beta terminal').closest('[role="tabpanel"]')).toHaveAttribute('hidden')
  })

  it('migrates terminal-only v1 storage into typed v2 tabs without storing remote paths', async () => {
    localStorage.setItem('remoteterminal.workspace.v1:operator', JSON.stringify({
      openIds: [beta.id, alpha.id],
      selectedId: beta.id,
    }))

    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)

    await screen.findByRole('tab', { name: /beta/i })
    await waitFor(() => {
      expect(localStorage.getItem('remoteterminal.workspace.v2:operator')).toBe(JSON.stringify({
        openTabs: [
          { kind: 'terminal', id: beta.id },
          { kind: 'terminal', id: alpha.id },
        ],
        selectedTab: { kind: 'terminal', id: beta.id },
      }))
    })
    expect(localStorage.getItem('remoteterminal.workspace.v2:operator')).not.toContain('/home')
  })

  it('opens mixed terminal and Code Server tabs with a distinct editor treatment', async () => {
    mocks.getCodeServers.mockResolvedValue([projectCodeServer])
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    await user.click(screen.getByRole('button', { name: /open project code server/i }))

    const tablist = screen.getByRole('tablist', { name: /open terminal sessions and code servers/i })
    const tabs = within(tablist).getAllByRole('tab')
    expect(tabs).toHaveLength(2)
    expect(tabs[0]).toHaveTextContent('alpha')
    expect(tabs[1]).toHaveTextContent('project')
    expect(tabs[1].closest('.session-tab')).toHaveClass('session-tab--code-server')
    expect(screen.getByTitle('project Code Server')).toHaveAttribute('src', projectCodeServer.url)
    expect(screen.getByTitle('alpha terminal')).toBeInTheDocument()
    expect(screen.getByTitle('alpha terminal').closest('[role="tabpanel"]')).toHaveAttribute('hidden')
    expect(screen.getByRole('heading', { name: 'Workshop Mill / project' })).toBeInTheDocument()
    const topbar = document.querySelector('.topbar') as HTMLElement
    expect(topbar.querySelector('.topbar__actions')).not.toBeInTheDocument()
    expect(within(topbar).queryByRole('button', { name: 'Reload Code Server editor' })).not.toBeInTheDocument()
    expect(within(topbar).queryByRole('button', { name: 'Shut down project Code Server' })).not.toBeInTheDocument()
  })

  it('keeps restored Code Server frames lazy until first activation and mounted afterward', async () => {
    mocks.getCodeServers.mockResolvedValue([projectCodeServer])
    localStorage.setItem('remoteterminal.workspace.v2:operator', JSON.stringify({
      openTabs: [
        { kind: 'terminal', id: alpha.id },
        { kind: 'codeServer', id: projectCodeServer.id },
      ],
      selectedTab: { kind: 'terminal', id: alpha.id },
    }))
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await screen.findByTitle('alpha terminal')
    expect(screen.queryByTitle('project Code Server')).not.toBeInTheDocument()

    await user.click(screen.getByRole('tab', { name: /project/i }))
    expect(await screen.findByTitle('project Code Server')).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: /alpha/i }))
    expect(screen.getByTitle('project Code Server')).toBeInTheDocument()
    expect(screen.getByTitle('project Code Server').closest('[role="tabpanel"]')).toHaveAttribute('hidden')
  })

  it('closes only the Code Server browser tab and confirms shutdown separately', async () => {
    mocks.getCodeServers.mockResolvedValue([projectCodeServer])
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /open project code server/i }))
    await user.click(screen.getByRole('button', { name: 'Close project browser tab' }))

    expect(mocks.shutdownCodeServer).not.toHaveBeenCalled()
    expect(screen.queryByRole('tab', { name: /project/i })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: /open project code server/i })).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Shut down project Code Server' }))
    expect(screen.getByRole('dialog', { name: /shut down “project”/i })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Shut down Code Server' }))

    await waitFor(() => expect(mocks.shutdownCodeServer).toHaveBeenCalledWith(projectCodeServer.id))
    expect(screen.queryByRole('button', { name: /project code server/i })).not.toBeInTheDocument()
  })

  it('focuses a stable tab successor after shutting down the selected Code Server', async () => {
    mocks.getCodeServers.mockResolvedValue([projectCodeServer])
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    await user.click(screen.getByRole('button', { name: /open project code server/i }))
    await user.click(screen.getByRole('button', { name: 'Shut down project Code Server' }))
    await user.click(screen.getByRole('button', { name: 'Shut down Code Server' }))

    await waitFor(() => expect(mocks.shutdownCodeServer).toHaveBeenCalledWith(projectCodeServer.id))
    const alphaTab = screen.getByRole('tab', { name: /alpha/i })
    expect(alphaTab).toHaveAttribute('aria-selected', 'true')
    await waitFor(() => expect(alphaTab).toHaveFocus())
  })

  it('launches a Code Server from the remote folder picker and opens its tab', async () => {
    const staleList = deferred<CodeServerInstance[]>()
    mocks.getCodeServers
      .mockResolvedValueOnce([])
      .mockReturnValueOnce(staleList.promise)
    mocks.getDirectories.mockImplementation(async (path?: string) => path === '/home/operator/project'
      ? {
          path: '/home/operator/project',
          parentPath: '/home/operator',
          directories: [],
          truncated: false,
        }
      : {
          path: '/home/operator',
          parentPath: '/home',
          directories: [{ name: 'project', path: '/home/operator/project' }],
          truncated: false,
        })
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await waitFor(() => expect(mocks.getCodeServers).toHaveBeenCalledTimes(1))
    window.dispatchEvent(new Event('focus'))
    await waitFor(() => expect(mocks.getCodeServers).toHaveBeenCalledTimes(2))
    await user.click(screen.getAllByRole('button', { name: 'Launch Code Server' })[0])
    const dialog = screen.getByRole('dialog', { name: 'Launch Code Server' })
    expect(await within(dialog).findByDisplayValue('/home/operator')).toBeInTheDocument()
    expect(dialog).toHaveTextContent('run with the configured Linux account’s authority')
    expect(dialog).toHaveTextContent('Open only folders and extensions you trust')
    await user.click(within(dialog).getByRole('button', { name: /project/i }))
    expect(await within(dialog).findByDisplayValue('/home/operator/project')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Launch in this folder' }))

    await waitFor(() => expect(mocks.launchCodeServer).toHaveBeenCalledWith('/home/operator/project'))
    expect(await screen.findByRole('tab', { name: /project/i })).toHaveAttribute('aria-selected', 'true')
    await act(async () => staleList.resolve([]))
    expect(screen.getByRole('tab', { name: /project/i })).toBeInTheDocument()
  })

  it('polls active Code Servers every five seconds only while the page is visible', async () => {
    vi.useFakeTimers()
    try {
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
      render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
      await act(async () => {
        await Promise.resolve()
      })
      expect(mocks.getCodeServers).toHaveBeenCalledTimes(1)

      await act(async () => vi.advanceTimersByTimeAsync(5000))
      expect(mocks.getCodeServers).toHaveBeenCalledTimes(2)

      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'hidden' })
      act(() => document.dispatchEvent(new Event('visibilitychange')))
      await act(async () => vi.advanceTimersByTimeAsync(10000))
      expect(mocks.getCodeServers).toHaveBeenCalledTimes(2)

      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
      await act(async () => {
        document.dispatchEvent(new Event('visibilitychange'))
        await Promise.resolve()
      })
      expect(mocks.getCodeServers).toHaveBeenCalledTimes(3)
    } finally {
      vi.useRealTimers()
      Object.defineProperty(document, 'visibilityState', { configurable: true, value: 'visible' })
    }
  })

  it('coalesces overlapping Code Server poll triggers and refreshes after completion', async () => {
    const current = deferred<CodeServerInstance[]>()
    mocks.getCodeServers
      .mockReturnValueOnce(current.promise)
      .mockResolvedValueOnce([projectCodeServer])
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)

    await waitFor(() => expect(mocks.getCodeServers).toHaveBeenCalledTimes(1))
    window.dispatchEvent(new Event('focus'))
    expect(mocks.getCodeServers).toHaveBeenCalledTimes(1)

    await act(async () => current.resolve([]))
    window.dispatchEvent(new Event('focus'))
    expect(await screen.findByRole('button', { name: /open project code server/i })).toBeInTheDocument()
    expect(mocks.getCodeServers).toHaveBeenCalledTimes(2)
  })

  it('aborts the owned Code Server poll on teardown so stale auth work cannot outlive the workspace', async () => {
    let requestSignal: AbortSignal | undefined
    mocks.getCodeServers.mockImplementationOnce((signal?: AbortSignal) => new Promise<CodeServerInstance[]>((_resolve, reject) => {
      requestSignal = signal
      signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
    }))
    const onLogout = vi.fn()
    const { unmount } = render(<Workspace machineName="Workshop Mill" username="operator" onLogout={onLogout} />)

    await waitFor(() => expect(requestSignal).toBeDefined())
    window.dispatchEvent(new Event('focus'))
    expect(mocks.getCodeServers).toHaveBeenCalledTimes(1)

    unmount()
    expect(requestSignal?.aborted).toBe(true)
    await act(async () => Promise.resolve())
    expect(onLogout).not.toHaveBeenCalled()
  })

  it('invalidates an in-flight list when Code Server shutdown completes', async () => {
    mocks.getCodeServers.mockResolvedValueOnce([projectCodeServer])
    const stale = deferred<CodeServerInstance[]>()
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await screen.findByRole('button', { name: /open project code server/i })
    mocks.getCodeServers.mockReturnValueOnce(stale.promise)
    window.dispatchEvent(new Event('focus'))
    await waitFor(() => expect(mocks.getCodeServers).toHaveBeenCalledTimes(2))

    await user.click(screen.getByRole('button', { name: 'Shut down project Code Server' }))
    await user.click(screen.getByRole('button', { name: 'Shut down Code Server' }))
    await waitFor(() => expect(mocks.shutdownCodeServer).toHaveBeenCalledWith(projectCodeServer.id))

    await act(async () => stale.resolve([projectCodeServer]))
    expect(screen.queryByRole('button', { name: /project code server/i })).not.toBeInTheDocument()
  })

  it('restores terminals while the first Code Server list fails and does not steal later focus', async () => {
    const stored = {
      openTabs: [
        { kind: 'terminal', id: alpha.id },
        { kind: 'codeServer', id: projectCodeServer.id },
      ],
      selectedTab: { kind: 'codeServer', id: projectCodeServer.id },
    }
    localStorage.setItem('remoteterminal.workspace.v2:operator', JSON.stringify(stored))
    mocks.getCodeServers
      .mockRejectedValueOnce(new ApiError('Code Servers are temporarily unavailable.', 503, 'unavailable'))
      .mockResolvedValueOnce([projectCodeServer])
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    expect(await screen.findByRole('tab', { name: /alpha/i })).toHaveAttribute('aria-selected', 'true')
    expect(localStorage.getItem('remoteterminal.workspace.v2:operator')).toBe(JSON.stringify(stored))
    await user.click(screen.getByRole('button', { name: /beta.*open/i }))
    expect(screen.getByRole('tab', { name: /beta/i })).toHaveAttribute('aria-selected', 'true')

    const error = await screen.findByRole('alert')
    await user.click(within(error).getByRole('button', { name: 'Retry' }))
    expect(await screen.findByRole('tab', { name: /project/i })).toBeInTheDocument()
    expect(screen.getByRole('tab', { name: /beta/i })).toHaveAttribute('aria-selected', 'true')
    await waitFor(() => expect(localStorage.getItem('remoteterminal.workspace.v2:operator')).toContain(projectCodeServer.id))
  })

  it('shows the configured machine name in the terminal page header', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: 'Workshop Mill / Workspace' })).toBeInTheDocument()
  })

  it('makes closed mobile navigation inert and restores focus when it is dismissed', async () => {
    installMobileMatchMedia()
    const { unmount } = render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)

    const sidebar = document.querySelector('.sidebar') as HTMLElement
    const menuButton = screen.getByLabelText('Open session navigation')
    expect(sidebar).toHaveAttribute('aria-hidden', 'true')
    expect(sidebar).toHaveAttribute('inert')
    expect(screen.queryByRole('button', { name: 'New terminal session' })).not.toBeInTheDocument()

    menuButton.click()
    const closeButton = screen.getByLabelText('Close navigation')
    await waitFor(() => expect(closeButton).toHaveFocus())
    expect(sidebar).not.toHaveAttribute('aria-hidden')
    expect(sidebar).not.toHaveAttribute('inert')

    closeButton.click()
    await waitFor(() => {
      expect(sidebar).toHaveAttribute('aria-hidden', 'true')
      expect(sidebar).toHaveAttribute('inert')
    })
    expect(menuButton).toHaveFocus()

    unmount()
  })

  it('shows native-copy guidance without adding actions to the page header', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)

    const note = await screen.findByText(/The selection is copied automatically/)
    expect(note).toHaveTextContent('Shift')
    expect(note).toHaveTextContent('Option')
    expect(screen.queryByRole('button', { name: 'Copy & paste' })).not.toBeInTheDocument()
    expect(document.querySelector('.topbar__actions')).not.toBeInTheDocument()
  })

  it('uses native xterm copy without exposing the legacy selection request', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    expect(await screen.findByTitle('alpha terminal')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Copy selection' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Copy & paste' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Reconnect terminal' })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Delete current terminal session' })).not.toBeInTheDocument()
    expect(mocks.takeLatestSelection).not.toHaveBeenCalled()
    expect(mocks.discardSelections).not.toHaveBeenCalled()
  })
})
