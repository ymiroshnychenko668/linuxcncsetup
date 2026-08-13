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
  fit: ReturnType<typeof vi.fn>
  refresh: ReturnType<typeof vi.fn>
  rows: number
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
  const terminal = {
    fit: vi.fn(),
    refresh: vi.fn(),
    rows: 24,
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
})
