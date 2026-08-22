export const DEFAULT_BLOCK_SIZE = 1 << 20
export const SPARSE_LINE_STRIDE = 256
export const MAX_STORED_MATCHES = 512
export const MAX_SPARSE_ENTRIES = 100_000
export const MAX_TOOL_TABLE_ENTRIES = 1_024
const MAX_TOOL_LINE_CHARS = 65_536
const LINUXCNC_GCODE_FUNCTIONS = new Set([
	'ABS', 'ACOS', 'ASIN', 'ATAN', 'COS', 'EXISTS', 'EXP',
	'FIX', 'FUP', 'LN', 'ROUND', 'SIN', 'SQRT', 'TAN',
])

export interface RangeSource {
  read(start: number, endInclusive: number, version: string, signal: AbortSignal): Promise<Uint8Array>
}

export interface ScanProgress {
  completedBytes: number
  totalBytes: number
}

export interface SparseLineEntry {
  line: number
  byteOffset: number
}

export interface LineIndexResult {
  lineCount: number
  entries: SparseLineEntry[]
  tools: ToolTableEntry[]
  toolsTruncated: boolean
}

export interface ToolTableEntry {
  toolNumber: number
  firstLine: number
  references: number
  changes: number
}

export interface SearchResult {
  totalMatches: number
	lineNumbers: Float64Array
  matchOffset: number
  truncated: boolean
}

interface ScanOptions {
  source: RangeSource
  version: string
  byteSize: number
  signal: AbortSignal
  blockSize?: number
  onProgress?: (progress: ScanProgress) => void
}

interface ResolveLineAnchorOptions extends ScanOptions {
  entry: SparseLineEntry
  targetLine: number
}

function checkedOptions(options: ScanOptions): number {
  if (!Number.isSafeInteger(options.byteSize) || options.byteSize < 0) {
    throw new Error('INVALID_SIZE')
  }
  const blockSize = options.blockSize ?? DEFAULT_BLOCK_SIZE
  if (!Number.isSafeInteger(blockSize) || blockSize < 1 || blockSize > 4 << 20) {
    throw new Error('INVALID_BLOCK_SIZE')
  }
  return blockSize
}

