import { useEffect, useId, type ReactNode } from 'react'
import { XIcon } from '../icons'

interface ModalProps {
  title: string
  description?: string
  children: ReactNode
  footer?: ReactNode
  onClose: () => void
  closeLabel?: string
}

export function Modal({
  title,
  description,
  children,
  footer,
  onClose,
  closeLabel = 'Close dialog',
}: ModalProps) {
  const titleId = useId()
  const descriptionId = useId()

  useEffect(() => {
    const previouslyFocused = document.activeElement as HTMLElement | null
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onClose()
    }
    window.addEventListener('keydown', onKeyDown)
    return () => {
      window.removeEventListener('keydown', onKeyDown)
      previouslyFocused?.focus()
    }
  }, [onClose])

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.target === event.currentTarget) onClose()
    }}>
      <section
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
      >
        <header className="modal__header">
          <div>
            <h2 id={titleId}>{title}</h2>
            {description ? <p id={descriptionId}>{description}</p> : null}
          </div>
          <button className="icon-button" type="button" aria-label={closeLabel} onClick={onClose}>
            <XIcon />
          </button>
        </header>
        <div className="modal__body">{children}</div>
        {footer ? <footer className="modal__footer">{footer}</footer> : null}
      </section>
    </div>
  )
}
