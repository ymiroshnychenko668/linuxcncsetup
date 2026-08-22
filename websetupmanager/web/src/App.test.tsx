import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type CatalogSetup, type CatalogSnapshot } from './api'
import { App } from './App'
import { allowGCodeCacheScope, blockGCodeCacheScope, captureGCodeCacheAuthGeneration } from './gcodeCache'

const mocks = vi.hoisted(() => ({
	activateExplicitAuthSession: vi.fn(),
	acceptExplicitAuthSession: vi.fn(),
	quarantineExplicitAuthSession: vi.fn(),
  getAuthSession: vi.fn(),
  login: vi.fn(),
  logout: vi.fn(),
  logoutSessionIfCurrent: vi.fn(),
	reconcileStaleAuthSession: vi.fn(),
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
  newIdempotencyKey: vi.fn(),
}))

const previewMode = vi.hoisted(() => ({
  real: false,
  mountCount: 0,
  completeAnalysis: undefined as (() => void) | undefined,
  completeTruncatedAnalysis: undefined as (() => void) | undefined,
}))

vi.mock('./api', async (loadOriginal) => ({
  ...await loadOriginal<typeof import('./api')>(),
  ...mocks,
  newIdempotencyKey: mocks.newIdempotencyKey,
}))

vi.mock('./components/GCodePreview', async (loadOriginal) => {
  const React = await import('react')
  const actual = await loadOriginal<typeof import('./components/GCodePreview')>()
  const ActualGCodePreview = actual.GCodePreview
  return {
    ...actual,
    GCodePreview: (props: Parameters<typeof ActualGCodePreview>[0]) => {
      React.useEffect(() => { previewMode.mountCount += 1 }, [])
      previewMode.completeAnalysis = () => props.onAnalysisChanged?.({
        artifactId: props.artifact.artifactId,
        version: props.artifact.version,
        progress: 1,
        complete: true,
        lineCount: 20,
        tools: [{ toolNumber: 7, firstLine: 12, references: 2, changes: 1 }],
        toolsTruncated: false,
        validation: 'online',
      })
      previewMode.completeTruncatedAnalysis = () => props.onAnalysisChanged?.({
        artifactId: props.artifact.artifactId,
        version: props.artifact.version,
        progress: 1,
        complete: true,
        lineCount: 20,
        tools: [],
        toolsTruncated: true,
        validation: 'online',
      })
      return previewMode.real
        ? <ActualGCodePreview {...props} />
        : <div data-testid="gcode-preview">Preview: {props.artifact.displayName} · {props.contentUrl}</div>
    },
  }
})

