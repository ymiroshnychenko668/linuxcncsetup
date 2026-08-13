import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Modal } from './Modal'

describe('Modal', () => {
  it('moves focus inside, contains Tab navigation, and restores prior focus', () => {
    const trigger = document.createElement('button')
    document.body.appendChild(trigger)
    trigger.focus()

    const { unmount } = render(
      <Modal
        title="Focused dialog"
        onClose={vi.fn()}
        footer={<button type="button">Last action</button>}
      >
        <input aria-label="Dialog input" />
      </Modal>,
    )

    const close = screen.getByRole('button', { name: 'Close dialog' })
    const last = screen.getByRole('button', { name: 'Last action' })
    expect(close).toHaveFocus()

    fireEvent.keyDown(window, { key: 'Tab', shiftKey: true })
    expect(last).toHaveFocus()
    fireEvent.keyDown(window, { key: 'Tab' })
    expect(close).toHaveFocus()

    unmount()
    expect(trigger).toHaveFocus()
    trigger.remove()
  })

  it('blocks every close path while close is disabled', () => {
    const onClose = vi.fn()
    const { container, rerender } = render(
      <Modal title="Busy dialog" onClose={onClose} closeDisabled>
        <button type="button">Working</button>
      </Modal>,
    )

    const backdrop = container.querySelector('.modal-backdrop') as HTMLElement
    expect(screen.getByRole('dialog')).toHaveAttribute('aria-busy', 'true')
    expect(screen.getByRole('button', { name: 'Close dialog' })).toBeDisabled()
    fireEvent.keyDown(window, { key: 'Escape' })
    fireEvent.mouseDown(backdrop)
    expect(onClose).not.toHaveBeenCalled()

    rerender(
      <Modal title="Busy dialog" onClose={onClose}>
        <button type="button">Finished</button>
      </Modal>,
    )
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})
