import { describe, it, expect } from 'vitest'
import { mapConcurrent } from './mapConcurrent'

describe('mapConcurrent', () => {
  it('overlaps requests within the limit and preserves order', async () => {
    let active = 0, peak = 0
    const result = await mapConcurrent([0, 1, 2, 3, 4, 5, 6], 3, async (value) => {
      peak = Math.max(peak, ++active)
      await new Promise(resolve => setTimeout(resolve, (7 - value) * 2))
      active--
      return value * 2
    })
    expect(peak).toBe(3)
    expect(result).toEqual([0, 2, 4, 6, 8, 10, 12])
  })
  it('stops scheduling and drains requests before rejecting', async () => {
    let active = 0
    const started: number[] = []
    const failure = new Error('download failed')
    await expect(mapConcurrent([0, 1, 2, 3], 2, async value => {
      started.push(value)
      if (value === 0) throw failure
      active++
      await new Promise(resolve => setTimeout(resolve, 10))
      active--
    })).rejects.toBe(failure)
    expect(active).toBe(0)
    expect(started).toEqual([0, 1])
  })
})
