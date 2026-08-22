import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
	__gCodeCacheAuthGenerationRecordCountForTests,
	__resetGCodeCacheForTests,
  allowGCodeCacheScope,
	  blockGCodeCacheScope,
	  captureDurableGCodeCacheAuthGeneration,
	  captureGCodeCacheAuthGeneration,
  clearGCodeCacheScope,
  createCachedRangeSource,
  createUploadedFileRangeSource,
	  GCODE_CACHE_CHUNK_BYTES,
	  isGCodeCachePersistenceQuarantined,
  MAX_PERSISTENT_GCODE_BYTES,
  readCachedGCodeAnalysis,
  readCachedGCodeRange,
  readLocalGCodeCacheState,
  updateLocalGCodeCacheState,
  writeCachedGCodeAnalysis,
  writeCachedGCodeRange,
  type GCodeCacheIdentity,
} from './gcodeCache'

class MemoryCache {
  readonly values = new Map<string, Response>()

  match(request: RequestInfo | URL): Promise<Response | undefined> {
    const key = request instanceof Request ? request.url : String(request)
    return Promise.resolve(this.values.get(key)?.clone())
  }

  put(request: RequestInfo | URL, response: Response): Promise<void> {
    const key = request instanceof Request ? request.url : String(request)
    this.values.set(key, response.clone())
    return Promise.resolve()
  }

  keys(): Promise<readonly Request[]> {
    return Promise.resolve([...this.values.keys()].map((url) => new Request(url)))
  }

  delete(request: RequestInfo | URL): Promise<boolean> {
    const key = request instanceof Request ? request.url : String(request)
    return Promise.resolve(this.values.delete(key))
  }
}

const identity: GCodeCacheIdentity = {
  scope: 'user:operator:library-1',
  artifactId: 'artifact-1',
  version: 'version-1',
  byteSize: GCODE_CACHE_CHUNK_BYTES + 3,
}

function authOrdinalForTest(generation: string): number {
	const match = /^g([1-9]\d*):/.exec(generation)
	if (!match) throw new Error(`invalid test auth generation: ${generation}`)
	return Number(match[1])
}

let memoryCache: MemoryCache
let originalLocks: PropertyDescriptor | undefined

function installSerialWebLocks(): void {
	let tail = Promise.resolve()
	Object.defineProperty(navigator, 'locks', {
		configurable: true,
		value: {
			request: vi.fn(async (_name: string, _options: object, operation: () => Promise<unknown>) => {
				let release!: () => void
				const previous = tail
				tail = new Promise<void>((resolve) => { release = resolve })
				await previous
				try {
					return await operation()
				} finally {
					release()
				}
			}),
		},
	})
}

beforeEach(async () => {
	originalLocks = Object.getOwnPropertyDescriptor(navigator, 'locks')
	installSerialWebLocks()
	window.localStorage.clear()
	__resetGCodeCacheForTests()
  memoryCache = new MemoryCache()
	vi.stubGlobal('caches', {
		open: vi.fn().mockImplementation(() => Promise.resolve(memoryCache)),
		delete: vi.fn().mockImplementation(() => {
			memoryCache = new MemoryCache()
			return Promise.resolve(true)
		}),
	})
	expect(await allowGCodeCacheScope(identity.scope)).toBe(true)
	expect(await allowGCodeCacheScope('user:other:library-1')).toBe(true)
	expect(isGCodeCachePersistenceQuarantined(identity.scope)).toBe(false)
})

afterEach(() => {
	vi.unstubAllGlobals()
	if (originalLocks) Object.defineProperty(navigator, 'locks', originalLocks)
	else Reflect.deleteProperty(navigator, 'locks')
})