export async function buildSparseLineIndex(options: ScanOptions): Promise<LineIndexResult> {
  const blockSize = checkedOptions(options)
	const entries: SparseLineEntry[] = [{ line: 1, byteOffset: 0 }]
	const tools = new Map<number, ToolTableEntry>()
	const decoder = new TextDecoder('utf-8', { fatal: true })
	let toolCarry = ''
	let toolLine = 1
	let skipToolLine = false
	let toolsTruncated = false
	let pendingTool: number | undefined
	let line = 1
	let stride = SPARSE_LINE_STRIDE

	const recordReference = (toolNumber: number, sourceLine: number) => {
		const existing = tools.get(toolNumber)
		if (existing) {
			existing.references = Math.min(Number.MAX_SAFE_INTEGER, existing.references + 1)
			return
		}
		if (tools.size >= MAX_TOOL_TABLE_ENTRIES) {
			toolsTruncated = true
			return
		}
		tools.set(toolNumber, { toolNumber, firstLine: sourceLine, references: 1, changes: 0 })
	}
	const analyzeToolLine = (sourceLine: string, sourceLineNumber: number) => {
		let code = ''
		let commentDepth = 0
		for (let index = 0; index < sourceLine.length; index += 1) {
			const character = sourceLine[index]
			if (character === ';' && commentDepth === 0) break
			if (character === '(') { commentDepth += 1; continue }
			if (character === ')' && commentDepth > 0) { commentDepth -= 1; continue }
			if (commentDepth === 0) code += character
		}
		// LinuxCNC ignores spaces and tabs outside comments, including between
		// the digits of a word and inside parameter references.
		code = code.replace(/[ \t]/g, '')
		let hasToolWord = false
		let lastToolOnLine: number | undefined
		let changeWords = 0
		const numberWord = /[+-]?(?:\d+(?:\.\d*)?|\.\d+)/y
		const skipBracketExpression = (start: number): number => {
			let depth = 0
			for (let cursor = start; cursor < code.length; cursor += 1) {
				if (code[cursor] === '[') depth += 1
				else if (code[cursor] === ']' && --depth === 0) return cursor + 1
			}
			return code.length
		}
		const skipParameter = (start: number): number => {
			let cursor = start + 1
			if (code[cursor] === '<') {
				const end = code.indexOf('>', cursor + 1)
				return end < 0 ? code.length : end + 1
			}
			if (code[cursor] === '[') return skipBracketExpression(cursor)
			while (cursor < code.length && /\d/.test(code[cursor])) cursor += 1
			return cursor
		}
		for (let index = 0; index < code.length;) {
			const character = code[index]
			if (character === '[') { index = skipBracketExpression(index); continue }
			if (character === '#') { index = skipParameter(index); continue }
			if (!/[A-Za-z]/.test(character) || (index > 0 && /[A-Za-z_]/.test(code[index - 1]))) {
				index += 1
				continue
			}
			const letter = character.toUpperCase()
			let valueStart = index + 1
			while (valueStart < code.length && /\s/.test(code[valueStart])) valueStart += 1
			if (letter === 'O' && code[valueStart] === '<') {
				const end = code.indexOf('>', valueStart + 1)
				index = end < 0 ? code.length : end + 1
				continue
			}
			let expressionStart = valueStart
			if (code[expressionStart] === '+' || code[expressionStart] === '-') expressionStart += 1
			if (code[expressionStart] === '[' || code[expressionStart] === '#') {
				if (letter === 'T') {
					hasToolWord = true
					lastToolOnLine = undefined
				}
				index = code[expressionStart] === '['
					? skipBracketExpression(expressionStart)
					: skipParameter(expressionStart)
				continue
			}
			if (letter === 'T' && /[A-Za-z]/.test(code[expressionStart])) {
				let functionEnd = expressionStart + 1
				while (functionEnd < code.length && /[A-Za-z]/.test(code[functionEnd])) functionEnd += 1
				const functionName = code.slice(expressionStart, functionEnd).toUpperCase()
				if (LINUXCNC_GCODE_FUNCTIONS.has(functionName) && code[functionEnd] === '[') {
					hasToolWord = true
					lastToolOnLine = undefined
					index = skipBracketExpression(functionEnd)
					continue
				}
			}
			numberWord.lastIndex = valueStart
			const match = numberWord.exec(code)
			if (!match) {
				index += 1
				continue
			}
			const value = Number(match[0])
			if (letter === 'T') {
				hasToolWord = true
				if (Number.isSafeInteger(value) && value >= 0 && value <= 999_999_999) {
					lastToolOnLine = value
					recordReference(value, sourceLineNumber)
				} else {
					lastToolOnLine = undefined
				}
			}
			if (letter === 'M' && value === 6) changeWords += 1
			index = numberWord.lastIndex
		}
		if (hasToolWord) pendingTool = lastToolOnLine
		if (changeWords > 0 && pendingTool !== undefined) {
			const entry = tools.get(pendingTool)
			if (entry) entry.changes = Math.min(Number.MAX_SAFE_INTEGER, entry.changes + changeWords)
			pendingTool = undefined
		}
	}
	const appendToolFragment = (fragment: string) => {
		if (skipToolLine) return
		if (toolCarry.length + fragment.length > MAX_TOOL_LINE_CHARS) {
			toolCarry = ''
			skipToolLine = true
			toolsTruncated = true
			pendingTool = undefined
			return
		}
		toolCarry += fragment
	}
	const consumeToolText = (text: string, final = false) => {
		let start = 0
		for (;;) {
			const newline = text.indexOf('\n', start)
			if (newline < 0) break
			appendToolFragment(text.slice(start, newline))
			if (!skipToolLine) analyzeToolLine(toolCarry.endsWith('\r') ? toolCarry.slice(0, -1) : toolCarry, toolLine)
			toolCarry = ''
			skipToolLine = false
			toolLine += 1
			start = newline + 1
		}
		appendToolFragment(text.slice(start))
		if (final && !skipToolLine) analyzeToolLine(toolCarry.endsWith('\r') ? toolCarry.slice(0, -1) : toolCarry, toolLine)
	}
  for (let offset = 0; offset < options.byteSize;) {
    options.signal.throwIfAborted()
    const end = Math.min(options.byteSize, offset + blockSize)
    const block = await options.source.read(offset, end - 1, options.version, options.signal)
    if (block.byteLength !== end - offset) throw new Error('INCOMPLETE_RANGE')
		try {
			consumeToolText(decoder.decode(block, { stream: end < options.byteSize }))
		} catch {
			throw new Error('UNSUPPORTED_ENCODING')
		}
    for (let index = 0; index < block.byteLength; index += 1) {
      if (block[index] !== 0x0a) continue
      line += 1
			const byteOffset = offset + index + 1
			const lastEntry = entries[entries.length - 1]
			if ((line - 1) % stride === 0 || byteOffset - lastEntry.byteOffset >= blockSize) {
				entries.push({ line, byteOffset })
				if (entries.length > MAX_SPARSE_ENTRIES) {
					for (let target = 1, source = 2; source < entries.length; target += 1, source += 2) {
						entries[target] = entries[source]
					}
					entries.length = Math.ceil(entries.length / 2)
					stride *= 2
				}
      }
    }
    offset = end
    options.onProgress?.({ completedBytes: offset, totalBytes: options.byteSize })
  }
  try {
		consumeToolText(decoder.decode(), true)
	} catch {
		throw new Error('UNSUPPORTED_ENCODING')
	}
  if (options.byteSize === 0) options.onProgress?.({ completedBytes: 0, totalBytes: 0 })
  return {
		lineCount: line,
		entries,
		tools: [...tools.values()].sort((left, right) => left.toolNumber - right.toolNumber),
		toolsTruncated,
	}
}

