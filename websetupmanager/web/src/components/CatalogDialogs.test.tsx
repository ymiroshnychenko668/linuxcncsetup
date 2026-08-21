import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type CatalogSetup } from '../api'
import { ComponentUploadDialog, UploadSetupDialog } from './CatalogDialogs'

const mocks = vi.hoisted(() => ({
  createCatalogSetup: vi.fn(),
  putCatalogComponent: vi.fn(),
  newIdempotencyKey: vi.fn(),
}))

vi.mock('../api', async (loadOriginal) => ({
  ...await loadOriginal<typeof import('../api')>(),
  createCatalogSetup: mocks.createCatalogSetup,
  putCatalogComponent: mocks.putCatalogComponent,
  newIdempotencyKey: mocks.newIdempotencyKey,
}))

const setup: CatalogSetup = {
  setupId: 'setup-1', name: 'Деталь', revision: 1, program: null, setupSheet: null,
  updatedAt: '2026-08-21T08:00:00Z',
}

beforeEach(() => {
  Object.values(mocks).forEach((mock) => mock.mockReset())
  let key = 0
  mocks.newIdempotencyKey.mockImplementation(() => `key-${++key}`)
})

describe('catalog mutation dialogs', () => {
  it('replays a lost create response with the same key and rotates only after request changes', async () => {
    const user = userEvent.setup()
    mocks.createCatalogSetup
      .mockRejectedValueOnce(new ApiError({ message: 'Временная ошибка.', status: 503, code: 'UNAVAILABLE', retryable: true }))
      .mockRejectedValueOnce(new ApiError({ message: 'Временная ошибка.', status: 503, code: 'UNAVAILABLE', retryable: true }))
      .mockResolvedValue(setup)
    render(<UploadSetupDialog
      folders={[]}
      destination={{ rootLabel: 'LinuxCNC', rootDisplay: '~/linuxcnc/nc_files' }}
      gcodeExtensions={['.ngc']}
      onClose={vi.fn()}
      onSaved={vi.fn()}
    />)

    const name = screen.getByRole('textbox', { name: 'Название сетапа' })
    await user.type(name, 'Деталь')
    await user.click(screen.getByRole('button', { name: 'Создать неполный сетап' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Временная ошибка')
    await user.click(screen.getByRole('button', { name: 'Создать неполный сетап' }))
    await waitFor(() => expect(mocks.createCatalogSetup).toHaveBeenCalledTimes(2))
    const firstKey = mocks.createCatalogSetup.mock.calls[0]?.[1] as unknown
    expect(mocks.createCatalogSetup.mock.calls[1]?.[1]).toBe(firstKey)

    await user.type(name, ' 2')
    await user.click(screen.getByRole('button', { name: 'Создать неполный сетап' }))
    await waitFor(() => expect(mocks.createCatalogSetup).toHaveBeenCalledTimes(3))
    expect(mocks.createCatalogSetup.mock.calls[2]?.[1]).not.toBe(firstKey)
  })

  it('replays an ambiguous component upload with the same file and key', async () => {
    const user = userEvent.setup()
    const saved = {
      ...setup,
      revision: 2,
      program: {
        artifactId: 'program-1', displayName: 'part.ngc', mediaType: 'text/x-gcode', byteSize: 4,
        version: 'v1', relativePath: 'part.ngc',
      },
    }
    mocks.putCatalogComponent
      .mockRejectedValueOnce(new ApiError({ message: 'Ответ потерян.', status: 0, code: 'NETWORK_ERROR', retryable: true }))
      .mockResolvedValue(saved)
    const onSaved = vi.fn()
    const onClose = vi.fn()
    render(<ComponentUploadDialog
      setup={setup}
      component="program"
      destination={{ rootLabel: 'LinuxCNC', rootDisplay: '~/linuxcnc/nc_files' }}
      gcodeExtensions={['.ngc']}
      onClose={onClose}
      onSaved={onSaved}
    />)

    const file = new File(['G0 X0'], 'part.ngc', { type: 'text/plain' })
    await user.upload(screen.getByLabelText('G-code программу'), file)
    fireEvent.submit(document.getElementById('catalog-component-form')!)
    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledTimes(1))
    expect(await screen.findByRole('alert')).toHaveTextContent('Ответ потерян')
    fireEvent.submit(document.getElementById('catalog-component-form')!)
    await waitFor(() => expect(onClose).toHaveBeenCalledTimes(1))
    expect(mocks.putCatalogComponent.mock.calls[0]?.[3] as unknown).toBe('key-1')
    expect(mocks.putCatalogComponent.mock.calls[1]?.[3] as unknown).toBe('key-1')
    expect(onSaved).toHaveBeenCalledWith(saved)
  })
})
