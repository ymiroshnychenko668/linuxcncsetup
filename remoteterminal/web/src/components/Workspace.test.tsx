import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { TerminalSession } from '../api'
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

const mocks = vi.hoisted(() => ({
  getSessions: vi.fn(),
  connectSession: vi.fn(),
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
    expect(within(dialog).getByText('Copy with tmux')).toBeInTheDocument()
    expect(within(dialog).getByText('Browser selection fallback')).toBeInTheDocument()
    expect(within(dialog).getByText('Paste from this device')).toBeInTheDocument()
    expect(dialog).toHaveTextContent('Shift+drag')
    expect(dialog).toHaveTextContent('Ctrl+V')
    expect(dialog).toHaveTextContent('Yellow highlighting is tmux copy mode')
    expect(dialog).toHaveTextContent('Release to copy it to this device')
    expect(dialog).toHaveTextContent('Press Esc to clear a selection')
    expect(dialog).not.toHaveTextContent('Ctrl+Shift+C')
  })
})
