import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import Checkbox from '@mui/material/Checkbox'
import FormControlLabel from '@mui/material/FormControlLabel'
import IconButton from '@mui/material/IconButton'
import type { Artifact, Setup } from '../domain'
import {
  createCachedRangeSource,
  createUploadedFileRangeSource,
	isGCodeCachePersistenceQuarantined,
  readLocalGCodeCacheState,
  updateLocalGCodeCacheState,
  type GCodeCacheIdentity,
} from '../gcodeCache'
import { tokenizeGCode } from '../gcodeHighlight'
import { resolveSparseLineAnchor, type SparseLineEntry, type ToolTableEntry } from '../workers/gcodeCore'

const BLOCK_BYTES = 1 << 20
const PREVIEW_PREFIX_BYTES = 64 << 10
const MAX_CACHE_BLOCKS = 8
const LINE_HEIGHT = 24
const VIEWPORT_LINES = 28
const OVERSCAN = 8
const MAX_RENDERED_LINE = 4096
const MATCH_PAGE_SIZE = 512
const MATCH_RESULT_WINDOW = 17
const MAX_PROGRESS_PERSIST_STEP = 64 << 20
// Browser engines clamp layout coordinates far below the number of pixels
// represented by hundreds of millions of G-code lines. Keep the scroll track
// bounded and map it proportionally across the complete logical line range.
const MAX_SCROLL_PIXELS = 8_000_000

interface CachedBlock {
  offset: number
  startLine: number
  lines: string[]
  truncatedLastLine: boolean
}

type WorkerResponse =
	| { type: 'progress'; requestId: string; completedBytes: number; totalBytes: number; cachedBytes?: number; totalMatches?: number }
  | { type: 'indexResult'; requestId: string; lineCount: number; entries: SparseLineEntry[]; tools?: ToolTableEntry[]; toolsTruncated?: boolean }
	| { type: 'searchResult'; requestId: string; totalMatches: number; lineNumbers: Float64Array; matchOffset: number; truncated: boolean }
  | { type: 'error'; requestId: string; code: string }

interface Props {
  setup: Setup
  artifact: Artifact
  contentUrl?: string
  compact?: boolean
  onOpenSetupSheet?: () => void
  onArtifactChanged?: () => void
  initialLine?: number
  onLineChanged?: (line: number) => void
  onAnalysisChanged?: (analysis: GCodeAnalysisState) => void
  cacheScope?: string
  sourceFile?: File
}

export interface GCodeAnalysisState {
  artifactId: string
  version: string
  progress: number
  complete: boolean
  lineCount: number
  tools: ToolTableEntry[]
  toolsTruncated: boolean
  validation: 'pending' | 'online' | 'offline'
  error?: string
}

function contentURL(setupId: string, artifactId: string): string {
  return `/api/v1/setups/${encodeURIComponent(setupId)}/programs/${encodeURIComponent(artifactId)}/content`
}

function displayLine(value: string): { text: string; truncated: boolean } {
  if (value.length <= MAX_RENDERED_LINE) return { text: value, truncated: false }
  return { text: `${value.slice(0, MAX_RENDERED_LINE)}…`, truncated: true }
}

async function fetchRangeBytes(
  url: string,
  identity: GCodeCacheIdentity,
  start: number,
  endInclusive: number,
  signal: AbortSignal,
  networkFirst = false,
  onOfflineFallback?: () => void,
): Promise<Uint8Array> {
  const { version, byteSize: total } = identity
  if (!Number.isSafeInteger(total) || total < 1 ||
      !Number.isSafeInteger(start) || !Number.isSafeInteger(endInclusive) ||
      start < 0 || endInclusive < start || endInclusive >= total) {
    throw new Error('RANGE_FAILED')
  }
  signal.throwIfAborted()
	return createCachedRangeSource(identity, url, { networkFirst, onOfflineFallback }).read(start, endInclusive, version, signal)
}

