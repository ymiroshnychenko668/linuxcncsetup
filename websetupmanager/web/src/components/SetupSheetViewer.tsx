import { useCallback, useEffect, useRef, useState } from 'react'
import Alert from '@mui/material/Alert'
import Button from '@mui/material/Button'
import CircularProgress from '@mui/material/CircularProgress'
import IconButton from '@mui/material/IconButton'
import type { Artifact, Setup } from '../domain'
import { Modal } from './Modal'
import { PDFViewer } from './PDFViewer'

interface Props {
  setup: Setup
  artifact: Artifact
  contentUrl?: string
  onClose?: () => void
  onReplace: (trigger?: HTMLElement) => void
  inline?: boolean
}

// The backend writes this only after a clean, version-consistent source EOF.
// Source comments are removed by the sanitizer, so uploaded content cannot
// forge a successful tail after an interrupted streaming response.
const sanitizedHTMLCompletionSuffix = '</body></html><!--websetupmanager:sanitized-html-complete:v1-->'

export function SetupSheetViewer({ setup, artifact, contentUrl, onClose, onReplace, inline = false }: Props) {
  const surfaceRef = useRef<HTMLDivElement>(null)
  const [failed, setFailed] = useState(false)
  const [htmlDocument, setHTMLDocument] = useState<{ source: string; objectURL: string }>()
  const [htmlScale, setHTMLScale] = useState(1)
  const contentURL = contentUrl ?? `/api/v1/setups/${encodeURIComponent(setup.setupId)}/setup-sheet/content`
  const versionedContentURL = `${contentURL}?version=${encodeURIComponent(artifact.version)}`
  const htmlReady = htmlDocument?.source === versionedContentURL
  const handleError = useCallback(() => setFailed(true), [])
  const requestFullscreen = useCallback(() => {
    void surfaceRef.current?.requestFullscreen?.()
  }, [])

  useEffect(() => {
    setFailed(false)
    setHTMLDocument(undefined)
    setHTMLScale(1)
    if (artifact.mediaType === 'application/pdf') return

    const controller = new AbortController()
    let objectURL: string | undefined
    void fetch(versionedContentURL, {
      method: 'GET',
      headers: {
        Accept: 'text/html',
        'If-Match': `"${artifact.version}"`,
      },
      credentials: 'same-origin',
      cache: 'no-store',
      redirect: 'error',
      referrerPolicy: 'no-referrer',
      signal: controller.signal,
    }).then(async (response) => {
      const contentType = response.headers.get('content-type')?.toLowerCase() ?? ''
      if (!response.ok
        || response.headers.get('etag') !== `"${artifact.version}"`
        || !contentType.startsWith('text/html')) {
        throw new Error('SETUP_SHEET_VERSION_MISMATCH')
      }
      const sanitizedDocument = await response.blob()
      const completion = await sanitizedDocument.slice(-sanitizedHTMLCompletionSuffix.length).text()
      if (completion !== sanitizedHTMLCompletionSuffix) {
        throw new Error('SETUP_SHEET_INCOMPLETE')
      }
      if (controller.signal.aborted) return
      objectURL = URL.createObjectURL(new Blob([sanitizedDocument], { type: 'text/html;charset=utf-8' }))
      setHTMLDocument({ source: versionedContentURL, objectURL })
    }).catch((reason: unknown) => {
      if (controller.signal.aborted || (reason instanceof DOMException && reason.name === 'AbortError')) return
      setFailed(true)
    })
    return () => {
      controller.abort()
      if (objectURL) URL.revokeObjectURL(objectURL)
    }
  }, [artifact.mediaType, artifact.version, versionedContentURL])

  const surface = (
    <div ref={surfaceRef} className="sheet-viewer-surface">
      {failed ? (
        <Alert className="viewer-error" severity="error" role="alert" icon={false}>
          Setup Sheet не удалось показать. Документ мог быть повреждён или изменён.
          <Button type="button" className="button button--quiet" variant="outlined" onClick={(event) => onReplace(event.currentTarget)}>Заменить документ</Button>
        </Alert>
      ) : artifact.mediaType === 'application/pdf' ? (
        <PDFViewer artifact={artifact} url={contentURL} onError={handleError} />
      ) : (
        <div className="html-sheet-viewer">
          <div className="viewer-toolbar" role="toolbar" aria-label="Управление HTML Setup Sheet">
            <IconButton type="button" size="small" onClick={() => setHTMLScale((value) => Math.max(0.5, value - 0.25))} aria-label="Уменьшить масштаб">−</IconButton>
            <output aria-label="Масштаб HTML Setup Sheet">{Math.round(htmlScale * 100)}%</output>
            <IconButton type="button" size="small" onClick={() => setHTMLScale((value) => Math.min(4, value + 0.25))} aria-label="Увеличить масштаб">+</IconButton>
            {!htmlReady ? <span role="status"><CircularProgress size="1em" aria-hidden="true" /> Загружаем Setup Sheet…</span> : null}
          </div>
          <div className="html-sheet-scroll" tabIndex={0} aria-label={`HTML Setup Sheet ${artifact.displayName}`}>
            {htmlReady ? (
              <iframe
                className="html-sheet-frame"
                title={`Setup Sheet ${artifact.displayName}`}
                src={htmlDocument.objectURL}
                sandbox=""
                referrerPolicy="no-referrer"
                onError={handleError}
                style={{ zoom: htmlScale, width: `${100 / htmlScale}%`, height: `${100 / htmlScale}%` }}
              />
            ) : null}
          </div>
        </div>
      )}
    </div>
  )

  if (inline) {
    return <section className="sheet-viewer-inline" aria-label={`Setup Sheet ${artifact.displayName}`}>{surface}</section>
  }

  return (
    <Modal
      title={artifact.displayName}
      description={`Setup Sheet · ${artifact.mediaType === 'application/pdf' ? 'PDF' : 'HTML'} · ${artifact.byteSize.toLocaleString()} байт`}
      onClose={onClose ?? (() => undefined)}
      className="sheet-viewer-modal"
      footer={(
        <>
          <Button type="button" className="button button--quiet" variant="outlined" onClick={requestFullscreen}>На весь экран</Button>
          <Button type="button" className="button button--quiet" variant="outlined" onClick={(event) => onReplace(event.currentTarget)}>Заменить</Button>
          <Button type="button" className="button button--primary" variant="contained" onClick={onClose}>Закрыть</Button>
        </>
      )}
    >
      {surface}
    </Modal>
  )
}