// resolveSparseLineAnchor advances from a trusted sparse line boundary to the
// exact byte boundary of a later line. Index thinning deliberately bounds the
// number of retained entries, so two adjacent entries may eventually be many
// range blocks apart. This scan retains only one caller-bounded block at a
// time and checks cancellation around every asynchronous read.
export async function resolveSparseLineAnchor(options: ResolveLineAnchorOptions): Promise<SparseLineEntry> {
  const blockSize = checkedOptions(options)
  const { entry, targetLine } = options
  if (!Number.isSafeInteger(entry.line) || entry.line < 1 ||
      !Number.isSafeInteger(entry.byteOffset) || entry.byteOffset < 0 || entry.byteOffset > options.byteSize ||
      !Number.isSafeInteger(targetLine) || targetLine < entry.line) {
    throw new Error('INVALID_LINE_ANCHOR')
  }
  if (targetLine === entry.line || entry.byteOffset === options.byteSize) return entry

  let line = entry.line
  for (let offset = entry.byteOffset; offset < options.byteSize;) {
    options.signal.throwIfAborted()
    const end = Math.min(options.byteSize, offset + blockSize)
    const block = await options.source.read(offset, end - 1, options.version, options.signal)
    options.signal.throwIfAborted()
    if (block.byteLength !== end - offset) throw new Error('INCOMPLETE_RANGE')
    for (let index = 0; index < block.byteLength; index += 1) {
      if (block[index] !== 0x0a) continue
      line += 1
      if (line === targetLine) return { line, byteOffset: offset + index + 1 }
    }
    offset = end
  }
  throw new Error('LINE_NOT_FOUND')
}

function foldSearchCodePoint(value: string, caseSensitive: boolean): string {
	if (caseSensitive) return value
	const folded = value.toLowerCase()
	const iterator = folded[Symbol.iterator]()
	const first = iterator.next()
	if (first.done || !iterator.next().done) return value
	return first.value
}

export async function literalSearch(
	options: ScanOptions & {
		query: string
		caseSensitive: boolean
		maxStoredMatches?: number
		matchOffset?: number
		onMatchProgress?: (totalMatches: number) => void
		onDiagnostics?: (diagnostics: { comparisons: number; scannedCodePoints: number }) => void
	},
): Promise<SearchResult> {
  const blockSize = checkedOptions(options)
	if (options.query.length === 0) throw new Error('EMPTY_QUERY')
	if (options.query.length > 1024) throw new Error('QUERY_TOO_LONG')
  const maxStored = options.maxStoredMatches ?? MAX_STORED_MATCHES
  if (!Number.isSafeInteger(maxStored) || maxStored < 0) throw new Error('INVALID_MATCH_LIMIT')
  const matchOffset = options.matchOffset ?? 0
  if (!Number.isSafeInteger(matchOffset) || matchOffset < 0) throw new Error('INVALID_MATCH_OFFSET')

  const decoder = new TextDecoder('utf-8', { fatal: true })
  const pattern = Array.from(options.query, (value) => foldSearchCodePoint(value, options.caseSensitive))
  const failure = new Uint16Array(pattern.length)
  let comparisons = 0
  for (let index = 1; index < pattern.length; index += 1) {
		let candidate = failure[index - 1]
		for (;;) {
			comparisons += 1
			if (pattern[index] === pattern[candidate]) {
				failure[index] = candidate + 1
				break
			}
			if (candidate === 0) break
			candidate = failure[candidate - 1]
		}
  }
  const stored: number[] = []
  const startLines = new Float64Array(pattern.length)
  let totalMatches = 0
  let matched = 0
  let scannedCodePoints = 0
  let line = 1

  const process = (text: string) => {
		for (const sourceCodePoint of text) {
			startLines[scannedCodePoints % pattern.length] = line
			const folded = foldSearchCodePoint(sourceCodePoint, options.caseSensitive)
			for (;;) {
				comparisons += 1
				if (folded === pattern[matched]) {
					matched += 1
					break
				}
				if (matched === 0) break
				matched = failure[matched - 1]
			}
			if (matched === pattern.length) {
				const ordinal = totalMatches
				totalMatches += 1
				if (ordinal >= matchOffset && stored.length < maxStored) {
					const start = scannedCodePoints - pattern.length + 1
					stored.push(startLines[start % pattern.length])
				}
				matched = failure[matched - 1]
			}
			if (sourceCodePoint === '\n') line += 1
			scannedCodePoints += 1
		}
  }

  for (let offset = 0; offset < options.byteSize;) {
    options.signal.throwIfAborted()
    const end = Math.min(options.byteSize, offset + blockSize)
    const block = await options.source.read(offset, end - 1, options.version, options.signal)
    if (block.byteLength !== end - offset) throw new Error('INCOMPLETE_RANGE')
    process(decoder.decode(block, { stream: end < options.byteSize }))
    offset = end
    options.onProgress?.({ completedBytes: offset, totalBytes: options.byteSize })
		options.onMatchProgress?.(totalMatches)
  }
  process(decoder.decode())
  if (options.byteSize === 0) options.onProgress?.({ completedBytes: 0, totalBytes: 0 })
  options.onDiagnostics?.({ comparisons, scannedCodePoints })
  return {
    totalMatches,
		lineNumbers: Float64Array.from(stored),
    matchOffset,
    truncated: matchOffset > 0 || totalMatches > matchOffset + stored.length,
  }
}
