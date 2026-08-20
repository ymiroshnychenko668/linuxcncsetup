import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from './api'
import type { Setup, SetupSummary } from './domain'
import { App } from './App'

const mocks = vi.hoisted(() => ({
  getAuthSession: vi.fn(), login: vi.fn(), logout: vi.fn(), setUnauthorizedHandler: vi.fn(), clearApiSession: vi.fn(),
  getCapabilities: vi.fn(), listSetups: vi.fn(), getCurrentSetup: vi.fn(), getSetup: vi.fn(),
  checkSetupName: vi.fn(), createSetup: vi.fn(), updateSetup: vi.fn(), setCurrentSetup: vi.fn(), clearCurrentSetup: vi.fn(),
  setupAction: vi.fn(), waitForJob: vi.fn(), uploadProgram: vi.fn(), uploadSetupSheet: vi.fn(),
  mutateProgram: vi.fn(), deleteArtifact: vi.fn(), permanentDelete: vi.fn(), apiRequest: vi.fn(),
  startImport: vi.fn(), uploadImportArtifact: vi.fn(), commitImport: vi.fn(), cancelImport: vi.fn(),
  getImport: vi.fn(), excludeImportArtifact: vi.fn(), listRecentSetups: vi.fn(), touchRecentSetup: vi.fn(),
  deleteRecentSetup: vi.fn(), clearRecentSetups: vi.fn(), getUIState: vi.fn(), putUIState: vi.fn(),
  getReadiness: vi.fn(), uploadPrograms: vi.fn(),
  cancelJob: vi.fn(), listJobs: vi.fn(), listActiveJobs: vi.fn(),
}))

vi.mock('./api', async (loadOriginal) => ({
  ...await loadOriginal<typeof import('./api')>(),
  ...mocks,
  newIdempotencyKey: () => 'test-idempotency-key',
}))

vi.mock('./components/GCodePreview', () => ({
  GCodePreview: ({ artifact }: { artifact: { displayName: string } }) => <div data-testid="gcode-preview">Preview: {artifact.displayName}</div>,
}))
vi.mock('./components/SetupSheetViewer', () => ({
  SetupSheetViewer: ({ artifact, onClose }: { artifact: { displayName: string }; onClose: () => void }) => <div role="dialog" aria-label="Setup Sheet viewer"><span>{artifact.displayName}</span><button type="button" onClick={onClose}>Закрыть</button></div>,
}))

const baseSetup: Setup = {
  setupId: 'setup-1', libraryId: 'library-1', name: 'Корпус насоса', description: 'Операция 20',
  status: 'ready', revision: 3, source: 'created', notReadyReasons: [],
  createdAt: '2026-08-20T08:00:00Z', updatedAt: '2026-08-20T09:00:00Z',
  artifacts: [{
    artifactId: 'program-1', setupId: 'setup-1', role: 'program', displayName: 'finish.ngc',
    mediaType: 'text/x-gcode', byteSize: 120, version: 'version-1', position: 0, primary: true,
    state: 'available', createdAt: '2026-08-20T08:00:00Z', updatedAt: '2026-08-20T09:00:00Z',
  }],
}

const summary: SetupSummary = {
  setupId: baseSetup.setupId, name: baseSetup.name, description: baseSetup.description,
  status: baseSetup.status, revision: baseSetup.revision, programCount: 1,
  hasSetupSheet: false, isCurrent: false, createdAt: baseSetup.createdAt, updatedAt: baseSetup.updatedAt,
}

