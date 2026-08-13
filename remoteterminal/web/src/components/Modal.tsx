import { useEffect, useId, useRef, type ReactNode, type RefObject } from 'react'
import { XIcon } from '../icons'

interface ModalProps {
  title: string
  description?: string
  children: ReactNode
  footer?: ReactNode
  onClose: () => void
  closeLabel?: string
  className?: string
  closeDisabled?: boolean
  initialFocusRef?: RefObject<HTMLElement>
  returnFocusRef?: RefObject<HTMLElement>
}

export function Modal({
  title,
  description,
  children,
  footer,
  onClose,
  closeLabel = 'Close dialog',
  className = '',
  closeDisabled = false,
  initialFocusRef,
  returnFocusRef,
}: ModalProps) {
  const titleId = useId()
  const descriptionId = useId()
  const dialogRef = useRef<HTMLElement>(null)
  const onCloseRef = useRef(onClose)
  const closeDisabledRef = useRef(closeDisabled)
  onCloseRef.current = onClose
  closeDisabledRef.current = closeDisabled

  useEffect(() => {
    const previouslyFocused = returnFocusRef?.current ?? (document.activeElement as HTMLElement | null)
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === 'Escape') {
        event.preventDefault()
        if (!closeDisabledRef.current) onCloseRef.current()
        return
      }
      if (event.key !== 'Tab') return

      const dialog = dialogRef.current
      if (!dialog) return
      const focusable = Array.from(dialog.querySelectorAll<HTMLElement>(
        'a[href], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
      ))
      if (focusable.length === 0) {
        event.preventDefault()
        dialog.focus()
        return
      }

      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      const active = document.activeElement
      const activeIsFocusable = focusable.some((element) => element === active)
      if (event.shiftKey && (active === first || !activeIsFocusable)) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && (active === last || !activeIsFocusable)) {
        event.preventDefault()
        first.focus()
      }
    }
    window.addEventListener('keydown', onKeyDown)

    const dialog = dialogRef.current
    const requestedInitialFocus = initialFocusRef?.current
    const initialFocus = requestedInitialFocus && dialog?.contains(requestedInitialFocus)
      ? requestedInitialFocus
      : dialog?.querySelector<HTMLElement>(
          '[autofocus], button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), [tabindex]:not([tabindex="-1"])',
        )
    if (initialFocus) initialFocus.focus()
    else dialog?.focus()

    return () => {
      window.removeEventListener('keydown', onKeyDown)
      previouslyFocused?.focus()
    }
  }, [initialFocusRef, returnFocusRef])

  return (
    <div className="modal-backdrop" role="presentation" onMouseDown={(event) => {
      if (event.target === event.currentTarget && !closeDisabled) onClose()
    }}>
      <section
        ref={dialogRef}
        className={`modal ${className}`.trim()}
        role="dialog"
        aria-modal="true"
        aria-busy={closeDisabled || undefined}
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        tabIndex={-1}
      >
        <header className="modal__header">
          <div>
            <h2 id={titleId}>{title}</h2>
            {description ? <p id={descriptionId}>{description}</p> : null}
          </div>
          <button className="icon-button" type="button" aria-label={closeLabel} onClick={onClose} disabled={closeDisabled}>
            <XIcon />
          </button>
        </header>
        <div className="modal__body">{children}</div>
        {footer ? <footer className="modal__footer">{footer}</footer> : null}
      </section>
    </div>
  )
}
