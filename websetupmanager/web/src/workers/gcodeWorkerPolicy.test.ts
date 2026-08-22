import { describe, expect, it, vi } from 'vitest'
import { classifyCachedAnalysisRefillError, resolveWorkerPersistentCacheDisabled } from './gcodeWorkerPolicy'

describe('cached G-code analysis refill policy', () => {
	it.each([
		new TypeError('Failed to fetch'),
		new Error('RANGE_FAILED'),
		new Error('INCOMPLETE_RANGE'),
		new Error('FILE_READ_FAILED'),
		new Error('FILE_READ_UNAVAILABLE'),
		new DOMException('The file cannot be read', 'NotReadableError'),
	])('suppresses a recoverable refill failure: %s', (error) => {
		expect(classifyCachedAnalysisRefillError(error, false)).toBe('recoverable')
	})

	it.each([
		new Error('ARTIFACT_CHANGED'),
		new Error('CACHE_SCOPE_BLOCKED'),
		new Error('INVALID_CACHE_RANGE'),
		'unknown failure',
	])('rethrows a fatal refill failure: %s', (error) => {
		expect(classifyCachedAnalysisRefillError(error, false)).toBe('fatal')
	})

	it('rethrows aborts whether they come from the signal or the thrown error', () => {
		expect(classifyCachedAnalysisRefillError(new Error('RANGE_FAILED'), true)).toBe('abort')
		expect(classifyCachedAnalysisRefillError(new DOMException('cancelled', 'AbortError'), false)).toBe('abort')
	})

	it('verifies Web Locks independently in the Worker realm and degrades to network/File-only', async () => {
		const verified = vi.fn().mockResolvedValue(true)
		expect(await resolveWorkerPersistentCacheDisabled(false, verified)).toBe(false)
		expect(verified).toHaveBeenCalledTimes(1)

		const unavailable = vi.fn().mockRejectedValue(new DOMException('unsupported', 'NotSupportedError'))
		expect(await resolveWorkerPersistentCacheDisabled(false, unavailable)).toBe(true)
		expect(await resolveWorkerPersistentCacheDisabled(true, vi.fn())).toBe(true)
	})
})
