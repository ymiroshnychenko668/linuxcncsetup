const MAX_PDF_CSS_EDGE = 8192
const MAX_PDF_CSS_PIXELS = 32_000_000
const MAX_PDF_CANVAS_EDGE = 4096
const MAX_PDF_CANVAS_PIXELS = 4_000_000
const MAX_PDF_ACCESSIBLE_TEXT = 100_000
const MAX_PDF_ACCESSIBLE_ITEMS = 20_000
const PDF_TRUNCATED_SUFFIX = ' … [текст страницы сокращён]'

interface PDFCanvasGeometry {
  width: number
  height: number
  ratio: number
  cssWidth: number
  cssHeight: number
}

// A PDF MediaBox is untrusted input. Keep both layout geometry and the backing
// RGBA canvas bounded before assigning dimensions, while retaining CSS zoom
// through a lower-resolution backing canvas when required.
export function pdfCanvasGeometry(width: number, height: number, devicePixelRatio: number): PDFCanvasGeometry {
  if (!Number.isFinite(width) || !Number.isFinite(height) || width <= 0 || height <= 0 ||
      width > MAX_PDF_CSS_EDGE || height > MAX_PDF_CSS_EDGE || width * height > MAX_PDF_CSS_PIXELS) {
    throw new Error('PDF_PAGE_DIMENSIONS_UNSAFE')
  }
  const desiredRatio = Math.min(2, Math.max(1, Number.isFinite(devicePixelRatio) ? devicePixelRatio : 1))
  const ratio = Math.min(
    desiredRatio,
    MAX_PDF_CANVAS_EDGE / width,
    MAX_PDF_CANVAS_EDGE / height,
    Math.sqrt(MAX_PDF_CANVAS_PIXELS / (width * height)),
  )
  if (!Number.isFinite(ratio) || ratio <= 0) throw new Error('PDF_PAGE_DIMENSIONS_UNSAFE')
  return {
    width: Math.max(1, Math.floor(width * ratio)),
    height: Math.max(1, Math.floor(height * ratio)),
    ratio,
    cssWidth: Math.max(1, Math.floor(width)),
    cssHeight: Math.max(1, Math.floor(height)),
  }
}

// PDF.js exposes text items as an untrusted heterogeneous array. Build a
// bounded plain-text alternative for assistive technology without trusting
// item shape or retaining an unbounded joined string.
function appendPDFText(items: readonly unknown[], initial = '', initialItems = 0): { text: string; itemCount: number; truncated: boolean } {
  let text = initial
  let itemCount = initialItems
  let truncated = false
  for (const item of items) {
    itemCount += 1
    if (itemCount > MAX_PDF_ACCESSIBLE_ITEMS) {
      truncated = true
      break
    }
    if (!item || typeof item !== 'object' || !('str' in item) || typeof item.str !== 'string') continue
    const separator = text.length > 0 && item.str.length > 0 ? ' ' : ''
    const remaining = MAX_PDF_ACCESSIBLE_TEXT - text.length
    if (remaining <= 0) {
      truncated = true
      break
    }
    const segment = `${separator}${item.str}`
    text += segment.slice(0, remaining)
    if (segment.length > remaining) {
      truncated = true
      break
    }
  }
  return { text, itemCount, truncated }
}

function finishedPDFText(text: string, truncated: boolean): string {
  const normalized = text.trim()
  return truncated ? `${normalized}${PDF_TRUNCATED_SUFFIX}` : normalized
}

export function pdfAccessibleText(items: readonly unknown[]): string {
  const result = appendPDFText(items)
  return finishedPDFText(result.text, result.truncated)
}

export async function pdfAccessibleTextFromStream(
  stream: ReadableStream<{ items: readonly unknown[] }>,
  signal?: AbortSignal,
): Promise<string> {
  const reader = stream.getReader()
  let text = ''
  let itemCount = 0
  let truncated = false
  const abort = () => { void reader.cancel(signal?.reason) }
  signal?.addEventListener('abort', abort, { once: true })
  try {
    while (!signal?.aborted) {
      const chunk = await reader.read()
      if (chunk.done) break
      const result = appendPDFText(chunk.value.items, text, itemCount)
      text = result.text
      itemCount = result.itemCount
      if (result.truncated) {
        truncated = true
        await reader.cancel('PDF_TEXT_LIMIT_REACHED')
        break
      }
    }
  } finally {
    signal?.removeEventListener('abort', abort)
    reader.releaseLock()
  }
  return signal?.aborted ? '' : finishedPDFText(text, truncated)
}
