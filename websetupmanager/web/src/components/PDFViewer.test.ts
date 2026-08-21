import { describe, expect, it } from 'vitest'
import { pdfAccessibleText, pdfAccessibleTextFromStream, pdfCanvasGeometry } from '../pdfGeometry'

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

  it('creates a bounded plain-text alternative from PDF.js text items', () => {
    expect(pdfAccessibleText([{ str: 'Operation' }, { str: 'T1' }, { type: 'marked-content' }])).toBe('Operation T1')
    expect(pdfAccessibleText([{ str: 'X'.repeat(120_000) }])).toBe(`${'X'.repeat(100_000)} … [текст страницы сокращён]`)
  })

  it('cancels streamed PDF text as soon as the accessible-text limit is reached', async () => {
    let cancelled = false
    const stream = new ReadableStream<{ items: readonly unknown[] }>({
      start(controller) {
        controller.enqueue({ items: [{ str: 'X'.repeat(120_000) }] })
      },
      cancel() { cancelled = true },
    })
    const text = await pdfAccessibleTextFromStream(stream)
    expect(text).toBe(`${'X'.repeat(100_000)} … [текст страницы сокращён]`)
    expect(cancelled).toBe(true)
  })
})
