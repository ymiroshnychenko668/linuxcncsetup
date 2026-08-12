import { useState } from 'react'
import { ApiError, api, type TerminalSession } from '../api'
import { TrashIcon } from '../icons'
import { Modal } from './Modal'

interface DeleteSessionModalProps {
  session: TerminalSession
  onClose: () => void
  onDeleted: (id: string) => void
}

export function DeleteSessionModal({ session, onClose, onDeleted }: DeleteSessionModalProps) {
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const remove = async () => {
    setSubmitting(true)
    setError(null)
    try {
      await api.deleteSession(session.id)
      onDeleted(session.id)
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : 'The session could not be deleted.')
      setSubmitting(false)
    }
  }

  return (
    <Modal
      title={`Delete “${session.name}”?`}
      description="This is different from closing a browser tab."
      onClose={onClose}
      footer={(
        <>
          <button className="button button--secondary" type="button" onClick={onClose} disabled={submitting}>
            Keep session
          </button>
          <button className="button button--danger" type="button" onClick={() => void remove()} disabled={submitting}>
            {submitting ? <span className="spinner" aria-hidden="true" /> : <TrashIcon />}
            {submitting ? 'Deleting…' : 'Delete tmux session'}
          </button>
        </>
      )}
    >
      <div className="delete-warning">
        <TrashIcon />
        <p>
          The shell and every process running inside <strong>{session.name}</strong> will be stopped.
          This cannot be undone.
        </p>
      </div>
      {error ? <div className="notice notice--error" role="alert">{error}</div> : null}
    </Modal>
  )
}