beforeEach(() => {
  mocks.getAuthSession.mockResolvedValue({
    authenticated: true, loginRequired: false, user: null, csrfToken: 'local-session-token',
  })
  mocks.login.mockResolvedValue({
    authenticated: true, loginRequired: true, user: { username: 'operator' }, csrfToken: 'remote-session-token',
  })
  mocks.logout.mockResolvedValue(undefined)
  mocks.getCapabilities.mockResolvedValue({
    libraryId: 'library-1', libraryAlias: 'Производственные сетапы', csrfToken: 'token',
    gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {},
  })
  mocks.listSetups.mockResolvedValue({ items: [summary] })
  mocks.getCurrentSetup.mockResolvedValue(null)
  mocks.getSetup.mockResolvedValue(baseSetup)
  mocks.checkSetupName.mockResolvedValue(undefined)
  mocks.setCurrentSetup.mockResolvedValue({ libraryId: 'library-1', setupId: 'setup-1', revisionSelected: 3, selectedAt: '2026-08-20T09:00:00Z' })
  mocks.updateSetup.mockResolvedValue(baseSetup)
  mocks.listRecentSetups.mockResolvedValue([])
  mocks.touchRecentSetup.mockResolvedValue(undefined)
  mocks.deleteRecentSetup.mockResolvedValue(undefined)
  mocks.clearRecentSetups.mockResolvedValue(undefined)
  mocks.getUIState.mockResolvedValue({ clientId: 'test-client', screen: 'library', filters: {}, view: {} })
  mocks.putUIState.mockResolvedValue({ clientId: 'test-client', screen: 'library', filters: {}, view: {} })
  mocks.getReadiness.mockResolvedValue({ ok: true })
	mocks.listJobs.mockResolvedValue([])
	mocks.listActiveJobs.mockResolvedValue([])
  Object.values(mocks).forEach((mock) => mock.mockClear())
})

afterEach(() => vi.unstubAllGlobals())

