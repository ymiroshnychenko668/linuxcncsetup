import {
  useEffect,
  useId,
  useRef,
  type ReactNode,
  type RefObject,
} from 'react'
import { createPortal } from 'react-dom'

const focusableSelector = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  '[tabindex]:not([tabindex="-1"])',
].join(',')

function focusableElements(container: HTMLElement): HTMLElement[] {
  return Array.from(container.querySelectorAll<HTMLElement>(focusableSelector))
    .filter((element) => !element.hidden && element.getAttribute('aria-hidden') !== 'true')
}

export interface ModalProps {
  title: string
  children: ReactNode
  onClose: () => void
  description?: string
  footer?: ReactNode
  closeLabel?: string
  closeDisabled?: boolean
  initialFocusRef?: RefObject<HTMLElement>
  returnFocusRef?: RefObject<HTMLElement>
  className?: string
}

export function Modal({
  title,
  children,
  onClose,
  description,
  footer,
  closeLabel = 'Закрыть диалог',
  closeDisabled = false,
  initialFocusRef,
  returnFocusRef,
  className = '',
}: ModalProps) {
  const titleId = useId()
  const descriptionId = useId()
  const dialogRef = useRef<HTMLElement>(null)
  const onCloseRef = useRef(onClose)
  const closeDisabledRef = useRef(closeDisabled)
  const previouslyFocusedRef = useRef<HTMLElement | null | undefined>(undefined)

  // React applies a descendant's autoFocus during the commit before this
  // component's effect runs. Capture the initiator while rendering so the
  // portal cannot replace it before we remember where focus must return.
  if (previouslyFocusedRef.current === undefined) {
    previouslyFocusedRef.current = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null
  }

  onCloseRef.current = onClose
  closeDisabledRef.current = closeDisabled

  useEffect(() => {
    const explicitReturnTarget = returnFocusRef?.current
    const previouslyFocused = explicitReturnTarget
      ?? previouslyFocusedRef.current
    const previousOverflow = document.body.style.overflow
    document.body.style.overflow = 'hidden'

    const focusFirst = () => {
      const dialog = dialogRef.current
      if (!dialog) return
      const requested = initialFocusRef?.current
      if (requested && dialog.contains(requested)) {
        requested.focus()
        return
      }
      ;(focusableElements(dialog)[0] ?? dialog).focus()
    }

    const onKeyDown = (event: KeyboardEvent) => {
      const dialog = dialogRef.current
      if (!dialog) return

      if (event.key === 'Escape') {
        event.preventDefault()
        if (!closeDisabledRef.current) onCloseRef.current()
        return
      }
      if (event.key !== 'Tab') return

      const focusable = focusableElements(dialog)
      if (focusable.length === 0) {
        event.preventDefault()
        dialog.focus()
        return
      }

      const first = focusable[0]
      const last = focusable[focusable.length - 1]
      const active = document.activeElement
      const focusIsInside = active instanceof Node && dialog.contains(active)

      if (event.shiftKey && (active === first || !focusIsInside)) {
        event.preventDefault()
        last.focus()
      } else if (!event.shiftKey && (active === last || !focusIsInside)) {
        event.preventDefault()
        first.focus()
      }
    }

    const onFocusIn = (event: FocusEvent) => {
      const dialog = dialogRef.current
      if (dialog && event.target instanceof Node && !dialog.contains(event.target)) {
        focusFirst()
      }
    }

    document.addEventListener('keydown', onKeyDown)
    document.addEventListener('focusin', onFocusIn)
    focusFirst()

    return () => {
      document.removeEventListener('keydown', onKeyDown)
      document.removeEventListener('focusin', onFocusIn)
      document.body.style.overflow = previousOverflow
      const returnTarget = explicitReturnTarget ?? previouslyFocused
      if (returnTarget?.isConnected) {
        returnTarget.focus()
        return
      }
      // A successful mutation may legitimately replace the button that opened
      // the dialog. Never leave focus attached to the removed portal subtree;
      // return to the stable application landmark in that case.
      const applicationMain = document.getElementById('catalog-editor')
        ?? document.getElementById('main-content')
      if (applicationMain instanceof HTMLElement && applicationMain.isConnected) {
        applicationMain.focus()
      }
    }
  }, [initialFocusRef, returnFocusRef])

  return createPortal(
    <div
      className="modal-backdrop"
      role="presentation"
      onMouseDown={(event) => {
        if (event.target === event.currentTarget && !closeDisabled) onClose()
      }}
    >
      <section
        ref={dialogRef}
        className={`modal ${className}`.trim()}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={description ? descriptionId : undefined}
        aria-busy={closeDisabled || undefined}
        tabIndex={-1}
      >
        <header className="modal__header">
          <div>
            <h2 id={titleId}>{title}</h2>
            {description ? <p id={descriptionId}>{description}</p> : null}
          </div>
          <button
            className="icon-button"
            type="button"
            aria-label={closeLabel}
            onClick={onClose}
            disabled={closeDisabled}
          >
            <span aria-hidden="true">×</span>
          </button>
        </header>
        <div className="modal__body">{children}</div>
        {footer ? <footer className="modal__footer">{footer}</footer> : null}
      </section>
    </div>,
    document.body,
  )
}
