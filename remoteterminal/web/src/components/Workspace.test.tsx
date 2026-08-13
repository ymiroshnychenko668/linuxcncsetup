import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type TerminalSession } from '../api'
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
  connectSession: vi.fn(),
  getLatestSelection: vi.fn(),
  createSession: vi.fn(),
  deleteSession: vi.fn(),
  logout: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, api: { ...actual.api, ...mocks } }
})

describe('Workspace', () => {
  beforeEach(() => {
    Object.values(mocks).forEach((mock) => mock.mockReset())
    mocks.getSessions.mockResolvedValue([alpha, beta])
    mocks.connectSession.mockImplementation(async (id: string) => ({
      session: id === alpha.id ? alpha : beta,
      terminalUrl: `/terminal/${id}/`,
    }))
    mocks.getLatestSelection.mockResolvedValue('selected terminal text')
    mocks.deleteSession.mockResolvedValue(undefined)
    mocks.logout.mockResolvedValue(undefined)
    if (!navigator.clipboard) {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: { writeText: vi.fn().mockResolvedValue(undefined) },
      })
    } else {
      vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue(undefined)
    }
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

  it('focuses ttyd after a clicked tab switch but keeps arrow navigation on the tablist', async () => {
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
    await new Promise((resolve) => window.setTimeout(resolve, 30))
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

  it('shows the configured machine name in the terminal page header', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)

    expect(await screen.findByRole('heading', { name: 'Workshop Mill / Workspace' })).toBeInTheDocument()
  })

  it('explains how tmux mouse mode affects browser copy and paste', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(screen.getByRole('button', { name: 'Copy & paste' }))

    const dialog = screen.getByRole('dialog', { name: 'Terminal copy and paste' })
    expect(within(dialog).getByText('Select with tmux')).toBeInTheDocument()
    expect(within(dialog).getByText('Copy yellow selection')).toBeInTheDocument()
    expect(within(dialog).getByText('Native browser copy')).toBeInTheDocument()
    expect(within(dialog).getByText('Paste from this device')).toBeInTheDocument()
    expect(dialog).toHaveTextContent('Shift+drag, then Ctrl+C')
    expect(dialog).toHaveTextContent('Ctrl+V')
    expect(dialog).toHaveTextContent('Yellow highlighting is tmux copy mode')
    expect(dialog).toHaveTextContent('Copy selection')
    expect(dialog).toHaveTextContent('Copy now')
    expect(dialog).toHaveTextContent('Press Esc to clear a selection')
    expect(dialog).not.toHaveTextContent('Ctrl+Shift+C')
    expect(dialog).not.toHaveTextContent('Release to copy')
  })

  it('loads a yellow tmux selection and copies it on a separate click', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    const copyButton = screen.getByRole('button', { name: 'Copy selection' })
    copyButton.click()

    await waitFor(() => expect(mocks.getLatestSelection).toHaveBeenCalledWith(alpha.id))
    expect(await screen.findByRole('button', { name: 'Copy selection now' })).toHaveTextContent('Copy now')
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()

    screen.getByRole('button', { name: 'Copy selection now' }).click()
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith('selected terminal text'))
    expect(screen.getByText('Copied to this device.')).toBeInTheDocument()
  })

  it('prefetches on pointer hover so the click can write cached text', async () => {
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    const copyButton = screen.getByRole('button', { name: 'Copy selection' })
    await user.hover(copyButton)
    await waitFor(() => expect(mocks.getLatestSelection).toHaveBeenCalledWith(alpha.id))
    await screen.findByRole('button', { name: 'Copy selection now' })

    await user.click(screen.getByRole('button', { name: 'Copy selection now' }))
    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith('selected terminal text'))
  })

  it('refreshes a prefetched selection when the pointer returns', async () => {
    mocks.getLatestSelection
      .mockResolvedValueOnce('older terminal text')
      .mockResolvedValueOnce('newer terminal text')
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    const copyButton = screen.getByRole('button', { name: 'Copy selection' })
    await user.hover(copyButton)
    await waitFor(() => expect(mocks.getLatestSelection).toHaveBeenCalledTimes(1))
    await screen.findByRole('button', { name: 'Copy selection now' })

    await user.unhover(copyButton)
    await user.hover(copyButton)
    await waitFor(() => expect(mocks.getLatestSelection).toHaveBeenCalledTimes(2))
    await screen.findByRole('button', { name: 'Copy selection now' })
    screen.getByRole('button', { name: 'Copy selection now' }).click()

    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith('newer terminal text'))
  })

  it('ignores a clipboard response from a terminal that is no longer active', async () => {
    let resolveAlpha!: (text: string) => void
    const alphaSelection = new Promise<string>((resolve) => {
      resolveAlpha = resolve
    })
    mocks.getLatestSelection
      .mockReturnValueOnce(alphaSelection)
      .mockResolvedValueOnce('beta terminal text')
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    screen.getByRole('button', { name: 'Copy selection' }).click()
    await waitFor(() => expect(mocks.getLatestSelection).toHaveBeenCalledWith(alpha.id))

    await user.click(screen.getByRole('button', { name: /beta.*open/i }))
    await screen.findByRole('heading', { name: 'Workshop Mill / beta' })
    screen.getByRole('button', { name: 'Copy selection' }).click()
    await waitFor(() => expect(mocks.getLatestSelection).toHaveBeenCalledWith(beta.id))
    await screen.findByRole('button', { name: 'Copy selection now' })

    resolveAlpha('alpha terminal text')
    await Promise.resolve()
    screen.getByRole('button', { name: 'Copy selection now' }).click()

    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith('beta terminal text'))
    expect(navigator.clipboard.writeText).not.toHaveBeenCalledWith('alpha terminal text')
  })

  it('keeps the copy control focused while a keyboard prefetch is loading', async () => {
    let resolveSelection!: (text: string) => void
    mocks.getLatestSelection.mockReturnValueOnce(new Promise<string>((resolve) => {
      resolveSelection = resolve
    }))
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    const copyButton = screen.getByRole('button', { name: 'Copy selection' })
    copyButton.focus()
    await waitFor(() => expect(mocks.getLatestSelection).toHaveBeenCalledWith(alpha.id))
    expect(copyButton).toHaveFocus()
    expect(copyButton).not.toBeDisabled()
    expect(copyButton).toHaveAttribute('aria-busy', 'true')

    await user.keyboard('{Enter}')
    expect(copyButton).toHaveFocus()
    expect(navigator.clipboard.writeText).not.toHaveBeenCalled()

    resolveSelection('keyboard terminal text')
    const readyButton = await screen.findByRole('button', { name: 'Copy selection now' })
    expect(readyButton).toHaveFocus()
    await user.keyboard('{Enter}')

    await waitFor(() => expect(navigator.clipboard.writeText).toHaveBeenCalledWith('keyboard terminal text'))
  })

  it('reports an unavailable selection and a browser clipboard rejection', async () => {
    mocks.getLatestSelection.mockRejectedValueOnce(new ApiError('Select terminal text with the mouse first.', 409, 'no_selection'))
    render(<Workspace machineName="Workshop Mill" username="operator" onLogout={vi.fn()} />)
    const user = userEvent.setup()

    await user.click(await screen.findByRole('button', { name: /alpha.*open/i }))
    screen.getByRole('button', { name: 'Copy selection' }).click()
    expect(await screen.findByRole('alert')).toHaveTextContent('Select terminal text with the mouse first')

    mocks.getLatestSelection.mockResolvedValueOnce('retry text')
    vi.mocked(navigator.clipboard.writeText).mockRejectedValueOnce(new DOMException('denied', 'NotAllowedError'))
    screen.getByRole('button', { name: 'Copy selection' }).click()
    await screen.findByRole('button', { name: 'Copy selection now' })
    screen.getByRole('button', { name: 'Copy selection now' }).click()

    expect(await screen.findByRole('alert')).toHaveTextContent('Hold Shift while dragging, then press Ctrl+C')
  })
})
