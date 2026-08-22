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

  it('builds a bounded lexical Tool Table during the same streamed index pass', async () => {
    const { source, bytes } = memorySource([
      '\ufeffT1 (roughing T99)\r',
      'M06',
      '; T88 M6',
      'm6 t 2',
      'T3 T4 M6',
      'M6',
      'T#1 M6',
      'T2',
    ].join('\n'))
    const result = await buildSparseLineIndex({
      source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
      blockSize: 5,
    })
    expect(result.tools).toEqual([
      { toolNumber: 1, firstLine: 1, references: 1, changes: 1 },
      { toolNumber: 2, firstLine: 4, references: 2, changes: 1 },
      { toolNumber: 3, firstLine: 5, references: 1, changes: 0 },
      { toolNumber: 4, firstLine: 5, references: 1, changes: 1 },
    ])
    expect(result.toolsTruncated).toBe(false)
  })

  it('does not retain an unbounded line solely to extract tools', async () => {
    const { source, bytes } = memorySource(`${'X'.repeat(70_000)}T7\nT8 M6`)
    const result = await buildSparseLineIndex({
      source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
      blockSize: 1_024,
    })
    expect(result.tools).toEqual([{ toolNumber: 8, firstLine: 2, references: 1, changes: 1 }])
    expect(result.toolsTruncated).toBe(true)
  })

	it('clears a pending tool when an overlong line cannot be classified safely', async () => {
		const { source, bytes } = memorySource(`T1\n${'X'.repeat(70_000)}T2\nM6`)
		const result = await buildSparseLineIndex({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 1_024,
		})
		expect(result.tools).toEqual([{ toolNumber: 1, firstLine: 1, references: 1, changes: 0 }])
		expect(result.toolsTruncated).toBe(true)
	})

  it('does not invent tools from expressions, named parameters, or O-word labels', async () => {
    const { source, bytes } = memorySource([
      'O100 IF [#1 LT 7]',
      '#<T12> = 3',
      'o<T44> call',
      'T1 #<M6>',
      'G1X2T5 M6',
    ].join('\n'))
    const result = await buildSparseLineIndex({
      source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
      blockSize: 7,
    })
    expect(result.tools).toEqual([
      { toolNumber: 1, firstLine: 4, references: 1, changes: 0 },
      { toolNumber: 5, firstLine: 5, references: 1, changes: 1 },
    ])
  })

	it('lets the last dynamic T word clear a pending static tool before M6', async () => {
		const { source, bytes } = memorySource([
			'T1',
			'T#2 M6',
			'T1',
			'T[2+3] M6',
			'T1',
			'T2 T#1 M6',
			'T#1 T2 M6',
			'T2 T1.5 M6',
			'X[T99] M6',
		].join('\n'))
		const result = await buildSparseLineIndex({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 3,
		})
		expect(result.tools).toEqual([
			{ toolNumber: 1, firstLine: 1, references: 3, changes: 0 },
			{ toolNumber: 2, firstLine: 6, references: 3, changes: 1 },
		])
	})

	it('applies LinuxCNC whitespace rules before classifying static and dynamic tools', async () => {
		const { source, bytes } = memorySource([
			'T1',
			'T + #2 M6',
			'T 1 2 M6',
			'# <T44> = 9',
			'T1',
			'T\t#\t2 M6',
		].join('\n'))
		const result = await buildSparseLineIndex({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 2,
		})
		expect(result.tools).toEqual([
			{ toolNumber: 1, firstLine: 1, references: 2, changes: 0 },
			{ toolNumber: 12, firstLine: 3, references: 1, changes: 1 },
		])
	})

	it('clears a pending static tool for every supported LinuxCNC function operand', async () => {
		const functionOperands = [
			'ABS[-1]', 'ACOS[0]', 'ASIN[0]', 'ATAN[1]/[2]', 'COS[90]',
			'EXISTS[#<_tool>]', 'EXP[1]', 'FIX[1.5]', 'FUP[1.5]', 'LN[1]',
			'ROUND[1.5]', 'SIN[90]', 'SQRT[4]', 'TAN[45]',
		]
		const lines = functionOperands.flatMap((operand, index) => [
			`T${index + 1}`,
			`T ${operand} M6`,
		])
		const { source, bytes } = memorySource(lines.join('\n'))
		const result = await buildSparseLineIndex({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 1,
		})
		expect(result.tools).toEqual(functionOperands.map((_, index) => ({
			toolNumber: index + 1,
			firstLine: index * 2 + 1,
			references: 1,
			changes: 0,
		})))
	})

  it('rejects invalid UTF-8 while building the shared index and Tool Table', async () => {
    const bytes = Uint8Array.from([0x54, 0x31, 0x0a, 0xff])
    await expect(buildSparseLineIndex({
      source: { read: (start, end) => Promise.resolve(bytes.slice(start, end + 1)) },
      byteSize: bytes.length, version: 'v', signal: new AbortController().signal, blockSize: 2,
    })).rejects.toThrow('UNSUPPORTED_ENCODING')
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

	it('keeps case-insensitive match offsets aligned after Unicode characters', async () => {
		const { source, bytes } = memorySource('İİX\nİx\nX')
		const result = await literalSearch({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 3, query: 'x', caseSensitive: false,
		})
		expect(result.totalMatches).toBe(3)
		expect(Array.from(result.lineNumbers)).toEqual([1, 2, 3])
	})

	it('matches Cyrillic literals case-insensitively across streamed boundaries', async () => {
		const { source, bytes } = memorySource('; ИНСТРУМЕНТ\n; инструмент\n')
		const result = await literalSearch({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 3, query: 'инструмент', caseSensitive: false,
		})
		expect(result.totalMatches).toBe(2)
		expect(Array.from(result.lineNumbers)).toEqual([1, 2])
	})

	it('finds a folded literal spanning a streamed newline boundary without shifting its line', async () => {
		const { source, bytes } = memorySource('İİX\nAbC\n')
		const result = await literalSearch({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 2, query: 'x\na', caseSensitive: false,
		})
		expect(result.totalMatches).toBe(1)
		expect(Array.from(result.lineNumbers)).toEqual([1])
	})

	it('treats regular-expression metacharacters literally and retains overlaps', async () => {
		const { source, bytes } = memorySource('A+A+A\n.*[x]+?\\\n.*[x]+?\\')
		const overlaps = await literalSearch({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 2, query: 'a+a', caseSensitive: false,
		})
		expect(overlaps.totalMatches).toBe(2)
		expect(Array.from(overlaps.lineNumbers)).toEqual([1, 1])

		const metacharacters = await literalSearch({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 2, query: '.*[x]+?\\', caseSensitive: true,
		})
		expect(metacharacters.totalMatches).toBe(2)
		expect(Array.from(metacharacters.lineNumbers)).toEqual([2, 3])
	})

	it('rejects an empty literal before constructing the matcher', async () => {
		const { source, bytes } = memorySource('')
		await expect(literalSearch({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 2, query: '', caseSensitive: false,
		})).rejects.toThrow('EMPTY_QUERY')
	})

	it('keeps adversarial repeated-prefix search within the KMP comparison bound', async () => {
		const text = 'a'.repeat(1 << 20)
		const query = `${'a'.repeat(1_023)}b`
		const { source, bytes } = memorySource(text)
		let comparisons = -1
		let scannedCodePoints = -1
		const result = await literalSearch({
			source, byteSize: bytes.length, version: 'v', signal: new AbortController().signal,
			blockSize: 4_093, query, caseSensitive: true,
			onDiagnostics: (diagnostics) => {
				comparisons = diagnostics.comparisons
				scannedCodePoints = diagnostics.scannedCodePoints
			},
		})
		expect(result.totalMatches).toBe(0)
		expect(scannedCodePoints).toBe(text.length)
		expect(comparisons).toBeLessThan(3 * (text.length + query.length))
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
