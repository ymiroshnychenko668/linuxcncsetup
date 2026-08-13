import { createRef } from 'react'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TerminalSession } from '../api'
import { TerminalPanel, type TerminalPanelHandle } from './TerminalPanel'

const session: TerminalSession = {
  id: 'session-alpha',
  name: 'alpha',
  attached: false,
  windows: 1,
  terminalConnected: false,
}

const mocks = vi.hoisted(() => ({
  connectSession: vi.fn(),
}))

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, api: { ...actual.api, connectSession: mocks.connectSession } }
})

interface MockTerminal {
  fit: ReturnType<typeof vi.fn>
  focus: ReturnType<typeof vi.fn>
  focusTextarea: () => void
  clearSelection: ReturnType<typeof vi.fn>
  getSelection: ReturnType<typeof vi.fn>
  refresh: ReturnType<typeof vi.fn>
  rows: number
  textarea: HTMLTextAreaElement
}

class MockResizeObserver {
  static instances: MockResizeObserver[] = []

  readonly observe = vi.fn()
  readonly unobserve = vi.fn()
  readonly disconnect = vi.fn()

  constructor(private readonly callback: ResizeObserverCallback) {
    MockResizeObserver.instances.push(this)
  }

  trigger() {
    this.callback([], this as unknown as ResizeObserver)
  }
}

let nextAnimationFrameId: number
let animationFrames: Map<number, FrameRequestCallback>

function flushAnimationFrames() {
  act(() => {
    const pending = [...animationFrames.values()]
    animationFrames.clear()
    pending.forEach((callback) => callback(performance.now()))
  })
}

function installTerminal(frame: HTMLIFrameElement): MockTerminal {
  const terminalDocument = document.implementation.createHTMLDocument('terminal')
  const textarea = terminalDocument.createElement('textarea')
  terminalDocument.body.append(textarea)
  let textareaFocused = false
  const focusTextarea = () => {
    textareaFocused = true
  }
  Object.defineProperty(terminalDocument, 'activeElement', {
    configurable: true,
    get: () => textareaFocused ? textarea : terminalDocument.body,
  })
  const terminal = {
    fit: vi.fn(),
    focus: vi.fn(() => {
      frame.focus()
      focusTextarea()
    }),
    focusTextarea,
    clearSelection: vi.fn(),
    getSelection: vi.fn(() => ''),
    refresh: vi.fn(),
    rows: 24,
    textarea,
  }
  Object.defineProperty(frame.contentWindow, 'term', {
    configurable: true,
    value: terminal,
  })
  return terminal
}

