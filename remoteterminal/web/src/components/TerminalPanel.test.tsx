import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { TerminalSession } from '../api'
import { TerminalPanel } from './TerminalPanel'

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
  execCommand: ReturnType<typeof vi.fn>
  fit: ReturnType<typeof vi.fn>
  fireSelectionChange: () => void
  focus: ReturnType<typeof vi.fn>
  focusTextarea: () => void
  getSelection: ReturnType<typeof vi.fn>
  onSelectionChange: ReturnType<typeof vi.fn>
  refresh: ReturnType<typeof vi.fn>
  rows: number
  selectionDisposers: ReturnType<typeof vi.fn>[]
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
  const execCommand = vi.fn()
  Object.defineProperty(terminalDocument, 'execCommand', {
    configurable: true,
    value: execCommand,
  })
  Object.defineProperty(frame, 'contentDocument', {
    configurable: true,
    value: terminalDocument,
  })
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
  const selectionChangeListeners: Array<() => void> = []
  const selectionDisposers: ReturnType<typeof vi.fn>[] = []
  const terminal = {
    execCommand,
    fit: vi.fn(),
    fireSelectionChange: () => {
      selectionChangeListeners[selectionChangeListeners.length - 1]?.()
    },
    focus: vi.fn(() => {
      frame.focus()
      focusTextarea()
    }),
    focusTextarea,
    getSelection: vi.fn(() => ''),
    onSelectionChange: vi.fn((listener: () => void) => {
      selectionChangeListeners.push(listener)
      const dispose = vi.fn()
      selectionDisposers.push(dispose)
      return { dispose }
    }),
    refresh: vi.fn(),
    rows: 24,
    selectionDisposers,
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

  it('copies nonempty xterm selections and disposes selection listeners on reload and cleanup', async () => {
    const { unmount } = render(
      <TerminalPanel
        session={session}
        active
        focusRequestKey={0}
        onStateChange={vi.fn()}
        onSessionChange={vi.fn()}
      />,
    )

    const frame = await screen.findByTitle('alpha terminal') as HTMLIFrameElement
    expect(frame).toHaveAttribute('allow', "clipboard-read 'none'; clipboard-write 'none'")
    const terminal = installTerminal(frame)
    fireEvent.load(frame)

    expect(terminal.onSelectionChange).toHaveBeenCalledTimes(1)
    terminal.fireSelectionChange()
    expect(terminal.execCommand).not.toHaveBeenCalled()

    terminal.getSelection.mockReturnValue('selected terminal text')
    terminal.fireSelectionChange()
    expect(terminal.execCommand).toHaveBeenCalledOnce()
    expect(terminal.execCommand).toHaveBeenCalledWith('copy')

    fireEvent.load(frame)
    expect(terminal.selectionDisposers[0]).toHaveBeenCalledOnce()
    expect(terminal.onSelectionChange).toHaveBeenCalledTimes(2)

    unmount()
    expect(terminal.selectionDisposers[1]).toHaveBeenCalledOnce()
  })

  it('retries native copy setup when ttyd exposes xterm after the iframe load event', async () => {
    render(
      <TerminalPanel
        session={session}
        active
        focusRequestKey={0}
        onStateChange={vi.fn()}
        onSessionChange={vi.fn()}
      />,
    )

    const frame = await screen.findByTitle('alpha terminal') as HTMLIFrameElement
    vi.useFakeTimers()
    try {
      fireEvent.load(frame)
      const terminal = installTerminal(frame)
      expect(terminal.onSelectionChange).not.toHaveBeenCalled()

      act(() => vi.advanceTimersByTime(50))
      expect(terminal.onSelectionChange).toHaveBeenCalledOnce()

      terminal.getSelection.mockReturnValue('late ttyd selection')
      terminal.fireSelectionChange()
      expect(terminal.execCommand).toHaveBeenCalledWith('copy')
    } finally {
      vi.useRealTimers()
    }
  })
})