vi.mock('./components/SetupSheetViewer', () => ({
  SetupSheetViewer: ({ artifact, contentUrl, inline }: { artifact: { displayName: string }; contentUrl: string; inline?: boolean }) => <section data-testid="setup-sheet-viewer" data-inline={inline ? 'true' : 'false'} aria-label={`Setup Sheet ${artifact.displayName}`}>{artifact.displayName} · {contentUrl}</section>,
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

let originalLocks: PropertyDescriptor | undefined

function installSerialWebLocks(): void {
	let tail = Promise.resolve()
	Object.defineProperty(navigator, 'locks', {
		configurable: true,
		value: {
			request: vi.fn(async (_name: string, _options: object, operation: () => Promise<unknown>) => {
				let release!: () => void
				const previous = tail
				tail = new Promise<void>((resolve) => { release = resolve })
				await previous
				try {
					return await operation()
				} finally {
					release()
				}
			}),
		},
	})
}

beforeEach(async () => {
	originalLocks = Object.getOwnPropertyDescriptor(navigator, 'locks')
	installSerialWebLocks()
	await allowGCodeCacheScope('local:LinuxCNC')
	await allowGCodeCacheScope('user:operator:LinuxCNC')
	window.localStorage.clear()
  previewMode.real = false
  previewMode.mountCount = 0
  previewMode.completeAnalysis = undefined
  previewMode.completeTruncatedAnalysis = undefined
  Object.values(mocks).forEach((mock) => mock.mockReset())
  mocks.getAuthSession.mockResolvedValue({ authenticated: true, loginRequired: false, user: null, csrfToken: 'local-token' })
  mocks.newIdempotencyKey.mockReturnValue('test-idempotency-key')
  mocks.login.mockResolvedValue({ authenticated: true, loginRequired: true, user: { username: 'operator' }, csrfToken: 'remote-token' })
  mocks.logout.mockResolvedValue(undefined)
  mocks.logoutSessionIfCurrent.mockResolvedValue(true)
	mocks.activateExplicitAuthSession.mockResolvedValue(true)
	mocks.acceptExplicitAuthSession.mockResolvedValue(undefined)
	mocks.quarantineExplicitAuthSession.mockResolvedValue({
		schema: 1,
		fingerprint: `sha256:${'a'.repeat(64)}`,
		supersededMarkers: [],
	})
	mocks.reconcileStaleAuthSession.mockResolvedValue(true)
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

afterEach(() => {
	vi.unstubAllGlobals()
	if (originalLocks) Object.defineProperty(navigator, 'locks', originalLocks)
	else Reflect.deleteProperty(navigator, 'locks')
})

describe('catalog workbench', () => {
  it('completes login, left-tree file navigation, direct upload, preview search, line jump, and logout by keyboard', async () => {
    previewMode.real = true
    const user = userEvent.setup()
    const gcode = new TextEncoder().encode('G0 X0\nG1 X10\nM3 S1000\nM30')
		let indexedUploadFile: File | undefined

    class PreviewWorker {
      private listener?: (event: MessageEvent<unknown>) => void
      addEventListener(_type: string, listener: EventListener) { this.listener = listener as (event: MessageEvent<unknown>) => void }
      removeEventListener() { this.listener = undefined }
      terminate() { this.listener = undefined }
			postMessage(message: { type: string; requestId: string; query?: string; file?: File }) {
        if (message.type === 'index') {
					if (message.file) indexedUploadFile = message.file
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
        headers: {
          etag: headers?.['If-Match'] ?? '',
          'content-range': `bytes ${start}-${end}/${gcode.byteLength}`,
        },
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

    const editor = await screen.findByLabelText('Просмотр файла')
    await waitFor(() => expect(editor).toHaveFocus())
    const treeSetup = await screen.findByRole('treeitem', { name: /bracket\.ngc/ })
    for (let step = 0; step < 8 && document.activeElement !== treeSetup; step += 1) await user.tab({ shift: true })
    expect(treeSetup).toHaveFocus()
    await user.keyboard('{ArrowRight}{Enter}')
    const treeSheet = screen.getByRole('treeitem', { name: /bracket\.pdf/ })
    expect(treeSheet).toHaveFocus()
    expect(treeSheet).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('setup-sheet-viewer')).toHaveAttribute('data-inline', 'true')
    expect(screen.queryByRole('dialog', { name: /Setup Sheet/i })).not.toBeInTheDocument()
    await user.keyboard('{ArrowLeft}{Enter}')
    expect(treeSetup).toHaveFocus()
    expect(treeSetup).toHaveAttribute('aria-selected', 'true')

    const uploadButton = screen.getByRole('button', { name: 'Добавить' })
    for (let step = 0; step < 24 && document.activeElement !== uploadButton; step += 1) await user.tab({ shift: true })
    expect(uploadButton).toHaveFocus()
    await user.keyboard('{Enter}')
    const program = new File([gcode], 'keyboard.ngc', { type: 'text/plain' })
    await user.upload(screen.getByLabelText('Файлы нового сетапа'), program)

    await waitFor(() => expect(mocks.createCatalogSetup).toHaveBeenCalledWith(
      expect.objectContaining({ name: 'keyboard' }),
      'test-idempotency-key',
      expect.any(AbortSignal),
    ))
    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledWith(
      expect.objectContaining({ setupId: 'keyboard-setup' }),
      'program',
      program,
      'test-idempotency-key',
      expect.any(Object),
    ))
    await waitFor(() => expect(uploadButton).toHaveFocus())
    expect(await screen.findByLabelText('G-code keyboard.ngc')).toBeInTheDocument()
    expect(screen.getByText('M30')).toBeInTheDocument()
		expect(indexedUploadFile).toBe(program)

    const search = screen.getByRole('searchbox', { name: 'Поиск' })
    for (let step = 0; step < 40 && document.activeElement !== search; step += 1) await user.tab()
    expect(search).toHaveFocus()
    await user.type(search, 'M30')
    await waitFor(() => expect(screen.getByRole('button', { name: 'Найти' })).toBeEnabled())
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
    for (let step = 0; step < 40 && document.activeElement !== logoutButton; step += 1) await user.tab({ shift: true })
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

    expect(await screen.findByLabelText('Файлы сетапов')).toBeInTheDocument()
    expect(mocks.login).toHaveBeenCalledWith('operator', 'secret', false)
		expect(mocks.quarantineExplicitAuthSession).toHaveBeenCalledWith(expect.objectContaining({ csrfToken: 'remote-token' }))
		expect(mocks.activateExplicitAuthSession).toHaveBeenCalledWith(
			expect.objectContaining({ csrfToken: 'remote-token' }),
			expect.objectContaining({ schema: 1 }),
		)
		expect(mocks.acceptExplicitAuthSession).toHaveBeenCalledWith(
			expect.objectContaining({ csrfToken: 'remote-token' }),
			expect.objectContaining({ schema: 1 }),
		)
		expect(mocks.quarantineExplicitAuthSession.mock.invocationCallOrder[0]).toBeLessThan(mocks.getCapabilities.mock.invocationCallOrder[0])
		expect(mocks.activateExplicitAuthSession.mock.invocationCallOrder[0]).toBeLessThan(mocks.getCapabilities.mock.invocationCallOrder[0])
		expect(mocks.getCapabilities.mock.invocationCallOrder[0]).toBeLessThan(mocks.acceptExplicitAuthSession.mock.invocationCallOrder[0])
    expect(screen.getByText('operator')).toBeInTheDocument()
    await waitFor(() => expect(document.getElementById('catalog-editor')).toHaveFocus())
  })

  it('labels the catalog search and returns focus after clearing it', async () => {
    const user = userEvent.setup()
    render(<App />)

    const search = await screen.findByRole('searchbox', { name: 'Поиск файлов' })
    await user.type(search, 'bracket')
    await user.click(screen.getByRole('button', { name: 'Очистить поиск' }))

    expect(search).toHaveValue('')
    expect(search).toHaveFocus()
  })

	it('revokes and refuses an explicit session that cannot seal its provisional browser journal', async () => {
		mocks.getAuthSession.mockResolvedValueOnce({ authenticated: false, loginRequired: true, user: null })
		mocks.quarantineExplicitAuthSession.mockResolvedValueOnce(undefined)
		const user = userEvent.setup()
		render(<App />)
		await screen.findByRole('heading', { name: 'Вход в систему' })
		await user.type(screen.getByRole('textbox', { name: 'Имя пользователя' }), 'operator')
		await user.type(screen.getByLabelText('Пароль'), 'secret')
		await user.click(screen.getByRole('button', { name: 'Открыть каталог сетапов' }))

		expect(await screen.findByText(/не смог надёжно защитить новую сессию/i)).toBeInTheDocument()
		expect(mocks.logoutSessionIfCurrent).toHaveBeenCalledWith('remote-token')
		expect(mocks.getCapabilities).not.toHaveBeenCalled()
		expect(screen.queryByLabelText('Файлы сетапов')).not.toBeInTheDocument()
	})

	it('revokes and refuses an explicit session whose server activation cannot be confirmed', async () => {
		mocks.getAuthSession.mockResolvedValueOnce({ authenticated: false, loginRequired: true, user: null })
		mocks.activateExplicitAuthSession.mockResolvedValueOnce(false)
		const user = userEvent.setup()
		render(<App />)
		await screen.findByRole('heading', { name: 'Вход в систему' })
		await user.type(screen.getByRole('textbox', { name: 'Имя пользователя' }), 'operator')
		await user.type(screen.getByLabelText('Пароль'), 'secret')
		await user.click(screen.getByRole('button', { name: 'Открыть каталог сетапов' }))

		expect(await screen.findByText(/не смог подтвердить защищённую сессию/i)).toBeInTheDocument()
		expect(mocks.logoutSessionIfCurrent).toHaveBeenCalledWith('remote-token')
		expect(mocks.getCapabilities).not.toHaveBeenCalled()
	})

	it('revokes a journaled explicit session when its capability continuation fails', async () => {
		mocks.getAuthSession.mockResolvedValueOnce({ authenticated: false, loginRequired: true, user: null })
		mocks.getCapabilities.mockRejectedValueOnce(new ApiError({
			message: 'Configuration temporarily unavailable.', status: 503, code: 'UNAVAILABLE', retryable: true,
		}))
		const user = userEvent.setup()
		render(<App />)
		await screen.findByRole('heading', { name: 'Вход в систему' })
		await user.type(screen.getByRole('textbox', { name: 'Имя пользователя' }), 'operator')
		await user.type(screen.getByLabelText('Пароль'), 'secret')
		await user.click(screen.getByRole('button', { name: 'Открыть каталог сетапов' }))

		expect(await screen.findByRole('alert')).toHaveTextContent('Configuration temporarily unavailable.')
		expect(mocks.logoutSessionIfCurrent).toHaveBeenCalledWith('remote-token')
		expect(mocks.acceptExplicitAuthSession).not.toHaveBeenCalled()
		expect(screen.queryByLabelText('Файлы сетапов')).not.toBeInTheDocument()
	})

	 it('does not mount a stale authenticated workspace after a newer logout block', async () => {
		let release!: () => void
		let started!: () => void
		const gate = new Promise<void>((resolve) => { release = resolve })
		const matchStarted = new Promise<void>((resolve) => { started = resolve })
		let gated = false
		const cache = {
			match: vi.fn(async (request: Request) => {
				if (!gated && request.url.includes('kind=scope-block')) {
					gated = true
					started()
					await gate
				}
				return undefined
			}),
			put: vi.fn().mockResolvedValue(undefined),
			keys: vi.fn().mockResolvedValue([]),
			delete: vi.fn().mockResolvedValue(true),
		}
		vi.stubGlobal('caches', { open: vi.fn().mockResolvedValue(cache) })
		render(<App />)
		await matchStarted
		const newerToken = await blockGCodeCacheScope('local:LinuxCNC')
		release()

		expect(await screen.findByRole('heading', { name: 'Вход в систему' })).toBeInTheDocument()
		expect(screen.getByText(/Сессия изменилась во время входа/)).toBeInTheDocument()
		expect(screen.queryByLabelText('Файлы сетапов')).not.toBeInTheDocument()

		expect(await allowGCodeCacheScope('local:LinuxCNC', newerToken, captureGCodeCacheAuthGeneration())).toBe(true)
	 })

	it('invalidates a pending authentication continuation when an unauthorized response arrives', async () => {
		let resolveCapabilities!: (value: Awaited<ReturnType<typeof mocks.getCapabilities>>) => void
		mocks.getCapabilities.mockReturnValue(new Promise((resolve) => { resolveCapabilities = resolve }))
		render(<App />)
		await waitFor(() => expect(mocks.getCapabilities).toHaveBeenCalledTimes(1))
		const unauthorized = mocks.setUnauthorizedHandler.mock.calls
			.map((call) => call[0] as (() => void) | undefined)
			.find((handler) => typeof handler === 'function')!
		act(() => unauthorized())
		expect(await screen.findByRole('heading', { name: 'Вход в систему' })).toBeInTheDocument()

		await act(async () => {
			resolveCapabilities({ libraryAlias: 'LinuxCNC', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} })
			await Promise.resolve()
		})
		expect(screen.getByRole('heading', { name: 'Вход в систему' })).toBeInTheDocument()
		expect(screen.queryByLabelText('Файлы сетапов')).not.toBeInTheDocument()
	})

	it('revokes a PAM session whose login continuation lost to a concurrent logout generation', async () => {
		mocks.getAuthSession.mockResolvedValueOnce({ authenticated: false, loginRequired: true, user: null })
		let resolveCapabilities!: (value: Awaited<ReturnType<typeof mocks.getCapabilities>>) => void
		mocks.getCapabilities.mockReturnValue(new Promise((resolve) => { resolveCapabilities = resolve }))
		const user = userEvent.setup()
		render(<App />)
		await screen.findByRole('heading', { name: 'Вход в систему' })
		await user.type(screen.getByRole('textbox', { name: 'Имя пользователя' }), 'operator')
		await user.type(screen.getByLabelText('Пароль'), 'secret')
		await user.click(screen.getByRole('button', { name: 'Открыть каталог сетапов' }))
		await waitFor(() => expect(mocks.login).toHaveBeenCalledTimes(1))
		const newerToken = await blockGCodeCacheScope('user:operator:LinuxCNC')

		resolveCapabilities({ libraryAlias: 'LinuxCNC', gcodeExtensions: ['.ngc'], requireSetupSheetForReady: false, features: {} })
		expect(await screen.findByRole('heading', { name: 'Вход в систему' })).toBeInTheDocument()
		expect(mocks.logoutSessionIfCurrent).toHaveBeenCalledWith('remote-token')
		expect(screen.queryByLabelText('Файлы сетапов')).not.toBeInTheDocument()
		expect(await allowGCodeCacheScope('user:operator:LinuxCNC', newerToken, captureGCodeCacheAuthGeneration())).toBe(true)
	})

  it('renders the file tree on the left, the viewer on the right, and one exact LinuxCNC destination line', async () => {
    render(<App />)
    const editor = await screen.findByLabelText('Просмотр файла')
    const explorer = screen.getByLabelText('Файлы сетапов')
    expect(explorer.compareDocumentPosition(editor) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(await screen.findByTestId('gcode-preview')).toHaveTextContent('/api/v1/catalog/setups/setup-1/program/content')
    expect(screen.getAllByText('~/linuxcnc/nc_files', { exact: true }).length).toBeGreaterThan(0)
    expect(screen.getByText('~/linuxcnc/nc_files/Заказы/2026/bracket.ngc')).toBeInTheDocument()
    expect(screen.getByRole('treeitem', { name: /bracket\.pdf/ })).toHaveAttribute('aria-level', '5')
    expect(document.querySelector('.editor-breadcrumbs')).not.toBeInTheDocument()
    expect(document.querySelector('.editor-commandbar')).not.toBeInTheDocument()
    expect(screen.queryByText(/готовност|проверить сетап|текущий сетап/i)).not.toBeInTheDocument()
  })

  it('navigates Program, empty Toolpath, generated Tool Table, and Setup Sheet tabs by keyboard', async () => {
    const user = userEvent.setup()
    render(<App />)
    const programTab = await screen.findByRole('tab', { name: 'bracket.ngc' })
    programTab.focus()
    await user.keyboard('{ArrowRight}')
    const toolpathTab = screen.getByRole('tab', { name: 'Toolpath' })
    expect(toolpathTab).toHaveFocus()
    expect(screen.getByRole('heading', { name: 'Toolpath' })).toBeInTheDocument()
    await user.keyboard('{ArrowRight}')
    const toolTableTab = screen.getByRole('tab', { name: 'Tool Table' })
    expect(toolTableTab).toHaveFocus()
    expect(screen.getByText('Проверяем версию программы…')).toBeInTheDocument()
    act(() => previewMode.completeAnalysis?.())
    expect(await screen.findByRole('rowheader', { name: 'T7' })).toBeInTheDocument()
		expect(screen.getByRole('progressbar', { name: 'Прогресс индексации G-code' })).toHaveAttribute('value', '1')
		expect(screen.getByText('Индекс 100%')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Строка 12' }))
    expect(programTab).toHaveFocus()
    expect(programTab).toHaveAttribute('aria-selected', 'true')
    await user.keyboard('{End}')
    const sheetTab = screen.getByRole('tab', { name: /bracket\.pdf/ })
    expect(sheetTab).toHaveFocus()
    expect(sheetTab).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('setup-sheet-viewer')).toHaveAttribute('data-inline', 'true')
    await user.keyboard('{Home}')
    expect(programTab).toHaveFocus()
    expect(programTab).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByTestId('gcode-preview')).toBeInTheDocument()
  })

  it('does not hide a truncated Tool Table when no static tool was found in the processed prefix', async () => {
    const user = userEvent.setup()
    render(<App />)
    await user.click(await screen.findByRole('tab', { name: 'Tool Table' }))
    act(() => previewMode.completeTruncatedAnalysis?.())

    expect(await screen.findByRole('heading', { name: 'Инструменты не найдены' })).toBeInTheDocument()
    expect(screen.getByText(/В обработанной части программы/)).toBeInTheDocument()
    expect(screen.getByText(/Результат ограничен первыми 1024/)).toBeInTheDocument()
    expect(screen.queryByText('В программе нет статических целочисленных слов T.')).not.toBeInTheDocument()
  })

  it('keeps a legacy sheet-only record recoverable without making it a new-setup workflow', async () => {
    const user = userEvent.setup()
    mocks.getCatalog.mockResolvedValue({ ...snapshot, setups: [{ ...catalogSetup, program: null, programRelativePath: undefined }] })
    render(<App />)
    expect(await screen.findByTestId('setup-sheet-viewer')).toHaveAttribute('data-inline', 'true')
    await user.click(screen.getByRole('tab', { name: /Кронштейн/ }))
    expect(await screen.findByRole('heading', { name: 'Нужен G-code' })).toBeInTheDocument()
    const addProgram = screen.getAllByRole('button', { name: 'Добавить G-code' }).at(-1)!
    expect(addProgram).toBeEnabled()
    expect(addProgram).not.toHaveClass('workbench-button--primary')
  })

  it('directly uploads one G-code and one optional sheet without an application popup', async () => {
    const user = userEvent.setup()
    const created: CatalogSetup = { ...catalogSetup, setupId: 'new-setup', name: 'Новая деталь', revision: 1, program: null, setupSheet: null, programRelativePath: undefined, setupSheetRelativePath: undefined }
    const withProgram: CatalogSetup = { ...created, revision: 2, program: { ...catalogSetup.program!, artifactId: 'new-program', displayName: 'new.ngc', relativePath: 'Заказы/2026/new.ngc' }, programRelativePath: 'Заказы/2026/new.ngc' }
    const complete: CatalogSetup = { ...withProgram, revision: 3, setupSheet: { ...catalogSetup.setupSheet!, artifactId: 'new-sheet', displayName: 'new.pdf', relativePath: 'Заказы/2026/new.pdf' }, setupSheetRelativePath: 'Заказы/2026/new.pdf' }
    mocks.createCatalogSetup.mockResolvedValue(created)
    mocks.putCatalogComponent.mockResolvedValueOnce(withProgram).mockResolvedValueOnce(complete)
    render(<App />)
    await screen.findByLabelText('Файлы сетапов')
    const add = screen.getByRole('button', { name: 'Добавить' })
    await user.click(add)
    const program = new File(['G0 X0'], 'new.ngc', { type: 'text/plain' })
    const sheet = new File(['pdf'], 'new.pdf', { type: 'application/pdf' })
    await user.upload(screen.getByLabelText('Файлы нового сетапа'), [program, sheet])

    await waitFor(() => expect(mocks.createCatalogSetup).toHaveBeenCalledWith(
      { folderId: 'folder-2026', name: 'new' },
      'test-idempotency-key',
      expect.any(AbortSignal),
    ))
    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledTimes(2))
    expect(mocks.putCatalogComponent.mock.calls[0]?.slice(1, 3)).toEqual(['program', program])
    expect(mocks.putCatalogComponent.mock.calls[1]?.slice(1, 3)).toEqual(['setup-sheet', sheet])
    expect(await screen.findByText('Сохранено в LinuxCNC: ~/linuxcnc/nc_files/Заказы/2026/new.ngc')).toBeInTheDocument()
    expect(screen.queryByRole('dialog', { name: 'Загрузить сетап' })).not.toBeInTheDocument()
    expect(add).toHaveFocus()
  })

  it('replays a response-lost create with the same key before uploading the program', async () => {
    const user = userEvent.setup()
    const created: CatalogSetup = { ...catalogSetup, setupId: 'lost-create', name: 'lost', revision: 1, program: null, setupSheet: null, programRelativePath: undefined, setupSheetRelativePath: undefined }
    const complete: CatalogSetup = { ...created, revision: 2, program: { ...catalogSetup.program!, displayName: 'lost.ngc' }, programRelativePath: 'Заказы/2026/lost.ngc' }
    mocks.newIdempotencyKey.mockReturnValueOnce('create-replay-key').mockReturnValueOnce('program-replay-key')
    mocks.createCatalogSetup.mockRejectedValueOnce(new TypeError('Failed to fetch')).mockResolvedValueOnce(created)
    mocks.putCatalogComponent.mockResolvedValue(complete)
    render(<App />)
    await screen.findByLabelText('Файлы сетапов')

    await user.upload(screen.getByLabelText('Файлы нового сетапа'), new File(['G0'], 'lost.ngc', { type: 'text/plain' }))
    await user.click(await screen.findByRole('button', { name: 'Повторить: lost.ngc' }))

    await waitFor(() => expect(mocks.createCatalogSetup).toHaveBeenCalledTimes(2))
    expect(mocks.createCatalogSetup.mock.calls[0]?.[1]).toBe('create-replay-key')
    expect(mocks.createCatalogSetup.mock.calls[1]?.[1]).toBe('create-replay-key')
    expect(mocks.putCatalogComponent).toHaveBeenCalledTimes(1)
    expect(mocks.putCatalogComponent.mock.calls[0]?.[3]).toBe('program-replay-key')
  })

  it('replays a structurally invalid create response with the same key', async () => {
    const user = userEvent.setup()
    const created: CatalogSetup = { ...catalogSetup, setupId: 'invalid-create', name: 'invalid', revision: 1, program: null, setupSheet: null, programRelativePath: undefined, setupSheetRelativePath: undefined }
    const complete: CatalogSetup = { ...created, revision: 2, program: { ...catalogSetup.program!, displayName: 'invalid.ngc' }, programRelativePath: 'Заказы/2026/invalid.ngc' }
    mocks.newIdempotencyKey.mockReturnValueOnce('invalid-create-key').mockReturnValueOnce('invalid-program-key')
    mocks.createCatalogSetup
      .mockRejectedValueOnce(new ApiError({ message: 'Missing setupId.', status: 0, code: 'INVALID_RESPONSE' }))
      .mockResolvedValueOnce(created)
    mocks.putCatalogComponent.mockResolvedValue(complete)
    render(<App />)
    await screen.findByLabelText('Файлы сетапов')

    await user.upload(screen.getByLabelText('Файлы нового сетапа'), new File(['G0'], 'invalid.ngc', { type: 'text/plain' }))
    await user.click(await screen.findByRole('button', { name: 'Повторить: invalid.ngc' }))

    await waitFor(() => expect(mocks.createCatalogSetup).toHaveBeenCalledTimes(2))
    expect(mocks.createCatalogSetup.mock.calls[0]?.[1]).toBe('invalid-create-key')
    expect(mocks.createCatalogSetup.mock.calls[1]?.[1]).toBe('invalid-create-key')
    expect(mocks.putCatalogComponent.mock.calls[0]?.[3]).toBe('invalid-program-key')
  })

  it('resumes a response-lost program upload without creating another Setup', async () => {
    const user = userEvent.setup()
    const created: CatalogSetup = { ...catalogSetup, setupId: 'lost-program', name: 'resume', revision: 1, program: null, setupSheet: null, programRelativePath: undefined, setupSheetRelativePath: undefined }
    const complete: CatalogSetup = { ...created, revision: 2, program: { ...catalogSetup.program!, displayName: 'resume.ngc' }, programRelativePath: 'Заказы/2026/resume.ngc' }
    mocks.newIdempotencyKey.mockReturnValueOnce('create-once-key').mockReturnValueOnce('program-stable-key')
    mocks.createCatalogSetup.mockResolvedValue(created)
    mocks.putCatalogComponent.mockRejectedValueOnce(new TypeError('connection lost')).mockResolvedValueOnce(complete)
    render(<App />)
    await screen.findByLabelText('Файлы сетапов')

    await user.upload(screen.getByLabelText('Файлы нового сетапа'), new File(['G0'], 'resume.ngc', { type: 'text/plain' }))
    await user.click(await screen.findByRole('button', { name: 'Повторить: resume.ngc' }))

    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledTimes(2))
    expect(mocks.createCatalogSetup).toHaveBeenCalledTimes(1)
    expect(mocks.putCatalogComponent.mock.calls[0]?.[3]).toBe('program-stable-key')
    expect(mocks.putCatalogComponent.mock.calls[1]?.[3]).toBe('program-stable-key')
  })

  it('resumes only a response-lost Sheet step with its original key', async () => {
    const user = userEvent.setup()
    const created: CatalogSetup = { ...catalogSetup, setupId: 'lost-sheet', name: 'paired', revision: 1, program: null, setupSheet: null, programRelativePath: undefined, setupSheetRelativePath: undefined }
    const withProgram: CatalogSetup = { ...created, revision: 2, program: { ...catalogSetup.program!, displayName: 'paired.ngc' }, programRelativePath: 'Заказы/2026/paired.ngc' }
    const complete: CatalogSetup = { ...withProgram, revision: 3, setupSheet: { ...catalogSetup.setupSheet!, displayName: 'paired.pdf' }, setupSheetRelativePath: 'Заказы/2026/paired.pdf' }
    mocks.newIdempotencyKey.mockReturnValueOnce('paired-create-key').mockReturnValueOnce('paired-program-key').mockReturnValueOnce('paired-sheet-key')
    mocks.createCatalogSetup.mockResolvedValue(created)
    mocks.putCatalogComponent.mockResolvedValueOnce(withProgram).mockRejectedValueOnce(new TypeError('connection lost')).mockResolvedValueOnce(complete)
    render(<App />)
    await screen.findByLabelText('Файлы сетапов')

    await user.upload(screen.getByLabelText('Файлы нового сетапа'), [
      new File(['G0'], 'paired.ngc', { type: 'text/plain' }),
      new File(['pdf'], 'paired.pdf', { type: 'application/pdf' }),
    ])
    await user.click(await screen.findByRole('button', { name: 'Повторить: paired.ngc + paired.pdf' }))

    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledTimes(3))
    expect(mocks.createCatalogSetup).toHaveBeenCalledTimes(1)
    expect(mocks.putCatalogComponent.mock.calls[0]?.slice(1, 4)).toEqual(['program', expect.any(File), 'paired-program-key'])
    expect(mocks.putCatalogComponent.mock.calls[1]?.slice(1, 4)).toEqual(['setup-sheet', expect.any(File), 'paired-sheet-key'])
    expect(mocks.putCatalogComponent.mock.calls[2]?.slice(1, 4)).toEqual(['setup-sheet', expect.any(File), 'paired-sheet-key'])
  })

  it('shows a lone G-code as a leaf and attaches its Setup Sheet directly beneath it', async () => {
    const user = userEvent.setup()
    const programOnly = { ...catalogSetup, setupSheet: null, setupSheetRelativePath: undefined }
    const withSheet = { ...catalogSetup, revision: programOnly.revision + 1 }
    mocks.getCatalog.mockResolvedValue({ ...snapshot, setups: [programOnly] })
    mocks.putCatalogComponent.mockResolvedValue(withSheet)
    render(<App />)

    const programNode = await screen.findByRole('treeitem', { name: /bracket\.ngc/ })
    expect(programNode).not.toHaveAttribute('aria-expanded')
    expect(screen.queryByRole('treeitem', { name: /bracket\.pdf/ })).not.toBeInTheDocument()

    const attach = screen.getByRole('button', { name: /Setup Sheet/ })
    await user.click(attach)
    const sheet = new File(['pdf'], 'bracket.pdf', { type: 'application/pdf' })
    await user.upload(screen.getByLabelText('Выбрать Setup Sheet'), sheet)

    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledWith(
      expect.objectContaining({ setupId: 'setup-1', setupSheet: null }),
      'setup-sheet',
      sheet,
      'test-idempotency-key',
      expect.any(Object),
    ))
    expect(await screen.findByRole('treeitem', { name: /bracket\.pdf/ })).toHaveAttribute('aria-level', '5')
    expect(screen.getByTestId('setup-sheet-viewer')).toHaveAttribute('data-inline', 'true')
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('tab', { name: /bracket\.pdf/ })).toHaveFocus())
  })

  it('cancels and safely replays a direct Sheet attachment with one stable key', async () => {
    const user = userEvent.setup()
    const programOnly = { ...catalogSetup, setupSheet: null, setupSheetRelativePath: undefined }
    const withSheet = { ...catalogSetup, revision: programOnly.revision + 1, setupSheet: { ...catalogSetup.setupSheet!, displayName: 'attach.pdf' } }
    let aborted = false
    mocks.getCatalog.mockResolvedValue({ ...snapshot, setups: [programOnly] })
    mocks.newIdempotencyKey.mockReturnValueOnce('attach-stable-key')
    mocks.putCatalogComponent.mockImplementationOnce((
      _setup: CatalogSetup,
      _component: string,
      _file: File,
      _key: string,
      options: { signal?: AbortSignal },
    ) => new Promise((_resolve, reject) => {
      options.signal?.addEventListener('abort', () => {
        aborted = true
        reject(new DOMException('cancelled', 'AbortError'))
      }, { once: true })
    })).mockResolvedValueOnce(withSheet)
    render(<App />)
    await screen.findByRole('treeitem', { name: /bracket\.ngc/ })

    await user.click(screen.getByRole('button', { name: /Setup Sheet/ }))
    await user.upload(screen.getByLabelText('Выбрать Setup Sheet'), new File(['pdf'], 'attach.pdf', { type: 'application/pdf' }))
    await user.click(await screen.findByRole('button', { name: 'Отменить' }))
    expect(aborted).toBe(true)
    await user.click(await screen.findByRole('button', { name: 'Повторить: attach.pdf' }))

    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledTimes(2))
    expect(mocks.putCatalogComponent.mock.calls[0]?.[3]).toBe('attach-stable-key')
    expect(mocks.putCatalogComponent.mock.calls[1]?.[3]).toBe('attach-stable-key')
    expect(mocks.createCatalogSetup).not.toHaveBeenCalled()
    expect(await screen.findByRole('treeitem', { name: /attach\.pdf/ })).toBeInTheDocument()
  })

  it('replays a structurally invalid PUT response with the same key', async () => {
    const user = userEvent.setup()
    const programOnly = { ...catalogSetup, setupSheet: null, setupSheetRelativePath: undefined }
    const withSheet = { ...catalogSetup, revision: programOnly.revision + 1, setupSheet: { ...catalogSetup.setupSheet!, displayName: 'invalid-put.pdf' } }
    mocks.getCatalog.mockResolvedValue({ ...snapshot, setups: [programOnly] })
    mocks.newIdempotencyKey.mockReturnValueOnce('invalid-put-key')
    mocks.putCatalogComponent
      .mockRejectedValueOnce(new ApiError({ message: 'Missing revision.', status: 0, code: 'INVALID_RESPONSE' }))
      .mockResolvedValueOnce(withSheet)
    render(<App />)
    await screen.findByRole('treeitem', { name: /bracket\.ngc/ })

    await user.click(screen.getByRole('button', { name: /Setup Sheet/ }))
    await user.upload(screen.getByLabelText('Выбрать Setup Sheet'), new File(['pdf'], 'invalid-put.pdf', { type: 'application/pdf' }))
    await user.click(await screen.findByRole('button', { name: 'Повторить: invalid-put.pdf' }))

    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledTimes(2))
    expect(mocks.putCatalogComponent.mock.calls[0]?.[3]).toBe('invalid-put-key')
    expect(mocks.putCatalogComponent.mock.calls[1]?.[3]).toBe('invalid-put-key')
  })

  it('uses a fresh revision, file, and key after a deterministic upload conflict', async () => {
    const user = userEvent.setup()
    const programOnly = { ...catalogSetup, setupSheet: null, setupSheetRelativePath: undefined }
    const refreshed = { ...programOnly, revision: programOnly.revision + 1 }
    const withSheet = {
      ...refreshed,
      revision: refreshed.revision + 1,
      setupSheet: { ...catalogSetup.setupSheet!, displayName: 'same.pdf' },
      setupSheetRelativePath: 'Заказы/2026/same.pdf',
    }
    const firstFile = new File(['aaa'], 'same.pdf', { type: 'application/pdf', lastModified: 1234 })
    const correctedFile = new File(['bbb'], 'same.pdf', { type: 'application/pdf', lastModified: 1234 })
    expect([firstFile.size, firstFile.lastModified, firstFile.type]).toEqual([
      correctedFile.size,
      correctedFile.lastModified,
      correctedFile.type,
    ])
    mocks.getCatalog.mockResolvedValueOnce({ ...snapshot, setups: [programOnly] })
      .mockResolvedValue({ ...snapshot, generation: 'generation-2', setups: [refreshed] })
    mocks.newIdempotencyKey.mockReturnValueOnce('conflicted-key').mockReturnValueOnce('fresh-key')
    mocks.putCatalogComponent
      .mockRejectedValueOnce(new ApiError({ message: 'Expected revision 3, actual 4.', status: 409, code: 'REVISION_CONFLICT' }))
      .mockResolvedValueOnce(withSheet)
    render(<App />)
    await screen.findByRole('treeitem', { name: /bracket\.ngc/ })

    await user.click(screen.getByRole('button', { name: /Setup Sheet/ }))
    await user.upload(screen.getByLabelText('Выбрать Setup Sheet'), firstFile)
    expect(await screen.findByRole('alert')).toHaveTextContent('Expected revision 3, actual 4.')
    expect(screen.queryByRole('button', { name: /Повторить:/ })).not.toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Обновить каталог' }))
    await waitFor(() => expect(mocks.getCatalog).toHaveBeenCalledTimes(2))
    await user.click(screen.getByRole('button', { name: /Setup Sheet/ }))
    await user.upload(screen.getByLabelText('Выбрать Setup Sheet'), correctedFile)

    await waitFor(() => expect(mocks.putCatalogComponent).toHaveBeenCalledTimes(2))
    expect(mocks.putCatalogComponent.mock.calls[0]?.[0]).toMatchObject({ setupId: 'setup-1', revision: 3 })
    expect(mocks.putCatalogComponent.mock.calls[0]?.[2]).toBe(firstFile)
    expect(mocks.putCatalogComponent.mock.calls[0]?.[3]).toBe('conflicted-key')
    expect(mocks.putCatalogComponent.mock.calls[1]?.[0]).toMatchObject({ setupId: 'setup-1', revision: 4 })
    expect(mocks.putCatalogComponent.mock.calls[1]?.[2]).toBe(correctedFile)
    expect(mocks.putCatalogComponent.mock.calls[1]?.[3]).toBe('fresh-key')
  })

  it('supports keyboard resizing of the left explorer', async () => {
    const user = userEvent.setup()
    render(<App />)
    const separator = await screen.findByRole('separator', { name: 'Изменить ширину дерева файлов' })
    expect(separator).toHaveAttribute('aria-valuenow', '320')
    separator.focus()
    await user.keyboard('{ArrowRight}{ArrowRight}')
    expect(separator).toHaveAttribute('aria-valuenow', '352')
    await user.keyboard('{Home}')
    expect(separator).toHaveAttribute('aria-valuenow', '260')
  })

  it('opens the left explorer as a keyboard-dismissible mobile drawer', async () => {
    const user = userEvent.setup()
    vi.stubGlobal('matchMedia', vi.fn().mockReturnValue({ matches: true }))
    render(<App />)
    const toggle = await screen.findByRole('button', { name: 'Открыть дерево файлов' })
    await user.click(toggle)
    const explorer = screen.getByLabelText('Файлы сетапов')
    const editor = screen.getByLabelText('Просмотр файла')
    expect(explorer).toHaveClass('catalog-explorer--open')
    expect(editor).toHaveAttribute('inert')
    expect(screen.getByRole('button', { name: 'Закрыть дерево файлов' })).toHaveFocus()
    editor.focus()
    fireEvent.keyDown(window, { key: 'Tab' })
    const firstFocusable = explorer.querySelector<HTMLElement>('button:not(:disabled):not([tabindex="-1"])')
    expect(firstFocusable).toHaveFocus()
    fireEvent.keyDown(window, { key: 'Tab', shiftKey: true })
    const focusable = explorer.querySelectorAll<HTMLElement>('button:not(:disabled):not([tabindex="-1"]), input:not(:disabled):not([tabindex="-1"]), [tabindex]:not([tabindex="-1"])')
    expect(focusable[focusable.length - 1]).toHaveFocus()
    await user.keyboard('{Escape}')
    expect(explorer).not.toHaveClass('catalog-explorer--open')
    expect(editor).not.toHaveAttribute('inert')
    await waitFor(() => expect(toggle).toHaveFocus())
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
    await screen.findByLabelText('Файлы сетапов')
    await user.click(screen.getByRole('button', { name: 'Новый каталог' }))
    await user.type(screen.getByRole('textbox', { name: 'Название' }), 'Серия 42')
    expect(screen.getByText('~/linuxcnc/nc_files/Серия 42')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Создать каталог' }))
    await waitFor(() => expect(mocks.createCatalogFolder).toHaveBeenCalledWith(undefined, 'Серия 42', 'test-idempotency-key'))
  })

  it('preserves authentication expiry and logout behavior around the workbench', async () => {
    mocks.getAuthSession.mockResolvedValue({ authenticated: true, loginRequired: true, user: { username: 'operator' }, csrfToken: 'remote-token' })
    render(<App />)
    await screen.findByLabelText('Файлы сетапов')
    const handler = mocks.setUnauthorizedHandler.mock.calls.map(([candidate]) => candidate as (() => void) | undefined).find(Boolean)
    act(() => handler?.())
    expect(await screen.findByRole('status')).toHaveTextContent('Сессия истекла')
    expect(screen.queryByLabelText('Файлы сетапов')).not.toBeInTheDocument()
    expect(screen.getByRole('textbox', { name: 'Имя пользователя' })).toHaveFocus()
  })

	it('waits for an expired-session cache cleanup before allowing the same principal again', async () => {
		mocks.getAuthSession.mockResolvedValue({ authenticated: true, loginRequired: true, user: { username: 'operator' }, csrfToken: 'remote-token' })
		let releaseCleanup!: () => void
		let cleanupStarted!: () => void
		const cleanupGate = new Promise<void>((resolve) => { releaseCleanup = resolve })
		const started = new Promise<void>((resolve) => { cleanupStarted = resolve })
		const values = new Map<string, Response>()
		let gateKeys = false
		vi.stubGlobal('caches', { open: vi.fn().mockResolvedValue({
			match: (request: Request) => Promise.resolve(values.get(request.url)?.clone()),
			put: (request: Request, response: Response) => { values.set(request.url, response.clone()); return Promise.resolve() },
			delete: (request: Request) => Promise.resolve(values.delete(request.url)),
			keys: async () => {
				if (gateKeys) {
					cleanupStarted()
					await cleanupGate
				}
				return [...values.keys()].map((url) => new Request(url))
			},
		}) })
		const user = userEvent.setup()
		render(<App />)
		await screen.findByLabelText('Файлы сетапов')
		gateKeys = true
		const handler = mocks.setUnauthorizedHandler.mock.calls
			.map(([candidate]) => candidate as (() => void) | undefined)
			.find(Boolean)
		act(() => handler?.())
		await started
		await user.type(screen.getByRole('textbox', { name: 'Имя пользователя' }), 'operator')
		await user.type(screen.getByLabelText('Пароль'), 'secret')
		await user.click(screen.getByRole('button', { name: 'Открыть каталог сетапов' }))
		expect(mocks.login).not.toHaveBeenCalled()
		expect(screen.queryByLabelText('Файлы сетапов')).not.toBeInTheDocument()
		expect(screen.getByRole('button', { name: 'Выполняется вход…' })).toBeDisabled()
		releaseCleanup()
		await waitFor(() => expect(mocks.login).toHaveBeenCalled())
		expect(await screen.findByLabelText('Файлы сетапов')).toBeInTheDocument()
	}, 15_000)

  it('unblocks the cache and remounts the preview after a failed logout', async () => {
    mocks.getAuthSession.mockResolvedValue({ authenticated: true, loginRequired: true, user: { username: 'operator' }, csrfToken: 'remote-token' })
    mocks.logout.mockRejectedValue(new TypeError('network down'))
    const user = userEvent.setup()
    render(<App />)
    await screen.findByTestId('gcode-preview')
    const mountsBefore = previewMode.mountCount
    await user.click(screen.getByRole('button', { name: 'Выйти' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Не удалось выйти')
    expect(screen.getByTestId('gcode-preview')).toBeInTheDocument()
    await waitFor(() => expect(previewMode.mountCount).toBeGreaterThan(mountsBefore))
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
    fireEvent(window, new MouseEvent('pointermove', { bubbles: true, clientX: 1050 }))
    expect(separator).toHaveAttribute('aria-valuenow', '370')
    fireEvent(window, new MouseEvent('pointercancel', { bubbles: true }))
    expect(document.body).not.toHaveClass('catalog-resizing')
    expect(screen.getByTestId('gcode-preview')).toHaveTextContent('bracket.ngc')
  })
})
