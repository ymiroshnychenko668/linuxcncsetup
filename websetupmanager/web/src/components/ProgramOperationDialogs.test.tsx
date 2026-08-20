import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { Job, Setup } from '../domain'
import { DeleteProgramDialog, MultiProgramUploadDialog } from './ProgramOperationDialogs'
import { DuplicateSetupDialog } from './SetupOperationDialogs'

const api = vi.hoisted(() => ({ deleteArtifact: vi.fn(), getSetup: vi.fn(), uploadPrograms: vi.fn() }))
vi.mock('../api', async (loadOriginal) => ({ ...await loadOriginal<typeof import('../api')>(), ...api, newIdempotencyKey: () => 'key' }))

const setup: Setup = {
  setupId: 'setup-1', libraryId: 'library-1', name: 'Part', status: 'draft', revision: 2,
  source: 'created', createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z',
  artifacts: [
    { artifactId: 'primary', setupId: 'setup-1', role: 'program', displayName: 'main.ngc', mediaType: 'text/x-gcode', byteSize: 2, version: 'v1', position: 0, primary: true, state: 'available', createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z' },
    { artifactId: 'other', setupId: 'setup-1', role: 'program', displayName: 'finish.ngc', mediaType: 'text/x-gcode', byteSize: 2, version: 'v2', position: 1, primary: false, state: 'available', createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z' },
  ],
}

const uploadJob: Job = {
  jobId: 'job-upload', kind: 'addPrograms', setupId: 'setup-1', state: 'succeeded',
  progress: { completedBytes: 4, totalBytes: 4, completedItems: 2, totalItems: 2 },
  createdAt: '2026-08-20T00:00:00Z', completedAt: '2026-08-20T00:00:01Z',
}

beforeEach(() => {
  api.deleteArtifact.mockReset().mockResolvedValue(setup)
  api.getSetup.mockReset().mockResolvedValue(setup)
  api.uploadPrograms.mockReset().mockResolvedValue({ job: uploadJob, transfer: Promise.resolve(uploadJob) })
})

describe('program operation dialogs', () => {
  it('requires an explicit replacement or explicit unassigned choice before deleting primary', async () => {
    const user = userEvent.setup()
    render(<DeleteProgramDialog setup={setup} artifact={setup.artifacts[0]} onClose={vi.fn()} onChanged={vi.fn()} onReload={vi.fn()} />)
    const remove = screen.getByRole('button', { name: 'Удалить программу' })
    expect(remove).toBeDisabled()
    await user.selectOptions(screen.getByRole('combobox', { name: 'После удаления основной программы' }), 'other')
    expect(remove).toBeEnabled()
    await user.click(remove)
    expect(api.deleteArtifact).toHaveBeenCalledWith(setup, setup.artifacts[0], 'key', {
      replacementPrimaryArtifactId: 'other', leavePrimaryUnassigned: false, confirmDeleteLastProgram: false,
    })
  })

  it('requires a separate acknowledgement before deleting the last program', async () => {
    const user = userEvent.setup()
    const only = { ...setup, artifacts: [setup.artifacts[0]] }
    render(<DeleteProgramDialog setup={only} artifact={only.artifacts[0]} onClose={vi.fn()} onChanged={vi.fn()} onReload={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Удалить программу' })).toBeDisabled()
    await user.click(screen.getByRole('checkbox', { name: /Подтверждаю удаление последней/ }))
    await user.click(screen.getByRole('button', { name: 'Удалить программу' }))
    expect(api.deleteArtifact).toHaveBeenCalledWith(only, only.artifacts[0], 'key', expect.objectContaining({ confirmDeleteLastProgram: true }))
  })

  it('confirms every basename and uploads multiple programs in one call', async () => {
    const user = userEvent.setup()
    const files = [new File(['G0'], 'Main.ngc'), new File(['G1'], 'main.ngc')]
    const onChanged = vi.fn()
    render(<MultiProgramUploadDialog setup={setup} files={files} onClose={vi.fn()} onChanged={onChanged} onReload={vi.fn()} />)
    expect(screen.getByRole('button', { name: 'Добавить атомарно' })).toBeDisabled()
    const second = screen.getByRole('textbox', { name: 'Basename main.ngc' })
    await user.clear(second)
    await user.type(second, 'finish-2.ngc')
    await user.click(screen.getByRole('button', { name: 'Добавить атомарно' }))
    const uploadCall = api.uploadPrograms.mock.calls[0] as unknown[]
    expect(uploadCall.slice(0, 3)).toEqual([setup, [
      { file: files[0], displayName: 'Main.ngc' }, { file: files[1], displayName: 'finish-2.ngc' },
    ], 'key'])
    expect((uploadCall[3] as { signal: unknown }).signal).toBeInstanceOf(AbortSignal)
		await vi.waitFor(() => expect(onChanged).toHaveBeenCalledWith(setup))
  })

	it('keeps selected files and basenames after a terminal revision conflict', async () => {
		const user = userEvent.setup()
		const conflict = { ...uploadJob, state: 'conflict' as const, errorCode: 'REVISION_CONFLICT' }
		api.uploadPrograms.mockResolvedValueOnce({ job: conflict, transfer: Promise.resolve(conflict) })
		const file = new File(['G0'], 'kept.ngc')
		const onChanged = vi.fn()
		render(<MultiProgramUploadDialog setup={setup} files={[file]} onClose={vi.fn()} onChanged={onChanged} onReload={vi.fn()} />)
		await user.click(screen.getByRole('button', { name: 'Добавить атомарно' }))
		expect(await screen.findByRole('alert')).toHaveTextContent('REVISION_CONFLICT')
		expect(screen.getByRole('textbox', { name: 'Basename kept.ngc' })).toHaveValue('kept.ngc')
		expect(onChanged).not.toHaveBeenCalled()
	})
})

describe('setup job dialogs', () => {
	it('preserves the requested duplicate name after a revision conflict', async () => {
		const user = userEvent.setup()
		const start = vi.fn().mockRejectedValue(new Error('REVISION_CONFLICT: reload and retry'))
		render(<DuplicateSetupDialog setup={setup} onClose={vi.fn()} onStart={start} onReload={vi.fn().mockResolvedValue(undefined)} />)
		const name = screen.getByRole('textbox', { name: 'Название копии' })
		await user.clear(name)
		await user.type(name, 'Моя сохранённая копия')
		await user.click(screen.getByRole('button', { name: 'Запустить дублирование' }))
		expect(await screen.findByRole('alert')).toHaveTextContent('REVISION_CONFLICT')
		expect(name).toHaveValue('Моя сохранённая копия')
	})
})
