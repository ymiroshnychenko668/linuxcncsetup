import { useEffect, useRef, useState, type FormEvent } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import Checkbox from '@mui/material/Checkbox'
import CircularProgress from '@mui/material/CircularProgress'
import FormControlLabel from '@mui/material/FormControlLabel'
import TextField from '@mui/material/TextField'
import { ApiError, login, type AuthSession } from '../api'

interface LoginViewProps {
  message?: string
  onAuthenticated: (session: AuthSession, cacheAuthGeneration?: string) => void
	captureAuthGeneration?: () => string | Promise<string>
}

const isRemotePlainHTTP = (location: Pick<Location, 'protocol' | 'hostname'> = window.location): boolean => {
  const hostname = location.hostname.toLowerCase()
  const loopback = hostname === 'localhost'
    || hostname === '127.0.0.1'
    || hostname === '::1'
    || hostname === '[::1]'
  return location.protocol === 'http:' && !loopback
}

export function LoginView({ message, onAuthenticated, captureAuthGeneration }: LoginViewProps) {
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
		void Promise.resolve().then(() => captureAuthGeneration?.()).then(async (cacheAuthGeneration) => ({
			cacheAuthGeneration,
			session: await login(normalizedUsername, password, rememberMe),
		})).then(({ session, cacheAuthGeneration }) => {
      if (!session.authenticated) {
        throw new ApiError({
          message: 'Authentication failed.',
          status: 401,
          code: 'AUTHENTICATION_FAILED',
        })
      }
      setPassword('')
			if (cacheAuthGeneration === undefined) onAuthenticated(session)
			else onAuthenticated(session, cacheAuthGeneration)
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
        <h1>Каталог программ этого станка</h1>
        <p>
          Войдите под своей учётной записью Linux, чтобы загружать программы
          в каталог LinuxCNC, группировать сетапы и просматривать G-code.
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

        {message ? <Alert className="auth-notice auth-notice--info" severity="info" role="status">{message}</Alert> : null}
        {isRemotePlainHTTP() ? (
          <Alert className="auth-notice auth-notice--warning" severity="warning" role="alert">
            Пароль и данные сессии могут быть перехвачены или изменены в сети.
          </Alert>
        ) : null}
        {error ? <Alert id="login-error" className="auth-notice auth-notice--error" severity="error" role="alert">{error}</Alert> : null}

        <form className="login-form" aria-busy={submitting} onSubmit={submit}>
          <TextField
            id="login-username"
            name="username"
            label="Имя пользователя"
            type="text"
            autoComplete="username"
            value={username}
            disabled={submitting}
            autoFocus
            fullWidth
            margin="normal"
            slotProps={{ htmlInput: { autoCapitalize: 'none', required: true, spellCheck: false } }}
            onChange={(event) => setUsername(event.target.value)}
          />

          <TextField
            inputRef={passwordRef}
            id="login-password"
            name="password"
            label="Пароль"
            type="password"
            autoComplete="current-password"
            value={password}
            disabled={submitting}
            error={Boolean(error)}
            fullWidth
            margin="normal"
            slotProps={{ htmlInput: { 'aria-describedby': error ? 'login-error' : undefined, required: true } }}
            onChange={(event) => setPassword(event.target.value)}
          />

          <FormControlLabel
            className="remember-option"
            disabled={submitting}
            control={(
              <Checkbox
                id="login-remember"
                name="rememberMe"
                checked={rememberMe}
                disableRipple
                size="small"
                sx={{ alignSelf: 'flex-start', marginTop: '0.2rem', padding: 0 }}
                onChange={(event) => setRememberMe(event.target.checked)}
              />
            )}
            label={(
              <>
              <strong>Запомнить меня</strong>
              <small>Сохранить вход после закрытия браузера на этом доверенном устройстве.</small>
              </>
            )}
          />

          <Button
            className="button button--primary login-form__submit"
            variant="contained"
            type="submit"
            disabled={submitting}
          >
            {submitting ? <CircularProgress aria-hidden="true" color="inherit" size={16} /> : null}
            {submitting ? 'Выполняется вход…' : 'Открыть каталог сетапов'}
          </Button>
        </form>

        <p className="login-card__footnote">
          Пароль передаётся только для проверки PAM и не сохраняется Web Setup Manager.
        </p>
      </div>
    </section>
  )
}