describe('TerminalPanel', () => {
  beforeEach(() => {
    mocks.connectSession.mockReset()
    mocks.connectSession.mockResolvedValue({
      session,
      terminalUrl: `/terminal/${session.id}/`,
    })

    nextAnimationFrameId = 1
    animationFrames = new Map()
    vi.stubGlobal('requestAnimationFrame', vi.fn((callback: FrameRequestCallback) => {
      const id = nextAnimationFrameId++
      animationFrames.set(id, callback)
      return id
    }))
    vi.stubGlobal('cancelAnimationFrame', vi.fn((id: number) => {
      animationFrames.delete(id)
    }))
    MockResizeObserver.instances = []
    vi.stubGlobal('ResizeObserver', MockResizeObserver)
  })

  afterEach(() => {
    vi.unstubAllGlobals()
  })

  it('refits and refreshes the mounted ttyd terminal when its panel becomes active', async () => {
    const onStateChange = vi.fn()
    const onSessionChange = vi.fn()
    const { rerender } = render(
      <TerminalPanel
        session={session}
        active={false}
        focusRequestKey={0}
        reconnectKey={0}
        onStateChange={onStateChange}
        onSessionChange={onSessionChange}
      />,
    )

    const frame = await screen.findByTitle('alpha terminal') as HTMLIFrameElement
    const terminal = installTerminal(frame)

    rerender(
      <TerminalPanel
        session={session}
        active
        focusRequestKey={0}
        reconnectKey={0}
        onStateChange={onStateChange}
        onSessionChange={onSessionChange}
      />,
    )
    flushAnimationFrames()

    expect(screen.getByTitle('alpha terminal')).toBe(frame)
    expect(terminal.fit).toHaveBeenCalledTimes(1)
    expect(terminal.refresh).toHaveBeenCalledWith(0, 23)
    expect(mocks.connectSession).toHaveBeenCalledTimes(1)

    terminal.fit.mockClear()
    terminal.refresh.mockClear()
    fireEvent.load(frame)
    flushAnimationFrames()
    expect(terminal.fit).toHaveBeenCalledTimes(1)
    expect(terminal.refresh).toHaveBeenCalledWith(0, 23)
  })

  it('coalesces active panel resizes and disconnects the observer when inactive', async () => {
    const onStateChange = vi.fn()
    const onSessionChange = vi.fn()
    const { rerender, unmount } = render(
      <TerminalPanel
        session={session}
        active={false}
        focusRequestKey={0}
        reconnectKey={0}
        onStateChange={onStateChange}
        onSessionChange={onSessionChange}
      />,
    )

    const frame = await screen.findByTitle('alpha terminal') as HTMLIFrameElement
    const terminal = installTerminal(frame)
    rerender(
      <TerminalPanel
        session={session}
        active
        focusRequestKey={0}
        reconnectKey={0}
        onStateChange={onStateChange}
        onSessionChange={onSessionChange}
      />,
    )
    flushAnimationFrames()
    terminal.fit.mockClear()
    terminal.refresh.mockClear()

    const observer = MockResizeObserver.instances[MockResizeObserver.instances.length - 1]
    expect(observer).toBeDefined()
    observer!.trigger()
    observer!.trigger()
    expect(animationFrames.size).toBe(1)
    flushAnimationFrames()
    expect(terminal.fit).toHaveBeenCalledTimes(1)
    expect(terminal.refresh).toHaveBeenCalledTimes(1)

    rerender(
      <TerminalPanel
        session={session}
        active={false}
        focusRequestKey={0}
        reconnectKey={0}
        onStateChange={onStateChange}
        onSessionChange={onSessionChange}
      />,
    )
    expect(observer!.disconnect).toHaveBeenCalledTimes(1)
    terminal.fit.mockClear()
    observer!.trigger()
    flushAnimationFrames()
    expect(terminal.fit).not.toHaveBeenCalled()

    unmount()
    expect(animationFrames.size).toBe(0)
    await waitFor(() => expect(mocks.connectSession).toHaveBeenCalledTimes(1))
  })

  it('retries a requested keyboard focus until xterm is ready and abandons inactive requests', async () => {
    const onStateChange = vi.fn()
    const onSessionChange = vi.fn()
    const { rerender } = render(
      <TerminalPanel
        session={session}
        active={false}
        focusRequestKey={0}
        reconnectKey={0}
        onStateChange={onStateChange}
        onSessionChange={onSessionChange}
      />,
    )
    const frame = await screen.findByTitle('alpha terminal') as HTMLIFrameElement
    const frameFocus = vi.spyOn(frame, 'focus')

    vi.useFakeTimers()
    try {
      rerender(
        <TerminalPanel
          session={session}
          active
          focusRequestKey={1}
          reconnectKey={0}
          onStateChange={onStateChange}
          onSessionChange={onSessionChange}
        />,
      )
      flushAnimationFrames()
      expect(frameFocus).toHaveBeenCalledTimes(1)

      const terminal = installTerminal(frame)
      let focusCalls = 0
      terminal.focus.mockImplementation(() => {
        frame.focus()
        focusCalls += 1
        if (focusCalls > 1) terminal.focusTextarea()
      })
      act(() => vi.advanceTimersByTime(50))
      flushAnimationFrames()
      expect(terminal.focus).toHaveBeenCalledTimes(1)

      act(() => vi.advanceTimersByTime(100))
      flushAnimationFrames()
      expect(terminal.focus).toHaveBeenCalledTimes(2)
      expect(terminal.textarea.ownerDocument.activeElement).toBe(terminal.textarea)

      terminal.focus.mockClear()
      rerender(
        <TerminalPanel
          session={session}
          active={false}
          focusRequestKey={2}
          reconnectKey={0}
          onStateChange={onStateChange}
          onSessionChange={onSessionChange}
        />,
      )
      act(() => vi.advanceTimersByTime(2000))
      flushAnimationFrames()
      expect(terminal.focus).not.toHaveBeenCalled()

      rerender(
        <TerminalPanel
          session={session}
          active
          focusRequestKey={2}
          reconnectKey={0}
          onStateChange={onStateChange}
          onSessionChange={onSessionChange}
        />,
      )
      flushAnimationFrames()
      expect(terminal.focus).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })

  it('does not steal focus back when the user moves on while xterm is loading', async () => {
    const onStateChange = vi.fn()
    const onSessionChange = vi.fn()
    const { rerender } = render(
      <TerminalPanel
        session={session}
        active={false}
        focusRequestKey={0}
        reconnectKey={0}
        onStateChange={onStateChange}
        onSessionChange={onSessionChange}
      />,
    )
    const frame = await screen.findByTitle('alpha terminal') as HTMLIFrameElement
    const panel = frame.closest('[role="tabpanel"]') as HTMLElement

    vi.useFakeTimers()
    try {
      rerender(
        <TerminalPanel
          session={session}
          active
          focusRequestKey={1}
          reconnectKey={0}
          onStateChange={onStateChange}
          onSessionChange={onSessionChange}
        />,
      )
      flushAnimationFrames()
      expect(document.activeElement).toBe(frame)

      panel.focus()
      const terminal = installTerminal(frame)
      act(() => vi.advanceTimersByTime(50))
      flushAnimationFrames()

      expect(document.activeElement).toBe(panel)
      expect(terminal.focus).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })

  it('takes and clears the exact same-origin xterm selection and safely returns empty while unavailable or navigating', async () => {
    const ref = createRef<TerminalPanelHandle>()
    render(
      <TerminalPanel
        ref={ref}
        session={session}
        active
        focusRequestKey={0}
        reconnectKey={0}
        onStateChange={vi.fn()}
        onSessionChange={vi.fn()}
      />,
    )

    const frame = await screen.findByTitle('alpha terminal') as HTMLIFrameElement
    expect(frame).toHaveAttribute('allow', "clipboard-read 'none'; clipboard-write 'none'")
    expect(ref.current?.takeSelection()).toBe('')

    const terminal = installTerminal(frame)
    const exactText = '  first\nКиїв\t😀\n'
    terminal.getSelection.mockReturnValue(exactText)
    expect(ref.current?.takeSelection()).toBe(exactText)
    expect(terminal.clearSelection).toHaveBeenCalledTimes(1)

    terminal.clearSelection.mockImplementationOnce(() => {
      throw new DOMException('navigating', 'SecurityError')
    })
    expect(ref.current?.takeSelection()).toBe(exactText)

    terminal.getSelection.mockImplementation(() => {
      throw new DOMException('navigating', 'SecurityError')
    })
    expect(ref.current?.takeSelection()).toBe('')
  })
})
