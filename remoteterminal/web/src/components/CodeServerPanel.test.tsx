import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'
import type { CodeServerInstance } from '../api'
import { CodeServerPanel } from './CodeServerPanel'

const codeServer: CodeServerInstance = {
  id: 'code-project',
  name: 'project',
  folderPath: '/home/operator/project',
  url: '/code/code-project/',
}

function installFrameDocument(frame: HTMLElement, contentType = 'text/html', path = codeServer.url) {
  Object.defineProperty(frame, 'contentDocument', {
    configurable: true,
    value: {
      contentType,
      URL: new URL(path, window.location.origin).href,
    },
  })
}

describe('CodeServerPanel', () => {
  it('loads the same-origin editor iframe and reports its state', () => {
    const onStateChange = vi.fn()
    render(
      <CodeServerPanel
        codeServer={codeServer}
        active
        reloadKey={0}
        onStateChange={onStateChange}
        onReload={vi.fn()}
      />,
    )

    const frame = screen.getByTitle('project Code Server')
    expect(frame).toHaveAttribute('src', '/code/code-project/')
    expect(frame).toHaveAttribute('aria-hidden', 'true')
    expect(frame).toHaveAttribute('tabindex', '-1')
    expect(screen.getByRole('status')).toHaveTextContent('Loading the persistent editor')

    installFrameDocument(frame)
    fireEvent.load(frame)
    expect(onStateChange).toHaveBeenLastCalledWith(codeServer.id, 'ready')
    expect(frame).not.toHaveAttribute('aria-hidden')
    expect(frame).toHaveAttribute('tabindex', '0')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('keeps the frame mounted while hidden', () => {
    const onReload = vi.fn()
    const onStateChange = vi.fn()
    render(
      <CodeServerPanel
        codeServer={codeServer}
        active={false}
        reloadKey={0}
        onStateChange={onStateChange}
        onReload={onReload}
      />,
    )

    const frame = screen.getByTitle('project Code Server')
    const panel = frame.closest('[role="tabpanel"]')
    expect(panel).toHaveAttribute('hidden')
    expect(frame).toHaveAttribute('aria-hidden', 'true')
    expect(frame).toHaveAttribute('tabindex', '-1')
  })

  it('reloads the editor when its reload key changes', () => {
    const onReload = vi.fn()
    const onStateChange = vi.fn()
    const { rerender } = render(
      <CodeServerPanel
        codeServer={codeServer}
        active
        reloadKey={0}
        onStateChange={onStateChange}
        onReload={onReload}
      />,
    )

    const firstFrame = screen.getByTitle('project Code Server')
    installFrameDocument(firstFrame)
    fireEvent.load(firstFrame)
    expect(onStateChange).toHaveBeenLastCalledWith(codeServer.id, 'ready')

    rerender(
      <CodeServerPanel
        codeServer={codeServer}
        active
        reloadKey={1}
        onStateChange={onStateChange}
        onReload={onReload}
      />,
    )
    const reloadedFrame = screen.getByTitle('project Code Server')
    expect(reloadedFrame).not.toBe(firstFrame)
    expect(reloadedFrame).toHaveAttribute('aria-hidden', 'true')
    expect(reloadedFrame).toHaveAttribute('tabindex', '-1')
    expect(screen.getByRole('status')).toHaveTextContent('Loading the persistent editor')
  })

  it.each([
    ['application/json', codeServer.url],
    ['text/plain', codeServer.url],
    ['text/html', '/api/auth/session'],
  ])('rejects an unexpected %s iframe response at %s', (contentType, path) => {
    const onStateChange = vi.fn()
    render(
      <CodeServerPanel
        codeServer={codeServer}
        active
        reloadKey={0}
        onStateChange={onStateChange}
        onReload={vi.fn()}
      />,
    )

    const frame = screen.getByTitle('project Code Server')
    installFrameDocument(frame, contentType, path)
    fireEvent.load(frame)

    expect(onStateChange).toHaveBeenLastCalledWith(codeServer.id, 'error')
    expect(frame).toHaveAttribute('aria-hidden', 'true')
    expect(frame).toHaveAttribute('tabindex', '-1')
    expect(screen.getByRole('alert')).toHaveTextContent('Code Server could not be loaded')
  })
})
