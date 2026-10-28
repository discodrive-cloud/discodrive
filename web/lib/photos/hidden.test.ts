import { describe, it, expect, vi, afterEach } from 'vitest'
import { photosHiddenToken, photosHiddenHeaders, photosHiddenUnlocked, setPhotosHiddenToken, lockPhotosHidden } from './hidden'

afterEach(() => {
  lockPhotosHidden()
  vi.useRealTimers()
})

describe('hidden token', () => {
  it('hands out the header while valid', () => {
    setPhotosHiddenToken('tok', Date.now() + 600_000)
    expect(photosHiddenUnlocked.value).toBe(true)
    expect(photosHiddenHeaders()).toEqual({ 'X-Photos-Hidden': 'tok' })
  })

  it('locks itself on expiry', () => {
    vi.useFakeTimers()
    const now = Date.now()
    setPhotosHiddenToken('tok', new Date(now + 60_000).toISOString())
    vi.advanceTimersByTime(56_000)
    expect(photosHiddenUnlocked.value).toBe(false)
    expect(photosHiddenHeaders()).toEqual({})
  })

  it('refuses an already expired token and reads expiry lazily too', () => {
    setPhotosHiddenToken('old', Date.now() - 1)
    expect(photosHiddenUnlocked.value).toBe(false)
    setPhotosHiddenToken('tok', 10_000_000, 0)
    expect(photosHiddenToken(10_000_000)).toBeNull()
    expect(photosHiddenUnlocked.value).toBe(false)
  })

  it('lock forgets it', () => {
    setPhotosHiddenToken('tok', Date.now() + 600_000)
    lockPhotosHidden()
    expect(photosHiddenToken()).toBeNull()
  })
})