describe('App', () => {
  it('offers a retry when the local Backend is unavailable', async () => {
    const user = userEvent.setup()
    mocks.getCapabilities.mockRejectedValueOnce(new ApiError({
      message: 'Локальный сервис не отвечает.', status: 0, code: 'NETWORK_ERROR', retryable: true,
    })).mockResolvedValueOnce({
      libraryId: 'library-1', libraryAlias: 'После reconnect', csrfToken: 'token',
      gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {},
    })
    render(<App />)
    expect(await screen.findByRole('heading', { name: 'Локальный Backend недоступен' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Повторить подключение' }))
    expect(await screen.findByRole('heading', { name: 'После reconnect' })).toBeInTheDocument()
    expect(mocks.getCapabilities).toHaveBeenCalledTimes(2)
  })

  it('checks the session before capabilities and signs in from an accessible login view', async () => {
    mocks.getAuthSession.mockResolvedValue({
      authenticated: false, loginRequired: true, user: null,
    })
    const user = userEvent.setup()
    render(<App />)

    expect(await screen.findByRole('heading', { name: 'Вход в систему' })).toBeInTheDocument()
    expect(mocks.getCapabilities).not.toHaveBeenCalled()
    expect(screen.getByRole('textbox', { name: 'Имя пользователя' })).toHaveFocus()
    await user.type(screen.getByRole('textbox', { name: 'Имя пользователя' }), 'operator')
    await user.type(screen.getByLabelText('Пароль'), 'secret')
    await user.click(screen.getByRole('checkbox', { name: /Запомнить меня/ }))
    await user.click(screen.getByRole('button', { name: 'Открыть библиотеку сетапов' }))

    expect(await screen.findByRole('heading', { name: 'Производственные сетапы' })).toBeInTheDocument()
    expect(mocks.login).toHaveBeenCalledWith('operator', 'secret', true)
    expect(screen.getByText('operator')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Выйти' })).toBeInTheDocument()
    await waitFor(() => expect(document.getElementById('main-content')).toHaveFocus())
  })

  it('unmounts the workspace and focuses login when the API reports session expiry', async () => {
    mocks.getAuthSession.mockResolvedValue({
      authenticated: true, loginRequired: true,
      user: { username: 'operator' }, csrfToken: 'remote-token',
    })
    render(<App />)
    expect(await screen.findByRole('heading', { name: 'Производственные сетапы' })).toBeInTheDocument()
    const handler = mocks.setUnauthorizedHandler.mock.calls
      .map(([candidate]) => candidate as (() => void) | undefined)
      .find((candidate) => typeof candidate === 'function')
    expect(handler).toBeTypeOf('function')

    act(() => handler?.())

    expect(await screen.findByRole('status')).toHaveTextContent('Сессия истекла')
    expect(screen.queryByRole('heading', { name: 'Производственные сетапы' })).not.toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Имя пользователя' })).toHaveFocus()
  })

  it('disables logout while pending and announces a successful logout', async () => {
    let resolveLogout!: () => void
    mocks.getAuthSession.mockResolvedValue({
      authenticated: true, loginRequired: true,
      user: { username: 'operator' }, csrfToken: 'remote-token',
    })
    mocks.logout.mockReturnValue(new Promise<void>((resolve) => { resolveLogout = resolve }))
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Производственные сетапы' })

    await user.click(screen.getByRole('button', { name: 'Выйти' }))
    expect(screen.getByRole('button', { name: 'Выходим…' })).toBeDisabled()
    act(() => resolveLogout())

    expect(await screen.findByRole('status')).toHaveTextContent('Вы вышли')
    expect(screen.getByRole('textbox', { name: 'Имя пользователя' })).toHaveFocus()
    expect(mocks.logout).toHaveBeenCalledTimes(1)
  })

  it('keeps the loaded workspace and logout focus when logout fails', async () => {
    mocks.getAuthSession.mockResolvedValue({
      authenticated: true, loginRequired: true,
      user: { username: 'operator' }, csrfToken: 'remote-token',
    })
    mocks.logout.mockRejectedValue(new ApiError({
      message: 'Сервис временно недоступен.', status: 0, code: 'NETWORK_ERROR', retryable: true,
    }))
    const user = userEvent.setup()
    render(<App />)
    await screen.findByRole('heading', { name: 'Производственные сетапы' })
    const logoutButton = screen.getByRole('button', { name: 'Выйти' })
    await user.click(logoutButton)

    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось выйти')
    expect(screen.getByRole('heading', { name: 'Производственные сетапы' })).toBeInTheDocument()
    expect(logoutButton).toBeEnabled()
    expect(logoutButton).toHaveFocus()
  })

  it('loads the pinned current area and setup library without exposing a file browser', async () => {
    render(<App />)
    expect(screen.getByRole('heading', { name: 'Проверяем защищённую сессию' })).toBeInTheDocument()
    expect(await screen.findByRole('heading', { name: 'Производственные сетапы' })).toBeInTheDocument()
    expect(await screen.findByRole('heading', { name: 'Не выбран' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: baseSetup.name })).toBeInTheDocument()
    expect(screen.queryByText(/файловое дерево|абсолютн.*путь/i)).not.toBeInTheDocument()
  })

  it('applies search and filters before using the opaque load-more cursor', async () => {
    const user = userEvent.setup()
    mocks.listSetups.mockResolvedValueOnce({ items: [summary], nextCursor: 'opaque-next' })
      .mockResolvedValue({ items: [summary], nextCursor: 'opaque-next' })
    render(<App />)
    await screen.findByRole('heading', { name: baseSetup.name })
    await user.type(screen.getByRole('searchbox'), 'finish')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Статус' }), 'ready')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Setup Sheet' }), 'no')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Текущий' }), 'no')
    await user.selectOptions(screen.getByRole('combobox', { name: 'Сортировка' }), 'name_asc')
    await user.click(screen.getByRole('button', { name: 'Найти' }))
    await waitFor(() => expect(mocks.listSetups).toHaveBeenCalledWith(expect.objectContaining({
      query: 'finish', statuses: ['ready'], hasSetupSheet: false, current: false, sort: 'name_asc',
    }), expect.any(AbortSignal)))
    await user.click(screen.getByRole('button', { name: 'Показать ещё' }))
    expect(mocks.listSetups).toHaveBeenLastCalledWith(expect.objectContaining({ cursor: 'opaque-next' }))
  })

  it('opens a card with the keyboard and preserves the selected program preview', async () => {
    const user = userEvent.setup()
    render(<App />)
    const open = await screen.findByRole('button', { name: `Открыть сетап ${baseSetup.name}` })
    open.focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('heading', { name: baseSetup.name, level: 1 })).toBeInTheDocument()
    expect(screen.getByTestId('gcode-preview')).toHaveTextContent('finish.ngc')
    expect(screen.getByText(/не проверяет станок, оснастку, траекторию/i)).toBeInTheDocument()
  })

  it('creates a draft from a focus-managed dialog', async () => {
    const user = userEvent.setup()
    const created = { ...baseSetup, setupId: 'new-setup', name: 'Новая деталь', status: 'draft' as const, revision: 1, artifacts: [] }
    mocks.createSetup.mockResolvedValue(created)
    render(<App />)
    await user.click(await screen.findByRole('button', { name: 'Создать сетап' }))
    const name = screen.getByRole('textbox', { name: /Название/ })
    expect(name).toHaveFocus()
    await user.type(name, created.name)
    await user.click(screen.getByRole('button', { name: 'Создать черновик' }))
    expect(mocks.createSetup).toHaveBeenCalledWith(created.name, '', 'test-idempotency-key')
    expect(await screen.findByRole('heading', { name: created.name, level: 1 })).toBeInTheDocument()
  })

  it('warns about an exact existing display name before allowing a distinct setup', async () => {
    const user = userEvent.setup()
    const duplicate = { ...baseSetup, setupId: 'another-id', status: 'draft' as const, revision: 1, artifacts: [] }
    mocks.createSetup.mockResolvedValue(duplicate)
    mocks.checkSetupName.mockResolvedValue({ setupId: baseSetup.setupId, name: baseSetup.name })
    render(<App />)
    await user.click(await screen.findByRole('button', { name: 'Создать сетап' }))
    await user.type(screen.getByRole('textbox', { name: /Название/ }), baseSetup.name)
    expect(await screen.findByText(/Уже существует сетап «Корпус насоса»/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Создать черновик' }))
    expect(mocks.createSetup).not.toHaveBeenCalled()
    await user.click(screen.getByRole('checkbox', { name: /Создать отдельный сетап/ }))
    await user.click(screen.getByRole('button', { name: 'Создать черновик' }))
    await waitFor(() => expect(mocks.createSetup).toHaveBeenCalledWith(baseSetup.name, '', 'test-idempotency-key'))
  })

  it('summarizes and resets a no-match query without implying data was deleted', async () => {
    const user = userEvent.setup()
    mocks.listSetups.mockResolvedValue({ items: [] })
    render(<App />)
    const search = await screen.findByRole('searchbox')
    await user.type(search, 'несуществующий')
    await user.click(screen.getByRole('button', { name: 'Найти' }))
    expect(await screen.findByText(/Активные условия: запрос «несуществующий»/)).toBeInTheDocument()
    expect(screen.getByText(/существующие сетапы не были удалены/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Сбросить запрос и фильтры' }))
    expect(search).toHaveValue('')
  })

  it('preserves a failed current selection and offers an explicit retry', async () => {
    const user = userEvent.setup()
    mocks.getCurrentSetup.mockResolvedValue({
      libraryId: 'library-1', setupId: baseSetup.setupId,
      revisionSelected: baseSetup.revision, selectedAt: baseSetup.updatedAt,
    })
    mocks.getSetup.mockRejectedValueOnce(new ApiError({
      message: 'Карточка временно недоступна.', status: 503, code: 'DATABASE_UNAVAILABLE', retryable: true,
    })).mockResolvedValue(baseSetup)
    render(<App />)
    expect(await screen.findByRole('heading', { name: 'Текущий сетап недоступен' })).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('Карточка временно недоступна')
    await user.click(screen.getByRole('button', { name: 'Повторить загрузку текущего' }))
    await waitFor(() => expect(mocks.getSetup).toHaveBeenCalledTimes(2))
    expect(screen.getAllByRole('heading', { name: baseSetup.name }).length).toBeGreaterThanOrEqual(2)
  })

  it('keeps metadata input across a revision conflict and retries against the reloaded revision', async () => {
    const user = userEvent.setup()
    const conflict = new ApiError({ message: 'Expected revision 3, actual 4.', status: 409, code: 'REVISION_CONFLICT' })
    const fresh = { ...baseSetup, revision: 4, description: 'Изменено другим клиентом' }
    const saved = { ...fresh, name: 'Мой сохранённый текст', revision: 5 }
    mocks.getSetup.mockResolvedValueOnce(baseSetup).mockResolvedValueOnce(fresh)
    mocks.updateSetup.mockRejectedValueOnce(conflict).mockResolvedValueOnce(saved)
    render(<App />)
    await user.click(await screen.findByRole('button', { name: `Открыть сетап ${baseSetup.name}` }))
    await user.click(await screen.findByRole('button', { name: 'Изменить метаданные' }))
    const input = screen.getByRole('textbox', { name: 'Название' })
    await user.clear(input)
    await user.type(input, saved.name)
    await user.click(screen.getByRole('button', { name: 'Сохранить revision' }))
    expect(await screen.findByText('Карточка уже изменилась')).toBeInTheDocument()
    expect(input).toHaveValue(saved.name)
    await user.click(screen.getByRole('button', { name: 'Загрузить актуальную revision' }))
    await waitFor(() => expect(mocks.getSetup).toHaveBeenCalledTimes(2))
    expect(input).toHaveValue(saved.name)
    await user.click(screen.getByRole('button', { name: 'Сохранить revision' }))
    await waitFor(() => expect(mocks.updateSetup).toHaveBeenLastCalledWith('setup-1', 4, saved.name, baseSetup.description, 'test-idempotency-key'))
  })

  it('requires explicit no-execution confirmation before selecting current', async () => {
    const user = userEvent.setup()
    render(<App />)
    await user.click(await screen.findByRole('button', { name: `Открыть сетап ${baseSetup.name}` }))
    await user.click(await screen.findByRole('button', { name: 'Выбрать текущим' }))
    expect(screen.getByRole('dialog')).toHaveTextContent('G-code не копируется, не исполняется и LinuxCNC не запускается')
    await user.click(screen.getByRole('button', { name: 'Выбрать, не запускать' }))
    await waitFor(() => expect(mocks.setCurrentSetup).toHaveBeenCalledWith('setup-1', 3, null, 'test-idempotency-key'))
  })

  it('shows terminal validation issues with artifact name and corrective action', async () => {
    const user = userEvent.setup()
    const initial = {
      jobId: 'job-1', kind: 'validate', setupId: 'setup-1', state: 'running',
      progress: { completedBytes: 60, totalBytes: 120, completedItems: 0, totalItems: 1 },
      createdAt: baseSetup.createdAt,
    }
    const terminal = { ...initial, state: 'succeeded', progress: { completedBytes: 120, totalBytes: 120, completedItems: 1, totalItems: 1 }, result: { issues: [{ code: 'INVALID_NAME', severity: 'error', message: 'invalid basename', artifactId: 'program-1', action: 'rename the artifact' }] } }
    mocks.setupAction.mockResolvedValue(initial)
    mocks.waitForJob.mockResolvedValue(terminal)
    render(<App />)
    await user.click(await screen.findByRole('button', { name: `Открыть сетап ${baseSetup.name}` }))
    await user.click(await screen.findByRole('button', { name: 'Проверить' }))
    await user.click(screen.getByRole('button', { name: 'Запустить проверку' }))
    expect(await screen.findByText(/finish.ngc · Ошибка/)).toBeInTheDocument()
    expect(screen.getByText('invalid basename')).toBeInTheDocument()
    expect(screen.getByText('Действие: rename the artifact')).toBeInTheDocument()
  })

  it('restores filters, detail and selected artifact by stable IDs and returns by keyboard without losing state', async () => {
    const user = userEvent.setup()
    mocks.getUIState.mockResolvedValue({
      clientId: 'test-client', screen: 'detail', selectedSetupId: 'setup-1', selectedArtifactId: 'program-1',
      filters: { query: 'finish', status: 'ready', sheet: 'no', current: 'any', sort: 'name_asc' },
      view: { line: 42 },
    })
    render(<App />)
    expect(await screen.findByTestId('gcode-preview')).toHaveTextContent('finish.ngc')
    await waitFor(() => expect(mocks.putUIState).toHaveBeenCalled())
    const persistedState = mocks.putUIState.mock.calls.at(-1)?.[0] as unknown
    expect(persistedState).toMatchObject({
      screen: 'detail', selectedSetupId: 'setup-1', selectedArtifactId: 'program-1',
      filters: { query: 'finish', status: 'ready' }, view: { line: 42 },
    })
    const brand = screen.getByRole('button', { name: 'Web Setup Manager — библиотека' })
    brand.focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('searchbox')).toHaveValue('finish')
  })

  it('opens and removes recent history entries using semantic keyboard controls', async () => {
    const user = userEvent.setup()
    mocks.listRecentSetups.mockResolvedValue([{
      libraryId: 'library-1', setupId: 'setup-1', setupName: baseSetup.name, setupStatus: 'ready',
      lastArtifactId: 'program-1', lastLine: 18, lastOpenedAt: baseSetup.updatedAt,
    }])
    render(<App />)
    const history = await screen.findByRole('region', { name: 'Недавние сетапы' })
    const recent = within(history).getAllByRole('button').find((button) => button.classList.contains('recent-list__open'))!
    recent.focus()
    await user.keyboard('{Enter}')
    expect(await screen.findByTestId('gcode-preview')).toBeInTheDocument()
    expect(mocks.touchRecentSetup).toHaveBeenCalledWith('setup-1', 'program-1')
    await user.click(screen.getByRole('button', { name: 'Web Setup Manager — библиотека' }))
    await user.click(await screen.findByRole('button', { name: `Удалить ${baseSetup.name} из недавних` }))
    expect(mocks.deleteRecentSetup).toHaveBeenCalledWith('setup-1')
  })

  it('distinguishes managed storage failure while preserving the loaded workspace', async () => {
    const user = userEvent.setup()
    mocks.getReadiness.mockResolvedValueOnce({ ok: false, code: 'STORAGE_UNAVAILABLE', message: 'Managed storage is unavailable.' })
      .mockResolvedValue({ ok: true })
    render(<App />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Управляемое хранилище недоступно')
    expect(screen.getByRole('heading', { name: 'Производственные сетапы' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Проверить снова' }))
    await waitFor(() => expect(screen.queryByText('Управляемое хранилище недоступно')).not.toBeInTheDocument())
  })

  it('shows a reconnect banner when readiness polling loses the Backend after load', async () => {
    const user = userEvent.setup()
    mocks.getReadiness.mockRejectedValueOnce(new ApiError({
      message: 'Локальный Backend недоступен.', status: 0, code: 'NETWORK_ERROR', retryable: true,
    })).mockResolvedValue({ ok: true })
    render(<App />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Локальный сервис временно не готов')
    expect(screen.getByRole('heading', { name: 'Производственные сетапы' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Проверить снова' }))
    await waitFor(() => expect(screen.queryByText('Локальный сервис временно не готов')).not.toBeInTheDocument())
  })

  it('keeps the loaded local UI visible when the external network goes offline', async () => {
    render(<App />)
    await screen.findByRole('heading', { name: 'Производственные сетапы' })
    window.dispatchEvent(new Event('offline'))
    expect(await screen.findByText(/Setup Manager продолжает работать с локальным Backend/)).toHaveAttribute('role', 'status')
    expect(screen.getByRole('heading', { name: 'Производственные сетапы' })).toBeInTheDocument()
  })
})
