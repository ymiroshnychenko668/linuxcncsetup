export const DEFAULT_BLOCK_SIZE = 1 << 20
export const SPARSE_LINE_STRIDE = 256
export const MAX_STORED_MATCHES = 512
export const MAX_SPARSE_ENTRIES = 100_000

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
	let line = 1
	let stride = SPARSE_LINE_STRIDE
  for (let offset = 0; offset < options.byteSize;) {
    options.signal.throwIfAborted()
    const end = Math.min(options.byteSize, offset + blockSize)
    const block = await options.source.read(offset, end - 1, options.version, options.signal)
    if (block.byteLength !== end - offset) throw new Error('INCOMPLETE_RANGE')
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
  if (options.byteSize === 0) options.onProgress?.({ completedBytes: 0, totalBytes: 0 })
  return { lineCount: line, entries }
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

export async function literalSearch(
	options: ScanOptions & {
		query: string
		caseSensitive: boolean
		maxStoredMatches?: number
		matchOffset?: number
		onMatchProgress?: (totalMatches: number) => void
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
  const needle = options.caseSensitive ? options.query : options.query.toLocaleLowerCase()
  const stored: number[] = []
  let totalMatches = 0
  let carry = ''
  let lineAtCarry = 1

  const process = (text: string, final: boolean) => {
    const combined = carry + text
    const comparable = options.caseSensitive ? combined : combined.toLocaleLowerCase()
    const retainedUnits = final ? 0 : Math.max(needle.length - 1, 0)
    const safeLength = Math.max(0, combined.length - retainedUnits)
    let scanPosition = 0
    let line = lineAtCarry
    let from = 0
    while (from < safeLength) {
      const match = comparable.indexOf(needle, from)
      if (match < 0 || match >= safeLength) break
      while (scanPosition < match) {
        if (combined.charCodeAt(scanPosition) === 0x0a) line += 1
        scanPosition += 1
      }
      const ordinal = totalMatches
      totalMatches += 1
      if (ordinal >= matchOffset && stored.length < maxStored) stored.push(line)
      from = match + 1
    }
    while (scanPosition < safeLength) {
      if (combined.charCodeAt(scanPosition) === 0x0a) line += 1
      scanPosition += 1
    }
    carry = combined.slice(safeLength)
    lineAtCarry = line
  }

  for (let offset = 0; offset < options.byteSize;) {
    options.signal.throwIfAborted()
    const end = Math.min(options.byteSize, offset + blockSize)
    const block = await options.source.read(offset, end - 1, options.version, options.signal)
    if (block.byteLength !== end - offset) throw new Error('INCOMPLETE_RANGE')
    process(decoder.decode(block, { stream: end < options.byteSize }), false)
    offset = end
    options.onProgress?.({ completedBytes: offset, totalBytes: options.byteSize })
		options.onMatchProgress?.(totalMatches)
  }
  process(decoder.decode(), true)
  if (options.byteSize === 0) options.onProgress?.({ completedBytes: 0, totalBytes: 0 })
  return {
    totalMatches,
		lineNumbers: Float64Array.from(stored),
    matchOffset,
    truncated: matchOffset > 0 || totalMatches > matchOffset + stored.length,
  }
}
