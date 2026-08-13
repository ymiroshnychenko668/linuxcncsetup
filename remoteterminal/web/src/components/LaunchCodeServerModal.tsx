import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from 'react'
import { ApiError, api, type DirectoryListing, type LaunchCodeServerResult } from '../api'
import { AlertIcon, ChevronIcon, CodeServerIcon, FolderIcon, RefreshIcon } from '../icons'
import { Modal } from './Modal'

interface LaunchCodeServerModalProps {
  onClose: () => void
  onLaunched: (result: LaunchCodeServerResult) => void
}

interface Breadcrumb {
  label: string
  path: string
}

function breadcrumbs(path: string): Breadcrumb[] {
  if (path === '/') return [{ label: '/', path: '/' }]

  const parts = path.split('/').filter(Boolean)
  return [
    { label: '/', path: '/' },
    ...parts.map((label, index) => ({
      label,
      path: `/${parts.slice(0, index + 1).join('/')}`,
    })),
  ]
}

export function LaunchCodeServerModal({ onClose, onLaunched }: LaunchCodeServerModalProps) {
  const [listing, setListing] = useState<DirectoryListing | null>(null)
  const [pathInput, setPathInput] = useState('')
  const [loading, setLoading] = useState(true)
  const [submitting, setSubmitting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const requestRef = useRef<AbortController | null>(null)
  const inputRef = useRef<HTMLInputElement>(null)
  const focusedInitialDirectoryRef = useRef(false)

  const loadDirectory = useCallback(async (path?: string): Promise<DirectoryListing | null> => {
    requestRef.current?.abort()
    const controller = new AbortController()
    requestRef.current = controller
    setLoading(true)
    setError(null)
    try {
      const loaded = await api.getDirectories(path, controller.signal)
      if (requestRef.current !== controller) return null
      setListing(loaded)
      setPathInput(loaded.path)
      return loaded
    } catch (cause) {
      if (cause instanceof DOMException && cause.name === 'AbortError') return null
      if (requestRef.current === controller) {
        setError(cause instanceof ApiError ? cause.message : 'The folder could not be opened.')
      }
      return null
    } finally {
      if (requestRef.current === controller) {
        requestRef.current = null
        setLoading(false)
      }
    }
  }, [])

  useEffect(() => {
    void loadDirectory()
    return () => requestRef.current?.abort()
  }, [loadDirectory])

  useEffect(() => {
    if (!loading && listing && !focusedInitialDirectoryRef.current) {
      focusedInitialDirectoryRef.current = true
      inputRef.current?.focus()
    }
  }, [listing, loading])

  const browse = (event: FormEvent) => {
    event.preventDefault()
    const path = pathInput.trim()
    if (!path || loading || submitting) return
    void loadDirectory(path)
  }

  const launch = async () => {
    if (submitting || loading) return
    setSubmitting(true)
    setError(null)
    try {
      const requestedPath = pathInput.trim()
      let folderPath = listing?.path
      if (!requestedPath) {
        setError('Enter an absolute folder path.')
        setSubmitting(false)
        return
      }
      if (requestedPath !== folderPath) {
        const resolved = await loadDirectory(requestedPath)
        folderPath = resolved?.path
      }
      if (!folderPath) {
        setSubmitting(false)
        return
      }
      const result = await api.launchCodeServer(folderPath)
      onLaunched(result)
    } catch (cause) {
      setError(cause instanceof ApiError ? cause.message : 'Code Server could not be launched.')
      setSubmitting(false)
    }
  }

  const crumbs = useMemo(() => listing ? breadcrumbs(listing.path) : [], [listing])

  return (
    <Modal
      title="Launch Code Server"
      description="Choose a remote working folder. The editor keeps running after you close its browser tab."
      onClose={onClose}
      closeLabel="Close folder picker"
      className="modal--folder-picker"
      closeDisabled={submitting}
      footer={(
        <>
          <button className="button button--secondary" type="button" onClick={onClose} disabled={submitting}>
            Cancel
          </button>
          <button
            className="button button--code-primary"
            type="button"
            onClick={() => void launch()}
            disabled={loading || submitting || !pathInput.trim()}
          >
            {submitting ? <span className="spinner" aria-hidden="true" /> : <CodeServerIcon />}
            {submitting ? 'Launching…' : 'Launch in this folder'}
          </button>
        </>
      )}
    >
      <div className="folder-picker">
        <form className="folder-path-form" onSubmit={browse}>
          <label htmlFor="code-server-folder">Working folder</label>
          <div>
            <input
              ref={inputRef}
              id="code-server-folder"
              type="text"
              value={pathInput}
              onChange={(event) => setPathInput(event.target.value)}
              placeholder="/home/operator/project"
              autoComplete="off"
              spellCheck="false"
              disabled={submitting}
              required
            />
            <button className="button button--secondary button--compact" type="submit" disabled={!pathInput.trim() || loading || submitting}>
              {loading ? <span className="spinner" aria-hidden="true" /> : <RefreshIcon />}
              Browse
            </button>
          </div>
        </form>

        <nav className="folder-breadcrumbs" aria-label="Current folder" aria-busy={loading || undefined}>
          {listing ? crumbs.map((crumb, index) => (
            <span key={crumb.path}>
              {index > 0 ? <ChevronIcon width={13} height={13} /> : null}
              <button
                type="button"
                title={crumb.path}
                aria-current={crumb.path === listing.path ? 'location' : undefined}
                onClick={() => void loadDirectory(crumb.path)}
                disabled={loading || submitting || crumb.path === listing.path}
              >
                {crumb.label}
              </button>
            </span>
          )) : null}
        </nav>

        <div className="folder-list" aria-live="polite">
          {loading && !listing ? (
            <div className="folder-list__status" role="status">
              <span className="spinner" aria-hidden="true" /> Loading folders…
            </div>
          ) : listing ? (
            <>
              {listing.parentPath !== null ? (
                <button
                  className="folder-row folder-row--parent"
                  type="button"
                  onClick={() => void loadDirectory(listing.parentPath ?? '/')}
                  disabled={loading || submitting}
                >
                  <FolderIcon />
                  <span><strong>..</strong><small>Parent folder</small></span>
                  <ChevronIcon />
                </button>
              ) : null}
              {listing.directories.map((directory, index) => (
                <button
                  className="folder-row"
                  type="button"
                  key={`${directory.name}\u0000${directory.path}\u0000${index}`}
                  title={directory.path}
                  onClick={() => void loadDirectory(directory.path)}
                  disabled={loading || submitting}
                >
                  <FolderIcon />
                  <span><strong>{directory.name}</strong><small>{directory.path}</small></span>
                  <ChevronIcon />
                </button>
              ))}
              {!listing.directories.length ? <p className="folder-list__empty">No readable child folders.</p> : null}
            </>
          ) : null}
        </div>

        <div className="folder-picker__messages">
          {listing?.truncated ? (
            <p className="folder-picker__hint"><AlertIcon /> Only the first 1,000 folders are shown. Enter a full path above to open another.</p>
          ) : null}
          <p className="folder-picker__trust-note">
            Code Server, workspace code, and extensions run with the configured Linux account’s authority.
            Open only folders and extensions you trust.
          </p>
          {error ? <div className="notice notice--error" role="alert">{error}</div> : null}
        </div>
      </div>
    </Modal>
  )
}
