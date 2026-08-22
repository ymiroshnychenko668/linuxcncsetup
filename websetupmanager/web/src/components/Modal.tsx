import {
  useEffect,
  useId,
  useRef,
  type ReactNode,
  type RefObject,
} from 'react'
import Dialog from '@mui/material/Dialog'
import DialogActions from '@mui/material/DialogActions'
import DialogContent from '@mui/material/DialogContent'
import DialogTitle from '@mui/material/DialogTitle'
import Box from '@mui/material/Box'
import IconButton from '@mui/material/IconButton'
import Typography from '@mui/material/Typography'

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
  const dialogRef = useRef<HTMLDivElement | null>(null)
  const previouslyFocusedRef = useRef<HTMLElement | null | undefined>(undefined)

  // React applies a descendant's autoFocus during the commit before this
  // component's effect runs. Capture the initiator while rendering so the
  // portal cannot replace it before we remember where focus must return.
  if (previouslyFocusedRef.current === undefined) {
    previouslyFocusedRef.current = document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null
  }

  useEffect(() => {
    const explicitReturnTarget = returnFocusRef?.current
    const previouslyFocused = explicitReturnTarget
      ?? previouslyFocusedRef.current
    const focusFirst = (): boolean => {
      const labelledElement = document.getElementById(titleId)
      const dialog = dialogRef.current
        ?? labelledElement?.closest<HTMLDivElement>('[role="dialog"]')
      if (!dialog) return false
      dialogRef.current = dialog
      const requested = initialFocusRef?.current
      if (requested && dialog.contains(requested)) {
        requested.focus()
        return true
      }
      ;(focusableElements(dialog)[0] ?? dialog).focus()
      return true
    }

    const onKeyDown = (event: KeyboardEvent) => {
      const dialog = dialogRef.current
      if (!dialog) return

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
    const focusTimer = focusFirst() ? undefined : window.setTimeout(focusFirst, 0)

    return () => {
      if (focusTimer !== undefined) window.clearTimeout(focusTimer)
      document.removeEventListener('keydown', onKeyDown)
      document.removeEventListener('focusin', onFocusIn)
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
  }, [initialFocusRef, returnFocusRef, titleId])

  return (
    <Dialog
      open
      maxWidth={false}
      scroll="paper"
      transitionDuration={0}
      disableAutoFocus
      disableEnforceFocus
      disableRestoreFocus
      aria-labelledby={titleId}
      aria-describedby={description ? descriptionId : undefined}
      onClose={(_event, reason) => {
        if (closeDisabled && (reason === 'escapeKeyDown' || reason === 'backdropClick')) return
        onClose()
      }}
      slotProps={{
        backdrop: { className: 'modal-backdrop' },
        container: { className: 'modal-container' },
        paper: {
          ref: dialogRef,
          className: `modal ${className}`.trim(),
          'aria-busy': closeDisabled || undefined,
          sx: { m: { xs: 0, sm: 2 } },
        },
      }}
    >
      <Box component="header" className="modal__header" sx={{ borderBottom: 1, borderColor: 'divider' }}>
        <div>
          <DialogTitle id={titleId} component="h2" sx={{ p: 0 }}>{title}</DialogTitle>
          {description ? <Typography id={descriptionId} component="p" variant="body2" color="text.secondary">{description}</Typography> : null}
        </div>
        <IconButton
          className="icon-button"
          type="button"
          aria-label={closeLabel}
          onClick={onClose}
          disabled={closeDisabled}
          sx={{ color: 'text.primary', backgroundColor: 'action.hover' }}
        >
          <span aria-hidden="true">×</span>
        </IconButton>
      </Box>
      <DialogContent className="modal__body">{children}</DialogContent>
      {footer ? <DialogActions className="modal__footer" sx={{ borderTop: 1, borderColor: 'divider', backgroundColor: 'background.default', '& > :not(style) ~ :not(style)': { marginLeft: 0 } }}>{footer}</DialogActions> : null}
    </Dialog>
  )
}
