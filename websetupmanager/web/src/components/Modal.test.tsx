import { createRef } from 'react'
import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import { Modal } from './Modal'

describe('Modal', () => {
  it('labels the dialog, traps Tab navigation, and restores focus', () => {
    const trigger = document.createElement('button')
    trigger.textContent = 'Открыть'
    document.body.appendChild(trigger)
    trigger.focus()

    const { unmount } = render(
      <Modal
        title="Новый сетап"
        description="Введите метаданные."
        onClose={vi.fn()}
        footer={<button type="button">Создать</button>}
      >
        <input aria-label="Название" />
      </Modal>,
    )

    const dialog = screen.getByRole('dialog', { name: 'Новый сетап' })
    const closeButton = screen.getByRole('button', { name: 'Закрыть диалог' })
    const finalButton = screen.getByRole('button', { name: 'Создать' })
    expect(dialog).toHaveAccessibleDescription('Введите метаданные.')
    expect(closeButton).toHaveFocus()

    fireEvent.keyDown(document, { key: 'Tab', shiftKey: true })
    expect(finalButton).toHaveFocus()
    fireEvent.keyDown(document, { key: 'Tab' })
    expect(closeButton).toHaveFocus()

    unmount()
    expect(trigger).toHaveFocus()
    trigger.remove()
  })

  it('honors explicit initial and return focus targets', () => {
    const returnTarget = document.createElement('button')
    returnTarget.textContent = 'Вернуться сюда'
    document.body.appendChild(returnTarget)
    const returnFocusRef = { current: returnTarget }
    const initialTarget = createRef<HTMLInputElement>()
    const { unmount } = render(
      <Modal
        title="Переименование"
        onClose={vi.fn()}
        initialFocusRef={initialTarget}
        returnFocusRef={returnFocusRef}
      >
        <input ref={initialTarget} aria-label="Новое имя" />
      </Modal>,
    )

    expect(screen.getByRole('textbox', { name: 'Новое имя' })).toHaveFocus()
    unmount()
    expect(returnTarget).toHaveFocus()
    returnTarget.remove()
  })

  it('captures the initiator before a portal child applies autoFocus', () => {
    const trigger = document.createElement('button')
    trigger.textContent = 'Загрузить'
    document.body.appendChild(trigger)
    trigger.focus()

    const { unmount } = render(
      <Modal title="Загрузить сетап" onClose={vi.fn()}>
        <input aria-label="Название сетапа" autoFocus />
      </Modal>,
    )

    expect(screen.getByRole('button', { name: 'Закрыть диалог' })).toHaveFocus()
    unmount()
    expect(trigger).toHaveFocus()
    trigger.remove()
  })

  it('prefers the stable catalog editor when the initiating control was removed', () => {
    const main = document.createElement('main')
    main.id = 'main-content'
    main.tabIndex = -1
    const editor = document.createElement('section')
    editor.id = 'catalog-editor'
    editor.tabIndex = -1
    const trigger = document.createElement('button')
    main.append(editor, trigger)
    document.body.appendChild(main)
    trigger.focus()

    const { unmount } = render(<Modal title="Загрузка" onClose={vi.fn()}><input autoFocus /></Modal>)
    trigger.remove()
    unmount()

    expect(editor).toHaveFocus()
    main.remove()
  })

  it('returns focus to the application landmark when a mutation replaces its initiator', () => {
    const main = document.createElement('main')
    main.id = 'main-content'
    main.tabIndex = -1
    const trigger = document.createElement('button')
    main.appendChild(trigger)
    document.body.appendChild(main)
    trigger.focus()
    const { unmount } = render(<Modal title="Архивирование" onClose={vi.fn()}><button type="button">Подтвердить</button></Modal>)

    trigger.remove()
    unmount()
    expect(main).toHaveFocus()
    main.remove()
  })

  it('supports Escape and backdrop closing, but blocks both while busy', () => {
    const onClose = vi.fn()
    const { container, rerender } = render(
      <Modal title="Импорт" onClose={onClose} closeDisabled>
        <button type="button">Отмена</button>
      </Modal>,
    )

    const backdrop = document.querySelector('.modal-backdrop') as HTMLElement
    expect(screen.getByRole('dialog')).toHaveAttribute('aria-busy', 'true')
    fireEvent.keyDown(document, { key: 'Escape' })
    fireEvent.mouseDown(backdrop)
    expect(onClose).not.toHaveBeenCalled()

    rerender(
      <Modal title="Импорт" onClose={onClose}>
        <button type="button">Отмена</button>
      </Modal>,
    )
    fireEvent.keyDown(document, { key: 'Escape' })
    expect(onClose).toHaveBeenCalledTimes(1)
    fireEvent.mouseDown(container.ownerDocument.querySelector('.modal-backdrop') as HTMLElement)
    expect(onClose).toHaveBeenCalledTimes(2)
  })

  it('brings escaped focus back into the dialog', () => {
    const outside = document.createElement('button')
    document.body.appendChild(outside)
    render(
      <Modal title="Проверка" onClose={vi.fn()}>
        <button type="button">Внутри</button>
      </Modal>,
    )

    outside.focus()
    expect(screen.getByRole('button', { name: 'Закрыть диалог' })).toHaveFocus()
    outside.remove()
  })
})
