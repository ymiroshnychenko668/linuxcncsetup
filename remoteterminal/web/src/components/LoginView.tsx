import { useRef, useState, type FormEvent } from 'react'
import { ApiError, api, type AuthSession } from '../api'
import { LockIcon, TerminalIcon } from '../icons'

interface LoginViewProps {
  machineName: string
  onAuthenticated: (session: AuthSession) => void
  message?: string
}

export function isRemotePlainHTTP(location: Pick<Location, 'protocol' | 'hostname'> = window.location) {
  const hostname = location.hostname.toLowerCase()
  const loopback = hostname === 'localhost' || hostname === '127.0.0.1' || hostname === '::1' || hostname === '[::1]'
  return location.protocol === 'http:' && !loopback
}

export function isPlainHTTP(location: Pick<Location, 'protocol'> = window.location) {
  return location.protocol === 'http:'
}

export function LoginView({ machineName, onAuthenticated, message }: LoginViewProps) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const passwordRef = useRef<HTMLInputElement>(null)
  const plaintextTransport = isPlainHTTP()
  const remoteInsecureOrigin = isRemotePlainHTTP()

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    if (!username.trim() || !password || submitting) return

    setSubmitting(true)
    setError(null)
    try {
      const session = await api.login(username.trim(), password)
      if (!session.authenticated || !session.user) {
        throw new ApiError('Authentication failed.', 401, 'authentication_failed')
      }
      setPassword('')
      onAuthenticated(session)
    } catch (cause) {
      setPassword('')
      passwordRef.current?.focus()
      const status = cause instanceof ApiError
        ? cause.status
        : typeof cause === 'object' && cause !== null && 'status' in cause && typeof cause.status === 'number'
          ? cause.status
          : undefined
      if (status === 401 || status === 403 || status === 429) {
        setError(
          status === 429
            ? 'Too many attempts. Wait a moment and try again.'
            : 'The username or password was not accepted.',
        )
      } else {
        setError(cause instanceof Error ? cause.message : 'Sign in failed. Please try again.')
      }
    } finally {
      setSubmitting(false)
    }
  }

  return (
    <main className="login-page">
      <section className="login-intro" aria-label={`${machineName} Remote Terminal introduction`}>
        <div className="brand brand--large">
          <span className="brand__mark"><TerminalIcon width={24} height={24} /></span>
          <span className="brand__text">
            <span>Remote Terminal</span>
            <small>{machineName}</small>
          </span>
        </div>
        <div className="login-intro__copy">
          <p className="eyebrow">LinuxCNC workstation access</p>
          <h1>Your machine,<br />one focused workspace.</h1>
          <p>
            Resume persistent tmux sessions from any trusted browser on your local network.
          </p>
        </div>
        <div className="login-intro__security">
          <LockIcon />
          <span>{plaintextTransport ? 'Plain HTTP · Trusted isolated machine LAN only' : 'Encrypted connection · System account authentication'}</span>
        </div>
      </section>

      <section className="login-panel" aria-labelledby="login-title">
        <div className="login-card">
          <div className="login-card__heading">
            <p className="eyebrow">Welcome back</p>
            <h2 id="login-title">Sign in to {machineName}</h2>
            <p>Use the username and password for the configured Linux account.</p>
          </div>

          {message ? <div className="notice notice--info" role="status">{message}</div> : null}
          {plaintextTransport ? (
            <div className="notice notice--warning" role="alert">
              This connection is not encrypted. Your Linux account password and terminal traffic can be observed or changed on the network.
              {remoteInsecureOrigin
                ? ' Code Server webviews also require this origin to be explicitly treated as secure by the managed browser.'
                : null}
            </div>
          ) : null}
          {error ? <div className="notice notice--error" role="alert">{error}</div> : null}

          <form className="login-form" onSubmit={submit}>
            <label htmlFor="username">Username</label>
            <input
              id="username"
              name="username"
              type="text"
              autoComplete="username"
              autoCapitalize="none"
              spellCheck="false"
              value={username}
              onChange={(event) => setUsername(event.target.value)}
              disabled={submitting}
              autoFocus
              required
            />

            <label htmlFor="password">Password</label>
            <input
              ref={passwordRef}
              id="password"
              name="password"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={(event) => setPassword(event.target.value)}
              disabled={submitting}
              required
            />

            <button className="button button--primary button--large" type="submit" disabled={submitting}>
              {submitting ? <span className="spinner" aria-hidden="true" /> : <TerminalIcon />}
              {submitting ? 'Signing in…' : 'Open terminal workspace'}
            </button>
          </form>

          <p className="login-card__footnote">
            {plaintextTransport
              ? 'Credentials are sent once over plaintext HTTP and are never stored in this browser.'
              : 'Credentials are sent once over HTTPS and are never stored in this browser.'}
          </p>
        </div>
      </section>
    </main>
  )
}
