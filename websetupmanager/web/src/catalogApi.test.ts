import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  catalogContentURL,
  clearApiSession,
  createCatalogFolder,
  createCatalogSetup,
  deleteCatalogComponent,
  deleteCatalogFolder,
  deleteCatalogSetup,
  getCatalog,
  putCatalogComponent,
  setCsrfToken,
  updateCatalogFolder,
  updateCatalogSetup,
  type CatalogFolder,
  type CatalogSetup,
} from './api'

const folderPayload = {
  folderId: 'folder/encoded', name: 'Заказы', relativePath: 'Заказы', revision: 2,
}

const setupPayload = {
  setupId: 'setup/encoded', folderId: 'folder/encoded', name: 'Кронштейн', description: 'Операция 20',
  revision: 3,
  program: {
    artifactId: 'program-1', displayName: 'деталь №1.ngc', mediaType: 'text/x-gcode', byteSize: 12,
    version: 'version-1', relativePath: 'Заказы/деталь №1.ngc', storageKey: 'must-not-leak',
  },
  setupSheet: null,
  updatedAt: '2026-08-21T08:00:00Z',
  absolutePath: '/srv/linuxcnc/nc_files/Заказы/деталь №1.ngc',
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

function jsonRequestBody(call: [string, RequestInit] | undefined): unknown {
  const body = call?.[1].body
  if (typeof body !== 'string') throw new Error('Expected a JSON string request body.')
  return JSON.parse(body) as unknown
}

afterEach(() => {
  clearApiSession()
  vi.unstubAllGlobals()
})

describe('catalog API contract', () => {
  it('normalizes the bounded catalog and drops physical storage fields', async () => {
    const fetchMock = vi.fn().mockResolvedValue(jsonResponse({
      destination: { rootLabel: 'LinuxCNC PROGRAM_PREFIX', rootDisplay: '~/linuxcnc/nc_files', absolutePath: '/srv/private' },
      generation: '9', folders: [folderPayload], setups: [setupPayload],
    }))
    vi.stubGlobal('fetch', fetchMock)

    const catalog = await getCatalog()
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/v1/catalog')
    expect(catalog.setups[0]?.program).toEqual({
      artifactId: 'program-1', displayName: 'деталь №1.ngc', mediaType: 'text/x-gcode', byteSize: 12,
      version: 'version-1', relativePath: 'Заказы/деталь №1.ngc',
    })
    expect(JSON.stringify(catalog)).not.toContain('/srv/private')
    expect(JSON.stringify(catalog)).not.toContain('must-not-leak')
  })

  it('sends revisions, nullable parents, JSON and idempotency on catalog mutations', async () => {
    const updatedFolder = { ...folderPayload, name: 'Срочные', revision: 3 }
    const updatedSetup = { ...setupPayload, name: 'Кронштейн 2', revision: 4 }
    const withoutProgram = { ...updatedSetup, revision: 5, program: null }
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(jsonResponse(folderPayload))
      .mockResolvedValueOnce(jsonResponse(updatedFolder))
      .mockResolvedValueOnce(jsonResponse(setupPayload))
      .mockResolvedValueOnce(jsonResponse(updatedSetup))
      .mockResolvedValueOnce(jsonResponse(withoutProgram))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    setCsrfToken('csrf-token')

    const folder = await createCatalogFolder(undefined, 'Заказы', 'folder-create-key')
    await updateCatalogFolder(folder, { name: 'Срочные', parentFolderId: null }, 'folder-update-key')
    const setup = await createCatalogSetup({ folderId: folder.folderId, name: 'Кронштейн' }, 'setup-create-key')
    const renamed = await updateCatalogSetup(setup, { name: 'Кронштейн 2', folderId: null }, 'setup-update-key')
    await deleteCatalogComponent(renamed, 'program', 'component-delete-key')
    await deleteCatalogFolder(updatedFolder as CatalogFolder, 'folder-delete-key')
    await deleteCatalogSetup(withoutProgram as CatalogSetup, 'setup-delete-key')

    const calls = fetchMock.mock.calls as Array<[string, RequestInit]>
    expect(calls.map(([path]) => path)).toEqual([
      '/api/v1/catalog/folders',
      '/api/v1/catalog/folders/folder%2Fencoded',
      '/api/v1/catalog/setups',
      '/api/v1/catalog/setups/setup%2Fencoded',
      '/api/v1/catalog/setups/setup%2Fencoded/program?expectedRevision=4',
      '/api/v1/catalog/folders/folder%2Fencoded?expectedRevision=3',
      '/api/v1/catalog/setups/setup%2Fencoded?expectedRevision=5',
    ])
    expect(jsonRequestBody(calls[0])).toEqual({ parentFolderId: null, name: 'Заказы' })
    expect(jsonRequestBody(calls[1])).toEqual({ expectedRevision: 2, name: 'Срочные', parentFolderId: null })
    expect(jsonRequestBody(calls[2])).toEqual({ folderId: 'folder/encoded', name: 'Кронштейн', description: '' })
    expect(jsonRequestBody(calls[3])).toEqual({ expectedRevision: 3, name: 'Кронштейн 2', folderId: null })
    expect(new Headers(calls[4]?.[1].headers).get('Idempotency-Key')).toBe('component-delete-key')
    expect(calls.every(([, init]) => new Headers(init.headers).get('X-CSRF-Token') === 'csrf-token')).toBe(true)
  })

  it('uploads one component as a raw body with an encoded Unicode filename', async () => {
    class FakeXMLHttpRequest extends EventTarget {
      static latest: FakeXMLHttpRequest
      readonly upload = new EventTarget()
      readonly headers = new Map<string, string>()
      status = 0
      responseText = ''
      withCredentials = false
      method = ''
      url = ''
      body?: XMLHttpRequestBodyInit | null

      constructor() { super(); FakeXMLHttpRequest.latest = this }
      open(method: string, url: string): void { this.method = method; this.url = url }
      send(body?: XMLHttpRequestBodyInit | null): void { this.body = body }
      abort(): void { this.dispatchEvent(new Event('abort')) }
      setRequestHeader(name: string, value: string): void { this.headers.set(name, value) }
      getResponseHeader(): string | null { return null }
      respond(status: number, body: unknown): void {
        this.status = status
        this.responseText = JSON.stringify(body)
        this.dispatchEvent(new Event('load'))
      }
    }
    vi.stubGlobal('XMLHttpRequest', FakeXMLHttpRequest)
    setCsrfToken('csrf-token')
    const file = new File(['G0 X0'], 'деталь №1.ngc', { type: 'text/x-gcode' })
    const pending = putCatalogComponent(setupPayload as CatalogSetup, 'program', file, 'upload-key')
    const request = FakeXMLHttpRequest.latest

    expect(request.method).toBe('PUT')
    expect(request.url).toBe('/api/v1/catalog/setups/setup%2Fencoded/program?expectedRevision=3')
    expect(request.body).toBe(file)
    expect(request.withCredentials).toBe(true)
    expect(request.headers.get('Content-Type')).toBe('text/x-gcode')
    expect(request.headers.get('X-File-Name')).toBe(encodeURIComponent(file.name))
    expect(request.headers.get('Idempotency-Key')).toBe('upload-key')
    request.respond(200, { ...setupPayload, revision: 4 })
    await expect(pending).resolves.toMatchObject({ setupId: 'setup/encoded', revision: 4 })
    expect(catalogContentURL('setup/encoded', 'setup-sheet')).toBe('/api/v1/catalog/setups/setup%2Fencoded/setup-sheet/content')
  })

  it('rejects malformed catalog artifacts before exposing them to the workbench', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(jsonResponse({
      destination: { rootLabel: 'LinuxCNC', rootDisplay: '~/linuxcnc/nc_files' }, generation: '1',
      folders: [], setups: [{ ...setupPayload, program: { ...setupPayload.program, version: '' } }],
    })))
    await expect(getCatalog()).rejects.toMatchObject({ code: 'INVALID_RESPONSE' })
  })
})
