import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, it, vi } from 'vitest'
import type { Setup } from '../domain'
import { CreateSetupDialog } from './CreateSetupDialog'

const api = vi.hoisted(() => ({ checkSetupName: vi.fn(), createSetup: vi.fn() }))
vi.mock('../api', async (loadOriginal) => ({
  ...await loadOriginal<typeof import('../api')>(),
  ...api,
  newIdempotencyKey: () => 'create-key',
}))

const created: Setup = {
  setupId: 'setup-new', libraryId: 'library-1', name: 'Σ', status: 'draft', revision: 1,
  source: 'created', artifacts: [], createdAt: '2026-08-20T00:00:00Z', updatedAt: '2026-08-20T00:00:00Z',
}

beforeEach(() => {
  api.checkSetupName.mockReset().mockResolvedValue({ setupId: 'setup-existing', name: 'ς' })
  api.createSetup.mockReset().mockResolvedValue(created)
})

it('requires acknowledgement for the backend full-Unicode-fold match', async () => {
  const user = userEvent.setup()
  const onCreated = vi.fn()
  render(<CreateSetupDialog onClose={vi.fn()} onCreated={onCreated} />)

  await user.type(screen.getByRole('textbox', { name: /Название/ }), 'Σ')
  expect(await screen.findByText(/Уже существует сетап «ς»/)).toBeInTheDocument()
  await user.click(screen.getByRole('button', { name: 'Создать черновик' }))
  expect(api.createSetup).not.toHaveBeenCalled()
  expect(screen.getByText('Подтвердите создание отдельного сетапа с совпадающим названием.')).toBeInTheDocument()

  await user.click(screen.getByRole('checkbox', { name: /Создать отдельный сетап/ }))
  await user.click(screen.getByRole('button', { name: 'Создать черновик' }))
  expect(api.createSetup).toHaveBeenCalledWith('Σ', '', 'create-key')
  expect(onCreated).toHaveBeenCalledWith(created)
})
