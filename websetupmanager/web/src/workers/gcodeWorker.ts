/// <reference lib="webworker" />

import { buildSparseLineIndex, literalSearch, type RangeSource } from './gcodeCore'

type WorkerRequest =
  | { type: 'index'; requestId: string; url: string; version: string; byteSize: number }
  | { type: 'search'; requestId: string; url: string; version: string; byteSize: number; query: string; caseSensitive: boolean; matchOffset?: number }
  | { type: 'cancel'; requestId: string }

const active = new Map<string, AbortController>()

const sourceFor = (url: string): RangeSource => ({
  async read(start, endInclusive, version, signal) {
    const response = await fetch(url, {
      headers: { Range: `bytes=${start}-${endInclusive}`, 'If-Match': `"${version}"` },
      credentials: 'same-origin',
      cache: 'no-store',
      signal,
    })
    if (response.status !== 206 && !(response.status === 200 && start === 0)) {
      throw new Error(response.status === 412 ? 'ARTIFACT_CHANGED' : 'RANGE_FAILED')
    }
    if (response.headers.get('etag') !== `"${version}"`) throw new Error('ARTIFACT_CHANGED')
    return new Uint8Array(await response.arrayBuffer())
  },
})

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
  const common = {
    source: sourceFor(request.url), version: request.version,
    byteSize: request.byteSize, signal: controller.signal,
    onProgress: (progress: { completedBytes: number; totalBytes: number }) => {
		completedBytes = progress.completedBytes
      worker.postMessage({ type: 'progress', requestId: request.requestId, ...progress })
    },
  }
  const operation = request.type === 'index'
    ? buildSparseLineIndex(common)
		: literalSearch({
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
