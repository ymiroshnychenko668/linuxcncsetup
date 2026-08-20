const MAX_PDF_CSS_EDGE = 8192
const MAX_PDF_CSS_PIXELS = 32_000_000
const MAX_PDF_CANVAS_EDGE = 4096
const MAX_PDF_CANVAS_PIXELS = 4_000_000

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
