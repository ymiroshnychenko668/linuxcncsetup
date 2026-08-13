import { useState } from 'react'
import { ApiError, api, type CodeServerInstance } from '../api'
import { CodeServerIcon } from '../icons'
import { Modal } from './Modal'

interface ShutdownCodeServerModalProps {
  codeServer: CodeServerInstance
  onClose: () => void
  onShutdown: (id: string) => void
}

export function ShutdownCodeServerModal({ codeServer, onClose, onShutdown }: ShutdownCodeServerModalProps) {
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const shutdown = async () => {
    setSubmitting(true)
    setError(null)
    try {
      await api.shutdownCodeServer(codeServer.id)
      onShutdown(codeServer.id)
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : 'Code Server could not be shut down.')
      setSubmitting(false)
    }
  }

  return (
    <Modal
      title={`Shut down “${codeServer.name}”?`}
      description="Closing its browser tab leaves the editor running; this action stops it."
      onClose={onClose}
      closeDisabled={submitting}
      footer={(
        <>
          <button className="button button--secondary" type="button" onClick={onClose} disabled={submitting}>
            Keep running
          </button>
          <button className="button button--danger" type="button" onClick={() => void shutdown()} disabled={submitting}>
            {submitting ? <span className="spinner" aria-hidden="true" /> : <CodeServerIcon />}
            {submitting ? 'Shutting down…' : 'Shut down Code Server'}
          </button>
        </>
      )}
    >
      <div className="delete-warning code-server-warning">
        <CodeServerIcon />
        <p>
          The editor for <strong>{codeServer.folderPath}</strong> and processes owned by that Code Server instance will be stopped.
          Its persistent editor profile will be kept.
        </p>
      </div>
      {error ? <div className="notice notice--error" role="alert">{error}</div> : null}
    </Modal>
  )
}
