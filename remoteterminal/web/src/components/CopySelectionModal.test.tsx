import { useRef, useState } from 'react'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { CopySelectionModal } from './CopySelectionModal'

describe('CopySelectionModal', () => {
  it('shows exact text in a visible readonly field and keeps native copy retryable', async () => {
    const text = '\n  Київ\t😀\u00a0\nlast line  \n'
    const onClose = vi.fn()
    function Harness() {
      const [open, setOpen] = useState(false)
      const returnFocusRef = useRef<HTMLButtonElement>(null)
      return (
        <>
          <button ref={returnFocusRef} type="button" onClick={() => setOpen(true)}>Copy selection</button>
          {open ? (
            <CopySelectionModal
              text={text}
              returnFocusRef={returnFocusRef}
              onClose={() => {
                onClose()
                setOpen(false)
              }}
            />
          ) : null}
        </>
      )
    }
    render(<Harness />)
    const user = userEvent.setup()
    const trigger = screen.getByRole('button', { name: 'Copy selection' })
    trigger.focus()
    await user.click(trigger)

    const textarea = screen.getByRole('textbox', { name: 'Selected terminal text' }) as HTMLTextAreaElement
    expect(textarea).toBeVisible()
    expect(textarea).toHaveAttribute('readonly')
    expect(textarea.value).toBe(text)
    expect(textarea).toHaveFocus()
    expect(textarea.selectionStart).toBe(0)
    expect(textarea.selectionEnd).toBe(text.length)

    fireEvent.select(textarea, { target: { selectionStart: 3, selectionEnd: 7 } })
    await user.click(screen.getByRole('button', { name: 'Select all' }))
    expect(textarea.selectionStart).toBe(0)
    expect(textarea.selectionEnd).toBe(text.length)
    expect(onClose).not.toHaveBeenCalled()

    await user.click(screen.getByRole('button', { name: 'Close' }))
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(trigger).toHaveFocus())
  })
})