async function fetchBlock(
  url: string,
  identity: GCodeCacheIdentity,
  entry: SparseLineEntry,
  signal: AbortSignal,
  blockBytes = BLOCK_BYTES,
  networkFirst = false,
  onOfflineFallback?: () => void,
  sourceFile?: File,
): Promise<CachedBlock> {
	const total = identity.byteSize
	if (entry.byteOffset === total) {
		return { offset: entry.byteOffset, startLine: entry.line, lines: [''], truncatedLastLine: false }
	}
  const end = Math.min(total - 1, entry.byteOffset + blockBytes - 1)
	let bytes: Uint8Array
	if (sourceFile && sourceFile.size === total) {
		bytes = await createUploadedFileRangeSource(identity, sourceFile).read(
			entry.byteOffset,
			end,
			identity.version,
			signal,
		)
	} else {
		bytes = await fetchRangeBytes(url, identity, entry.byteOffset, end, signal, networkFirst, onOfflineFallback)
	}
	let decoded: string | undefined
	const removableSuffix = end < total - 1 ? Math.min(3, bytes.byteLength) : 0
	for (let trim = 0; trim <= removableSuffix && decoded === undefined; trim += 1) {
		try {
			decoded = new TextDecoder('utf-8', { fatal: true }).decode(bytes.subarray(0, bytes.byteLength - trim))
		} catch {
			// A Range boundary may split one UTF-8 scalar. Only a suffix of at
			// most three bytes is ignored; invalid bytes inside the block remain
			// a hard preview error and the full Worker scan validates the stream.
		}
	}
	if (decoded === undefined) throw new Error('UNSUPPORTED_ENCODING')
  if (entry.byteOffset === 0 && decoded.charCodeAt(0) === 0xfeff) decoded = decoded.slice(1)
  const lines = decoded.split('\n').map((line) => line.endsWith('\r') ? line.slice(0, -1) : line)
  const nonFinal = end < total - 1
  const incomplete = nonFinal && !decoded.endsWith('\n')
  // A non-final block never owns the fragment after its last newline. That
  // fragment may be an empty string when the Range ends exactly on `\n`; if it
  // remained cached, the first line of the next block would look permanently
  // loaded-but-empty. Keep only a single unterminated long first line as an
  // explicitly truncated preview.
  if (nonFinal && (!incomplete || lines.length > 1)) lines.pop()
  return { offset: entry.byteOffset, startLine: entry.line, lines, truncatedLastLine: incomplete && lines.length === 1 }
}

function findIndexEntry(entries: SparseLineEntry[], line: number): SparseLineEntry {
  let selected = entries[0] ?? { line: 1, byteOffset: 0 }
  for (const entry of entries) {
    if (entry.line > line) break
    selected = entry
  }
  return selected
}

function virtualScrollHeight(lineCount: number): number {
  return Math.min(MAX_SCROLL_PIXELS, Math.max(LINE_HEIGHT, lineCount * LINE_HEIGHT))
}

function scrollTopForLine(line: number, lineCount: number): number {
  if (lineCount * LINE_HEIGHT <= MAX_SCROLL_PIXELS) return Math.max(0, line - 1) * LINE_HEIGHT
  const lastWindowLine = Math.max(1, lineCount - VIEWPORT_LINES + 1)
  const logicalLine = Math.max(1, Math.min(lastWindowLine, line))
  const maximumScroll = MAX_SCROLL_PIXELS - VIEWPORT_LINES * LINE_HEIGHT
  return lastWindowLine <= 1 ? 0 : ((logicalLine - 1) / (lastWindowLine - 1)) * maximumScroll
}

function lineForScrollTop(scrollTop: number, scrollHeight: number, clientHeight: number, lineCount: number): number {
  if (lineCount * LINE_HEIGHT <= MAX_SCROLL_PIXELS) return Math.floor(scrollTop / LINE_HEIGHT) + 1
  const lastWindowLine = Math.max(1, lineCount - VIEWPORT_LINES + 1)
  const maximumScroll = Math.max(1, scrollHeight - clientHeight)
  return Math.floor((Math.max(0, scrollTop) / maximumScroll) * (lastWindowLine - 1)) + 1
}