describe('persistent G-code cache', () => {
	it('releases same-realm auth generation records across repeated login cycles', async () => {
		for (let cycle = 0; cycle < 4; cycle += 1) {
			const token = await blockGCodeCacheScope(identity.scope)
			expect(token).toBeDefined()
			expect(__gCodeCacheAuthGenerationRecordCountForTests()).toBe(1)
			expect(await allowGCodeCacheScope(identity.scope, token, captureGCodeCacheAuthGeneration())).toBe(true)
			expect(__gCodeCacheAuthGenerationRecordCountForTests()).toBe(0)
		}
	})

  it('stores only complete aligned chunks and reads a range across them', async () => {
    const first = new Uint8Array(GCODE_CACHE_CHUNK_BYTES).fill(0x41)
    const second = Uint8Array.from([0x42, 0x43, 0x44])
    expect(await writeCachedGCodeRange(identity, 0, first)).toBe(true)
    expect(await writeCachedGCodeRange(identity, GCODE_CACHE_CHUNK_BYTES, second)).toBe(true)
    expect(await writeCachedGCodeRange(identity, 7, Uint8Array.from([1]))).toBe(false)

    const restored = await readCachedGCodeRange(identity, GCODE_CACHE_CHUNK_BYTES - 2, GCODE_CACHE_CHUNK_BYTES + 1)
    expect(Array.from(restored ?? [])).toEqual([0x41, 0x41, 0x42, 0x43])
  })

	it('separates principals and versions without serving either identity the other bytes', async () => {
    const small = { ...identity, byteSize: 3 }
    const newer = { ...small, version: 'version-2' }
    const anotherUser = { ...small, scope: 'user:other:library-1' }
    await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))
    await writeCachedGCodeRange(newer, 0, Uint8Array.from([4, 5, 6]))
    await writeCachedGCodeRange(anotherUser, 0, Uint8Array.from([7, 8, 9]))

		expect(Array.from((await readCachedGCodeRange(small, 0, 2)) ?? [])).toEqual([1, 2, 3])
    expect(Array.from((await readCachedGCodeRange(newer, 0, 2)) ?? [])).toEqual([4, 5, 6])
    expect(Array.from((await readCachedGCodeRange(anotherUser, 0, 2)) ?? [])).toEqual([7, 8, 9])
  })

  it('validates online before a warm hit and marks an explicit offline fallback', async () => {
    const small = { ...identity, byteSize: 3 }
    await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))
    const fetchMock = vi.fn().mockResolvedValueOnce(new Response(Uint8Array.from([4, 5, 6]), {
      status: 206,
      headers: { etag: '"version-1"', 'content-range': 'bytes 0-2/3' },
    })).mockRejectedValueOnce(new TypeError('offline'))
    vi.stubGlobal('fetch', fetchMock)
    const offline = vi.fn()
    const source = createCachedRangeSource(small, '/content', { networkFirst: true, onOfflineFallback: offline })

    expect(Array.from(await source.read(0, 2, 'version-1', new AbortController().signal))).toEqual([4, 5, 6])
    expect(offline).not.toHaveBeenCalled()
    expect(Array.from(await source.read(0, 2, 'version-1', new AbortController().signal))).toEqual([4, 5, 6])
    expect(offline).toHaveBeenCalledTimes(1)
  })

	it('accepts an offline version only when every exact raw chunk is present', async () => {
		const first = new Uint8Array(GCODE_CACHE_CHUNK_BYTES).fill(1)
		const tail = Uint8Array.from([2, 3, 4])
		expect(await writeCachedGCodeRange(identity, 0, first)).toBe(true)
		vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('offline')))
		const source = createCachedRangeSource(identity, '/content', { networkFirst: true })
		await expect(source.read(0, 2, identity.version, new AbortController().signal))
			.rejects.toThrow('OFFLINE_COPY_INCOMPLETE')

		expect(await writeCachedGCodeRange(identity, GCODE_CACHE_CHUNK_BYTES, tail)).toBe(true)
		expect(Array.from(await source.read(0, 2, identity.version, new AbortController().signal))).toEqual([1, 1, 1])
	})

  it('indexes an upload from its immutable File and persists complete chunks without downloading it again', async () => {
    const small = { ...identity, byteSize: 3 }
    const file = new File([Uint8Array.from([0x54, 0x31, 0x0a])], 'part.ngc')
    const cached = vi.fn()
    const source = createUploadedFileRangeSource(small, file, { onCacheProgress: cached })
    expect(Array.from(await source.read(0, 2, small.version, new AbortController().signal))).toEqual([0x54, 0x31, 0x0a])
    expect(cached).toHaveBeenCalledWith(3)
    expect(Array.from((await readCachedGCodeRange(small, 0, 2)) ?? [])).toEqual([0x54, 0x31, 0x0a])
  })

  it('rejects ignored Range responses instead of buffering an unrestricted 200 response', async () => {
    const small = { ...identity, byteSize: 3 }
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(Uint8Array.from([1, 2, 3]), {
      status: 200, headers: { etag: '"version-1"' },
    })))
    await expect(createCachedRangeSource(small, '/content', { networkFirst: true })
      .read(0, 2, 'version-1', new AbortController().signal)).rejects.toThrow('RANGE_FAILED')
  })

	it('falls through to a verified Range when Cache Storage probing fails', async () => {
		const small = { ...identity, byteSize: 3 }
		memoryCache.match = () => Promise.reject(new DOMException('cache disabled', 'InvalidStateError'))
		const fetchMock = vi.fn().mockResolvedValue(new Response(Uint8Array.from([7, 8, 9]), {
			status: 206,
			headers: { etag: '"version-1"', 'content-range': 'bytes 0-2/3' },
		}))
		vi.stubGlobal('fetch', fetchMock)
		const source = createCachedRangeSource(small, '/content')
		expect(Array.from(await source.read(0, 2, small.version, new AbortController().signal))).toEqual([7, 8, 9])
		expect(fetchMock).toHaveBeenCalledTimes(1)
	})

  it('persists bounded progress and tools in localStorage and clears one principal', async () => {
    const small = { ...identity, byteSize: 3 }
	    await updateLocalGCodeCacheState(small, {
      indexedBytes: 3,
      cachedBytes: 3,
      lineCount: 2,
      tools: [{ toolNumber: 7, firstLine: 1, references: 1, changes: 1 }],
      toolsTruncated: false,
    })
    expect(readLocalGCodeCacheState(small)).toMatchObject({ indexedBytes: 3, lineCount: 2 })
    window.localStorage.setItem('unrelated', 'keep')
    await clearGCodeCacheScope(small.scope)
    expect(readLocalGCodeCacheState(small)).toBeUndefined()
    expect(window.localStorage.getItem('unrelated')).toBe('keep')
			await updateLocalGCodeCacheState(small, { indexedBytes: 3, analysisComplete: true })
		await allowGCodeCacheScope(small.scope)
		expect(readLocalGCodeCacheState(small)).toBeUndefined()
  })

	it('does not regress terminal analysis when a delayed partial progress update arrives', async () => {
		const small = { ...identity, byteSize: 3 }
		await updateLocalGCodeCacheState(small, {
			indexedBytes: 3,
			cachedBytes: 3,
			analysisComplete: true,
			lineCount: 2,
			tools: [{ toolNumber: 7, firstLine: 1, references: 2, changes: 1 }],
			toolsTruncated: false,
		})
		await updateLocalGCodeCacheState(small, {
			indexedBytes: 1,
			cachedBytes: 1,
			analysisComplete: false,
			lineCount: 1,
			tools: [],
			toolsTruncated: true,
		})
		expect(readLocalGCodeCacheState(small)).toMatchObject({
			indexedBytes: 3,
			cachedBytes: 3,
			analysisComplete: true,
			lineCount: 2,
			tools: [{ toolNumber: 7, firstLine: 1, references: 2, changes: 1 }],
			toolsTruncated: false,
		})
	})

  it('does not persist raw content for oversized sparse files', async () => {
    const huge = { ...identity, byteSize: MAX_PERSISTENT_GCODE_BYTES + 1 }
    expect(await writeCachedGCodeRange(huge, 0, new Uint8Array(GCODE_CACHE_CHUNK_BYTES))).toBe(false)
    expect([...memoryCache.values.keys()].some((url) => url.includes('kind=chunk'))).toBe(false)
  })

  it('round-trips version-bound analysis and rejects mismatched cached metadata', async () => {
    const small = { ...identity, byteSize: 3 }
    const analysis = {
      lineCount: 2,
      entries: [{ line: 1, byteOffset: 0 }],
      tools: [{ toolNumber: 1, firstLine: 1, references: 1, changes: 1 }],
      toolsTruncated: false,
    }
    expect(await writeCachedGCodeAnalysis(small, analysis)).toBe(true)
    expect(await readCachedGCodeAnalysis(small)).toEqual(analysis)
    expect(await readCachedGCodeAnalysis({ ...small, version: 'other' })).toBeUndefined()
  })

  it('rejects structurally poisoned sparse analysis without an unlocked delete', async () => {
    const small = { ...identity, byteSize: 3 }
    await writeCachedGCodeAnalysis(small, {
      lineCount: 2, entries: [{ line: 1, byteOffset: 0 }], tools: [], toolsTruncated: false,
    })
    const key = [...memoryCache.values.keys()].find((url) => url.includes('kind=analysis'))!
    memoryCache.values.set(key, new Response(JSON.stringify({
      schema: 1,
      version: small.version,
      byteSize: small.byteSize,
      lineCount: 2,
      entries: [{ line: 2, byteOffset: 1 }],
      tools: [{ toolNumber: 9, firstLine: 3, references: 1, changes: 0 }],
      toolsTruncated: false,
    }), { headers: { 'X-WSM-Cached-At': String(Date.now()) } }))
    expect(await readCachedGCodeAnalysis(small)).toBeUndefined()
    expect(memoryCache.values.has(key)).toBe(true)
  })

	it('rejects null and physically impossible cached line indexes without an unlocked delete', async () => {
		const small = { ...identity, byteSize: 100 }
		const valid = {
			lineCount: 2, entries: [{ line: 1, byteOffset: 0 }], tools: [], toolsTruncated: false,
		}
		for (const poison of [
			{ ...valid, entries: [null] },
			{ ...valid, lineCount: Number.MAX_SAFE_INTEGER },
			{ ...valid, lineCount: 100, entries: [{ line: 1, byteOffset: 0 }, { line: 100, byteOffset: 1 }] },
		]) {
			expect(await writeCachedGCodeAnalysis(small, valid)).toBe(true)
			const key = [...memoryCache.values.keys()].find((url) => url.includes('kind=analysis'))!
			memoryCache.values.set(key, new Response(JSON.stringify({
				schema: 1, version: small.version, byteSize: small.byteSize, ...poison,
			}), { headers: { 'X-WSM-Cached-At': String(Date.now()) } }))
			expect(await readCachedGCodeAnalysis(small)).toBeUndefined()
			expect(memoryCache.values.has(key)).toBe(true)
		}
	})

	it('rejects expired raw chunks without an unlocked delete', async () => {
		const small = { ...identity, byteSize: 3 }
		expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))).toBe(true)
		const key = [...memoryCache.values.keys()].find((url) => url.includes('kind=chunk'))!
		memoryCache.values.set(key, new Response(Uint8Array.from([1, 2, 3]), { headers: {
			'X-WSM-Artifact-Version': small.version,
			'X-WSM-Byte-Start': '0',
			'X-WSM-Byte-Length': '3',
			'X-WSM-Cached-At': String(Date.now() - 31 * 24 * 60 * 60 * 1_000),
		} }))
		expect(await readCachedGCodeRange(small, 0, 2)).toBeUndefined()
		expect(memoryCache.values.has(key)).toBe(true)
	})

  it('uses a scope tombstone to remove a write that finishes after logout starts', async () => {
    const small = { ...identity, byteSize: 3 }
    const originalPut = memoryCache.put.bind(memoryCache)
    let release!: () => void
    let started!: () => void
    const gate = new Promise<void>((resolve) => { release = resolve })
    const putStarted = new Promise<void>((resolve) => { started = resolve })
    memoryCache.put = async (request, response) => {
      started()
      await gate
      await originalPut(request, response)
    }
    const writing = writeCachedGCodeAnalysis(small, {
      lineCount: 1,
      entries: [{ line: 1, byteOffset: 0 }],
      tools: [],
      toolsTruncated: false,
	    })
	    await putStarted
	    const blocking = blockGCodeCacheScope(small.scope)
	    release()
	    expect(await writing).toBe(false)
			const token = await blocking
			expect(await clearGCodeCacheScope(small.scope, token)).toBe(true)
			expect(await allowGCodeCacheScope(small.scope, token)).toBe(true)
		expect([...memoryCache.values.keys()].some((url) => url.includes(`scope=${encodeURIComponent(small.scope)}`))).toBe(false)
  })

	it('rejects a delayed cached read from an old epoch even after the same scope is allowed again', async () => {
		const small = { ...identity, byteSize: 3 }
		expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))).toBe(true)
		const originalMatch = memoryCache.match.bind(memoryCache)
		let release!: () => void
		let started!: () => void
		const gate = new Promise<void>((resolve) => { release = resolve })
		const matchStarted = new Promise<void>((resolve) => { started = resolve })
		memoryCache.match = async (request) => {
			const url = request instanceof Request ? request.url : String(request)
			if (url.includes('kind=chunk')) {
				started()
				await gate
			}
			return originalMatch(request)
		}
		const source = createCachedRangeSource(small, '/content')
		const reading = source.read(0, 2, small.version, new AbortController().signal)
		await matchStarted
		const blocking = blockGCodeCacheScope(small.scope)
		release()
		await expect(reading).rejects.toThrow('CACHE_SCOPE_BLOCKED')
		const token = await blocking
		expect(await allowGCodeCacheScope(small.scope, token)).toBe(true)
	})

	it('serializes a cached read with a same-key replacement and preserves the replacement', async () => {
		const small = { ...identity, byteSize: 3 }
		expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))).toBe(true)
		const originalMatch = memoryCache.match.bind(memoryCache)
		let release!: () => void
		let started!: () => void
		const gate = new Promise<void>((resolve) => { release = resolve })
		const matchStarted = new Promise<void>((resolve) => { started = resolve })
		let gated = false
		memoryCache.match = async (request) => {
			const response = await originalMatch(request)
			const url = request instanceof Request ? request.url : String(request)
			if (!gated && url.includes('kind=chunk')) {
				gated = true
				started()
				await gate
			}
			return response
		}
		const oldRead = readCachedGCodeRange(small, 0, 2)
		await matchStarted
		const replacement = writeCachedGCodeRange(small, 0, Uint8Array.from([7, 8, 9]))
		release()
		expect(Array.from((await oldRead)!)).toEqual([1, 2, 3])
		expect(await replacement).toBe(true)
		memoryCache.match = originalMatch
		expect(Array.from((await readCachedGCodeRange(small, 0, 2))!)).toEqual([7, 8, 9])
	})

	it('serializes concurrent raw budget reservations before evicting and publishing chunks', async () => {
		const oldest = { ...identity, artifactId: 'oldest', version: 'oldest', byteSize: 1 }
		const newest = { ...identity, artifactId: 'newest', version: 'newest', byteSize: 1 }
		expect(await writeCachedGCodeRange(oldest, 0, Uint8Array.of(1))).toBe(true)
		expect(await writeCachedGCodeRange(newest, 0, Uint8Array.of(2))).toBe(true)
		const declaredSizes = [GCODE_CACHE_CHUNK_BYTES, 127 * GCODE_CACHE_CHUNK_BYTES]
		let position = 0
		for (const [key, response] of memoryCache.values) {
			if (!key.includes('kind=chunk')) continue
			const headers = new Headers(response.headers)
			headers.set('X-WSM-Byte-Length', String(declaredSizes[position]))
			headers.set('X-WSM-Cached-At', String(Date.now() - (2 - position) * 1_000))
			memoryCache.values.set(key, new Response(Uint8Array.of(position + 1), { headers }))
			position += 1
		}
		const originalKeys = memoryCache.keys.bind(memoryCache)
		let activeSnapshots = 0
		let maximumConcurrentSnapshots = 0
		memoryCache.keys = async () => {
			activeSnapshots += 1
			maximumConcurrentSnapshots = Math.max(maximumConcurrentSnapshots, activeSnapshots)
			await Promise.resolve()
			const keys = await originalKeys()
			activeSnapshots -= 1
			return keys
		}
		const first = { ...identity, artifactId: 'concurrent-1', version: 'concurrent-1', byteSize: GCODE_CACHE_CHUNK_BYTES }
		const second = { ...identity, artifactId: 'concurrent-2', version: 'concurrent-2', byteSize: GCODE_CACHE_CHUNK_BYTES }
		const chunk = new Uint8Array(GCODE_CACHE_CHUNK_BYTES)
		expect(await Promise.all([
			writeCachedGCodeRange(first, 0, chunk),
			writeCachedGCodeRange(second, 0, chunk),
		])).toEqual([true, true])
		const total = [...memoryCache.values.entries()]
			.filter(([url]) => url.includes('kind=chunk'))
			.reduce((sum, [, response]) => sum + Number(response.headers.get('X-WSM-Byte-Length')), 0)
		expect(total).toBeLessThanOrEqual(128 * GCODE_CACHE_CHUNK_BYTES)
		expect(maximumConcurrentSnapshots).toBe(1)
	}, 15_000)

	it('bounds tiny raw cache versions by charging a minimum group cost', async () => {
		for (let index = 0; index < 129; index += 1) {
			const tiny = { ...identity, artifactId: `tiny-${index}`, version: `tiny-${index}`, byteSize: 1 }
			expect(await writeCachedGCodeRange(tiny, 0, Uint8Array.of(index))).toBe(true)
		}
		const chunkURLs = [...memoryCache.values.keys()].filter((url) => url.includes('kind=chunk'))
		expect(chunkURLs).toHaveLength(128)
		expect(chunkURLs.some((url) => url.includes('artifact=tiny-0'))).toBe(false)
		expect(chunkURLs.some((url) => url.includes('artifact=tiny-128'))).toBe(true)
	}, 15_000)

	it('resumes an interrupted durable logout cleanup before allowing the same scope', async () => {
		const small = { ...identity, byteSize: 3 }
		expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))).toBe(true)
			await updateLocalGCodeCacheState(small, { indexedBytes: 3, analysisComplete: true })
		const chunkKey = [...memoryCache.values.keys()].find((url) => url.includes('kind=chunk'))!
		const staleChunk = memoryCache.values.get(chunkKey)!.clone()
		const staleManifest = window.localStorage.getItem('web-setup-manager.gcode-cache.v3')!
		await clearGCodeCacheScope(small.scope)
		// Simulate a browser crash/reload after the durable marker was written but
		// before an old context's storage became fully quiescent.
		memoryCache.values.set(chunkKey, staleChunk)
		window.localStorage.setItem('web-setup-manager.gcode-cache.v3', staleManifest)
		await allowGCodeCacheScope(small.scope)
		expect(await readCachedGCodeRange(small, 0, 2)).toBeUndefined()
		expect(readLocalGCodeCacheState(small)).toBeUndefined()
			expect([...memoryCache.values.keys()].some((url) => url.includes(`scope=${encodeURIComponent(small.scope)}`))).toBe(false)
		})

		it('serializes a derived manifest update behind logout cleanup', async () => {
			const small = { ...identity, byteSize: 3 }
			await updateLocalGCodeCacheState(small, { indexedBytes: 1, analysisComplete: false })
			const originalKeys = memoryCache.keys.bind(memoryCache)
			let release!: () => void
			let started!: () => void
			const gate = new Promise<void>((resolve) => { release = resolve })
			const keysStarted = new Promise<void>((resolve) => { started = resolve })
			let gated = false
			memoryCache.keys = async () => {
				if (!gated) {
					gated = true
					started()
					await gate
				}
				return originalKeys()
			}

			const cleanup = clearGCodeCacheScope(small.scope)
			await keysStarted
			const delayedUpdate = updateLocalGCodeCacheState(small, { indexedBytes: 3, analysisComplete: true })
			release()
			expect(await cleanup).toBe(true)
			await delayedUpdate

			const persisted = JSON.parse(window.localStorage.getItem('web-setup-manager.gcode-cache.v3') ?? '{"records":[]}') as { records?: Array<{ scope?: string }> }
			expect(persisted.records?.some((record) => record.scope === small.scope)).toBe(false)
		})

		it('keeps persistence quarantined after a transient purge failure while verified network reads continue', async () => {
			const small = { ...identity, byteSize: 3 }
			expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))).toBe(true)
			await updateLocalGCodeCacheState(small, { indexedBytes: 3, analysisComplete: true })
			const originalKeys = memoryCache.keys.bind(memoryCache)
			memoryCache.keys = () => Promise.reject(new DOMException('storage temporarily unavailable', 'InvalidStateError'))

			expect(await clearGCodeCacheScope(small.scope)).toBe(false)
			await allowGCodeCacheScope(small.scope)
			expect(isGCodeCachePersistenceQuarantined(small.scope)).toBe(true)
			expect(await readCachedGCodeRange(small, 0, 2)).toBeUndefined()
			expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([9, 9, 9]))).toBe(false)

			const fetchMock = vi.fn().mockResolvedValue(new Response(Uint8Array.from([7, 8, 9]), {
				status: 206,
				headers: { etag: '"version-1"', 'content-range': 'bytes 0-2/3' },
			}))
			vi.stubGlobal('fetch', fetchMock)
			const source = createCachedRangeSource(small, '/content', {
				persistentCacheDisabled: isGCodeCachePersistenceQuarantined(small.scope),
			})
			expect(Array.from(await source.read(0, 2, small.version, new AbortController().signal))).toEqual([7, 8, 9])
			expect(fetchMock).toHaveBeenCalledTimes(1)

			memoryCache.keys = originalKeys
			expect(await allowGCodeCacheScope(small.scope)).toBe(true)
			expect(isGCodeCachePersistenceQuarantined(small.scope)).toBe(false)
			expect(await readCachedGCodeRange(small, 0, 2)).toBeUndefined()
			expect([...memoryCache.values.keys()].some((url) => url.includes(`scope=${encodeURIComponent(small.scope)}`))).toBe(false)
		})

		it('does not let an older cleanup generation erase a newer scope block', async () => {
			const small = { ...identity, byteSize: 3 }
			expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))).toBe(true)
			const firstToken = (await blockGCodeCacheScope(small.scope))!
			const firstCleanup = clearGCodeCacheScope(small.scope, firstToken)
			const blockKey = Array.from({ length: window.localStorage.length }, (_, index) => window.localStorage.key(index))
				.find((key) => key?.startsWith('web-setup-manager.gcode-cache.scope-block.v1.'))!
			window.localStorage.removeItem(blockKey)
			const newerToken = await blockGCodeCacheScope(small.scope)
			expect(newerToken).not.toBe(firstToken)

			expect(await firstCleanup).toBe(false)
			expect(window.localStorage.getItem(blockKey)).toBe(newerToken)
			expect(isGCodeCachePersistenceQuarantined(small.scope)).toBe(true)
			await allowGCodeCacheScope(small.scope)
			expect(isGCodeCachePersistenceQuarantined(small.scope)).toBe(false)
			expect(await readCachedGCodeRange(small, 0, 2)).toBeUndefined()
		})

		it('does not let a delayed allow continuation release a newer logout generation', async () => {
			const oldToken = (await blockGCodeCacheScope(identity.scope))!
			const originalMatch = memoryCache.match.bind(memoryCache)
			let release!: () => void
			let started!: () => void
			const gate = new Promise<void>((resolve) => { release = resolve })
			const matchStarted = new Promise<void>((resolve) => { started = resolve })
			let gated = false
			memoryCache.match = async (request) => {
				const url = request instanceof Request ? request.url : String(request)
				if (!gated && url.includes('kind=scope-block')) {
					gated = true
					started()
					await gate
				}
				return originalMatch(request)
			}

			const staleAllow = allowGCodeCacheScope(identity.scope, oldToken)
			await matchStarted
			const newerToken = (await blockGCodeCacheScope(identity.scope))!
			release()
			await staleAllow
			expect(newerToken).not.toBe(oldToken)
			expect(isGCodeCachePersistenceQuarantined(identity.scope)).toBe(true)
			expect(window.localStorage.getItem(Array.from({ length: window.localStorage.length }, (_, index) => window.localStorage.key(index))
				.find((key) => key?.startsWith('web-setup-manager.gcode-cache.scope-block.v1.'))!)).toBe(newerToken)

			memoryCache.match = originalMatch
			await allowGCodeCacheScope(identity.scope, newerToken)
			expect(isGCodeCachePersistenceQuarantined(identity.scope)).toBe(false)
		})

		it('serializes a newer durable block against the final marker deletion of an older allow', async () => {
			const small = { ...identity, byteSize: 3 }
			const oldToken = (await blockGCodeCacheScope(small.scope))!
			expect(await clearGCodeCacheScope(small.scope, oldToken)).toBe(true)
			const originalDelete = memoryCache.delete.bind(memoryCache)
			let release!: () => void
			let started!: () => void
			const gate = new Promise<void>((resolve) => { release = resolve })
			const deleteStarted = new Promise<void>((resolve) => { started = resolve })
			let gated = false
			memoryCache.delete = async (request) => {
				const url = request instanceof Request ? request.url : String(request)
				if (!gated && url.includes('kind=scope-block')) {
					gated = true
					started()
					await gate
				}
				return originalDelete(request)
			}

			const staleAllow = allowGCodeCacheScope(small.scope, oldToken)
			await deleteStarted
			const newerBlock = blockGCodeCacheScope(small.scope)
			release()
			expect(await staleAllow).toBe(false)
			const newerToken = (await newerBlock)!
			expect(newerToken).not.toBe(oldToken)
			expect(await clearGCodeCacheScope(small.scope, newerToken)).toBe(true)
			expect(isGCodeCachePersistenceQuarantined(small.scope)).toBe(true)
			expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([4, 5, 6]))).toBe(false)
			const marker = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=scope-block'))?.[1]
			expect(marker?.headers.get('X-WSM-Block-Token')).toBe(newerToken)

			memoryCache.delete = originalDelete
			expect(await allowGCodeCacheScope(small.scope, newerToken)).toBe(true)
		})

		it('rejects an auth authority captured before another context rotated the durable logout generation', async () => {
			const staleAuthGeneration = captureGCodeCacheAuthGeneration()
			const token = (await blockGCodeCacheScope(identity.scope))!
			expect(await clearGCodeCacheScope(identity.scope, token)).toBe(true)

			expect(await allowGCodeCacheScope(identity.scope, undefined, staleAuthGeneration)).toBe(false)
			expect(isGCodeCachePersistenceQuarantined(identity.scope)).toBe(true)
			const marker = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=scope-block'))?.[1]
			expect(marker?.headers.get('X-WSM-Block-Token')).toBe(token)
			expect(marker?.headers.get('X-WSM-Auth-Generation')).toBe(captureGCodeCacheAuthGeneration())

			expect(await allowGCodeCacheScope(identity.scope, token, captureGCodeCacheAuthGeneration())).toBe(true)
		})

		it('rejects an observed newer durable generation when its scope marker is unreadable', async () => {
			const staleToken = (await blockGCodeCacheScope(identity.scope))!
			const staleGeneration = captureGCodeCacheAuthGeneration()
			const newerGeneration = `g${authOrdinalForTest(staleGeneration) + 1}:peer-generation`
			const authorityEntry = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=auth-generation'))!
			memoryCache.values.set(authorityEntry[0], new Response(JSON.stringify({
				schema: 1,
				generation: newerGeneration,
				pending: [{ scope: identity.scope, token: 'peer-token', authGeneration: newerGeneration }],
			}), { headers: { 'X-WSM-Auth-Generation': newerGeneration } }))
			const originalMatch = memoryCache.match.bind(memoryCache)
			memoryCache.match = async (request) => {
				const url = request instanceof Request ? request.url : String(request)
				if (url.includes('kind=scope-block')) throw new DOMException('marker temporarily unavailable', 'InvalidStateError')
				return originalMatch(request)
			}
			try {
				expect(await allowGCodeCacheScope(identity.scope, staleToken, staleGeneration)).toBe(false)
				expect(isGCodeCachePersistenceQuarantined(identity.scope)).toBe(true)
			} finally {
				memoryCache.match = originalMatch
			}
		})

		it('recovers the auth authority from Cache Storage when localStorage state is lost', async () => {
			const token = (await blockGCodeCacheScope(identity.scope))!
			expect(await clearGCodeCacheScope(identity.scope, token)).toBe(true)
			const expectedGeneration = captureGCodeCacheAuthGeneration()
			window.localStorage.clear()

			vi.resetModules()
			const fresh = await import('./gcodeCache')
			const recoveredGeneration = await fresh.captureDurableGCodeCacheAuthGeneration()
			expect(recoveredGeneration).toBe(expectedGeneration)
			expect(await fresh.allowGCodeCacheScope(identity.scope, undefined, recoveredGeneration)).toBe(true)
			expect([...memoryCache.values.keys()].some((url) => url.includes(`scope=${encodeURIComponent(identity.scope)}`))).toBe(false)
		})

		it('recovers a global-pending-only crash and fences payload I/O before cleanup', async () => {
			const small = { ...identity, byteSize: 3 }
			expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))).toBe(true)
			expect(await writeCachedGCodeAnalysis(small, {
				lineCount: 1, entries: [{ line: 1, byteOffset: 0 }], tools: [], toolsTruncated: false,
			})).toBe(true)
			await updateLocalGCodeCacheState(small, { indexedBytes: 3, cachedBytes: 3, analysisComplete: true })

			const originalPut = memoryCache.put.bind(memoryCache)
			const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
				throw new DOMException('localStorage disabled', 'SecurityError')
			})
			memoryCache.put = async (request, response) => {
				const url = request instanceof Request ? request.url : String(request)
				if (url.includes('kind=scope-block')) throw new DOMException('marker write failed', 'InvalidStateError')
				await originalPut(request, response)
			}
			const token = await blockGCodeCacheScope(small.scope)
			expect(token).toBeDefined()
			expect([...memoryCache.values.keys()].some((url) => url.includes('kind=scope-block'))).toBe(false)
			const authorityEntry = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=auth-generation'))
			expect(authorityEntry).toBeDefined()
			expect(JSON.parse(await authorityEntry![1].clone().text())).toMatchObject({
				pending: [{ scope: small.scope, token }],
			})

			memoryCache.put = originalPut
			setItem.mockRestore()
			__resetGCodeCacheForTests()
			vi.resetModules()
			const fresh = await import('./gcodeCache')
			const generation = await fresh.captureDurableGCodeCacheAuthGeneration()
			expect(await fresh.ensureGCodeCacheConcurrency()).toBe(true)
			expect(await fresh.readCachedGCodeRange(small, 0, 2)).toBeUndefined()
			expect(await fresh.writeCachedGCodeRange(small, 0, Uint8Array.from([9, 9, 9]))).toBe(false)
			expect(await fresh.allowGCodeCacheScope(small.scope, undefined, generation)).toBe(true)
			expect(await fresh.readCachedGCodeRange(small, 0, 2)).toBeUndefined()
			expect(fresh.readLocalGCodeCacheState(small)).toBeUndefined()
			const finalAuthority = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=auth-generation'))
			expect(JSON.parse(await finalAuthority![1].clone().text())).toMatchObject({ pending: [] })
			expect([...memoryCache.values.keys()].some((url) => url.includes(`scope=${encodeURIComponent(small.scope)}`))).toBe(false)
			fresh.__resetGCodeCacheForTests()
		})

		it('does not accept an unlocked global journal as the only logout seal', async () => {
			const locks = Object.getOwnPropertyDescriptor(navigator, 'locks')
			Reflect.deleteProperty(navigator, 'locks')
			const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
				throw new DOMException('localStorage disabled', 'SecurityError')
			})
			const originalPut = memoryCache.put.bind(memoryCache)
			memoryCache.put = async (request, response) => {
				const url = request instanceof Request ? request.url : String(request)
				if (url.includes('kind=scope-block')) throw new DOMException('marker write failed', 'InvalidStateError')
				await originalPut(request, response)
			}
			try {
				expect(await blockGCodeCacheScope(identity.scope)).toBeUndefined()
				expect([...memoryCache.values.keys()].some((url) => url.includes('kind=scope-block'))).toBe(false)
			} finally {
				memoryCache.put = originalPut
				setItem.mockRestore()
				if (locks) Object.defineProperty(navigator, 'locks', locks)
			}
		})

		it('requires a writable scope marker when Cache Storage is unavailable', async () => {
			const small = { ...identity, byteSize: 3 }
			await updateLocalGCodeCacheState(small, { indexedBytes: 3, cachedBytes: 0, analysisComplete: true })
			const persistedManifest = window.localStorage.getItem('web-setup-manager.gcode-cache.v3')
			expect(persistedManifest).not.toBeNull()
			vi.stubGlobal('caches', undefined)
			const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
				throw new DOMException('localStorage full', 'QuotaExceededError')
			})
			const removeItem = vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => {
				throw new DOMException('localStorage denied', 'SecurityError')
			})
			try {
				expect(await blockGCodeCacheScope(small.scope)).toBeUndefined()
				expect(window.localStorage.getItem('web-setup-manager.gcode-cache.v3')).toBe(persistedManifest)
			} finally {
				setItem.mockRestore()
				removeItem.mockRestore()
			}
		})

		it('chooses a newer pending token over a strictly older scope marker after a crash', async () => {
			const firstToken = (await blockGCodeCacheScope(identity.scope))!
			const originalPut = memoryCache.put.bind(memoryCache)
			memoryCache.put = async (request, response) => {
				const url = request instanceof Request ? request.url : String(request)
				if (url.includes('kind=scope-block')) throw new DOMException('crash before marker', 'InvalidStateError')
				await originalPut(request, response)
			}
			const secondToken = (await blockGCodeCacheScope(identity.scope))!
			expect(secondToken).not.toBe(firstToken)
			const oldMarker = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=scope-block'))![1]
			expect(oldMarker.headers.get('X-WSM-Block-Token')).toBe(firstToken)
			const pendingAuthority = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=auth-generation'))![1]
			expect(JSON.parse(await pendingAuthority.clone().text())).toMatchObject({
				pending: [{ scope: identity.scope, token: secondToken }],
			})

			memoryCache.put = originalPut
			window.localStorage.clear()
			__resetGCodeCacheForTests()
			vi.resetModules()
			const fresh = await import('./gcodeCache')
			const generation = await fresh.captureDurableGCodeCacheAuthGeneration()
			expect(await fresh.allowGCodeCacheScope(identity.scope, undefined, generation)).toBe(true)
			expect([...memoryCache.values.keys()].some((url) => url.includes(`scope=${encodeURIComponent(identity.scope)}`))).toBe(false)
			fresh.__resetGCodeCacheForTests()
		})

		it('adopts durable pending authority when localStorage is readable but unwritable', async () => {
			const token = (await blockGCodeCacheScope(identity.scope))!
			window.localStorage.clear()
			__resetGCodeCacheForTests()
			vi.resetModules()
			const fresh = await import('./gcodeCache')
			const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
				throw new DOMException('quota exhausted', 'QuotaExceededError')
			})
			const generation = await fresh.captureDurableGCodeCacheAuthGeneration()
			expect(await fresh.allowGCodeCacheScope(identity.scope, undefined, generation)).toBe(true)
			expect(fresh.isGCodeCachePersistenceQuarantined(identity.scope)).toBe(true)
			const authority = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=auth-generation'))![1]
			expect(JSON.parse(await authority.clone().text())).toMatchObject({ pending: [{ token }] })

			setItem.mockRestore()
			expect(await fresh.allowGCodeCacheScope(identity.scope, token, generation)).toBe(true)
			expect(fresh.isGCodeCachePersistenceQuarantined(identity.scope)).toBe(false)
			fresh.__resetGCodeCacheForTests()
		})

		it('chooses a newer scope marker and removes a strictly older pending entry', async () => {
			const oldToken = (await blockGCodeCacheScope(identity.scope))!
			const oldGeneration = captureGCodeCacheAuthGeneration()
			const ordinal = authOrdinalForTest(oldGeneration) + 1
			const newGeneration = `g${ordinal}:newer-marker-authority`
			const newToken = 'newer-marker-token'
			const markerEntry = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=scope-block'))!
			const markerHeaders = new Headers(markerEntry[1].headers)
			markerHeaders.set('X-WSM-Block-Token', newToken)
			markerHeaders.set('X-WSM-Auth-Generation', newGeneration)
			memoryCache.values.set(markerEntry[0], new Response(null, { headers: markerHeaders }))
			const authorityEntry = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=auth-generation'))!
			const oldAuthority = JSON.parse(await authorityEntry[1].clone().text()) as { pending: unknown[] }
			memoryCache.values.set(authorityEntry[0], new Response(JSON.stringify({
				schema: 1,
				generation: newGeneration,
				pending: oldAuthority.pending,
			}), { headers: {
				'Content-Type': 'application/json',
				'X-WSM-Auth-Generation': newGeneration,
				'X-WSM-Updated-At': String(Date.now()),
			} }))
			window.localStorage.setItem('web-setup-manager.gcode-cache.auth-generation.v1', newGeneration)

			expect(await allowGCodeCacheScope(identity.scope, undefined, newGeneration)).toBe(true)
			expect(isGCodeCachePersistenceQuarantined(identity.scope)).toBe(false)
			expect([...memoryCache.values.keys()].some((url) => url.includes(`scope=${encodeURIComponent(identity.scope)}`))).toBe(false)
			const finalAuthority = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=auth-generation'))![1]
			expect(JSON.parse(await finalAuthority.clone().text())).toMatchObject({ generation: newGeneration, pending: [] })
			expect(oldToken).not.toBe(newToken)
		})

		it('returns no logout token when every durable sealing mechanism fails', async () => {
			const setItem = vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
				throw new DOMException('storage denied', 'SecurityError')
			})
			const originalPut = memoryCache.put.bind(memoryCache)
			memoryCache.put = () => Promise.reject(new DOMException('cache denied', 'InvalidStateError'))
			expect(await blockGCodeCacheScope(identity.scope)).toBeUndefined()
			expect(isGCodeCachePersistenceQuarantined(identity.scope)).toBe(true)
			memoryCache.put = originalPut
			setItem.mockRestore()
		})

		it('keeps equal-generation different-token authority fail-closed without oscillation', async () => {
			const token = (await blockGCodeCacheScope(identity.scope))!
			expect(await clearGCodeCacheScope(identity.scope, token)).toBe(true)
			const generation = captureGCodeCacheAuthGeneration()
			const markerEntry = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=scope-block'))!
			const headers = new Headers(markerEntry[1].headers)
			headers.set('X-WSM-Block-Token', 'conflicting-token')
			memoryCache.values.set(markerEntry[0], new Response(null, { headers }))

			expect(await allowGCodeCacheScope(identity.scope, undefined, generation)).toBe(false)
			expect(await allowGCodeCacheScope(identity.scope, undefined, generation)).toBe(false)
			expect(memoryCache.values.get(markerEntry[0])?.headers.get('X-WSM-Block-Token')).toBe('conflicting-token')
			const authority = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=auth-generation'))![1]
			expect(JSON.parse(await authority.clone().text())).toMatchObject({ pending: [{ token }] })
			expect(isGCodeCachePersistenceQuarantined(identity.scope)).toBe(true)
		})

		it('never exposes a repaired generation when the global authority write is unconfirmed', async () => {
			const token = (await blockGCodeCacheScope(identity.scope))!
			expect(await clearGCodeCacheScope(identity.scope, token)).toBe(true)
			const generation = captureGCodeCacheAuthGeneration()
			const markerEntry = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=scope-block'))!
			const headers = new Headers(markerEntry[1].headers)
			const ordinal = /^g([1-9]\d*):/.exec(generation)![1]
			headers.set('X-WSM-Auth-Generation', `g${ordinal}:ambiguous-peer`)
			memoryCache.values.set(markerEntry[0], new Response(null, { headers }))
			const originalPut = memoryCache.put.bind(memoryCache)
			memoryCache.put = async (request, response) => {
				const url = request instanceof Request ? request.url : String(request)
				if (url.includes('kind=auth-generation')) throw new DOMException('authority unavailable', 'InvalidStateError')
				await originalPut(request, response)
			}

			const unconfirmed = await captureDurableGCodeCacheAuthGeneration()
			expect(unconfirmed).toBe(generation)
			expect(await allowGCodeCacheScope(identity.scope, token, unconfirmed)).toBe(false)
			expect(memoryCache.values.has(markerEntry[0])).toBe(true)
			memoryCache.put = originalPut
			const recovered = await captureDurableGCodeCacheAuthGeneration()
			expect(recovered).not.toBe(generation)
			expect(await allowGCodeCacheScope(identity.scope, token, recovered)).toBe(true)
		})

		it('allows recovery of an older scope marker under the latest global auth generation', async () => {
			const oldToken = (await blockGCodeCacheScope(identity.scope))!
			expect(await clearGCodeCacheScope(identity.scope, oldToken)).toBe(true)
			const otherScope = 'user:other:library-1'
			const latestToken = (await blockGCodeCacheScope(otherScope))!
			expect(await clearGCodeCacheScope(otherScope, latestToken)).toBe(true)
			const latestGeneration = captureGCodeCacheAuthGeneration()

			expect(await allowGCodeCacheScope(identity.scope, oldToken, latestGeneration)).toBe(true)
			expect(await allowGCodeCacheScope(otherScope, latestToken, latestGeneration)).toBe(true)
		})

		it('treats equal auth ordinals with different nonces as ambiguous and fail-closed', async () => {
			const token = (await blockGCodeCacheScope(identity.scope))!
			expect(await clearGCodeCacheScope(identity.scope, token)).toBe(true)
			const expectedGeneration = captureGCodeCacheAuthGeneration()
			const markerEntry = [...memoryCache.values.entries()].find(([url]) => url.includes('kind=scope-block'))!
			const ordinal = /^g([1-9]\d*):/.exec(expectedGeneration)?.[1]
			expect(ordinal).toBeDefined()
			const headers = new Headers(markerEntry[1].headers)
			headers.set('X-WSM-Auth-Generation', `g${ordinal}:ambiguous-peer`)
			memoryCache.values.set(markerEntry[0], new Response(null, { headers }))

			expect(await allowGCodeCacheScope(identity.scope, token, expectedGeneration)).toBe(false)
			expect(memoryCache.values.get(markerEntry[0])?.headers.get('X-WSM-Block-Token')).toBe(token)
			const recoveredGeneration = await captureDurableGCodeCacheAuthGeneration()
			expect(recoveredGeneration).not.toBe(expectedGeneration)
			expect(await allowGCodeCacheScope(identity.scope, token, recoveredGeneration)).toBe(true)
		})

		it('purges only the logged-out scope when Web Locks are unavailable and retains every tombstone', async () => {
			const small = { ...identity, byteSize: 3 }
			const other = { ...small, scope: 'user:other:library-1', artifactId: 'other-artifact' }
			expect(await writeCachedGCodeRange(small, 0, Uint8Array.from([1, 2, 3]))).toBe(true)
			expect(await writeCachedGCodeRange(other, 0, Uint8Array.from([4, 5, 6]))).toBe(true)
			const otherToken = (await blockGCodeCacheScope(other.scope))!
			const locks = Object.getOwnPropertyDescriptor(navigator, 'locks')
			Reflect.deleteProperty(navigator, 'locks')
			const token = (await blockGCodeCacheScope(small.scope))!
			try {
				expect(await clearGCodeCacheScope(small.scope, token)).toBe(false)
				const entries = [...memoryCache.values.keys()]
				expect(entries.some((url) => url.includes(`scope=${encodeURIComponent(small.scope)}`) && url.includes('kind=chunk'))).toBe(false)
				expect(entries.some((url) => url.includes(`scope=${encodeURIComponent(small.scope)}`) && url.includes('kind=scope-block'))).toBe(true)
				expect(entries.some((url) => url.includes(`scope=${encodeURIComponent(other.scope)}`) && url.includes('kind=chunk'))).toBe(true)
				expect(entries.some((url) => url.includes(`scope=${encodeURIComponent(other.scope)}`) && url.includes('kind=scope-block'))).toBe(true)
				expect(await allowGCodeCacheScope(small.scope, token, captureGCodeCacheAuthGeneration())).toBe(true)
				expect(isGCodeCachePersistenceQuarantined(small.scope)).toBe(true)
			} finally {
				if (locks) Object.defineProperty(navigator, 'locks', locks)
			}
			expect(await allowGCodeCacheScope(small.scope, token, captureGCodeCacheAuthGeneration())).toBe(true)
			expect(await allowGCodeCacheScope(other.scope, otherToken, captureGCodeCacheAuthGeneration())).toBe(true)
		})

		it('persists a conservative quarantine when the exposed Web Lock API rejects acquisition', async () => {
			const originalLocks = Object.getOwnPropertyDescriptor(navigator, 'locks')
			Object.defineProperty(navigator, 'locks', {
				configurable: true,
				value: { request: vi.fn().mockRejectedValue(new DOMException('locks disabled', 'NotSupportedError')) },
			})
			try {
				const token = (await blockGCodeCacheScope(identity.scope))!
				const blockKey = Array.from({ length: window.localStorage.length }, (_, index) => window.localStorage.key(index))
					.find((key) => key?.startsWith('web-setup-manager.gcode-cache.scope-block.v1.'))!
				expect(window.localStorage.getItem(blockKey)).toBe(token)
				expect(captureGCodeCacheAuthGeneration()).not.toBe('initial')
				expect(await clearGCodeCacheScope(identity.scope, token)).toBe(false)
				expect(isGCodeCachePersistenceQuarantined(identity.scope)).toBe(true)
				expect(await writeCachedGCodeRange({ ...identity, byteSize: 3 }, 0, Uint8Array.from([1, 2, 3]))).toBe(false)
			} finally {
				if (originalLocks) Object.defineProperty(navigator, 'locks', originalLocks)
				else Reflect.deleteProperty(navigator, 'locks')
			}
			expect(await allowGCodeCacheScope(identity.scope, undefined, captureGCodeCacheAuthGeneration())).toBe(true)
		})
	})
