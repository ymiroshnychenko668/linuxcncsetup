import { describe, expect, it } from 'vitest'
import { buildSparseLineIndex, DEFAULT_BLOCK_SIZE, literalSearch, resolveSparseLineAnchor, type RangeSource } from './gcodeCore'

function memorySource(value: string): { source: RangeSource; bytes: Uint8Array } {
  const bytes = new TextEncoder().encode(value)
  return {
    bytes,
    source: {
		read(start, end, _version, signal) {
        signal.throwIfAborted()
		return Promise.resolve(bytes.slice(start, end + 1))
      },
    },
  }
}

describe('G-code worker core', () => {
  it('indexes LF and CRLF with sparse byte offsets', async () => {
    const text = Array.from({ length: 520 }, (_, index) => `N${index}\r\n`).join('')
    const { source, bytes } = memorySource(text)
    const progress: number[] = []
    const result = await buildSparseLineIndex({
      source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
      blockSize: 37, onProgress: (value) => progress.push(value.completedBytes),
    })
    expect(result.lineCount).toBe(521)
    expect(result.entries[0]).toEqual({ line: 1, byteOffset: 0 })
    expect(result.entries.map((entry) => entry.line)).toEqual(expect.arrayContaining([257, 513]))
    for (let index = 1; index < result.entries.length; index += 1) {
      expect(result.entries[index].byteOffset - result.entries[index - 1].byteOffset).toBeLessThanOrEqual(43)
    }
    expect(progress.at(-1)).toBe(bytes.length)
  })

  it('finds literal matches across block boundaries and keeps compact results', async () => {
    const { source, bytes } = memorySource('G0 X0\nneedle\nG1\nNEEDLE\nxxneedlezz')
    const result = await literalSearch({
      source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
      blockSize: 5, query: 'needle', caseSensitive: false, maxStoredMatches: 2,
    })
    expect(result.totalMatches).toBe(3)
    expect(Array.from(result.lineNumbers)).toEqual([2, 4])
    expect(result.matchOffset).toBe(0)
    expect(result.truncated).toBe(true)

		const tail = await literalSearch({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 5, query: 'needle', caseSensitive: false, matchOffset: 2, maxStoredMatches: 2,
		})
		expect(tail.totalMatches).toBe(3)
		expect(Array.from(tail.lineNumbers)).toEqual([5])
		expect(tail.matchOffset).toBe(2)
		expect(tail.truncated).toBe(true)
  })

  it('indexes line two after a first line larger than one range block', async () => {
    const firstLine = 'X'.repeat(DEFAULT_BLOCK_SIZE + 17)
    const { source, bytes } = memorySource(`${firstLine}\nG1 X1\n`)
    const result = await buildSparseLineIndex({
      source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
    })
    expect(result.entries).toContainEqual({ line: 2, byteOffset: DEFAULT_BLOCK_SIZE + 18 })
    expect(result.lineCount).toBe(3)
  })

  it('cancels before allocating or requesting a block', async () => {
    const controller = new AbortController()
    controller.abort()
		const source: RangeSource = { read: () => Promise.reject(new Error('should not read')) }
    await expect(buildSparseLineIndex({
      source, byteSize: 10_000_000_000, version: 'v', signal: controller.signal,
    })).rejects.toMatchObject({ name: 'AbortError' })
  })

	it('advances across multiple bounded blocks when thinned sparse anchors are far apart', async () => {
		const prefix = 'X'.repeat(80)
		const { source, bytes } = memorySource(`${prefix}\nTARGET\n`)
		const reads: Array<[number, number, string]> = []
		const traced: RangeSource = {
			read(start, end, version, signal) {
				reads.push([start, end, version])
				return source.read(start, end, version, signal)
			},
		}
		const result = await resolveSparseLineAnchor({
			source: traced, version: 'stable-version', byteSize: bytes.byteLength,
			signal: new AbortController().signal, blockSize: 16,
			entry: { line: 1, byteOffset: 0 }, targetLine: 2,
		})
		expect(result).toEqual({ line: 2, byteOffset: prefix.length + 1 })
		expect(reads.length).toBeGreaterThan(1)
		expect(reads.every(([, , version]) => version === 'stable-version')).toBe(true)
		expect(reads.every(([start, end]) => end - start + 1 <= 16)).toBe(true)
	})

	it('stops adaptive advancement after cancellation between bounded reads', async () => {
		const controller = new AbortController()
		const { source, bytes } = memorySource(`${'X'.repeat(80)}\nTARGET\n`)
		let reads = 0
		const cancellingSource: RangeSource = {
			async read(start, end, version, signal) {
				reads += 1
				const block = await source.read(start, end, version, signal)
				controller.abort()
				return block
			},
		}
		await expect(resolveSparseLineAnchor({
			source: cancellingSource, version: 'stable-version', byteSize: bytes.byteLength,
			signal: controller.signal, blockSize: 16,
			entry: { line: 1, byteOffset: 0 }, targetLine: 2,
		})).rejects.toMatchObject({ name: 'AbortError' })
		expect(reads).toBe(1)
	})
})
