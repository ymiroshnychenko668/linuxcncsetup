import { useEffect, useRef, useState, type FormEvent } from 'react'
import { ApiError, api, type TerminalSession } from '../api'
import { PlusIcon } from '../icons'
import { Modal } from './Modal'

interface CreateSessionModalProps {
  onClose: () => void
  onCreated: (session: TerminalSession) => void
}

export function CreateSessionModal({ onClose, onCreated }: CreateSessionModalProps) {
  const [name, setName] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [submitting, setSubmitting] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => inputRef.current?.focus(), [])

  const submit = async (event: FormEvent) => {
    event.preventDefault()
    const normalized = name.trim()
    if (!normalized || submitting) return

    setSubmitting(true)
    setError(null)
    try {
      const session = await api.createSession(normalized)
      onCreated(session)
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : 'The session could not be created.')
      setSubmitting(false)
    }
  }

  return (
    <Modal
      title="Create a terminal session"
      description="The tmux session keeps running when you close its browser tab."
      onClose={onClose}
      footer={(
        <>
          <button className="button button--secondary" type="button" onClick={onClose} disabled={submitting}>
            Cancel
          </button>
          <button className="button button--primary" type="submit" form="create-session-form" disabled={!name.trim() || submitting}>
            {submitting ? <span className="spinner" aria-hidden="true" /> : <PlusIcon />}
            {submitting ? 'Creating…' : 'Create and open'}
          </button>
        </>
      )}
    >
      <form id="create-session-form" className="form-stack" onSubmit={submit}>
        <label htmlFor="session-name">Session name</label>
        <input
          ref={inputRef}
          id="session-name"
          type="text"
          value={name}
          onChange={(event) => setName(event.target.value)}
          maxLength={48}
          autoComplete="off"
          spellCheck="false"
          placeholder="for example: diagnostics"
          disabled={submitting}
          required
        />
        <p className="field-hint">Choose a short name that identifies the task running in this shell.</p>
        {error ? <div className="notice notice--error" role="alert">{error}</div> : null}
      </form>
    </Modal>
  )
}
