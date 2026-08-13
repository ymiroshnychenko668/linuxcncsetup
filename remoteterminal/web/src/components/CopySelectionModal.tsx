import { useCallback, useLayoutEffect, useRef, type RefObject } from 'react'
import { Modal } from './Modal'

interface CopySelectionModalProps {
  text: string
  onClose: () => void
  returnFocusRef: RefObject<HTMLElement>
  preparing?: boolean
  preparationWarning?: string | null
}

export function CopySelectionModal({
  text,
  onClose,
  returnFocusRef,
  preparing = false,
  preparationWarning = null,
}: CopySelectionModalProps) {
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const selectAll = useCallback(() => {
    const textarea = textareaRef.current
    if (!textarea) return
    textarea.focus()
    textarea.select()
  }, [])

  useLayoutEffect(() => {
    selectAll()
  }, [selectAll, text])

  return (
    <Modal
      title="Copy terminal selection"
      description="The complete selection is ready. Press Ctrl+C or Command+C, or use the native context menu or long-press Copy command."
      className="modal--copy-selection"
      initialFocusRef={textareaRef}
      returnFocusRef={returnFocusRef}
      onClose={onClose}
      closeDisabled={preparing}
      footer={(
        <>
          <button className="button button--secondary" type="button" onClick={selectAll}>Select all</button>
          <button className="button button--primary" type="button" onClick={onClose} disabled={preparing}>Close</button>
        </>
      )}
    >
      <label className="copy-selection-field">
        <span>Selected terminal text</span>
        <textarea
          ref={textareaRef}
          className="copy-selection-textarea"
          value={text}
          readOnly
          spellCheck={false}
          wrap="off"
        />
      </label>
      {preparing ? (
        <p className="modal-note" role="status">Clearing older tmux selections. You can copy now; Close unlocks when this finishes.</p>
      ) : null}
      {preparationWarning ? <p className="form-alert" role="alert">{preparationWarning}</p> : null}
      <p className="modal-note">If the selection changes before you copy, close this dialog and click Copy selection again.</p>
    </Modal>
  )
}
