import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ApiError, login, type AuthSession } from '../api'

interface LoginViewProps {
  message?: string
  onAuthenticated: (session: AuthSession) => void
}

const isRemotePlainHTTP = (location: Pick<Location, 'protocol' | 'hostname'> = window.location): boolean => {
  const hostname = location.hostname.toLowerCase()
  const loopback = hostname === 'localhost'
    || hostname === '127.0.0.1'
    || hostname === '::1'
    || hostname === '[::1]'
  return location.protocol === 'http:' && !loopback
}

export function LoginView({ message, onAuthenticated }: LoginViewProps) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [rememberMe, setRememberMe] = useState(false)
  const [error, setError] = useState<string>()
  const [submitting, setSubmitting] = useState(false)
  const passwordRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    if (error && !submitting) passwordRef.current?.focus()
  }, [error, submitting])

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const normalizedUsername = username.trim()
    if (!normalizedUsername || !password || submitting) return

    setSubmitting(true)
    setError(undefined)
    const rejectLogin = (reason: unknown) => {
      setPassword('')
      if (reason instanceof ApiError && reason.status === 429) {
        setError('Слишком много попыток входа. Подождите немного и повторите.')
      } else if (reason instanceof ApiError && (reason.status === 401 || reason.status === 403)) {
        setError('Имя пользователя или пароль не приняты.')
      } else {
        setError(reason instanceof Error ? reason.message : 'Не удалось войти. Повторите попытку.')
      }
    }
    void Promise.resolve(login(normalizedUsername, password, rememberMe)).then((session) => {
      if (!session.authenticated) {
        throw new ApiError({
          message: 'Authentication failed.',
          status: 401,
          code: 'AUTHENTICATION_FAILED',
        })
      }
      setPassword('')
      onAuthenticated(session)
    }).catch((reason: unknown) => {
      rejectLogin(reason)
    }).finally(() => {
      setSubmitting(false)
    })
  }

  return (
    <section className="login-view" aria-labelledby="login-title">
      <div className="login-view__intro">
        <p className="eyebrow">Доступ к рабочему месту</p>
        <h1>Производственные сетапы в одном управляемом пространстве</h1>
        <p>
          Войдите под своей учётной записью Linux, чтобы открыть библиотеку,
          карточки сетапов и безопасный просмотр программ.
        </p>
        <div className="login-view__security">
          <span aria-hidden="true">●</span>
          {isRemotePlainHTTP()
            ? 'Соединение не зашифровано. Не вводите пароль вне доверенной изолированной сети.'
            : 'Защищённая сессия · пароль не сохраняется приложением'}
        </div>
      </div>

      <div className="login-card">
        <div className="login-card__heading">
          <p className="eyebrow">Web Setup Manager</p>
          <h2 id="login-title">Вход в систему</h2>
          <p>Используйте имя пользователя и пароль Linux этого станка.</p>
        </div>

        {message ? <div className="auth-notice auth-notice--info" role="status">{message}</div> : null}
        {isRemotePlainHTTP() ? (
          <div className="auth-notice auth-notice--warning" role="alert">
            Пароль и данные сессии могут быть перехвачены или изменены в сети.
          </div>
        ) : null}
        {error ? <div id="login-error" className="auth-notice auth-notice--error" role="alert">{error}</div> : null}

        <form className="login-form" aria-busy={submitting} onSubmit={submit}>
          <label htmlFor="login-username">Имя пользователя</label>
          <input
            id="login-username"
            name="username"
            type="text"
            autoComplete="username"
            autoCapitalize="none"
            spellCheck={false}
            value={username}
            disabled={submitting}
            autoFocus
            required
            onChange={(event) => setUsername(event.target.value)}
          />

          <label htmlFor="login-password">Пароль</label>
          <input
            ref={passwordRef}
            id="login-password"
            name="password"
            type="password"
            autoComplete="current-password"
            value={password}
            disabled={submitting}
            required
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? 'login-error' : undefined}
            onChange={(event) => setPassword(event.target.value)}
          />

          <label className="remember-option" htmlFor="login-remember">
            <input
              id="login-remember"
              name="rememberMe"
              type="checkbox"
              checked={rememberMe}
              disabled={submitting}
              onChange={(event) => setRememberMe(event.target.checked)}
            />
            <span>
              <strong>Запомнить меня</strong>
              <small>Сохранить вход после закрытия браузера на этом доверенном устройстве.</small>
            </span>
          </label>

          <button className="button button--primary login-form__submit" type="submit" disabled={submitting}>
            {submitting ? <span className="spinner spinner--small" aria-hidden="true" /> : null}
            {submitting ? 'Выполняется вход…' : 'Открыть библиотеку сетапов'}
          </button>
        </form>

        <p className="login-card__footnote">
          Пароль передаётся только для проверки PAM и не сохраняется Web Setup Manager.
        </p>
      </div>
    </section>
  )
}
