import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { App } from './App'

function capabilitiesResponse(alias = 'Производственные сетапы'): Response {
  return new Response(JSON.stringify({
    library_alias: alias,
    csrf_token: 'token',
  }), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('App', () => {
  it('shows a semantic loading state and then the setup library shell', async () => {
    let resolveFetch: ((response: Response) => void) | undefined
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>((resolve) => {
      resolveFetch = resolve
    })))

    render(<App />)
    expect(screen.getByRole('heading', { name: 'Загружаем библиотеку сетапов' }))
      .toBeInTheDocument()
    expect(screen.getByText('Проверяем локальный Backend и управляемое хранилище…'))
      .toHaveAttribute('role', 'status')

    resolveFetch?.(capabilitiesResponse())
    expect(await screen.findByRole('heading', { name: 'Производственные сетапы' }))
      .toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Не выбран' })).toBeInTheDocument()
    expect(screen.getByText(/каждый сетап объединяет G-code-программы/i))
      .toBeInTheDocument()
    expect(screen.queryByText(/файловое дерево/i)).not.toBeInTheDocument()
  })

  it('shows a recoverable local Backend unavailable state and retries', async () => {
    const fetchMock = vi.fn()
      .mockRejectedValueOnce(new TypeError('connection refused'))
      .mockResolvedValueOnce(capabilitiesResponse('Сетапы после reconnect'))
    vi.stubGlobal('fetch', fetchMock)

    render(<App />)
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('Локальный Backend недоступен')
    fireEvent.click(screen.getByRole('button', { name: 'Повторить подключение' }))

    expect(await screen.findByRole('heading', { name: 'Сетапы после reconnect' }))
      .toBeInTheDocument()
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })

  it('announces loss of external network without hiding a loaded local library', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(capabilitiesResponse()))
    render(<App />)
    await screen.findByRole('heading', { name: 'Производственные сетапы' })

    fireEvent(window, new Event('offline'))
    await waitFor(() => {
      expect(screen.getByRole('status')).toHaveTextContent(
        'Setup Manager продолжает работать с локальным Backend',
      )
    })
    expect(screen.getByRole('heading', { name: 'Производственные сетапы' }))
      .toBeInTheDocument()
  })
})
