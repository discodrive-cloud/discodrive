import { describe, it, expect } from 'vitest'
import { photosNavVisible, indexingRunning, indexingPercent } from './settings'

describe('photosNavVisible', () => {
  it('needs the gallery on and available', () => {
    expect(photosNavVisible({ enabled: true, available: true })).toBe(true)
    // lite build with the gallery left on in the database: no dead nav item
    expect(photosNavVisible({ enabled: true, available: false })).toBe(false)
    expect(photosNavVisible({ enabled: false, available: true })).toBe(false)
    expect(photosNavVisible(null)).toBe(false)
  })
  it('shows for galleries and albums others opened, with one\'s own off', () => {
    expect(photosNavVisible({ enabled: false, available: true, sharedWithMe: true })).toBe(true)
    expect(photosNavVisible({ enabled: false, available: false, sharedWithMe: true })).toBe(false)
  })
})

describe('indexingRunning', () => {
  const done = { total: 27, ready: 27, failed: 0 }
  it('stops when everything is processed', () => {
    expect(indexingRunning(done, 0)).toBe(false)
  })
  it('keeps polling after a save even when the old rows are all done (second source)', () => {
    expect(indexingRunning(done, 3)).toBe(true)
  })
  it('follows a running pass and its pending files', () => {
    expect(indexingRunning({ ...done, scanning: true }, 0)).toBe(true)
    expect(indexingRunning({ total: 35, ready: 27, failed: 0, pending: 8 }, 0)).toBe(true)
  })
  it('follows thumbnails still to make', () => {
    expect(indexingRunning({ total: 35, ready: 30, failed: 2 }, 0)).toBe(true)
  })
  it('is off without data', () => {
    expect(indexingRunning(null, 5)).toBe(false)
  })
})

describe('indexingPercent', () => {
  it('counts pending files in the total', () => {
    expect(indexingPercent({ total: 200, ready: 50, failed: 0, pending: 100 })).toBe(25)
    expect(indexingPercent({ total: 3, ready: 2, failed: 0 })).toBe(66)
    expect(indexingPercent({ total: 0, ready: 0, failed: 0 })).toBe(0)
  })
})
