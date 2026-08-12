import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { LoginView } from './LoginView'

const mocks = vi.hoisted(() => ({
  login: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, api: { ...actual.api, login: mocks.login } }
})

describe('LoginView', () => {
  beforeEach(() => mocks.login.mockReset())

  it('authenticates without storing the password', async () => {
    const onAuthenticated = vi.fn()
    mocks.login.mockResolvedValue({
      authenticated: true,
      user: { username: 'operator' },
      csrfToken: 'csrf-value',
    })

    render(<LoginView machineName="Workshop Mill" onAuthenticated={onAuthenticated} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Username'), 'operator')
    await user.type(screen.getByLabelText('Password'), 'secret')
    await user.click(screen.getByRole('button', { name: /open terminal workspace/i }))

    expect(mocks.login).toHaveBeenCalledWith('operator', 'secret')
    expect(onAuthenticated).toHaveBeenCalledWith(expect.objectContaining({
      user: { username: 'operator' },
    }))
    expect(localStorage.length).toBe(0)
  })

  it('uses a generic message for rejected credentials', async () => {
    mocks.login.mockResolvedValue({ authenticated: false, user: null })
    render(<LoginView machineName="Workshop Mill" onAuthenticated={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'unknown' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'bad' } })
    fireEvent.submit(screen.getByRole('button', { name: /open terminal workspace/i }).closest('form')!)

    expect(await screen.findByRole('alert')).toHaveTextContent('username or password was not accepted')
  })

  it('shows the configured machine name', () => {
    render(<LoginView machineName="Workshop Mill" onAuthenticated={vi.fn()} />)

    expect(screen.getByRole('heading', { name: 'Sign in to Workshop Mill' })).toBeInTheDocument()
  })
})
