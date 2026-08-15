import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { isPlainHTTP, isRemotePlainHTTP, LoginView } from './LoginView'

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

    expect(screen.getByRole('checkbox', { name: /remember me/i })).toBeChecked()
    expect(mocks.login).toHaveBeenCalledWith('operator', 'secret', true)
    expect(onAuthenticated).toHaveBeenCalledWith(expect.objectContaining({
      user: { username: 'operator' },
    }))
    expect(localStorage.length).toBe(0)
  })

  it('allows a browser-session-only sign in', async () => {
    mocks.login.mockResolvedValue({
      authenticated: true,
      user: { username: 'operator' },
      csrfToken: 'csrf-value',
    })
    render(<LoginView machineName="Workshop Mill" onAuthenticated={vi.fn()} />)
    const user = userEvent.setup()

    await user.type(screen.getByLabelText('Username'), 'operator')
    await user.type(screen.getByLabelText('Password'), 'secret')
    await user.click(screen.getByRole('checkbox', { name: /remember me/i }))
    await user.click(screen.getByRole('button', { name: /open terminal workspace/i }))

    expect(mocks.login).toHaveBeenCalledWith('operator', 'secret', false)
  })

  it('uses a generic message for rejected credentials', async () => {
    mocks.login.mockResolvedValue({ authenticated: false, user: null })
    render(<LoginView machineName="Workshop Mill" onAuthenticated={vi.fn()} />)

    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'unknown' } })
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'bad' } })
    fireEvent.submit(screen.getByRole('button', { name: /open terminal workspace/i }).closest('form')!)

    expect(await screen.findByText(/username or password was not accepted/i)).toBeInTheDocument()
  })

  it('shows the configured machine name', () => {
    render(<LoginView machineName="Workshop Mill" onAuthenticated={vi.fn()} />)

    expect(screen.getByRole('heading', { name: 'Sign in to Workshop Mill' })).toBeInTheDocument()
  })

  it('distinguishes remote plaintext origins from the localhost secure-context exception', () => {
    expect(isPlainHTTP(new URL('http://dominant.int:8443/'))).toBe(true)
    expect(isPlainHTTP(new URL('http://localhost:8443/'))).toBe(true)
    expect(isPlainHTTP(new URL('https://dominant.int:8443/'))).toBe(false)
    expect(isRemotePlainHTTP(new URL('http://dominant.int:8443/'))).toBe(true)
    expect(isRemotePlainHTTP(new URL('http://10.0.1.134:8443/'))).toBe(true)
    expect(isRemotePlainHTTP(new URL('http://localhost:8443/'))).toBe(false)
    expect(isRemotePlainHTTP(new URL('https://dominant.int:8443/'))).toBe(false)
  })
})
