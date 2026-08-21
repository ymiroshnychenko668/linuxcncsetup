import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type CatalogSetup, type CatalogSnapshot } from './api'
import { App } from './App'

const mocks = vi.hoisted(() => ({
  getAuthSession: vi.fn(),
  login: vi.fn(),
  logout: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
  clearApiSession: vi.fn(),
  getCapabilities: vi.fn(),
  getReadiness: vi.fn(),
  getCatalog: vi.fn(),
  createCatalogFolder: vi.fn(),
  updateCatalogFolder: vi.fn(),
  deleteCatalogFolder: vi.fn(),
  createCatalogSetup: vi.fn(),
  updateCatalogSetup: vi.fn(),
  deleteCatalogSetup: vi.fn(),
  putCatalogComponent: vi.fn(),
  deleteCatalogComponent: vi.fn(),
}))

const previewMode = vi.hoisted(() => ({ real: false }))

vi.mock('./api', async (loadOriginal) => ({
  ...await loadOriginal<typeof import('./api')>(),
  ...mocks,
  newIdempotencyKey: () => 'test-idempotency-key',
}))

vi.mock('./components/GCodePreview', async (loadOriginal) => {
  const actual = await loadOriginal<typeof import('./components/GCodePreview')>()
  const ActualGCodePreview = actual.GCodePreview
  return {
    ...actual,
    GCodePreview: (props: Parameters<typeof ActualGCodePreview>[0]) => previewMode.real
      ? <ActualGCodePreview {...props} />
      : <div data-testid="gcode-preview">Preview: {props.artifact.displayName} · {props.contentUrl}</div>,
  }
})

vi.mock('./components/SetupSheetViewer', () => ({
  SetupSheetViewer: ({ artifact, contentUrl, onClose }: { artifact: { displayName: string }; contentUrl: string; onClose: () => void }) => <div role="dialog" aria-label="Setup Sheet viewer"><span>{artifact.displayName} · {contentUrl}</span><button type="button" onClick={onClose}>Закрыть</button></div>,
}))

const catalogSetup: CatalogSetup = {
  setupId: 'setup-1',
  folderId: 'folder-2026',
  name: 'Кронштейн',
  description: 'Операция 20',
  revision: 3,
  program: {
    artifactId: 'program-1', displayName: 'bracket.ngc', mediaType: 'text/x-gcode',
    byteSize: 120, version: 'version-program', relativePath: 'Заказы/2026/bracket.ngc',
  },
  setupSheet: {
    artifactId: 'sheet-1', displayName: 'bracket.pdf', mediaType: 'application/pdf',
    byteSize: 80, version: 'version-sheet', relativePath: 'Заказы/2026/bracket.pdf',
  },
  programRelativePath: 'Заказы/2026/bracket.ngc',
  setupSheetRelativePath: 'Заказы/2026/bracket.pdf',
  updatedAt: '2026-08-21T08:00:00Z',
}

const snapshot: CatalogSnapshot = {
  destination: { rootLabel: 'LinuxCNC PROGRAM_PREFIX', rootDisplay: '~/linuxcnc/nc_files' },
  generation: 'generation-1',
  folders: [
    { folderId: 'folder-orders', name: 'Заказы', relativePath: 'Заказы', revision: 1 },
    { folderId: 'folder-2026', parentFolderId: 'folder-orders', name: '2026', relativePath: 'Заказы/2026', revision: 2 },
  ],
  setups: [catalogSetup],
}

beforeEach(() => {
  previewMode.real = false
  Object.values(mocks).forEach((mock) => mock.mockReset())
  mocks.getAuthSession.mockResolvedValue({ authenticated: true, loginRequired: false, user: null, csrfToken: 'local-token' })
  mocks.login.mockResolvedValue({ authenticated: true, loginRequired: true, user: { username: 'operator' }, csrfToken: 'remote-token' })
  mocks.logout.mockResolvedValue(undefined)
  mocks.getCapabilities.mockResolvedValue({ libraryAlias: 'LinuxCNC', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} })
  mocks.getReadiness.mockResolvedValue({ ok: true })
  mocks.getCatalog.mockResolvedValue(snapshot)
  mocks.createCatalogFolder.mockImplementation((parentFolderId: string | undefined, name: string) => ({
    folderId: 'new-folder', parentFolderId, name, relativePath: name, revision: 1,
  }))
  mocks.updateCatalogFolder.mockImplementation((folder: object) => folder)
  mocks.deleteCatalogFolder.mockResolvedValue(undefined)
  mocks.createCatalogSetup.mockImplementation((input: { folderId?: string; name: string; description?: string }) => ({
    setupId: 'new-setup', folderId: input.folderId, name: input.name, description: input.description,
    revision: 1, program: null, setupSheet: null, updatedAt: '2026-08-21T09:00:00Z',
  }))
  mocks.updateCatalogSetup.mockImplementation((setup: CatalogSetup, changes: Partial<CatalogSetup>) => ({ ...setup, ...changes, revision: setup.revision + 1 }))
  mocks.deleteCatalogSetup.mockResolvedValue(undefined)
  mocks.deleteCatalogComponent.mockImplementation((setup: CatalogSetup, component: string) => ({
    ...setup, revision: setup.revision + 1,
    program: component === 'program' ? null : setup.program,
    setupSheet: component === 'setup-sheet' ? null : setup.setupSheet,
  }))
})

