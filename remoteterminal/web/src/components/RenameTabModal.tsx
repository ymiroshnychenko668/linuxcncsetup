import { useEffect, useRef, useState, type FormEvent } from 'react'
import { EditIcon } from '../icons'
import { Modal } from './Modal'

interface RenameTabModalProps {
  currentName: string
  defaultName: string
  onClose: () => void
  onRename: (name: string) => void
}

export function RenameTabModal({ currentName, defaultName, onClose, onRename }: RenameTabModalProps) {
  const [name, setName] = useState(currentName)
  const inputRef = useRef<HTMLInputElement>(null)
  const normalized = name.trim()

  useEffect(() => {
    inputRef.current?.focus()
    inputRef.current?.select()
  }, [])

  const submit = (event: FormEvent) => {
    event.preventDefault()
    if (!normalized) return
    onRename(normalized)
  }

  return (
    <Modal
      title={`Rename “${currentName}” tab`}
      description="This changes only the browser tab label; the running terminal session or Code Server keeps its original name."
      onClose={onClose}
      initialFocusRef={inputRef}
      footer={(
        <>
          <button className="button button--secondary" type="button" onClick={onClose}>
            Cancel
          </button>
          <button className="button button--primary" type="submit" form="rename-tab-form" disabled={!normalized}>
            <EditIcon /> Rename tab
          </button>
        </>
      )}
    >
      <form id="rename-tab-form" className="form-stack" onSubmit={submit}>
        <label htmlFor="tab-name">Tab name</label>
        <input
          ref={inputRef}
          id="tab-name"
          type="text"
          value={name}
          onChange={(event) => setName(event.target.value)}
          maxLength={48}
          autoComplete="off"
          spellCheck="false"
          required
        />
        <p className="field-hint">The name cannot be empty. Default: {defaultName}</p>
      </form>
    </Modal>
  )
}
