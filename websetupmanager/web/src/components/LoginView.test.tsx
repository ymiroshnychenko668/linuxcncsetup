import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { LoginView } from './LoginView'

const mocks = vi.hoisted(() => ({ login: vi.fn() }))

vi.mock('../api', async (loadOriginal) => ({
  ...await loadOriginal<typeof import('../api')>(),
  login: mocks.login,
}))

describe('LoginView', () => {
  beforeEach(() => mocks.login.mockReset())

  it('supports keyboard login and keeps credentials out of web storage', async () => {
    const onAuthenticated = vi.fn()
    const localStorageLength = localStorage.length
    const sessionStorageLength = sessionStorage.length
    mocks.login.mockResolvedValue({
      authenticated: true,
      loginRequired: true,
      user: { username: 'operator' },
      csrfToken: 'csrf-secret',
    })
    render(<LoginView onAuthenticated={onAuthenticated} />)
    const user = userEvent.setup()

    const username = screen.getByRole('textbox', { name: 'Имя пользователя' })
    expect(username).toHaveFocus()
    expect(username).toHaveAttribute('autocomplete', 'username')
    expect(screen.getByLabelText('Пароль')).toHaveAttribute('autocomplete', 'current-password')
    expect(screen.getByRole('checkbox', { name: /Запомнить меня/ })).not.toBeChecked()

    await user.type(username, '  operator  ')
    await user.type(screen.getByLabelText('Пароль'), 'system-secret')
    await user.click(screen.getByRole('checkbox', { name: /Запомнить меня/ }))
    await user.keyboard('{Enter}')

    await waitFor(() => expect(mocks.login).toHaveBeenCalledWith('operator', 'system-secret', true))
    expect(onAuthenticated).toHaveBeenCalledWith(expect.objectContaining({
      user: { username: 'operator' },
    }))
    expect(screen.getByLabelText('Пароль')).toHaveValue('')
    expect(localStorage).toHaveLength(localStorageLength)
    expect(sessionStorage).toHaveLength(sessionStorageLength)
  })

  it('disables the form and prevents a second submission while PAM is pending', async () => {
    const onAuthenticated = vi.fn()
    let resolveLogin!: (value: unknown) => void
    mocks.login.mockReturnValue(new Promise((resolve) => { resolveLogin = resolve }))
    render(<LoginView onAuthenticated={onAuthenticated} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Имя пользователя'), 'operator')
    await user.type(screen.getByLabelText('Пароль'), 'secret')

    const submit = screen.getByRole('button', { name: 'Открыть каталог сетапов' })
    await user.click(submit)
    expect(screen.getByRole('button', { name: 'Выполняется вход…' })).toBeDisabled()
    expect(screen.getByLabelText('Имя пользователя')).toBeDisabled()
    expect(screen.getByLabelText('Пароль')).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Выполняется вход…' }))
    expect(mocks.login).toHaveBeenCalledTimes(1)

    resolveLogin({
      authenticated: true,
      loginRequired: true,
      user: { username: 'operator' },
      csrfToken: 'token',
    })
    await waitFor(() => expect(onAuthenticated).toHaveBeenCalledTimes(1))
  })

  it('uses a generic rejection, clears the password and returns focus to it', async () => {
    mocks.login.mockResolvedValue({
      authenticated: false,
      loginRequired: true,
      user: null,
    })
    render(<LoginView onAuthenticated={vi.fn()} />)
    const user = userEvent.setup()
    await user.type(screen.getByLabelText('Имя пользователя'), 'unknown')
    await user.type(screen.getByLabelText('Пароль'), 'wrong')
    await user.click(screen.getByRole('button', { name: 'Открыть каталог сетапов' }))

    expect(await screen.findByRole('alert')).toHaveTextContent('Имя пользователя или пароль не приняты')
    expect(screen.getByLabelText('Имя пользователя')).toHaveValue('unknown')
    expect(screen.getByLabelText('Пароль')).toHaveValue('')
    expect(screen.getByLabelText('Пароль')).toHaveFocus()
    expect(screen.getByLabelText('Пароль')).toHaveAttribute('aria-invalid', 'true')
  })

  it('announces an expired-session message without exposing it as a credential error', () => {
    render(<LoginView message="Сессия истекла. Войдите снова." onAuthenticated={vi.fn()} />)
    expect(screen.getByRole('status')).toHaveTextContent('Сессия истекла')
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })
})
