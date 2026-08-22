const RECOVERABLE_REFILL_CODES = new Set([
	'RANGE_FAILED',
	'INCOMPLETE_RANGE',
	'FILE_READ_FAILED',
	'FILE_READ_UNAVAILABLE',
])

const RECOVERABLE_IO_ERROR_NAMES = new Set([
	'NetworkError',
	'NotReadableError',
])

export type CachedAnalysisRefillErrorDisposition = 'recoverable' | 'abort' | 'fatal'

export async function resolveWorkerPersistentCacheDisabled(
	requestDisabled: boolean | undefined,
	ensureConcurrency: () => Promise<boolean>,
): Promise<boolean> {
	if (requestDisabled) return true
	try {
		return !await ensureConcurrency()
	} catch {
		return true
	}
}

function errorDetails(error: unknown): { name?: string; message?: string } {
	if ((typeof error !== 'object' && typeof error !== 'function') || error === null) return {}
	const candidate = error as { name?: unknown; message?: unknown }
	return {
		name: typeof candidate.name === 'string' ? candidate.name : undefined,
		message: typeof candidate.message === 'string' ? candidate.message : undefined,
	}
}

// Refilling raw chunks is best-effort once a version-bound analysis exists,
// but identity, scope, cancellation, and unexpected programming failures must
// still reach the worker client.
export function classifyCachedAnalysisRefillError(
	error: unknown,
	signalAborted: boolean,
): CachedAnalysisRefillErrorDisposition {
	const { name, message } = errorDetails(error)
	if (signalAborted || name === 'AbortError') return 'abort'
	if (message === 'ARTIFACT_CHANGED' || message === 'CACHE_SCOPE_BLOCKED') return 'fatal'
	if (error instanceof TypeError) return 'recoverable'
	if (message && RECOVERABLE_REFILL_CODES.has(message) || name && RECOVERABLE_IO_ERROR_NAMES.has(name)) return 'recoverable'
	return 'fatal'
}
