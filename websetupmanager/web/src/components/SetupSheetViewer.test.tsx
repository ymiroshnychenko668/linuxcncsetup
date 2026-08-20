import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { SetupSheetViewer } from './SetupSheetViewer'
import type { Artifact, Setup } from '../domain'

const sheet: Artifact = {
  artifactId: 'a'.repeat(32), setupId: 'b'.repeat(32), role: 'setup_sheet',
  displayName: 'instructions.html', mediaType: 'text/html', byteSize: 42,
  version: 'c'.repeat(64), position: 0, primary: false, state: 'available',
  createdAt: '', updatedAt: '',
}
const setup: Setup = {
  setupId: sheet.setupId, libraryId: 'd'.repeat(32), name: 'Комплект', status: 'draft',
  revision: 1, source: 'created', artifacts: [sheet], createdAt: '', updatedAt: '',
}

describe('SetupSheetViewer', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('preflights an exact version and streams it into a scriptless originless iframe', async () => {
    const close = vi.fn()
    const fetchMock = vi.fn<typeof fetch>().mockResolvedValue(new Response(null, {
      status: 200, headers: { 'content-type': 'text/html; charset=utf-8', etag: `"${sheet.version}"` },
    }))
    vi.stubGlobal('fetch', fetchMock)
    render(<SetupSheetViewer setup={setup} artifact={sheet} onClose={close} onReplace={vi.fn()} />)
    const frame = await screen.findByTitle('Setup Sheet instructions.html')
    const [requestURL, requestInit] = fetchMock.mock.calls[0]
    expect(requestURL).toBe(`/api/v1/setups/${setup.setupId}/setup-sheet/content`)
    expect(requestInit?.method).toBe('HEAD')
    expect(requestInit?.credentials).toBe('same-origin')
    expect(requestInit?.cache).toBe('no-store')
    expect(new Headers(requestInit?.headers).get('If-Match')).toBe(`"${sheet.version}"`)
    expect(frame).toHaveAttribute('sandbox', '')
    expect(frame).toHaveAttribute('referrerpolicy', 'no-referrer')
    expect(frame).toHaveAttribute('src', `/api/v1/setups/${setup.setupId}/setup-sheet/content?version=${sheet.version}`)
    expect(frame).not.toHaveAttribute('srcdoc')
    expect(screen.getByLabelText('Масштаб HTML Setup Sheet')).toHaveTextContent('100%')
    await userEvent.click(screen.getByRole('button', { name: 'Увеличить масштаб' }))
    expect(screen.getByLabelText('Масштаб HTML Setup Sheet')).toHaveTextContent('125%')
    await userEvent.click(screen.getByRole('button', { name: 'Закрыть' }))
    expect(close).toHaveBeenCalledOnce()
  })

  it('shows a replace action instead of rendering a stale or failed HTML response', async () => {
    const replace = vi.fn()
    vi.stubGlobal('fetch', vi.fn<typeof fetch>().mockResolvedValue(new Response(
      '{"error":{"code":"VERSION_CONFLICT"}}',
      { status: 412, headers: { 'content-type': 'application/json', etag: '"changed"' } },
    )))
    render(<SetupSheetViewer setup={setup} artifact={sheet} onClose={vi.fn()} onReplace={replace} />)
    expect(await screen.findByRole('alert')).toHaveTextContent('повреждён или изменён')
    expect(screen.queryByTitle('Setup Sheet instructions.html')).not.toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Заменить документ' }))
    expect(replace).toHaveBeenCalledOnce()
  })
})
