import { describe, expect, it } from 'vitest'
import { pdfCanvasGeometry } from '../pdfGeometry'

describe('PDF canvas resource bounds', () => {
  it('rejects hostile MediaBox dimensions before assigning a canvas', () => {
    expect(() => pdfCanvasGeometry(1_000_000, 100, 1)).toThrow('PDF_PAGE_DIMENSIONS_UNSAFE')
    expect(() => pdfCanvasGeometry(Number.POSITIVE_INFINITY, 842, 1)).toThrow('PDF_PAGE_DIMENSIONS_UNSAFE')
  })

  it('caps backing pixels and device scale for a valid page', () => {
    const geometry = pdfCanvasGeometry(2380, 3368, 8)
    expect(geometry.width).toBeLessThanOrEqual(4096)
    expect(geometry.height).toBeLessThanOrEqual(4096)
    expect(geometry.width * geometry.height).toBeLessThanOrEqual(4_000_000)
    expect(geometry.cssWidth).toBe(2380)
    expect(geometry.cssHeight).toBe(3368)
  })
})
