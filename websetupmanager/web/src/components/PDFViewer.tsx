import { useEffect, useRef, useState } from 'react'
import type { PDFDocumentLoadingTask, PDFDocumentProxy } from 'pdfjs-dist/types/src/display/api'
import type { Artifact } from '../domain'
import { pdfCanvasGeometry } from '../pdfGeometry'

interface Props {
  artifact: Artifact
  url: string
  onError: () => void
}

export function PDFViewer({ artifact, url, onError }: Props) {
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [documentProxy, setDocumentProxy] = useState<PDFDocumentProxy>()
  const [pageNumber, setPageNumber] = useState(1)
  const [scale, setScale] = useState(1)
  const [progress, setProgress] = useState(0)
  const [error, setError] = useState(false)

	useEffect(() => {
		setDocumentProxy(undefined)
		setPageNumber(1)
		setError(false)
		let disposed = false
		let loading: PDFDocumentLoadingTask | undefined
		void import('pdfjs-dist').then((pdfjs) => {
			if (disposed) return
			pdfjs.GlobalWorkerOptions.workerSrc = new URL('pdfjs-dist/build/pdf.worker.min.mjs', import.meta.url).toString()
			loading = pdfjs.getDocument({
				url,
				httpHeaders: { 'If-Match': `"${artifact.version}"` },
				withCredentials: false,
				isEvalSupported: false,
				enableXfa: false,
				useSystemFonts: true,
			})
			loading.onProgress = ({ loaded, total }: { loaded: number; total: number }) => setProgress(total > 0 ? loaded / total : 0)
			loading.onPassword = () => {
				setError(true)
				onError()
				void loading?.destroy()
			}
			void loading.promise.then((document) => {
				if (!disposed) setDocumentProxy(document)
			}, () => {
				if (disposed) return
				setError(true)
				onError()
			})
		}, () => {
			if (disposed) return
			setError(true)
			onError()
		})
		return () => {
			disposed = true
			void loading?.destroy()
		}
  }, [artifact.version, onError, url])

  useEffect(() => {
    if (!documentProxy || !canvasRef.current) return
    let cancelled = false
    let renderTask: { cancel: () => void; promise: Promise<void> } | undefined
    void documentProxy.getPage(pageNumber).then((page) => {
      if (cancelled || !canvasRef.current) return
      const viewport = page.getViewport({ scale })
      const geometry = pdfCanvasGeometry(viewport.width, viewport.height, window.devicePixelRatio || 1)
      const canvas = canvasRef.current
      const context = canvas.getContext('2d', { alpha: false })
      if (!context) throw new Error('CANVAS_UNAVAILABLE')
      canvas.width = geometry.width
      canvas.height = geometry.height
      canvas.style.width = `${geometry.cssWidth}px`
      canvas.style.height = `${geometry.cssHeight}px`
      renderTask = page.render({
        canvas, canvasContext: context, viewport,
        transform: geometry.ratio === 1 ? undefined : [geometry.ratio, 0, 0, geometry.ratio, 0, 0],
        annotationMode: 0,
      })
      return renderTask.promise
    }).catch((reason: unknown) => {
      if (cancelled || (reason instanceof Error && reason.name === 'RenderingCancelledException')) return
      setError(true)
      onError()
    })
    return () => {
      cancelled = true
      renderTask?.cancel()
    }
  }, [documentProxy, onError, pageNumber, scale])

  if (error) {
    return <div className="viewer-error" role="alert">PDF повреждён, защищён паролем, изменён или имеет небезопасный размер страницы. Закройте viewer и замените Setup Sheet.</div>
  }

  return (
    <div className="pdf-viewer">
      <div className="viewer-toolbar" aria-label="Управление PDF">
        <button type="button" onClick={() => setPageNumber((value) => Math.max(1, value - 1))} disabled={!documentProxy || pageNumber <= 1}>←</button>
        <label>Страница <input type="number" min={1} max={documentProxy?.numPages ?? 1} value={pageNumber} onChange={(event) => setPageNumber(Math.max(1, Math.min(documentProxy?.numPages ?? 1, Number(event.target.value))))} /></label>
        <span>из {documentProxy?.numPages ?? '…'}</span>
        <button type="button" onClick={() => setScale((value) => Math.max(0.5, value - 0.25))} aria-label="Уменьшить масштаб">−</button>
        <output>{Math.round(scale * 100)}%</output>
        <button type="button" onClick={() => setScale((value) => Math.min(4, value + 0.25))} aria-label="Увеличить масштаб">+</button>
        {!documentProxy ? <span role="status">Загрузка {Math.round(progress * 100)}%</span> : null}
      </div>
      <div className="pdf-canvas-scroll" tabIndex={0} aria-label={`PDF ${artifact.displayName}, страница ${pageNumber}`}>
        <canvas ref={canvasRef} />
      </div>
      <p className="viewer-safety-note">Интерактивные действия, JavaScript и внешние ссылки PDF отключены.</p>
    </div>
  )
}