afterEach(() => vi.unstubAllGlobals())

describe('catalog workbench', () => {
  it('completes login, tree navigation, upload, preview search, line jump, and logout by keyboard', async () => {
    previewMode.real = true
    const user = userEvent.setup()
    const gcode = new TextEncoder().encode('G0 X0\nG1 X10\nM3 S1000\nM30')

    class PreviewWorker {
      private listener?: (event: MessageEvent<unknown>) => void
      addEventListener(_type: string, listener: EventListener) { this.listener = listener as (event: MessageEvent<unknown>) => void }
      removeEventListener() { this.listener = undefined }
      terminate() { this.listener = undefined }
      postMessage(message: { type: string; requestId: string; query?: string }) {
        if (message.type === 'index') {
          queueMicrotask(() => this.listener?.({ data: {
            type: 'indexResult', requestId: message.requestId, lineCount: 4,
            entries: [{ line: 1, byteOffset: 0 }],
          } } as MessageEvent<unknown>))
        }
        if (message.type === 'search') {
          const lineNumbers = message.query === 'M30' ? Float64Array.from([4]) : new Float64Array()
          queueMicrotask(() => this.listener?.({ data: {
            type: 'searchResult', requestId: message.requestId, totalMatches: lineNumbers.length,
            lineNumbers, matchOffset: 0, truncated: false,
          } } as MessageEvent<unknown>))
        }
      }
    }
    vi.stubGlobal('Worker', PreviewWorker)
    vi.stubGlobal('fetch', vi.fn().mockImplementation((_url: string, init?: RequestInit) => {
      const headers = init?.headers as Record<string, string> | undefined
      const match = /^bytes=(\d+)-(\d+)$/.exec(headers?.Range ?? '')
      if (!match) throw new Error(`unexpected range ${headers?.Range}`)
      const start = Number(match[1])
      const end = Number(match[2])
      return Promise.resolve(new Response(gcode.slice(start, end + 1), {
        status: 206,
        headers: { etag: headers?.['If-Match'] ?? '' },
      }))
    }))

    const initialSetup: CatalogSetup = {
      ...catalogSetup,
      program: { ...catalogSetup.program!, byteSize: gcode.byteLength },
    }
    mocks.getAuthSession.mockResolvedValueOnce({ authenticated: false, loginRequired: true, user: null })
    mocks.getCatalog.mockResolvedValue({ ...snapshot, setups: [initialSetup] })
    const created: CatalogSetup = {
      ...initialSetup,
      setupId: 'keyboard-setup',
      name: 'Keyboard Flow',
      revision: 1,
      program: null,
      setupSheet: null,
      programRelativePath: undefined,
      setupSheetRelativePath: undefined,
    }
    const withProgram: CatalogSetup = {
      ...created,
      revision: 2,
      program: {
        ...catalogSetup.program!,
        artifactId: 'keyboard-program',
        displayName: 'keyboard.ngc',
        byteSize: gcode.byteLength,
        version: 'keyboard-version',
        relativePath: 'Заказы/2026/keyboard.ngc',
      },
      programRelativePath: 'Заказы/2026/keyboard.ngc',
    }
    mocks.createCatalogSetup.mockResolvedValue(created)
    mocks.putCatalogComponent.mockResolvedValue(withProgram)

    render(<App />)

    const username = await screen.findByRole('textbox', { name: 'Имя пользователя' })
    expect(username).toHaveFocus()
    await user.type(username, 'operator')
    await user.tab()
    const password = screen.getByLabelText('Пароль')
    expect(password).toHaveFocus()
    await user.type(password, 'system-secret')
    await user.tab()
    expect(screen.getByRole('checkbox', { name: /Запомнить меня/ })).toHaveFocus()
    await user.tab()
    const loginButton = screen.getByRole('button', { name: 'Открыть каталог сетапов' })
    expect(loginButton).toHaveFocus()
    await user.keyboard('{Enter}')

    const editor = await screen.findByLabelText('Просмотр сетапа')
    await waitFor(() => expect(editor).toHaveFocus())
    const treeSetup = await screen.findByRole('treeitem', { name: /Кронштейн/ })
    for (let step = 0; step < 40 && document.activeElement !== treeSetup; step += 1) await user.tab()
    expect(treeSetup).toHaveFocus()
    await user.keyboard('{ArrowLeft}')
    expect(screen.getByRole('treeitem', { name: '2026' })).toHaveFocus()
    await user.keyboard('{ArrowRight}{ArrowRight}{Enter}')
    expect(treeSetup).toHaveFocus()
    expect(treeSetup).toHaveAttribute('aria-selected', 'true')

    await user.tab()
    expect(document.body).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('link', { name: 'К просмотру G-code' })).toHaveFocus()
    await user.tab()
    const uploadButton = screen.getByRole('button', { name: 'Загрузить' })
    expect(uploadButton).toHaveFocus()
    await user.keyboard('{Enter}')

    const dialog = await screen.findByRole('dialog', { name: 'Загрузить сетап' })
    expect(dialog).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Закрыть диалог' })).toHaveFocus()
    await user.tab()
    expect(screen.getByLabelText('Каталог LinuxCNC')).toHaveFocus()
    await user.tab()
    const setupName = screen.getByLabelText('Название сетапа')
    expect(setupName).toHaveFocus()
    await user.type(setupName, 'Keyboard Flow')
    await user.tab()
    expect(screen.getByLabelText(/Описание/)).toHaveFocus()
    await user.tab()
    const programInput = screen.getByLabelText('G-code программа')
    expect(programInput).toHaveFocus()
    const program = new File([gcode], 'keyboard.ngc', { type: 'text/plain' })
    await user.upload(programInput, program)
    await user.tab()
    expect(screen.getByLabelText('Setup Sheet')).toHaveFocus()
    await user.tab()
    expect(screen.getByRole('button', { name: 'Отмена' })).toHaveFocus()
    await user.tab()
    const saveButton = screen.getByRole('button', { name: 'Создать и загрузить' })
    expect(saveButton).toHaveFocus()
    await user.keyboard('{Enter}')

    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledWith(
      expect.objectContaining({ setupId: 'keyboard-setup' }),
      'program',
      program,
      'test-idempotency-key',
      expect.any(Object),
    ))
    await waitFor(() => expect(uploadButton).toHaveFocus())
    expect(await screen.findByRole('heading', { name: 'keyboard.ngc' })).toBeInTheDocument()
    expect(screen.getByText('M30')).toBeInTheDocument()

    const search = screen.getByRole('searchbox', { name: 'Поиск' })
    for (let step = 0; step < 30 && document.activeElement !== search; step += 1) await user.tab()
    expect(search).toHaveFocus()
    await user.type(search, 'M30')
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('button', { name: 'Совпадение 1, строка 4' })).toHaveAttribute('aria-current', 'true')

    await user.tab({ shift: true })
    expect(screen.getByRole('button', { name: 'Перейти' })).toHaveFocus()
    await user.tab({ shift: true })
    const line = screen.getByRole('spinbutton', { name: 'Строка' })
    expect(line).toHaveFocus()
    await user.clear(line)
    await user.type(line, '2')
    await user.keyboard('{Enter}')
    await waitFor(() => expect(screen.getByRole('spinbutton', { name: 'Строка' })).toHaveValue(2))
    expect(screen.getByRole('spinbutton', { name: 'Строка' })).toHaveFocus()

    const logoutButton = screen.getByRole('button', { name: 'Выйти' })
    for (let step = 0; step < 25 && document.activeElement !== logoutButton; step += 1) await user.tab({ shift: true })
    expect(logoutButton).toHaveFocus()
    await user.keyboard('{Enter}')
    expect(await screen.findByRole('status')).toHaveTextContent('Вы вышли из Web Setup Manager')
    expect(screen.getByRole('textbox', { name: 'Имя пользователя' })).toHaveFocus()
    expect(mocks.logout).toHaveBeenCalledTimes(1)
  }, 45_000)

  it('authenticates before loading the catalog and enters the compact workbench', async () => {
    mocks.getAuthSession.mockResolvedValueOnce({ authenticated: false, loginRequired: true, user: null })
    const user = userEvent.setup()
    render(<App />)

    expect(await screen.findByRole('heading', { name: 'Вход в систему' })).toBeInTheDocument()
    expect(mocks.getCapabilities).not.toHaveBeenCalled()
    await user.type(screen.getByRole('textbox', { name: 'Имя пользователя' }), 'operator')
    await user.type(screen.getByLabelText('Пароль'), 'secret')
    await user.click(screen.getByRole('button', { name: 'Открыть каталог сетапов' }))

    expect(await screen.findByLabelText('Каталог сетапов')).toBeInTheDocument()
    expect(mocks.login).toHaveBeenCalledWith('operator', 'secret', false)
    expect(screen.getByText('operator')).toBeInTheDocument()
    await waitFor(() => expect(document.getElementById('catalog-editor')).toHaveFocus())
  })

  it('renders G-code on the left, the directory tree on the right, and the exact LinuxCNC destination', async () => {
    render(<App />)
    const editor = await screen.findByLabelText('Просмотр сетапа')
    const explorer = screen.getByLabelText('Каталог сетапов')
    expect(editor.compareDocumentPosition(explorer) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(await screen.findByTestId('gcode-preview')).toHaveTextContent('/api/v1/catalog/setups/setup-1/program/content')
    expect(screen.getAllByText('~/linuxcnc/nc_files', { exact: true }).length).toBeGreaterThan(0)
    expect(screen.getByText('~/linuxcnc/nc_files/Заказы/2026/bracket.ngc')).toBeInTheDocument()
    expect(screen.queryByText(/готовност|проверить сетап|текущий сетап/i)).not.toBeInTheDocument()
  })

  it('treats an incomplete setup as normal and offers independent file actions', async () => {
    mocks.getCatalog.mockResolvedValue({ ...snapshot, setups: [{ ...catalogSetup, program: null, programRelativePath: undefined }] })
    render(<App />)
    expect(await screen.findByRole('heading', { name: 'Программа ещё не загружена' })).toBeInTheDocument()
    expect(screen.getByText(/нормальный неполный сетап/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Добавить G-code' })).toBeEnabled()
    expect(screen.getByRole('button', { name: 'Открыть Setup Sheet' })).toBeEnabled()
  })

  it('uploads at most one program and one sheet and confirms the final destination after closing', async () => {
    const user = userEvent.setup()
    const created: CatalogSetup = { ...catalogSetup, setupId: 'new-setup', name: 'Новая деталь', revision: 1, program: null, setupSheet: null, programRelativePath: undefined, setupSheetRelativePath: undefined }
    const withProgram: CatalogSetup = { ...created, revision: 2, program: { ...catalogSetup.program!, artifactId: 'new-program', displayName: 'new.ngc', relativePath: 'Заказы/2026/new.ngc' }, programRelativePath: 'Заказы/2026/new.ngc' }
    const complete: CatalogSetup = { ...withProgram, revision: 3, setupSheet: { ...catalogSetup.setupSheet!, artifactId: 'new-sheet', displayName: 'new.pdf', relativePath: 'Заказы/2026/new.pdf' }, setupSheetRelativePath: 'Заказы/2026/new.pdf' }
    mocks.createCatalogSetup.mockResolvedValue(created)
    mocks.putCatalogComponent.mockResolvedValueOnce(withProgram).mockResolvedValueOnce(complete)
    render(<App />)
    await screen.findByLabelText('Каталог сетапов')
    await user.click(screen.getByRole('button', { name: 'Загрузить' }))
    await user.selectOptions(screen.getByLabelText('Каталог LinuxCNC'), 'folder-2026')
    await user.type(screen.getByLabelText('Название сетапа'), 'Новая деталь')
    const program = new File(['G0 X0'], 'new.ngc', { type: 'text/plain' })
    const sheet = new File(['pdf'], 'new.pdf', { type: 'application/pdf' })
    await user.upload(screen.getByLabelText('G-code программа'), program)
    await user.upload(screen.getByLabelText('Setup Sheet'), sheet)
    expect(screen.getByText('~/linuxcnc/nc_files/Заказы/2026/new.ngc')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Создать и загрузить' }))

    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledTimes(2))
    expect(mocks.putCatalogComponent.mock.calls[0]?.slice(1, 3)).toEqual(['program', program])
    expect(mocks.putCatalogComponent.mock.calls[1]?.slice(1, 3)).toEqual(['setup-sheet', sheet])
    expect(await screen.findByText('Сохранено в LinuxCNC: ~/linuxcnc/nc_files/Заказы/2026/new.ngc')).toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: 'Загрузить сетап' })).not.toBeInTheDocument()
  })

  it('supports keyboard resizing of the right explorer', async () => {
    const user = userEvent.setup()
    render(<App />)
    const separator = await screen.findByRole('separator', { name: 'Изменить ширину дерева сетапов' })
    expect(separator).toHaveAttribute('aria-valuenow', '320')
    separator.focus()
    await user.keyboard('{ArrowLeft}{ArrowLeft}')
    expect(separator).toHaveAttribute('aria-valuenow', '352')
    await user.keyboard('{End}')
    expect(separator).toHaveAttribute('aria-valuenow', '260')
  })

  it('opens the right explorer as a keyboard-dismissible mobile drawer', async () => {
    const user = userEvent.setup()
    render(<App />)
    const toggle = await screen.findByRole('button', { name: 'Открыть дерево сетапов' })
    await user.click(toggle)
    expect(screen.getByLabelText('Каталог сетапов')).toHaveClass('catalog-explorer--open')
    expect(screen.getByRole('button', { name: 'Закрыть дерево сетапов' })).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(screen.getByLabelText('Каталог сетапов')).not.toHaveClass('catalog-explorer--open')
    expect(toggle).toHaveFocus()
  })

  it('keeps the selected setup and entered properties on a local revision conflict', async () => {
    const user = userEvent.setup()
    mocks.updateCatalogSetup.mockRejectedValue(new ApiError({ message: 'Expected revision 3, actual 4.', status: 409, code: 'REVISION_CONFLICT' }))
    render(<App />)
    await screen.findByTestId('gcode-preview')
    await user.click(screen.getByRole('button', { name: 'Свойства и каталог сетапа' }))
    const name = screen.getByRole('textbox', { name: 'Название' })
    await user.clear(name)
    await user.type(name, 'Моё новое имя')
    await user.click(screen.getByRole('button', { name: 'Сохранить' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Конфликт изменений')
    expect(name).toHaveValue('Моё новое имя')
    expect(screen.getByTestId('gcode-preview')).toHaveTextContent('bracket.ngc')
  })

  it('creates a real folder below the selected catalog and retains the destination preview', async () => {
    const user = userEvent.setup()
    render(<App />)
    await screen.findByLabelText('Каталог сетапов')
    await user.click(screen.getByRole('button', { name: 'Новый каталог' }))
    await user.type(screen.getByRole('textbox', { name: 'Название' }), 'Серия 42')
    expect(screen.getByText('~/linuxcnc/nc_files/Серия 42')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Создать каталог' }))
    await waitFor(() => expect(mocks.createCatalogFolder).toHaveBeenCalledWith(undefined, 'Серия 42', 'test-idempotency-key'))
  })

  it('preserves authentication expiry and logout behavior around the workbench', async () => {
    mocks.getAuthSession.mockResolvedValue({ authenticated: true, loginRequired: true, user: { username: 'operator' }, csrfToken: 'remote-token' })
    render(<App />)
    await screen.findByLabelText('Каталог сетапов')
    const handler = mocks.setUnauthorizedHandler.mock.calls.map(([candidate]) => candidate as (() => void) | undefined).find(Boolean)
    act(() => handler?.())
    expect(await screen.findByRole('status')).toHaveTextContent('Сессия истекла')
    expect(screen.queryByLabelText('Каталог сетапов')).not.toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Имя пользователя' })).toHaveFocus()
  })

  it('keeps the editor visible while readiness reports a recoverable catalog error', async () => {
    mocks.getReadiness.mockResolvedValueOnce({ ok: false, code: 'STORAGE_UNAVAILABLE', message: 'PROGRAM_PREFIX недоступен.' }).mockResolvedValue({ ok: true })
    const user = userEvent.setup()
    render(<App />)
    expect(await screen.findByRole('alert')).toHaveTextContent('PROGRAM_PREFIX недоступен')
    expect(screen.getByTestId('gcode-preview')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Повторить' }))
    await waitFor(() => expect(screen.queryByText('PROGRAM_PREFIX недоступен.')).not.toBeInTheDocument())
  })

  it('supports pointer resizing without changing selection', async () => {
    render(<App />)
    const separator = await screen.findByRole('separator')
    fireEvent(separator, new MouseEvent('pointerdown', { bubbles: true, button: 0, clientX: 1000 }))
    expect(document.body).toHaveClass('catalog-resizing')
    fireEvent(window, new MouseEvent('pointermove', { bubbles: true, clientX: 950 }))
    expect(separator).toHaveAttribute('aria-valuenow', '370')
    fireEvent(window, new MouseEvent('pointercancel', { bubbles: true }))
    expect(document.body).not.toHaveClass('catalog-resizing')
    expect(screen.getByTestId('gcode-preview')).toHaveTextContent('bracket.ngc')
  })
})
