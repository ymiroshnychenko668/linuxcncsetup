/// <reference lib="webworker" />

import {
	createCachedRangeSource,
	createUploadedFileRangeSource,
	ensureGCodeCacheConcurrency,
	GCODE_CACHE_CHUNK_BYTES,
	hasCompleteCachedGCodeCopy,
	MAX_PERSISTENT_GCODE_BYTES,
	readCachedGCodeAnalysis,
	writeCachedGCodeAnalysis,
	type GCodeCacheIdentity,
} from '../gcodeCache'
import { buildSparseLineIndex, literalSearch } from './gcodeCore'
import { classifyCachedAnalysisRefillError, resolveWorkerPersistentCacheDisabled } from './gcodeWorkerPolicy'

type WorkerRequest =
  | { type: 'index'; requestId: string; cacheScope: string; artifactId: string; url: string; version: string; byteSize: number; file?: File; cacheDisabled?: boolean }
  | { type: 'search'; requestId: string; cacheScope: string; artifactId: string; url: string; version: string; byteSize: number; file?: File; cacheDisabled?: boolean; query: string; caseSensitive: boolean; matchOffset?: number }
  | { type: 'cancel'; requestId: string }

const active = new Map<string, AbortController>()

const worker = self as unknown as DedicatedWorkerGlobalScope

worker.addEventListener('message', (event: MessageEvent<WorkerRequest>) => {
  const request = event.data
  if (request.type === 'cancel') {
    active.get(request.requestId)?.abort()
    active.delete(request.requestId)
    return
  }
  active.get(request.requestId)?.abort()
  const controller = new AbortController()
  active.set(request.requestId, controller)
	let completedBytes = 0
	let cachedBytes = 0
	const identity: GCodeCacheIdentity = {
		scope: request.cacheScope,
		artifactId: request.artifactId,
		version: request.version,
		byteSize: request.byteSize,
	}
	const operation = (async () => {
		const cacheDisabled = await resolveWorkerPersistentCacheDisabled(request.cacheDisabled, ensureGCodeCacheConcurrency)
		controller.signal.throwIfAborted()
		const common = {
			source: request.file && request.file.size === request.byteSize
				? createUploadedFileRangeSource(identity, request.file, {
					onCacheProgress: (cachedThrough) => { cachedBytes = Math.max(cachedBytes, cachedThrough) },
					persistentCacheDisabled: cacheDisabled,
				})
				: createCachedRangeSource(identity, request.url, {
					onCacheProgress: (cachedThrough) => { cachedBytes = Math.max(cachedBytes, cachedThrough) },
					persistentCacheDisabled: cacheDisabled,
				}),
			version: request.version,
			byteSize: request.byteSize,
			signal: controller.signal,
			onProgress: (progress: { completedBytes: number; totalBytes: number }) => {
				completedBytes = progress.completedBytes
				worker.postMessage({ type: 'progress', requestId: request.requestId, ...progress, cachedBytes })
			},
		}
		if (request.type === 'index') {
			const cached = cacheDisabled ? undefined : await readCachedGCodeAnalysis(identity)
			if (cached) {
				if (request.byteSize <= MAX_PERSISTENT_GCODE_BYTES) {
					try {
						for (let start = 0; start < request.byteSize; start += GCODE_CACHE_CHUNK_BYTES) {
							controller.signal.throwIfAborted()
							const end = Math.min(request.byteSize, start + GCODE_CACHE_CHUNK_BYTES)
							await common.source.read(start, end - 1, request.version, controller.signal)
							worker.postMessage({
								type: 'progress', requestId: request.requestId,
								completedBytes: end, totalBytes: request.byteSize, cachedBytes,
							})
						}
					} catch (error) {
						if (classifyCachedAnalysisRefillError(error, controller.signal.aborted) !== 'recoverable') throw error
						if (!await hasCompleteCachedGCodeCopy(identity, controller.signal)) {
							throw new Error('OFFLINE_COPY_INCOMPLETE')
						}
						// The version-bound analysis remains useful online/offline even if a
						// missing raw chunk cannot be refilled right now.
					}
				} else {
					worker.postMessage({
						type: 'progress', requestId: request.requestId,
						completedBytes: request.byteSize, totalBytes: request.byteSize, cachedBytes,
					})
				}
				return cached
			}
			const result = await buildSparseLineIndex(common)
			if (!cacheDisabled) await writeCachedGCodeAnalysis(identity, result)
			return result
		}
		return literalSearch({
			...common,
			query: request.query,
			caseSensitive: request.caseSensitive,
			matchOffset: request.matchOffset,
			onMatchProgress: (totalMatches) => {
				worker.postMessage({
					type: 'progress', requestId: request.requestId,
					completedBytes, totalBytes: request.byteSize, totalMatches,
				})
			},
		})
	})()
  void operation.then(
    (result) => {
      if (active.get(request.requestId) !== controller) return
      active.delete(request.requestId)
      if ('lineNumbers' in result) {
        worker.postMessage({ type: 'searchResult', requestId: request.requestId, ...result }, [result.lineNumbers.buffer])
      } else {
        worker.postMessage({ type: 'indexResult', requestId: request.requestId, ...result })
      }
    },
    (error: unknown) => {
      if (active.get(request.requestId) !== controller) return
      active.delete(request.requestId)
      const code = controller.signal.aborted ? 'CANCELLED' : error instanceof Error ? error.message : 'WORKER_ERROR'
      worker.postMessage({ type: 'error', requestId: request.requestId, code })
    },
  )
})
