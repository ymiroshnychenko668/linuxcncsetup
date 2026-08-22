import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { GCodePreview } from './GCodePreview'
import type { Artifact, Setup } from '../domain'
import { tokenizeGCode } from '../gcodeHighlight'

const artifact: Artifact = {
  artifactId: 'a'.repeat(32), setupId: 'b'.repeat(32), role: 'program',
  displayName: 'main.ngc', mediaType: 'text/plain', byteSize: 0, version: 'c'.repeat(64),
  position: 0, primary: true, state: 'available', createdAt: '', updatedAt: '',
}
const setup: Setup = {
  setupId: artifact.setupId, libraryId: 'd'.repeat(32), name: 'Корпус', status: 'draft',
  revision: 2, source: 'created', artifacts: [artifact], createdAt: '', updatedAt: '',
}

function rangeHeaders(version: string, start: number, end: number, total: number): Record<string, string> {
  return { etag: `"${version}"`, 'content-range': `bytes ${start}-${end}/${total}` }
}

afterEach(() => vi.unstubAllGlobals())

describe('GCodePreview', () => {
  it('shows an explicit empty state and setup context', () => {
    render(<GCodePreview setup={setup} artifact={artifact} />)
    expect(screen.getByRole('heading', { name: 'main.ngc' })).toBeInTheDocument()
    expect(screen.getByText('Программа пуста.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Setup Sheet' })).toBeDisabled()
  })

  it('highlights domain tokens without HTML injection', () => {
		const tokens = tokenizeGCode('G1 X12.5 F300 ; move <script>')
		expect(tokens.some((token) => token.kind === 'command' && token.text === 'G1')).toBe(true)
		expect(tokens.some((token) => token.kind === 'axis' && token.text === 'X12.5')).toBe(true)
		expect(tokens.map((token) => token.text).join('')).toContain('<script>')
  })

  it('uses bounded natural-height rows when wrapping is enabled', async () => {
    const user = userEvent.setup()
    const { container } = render(<GCodePreview setup={setup} artifact={artifact} />)
    await user.click(screen.getByRole('checkbox', { name: 'Перенос строк' }))
    expect(container.querySelector('.code-viewport')).toHaveClass('code-viewport--wrap')
    const row = container.querySelector<HTMLElement>('.code-line')!
    expect(row).toHaveClass('code-line--wrapped')
    expect(row.style.position).not.toBe('absolute')
    expect(row.style.height).toBe('')
  })

	it('pages through the complete virtual line range while wrapped rows use natural heights', async () => {
		class IndexWorker {
			private listener?: (event: MessageEvent<unknown>) => void
			addEventListener(_type: string, listener: EventListener) { this.listener = listener as (event: MessageEvent<unknown>) => void }
			removeEventListener() { this.listener = undefined }
			terminate() { this.listener = undefined }
			postMessage(message: { type: string; requestId: string }) {
				if (message.type !== 'index') return
				queueMicrotask(() => this.listener?.({ data: {
					type: 'indexResult', requestId: message.requestId, lineCount: 100,
					entries: [{ line: 1, byteOffset: 0 }],
				} } as MessageEvent<unknown>))
			}
		}
		vi.stubGlobal('Worker', IndexWorker)
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(new Uint8Array([0x0a]), {
			status: 206, headers: rangeHeaders(artifact.version, 0, 0, 1),
		})))
		const user = userEvent.setup()
		render(<GCodePreview setup={setup} artifact={{ ...artifact, byteSize: 1 }} />)
		await user.click(screen.getByRole('checkbox', { name: 'Перенос строк' }))
		const next = await screen.findByRole('button', { name: 'Следующие строки' })
		expect(next).toBeEnabled()
		await user.click(next)
		expect(screen.getByText('Строки 21–65 из 100')).toBeInTheDocument()
		expect(screen.getByRole('button', { name: 'Предыдущие строки' })).toBeEnabled()
	})

		it('keeps the sparse index Worker alive when a parent supplies a new callback identity', async () => {
		let workerCount = 0
		let indexCount = 0
		class StableWorker {
			constructor() { workerCount += 1 }
			addEventListener() {}
			removeEventListener() {}
			terminate() {}
			postMessage(message: { type: string }) {
				if (message.type === 'index') indexCount += 1
			}
		}
		vi.stubGlobal('Worker', StableWorker)
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(new Uint8Array([0x0a]), {
			status: 206, headers: rangeHeaders(artifact.version, 0, 0, 1),
		})))
		const view = render(<GCodePreview setup={setup} artifact={{ ...artifact, byteSize: 1 }} onArtifactChanged={() => undefined} />)
			await waitFor(() => expect(workerCount).toBe(1))
			expect(indexCount).toBe(1)
			view.rerender(<GCodePreview setup={setup} artifact={{ ...artifact, byteSize: 1 }} onArtifactChanged={() => undefined} />)
			await waitFor(() => expect(workerCount).toBe(1))
			expect(indexCount).toBe(1)
		})

		it('shows one 64 KiB prefix before starting the background index', async () => {
			const requestedRanges: string[] = []
			let resolvePrefix!: (response: Response) => void
			const prefixRequest = new Promise<Response>((resolve) => { resolvePrefix = resolve })
			let workers = 0
			class DeferredIndexWorker {
				constructor() { workers += 1 }
				addEventListener() {}
				removeEventListener() {}
				terminate() {}
				postMessage() {}
			}
			vi.stubGlobal('Worker', DeferredIndexWorker)
			vi.stubGlobal('fetch', vi.fn().mockImplementation((_url: string, init?: RequestInit) => {
				const range = (init?.headers as Record<string, string> | undefined)?.Range ?? ''
				requestedRanges.push(range)
				return prefixRequest
			}))

			render(<GCodePreview compact setup={setup} artifact={{ ...artifact, byteSize: 70_000 }} />)
			expect(requestedRanges).toEqual(['bytes=0-65535'])
			expect(workers).toBe(0)

			const prefix = new Uint8Array(65_536).fill(0x20)
			prefix.set(new TextEncoder().encode('G90\nG0 X0\n'))
			resolvePrefix(new Response(prefix, { status: 206, headers: rangeHeaders(artifact.version, 0, 65_535, 70_000) }))

			expect(await screen.findByText('G90')).toBeInTheDocument()
			await waitFor(() => expect(workers).toBe(1))
			expect(requestedRanges).toEqual(['bytes=0-65535'])
			expect(screen.queryByRole('heading', { name: 'main.ngc' })).not.toBeInTheDocument()
		})

		it('does not let a late prefix from an old artifact overwrite the new generation', async () => {
			let resolveOld!: (response: Response) => void
			let resolveNew!: (response: Response) => void
			const oldRequest = new Promise<Response>((resolve) => { resolveOld = resolve })
			const newRequest = new Promise<Response>((resolve) => { resolveNew = resolve })
			const newer = { ...artifact, artifactId: 'new-artifact', displayName: 'new.ngc', version: 'd'.repeat(64), byteSize: 4 }
			vi.stubGlobal('fetch', vi.fn().mockImplementation((_url: string, init?: RequestInit) => {
				const version = (init?.headers as Record<string, string> | undefined)?.['If-Match']
				return version === `"${newer.version}"` ? newRequest : oldRequest
			}))

			const view = render(<GCodePreview compact setup={setup} artifact={{ ...artifact, byteSize: 4 }} />)
			view.rerender(<GCodePreview compact setup={setup} artifact={newer} />)
			await act(async () => {
				resolveNew(new Response(new TextEncoder().encode('NEW\n'), {
					status: 206, headers: rangeHeaders(newer.version, 0, 3, 4),
				}))
				await Promise.resolve()
			})
			expect(await screen.findByText('NEW')).toBeInTheDocument()

			await act(async () => {
				resolveOld(new Response(new TextEncoder().encode('OLD\n'), {
					status: 206, headers: rangeHeaders(artifact.version, 0, 3, 4),
				}))
				await Promise.resolve()
			})
			await waitFor(() => expect(screen.queryByText('OLD')).not.toBeInTheDocument())
			expect(screen.getByLabelText('G-code new.ngc')).toBeInTheDocument()
		})

		it('loads the real next line when the prefix ends exactly on a newline', async () => {
			const prefixSize = 64 << 10
			const tail = new TextEncoder().encode('TARGET')
			const bytes = new Uint8Array(prefixSize + tail.length)
			bytes.fill(0x58, 0, prefixSize - 1)
			bytes[prefixSize - 1] = 0x0a
			bytes.set(tail, prefixSize)
			const ranges: string[] = []
			class BoundaryWorker {
				private listener?: (event: MessageEvent<unknown>) => void
				addEventListener(_type: string, listener: EventListener) { this.listener = listener as (event: MessageEvent<unknown>) => void }
				removeEventListener() { this.listener = undefined }
				terminate() { this.listener = undefined }
				postMessage(message: { type: string; requestId: string }) {
					if (message.type !== 'index') return
					queueMicrotask(() => this.listener?.({ data: {
						type: 'indexResult', requestId: message.requestId, lineCount: 2,
						entries: [{ line: 1, byteOffset: 0 }, { line: 2, byteOffset: prefixSize }],
					} } as MessageEvent<unknown>))
				}
			}
			vi.stubGlobal('Worker', BoundaryWorker)
			vi.stubGlobal('fetch', vi.fn().mockImplementation((_url: string, init?: RequestInit) => {
				const range = (init?.headers as Record<string, string> | undefined)?.Range ?? ''
				ranges.push(range)
				const match = /^bytes=(\d+)-(\d+)$/.exec(range)
				if (!match) throw new Error(`unexpected range ${range}`)
				const start = Number(match[1])
				const end = Number(match[2])
				return Promise.resolve(new Response(bytes.slice(start, end + 1), {
					status: 206, headers: rangeHeaders(artifact.version, start, end, bytes.byteLength),
				}))
			}))

			const user = userEvent.setup()
			render(<GCodePreview compact setup={setup} artifact={{ ...artifact, byteSize: bytes.byteLength }} />)
			const line = screen.getByRole('spinbutton', { name: 'Строка' })
			await waitFor(() => expect(line).toHaveAttribute('max', '2'))
			await user.clear(line)
			await user.type(line, '2')
			await user.click(screen.getByRole('button', { name: 'Перейти' }))

			expect(await screen.findByText('TARGET')).toBeInTheDocument()
			expect(ranges[0]).toBe('bytes=0-65535')
			expect(ranges.some((range) => range.startsWith(`bytes=${prefixSize}-`))).toBe(true)
		})

	it('iteratively reaches a target line beyond a one-megabyte thinned-index gap', async () => {
		const blockBytes = 1 << 20
		const firstLine = 'X'.repeat(blockBytes + 32)
		const bytes = new TextEncoder().encode(`${firstLine}\nTARGET\nTAIL`)
		const targetOffset = firstLine.length + 1
		const requestedStarts: number[] = []
		const requestedVersions: string[] = []
		class ThinnedIndexWorker {
			private listener?: (event: MessageEvent<unknown>) => void
			addEventListener(_type: string, listener: EventListener) { this.listener = listener as (event: MessageEvent<unknown>) => void }
			removeEventListener() { this.listener = undefined }
			terminate() { this.listener = undefined }
			postMessage(message: { type: string; requestId: string }) {
				if (message.type !== 'index') return
				queueMicrotask(() => this.listener?.({ data: {
					type: 'indexResult', requestId: message.requestId, lineCount: 3,
					entries: [{ line: 1, byteOffset: 0 }],
				} } as MessageEvent<unknown>))
			}
		}
		vi.stubGlobal('Worker', ThinnedIndexWorker)
		vi.stubGlobal('fetch', vi.fn().mockImplementation((_url: string, init?: RequestInit) => {
			const headers = init?.headers as Record<string, string> | undefined
			const range = headers?.Range
			const match = /^bytes=(\d+)-(\d+)$/.exec(range ?? '')
			if (!match) throw new Error(`unexpected range ${range}`)
			const start = Number(match[1])
			const end = Number(match[2])
			requestedStarts.push(start)
			requestedVersions.push(headers?.['If-Match'] ?? '')
			return Promise.resolve(new Response(bytes.slice(start, end + 1), {
				status: 206, headers: rangeHeaders(artifact.version, start, end, bytes.byteLength),
			}))
		}))
		render(<GCodePreview setup={setup} artifact={{ ...artifact, byteSize: bytes.byteLength }} initialLine={2} />)
		expect(await screen.findByText('TARGET')).toBeInTheDocument()
		expect(requestedStarts).toContain(blockBytes)
		expect(requestedStarts).toContain(targetOffset)
		expect(requestedStarts.every((start) => start === 0 || start === blockBytes || start === targetOffset)).toBe(true)
		expect(requestedVersions.every((version) => version === `"${artifact.version}"`)).toBe(true)
	})

	it('maps hundreds of millions of logical lines onto a bounded browser scroll track', async () => {
		class HugeIndexWorker {
			private listener?: (event: MessageEvent<unknown>) => void
			addEventListener(_type: string, listener: EventListener) { this.listener = listener as (event: MessageEvent<unknown>) => void }
			removeEventListener() { this.listener = undefined }
			terminate() { this.listener = undefined }
			postMessage(message: { type: string; requestId: string }) {
				if (message.type !== 'index') return
				queueMicrotask(() => this.listener?.({ data: {
					type: 'indexResult', requestId: message.requestId, lineCount: 500_000_000,
					entries: [{ line: 1, byteOffset: 0 }],
				} } as MessageEvent<unknown>))
			}
		}
		vi.stubGlobal('Worker', HugeIndexWorker)
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(new Uint8Array([0x0a]), {
			status: 206, headers: rangeHeaders(artifact.version, 0, 0, 1),
		})))
		const user = userEvent.setup()
		const { container } = render(<GCodePreview setup={setup} artifact={{ ...artifact, byteSize: 1 }} />)
		const line = screen.getByRole('spinbutton', { name: 'Строка' })
		await user.clear(line)
		await user.type(line, '500000000')
		await user.click(screen.getByRole('button', { name: 'Перейти' }))
		const spacer = container.querySelector<HTMLElement>('.code-spacer')!
		expect(spacer.style.height).toBe('8000000px')
		const last = screen.getByText('500000000').closest<HTMLElement>('.code-line')!
		expect(Number.parseFloat(last.style.top)).toBeLessThanOrEqual(8_000_000)
	})

	it('loads a compact result page to navigate beyond the first large search window', async () => {
		const requestedOffsets: number[] = []
		class SearchWorker {
			private listener?: (event: MessageEvent<unknown>) => void
			addEventListener(_type: string, listener: EventListener) { this.listener = listener as (event: MessageEvent<unknown>) => void }
			removeEventListener() { this.listener = undefined }
			terminate() { this.listener = undefined }
			postMessage(message: { type: string; requestId: string; matchOffset?: number }) {
				if (message.type === 'index') {
					queueMicrotask(() => this.listener?.({ data: {
						type: 'indexResult', requestId: message.requestId, lineCount: 2_000,
						entries: [{ line: 1, byteOffset: 0 }],
					} } as MessageEvent<unknown>))
				}
				if (message.type === 'search') {
					const offset = message.matchOffset ?? 0
					requestedOffsets.push(offset)
					const length = Math.min(512, 1_001 - offset)
					const lines = Float64Array.from({ length }, (_, index) => offset + index + 1)
					queueMicrotask(() => this.listener?.({ data: {
						type: 'searchResult', requestId: message.requestId, totalMatches: 1_001,
						lineNumbers: lines, matchOffset: offset, truncated: true,
					} } as MessageEvent<unknown>))
				}
			}
		}
		vi.stubGlobal('Worker', SearchWorker)
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(new Uint8Array([0x0a]), {
			status: 206, headers: rangeHeaders(artifact.version, 0, 0, 1),
		})))
		const user = userEvent.setup()
		render(<GCodePreview setup={setup} artifact={{ ...artifact, byteSize: 1 }} />)
		await user.type(screen.getByRole('searchbox'), 'X')
		await user.click(screen.getByRole('button', { name: 'Найти' }))
		expect(await screen.findByText(/1 из 1001 · компактные страницы/)).toBeInTheDocument()
		const ordinal = screen.getByRole('spinbutton', { name: 'Совпадение №' })
		await user.clear(ordinal)
		await user.type(ordinal, '1001')
		await user.click(screen.getByRole('button', { name: 'Перейти к совпадению' }))
		expect(await screen.findByText(/1001 из 1001 · компактные страницы/)).toBeInTheDocument()
		expect(requestedOffsets).toEqual([0, 512])
		expect(screen.getByRole('button', { name: 'Совпадение 1001, строка 1001' })).toHaveAttribute('aria-current', 'true')
	})

	it('reports an index Worker failure to derived Tool Table consumers', async () => {
		class ErrorWorker {
			private listener?: (event: MessageEvent<unknown>) => void
			addEventListener(_type: string, listener: EventListener) { this.listener = listener as (event: MessageEvent<unknown>) => void }
			removeEventListener() { this.listener = undefined }
			terminate() { this.listener = undefined }
			postMessage(message: { type: string; requestId: string }) {
				if (message.type === 'index') queueMicrotask(() => this.listener?.({ data: {
					type: 'error', requestId: message.requestId, code: 'UNSUPPORTED_ENCODING',
				} } as MessageEvent<unknown>))
			}
		}
		vi.stubGlobal('Worker', ErrorWorker)
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(Uint8Array.from([0x0a]), {
			status: 206, headers: rangeHeaders(artifact.version, 0, 0, 1),
		})))
		const analysis = vi.fn()
		render(<GCodePreview compact setup={setup} artifact={{ ...artifact, byteSize: 1 }} onAnalysisChanged={analysis} />)
		await waitFor(() => expect(analysis).toHaveBeenCalledWith(expect.objectContaining({ error: 'UNSUPPORTED_ENCODING' })))
	})

	it('reports a module Worker construction failure instead of loading forever', async () => {
		class UnavailableWorker {
			constructor() { throw new Error('module workers disabled') }
		}
		vi.stubGlobal('Worker', UnavailableWorker)
		const analysis = vi.fn()
		render(<GCodePreview compact setup={setup} artifact={artifact} onAnalysisChanged={analysis} />)
		await waitFor(() => expect(analysis).toHaveBeenCalledWith(expect.objectContaining({
			complete: false,
			error: 'WORKER_UNAVAILABLE',
		})))
		expect(screen.getByRole('alert')).toHaveTextContent('Текстовый preview недоступен.')
	})

	it('reports a Worker runtime crash while indexing', async () => {
		class CrashedWorker {
			private listeners = new Map<string, EventListener>()
			addEventListener(type: string, listener: EventListener) { this.listeners.set(type, listener) }
			removeEventListener(type: string) { this.listeners.delete(type) }
			terminate() { this.listeners.clear() }
			postMessage(message: { type: string }) {
				if (message.type === 'index') queueMicrotask(() => this.listeners.get('error')?.(new Event('error')))
			}
		}
		vi.stubGlobal('Worker', CrashedWorker)
		const analysis = vi.fn()
		render(<GCodePreview compact setup={setup} artifact={artifact} onAnalysisChanged={analysis} />)
		await waitFor(() => expect(analysis).toHaveBeenCalledWith(expect.objectContaining({
			complete: false,
			error: 'WORKER_ERROR',
		})))
	})

	it('keeps completed derived analysis valid if the Worker later crashes during search', async () => {
		class SearchCrashWorker {
			private listeners = new Map<string, EventListener>()
			addEventListener(type: string, listener: EventListener) { this.listeners.set(type, listener) }
			removeEventListener(type: string) { this.listeners.delete(type) }
			terminate() { this.listeners.clear() }
			postMessage(message: { type: string; requestId: string }) {
				if (message.type === 'index') queueMicrotask(() => this.listeners.get('message')?.({ data: {
					type: 'indexResult', requestId: message.requestId, lineCount: 1,
					entries: [{ line: 1, byteOffset: 0 }],
					tools: [{ toolNumber: 7, firstLine: 1, references: 1, changes: 0 }], toolsTruncated: false,
				} } as MessageEvent<unknown>))
				if (message.type === 'search') queueMicrotask(() => this.listeners.get('error')?.(new Event('error')))
			}
		}
		vi.stubGlobal('Worker', SearchCrashWorker)
		const analysis = vi.fn()
		render(<GCodePreview setup={setup} artifact={artifact} onAnalysisChanged={analysis} />)
		await waitFor(() => expect(analysis).toHaveBeenCalledWith(expect.objectContaining({
			complete: true,
			tools: [expect.objectContaining({ toolNumber: 7 })],
			error: undefined,
		})))
		fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'X' } })
		fireEvent.submit(screen.getByRole('search'))
		expect(await screen.findByText('Поиск не выполнен.')).toBeInTheDocument()
		expect(analysis.mock.calls.at(-1)?.[0]).toEqual(expect.objectContaining({ complete: true, error: undefined }))
	})

	it('reports prefix validation failure instead of leaving derived views loading forever', async () => {
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(Uint8Array.from([0x0a]), {
			status: 206,
			headers: { etag: `"${artifact.version}"`, 'content-range': 'bytes 0-0/2' },
		})))
		const analysis = vi.fn()
		render(<GCodePreview compact setup={setup} artifact={{ ...artifact, byteSize: 1 }} onAnalysisChanged={analysis} />)
		await waitFor(() => expect(analysis).toHaveBeenCalledWith(expect.objectContaining({ error: 'RANGE_FAILED', validation: 'pending' })))
	})

	it('keeps a completed Tool Table valid when only literal search fails', async () => {
		class SearchErrorWorker {
			private listener?: (event: MessageEvent<unknown>) => void
			addEventListener(_type: string, listener: EventListener) { this.listener = listener as (event: MessageEvent<unknown>) => void }
			removeEventListener() { this.listener = undefined }
			terminate() { this.listener = undefined }
			postMessage(message: { type: string; requestId: string }) {
				if (message.type === 'index') queueMicrotask(() => this.listener?.({ data: {
					type: 'indexResult', requestId: message.requestId, lineCount: 2,
					entries: [{ line: 1, byteOffset: 0 }],
					tools: [{ toolNumber: 2, firstLine: 1, references: 1, changes: 1 }], toolsTruncated: false,
				} } as MessageEvent<unknown>))
				if (message.type === 'search') queueMicrotask(() => this.listener?.({ data: {
					type: 'error', requestId: message.requestId, code: 'QUERY_TOO_LONG',
				} } as MessageEvent<unknown>))
			}
		}
		vi.stubGlobal('Worker', SearchErrorWorker)
		vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(Uint8Array.from([0x0a]), {
			status: 206, headers: rangeHeaders(artifact.version, 0, 0, 1),
		})))
		const analysis = vi.fn()
		render(<GCodePreview setup={setup} artifact={{ ...artifact, byteSize: 1 }} onAnalysisChanged={analysis} />)
		await waitFor(() => expect(analysis).toHaveBeenCalledWith(expect.objectContaining({ progress: 1, tools: [expect.objectContaining({ toolNumber: 2 })], error: undefined })))
		fireEvent.change(screen.getByRole('searchbox'), { target: { value: 'X' } })
		fireEvent.submit(screen.getByRole('search'))
		expect(await screen.findByText('Поиск не выполнен.')).toBeInTheDocument()
		expect(analysis.mock.calls.at(-1)?.[0]).toEqual(expect.objectContaining({ progress: 1, error: undefined }))
	})
})
