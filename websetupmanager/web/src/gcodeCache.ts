import type { LineIndexResult, ToolTableEntry } from './workers/gcodeCore'

export const GCODE_CACHE_CHUNK_BYTES = 1 << 20
export const MAX_PERSISTENT_GCODE_BYTES = 32 << 20
const MAX_RAW_CACHE_BYTES = 128 << 20
const MIN_RAW_CACHE_GROUP_COST = GCODE_CACHE_CHUNK_BYTES
const RAW_CACHE_TTL_MS = 30 * 24 * 60 * 60 * 1_000
const CACHE_NAME = 'web-setup-manager-gcode-v3'
const LOCAL_STATE_KEY = 'web-setup-manager.gcode-cache.v3'
const LOCAL_SCOPE_BLOCK_PREFIX = 'web-setup-manager.gcode-cache.scope-block.v1.'
const LOCAL_AUTH_GENERATION_KEY = 'web-setup-manager.gcode-cache.auth-generation.v1'
const CACHE_SCHEMA = 3
const ANALYSIS_SCHEMA = 1
const MAX_LOCAL_RECORDS = 24
const MAX_ANALYSIS_RECORDS = 48
const MAX_ANALYSIS_JSON_CHARS = 4 << 20
const MAX_TOOL_ENTRIES = 1_024
const MAX_INDEX_ENTRIES = 100_001
const MAX_PENDING_CACHE_SCOPES = 128
const MAX_CACHE_AUTH_JSON_CHARS = 64 << 10
const SYNTHETIC_CACHE_URL = 'https://web-setup-manager.invalid/__gcode_cache_v3__'
const CACHE_CHANNEL_NAME = 'web-setup-manager-gcode-cache-control-v1'
const CACHE_MUTATION_LOCK_NAME = 'web-setup-manager-gcode-cache-mutation-v1'
const blockedScopes = new Set<string>()
const scopeEpochs = new Map<string, number>()
const scopeBlockTokens = new Map<string, string>()
const latestIssuedScopeTokens = new Map<string, string>()
const blockedScopeTokens = new Map<string, string>()
const scopeAuthGenerations = new Map<string, { token: string; authGeneration: string }>()
const scopeCleanupPromises = new Map<string, { token: string; promise: Promise<boolean> }>()
let cacheChannel: BroadcastChannel | undefined
let fallbackMutationTail: Promise<void> = Promise.resolve()
let authGenerationFallback = 'initial'
let mutationLockUnavailable = false

let mutationLockVerified = false
let cacheStorageQuarantined = false

/** @internal Vitest isolation for this module's origin-wide realm state. */
export function __resetGCodeCacheForTests(): void {
	if (import.meta.env.MODE !== 'test') return
	blockedScopes.clear()
	scopeEpochs.clear()
	scopeBlockTokens.clear()
	latestIssuedScopeTokens.clear()
	blockedScopeTokens.clear()
	scopeAuthGenerations.clear()
	scopeCleanupPromises.clear()
	cacheChannel?.close()
	cacheChannel = undefined
	fallbackMutationTail = Promise.resolve()
	authGenerationFallback = 'initial'
	mutationLockUnavailable = false
	mutationLockVerified = false
	cacheStorageQuarantined = false
}

/** @internal Verifies that auth cleanup metadata remains bounded in tests. */
export function __gCodeCacheAuthGenerationRecordCountForTests(): number {
	return import.meta.env.MODE === 'test' ? scopeAuthGenerations.size : 0
}

function originWideMutationLockAvailable(): boolean {
	return typeof navigator !== 'undefined' && Boolean(navigator.locks) && !mutationLockUnavailable
}

function persistentCacheConcurrencyVerified(): boolean {
	return originWideMutationLockAvailable() && mutationLockVerified && !cacheStorageQuarantined
}

async function withCacheMutationLock<T>(operation: () => Promise<T>): Promise<T> {
	if (originWideMutationLockAvailable()) {
		let entered = false
		try {
			const result: unknown = await navigator.locks.request(CACHE_MUTATION_LOCK_NAME, { mode: 'exclusive' }, () => {
				entered = true
				mutationLockVerified = true
				return operation()
			})
			return result as T
		} catch (error) {
			if (!entered) {
				mutationLockUnavailable = true
				mutationLockVerified = false
			}
			throw error
		}
	}
	let release!: () => void
	const previous = fallbackMutationTail
	fallbackMutationTail = new Promise<void>((resolve) => { release = resolve })
	await previous
	try {
		return await operation()
	} finally {
		release()
	}
}

async function verifyOriginWideMutationLock(): Promise<boolean> {
	if (!originWideMutationLockAvailable()) return false
	try {
		await withCacheMutationLock(() => Promise.resolve())
		// A previous storage fault may quarantine payload I/O, but recovery still
		// needs to acquire the verified lock in order to audit and repair it.
		return originWideMutationLockAvailable() && mutationLockVerified
	} catch {
		return false
	}
}

function ensureCacheChannel(): void {
  if (cacheChannel || typeof BroadcastChannel === 'undefined') return
  try {
    cacheChannel = new BroadcastChannel(CACHE_CHANNEL_NAME)
    cacheChannel.addEventListener('message', (event: MessageEvent<unknown>) => {
      if (!event.data || typeof event.data !== 'object') return
      const message = event.data as { type?: string; scope?: string; token?: string; authGeneration?: string }
      if (typeof message.scope !== 'string' || message.scope.length < 1 || message.scope.length > 512) return
      if (message.type === 'block') {
				if (validScopeBlockToken(message.authGeneration ?? null)) observeAuthGeneration(message.authGeneration!)
				if (validScopeBlockToken(message.token ?? null)) {
					blockedScopeTokens.set(message.scope, message.token!)
					if (validScopeBlockToken(message.authGeneration ?? null)) {
						rememberScopeAuthGeneration(message.scope, message.token!, message.authGeneration!)
					}
				}
        blockedScopes.add(message.scope)
        scopeEpochs.set(message.scope, (scopeEpochs.get(message.scope) ?? 0) + 1)
      } else if (message.type === 'allow') {
				const observedToken = blockedScopeTokens.get(message.scope)
				if (observedToken && (!validScopeBlockToken(message.token ?? null) || observedToken !== message.token)) return
				if (validScopeBlockToken(message.token ?? null)) forgetScopeAuthGeneration(message.scope, message.token)
				if (!message.token || scopeBlockTokens.get(message.scope) === message.token) scopeBlockTokens.delete(message.scope)
				blockedScopeTokens.delete(message.scope)
        blockedScopes.delete(message.scope)
      }
    })
  } catch {
    cacheChannel = undefined
  }
}

function localScopeBlockKey(scope: string): string {
	return `${LOCAL_SCOPE_BLOCK_PREFIX}${encodeURIComponent(scope)}`
}

function validScopeBlockToken(token: string | null): token is string {
	return token !== null && token.length >= 1 && token.length <= 128 && /^[A-Za-z0-9._:-]+$/.test(token)
}

function rememberScopeAuthGeneration(scope: string, token: string, authGeneration: string): void {
	// Only the newest token for a scope can still authorize cleanup. Keeping a
	// token-keyed history leaked one entry on every login/logout cycle because a
	// BroadcastChannel does not echo the sender's release message.
	scopeAuthGenerations.delete(scope)
	scopeAuthGenerations.set(scope, { token, authGeneration })
	while (scopeAuthGenerations.size > MAX_PENDING_CACHE_SCOPES) {
		const oldestScope = scopeAuthGenerations.keys().next().value
		if (oldestScope === undefined) break
		scopeAuthGenerations.delete(oldestScope)
	}
}

function rememberedScopeAuthGeneration(scope: string, token: string): string | undefined {
	const record = scopeAuthGenerations.get(scope)
	return record?.token === token ? record.authGeneration : undefined
}

function forgetScopeAuthGeneration(scope: string, token?: string): void {
	const record = scopeAuthGenerations.get(scope)
	if (record && (token === undefined || record.token === token)) scopeAuthGenerations.delete(scope)
}

