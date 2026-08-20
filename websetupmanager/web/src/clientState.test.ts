import { beforeEach, describe, expect, it } from 'vitest'
import { stableClientID } from './clientState'

describe('stableClientID', () => {
  beforeEach(() => localStorage.clear())

  it('persists one bounded opaque client ID without setup or path data', () => {
    const first = stableClientID()
    const second = stableClientID()
    expect(second).toBe(first)
    expect(first).toMatch(/^web:[A-Za-z0-9-]+$/)
    expect(first.length).toBeLessThanOrEqual(128)
  })

  it('replaces an invalid stored value', () => {
    localStorage.setItem('web-setup-manager.client-id.v1', '../physical/path')
    expect(stableClientID()).not.toBe('../physical/path')
  })
})
