import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import type { ImportArtifact, ImportSession, Setup } from '../domain'
import { ImportWizard } from './ImportWizard'

const api = vi.hoisted(() => ({
  startImport: vi.fn(), uploadImportArtifact: vi.fn(), commitImport: vi.fn(), cancelImport: vi.fn(),
  getImport: vi.fn(), excludeImportArtifact: vi.fn(), cancelJob: vi.fn(), getJob: vi.fn(),
	preflightImport: vi.fn(),
}))

vi.mock('../api', async (loadOriginal) => ({
  ...await loadOriginal<typeof import('../api')>(), ...api, newIdempotencyKey: () => 'key',
}))

const session: ImportSession = {
  importSessionId: 'import-1', jobId: 'job-import-1', name: 'Комплект', state: 'staging', artifacts: [], bytes: 0,
  expiresAt: '2026-08-21T00:00:00Z', createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z',
}

const created: Setup = {
  setupId: 'setup-imported', libraryId: 'library-1', name: 'Комплект', status: 'draft', revision: 1,
  source: 'imported', artifacts: [], createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z',
}

beforeEach(() => {
  api.startImport.mockReset().mockResolvedValue(session)
  api.uploadImportArtifact.mockReset().mockImplementation((_id: string, file: File, role: ImportArtifact['role']) => Promise.resolve({
    importArtifactId: `staged-${file.name}`, role, displayName: file.name, byteSize: file.size,
    bytes: file.size, state: 'staged',
  } satisfies ImportArtifact))
  api.commitImport.mockReset().mockResolvedValue(created)
  api.cancelImport.mockReset().mockResolvedValue({ ...session, state: 'cancelled' })
  api.getImport.mockReset().mockResolvedValue(session)
  api.excludeImportArtifact.mockReset().mockResolvedValue(session)
	api.preflightImport.mockReset().mockImplementation((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({
		items: items.map((item) => ({ clientId: item.clientId, displayName: item.displayName.normalize('NFC') })),
		collisions: [],
	}))
	api.getJob.mockReset().mockResolvedValue({
		jobId: 'job-import-1', kind: 'import', state: 'running', progress: { completedBytes: 0, completedItems: 0 },
		createdAt: '2026-08-20T00:00:00Z',
	})
	api.cancelJob.mockReset().mockResolvedValue({
		jobId: 'job-import-1', kind: 'import', state: 'cancelled', progress: { completedBytes: 0, completedItems: 0 },
		createdAt: '2026-08-20T00:00:00Z', completedAt: '2026-08-20T00:00:01Z',
	})
})

describe('ImportWizard', () => {
  it('stages several files and publishes one setup atomically with the chosen primary program', async () => {
    const user = userEvent.setup()
    const onImported = vi.fn()
    render(<ImportWizard
      capabilities={{ libraryAlias: 'Library', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} }}
      onClose={vi.fn()} onImported={onImported}
    />)
    await user.type(screen.getByRole('textbox', { name: 'Название сетапа' }), 'Комплект')
    const picker = document.querySelector<HTMLInputElement>('input[type="file"]')!
    const rough = new File(['G0 X0'], 'rough.ngc', { type: 'text/plain' })
    const finish = new File(['G1 X1'], 'finish.ngc', { type: 'text/plain' })
    const sheet = new File(['%PDF'], 'setup.pdf', { type: 'application/pdf' })
    await user.upload(picker, [rough, finish, sheet])
    await user.click(screen.getByRole('button', { name: 'Проверить роли' }))
    const radios = screen.getAllByRole('radio', { name: 'Основная' })
    await user.click(radios[1])
    await user.click(screen.getByRole('button', { name: 'Опубликовать комплект' }))
    await waitFor(() => expect(api.uploadImportArtifact).toHaveBeenCalledTimes(3))
    expect(api.commitImport).toHaveBeenCalledWith(
      'import-1', expect.arrayContaining([expect.objectContaining({ importArtifactId: 'staged-finish.ngc' })]),
      'staged-finish.ngc', false, 'key',
    )
    expect(onImported).toHaveBeenCalledWith(created)
    const firstUploadCall = api.uploadImportArtifact.mock.calls[0] as unknown[]
    expect(firstUploadCall.slice(0, 5)).toEqual(['import-1', rough, 'program', 'rough.ngc', 'key'])
    expect((firstUploadCall[5] as { signal: unknown }).signal).toBeInstanceOf(AbortSignal)
  })

  it('rejects more than one Setup Sheet before creating a staging session', async () => {
    const user = userEvent.setup()
    render(<ImportWizard
      capabilities={{ libraryAlias: 'Library', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} }}
      onClose={vi.fn()} onImported={vi.fn()}
    />)
    await user.type(screen.getByRole('textbox', { name: 'Название сетапа' }), 'Документы')
    await user.upload(document.querySelector<HTMLInputElement>('input[type="file"]')!, [
      new File(['G0'], 'main.ngc'), new File(['a'], 'one.pdf'), new File(['b'], 'two.html'),
    ])
    await user.click(screen.getByRole('button', { name: 'Проверить роли' }))
    expect(screen.getByRole('alert')).toHaveTextContent('только одна Setup Sheet')
    expect(screen.getByRole('button', { name: 'Опубликовать комплект' })).toBeDisabled()
    expect(api.startImport).not.toHaveBeenCalled()
  })

  it('keeps Cancel active during upload, aborts the request and cancels the staging session', async () => {
    const user = userEvent.setup()
    const onClose = vi.fn()
    api.uploadImportArtifact.mockImplementation((_session: string, _file: File, _role: string, _name: string, _key: string, options: { signal: AbortSignal; onProgress: (loaded: number) => void }) => new Promise((_resolve, reject) => {
      options.onProgress(1)
      options.signal.addEventListener('abort', () => reject(new DOMException('cancelled', 'AbortError')), { once: true })
    }))
    render(<ImportWizard capabilities={{ libraryAlias: 'Library', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} }} onClose={onClose} onImported={vi.fn()} />)
    await user.type(screen.getByRole('textbox', { name: 'Название сетапа' }), 'Cancel me')
    await user.upload(document.querySelector<HTMLInputElement>('input[type="file"]')!, new File(['G0'], 'main.ngc'))
    await user.click(screen.getByRole('button', { name: 'Проверить роли' }))
    await user.click(screen.getByRole('button', { name: 'Опубликовать комплект' }))
    await waitFor(() => expect(api.uploadImportArtifact).toHaveBeenCalled())
    expect(await screen.findByText('1 Б из 2 Б')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Отменить загрузку' }))
		await waitFor(() => expect(api.cancelJob).toHaveBeenCalledWith('job-import-1', 'key'))
		expect(api.cancelImport).not.toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
  })

  it('offers an atomic partial draft after one file fails', async () => {
    const user = userEvent.setup()
    const staged: ImportArtifact = { importArtifactId: 'staged-good.ngc', role: 'program', displayName: 'good.ngc', byteSize: 2, bytes: 2, state: 'staged' }
    const failed: ImportArtifact = { importArtifactId: 'failed-bad.ngc', role: 'program', displayName: 'bad.ngc', byteSize: 0, bytes: 0, state: 'failed', errorCode: 'UPLOAD_FAILED' }
    api.uploadImportArtifact.mockResolvedValueOnce(staged).mockRejectedValueOnce(new Error('disk unavailable'))
    api.getImport.mockResolvedValue({ ...session, artifacts: [staged, failed], bytes: 2 })
    api.excludeImportArtifact.mockResolvedValue({ ...session, artifacts: [staged, { ...failed, state: 'excluded' }], bytes: 2 })
    render(<ImportWizard capabilities={{ libraryAlias: 'Library', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} }} onClose={vi.fn()} onImported={vi.fn()} />)
    await user.type(screen.getByRole('textbox', { name: 'Название сетапа' }), 'Partial')
    await user.upload(document.querySelector<HTMLInputElement>('input[type="file"]')!, [new File(['G0'], 'good.ngc'), new File(['G1'], 'bad.ngc')])
    await user.click(screen.getByRole('button', { name: 'Проверить роли' }))
    await user.click(screen.getByRole('button', { name: 'Опубликовать комплект' }))
    expect(await screen.findByRole('button', { name: 'Сохранить staged как draft' })).toBeEnabled()
    await user.click(screen.getByRole('button', { name: 'Сохранить staged как draft' }))
    await waitFor(() => expect(api.commitImport).toHaveBeenCalledWith('import-1', [staged], 'staged-good.ngc', true, 'key'))
  })

  it('detects case-insensitive confirmed basename conflicts and preserves editable resolution', async () => {
    const user = userEvent.setup()
		api.preflightImport.mockImplementationOnce((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({
			items, collisions: [{ clientIds: items.map((item) => item.clientId) }],
		})).mockImplementationOnce((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({ items, collisions: [] }))
    render(<ImportWizard capabilities={{ libraryAlias: 'Library', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} }} onClose={vi.fn()} onImported={vi.fn()} />)
    await user.type(screen.getByRole('textbox', { name: 'Название сетапа' }), 'Names')
    await user.upload(document.querySelector<HTMLInputElement>('input[type="file"]')!, [new File(['G0'], 'Main.ngc'), new File(['G1'], 'main.ngc')])
    await user.click(screen.getByRole('button', { name: 'Проверить роли' }))
    expect(screen.getByRole('alert')).toHaveTextContent('совпадающие Unicode basename')
    const second = screen.getByRole('textbox', { name: 'Basename main.ngc' })
    await user.clear(second)
    await user.type(second, 'finish.ngc')
		expect(second).toHaveValue('finish.ngc')
		await user.click(screen.getByRole('button', { name: 'Проверить имена повторно' }))
		await waitFor(() => expect(screen.queryByText(/Backend обнаружил совпадающие Unicode basename/)).not.toBeInTheDocument())
    expect(screen.getByRole('button', { name: 'Опубликовать комплект' })).toBeEnabled()
  })

	it.each([
		['full fold ß/SS', 'Straße.ngc', 'STRASSE.ngc'],
		['final sigma fold', 'part-Σ.ngc', 'part-ς.ngc'],
	])('uses backend Unicode collision groups for %s and permits rename', async (_label, firstName, secondName) => {
		const user = userEvent.setup()
		api.preflightImport.mockImplementationOnce((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({
			items, collisions: [{ clientIds: items.map((item) => item.clientId) }],
		})).mockImplementationOnce((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({ items, collisions: [] }))
		render(<ImportWizard capabilities={{ libraryAlias: 'Library', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} }} onClose={vi.fn()} onImported={vi.fn()} />)
		await user.type(screen.getByRole('textbox', { name: 'Название сетапа' }), 'Unicode')
		await user.upload(document.querySelector<HTMLInputElement>('input[type="file"]')!, [new File(['G0'], firstName), new File(['G1'], secondName)])
		await user.click(screen.getByRole('button', { name: 'Проверить роли' }))
		expect(await screen.findByRole('alert')).toHaveTextContent('Backend обнаружил совпадающие Unicode basename')
		expect(screen.getAllByRole('button', { name: 'Оставить только этот файл' })).toHaveLength(2)
		const second = screen.getByRole('textbox', { name: `Basename ${secondName}` })
		await user.clear(second)
		await user.type(second, 'unique.ngc')
		await user.click(screen.getByRole('button', { name: 'Проверить имена повторно' }))
		await waitFor(() => expect(screen.getByRole('button', { name: 'Опубликовать комплект' })).toBeEnabled())
	})

	it('keeps unstaged files editable when the server reports a collision during streaming', async () => {
		const user = userEvent.setup()
		const staged: ImportArtifact = {
			importArtifactId: 'staged-first', role: 'program', displayName: 'first.ngc',
			byteSize: 2, bytes: 2, state: 'staged',
		}
		const failed: ImportArtifact = {
			importArtifactId: 'failed-second', role: 'program', displayName: 'second.ngc',
			byteSize: 0, bytes: 0, state: 'failed', errorCode: 'NAME_CONFLICT',
		}
		api.preflightImport
			.mockImplementationOnce((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({ items, collisions: [] }))
			.mockImplementationOnce((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({
				items, collisions: [{ clientIds: items.map((item) => item.clientId) }],
			}))
			.mockImplementationOnce((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({ items, collisions: [] }))
		api.uploadImportArtifact.mockResolvedValueOnce(staged).mockRejectedValueOnce(new ApiError({
			message: 'confirmed names collide', status: 409, code: 'NAME_CONFLICT',
		}))
		api.getImport.mockResolvedValue({ ...session, artifacts: [staged, failed], bytes: 2 })

		render(<ImportWizard capabilities={{ libraryAlias: 'Library', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} }} onClose={vi.fn()} onImported={vi.fn()} />)
		await user.type(screen.getByRole('textbox', { name: 'Название сетапа' }), 'Late collision')
		await user.upload(document.querySelector<HTMLInputElement>('input[type="file"]')!, [
			new File(['G0'], 'first.ngc'), new File(['G1'], 'second.ngc'),
		])
		await user.click(screen.getByRole('button', { name: 'Проверить роли' }))
		await user.click(screen.getByRole('button', { name: 'Опубликовать комплект' }))

		expect(await screen.findByText(/Backend обнаружил совпадающие Unicode basename/)).toBeInTheDocument()
		const first = screen.getByRole('textbox', { name: 'Basename first.ngc' })
		const second = screen.getByRole('textbox', { name: 'Basename second.ngc' })
		await waitFor(() => expect(first).toBeDisabled())
		expect(second).toBeEnabled()
		await user.clear(second)
		await user.type(second, 'renamed.ngc')
		await user.click(screen.getByRole('button', { name: 'Проверить имена повторно' }))
		await waitFor(() => expect(screen.getByRole('button', { name: 'Повторить загрузку' })).toBeEnabled())
	})

	it.each(['keep-one', 'exclude'] as const)('resolves a server collision with the %s action before streaming', async (action) => {
		const user = userEvent.setup()
		api.preflightImport
			.mockImplementationOnce((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({
				items, collisions: [{ clientIds: items.map((item) => item.clientId) }],
			}))
			.mockImplementationOnce((items: Array<{ clientId: string; displayName: string }>) => Promise.resolve({ items, collisions: [] }))
		render(<ImportWizard capabilities={{ libraryAlias: 'Library', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} }} onClose={vi.fn()} onImported={vi.fn()} />)
		await user.type(screen.getByRole('textbox', { name: 'Название сетапа' }), 'Resolve')
		await user.upload(document.querySelector<HTMLInputElement>('input[type="file"]')!, [
			new File(['G0'], 'Straße.ngc'), new File(['G1'], 'STRASSE.ngc'),
		])
		await user.click(screen.getByRole('button', { name: 'Проверить роли' }))
		await screen.findByText(/Backend обнаружил совпадающие Unicode basename/)
		if (action === 'keep-one') {
			await user.click(screen.getAllByRole('button', { name: 'Оставить только этот файл' })[0])
		} else {
			await user.click(screen.getAllByRole('button', { name: 'Исключить' })[1])
		}
		const included = screen.getAllByRole('checkbox')
		expect(included[0]).toBeChecked()
		expect(included[1]).not.toBeChecked()
		await user.click(screen.getByRole('button', { name: 'Проверить имена повторно' }))
		await waitFor(() => expect(screen.getByRole('button', { name: 'Опубликовать комплект' })).toBeEnabled())
		expect(api.startImport).not.toHaveBeenCalled()
	})
})