function newScopeBlockToken(): string {
	if (typeof crypto !== 'undefined' && typeof crypto.randomUUID === 'function') return crypto.randomUUID()
	return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2)}-${Math.random().toString(36).slice(2)}`
}

function authGenerationOrdinal(generation: string): number | undefined {
	if (generation === 'initial') return 0
	const match = /^g([1-9]\d*):/.exec(generation)
	if (!match) return undefined
	const ordinal = Number(match[1])
	return Number.isSafeInteger(ordinal) ? ordinal : undefined
}

function newAuthGeneration(): string {
	const ordinal = authGenerationOrdinal(currentGCodeCacheAuthGeneration()) ?? 0
	return `g${ordinal + 1}:${newScopeBlockToken()}`
}

function markerGenerationIsNewer(marker: string | undefined, expected: string): boolean {
	if (!marker) return false
	if (marker === expected) return false
	const markerOrdinal = authGenerationOrdinal(marker)
	const expectedOrdinal = authGenerationOrdinal(expected)
	// Equal ordinals with different nonces can happen when two isolated realms
	// cannot observe localStorage/BroadcastChannel. There is no safe ordering in
	// that case, so the durable marker must win (fail closed).
	return markerOrdinal !== undefined && expectedOrdinal !== undefined && markerOrdinal >= expectedOrdinal
}

function observeAuthGeneration(generation: string, preferEqualOrdinal = false): string {
	if (!validScopeBlockToken(generation) || authGenerationOrdinal(generation) === undefined) return authGenerationFallback
	const observedOrdinal = authGenerationOrdinal(generation)!
	const currentOrdinal = authGenerationOrdinal(authGenerationFallback) ?? -1
	if (observedOrdinal > currentOrdinal || (preferEqualOrdinal && observedOrdinal === currentOrdinal)) {
		authGenerationFallback = generation
	}
	return authGenerationFallback
}

function localScopePersistentBlockToken(scope: string): string | undefined {
	const realmToken = scopeBlockTokens.get(scope)
	if (realmToken) return realmToken
	if (typeof window === 'undefined') return undefined
	try {
		const token = window.localStorage.getItem(localScopeBlockKey(scope))
		if (validScopeBlockToken(token)) {
			scopeBlockTokens.set(scope, token)
			return token
		}
		return undefined
	} catch {
		// Fall through to the realm-local copy.
	}
	return scopeBlockTokens.get(scope)
}

function setLocalScopePersistentBlock(scope: string, token?: string): boolean {
	if (token) scopeBlockTokens.set(scope, token)
	else scopeBlockTokens.delete(scope)
	if (typeof window === 'undefined') return false
	try {
		if (token) window.localStorage.setItem(localScopeBlockKey(scope), token)
		else window.localStorage.removeItem(localScopeBlockKey(scope))
		return token ? window.localStorage.getItem(localScopeBlockKey(scope)) === token :
			window.localStorage.getItem(localScopeBlockKey(scope)) === null
	} catch {
		// Cache Storage tombstones and BroadcastChannel remain available.
	}
	return false
}

function currentGCodeCacheAuthGeneration(): string {
	if (typeof window === 'undefined') return authGenerationFallback
	try {
		const stored = window.localStorage.getItem(LOCAL_AUTH_GENERATION_KEY)
		if (validScopeBlockToken(stored)) observeAuthGeneration(stored, true)
	} catch {
		// Fall through to the generation observed in this realm/BroadcastChannel.
	}
	return authGenerationFallback
}

function persistGCodeCacheAuthGeneration(generation: string): void {
	authGenerationFallback = generation
	if (typeof window === 'undefined') return
	try {
		window.localStorage.setItem(LOCAL_AUTH_GENERATION_KEY, generation)
	} catch {
		// Cache tombstones and BroadcastChannel carry the same generation.
	}
}

export function captureGCodeCacheAuthGeneration(): string {
	ensureCacheChannel()
	return currentGCodeCacheAuthGeneration()
}

function cacheScopeActive(scope: string, epoch?: number): boolean {
	return !blockedScopes.has(scope) &&
		(epoch === undefined || (scopeEpochs.get(scope) ?? 0) === epoch)
}

function scopeBlockTokenCurrent(scope: string, token: string): boolean {
	return (localScopePersistentBlockToken(scope) ?? blockedScopeTokens.get(scope)) === token &&
		(!latestIssuedScopeTokens.has(scope) || latestIssuedScopeTokens.get(scope) === token)
}

export interface GCodeCacheIdentity {
  scope: string
  artifactId: string
  version: string
  byteSize: number
}

export interface CachedGCodeAnalysis extends LineIndexResult {
  tools: ToolTableEntry[]
  toolsTruncated: boolean
}

export interface LocalGCodeCacheState {
  scope: string
  artifactId: string
  version: string
  byteSize: number
  indexedBytes: number
  cachedBytes: number
  analysisComplete: boolean
  lineCount?: number
  tools?: ToolTableEntry[]
  toolsTruncated?: boolean
  updatedAt: number
}

export function isGCodeCachePersistenceQuarantined(scope: string): boolean {
	return typeof scope !== 'string' || scope.length < 1 || scope.length > 512 ||
		!persistentCacheConcurrencyVerified() || blockedScopes.has(scope) || blockedScopeTokens.has(scope) ||
		Boolean(localScopePersistentBlockToken(scope))
}

interface LocalManifest {
  schema: number
  records: LocalGCodeCacheState[]
}

function checkedIdentity(identity: GCodeCacheIdentity): GCodeCacheIdentity {
  if (!identity || typeof identity.scope !== 'string' || identity.scope.length < 1 || identity.scope.length > 512 ||
      typeof identity.artifactId !== 'string' || identity.artifactId.length < 1 || identity.artifactId.length > 256 ||
      typeof identity.version !== 'string' || identity.version.length < 1 || identity.version.length > 256 ||
      !Number.isSafeInteger(identity.byteSize) || identity.byteSize < 0) {
    throw new Error('INVALID_CACHE_IDENTITY')
  }
  return identity
}

function recordKey(identity: GCodeCacheIdentity): string {
  checkedIdentity(identity)
  return `${identity.scope}\u0000${identity.artifactId}\u0000${identity.version}\u0000${identity.byteSize}`
}

function cacheRequest(identity: GCodeCacheIdentity, kind: 'chunk' | 'analysis', start = 0): Request {
  checkedIdentity(identity)
  const url = new URL(SYNTHETIC_CACHE_URL)
  url.searchParams.set('schema', String(CACHE_SCHEMA))
  url.searchParams.set('scope', identity.scope)
  url.searchParams.set('artifact', identity.artifactId)
  url.searchParams.set('version', identity.version)
  url.searchParams.set('size', String(identity.byteSize))
  url.searchParams.set('kind', kind)
  if (kind === 'chunk') url.searchParams.set('start', String(start))
  return new Request(url, { method: 'GET', credentials: 'omit' })
}

function cacheScopeBlockRequest(scope: string): Request {
	if (typeof scope !== 'string' || scope.length < 1 || scope.length > 512) throw new Error('INVALID_CACHE_SCOPE')
	const url = new URL(SYNTHETIC_CACHE_URL)
	url.searchParams.set('schema', String(CACHE_SCHEMA))
	url.searchParams.set('scope', scope)
	url.searchParams.set('kind', 'scope-block')
	return new Request(url, { method: 'GET', credentials: 'omit' })
}

function cacheAuthGenerationRequest(): Request {
	const url = new URL(SYNTHETIC_CACHE_URL)
	url.searchParams.set('schema', String(CACHE_SCHEMA))
	url.searchParams.set('kind', 'auth-generation')
	return new Request(url, { method: 'GET', credentials: 'omit' })
}

interface CachePendingScope {
	scope: string
	token: string
	authGeneration: string
}

function authGenerationResponse(generation: string, pending: CachePendingScope[]): Response {
	return new Response(JSON.stringify({ schema: 1, generation, pending }), { headers: {
		'Content-Type': 'application/json',
		'X-WSM-Auth-Generation': generation,
		'X-WSM-Updated-At': String(Date.now()),
	} })
}

interface CacheGlobalAuthority {
	generation?: string
	pending: CachePendingScope[]
	complete: boolean
}

function validPendingScope(value: unknown): value is CachePendingScope {
	if (!value || typeof value !== 'object') return false
	const candidate = value as Partial<CachePendingScope>
	return typeof candidate.scope === 'string' && candidate.scope.length >= 1 && candidate.scope.length <= 512 &&
		validScopeBlockToken(candidate.token ?? null) && validScopeBlockToken(candidate.authGeneration ?? null) &&
		authGenerationOrdinal(candidate.authGeneration!) !== undefined
}

async function readCacheGlobalAuthority(cache: Cache | undefined): Promise<CacheGlobalAuthority> {
	const authority: CacheGlobalAuthority = { pending: [], complete: typeof caches === 'undefined' }
	if (!cache) return authority
	try {
		const response = await cache.match(cacheAuthGenerationRequest())
		if (!response) return { ...authority, complete: true }
		const headerGeneration = response.headers.get('X-WSM-Auth-Generation')
		if (validScopeBlockToken(headerGeneration) && authGenerationOrdinal(headerGeneration) !== undefined) {
			authority.generation = headerGeneration
		}
		if (response.body) {
			const parsed = JSON.parse(await readBoundedResponseText(response, MAX_CACHE_AUTH_JSON_CHARS)) as unknown
			if (!parsed || typeof parsed !== 'object') return authority
			const candidate = parsed as { schema?: unknown; generation?: unknown; pending?: unknown }
			if (candidate.schema !== 1 || candidate.generation !== authority.generation ||
				!Array.isArray(candidate.pending) || candidate.pending.length > MAX_PENDING_CACHE_SCOPES ||
				!candidate.pending.every(validPendingScope)) return authority
			const scopes = new Set<string>()
			for (const pending of candidate.pending) {
				if (scopes.has(pending.scope)) return authority
				scopes.add(pending.scope)
			}
			authority.pending = candidate.pending
		}
		authority.complete = true
	} catch {
		// Unknown/poisoned authority is fail-closed.
	}
	return authority
}

interface CacheAuthSnapshot {
	global?: string
	markers: string[]
	pending: CachePendingScope[]
	complete: boolean
}

async function readCacheAuthSnapshot(cache: Cache | undefined): Promise<CacheAuthSnapshot> {
	const globalAuthority = await readCacheGlobalAuthority(cache)
	const snapshot: CacheAuthSnapshot = {
		global: globalAuthority.generation,
		markers: [],
		pending: globalAuthority.pending,
		complete: globalAuthority.complete && typeof caches === 'undefined',
	}
	if (!cache) return snapshot
	try {
		for (const request of await cache.keys()) {
			const url = new URL(request.url)
			if (!isOwnedCacheURL(url) || url.searchParams.get('kind') !== 'scope-block') continue
			const markerGeneration = (await cache.match(request))?.headers.get('X-WSM-Auth-Generation') ?? null
			if (validScopeBlockToken(markerGeneration) && authGenerationOrdinal(markerGeneration) !== undefined) {
				snapshot.markers.push(markerGeneration)
			}
		}
		snapshot.complete = globalAuthority.complete
	} catch {
		// Return every authority that was read before the storage failure.
	}
	return snapshot
}

function newestAuthGeneration(generations: Array<string | undefined>): string | undefined {
	return generations.filter((generation): generation is string => generation !== undefined)
		.sort((left, right) => (authGenerationOrdinal(right) ?? -1) - (authGenerationOrdinal(left) ?? -1))[0]

}

function cacheAuthSnapshotObservesSuperseding(snapshot: CacheAuthSnapshot, expected: string): boolean {
	return [snapshot.global, ...snapshot.markers, ...snapshot.pending.map((entry) => entry.authGeneration)].some((generation) =>
		generation !== undefined && markerGenerationIsNewer(generation, expected))
}

function cacheAuthSnapshotSupersedes(snapshot: CacheAuthSnapshot, expected: string): boolean {
	return !snapshot.complete || cacheAuthSnapshotObservesSuperseding(snapshot, expected)
}

function cacheAuthSnapshotConfirms(snapshot: CacheAuthSnapshot, expected: string): boolean {
	if (!snapshot.complete) return false
	if (typeof caches === 'undefined') {
		// There is no persistent Cache Storage payload or journal to authorize.
		// Shared localStorage/realm generation still fences the small derived
		// manifest, which logout clears independently.
		return snapshot.global === undefined && snapshot.pending.length === 0 && snapshot.markers.length === 0 &&
			currentGCodeCacheAuthGeneration() === expected
	}
	if (expected === 'initial') {
		return snapshot.global === undefined && snapshot.pending.length === 0 && snapshot.markers.length === 0
	}
	return snapshot.global === expected
}

function samePendingScopes(left: CachePendingScope[], right: CachePendingScope[]): boolean {
	return left.length === right.length && left.every((entry, index) => {
		const other = right[index]
		return other?.scope === entry.scope && other.token === entry.token && other.authGeneration === entry.authGeneration
	})
}

async function writeCacheAuthGeneration(
	cache: Cache | undefined,
	generation: string,
	pending?: CachePendingScope[],
): Promise<boolean> {
	// Absence of Cache Storage means there is no global journal to confirm. The
	// caller may still rely on an independently verified localStorage scope
	// marker, but must never treat a no-op as a durable logout seal.
	if (!cache) return false
	const retainedPending = pending ?? (await readCacheGlobalAuthority(cache)).pending
	if (retainedPending.length > MAX_PENDING_CACHE_SCOPES) return false
	await cache.put(cacheAuthGenerationRequest(), authGenerationResponse(generation, retainedPending))
	const confirmed = await readCacheGlobalAuthority(cache)
	return confirmed.complete && confirmed.generation === generation && samePendingScopes(confirmed.pending, retainedPending)
}

/**
 * Captures the origin-wide logout generation before an authentication request.
 * Cache Storage is the recovery authority when localStorage is disabled.
 */
export async function captureDurableGCodeCacheAuthGeneration(): Promise<string> {
	ensureCacheChannel()
	const capture = async (canRepair: boolean) => {
		const local = currentGCodeCacheAuthGeneration()
		const cache = await openCache()
		const snapshot = await readCacheAuthSnapshot(cache)
		const durableGenerations = [snapshot.global, ...snapshot.markers, ...snapshot.pending.map((entry) => entry.authGeneration)]
		const newest = newestAuthGeneration([local, ...durableGenerations]) ?? local
		if (!snapshot.complete) {
			cacheStorageQuarantined = true
			observeAuthGeneration(newest, true)
			return newest
		}
		if (typeof caches === 'undefined') return local
		const globalOrdinal = snapshot.global ? authGenerationOrdinal(snapshot.global) : undefined
		const markerNeedsRecovery = [local, ...snapshot.markers, ...snapshot.pending.map((entry) => entry.authGeneration)].some((generation) => {
			if (generation === 'initial' && globalOrdinal === undefined) return false
			const ordinal = authGenerationOrdinal(generation)!
			return globalOrdinal === undefined || ordinal > globalOrdinal ||
				(ordinal === globalOrdinal && generation !== snapshot.global)
		})
		if (markerNeedsRecovery) {
			observeAuthGeneration(newest, true)
			const recovered = newAuthGeneration()
			if (canRepair) {
				const confirmed = await writeCacheAuthGeneration(cache, recovered).catch(() => false)
				if (!confirmed) {
					cacheStorageQuarantined = true
					return newest
				}
				persistGCodeCacheAuthGeneration(recovered)
			} else {
				// Read-only fencing in degraded mode. Never rewrite shared authority
				// without origin-wide exclusion.
				authGenerationFallback = recovered
			}
			return recovered
		}
		if (snapshot.global) {
			const durableOrdinal = authGenerationOrdinal(snapshot.global)!
			const localOrdinal = authGenerationOrdinal(local) ?? -1
			if (durableOrdinal > localOrdinal || (durableOrdinal === localOrdinal && snapshot.global !== local)) {
				if (canRepair) persistGCodeCacheAuthGeneration(snapshot.global)
				else observeAuthGeneration(snapshot.global, true)
				return snapshot.global
			}
		}
		return newest
	}
	if (originWideMutationLockAvailable()) {
		try {
			return await withCacheMutationLock(() => capture(true))
		} catch {
			// A rejecting Web Locks implementation disables persistent caching. The
			// durable read below still fences an in-flight login when available.
		}
	}
	return capture(false)
}

/** Verifies Cache Storage exclusion in the current Window or Worker realm. */
export async function ensureGCodeCacheConcurrency(): Promise<boolean> {
	ensureCacheChannel()
	if (cacheStorageQuarantined) return false
	return verifyOriginWideMutationLock()
}

type CacheScopeBlockProbe =
	| { state: 'clear' }
	| { state: 'blocked'; token: string; authGeneration?: string }
	| { state: 'unavailable' }

type DurableScopeBlockSelection =
	| { state: 'clear' }
	| { state: 'blocked'; token: string; authGeneration?: string }
	| { state: 'ambiguous' }

function selectDurableScopeBlock(
	pending: CachePendingScope | undefined,
	marker: Exclude<CacheScopeBlockProbe, { state: 'unavailable' }>,
): DurableScopeBlockSelection {
	if (!pending) return marker
	if (marker.state === 'clear') {
		return { state: 'blocked', token: pending.token, authGeneration: pending.authGeneration }
	}
	if (marker.token === pending.token) {
		const markerOrdinal = marker.authGeneration === undefined ? -1 : authGenerationOrdinal(marker.authGeneration) ?? -1
		return markerOrdinal > authGenerationOrdinal(pending.authGeneration)! ? marker : {
			state: 'blocked', token: pending.token, authGeneration: pending.authGeneration,
		}
	}
	const markerOrdinal = marker.authGeneration === undefined ? undefined : authGenerationOrdinal(marker.authGeneration)
	const pendingOrdinal = authGenerationOrdinal(pending.authGeneration)!
	if (markerOrdinal === undefined || pendingOrdinal > markerOrdinal) {
		return { state: 'blocked', token: pending.token, authGeneration: pending.authGeneration }
	}
	if (markerOrdinal > pendingOrdinal) return marker
	return { state: 'ambiguous' }
}

async function probeCacheScopeMarker(cache: Cache, scope: string): Promise<CacheScopeBlockProbe> {
	try {
		const response = await cache.match(cacheScopeBlockRequest(scope))
		if (!response) return { state: 'clear' }
		const token = response.headers.get('X-WSM-Block-Token')
		const authGeneration = response.headers.get('X-WSM-Auth-Generation')
		return {
			state: 'blocked',
			token: validScopeBlockToken(token) ? token : 'legacy',
			authGeneration: validScopeBlockToken(authGeneration) ? authGeneration : undefined,
		}
	} catch {
		// Cache Storage may be disabled even though the API object exists. The
		// in-memory/localStorage tombstone still applies; online I/O must continue.
		return { state: 'unavailable' }
	}
}

/**
 * Returns the effective durable tombstone for payload I/O. The global pending
 * journal is published before the per-scope marker, so it is independently
 * sufficient to fence a principal when a tab crashes in that small window.
 */
async function probeCacheScopeAuthority(cache: Cache, scope: string): Promise<CacheScopeBlockProbe> {
	const global = await readCacheGlobalAuthority(cache)
	if (!global.complete) return { state: 'unavailable' }
	const pending = global.pending.find((entry) => entry.scope === scope)
	const marker = await probeCacheScopeMarker(cache, scope)
	if (marker.state === 'unavailable') {
		if (!pending) return marker
		return { state: 'blocked', token: pending.token, authGeneration: pending.authGeneration }
	}
	const selected = selectDurableScopeBlock(pending, marker)
	// Same ordinal with different tokens is split-brain authority. Neither side
	// may enable persistence until recovery rotates a unified generation.
	return selected.state === 'ambiguous' ? { state: 'unavailable' } : selected
}

async function openCache(): Promise<Cache | undefined> {
  if (typeof caches === 'undefined') return undefined
  try {
    return await caches.open(CACHE_NAME)
  } catch {
    return undefined
  }
}

function expectedChunkLength(identity: GCodeCacheIdentity, start: number): number {
  return Math.min(GCODE_CACHE_CHUNK_BYTES, identity.byteSize - start)
}

async function readExactResponseBytes(response: Response, expected: number, signal?: AbortSignal): Promise<Uint8Array> {
  const declared = response.headers.get('content-length')
  if (declared !== null && declared !== String(expected)) throw new Error('INCOMPLETE_RANGE')
  if (!response.body) throw new Error('INCOMPLETE_RANGE')
  const output = new Uint8Array(expected)
  const reader = response.body.getReader()
  let offset = 0
  let complete = false
  try {
    for (;;) {
      signal?.throwIfAborted()
      const { done, value } = await reader.read()
      if (done) break
      if (offset + value.byteLength > expected) throw new Error('INCOMPLETE_RANGE')
      output.set(value, offset)
      offset += value.byteLength
    }
    if (offset !== expected) throw new Error('INCOMPLETE_RANGE')
    complete = true
    return output
  } finally {
    if (!complete) await reader.cancel().catch(() => undefined)
  }
}

async function readBoundedResponseText(response: Response, maximumBytes: number): Promise<string> {
  const declared = Number(response.headers.get('content-length'))
  if (Number.isFinite(declared) && declared > maximumBytes) throw new Error('CACHE_ANALYSIS_TOO_LARGE')
  if (!response.body) throw new Error('CACHE_ANALYSIS_INVALID')
  const reader = response.body.getReader()
  const decoder = new TextDecoder('utf-8', { fatal: true })
  let total = 0
  let output = ''
  let complete = false
  try {
    for (;;) {
      const { done, value } = await reader.read()
      if (done) break
      total += value.byteLength
      if (total > maximumBytes) throw new Error('CACHE_ANALYSIS_TOO_LARGE')
      output += decoder.decode(value, { stream: true })
    }
    output += decoder.decode()
    complete = true
    return output
  } finally {
    if (!complete) await reader.cancel().catch(() => undefined)
  }
}

function isCompleteChunk(identity: GCodeCacheIdentity, start: number, length: number): boolean {
  return Number.isSafeInteger(start) && start >= 0 && start % GCODE_CACHE_CHUNK_BYTES === 0 &&
    start < identity.byteSize && length === expectedChunkLength(identity, start)
}

async function putChunk(cache: Cache, identity: GCodeCacheIdentity, start: number, bytes: Uint8Array): Promise<void> {
  if (!isCompleteChunk(identity, start, bytes.byteLength)) return
  if (!cacheScopeActive(identity.scope)) throw new Error('CACHE_SCOPE_BLOCKED')
  const request = cacheRequest(identity, 'chunk', start)
  const body = bytes.slice().buffer
  await cache.put(request, new Response(body, {
    status: 200,
    headers: {
      'Content-Type': 'application/octet-stream',
      'X-WSM-Artifact-Version': identity.version,
      'X-WSM-Byte-Start': String(start),
      'X-WSM-Byte-Length': String(bytes.byteLength),
      'X-WSM-Cached-At': String(Date.now()),
    },
  }))
  if (!cacheScopeActive(identity.scope)) {
    await cache.delete(request)
    throw new Error('CACHE_SCOPE_BLOCKED')
  }
}

export async function readCachedGCodeRange(
  identity: GCodeCacheIdentity,
  start: number,
  endInclusive: number,
  signal?: AbortSignal,
): Promise<Uint8Array | undefined> {
  checkedIdentity(identity)
  ensureCacheChannel()
	if (!persistentCacheConcurrencyVerified()) return undefined
	const sourceEpoch = scopeEpochs.get(identity.scope) ?? 0
	if (!cacheScopeActive(identity.scope, sourceEpoch)) return undefined
	if (localScopePersistentBlockToken(identity.scope)) return undefined
  if (!Number.isSafeInteger(start) || !Number.isSafeInteger(endInclusive) || start < 0 || endInclusive < start || endInclusive >= identity.byteSize) {
    throw new Error('INVALID_CACHE_RANGE')
  }
  signal?.throwIfAborted()
  if (identity.byteSize > MAX_PERSISTENT_GCODE_BYTES) return undefined
	try {
		return await withCacheMutationLock(async () => {
			const cache = await openCache()
			if (!cache) return undefined
			if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) throw new Error('CACHE_SCOPE_BLOCKED')
			if ((await probeCacheScopeAuthority(cache, identity.scope)).state !== 'clear') return undefined
			if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) throw new Error('CACHE_SCOPE_BLOCKED')
			const firstChunk = Math.floor(start / GCODE_CACHE_CHUNK_BYTES) * GCODE_CACHE_CHUNK_BYTES
			const output = new Uint8Array(endInclusive - start + 1)
			let outputOffset = 0
			for (let chunkStart = firstChunk; chunkStart <= endInclusive; chunkStart += GCODE_CACHE_CHUNK_BYTES) {
				signal?.throwIfAborted()
				const response = await cache.match(cacheRequest(identity, 'chunk', chunkStart))
				if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) throw new Error('CACHE_SCOPE_BLOCKED')
				if (!response || response.headers.get('X-WSM-Artifact-Version') !== identity.version ||
					response.headers.get('X-WSM-Byte-Start') !== String(chunkStart)) return undefined
				const cachedAt = Number(response.headers.get('X-WSM-Cached-At'))
				if (!Number.isFinite(cachedAt) || cachedAt < 1 || Date.now() - cachedAt > RAW_CACHE_TTL_MS) return undefined
				const expected = expectedChunkLength(identity, chunkStart)
				const bytes = await readExactResponseBytes(response, expected, signal)
				if (bytes.byteLength !== expected || response.headers.get('X-WSM-Byte-Length') !== String(expected)) return undefined
				const from = Math.max(start, chunkStart)
				const to = Math.min(endInclusive + 1, chunkStart + bytes.byteLength)
				output.set(bytes.subarray(from - chunkStart, to - chunkStart), outputOffset)
				outputOffset += to - from
			}
			signal?.throwIfAborted()
			if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) throw new Error('CACHE_SCOPE_BLOCKED')
			return outputOffset === output.byteLength ? output : undefined
		})
	} catch (error) {
		if (signal?.aborted) throw error
		if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) throw new Error('CACHE_SCOPE_BLOCKED')
		return undefined
	}
}

export async function hasCompleteCachedGCodeCopy(identity: GCodeCacheIdentity, signal?: AbortSignal): Promise<boolean> {
	checkedIdentity(identity)
	if (identity.byteSize > MAX_PERSISTENT_GCODE_BYTES || !persistentCacheConcurrencyVerified()) return false
	if (identity.byteSize === 0) return true
	try {
		for (let start = 0; start < identity.byteSize; start += GCODE_CACHE_CHUNK_BYTES) {
			signal?.throwIfAborted()
			const end = Math.min(identity.byteSize, start + GCODE_CACHE_CHUNK_BYTES) - 1
			if (!await readCachedGCodeRange(identity, start, end, signal)) return false
		}
		return true
	} catch (error) {
		if (signal?.aborted) throw error
		return false
	}
}

export async function writeCachedGCodeRange(
  identity: GCodeCacheIdentity,
  start: number,
  bytes: Uint8Array,
): Promise<boolean> {
  checkedIdentity(identity)
  ensureCacheChannel()
	if (!persistentCacheConcurrencyVerified()) return false
  const sourceEpoch = scopeEpochs.get(identity.scope) ?? 0
  if (!cacheScopeActive(identity.scope, sourceEpoch)) return false
	if (localScopePersistentBlockToken(identity.scope)) return false
  if (identity.byteSize > MAX_PERSISTENT_GCODE_BYTES) return false
	if (!isCompleteChunk(identity, start, bytes.byteLength)) return false
	return withCacheMutationLock(async () => {
		if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return false
		const cache = await openCache()
		if (!cache || !cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return false
		if ((await probeCacheScopeAuthority(cache, identity.scope)).state !== 'clear') return false
		if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return false
		await prepareRawCacheBudget(identity)
		if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return false
		try {
			await putChunk(cache, identity, start, bytes)
		} catch (error) {
			if (!(error instanceof DOMException) || error.name !== 'QuotaExceededError') return false
			if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope) ||
				(await probeCacheScopeAuthority(cache, identity.scope)).state !== 'clear') return false
			await evictOldestRawCacheGroup(identity)
			try {
				await putChunk(cache, identity, start, bytes)
			} catch {
				return false
			}
		}
		if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) {
			await cache.delete(cacheRequest(identity, 'chunk', start))
			return false
		}
		return true
	}).catch(() => false)
}

interface RawCacheGroup {
  key: string
  requests: Request[]
  bytes: number
  newest: number
}

function isOwnedCacheURL(url: URL): boolean {
  const base = new URL(SYNTHETIC_CACHE_URL)
  return url.origin === base.origin && url.pathname === base.pathname && url.searchParams.get('schema') === String(CACHE_SCHEMA)
}

async function rawCacheGroups(cache: Cache, scope?: string): Promise<RawCacheGroup[]> {
  const groups = new Map<string, RawCacheGroup>()
  const now = Date.now()
  for (const request of await cache.keys()) {
    const url = new URL(request.url)
    const entryScope = url.searchParams.get('scope')
    if (!isOwnedCacheURL(url) || url.searchParams.get('kind') !== 'chunk' || !entryScope || (scope !== undefined && entryScope !== scope)) continue
    const response = await cache.match(request)
    const bytes = Number(response?.headers.get('X-WSM-Byte-Length'))
    const cachedAt = Number(response?.headers.get('X-WSM-Cached-At'))
    if (!response || !Number.isSafeInteger(bytes) || bytes < 1 || !Number.isFinite(cachedAt) || cachedAt < 1 || now - cachedAt > RAW_CACHE_TTL_MS) {
      await cache.delete(request)
      continue
    }
    const key = `${entryScope}\u0000${url.searchParams.get('artifact')}\u0000${url.searchParams.get('version')}\u0000${url.searchParams.get('size')}`
    const group = groups.get(key) ?? { key, requests: [], bytes: 0, newest: 0 }
    group.requests.push(request)
    group.bytes += bytes
    group.newest = Math.max(group.newest, cachedAt)
    groups.set(key, group)
  }
  return [...groups.values()]
}

async function prepareRawCacheBudget(identity: GCodeCacheIdentity): Promise<void> {
  const cache = await openCache()
  if (!cache || identity.byteSize > MAX_PERSISTENT_GCODE_BYTES) return
  const currentKey = `${identity.scope}\u0000${identity.artifactId}\u0000${identity.version}\u0000${identity.byteSize}`
  const groups = await rawCacheGroups(cache)
	const groupCost = (bytes: number) => Math.max(MIN_RAW_CACHE_GROUP_COST, bytes)
  let total = groups.reduce((sum, group) => sum + groupCost(group.bytes), 0)
	const currentBytes = groups.find((group) => group.key === currentKey)?.bytes ?? 0
	const required = Math.max(0, groupCost(identity.byteSize) - (currentBytes > 0 ? groupCost(currentBytes) : 0))
  const eviction = groups.filter((group) => group.key !== currentKey).sort((left, right) => left.newest - right.newest)
  while (total + required > MAX_RAW_CACHE_BYTES && eviction.length > 0) {
    const group = eviction.shift()!
    await Promise.all(group.requests.map((request) => cache.delete(request)))
		total -= groupCost(group.bytes)
  }
}

async function evictOldestRawCacheGroup(identity: GCodeCacheIdentity): Promise<void> {
  const cache = await openCache()
  if (!cache) return
  const currentKey = `${identity.scope}\u0000${identity.artifactId}\u0000${identity.version}\u0000${identity.byteSize}`
  const oldest = (await rawCacheGroups(cache))
    .filter((group) => group.key !== currentKey)
    .sort((left, right) => left.newest - right.newest)[0]
  if (oldest) await Promise.all(oldest.requests.map((request) => cache.delete(request)))
}

export function createUploadedFileRangeSource(identity: GCodeCacheIdentity, file: File, options: {
  onCacheProgress?: (cachedThrough: number) => void
	persistentCacheDisabled?: boolean
} = {}) {
  checkedIdentity(identity)
  if (file.size !== identity.byteSize) throw new Error('CACHE_FILE_SIZE_MISMATCH')
  ensureCacheChannel()
  const sourceEpoch = scopeEpochs.get(identity.scope) ?? 0
  let cacheWritable = !options.persistentCacheDisabled && persistentCacheConcurrencyVerified() && identity.byteSize <= MAX_PERSISTENT_GCODE_BYTES
  return {
    async read(start: number, endInclusive: number, version: string, signal: AbortSignal): Promise<Uint8Array> {
      if (!cacheScopeActive(identity.scope, sourceEpoch)) throw new Error('CACHE_SCOPE_BLOCKED')
      if (version !== identity.version) throw new Error('ARTIFACT_CHANGED')
      if (!Number.isSafeInteger(start) || !Number.isSafeInteger(endInclusive) || start < 0 || endInclusive < start || endInclusive >= identity.byteSize) {
        throw new Error('INVALID_CACHE_RANGE')
      }
      signal.throwIfAborted()
			const slice = file.slice(start, endInclusive + 1)
			let buffer: ArrayBuffer
			if (typeof slice.arrayBuffer === 'function') {
				buffer = await slice.arrayBuffer()
			} else if (typeof FileReader !== 'undefined') {
				buffer = await new Promise<ArrayBuffer>((resolve, reject) => {
					const reader = new FileReader()
					const abort = () => reader.abort()
					const cleanup = () => signal.removeEventListener('abort', abort)
					reader.addEventListener('load', () => {
						cleanup()
						if (reader.result instanceof ArrayBuffer) resolve(reader.result)
						else reject(new Error('INCOMPLETE_RANGE'))
					}, { once: true })
					reader.addEventListener('error', () => { cleanup(); reject(new Error('FILE_READ_FAILED')) }, { once: true })
					reader.addEventListener('abort', () => {
						cleanup()
						reject(signal.reason instanceof Error ? signal.reason : new DOMException('Aborted', 'AbortError'))
					}, { once: true })
					signal.addEventListener('abort', abort, { once: true })
					reader.readAsArrayBuffer(slice)
				})
			} else {
				throw new Error('FILE_READ_UNAVAILABLE')
			}
			const bytes = new Uint8Array(buffer)
      signal.throwIfAborted()
      if (bytes.byteLength !== endInclusive - start + 1) throw new Error('INCOMPLETE_RANGE')
      if (!cacheScopeActive(identity.scope, sourceEpoch)) throw new Error('CACHE_SCOPE_BLOCKED')
      if (cacheWritable && isCompleteChunk(identity, start, bytes.byteLength)) {
        let stored = false
        try {
          stored = await writeCachedGCodeRange(identity, start, bytes)
        } catch {
          cacheWritable = false
        }
        if (!cacheScopeActive(identity.scope, sourceEpoch)) {
          throw new Error('CACHE_SCOPE_BLOCKED')
        }
        if (stored) options.onCacheProgress?.(endInclusive + 1)
        else cacheWritable = false
      }
      return bytes
    },
  }
}

export function createCachedRangeSource(identity: GCodeCacheIdentity, url: string, options: {
  networkFirst?: boolean
  onCacheProgress?: (cachedThrough: number) => void
  onOfflineFallback?: () => void
	persistentCacheDisabled?: boolean
} = {}) {
  checkedIdentity(identity)
  ensureCacheChannel()
  const sourceEpoch = scopeEpochs.get(identity.scope) ?? 0
  let cacheWritable = !options.persistentCacheDisabled && persistentCacheConcurrencyVerified() && identity.byteSize <= MAX_PERSISTENT_GCODE_BYTES
  return {
    async read(start: number, endInclusive: number, version: string, signal: AbortSignal): Promise<Uint8Array> {
      if (!cacheScopeActive(identity.scope, sourceEpoch)) throw new Error('CACHE_SCOPE_BLOCKED')
      if (version !== identity.version) throw new Error('ARTIFACT_CHANGED')
      if (!options.networkFirst && !options.persistentCacheDisabled) {
        const cached = await readCachedGCodeRange(identity, start, endInclusive, signal)
        if (cached) {
          options.onCacheProgress?.(endInclusive + 1)
          return cached
        }
      }
      let response: Response
      try {
        response = await fetch(url, {
          headers: { Range: `bytes=${start}-${endInclusive}`, 'If-Match': `"${identity.version}"` },
          credentials: 'same-origin', cache: 'no-store', signal,
        })
      } catch (error) {
        if (options.networkFirst && !options.persistentCacheDisabled && error instanceof TypeError) {
			if (!await hasCompleteCachedGCodeCopy(identity, signal)) throw new Error('OFFLINE_COPY_INCOMPLETE')
          const cached = await readCachedGCodeRange(identity, start, endInclusive, signal)
          if (cached) {
            options.onOfflineFallback?.()
            options.onCacheProgress?.(endInclusive + 1)
            return cached
          }
        }
        throw error
      }
      if (response.status === 412 || response.headers.get('etag') !== `"${identity.version}"`) {
        throw new Error('ARTIFACT_CHANGED')
      }
      if (response.status !== 206 || response.headers.get('content-range') !== `bytes ${start}-${endInclusive}/${identity.byteSize}`) {
        throw new Error('RANGE_FAILED')
      }
      const bytes = await readExactResponseBytes(response, endInclusive - start + 1, signal)
      signal.throwIfAborted()
      if (!cacheScopeActive(identity.scope, sourceEpoch)) throw new Error('CACHE_SCOPE_BLOCKED')
      if (bytes.byteLength !== endInclusive - start + 1) throw new Error('INCOMPLETE_RANGE')
      // Cache failures (unsupported browser, private mode or quota pressure)
      // must never make the verified network preview fail.
      if (cacheWritable && isCompleteChunk(identity, start, bytes.byteLength)) {
        let stored = false
        try {
          stored = await writeCachedGCodeRange(identity, start, bytes)
        } catch {
          cacheWritable = false
        }
        if (!cacheScopeActive(identity.scope, sourceEpoch)) {
          throw new Error('CACHE_SCOPE_BLOCKED')
        }
        if (stored) options.onCacheProgress?.(endInclusive + 1)
        else cacheWritable = false
      }
      return bytes
    },
  }
}

function validTool(value: unknown): value is ToolTableEntry {
  if (!value || typeof value !== 'object') return false
  const tool = value as Partial<ToolTableEntry>
  return Number.isSafeInteger(tool.toolNumber) && Number(tool.toolNumber) >= 0 && Number(tool.toolNumber) <= 999_999_999 &&
    Number.isSafeInteger(tool.firstLine) && Number(tool.firstLine) >= 1 &&
    Number.isSafeInteger(tool.references) && Number(tool.references) >= 1 &&
    Number.isSafeInteger(tool.changes) && Number(tool.changes) >= 0
}

function validateAnalysis(value: unknown, identity: GCodeCacheIdentity): CachedGCodeAnalysis | undefined {
  if (!value || typeof value !== 'object') return undefined
  const candidate = value as Partial<CachedGCodeAnalysis> & { schema?: number; version?: string; byteSize?: number }
  if (candidate.schema !== ANALYSIS_SCHEMA || candidate.version !== identity.version || candidate.byteSize !== identity.byteSize ||
      !Number.isSafeInteger(candidate.lineCount) || Number(candidate.lineCount) < 1 || Number(candidate.lineCount) > identity.byteSize + 1 ||
      !Array.isArray(candidate.entries) || candidate.entries.length < 1 || candidate.entries.length > MAX_INDEX_ENTRIES ||
      !Array.isArray(candidate.tools) || candidate.tools.length > MAX_TOOL_ENTRIES || !candidate.tools.every(validTool) ||
      typeof candidate.toolsTruncated !== 'boolean') return undefined
  let lastLine = 0
  let lastOffset = -1
	const firstEntry = candidate.entries[0]
	if (!firstEntry || typeof firstEntry !== 'object' || firstEntry.line !== 1 || firstEntry.byteOffset !== 0) return undefined
  for (const entry of candidate.entries) {
    if (!entry || !Number.isSafeInteger(entry.line) || entry.line < 1 || !Number.isSafeInteger(entry.byteOffset) ||
        entry.byteOffset < 0 || entry.byteOffset > identity.byteSize || entry.line > Number(candidate.lineCount) ||
				entry.line > entry.byteOffset + 1 ||
        entry.line <= lastLine || entry.byteOffset <= lastOffset) return undefined
    lastLine = entry.line
    lastOffset = entry.byteOffset
  }
  let lastTool = -1
  for (const tool of candidate.tools) {
    if (tool.toolNumber <= lastTool || tool.firstLine > Number(candidate.lineCount)) return undefined
    lastTool = tool.toolNumber
  }
  return {
    lineCount: Number(candidate.lineCount),
    entries: candidate.entries,
    tools: candidate.tools,
    toolsTruncated: candidate.toolsTruncated,
  }
}

export async function readCachedGCodeAnalysis(identity: GCodeCacheIdentity): Promise<CachedGCodeAnalysis | undefined> {
  checkedIdentity(identity)
  ensureCacheChannel()
	if (!persistentCacheConcurrencyVerified()) return undefined
	const sourceEpoch = scopeEpochs.get(identity.scope) ?? 0
	if (!cacheScopeActive(identity.scope, sourceEpoch)) return undefined
	if (localScopePersistentBlockToken(identity.scope)) return undefined
  try {
		return await withCacheMutationLock(async () => {
			const cache = await openCache()
			if (!cache) return undefined
			const request = cacheRequest(identity, 'analysis')
			if ((await probeCacheScopeAuthority(cache, identity.scope)).state !== 'clear') return undefined
			if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return undefined
			const response = await cache.match(request)
			if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return undefined
			if (!response) return undefined
			const cachedAt = Number(response.headers.get('X-WSM-Cached-At'))
			if (!Number.isFinite(cachedAt) || cachedAt < 1 || Date.now() - cachedAt > RAW_CACHE_TTL_MS) return undefined
			const analysis = validateAnalysis(JSON.parse(await readBoundedResponseText(response, MAX_ANALYSIS_JSON_CHARS)), identity)
			if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return undefined
			return analysis
		})
  } catch {
    return undefined
  }
}

export async function writeCachedGCodeAnalysis(identity: GCodeCacheIdentity, analysis: CachedGCodeAnalysis): Promise<boolean> {
  checkedIdentity(identity)
  ensureCacheChannel()
	if (!persistentCacheConcurrencyVerified()) return false
	const sourceEpoch = scopeEpochs.get(identity.scope) ?? 0
	if (!cacheScopeActive(identity.scope, sourceEpoch)) return false
	if (localScopePersistentBlockToken(identity.scope)) return false
  if (!validateAnalysis({ ...analysis, schema: ANALYSIS_SCHEMA, version: identity.version, byteSize: identity.byteSize }, identity)) return false
	const payload = JSON.stringify({
		schema: ANALYSIS_SCHEMA,
		version: identity.version,
		byteSize: identity.byteSize,
		...analysis,
	})
	if (payload.length > MAX_ANALYSIS_JSON_CHARS) return false
	return withCacheMutationLock(async () => {
		const cache = await openCache()
		if (!cache || !cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return false
		if ((await probeCacheScopeAuthority(cache, identity.scope)).state !== 'clear') return false
		if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return false
		try {
			const analysisKeys: Array<{ request: Request; cachedAt: number }> = []
			const currentRequest = cacheRequest(identity, 'analysis')
			for (const request of await cache.keys()) {
				if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return false
				const url = new URL(request.url)
				if (!isOwnedCacheURL(url) || url.searchParams.get('kind') !== 'analysis') continue
				const response = await cache.match(request)
				const cachedAt = Number(response?.headers.get('X-WSM-Cached-At'))
				if (!response || !Number.isFinite(cachedAt) || cachedAt < 1 || Date.now() - cachedAt > RAW_CACHE_TTL_MS) {
					await cache.delete(request)
				} else if (request.url !== currentRequest.url) {
					analysisKeys.push({ request, cachedAt })
				}
			}
			analysisKeys.sort((left, right) => right.cachedAt - left.cachedAt)
			for (const { request } of analysisKeys.slice(MAX_ANALYSIS_RECORDS - 1)) {
				if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return false
				await cache.delete(request)
			}
			if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return false
			await cache.put(currentRequest, new Response(payload, { headers: {
				'Content-Type': 'application/json',
				'X-WSM-Cached-At': String(Date.now()),
			} }))
			if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) {
				await cache.delete(currentRequest)
				return false
			}
			return true
		} catch {
			return false
		}
	}).catch(() => false)
}

function readManifest(): LocalManifest {
  if (typeof window === 'undefined') return { schema: CACHE_SCHEMA, records: [] }
  try {
    const parsed = JSON.parse(window.localStorage.getItem(LOCAL_STATE_KEY) ?? 'null') as Partial<LocalManifest> | null
    if (!parsed || parsed.schema !== CACHE_SCHEMA || !Array.isArray(parsed.records)) return { schema: CACHE_SCHEMA, records: [] }
    return { schema: CACHE_SCHEMA, records: parsed.records.filter((value): value is LocalGCodeCacheState => {
      if (!value || typeof value !== 'object') return false
      const record = value as Partial<LocalGCodeCacheState>
      if (typeof record.scope !== 'string' || record.scope.length < 1 || record.scope.length > 512 ||
          typeof record.artifactId !== 'string' || record.artifactId.length < 1 || record.artifactId.length > 256 ||
          typeof record.version !== 'string' || record.version.length < 1 || record.version.length > 256 ||
          !Number.isSafeInteger(record.byteSize) || Number(record.byteSize) < 0 ||
          !Number.isSafeInteger(record.indexedBytes) || Number(record.indexedBytes) < 0 || Number(record.indexedBytes) > Number(record.byteSize) ||
          !Number.isSafeInteger(record.cachedBytes) || Number(record.cachedBytes) < 0 || Number(record.cachedBytes) > Number(record.byteSize) ||
          typeof record.analysisComplete !== 'boolean' ||
          !Number.isFinite(record.updatedAt)) return false
      if (record.lineCount !== undefined && (!Number.isSafeInteger(record.lineCount) || record.lineCount < 1)) return false
      if (record.tools !== undefined && (!Array.isArray(record.tools) || record.tools.length > MAX_TOOL_ENTRIES || !record.tools.every(validTool))) return false
      return record.toolsTruncated === undefined || typeof record.toolsTruncated === 'boolean'
    }).slice(0, MAX_LOCAL_RECORDS) }
  } catch {
    return { schema: CACHE_SCHEMA, records: [] }
  }
}

function writeManifest(manifest: LocalManifest): boolean {
  if (typeof window === 'undefined') return true
  try {
    window.localStorage.setItem(LOCAL_STATE_KEY, JSON.stringify(manifest))
		return true
  } catch {
    // localStorage can be disabled or full. Cache Storage remains usable.
		return false
  }
}

export function readLocalGCodeCacheState(identity: GCodeCacheIdentity): LocalGCodeCacheState | undefined {
	if (!persistentCacheConcurrencyVerified() || !cacheScopeActive(identity.scope) || localScopePersistentBlockToken(identity.scope)) return undefined
  const key = recordKey(identity)
  return readManifest().records.find((record) => recordKey(record) === key)
}

export async function updateLocalGCodeCacheState(
  identity: GCodeCacheIdentity,
  patch: Partial<Omit<LocalGCodeCacheState, 'scope' | 'artifactId' | 'version' | 'byteSize' | 'updatedAt'>>,
): Promise<LocalGCodeCacheState> {
	checkedIdentity(identity)
	ensureCacheChannel()
	const sourceEpoch = scopeEpochs.get(identity.scope) ?? 0
	const buildState = (previous?: LocalGCodeCacheState): LocalGCodeCacheState => ({
		scope: identity.scope,
		artifactId: identity.artifactId,
		version: identity.version,
		byteSize: identity.byteSize,
		indexedBytes: Math.max(0, Math.min(identity.byteSize, Math.max(patch.indexedBytes ?? 0, previous?.indexedBytes ?? 0))),
		cachedBytes: Math.max(0, Math.min(identity.byteSize, Math.max(patch.cachedBytes ?? 0, previous?.cachedBytes ?? 0))),
		analysisComplete: Boolean(previous?.analysisComplete || patch.analysisComplete),
		lineCount: previous?.analysisComplete && previous.lineCount !== undefined ? previous.lineCount : patch.lineCount ?? previous?.lineCount,
		tools: (previous?.analysisComplete && previous.tools !== undefined ? previous.tools : patch.tools ?? previous?.tools)?.slice(0, MAX_TOOL_ENTRIES),
		toolsTruncated: previous?.analysisComplete && previous.toolsTruncated !== undefined ? previous.toolsTruncated : patch.toolsTruncated ?? previous?.toolsTruncated,
		updatedAt: Date.now(),
	})
	if (!persistentCacheConcurrencyVerified()) return buildState()
	return withCacheMutationLock(async () => {
		const manifest = readManifest()
		const key = recordKey(identity)
		const previous = manifest.records.find((record) => recordKey(record) === key)
		const next = buildState(previous)
		if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return next
		manifest.records = [next, ...manifest.records.filter((record) => recordKey(record) !== key)]
			.sort((left, right) => right.updatedAt - left.updatedAt)
			.slice(0, MAX_LOCAL_RECORDS)
		await Promise.resolve()
		if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) return next
		writeManifest(manifest)
		// A logout can publish its localStorage token while this realm owns the
		// cross-origin lock. Roll back our derived metadata before releasing it;
		// the logout cleanup will make the same idempotent deletion afterwards.
		if (!cacheScopeActive(identity.scope, sourceEpoch) || localScopePersistentBlockToken(identity.scope)) {
			const current = readManifest()
			current.records = current.records.filter((record) => record.scope !== identity.scope)
			writeManifest(current)
		}
		return next
	}).catch(() => buildState())
}

async function purgeOwnedCacheWithoutOriginLock(scope: string): Promise<void> {
	// Persistent reads/writes from this build are already disabled when the
	// origin-wide lock is unavailable. A durable scope marker blocks writers in
	// other contexts while we delete only this principal's derived payload. Never
	// erase another scope's tombstone during a best-effort recovery.
	const manifest = readManifest()
	manifest.records = manifest.records.filter((record) => record.scope !== scope)
	writeManifest(manifest)
	const cache = await openCache()
	if (cache) {
		try {
			for (const request of await cache.keys()) {
				const url = new URL(request.url)
				if (isOwnedCacheURL(url) && url.searchParams.get('scope') === scope &&
					url.searchParams.get('kind') !== 'scope-block') await cache.delete(request)
			}
		} catch {
			// Best effort only; quarantine remains active below.
		}
	}
}

export async function clearGCodeCacheScope(
	scope: string,
	expectedToken?: string,
	expectedAuthGeneration?: string,
): Promise<boolean> {
	if (typeof scope !== 'string' || scope.length < 1 || scope.length > 512) return false
	if (expectedToken && !validScopeBlockToken(expectedToken)) return false
	if (expectedAuthGeneration !== undefined && !validScopeBlockToken(expectedAuthGeneration)) return false
	let token = expectedToken ?? localScopePersistentBlockToken(scope) ?? blockedScopeTokens.get(scope)
	if (!token) token = await blockGCodeCacheScope(scope)
	if (!token || !scopeBlockTokenCurrent(scope, token)) return false
	if (!localScopePersistentBlockToken(scope)) setLocalScopePersistentBlock(scope, token)
	ensureCacheChannel()
	const tokenAuthGeneration = rememberedScopeAuthGeneration(scope, token) ?? currentGCodeCacheAuthGeneration()
	const cleanupAuthority = expectedAuthGeneration ?? tokenAuthGeneration
	markGCodeCacheScopeBlocked(scope, token, tokenAuthGeneration)
	if (!await verifyOriginWideMutationLock()) {
		await purgeOwnedCacheWithoutOriginLock(scope)
		return false
	}
	const existing = scopeCleanupPromises.get(scope)
	if (existing?.token === token) return existing.promise
	if (existing) return existing.promise.then(() => clearGCodeCacheScope(scope, token, expectedAuthGeneration))
	const cleanup = (async () => {
		// Give other same-origin contexts one task to observe the tombstone before
		// the origin-wide mutation lock takes a deletion snapshot.
		await new Promise<void>((resolve) => setTimeout(resolve, 0))
		return withCacheMutationLock(async () => {
			if (!scopeBlockTokenCurrent(scope, token)) return false
			let cacheClean = typeof caches === 'undefined'
			for (let pass = 0; pass < 2; pass += 1) {
				if (!scopeBlockTokenCurrent(scope, token)) return false
				const cache = await openCache()
				if (cache) {
					try {
						const cleanupSnapshot = await readCacheAuthSnapshot(cache)
						if (cacheAuthSnapshotSupersedes(cleanupSnapshot, cleanupAuthority)) return false
						const pendingScope = cleanupSnapshot.pending.find((entry) => entry.scope === scope)
						if (pendingScope && pendingScope.token !== token) {
							const pendingOrdinal = authGenerationOrdinal(pendingScope.authGeneration)!
							const cleanupOrdinal = authGenerationOrdinal(cleanupAuthority)!
							if (pendingOrdinal >= cleanupOrdinal) {
								setLocalScopePersistentBlock(scope, pendingScope.token)
								markGCodeCacheScopeBlocked(scope, pendingScope.token, pendingScope.authGeneration)
								return false
							}
						}
						const existingMarker = await probeCacheScopeMarker(cache, scope)
						if (existingMarker.state === 'unavailable') return false
						if (existingMarker.state === 'blocked') {
							const markerOrdinal = existingMarker.authGeneration === undefined
								? undefined : authGenerationOrdinal(existingMarker.authGeneration)
							const currentOrdinal = authGenerationOrdinal(cleanupAuthority)
							const markerStrictlyOlder = markerOrdinal !== undefined && currentOrdinal !== undefined && markerOrdinal < currentOrdinal
							const tokenConflict = existingMarker.token !== 'legacy' && existingMarker.token !== token
							if (markerGenerationIsNewer(existingMarker.authGeneration, cleanupAuthority) ||
								(tokenConflict && !markerStrictlyOlder)) {
								if (existingMarker.token !== 'legacy') {
									setLocalScopePersistentBlock(scope, existingMarker.token)
									markGCodeCacheScopeBlocked(scope, existingMarker.token, existingMarker.authGeneration)
								}
								return false
							}
						}
						// A storage fault can let the independently durable scope marker
						// commit while the global journal write is unavailable. Rebuild the
						// missing global authority only from an exact token+generation marker;
						// a local-only generation is never sufficient.
						if (cleanupSnapshot.global === undefined && cleanupAuthority !== 'initial') {
							const markerConfirmsAuthority = existingMarker.state === 'blocked' &&
								existingMarker.token === token && existingMarker.authGeneration === cleanupAuthority
							if (!markerConfirmsAuthority || !await writeCacheAuthGeneration(
								cache,
								cleanupAuthority,
								cleanupSnapshot.pending,
							)) return false
						}
						await cache.put(cacheScopeBlockRequest(scope), new Response(null, { headers: {
							'X-WSM-Blocked-At': String(Date.now()),
							'X-WSM-Block-Token': token,
								'X-WSM-Auth-Generation': cleanupAuthority,
						} }))
						const keys = await cache.keys()
						for (const request of keys) {
							if (!scopeBlockTokenCurrent(scope, token)) return false
							const url = new URL(request.url)
							if (isOwnedCacheURL(url) && url.searchParams.get('scope') === scope && url.searchParams.get('kind') !== 'scope-block') await cache.delete(request)
						}
						const remaining = await cache.keys()
						const hasScopedPayload = remaining.some((request) => {
							const url = new URL(request.url)
							return isOwnedCacheURL(url) && url.searchParams.get('scope') === scope && url.searchParams.get('kind') !== 'scope-block'
						})
						const probe = await probeCacheScopeMarker(cache, scope)
						cacheClean = !hasScopedPayload && probe.state === 'blocked' && probe.token === token
					} catch {
						cacheClean = false
					}
				} else if (typeof caches !== 'undefined') {
					cacheClean = false
				}
				await Promise.resolve()
			}
			if (!scopeBlockTokenCurrent(scope, token)) return false
			const manifest = readManifest()
			manifest.records = manifest.records.filter((record) => record.scope !== scope)
			const manifestClean = writeManifest(manifest) && !readManifest().records.some((record) => record.scope === scope)
			return cacheClean && manifestClean
		})
	})().catch(() => false)
	const cleanupRecord = { token, promise: cleanup }
	scopeCleanupPromises.set(scope, cleanupRecord)
	void cleanup.finally(() => {
		if (scopeCleanupPromises.get(scope) === cleanupRecord) scopeCleanupPromises.delete(scope)
	})
	return cleanup
}

export function waitForGCodeCacheScopeCleanup(scope: string): Promise<void> {
	if (typeof scope !== 'string' || scope.length < 1 || scope.length > 512) return Promise.resolve()
	return scopeCleanupPromises.get(scope)?.promise.then(() => undefined) ?? Promise.resolve()
}

function markGCodeCacheScopeBlocked(scope: string, token?: string, authGeneration?: string): void {
	if (!blockedScopes.has(scope)) scopeEpochs.set(scope, (scopeEpochs.get(scope) ?? 0) + 1)
	blockedScopes.add(scope)
	if (token) blockedScopeTokens.set(scope, token)
	cacheChannel?.postMessage({ type: 'block', scope, token, authGeneration })
}

export async function blockGCodeCacheScope(scope: string): Promise<string | undefined> {
	if (typeof scope !== 'string' || scope.length < 1 || scope.length > 512) return undefined
	ensureCacheChannel()
	const token = newScopeBlockToken()
	let authGeneration: string | undefined
	latestIssuedScopeTokens.set(scope, token)
	// Publish a durable fail-closed marker before the first await. A 401 handler
	// may be followed by an immediate tab close while the origin lock is queued.
	let sealed = setLocalScopePersistentBlock(scope, token)
	// Stop this realm and notify peers before waiting for the origin lock. The
	// durable local/Cache marker itself is published while holding that lock, so
	// it cannot race a compare-and-delete from an older allow continuation.
	markGCodeCacheScopeBlocked(scope, token)
	await withCacheMutationLock(async () => {
		if (latestIssuedScopeTokens.get(scope) !== token) return
		const originLockHeld = persistentCacheConcurrencyVerified()
		const cache = await openCache()
		const authSnapshot = await readCacheAuthSnapshot(cache)
		const durableGeneration = newestAuthGeneration([
			authSnapshot.global,
			...authSnapshot.markers,
			...authSnapshot.pending.map((entry) => entry.authGeneration),
		])
		if (durableGeneration) observeAuthGeneration(durableGeneration, true)
		authGeneration = newAuthGeneration()
		rememberScopeAuthGeneration(scope, token, authGeneration)
		persistGCodeCacheAuthGeneration(authGeneration)
		sealed = setLocalScopePersistentBlock(scope, token) || sealed
		if (originLockHeld && authSnapshot.complete) {
			const pending = [
				...authSnapshot.pending.filter((entry) => entry.scope !== scope),
				{ scope, token, authGeneration },
			]
			sealed = await writeCacheAuthGeneration(cache, authGeneration, pending).catch(() => false) || sealed
		}
		try {
			await cache?.put(cacheScopeBlockRequest(scope), new Response(null, { headers: {
				'X-WSM-Blocked-At': String(Date.now()),
				'X-WSM-Block-Token': token,
				'X-WSM-Auth-Generation': authGeneration,
			} }))
			const marker = cache ? await probeCacheScopeMarker(cache, scope) : { state: 'unavailable' as const }
			sealed = marker.state === 'blocked' && marker.token === token && marker.authGeneration === authGeneration || sealed
		} catch {
			// localStorage/realm state still quarantines persistence. Cleanup will
			// retry the Cache Storage tombstone before releasing this generation.
		}
	}).catch(() => undefined)
	if (latestIssuedScopeTokens.get(scope) === token && (!sealed || authGeneration === undefined)) {
		// Some embedded/private-mode implementations expose navigator.locks but
		// reject every request. Persist a conservative quarantine outside the
		// unusable lock rather than returning a non-durable successful logout.
		const cache = await openCache()
		const authSnapshot = await readCacheAuthSnapshot(cache)
		const durableGeneration = newestAuthGeneration([
			authSnapshot.global,
			...authSnapshot.markers,
			...authSnapshot.pending.map((entry) => entry.authGeneration),
		])
		if (durableGeneration) observeAuthGeneration(durableGeneration, true)
		authGeneration = newAuthGeneration()
		rememberScopeAuthGeneration(scope, token, authGeneration)
		persistGCodeCacheAuthGeneration(authGeneration)
		sealed = setLocalScopePersistentBlock(scope, token) || sealed
		await cache?.put(cacheScopeBlockRequest(scope), new Response(null, { headers: {
			'X-WSM-Blocked-At': String(Date.now()),
			'X-WSM-Block-Token': token,
			'X-WSM-Auth-Generation': authGeneration,
		} })).catch(() => undefined)
		const marker = cache ? await probeCacheScopeMarker(cache, scope) : { state: 'unavailable' as const }
		sealed = marker.state === 'blocked' && marker.token === token && marker.authGeneration === authGeneration || sealed
	}
	if (latestIssuedScopeTokens.get(scope) === token) {
		markGCodeCacheScopeBlocked(scope, token, authGeneration)
		latestIssuedScopeTokens.delete(scope)
		return sealed ? token : undefined
	}
	return undefined
}

export async function allowGCodeCacheScope(
	scope: string,
	expectedBlockToken?: string,
	expectedAuthGeneration?: string,
): Promise<boolean> {
	if (typeof scope !== 'string' || scope.length < 1 || scope.length > 512) return false
	if (expectedBlockToken !== undefined && !validScopeBlockToken(expectedBlockToken)) return false
	if (expectedAuthGeneration !== undefined && !validScopeBlockToken(expectedAuthGeneration)) return false
	ensureCacheChannel()
	const authAuthority = expectedAuthGeneration ?? currentGCodeCacheAuthGeneration()
	const authorityCurrent = () => currentGCodeCacheAuthGeneration() === authAuthority
	if (!authorityCurrent()) return false
	// Capture the generation before the first await. An older login/logout
	// continuation must never observe and release a block created afterwards.
	let invocationToken = expectedBlockToken ?? localScopePersistentBlockToken(scope) ?? blockedScopeTokens.get(scope)
	const tokenCurrent = () => {
		const observed = localScopePersistentBlockToken(scope) ?? blockedScopeTokens.get(scope)
		return observed === invocationToken &&
			(!latestIssuedScopeTokens.has(scope) || latestIssuedScopeTokens.get(scope) === invocationToken)
	}
	if (latestIssuedScopeTokens.has(scope) && latestIssuedScopeTokens.get(scope) !== invocationToken) return false
	await waitForGCodeCacheScopeCleanup(scope)
	if (!authorityCurrent() || !tokenCurrent()) return false
	const originLockUsable = await verifyOriginWideMutationLock()
	if (!authorityCurrent() || !tokenCurrent()) return false
	const cacheBeforeRecovery = await openCache()
	const authSnapshotBeforeRecovery = await readCacheAuthSnapshot(cacheBeforeRecovery)
	const probeBeforeRecovery = cacheBeforeRecovery
		? await probeCacheScopeMarker(cacheBeforeRecovery, scope)
		: (typeof caches === 'undefined' ? { state: 'clear' as const } : { state: 'unavailable' as const })
	if (!authorityCurrent() || !tokenCurrent() ||
		cacheAuthSnapshotObservesSuperseding(authSnapshotBeforeRecovery, authAuthority)) return false
	if (probeBeforeRecovery.state === 'unavailable' || !authSnapshotBeforeRecovery.complete) {
		// Unknown durable state can never authorize marker replacement. Keep all
		// persistent data quarantined, but allow the verified session to continue
		// with network/File reads.
		cacheStorageQuarantined = true
		blockedScopes.delete(scope)
		if (!invocationToken || blockedScopeTokens.get(scope) === invocationToken) blockedScopeTokens.delete(scope)
		forgetScopeAuthGeneration(scope, invocationToken)
		return true
	}
	if (cacheAuthSnapshotSupersedes(authSnapshotBeforeRecovery, authAuthority)) return false
	const pendingBeforeRecovery = authSnapshotBeforeRecovery.pending.find((entry) => entry.scope === scope)
	if (!authorityCurrent() || (probeBeforeRecovery.state === 'blocked' &&
		markerGenerationIsNewer(probeBeforeRecovery.authGeneration, authAuthority))) return false
	const durableSelection = selectDurableScopeBlock(pendingBeforeRecovery, probeBeforeRecovery)
	if (durableSelection.state === 'ambiguous') {
		cacheStorageQuarantined = true
		return false
	}
	const durableToken = durableSelection.state === 'blocked' ? durableSelection.token : undefined
	// An explicitly supplied token belongs to a concrete continuation and may
	// never be silently upgraded to a newer logout. Implicit recovery, however,
	// must adopt the newer durable journal entry instead of oscillating with an
	// older local/scope marker after a crash.
	if (expectedBlockToken && durableToken && durableToken !== 'legacy' && durableToken !== expectedBlockToken) return false
	const expectedToken = durableToken === 'legacy' ? (invocationToken ?? newScopeBlockToken()) : (durableToken ?? invocationToken)
	if (!originLockUsable) {
		if (expectedToken && expectedToken !== invocationToken) setLocalScopePersistentBlock(scope, expectedToken)
		if (expectedToken) {
			const durableGeneration = durableSelection.state === 'blocked' ? durableSelection.authGeneration : undefined
			const tokenGeneration = durableGeneration ?? rememberedScopeAuthGeneration(scope, expectedToken) ??
				authAuthority
			markGCodeCacheScopeBlocked(scope, expectedToken, tokenGeneration)
			await purgeOwnedCacheWithoutOriginLock(scope)
		}
		if (!authorityCurrent()) return false
		const observed = localScopePersistentBlockToken(scope) ?? blockedScopeTokens.get(scope)
		if (expectedToken && observed !== expectedToken) return false
		blockedScopes.delete(scope)
		if (!expectedToken || blockedScopeTokens.get(scope) === expectedToken) blockedScopeTokens.delete(scope)
		forgetScopeAuthGeneration(scope, expectedToken)
		cacheChannel?.postMessage({ type: 'allow', scope, token: expectedToken, authGeneration: authAuthority })
		return true
	}
	if (expectedToken && expectedToken !== invocationToken) {
		const adopted = await withCacheMutationLock(async () => {
			if (!authorityCurrent() || !tokenCurrent() || latestIssuedScopeTokens.has(scope)) return false
			const cache = await openCache()
			const snapshot = await readCacheAuthSnapshot(cache)
			if (cacheAuthSnapshotSupersedes(snapshot, authAuthority)) return false
			const marker = cache
				? await probeCacheScopeMarker(cache, scope)
				: (typeof caches === 'undefined' ? { state: 'clear' as const } : { state: 'unavailable' as const })
			if (marker.state === 'unavailable' || !snapshot.complete) return false
			const selected = selectDurableScopeBlock(snapshot.pending.find((entry) => entry.scope === scope), marker)
			if (selected.state === 'ambiguous') return false
			const selectedToken = selected.state === 'blocked' ? selected.token : undefined
			if (selectedToken && selectedToken !== 'legacy' && selectedToken !== expectedToken) return false
			if (!selectedToken && durableToken) return false
			setLocalScopePersistentBlock(scope, expectedToken)
			markGCodeCacheScopeBlocked(scope, expectedToken,
				selected.state === 'blocked' ? selected.authGeneration : authAuthority)
			return localScopePersistentBlockToken(scope) === expectedToken && authorityCurrent()
		}).catch(() => false)
		if (!adopted) return false
		invocationToken = expectedToken
	}
	const cleanupSucceeded = expectedToken
		? await clearGCodeCacheScope(scope, expectedToken, authAuthority)
		: true
	if (!authorityCurrent()) return false
	if (!expectedToken) {
		const authorityStillDurable = await withCacheMutationLock(async () => {
			const snapshot = await readCacheAuthSnapshot(await openCache())
			return !cacheAuthSnapshotSupersedes(snapshot, authAuthority) && cacheAuthSnapshotConfirms(snapshot, authAuthority)
		})
			.catch(() => false)
		if (!authorityStillDurable || !authorityCurrent()) return false
	}
	if (expectedToken && (localScopePersistentBlockToken(scope) !== expectedToken ||
		(latestIssuedScopeTokens.has(scope) && latestIssuedScopeTokens.get(scope) !== expectedToken))) return false
	const release = cleanupSucceeded && expectedToken ? await withCacheMutationLock(async (): Promise<'released' | 'quarantined' | 'superseded'> => {
		if (!authorityCurrent() || localScopePersistentBlockToken(scope) !== expectedToken ||
			(latestIssuedScopeTokens.has(scope) && latestIssuedScopeTokens.get(scope) !== expectedToken)) return 'superseded'
		const cache = await openCache()
		if (!cache && typeof caches !== 'undefined') return 'quarantined'
		if (cache) {
			const releaseSnapshot = await readCacheAuthSnapshot(cache)
			if (cacheAuthSnapshotSupersedes(releaseSnapshot, authAuthority)) return 'superseded'
			if (!cacheAuthSnapshotConfirms(releaseSnapshot, authAuthority)) return 'quarantined'
			const pendingScope = releaseSnapshot.pending.find((entry) => entry.scope === scope)
			if (pendingScope && pendingScope.token !== expectedToken) {
				const pendingOrdinal = authGenerationOrdinal(pendingScope.authGeneration)!
				const authorityOrdinal = authGenerationOrdinal(authAuthority)!
				if (pendingOrdinal >= authorityOrdinal) {
					setLocalScopePersistentBlock(scope, pendingScope.token)
					markGCodeCacheScopeBlocked(scope, pendingScope.token, pendingScope.authGeneration)
					return 'superseded'
				}
			}
			const probe = await probeCacheScopeMarker(cache, scope)
			if (probe.state === 'unavailable') return 'quarantined'
			if (probe.state === 'blocked' && markerGenerationIsNewer(probe.authGeneration, authAuthority)) return 'superseded'
			if (probe.state === 'blocked' && probe.token !== expectedToken && probe.token !== 'legacy') {
				setLocalScopePersistentBlock(scope, probe.token)
				return 'superseded'
			}
			if (pendingScope && !await writeCacheAuthGeneration(
				cache,
				authAuthority,
				releaseSnapshot.pending.filter((entry) => entry.scope !== scope),
			)) return 'quarantined'
			await cache.delete(cacheScopeBlockRequest(scope))
			if ((await probeCacheScopeMarker(cache, scope)).state !== 'clear') return 'quarantined'
		}
		if (!authorityCurrent() || localScopePersistentBlockToken(scope) !== expectedToken ||
			(latestIssuedScopeTokens.has(scope) && latestIssuedScopeTokens.get(scope) !== expectedToken)) return 'superseded'
		setLocalScopePersistentBlock(scope)
		if (localScopePersistentBlockToken(scope)) return 'quarantined'
		if (latestIssuedScopeTokens.get(scope) === expectedToken) latestIssuedScopeTokens.delete(scope)
		return 'released'
	}).catch(() => 'quarantined' as const) : (expectedToken ? 'quarantined' : 'released')
	if (release === 'superseded') return false
	cacheStorageQuarantined = release !== 'released'
	// A failed browser-storage purge quarantines persistence but must not break
	// verified network/File reads in the still-authenticated session.
	blockedScopes.delete(scope)
	if (!expectedToken || blockedScopeTokens.get(scope) === expectedToken) blockedScopeTokens.delete(scope)
	forgetScopeAuthGeneration(scope, expectedToken)
	cacheChannel?.postMessage({ type: 'allow', scope, token: expectedToken, authGeneration: authAuthority })
	return true
}