export function GCodePreview({ setup, artifact, contentUrl, compact = false, onOpenSetupSheet, onArtifactChanged, initialLine = 1, onLineChanged, onAnalysisChanged, cacheScope, sourceFile }: Props) {
  const url = useMemo(
    () => contentUrl ?? contentURL(setup.setupId, artifact.artifactId),
    [artifact.artifactId, contentUrl, setup.setupId],
  )
  const workerRef = useRef<Worker | null>(null)
  const pendingBlockOffsetsRef = useRef(new Set<number>())
  const artifactGenerationRef = useRef(0)
  const viewportRef = useRef<HTMLDivElement>(null)
  const artifactChangedRef = useRef(onArtifactChanged)
  const lineChangedRef = useRef(onLineChanged)
  const analysisChangedRef = useRef(onAnalysisChanged)
  const identity = useMemo<GCodeCacheIdentity>(() => ({
    scope: cacheScope ?? `library:${setup.libraryId}`,
    artifactId: artifact.artifactId,
    version: artifact.version,
    byteSize: artifact.byteSize,
  }), [artifact.artifactId, artifact.byteSize, artifact.version, cacheScope, setup.libraryId])
  const restoredCache = useMemo(() => readLocalGCodeCacheState(identity), [identity])
  const [blocks, setBlocks] = useState<CachedBlock[]>([])
  const [entries, setEntries] = useState<SparseLineEntry[]>([{ line: 1, byteOffset: 0 }])
  const [lineCount, setLineCount] = useState(restoredCache?.lineCount ?? 1)
  const [firstLine, setFirstLine] = useState(() => Math.max(1, Math.floor(initialLine)))
  const [lineInput, setLineInput] = useState(() => String(Math.max(1, Math.floor(initialLine))))
  const [wrap, setWrap] = useState(false)
  const [indexProgress, setIndexProgress] = useState(0)
  const [indexComplete, setIndexComplete] = useState(false)
  const [tools, setTools] = useState<ToolTableEntry[]>(restoredCache?.tools ?? [])
  const [toolsTruncated, setToolsTruncated] = useState(restoredCache?.toolsTruncated ?? false)
  const [validation, setValidation] = useState<'pending' | 'online' | 'offline'>(artifact.byteSize === 0 ? 'online' : 'pending')
  const [workerReady, setWorkerReady] = useState(false)
  const [indexError, setIndexError] = useState<string>()
  const [previewError, setPreviewError] = useState<string>()
  const [searchError, setSearchError] = useState<string>()
  const [query, setQuery] = useState('')
  const [caseSensitive, setCaseSensitive] = useState(false)
	const [matches, setMatches] = useState<Float64Array>(() => new Float64Array())
  const [totalMatches, setTotalMatches] = useState(0)
  const [matchOffset, setMatchOffset] = useState(0)
  const [matchIndex, setMatchIndex] = useState(0)
	const [searchProgress, setSearchProgress] = useState(0)
	const [searching, setSearching] = useState(false)
	const [matchesTruncated, setMatchesTruncated] = useState(false)
  const pendingMatchIndexRef = useRef(0)
  const persistedIndexBytesRef = useRef(0)
  const requestBase = `${artifact.artifactId}-${artifact.version}`

  useEffect(() => {
    artifactChangedRef.current = onArtifactChanged
  }, [onArtifactChanged])

  useEffect(() => {
    lineChangedRef.current = onLineChanged
  }, [onLineChanged])

  useEffect(() => {
    analysisChangedRef.current = onAnalysisChanged
  }, [onAnalysisChanged])

  useEffect(() => {
    analysisChangedRef.current?.({
      artifactId: artifact.artifactId,
      version: artifact.version,
      progress: indexProgress,
      complete: indexComplete,
      lineCount,
      tools,
      toolsTruncated,
      validation,
      error: indexError,
    })
  }, [artifact.artifactId, artifact.version, indexComplete, indexError, indexProgress, lineCount, tools, toolsTruncated, validation])

  useEffect(() => {
    const restored = Math.max(1, Math.floor(initialLine))
    setFirstLine(restored)
    if (viewportRef.current) viewportRef.current.scrollTop = scrollTopForLine(restored, lineCount)
  }, [artifact.artifactId, initialLine, lineCount])

  useEffect(() => {
    setLineInput(String(Math.max(1, Math.floor(initialLine))))
  }, [artifact.artifactId, initialLine])

  useEffect(() => {
    if (!viewportRef.current) return
    viewportRef.current.scrollTop = wrap ? 0 : scrollTopForLine(firstLine, lineCount)
    // Switching modes preserves the logical window while wrapped rows use
    // natural measured heights instead of the fixed virtual row geometry.
  }, [firstLine, lineCount, wrap])

  const addBlock = useCallback((block: CachedBlock) => {
    setBlocks((current) => {
      const without = current.filter((item) => item.offset !== block.offset)
      return [...without, block].slice(-MAX_CACHE_BLOCKS)
    })
  }, [])

  useEffect(() => {
    const controller = new AbortController()
    const generation = artifactGenerationRef.current + 1
    artifactGenerationRef.current = generation
    const pendingOffsets = new Set<number>()
    pendingBlockOffsetsRef.current = pendingOffsets
		let indexStartTimer: number | undefined
		let worker: Worker | undefined
		let indexResolved = false
    setBlocks([])
    setEntries([{ line: 1, byteOffset: 0 }])
    const restored = readLocalGCodeCacheState(identity)
    persistedIndexBytesRef.current = 0
    setLineCount(restored?.lineCount ?? 1)
    setIndexProgress(0)
    setIndexComplete(false)
    setTools(restored?.tools ?? [])
    setToolsTruncated(restored?.toolsTruncated ?? false)
    setValidation(artifact.byteSize === 0 ? 'online' : 'pending')
    setWorkerReady(false)
    setIndexError(undefined)
    setPreviewError(undefined)
    setSearchError(undefined)
    setMatches(new Float64Array())
    setTotalMatches(0)
    setSearching(false)
    pendingOffsets.clear()

		const startWorker = () => {
			if (controller.signal.aborted || generation !== artifactGenerationRef.current) return
			if (typeof Worker === 'undefined') {
				setIndexError('WORKER_UNAVAILABLE')
				return
			}
			try {
				worker = new Worker(new URL('../workers/gcodeWorker.ts', import.meta.url), { type: 'module' })
			} catch {
				setIndexError('WORKER_UNAVAILABLE')
				return
			}
			workerRef.current = worker
			setWorkerReady(true)
			let workerStopped = false
			const stopWorker = () => {
				if (workerStopped) return
				workerStopped = true
				try { worker?.postMessage({ type: 'cancel', requestId: `${requestBase}-index` }) } catch { /* Worker already failed. */ }
				try { worker?.postMessage({ type: 'cancel', requestId: `${requestBase}-search` }) } catch { /* Worker already failed. */ }
				worker?.removeEventListener('message', onMessage)
				worker?.removeEventListener('error', onWorkerFailure)
				worker?.removeEventListener('messageerror', onWorkerFailure)
				worker?.terminate()
			}
			const onWorkerFailure = (event: Event) => {
				if (event.cancelable) event.preventDefault()
				if (controller.signal.aborted || generation !== artifactGenerationRef.current || workerStopped) return
				setWorkerReady(false)
				setSearching(false)
				if (indexResolved) setSearchError('WORKER_ERROR')
				else setIndexError('WORKER_ERROR')
				if (workerRef.current === worker) workerRef.current = null
				stopWorker()
			}
			const onMessage = (event: MessageEvent<WorkerResponse>) => {
        if (controller.signal.aborted || generation !== artifactGenerationRef.current) return
        const message = event.data
        if (message.requestId === `${requestBase}-index`) {
          if (message.type === 'progress') {
            const progress = message.totalBytes === 0 ? 1 : message.completedBytes / message.totalBytes
            setIndexProgress(progress)
            const persistStep = Math.min(MAX_PROGRESS_PERSIST_STEP, Math.max(BLOCK_BYTES, Math.floor(artifact.byteSize / 100)))
            if (message.completedBytes < message.totalBytes && message.completedBytes - persistedIndexBytesRef.current >= persistStep) {
              persistedIndexBytesRef.current = message.completedBytes
              void updateLocalGCodeCacheState(identity, { indexedBytes: message.completedBytes, cachedBytes: message.cachedBytes, analysisComplete: false })
            }
          }
					if (message.type === 'indexResult') {
						indexResolved = true
						setIndexError(undefined)
            setIndexComplete(true)
            setEntries(message.entries)
            setLineCount(message.lineCount)
            setTools(message.tools ?? [])
            setToolsTruncated(message.toolsTruncated ?? false)
            setIndexProgress(1)
            void updateLocalGCodeCacheState(identity, {
              indexedBytes: artifact.byteSize,
              analysisComplete: true,
              lineCount: message.lineCount,
              tools: message.tools ?? [],
              toolsTruncated: message.toolsTruncated ?? false,
            })
          }
          if (message.type === 'error') {
            setIndexError(message.code)
            if (message.code === 'ARTIFACT_CHANGED') artifactChangedRef.current?.()
          }
        }
        if (message.requestId === `${requestBase}-search`) {
					if (message.type === 'progress') {
						setSearchProgress(message.totalBytes === 0 ? 1 : message.completedBytes / message.totalBytes)
						if (message.totalMatches !== undefined) setTotalMatches(message.totalMatches)
					}
          if (message.type === 'searchResult') {
			setSearchError(undefined)
			setMatches(message.lineNumbers)
            setTotalMatches(message.totalMatches)
			setMatchOffset(message.matchOffset)
			const selectedIndex = Math.max(0, Math.min(message.lineNumbers.length - 1, pendingMatchIndexRef.current))
            setMatchIndex(selectedIndex)
						setMatchesTruncated(message.truncated)
						setSearchProgress(1)
						setSearching(false)
						if (message.lineNumbers.length > 0) {
							const selectedLine = message.lineNumbers[selectedIndex]
							setFirstLine(selectedLine)
							lineChangedRef.current?.(selectedLine)
						}
          }
						if (message.type === 'error') {
							setSearching(false)
							if (message.code !== 'CANCELLED') {
								setSearchError(message.code)
								if (message.code === 'ARTIFACT_CHANGED') artifactChangedRef.current?.()
							}
						}
        }
			}
			worker.addEventListener('error', onWorkerFailure)
			worker.addEventListener('messageerror', onWorkerFailure)
			// Keep the message handler last as well as semantically primary. Some
			// embedded WebViews expose only a minimal EventTarget-compatible Worker.
			worker.addEventListener('message', onMessage)
			workerCleanup = stopWorker
			try {
					worker.postMessage({ type: 'index', requestId: `${requestBase}-index`, cacheScope: identity.scope, artifactId: artifact.artifactId, url, version: artifact.version, byteSize: artifact.byteSize, file: sourceFile, cacheDisabled: isGCodeCachePersistenceQuarantined(identity.scope) })
			} catch {
				onWorkerFailure(new Event('messageerror'))
			}
		}

		let workerCleanup: () => void = () => undefined
    const scheduleIndex = () => {
      if (controller.signal.aborted) return
      indexStartTimer = window.setTimeout(startWorker, 0)
    }

    if (artifact.byteSize > 0) {
      let usedOfflineCache = false
      pendingOffsets.add(0)
      void fetchBlock(
        url,
        identity,
        { line: 1, byteOffset: 0 },
        controller.signal,
        PREVIEW_PREFIX_BYTES,
        true,
        () => { usedOfflineCache = true },
        sourceFile,
      ).then((block) => {
        pendingOffsets.delete(0)
        if (controller.signal.aborted || generation !== artifactGenerationRef.current) return
        addBlock(block)
        setIndexError(undefined)
        setValidation(usedOfflineCache ? 'offline' : 'online')
        setLineCount(Math.max(1, block.startLine + block.lines.length - 1))
        scheduleIndex()
      }, (reason: unknown) => {
        pendingOffsets.delete(0)
        if (controller.signal.aborted || generation !== artifactGenerationRef.current) return
        const code = reason instanceof Error ? reason.message : 'RANGE_FAILED'
        setIndexError(code)
        if (code === 'ARTIFACT_CHANGED') artifactChangedRef.current?.()
      })
    } else {
      scheduleIndex()
    }

    return () => {
      controller.abort()
      if (indexStartTimer !== undefined) window.clearTimeout(indexStartTimer)
      workerCleanup()
      if (workerRef.current === worker) workerRef.current = null
      pendingOffsets.clear()
    }
	}, [addBlock, artifact.artifactId, artifact.byteSize, artifact.version, identity, requestBase, sourceFile, url])

  useEffect(() => {
		const available = blocks.some((block) => firstLine >= block.startLine && firstLine < block.startLine + block.lines.length)
		if (available || artifact.byteSize === 0) return
		let entry = findIndexEntry(entries, firstLine)
		for (const block of blocks) {
			if (block.startLine <= firstLine && block.startLine > entry.line) {
				entry = { line: block.startLine, byteOffset: block.offset }
			}
		}
    const generation = artifactGenerationRef.current
    const pendingOffsets = pendingBlockOffsetsRef.current
    if (pendingOffsets.has(entry.byteOffset)) return
    pendingOffsets.add(entry.byteOffset)
    const controller = new AbortController()
		const source = sourceFile && sourceFile.size === artifact.byteSize
			? createUploadedFileRangeSource(identity, sourceFile)
			: { read: (start: number, endInclusive: number, _version: string, signal: AbortSignal) =>
				fetchRangeBytes(url, identity, start, endInclusive, signal) }
		void resolveSparseLineAnchor({
			source, version: artifact.version, byteSize: artifact.byteSize,
			signal: controller.signal, blockSize: BLOCK_BYTES, entry, targetLine: firstLine,
		}).then((resolved) => fetchBlock(url, identity, resolved, controller.signal, BLOCK_BYTES, false, undefined, sourceFile)).then((block) => {
      if (controller.signal.aborted || generation !== artifactGenerationRef.current) return
      setPreviewError(undefined)
      addBlock(block)
    }, (reason: unknown) => {
      if (controller.signal.aborted || generation !== artifactGenerationRef.current) return
      const code = reason instanceof Error ? reason.message : 'RANGE_FAILED'
      setPreviewError(code)
      if (code === 'ARTIFACT_CHANGED') artifactChangedRef.current?.()
    }).finally(() => pendingOffsets.delete(entry.byteOffset))
    return () => controller.abort()
  }, [addBlock, artifact.byteSize, artifact.version, blocks, entries, firstLine, identity, sourceFile, url])

  const visible = useMemo(() => {
    const start = Math.max(1, firstLine - OVERSCAN)
    const end = Math.min(lineCount, firstLine + VIEWPORT_LINES + OVERSCAN)
    const rows: Array<{ line: number; value?: string; truncated?: boolean }> = []
    for (let line = start; line <= end; line += 1) {
      const block = blocks.find((item) => line >= item.startLine && line < item.startLine + item.lines.length)
      const raw = block?.lines[line - (block?.startLine ?? 1)]
      const display = raw === undefined ? undefined : displayLine(raw)
      rows.push({ line, value: display?.text, truncated: display?.truncated || (block?.truncatedLastLine && line === block.startLine) })
    }
    return rows
  }, [blocks, firstLine, lineCount])

  const goToLine = useCallback((line: number) => {
    const safe = Math.max(1, Math.min(lineCount, Math.trunc(line)))
    setFirstLine(safe)
    setLineInput(String(safe))
    onLineChanged?.(safe)
    if (viewportRef.current) viewportRef.current.scrollTop = scrollTopForLine(safe, lineCount)
  }, [lineCount, onLineChanged])

  const submitSearch = (event: React.FormEvent) => {
    event.preventDefault()
    if (!workerRef.current) return
    workerRef.current?.postMessage({ type: 'cancel', requestId: `${requestBase}-search` })
		setMatches(new Float64Array())
    setSearchError(undefined)
    setTotalMatches(0)
		setMatchOffset(0)
		setMatchesTruncated(false)
		setSearchProgress(0)
    if (query === '') return
		setSearching(true)
		pendingMatchIndexRef.current = 0
	    workerRef.current?.postMessage({
	      type: 'search', requestId: `${requestBase}-search`, cacheScope: identity.scope, artifactId: artifact.artifactId, url,
	      version: artifact.version, byteSize: artifact.byteSize, file: sourceFile, query, caseSensitive, matchOffset: 0,
			cacheDisabled: isGCodeCachePersistenceQuarantined(identity.scope),
	    })
  }

	const cancelSearch = () => {
		workerRef.current?.postMessage({ type: 'cancel', requestId: `${requestBase}-search` })
		setSearching(false)
	}

  const requestMatchPage = (offset: number, selectedIndex: number) => {
		workerRef.current?.postMessage({ type: 'cancel', requestId: `${requestBase}-search` })
		pendingMatchIndexRef.current = selectedIndex
		setSearchProgress(0)
		setSearchError(undefined)
		setSearching(true)
			workerRef.current?.postMessage({
				type: 'search', requestId: `${requestBase}-search`, cacheScope: identity.scope, artifactId: artifact.artifactId, url,
				version: artifact.version, byteSize: artifact.byteSize, file: sourceFile, query, caseSensitive, matchOffset: offset,
				cacheDisabled: isGCodeCachePersistenceQuarantined(identity.scope),
			})
	}

  const selectMatch = (index: number) => {
		setMatchIndex(index)
		goToLine(matches[index])
	}

  const moveMatch = (direction: 1 | -1) => {
    if (totalMatches === 0 || matches.length === 0) return
		const ordinal = matchOffset + matchIndex
		const nextOrdinal = (ordinal + direction + totalMatches) % totalMatches
		const nextOffset = Math.floor(nextOrdinal / MATCH_PAGE_SIZE) * MATCH_PAGE_SIZE
		const nextIndex = nextOrdinal - nextOffset
		if (nextOffset === matchOffset && nextIndex < matches.length) {
			selectMatch(nextIndex)
			return
		}
		requestMatchPage(nextOffset, nextIndex)
  }

	const jumpToMatch = (event: React.FormEvent<HTMLFormElement>) => {
		event.preventDefault()
		if (totalMatches === 0) return
		const ordinal = Math.max(1, Math.min(totalMatches, Math.trunc(Number(new FormData(event.currentTarget).get('match'))))) - 1
		const offset = Math.floor(ordinal / MATCH_PAGE_SIZE) * MATCH_PAGE_SIZE
		const index = ordinal - offset
		if (offset === matchOffset && index < matches.length) selectMatch(index)
		else requestMatchPage(offset, index)
	}

	const matchWindowStart = Math.max(0, Math.min(matches.length - MATCH_RESULT_WINDOW, matchIndex - Math.floor(MATCH_RESULT_WINDOW / 2)))
	const visibleMatches = matches.slice(matchWindowStart, matchWindowStart + MATCH_RESULT_WINDOW)

  const hasSheet = setup.artifacts.some((item) => item.role === 'setup_sheet')
  const displayedError = indexError ?? previewError ?? searchError
  return (
    <section
      className={`gcode-preview${compact ? ' gcode-preview--compact' : ''}`}
      aria-labelledby={compact ? undefined : 'preview-title'}
      aria-label={compact ? `G-code ${artifact.displayName}` : undefined}
    >
      {!compact ? <header className="preview-header">
        <div>
          <p className="eyebrow">G-code preview · revision {setup.revision}</p>
          <h2 id="preview-title">{artifact.displayName}</h2>
          <p>{setup.name} · {artifact.byteSize.toLocaleString()} байт · версия {artifact.version.slice(0, 12)}… {artifact.primary ? '· Основная программа' : ''}</p>
        </div>
        <div className="preview-header__actions">
          <Button type="button" className="button button--quiet" variant="outlined" disabled={!hasSheet} onClick={onOpenSetupSheet}>Setup Sheet</Button>
          <FormControlLabel className="toggle" sx={{ margin: 0 }} control={<Checkbox size="small" sx={{ padding: '2px' }} checked={wrap} onChange={(event) => setWrap(event.target.checked)} />} label="Перенос строк" />
        </div>
      </header> : null}
      {!compact ? <div className="index-progress" role="status">
        Индекс строк: {Math.round(indexProgress * 100)}%
        <progress max={1} value={indexProgress} aria-label="Прогресс индекса строк" />
      </div> : null}
      {displayedError ? <Alert className="preview-error" severity="warning" role="alert">
        {displayedError === 'ARTIFACT_CHANGED' ? 'Артефакт изменён. Обновите карточку перед продолжением.' : searchError && !indexError && !previewError ? 'Поиск не выполнен.' : 'Текстовый preview недоступен.'}
        {displayedError === 'ARTIFACT_CHANGED' && onArtifactChanged ? <Button type="button" className="button button--quiet" variant="outlined" onClick={() => artifactChangedRef.current?.()}>Обновить карточку</Button> : null}
      </Alert> : null}
      {artifact.byteSize === 0 ? <div className="preview-empty" role="status">Программа пуста.</div> : null}
      <div className="preview-tools">
        {compact ? <FormControlLabel className="toggle" sx={{ margin: 0 }} control={<Checkbox size="small" sx={{ padding: '2px' }} checked={wrap} onChange={(event) => setWrap(event.target.checked)} />} label="Перенос" /> : null}
        <form onSubmit={(event) => { event.preventDefault(); goToLine(Number(lineInput)) }}>
          <label>Строка <input name="line" type="number" min={1} max={lineCount} value={lineInput} onChange={(event) => setLineInput(event.target.value)} /></label>
          <Button type="submit" className="button button--quiet" variant="outlined">Перейти</Button>
        </form>
        <form role="search" onSubmit={submitSearch}>
			<label>Поиск <input type="search" value={query} onChange={(event) => setQuery(event.target.value)} /></label>
          <FormControlLabel className="toggle" sx={{ margin: 0 }} control={<Checkbox size="small" sx={{ padding: '2px' }} checked={caseSensitive} onChange={(event) => setCaseSensitive(event.target.checked)} />} label="Учитывать регистр" />
          <Button type="submit" className="button button--quiet" variant="outlined" disabled={!workerReady || searching}>Найти</Button>
					{searching ? <Button type="button" className="button button--quiet" variant="outlined" onClick={cancelSearch}>Отменить поиск</Button> : null}
          <IconButton type="button" size="small" aria-label="Предыдущее совпадение" onClick={() => moveMatch(-1)} disabled={totalMatches === 0 || searching}>↑</IconButton>
          <IconButton type="button" size="small" aria-label="Следующее совпадение" onClick={() => moveMatch(1)} disabled={totalMatches === 0 || searching}>↓</IconButton>
						<output aria-live="polite">
							{searching ? `Поиск ${Math.round(searchProgress * 100)}% · найдено ${totalMatches}` : totalMatches > 0 ? `${matchOffset + matchIndex + 1} из ${totalMatches}${matchesTruncated ? ' · компактные страницы результатов' : ''}` : 'Нет совпадений'}
						</output>
        </form>
					<form onSubmit={jumpToMatch}>
						<label>Совпадение № <input key={`${matchOffset}-${matchIndex}-${totalMatches}`} name="match" type="number" min={1} max={Math.max(1, totalMatches)} defaultValue={totalMatches > 0 ? matchOffset + matchIndex + 1 : 1} disabled={totalMatches === 0 || searching} /></label>
						<Button type="submit" className="button button--quiet" variant="outlined" disabled={totalMatches === 0 || searching}>Перейти к совпадению</Button>
					</form>
      </div>
			{totalMatches > 0 && matches.length > 0 ? (
				<ol className="search-result-window" aria-label="Виртуализированные результаты поиска">
					{Array.from(visibleMatches, (line, index) => {
						const localIndex = matchWindowStart + index
						const ordinal = matchOffset + localIndex
							return <li key={ordinal}><button type="button" aria-current={localIndex === matchIndex ? 'true' : undefined} onClick={() => selectMatch(localIndex)}>Совпадение {ordinal + 1}, строка {line}</button></li>
					})}
				</ol>
			) : null}
      <div
        ref={viewportRef}
        className={`code-viewport${wrap ? ' code-viewport--wrap' : ''}`}
        onScroll={(event) => {
          if (wrap) return
          const line = lineForScrollTop(
            event.currentTarget.scrollTop,
            event.currentTarget.scrollHeight,
            event.currentTarget.clientHeight,
            lineCount,
          )
          setFirstLine(line)
          onLineChanged?.(line)
        }}
        tabIndex={0}
        aria-label={`Программа ${artifact.displayName}`}
      >
        <div className="code-spacer" style={wrap ? undefined : { height: `${virtualScrollHeight(lineCount)}px` }}>
          {visible.map((row) => (
            <div className={`code-line${wrap ? ' code-line--wrapped' : ''}`} key={row.line} style={wrap ? { minHeight: `${LINE_HEIGHT}px` } : {
              top: `${lineCount * LINE_HEIGHT > MAX_SCROLL_PIXELS
                ? scrollTopForLine(firstLine, lineCount) + (row.line - firstLine) * LINE_HEIGHT
                : (row.line - 1) * LINE_HEIGHT}px`,
              height: `${LINE_HEIGHT}px`,
            }}>
              <span className="code-line__number" aria-hidden="true">{row.line}</span>
				<code>{row.value === undefined ? '…' : tokenizeGCode(row.value).map((token, index) => token.kind
					? <span className={`gcode-token gcode-token--${token.kind}`} key={`${index}-${token.kind}`}>{token.text}</span>
					: token.text)}{row.truncated ? <span className="code-line__truncated"> [строка сокращена]</span> : null}</code>
            </div>
          ))}
        </div>
      </div>
			{wrap ? (
				<nav className="wrapped-window-nav" aria-label="Навигация по перенесённым строкам">
						<Button
							type="button"
							className="button button--quiet"
							variant="outlined"
							disabled={firstLine <= 1}
							onClick={() => goToLine(firstLine - VIEWPORT_LINES)}
						>
							Предыдущие строки
						</Button>
					<output aria-live="polite">
						Строки {Math.max(1, firstLine - OVERSCAN)}–{Math.min(lineCount, firstLine + VIEWPORT_LINES + OVERSCAN)} из {lineCount}
					</output>
						<Button
							type="button"
							className="button button--quiet"
							variant="outlined"
							disabled={firstLine + VIEWPORT_LINES > lineCount}
							onClick={() => goToLine(firstLine + VIEWPORT_LINES)}
						>
							Следующие строки
						</Button>
				</nav>
			) : null}
    </section>
  )
}
