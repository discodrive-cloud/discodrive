import { describe, expect, it } from 'vitest'
import { chunkRanges, ChunkHeights } from './chunkWindow'

describe('chunkRanges', () => {
  it('cuts a list into runs of the given size, the last one shorter', () => {
    expect(chunkRanges(250, 100)).toEqual([[0, 100], [100, 200], [200, 250]])
    expect(chunkRanges(100, 100)).toEqual([[0, 100]])
    expect(chunkRanges(1, 100)).toEqual([[0, 1]])
    expect(chunkRanges(0, 100)).toEqual([])
  })
})

describe('ChunkHeights', () => {
  it('guesses an unseen chunk from the row height seen so far', () => {
    const h = new ChunkHeights(40)
    expect(h.of(3, 100)).toBe(4000)
    h.measured(0, 100, 5000) // rows turned out 50px tall
    expect(h.of(3, 100)).toBe(5000)
    expect(h.of(4, 30)).toBe(1500)
  })
  it('keeps the height a chunk had when it was last on screen', () => {
    const h = new ChunkHeights(40)
    h.measured(2, 100, 4310)
    h.measured(0, 100, 5000)
    expect(h.of(2, 100)).toBe(4310)
  })
  it('forgets what it measured when the list is another one, but not the row height', () => {
    const h = new ChunkHeights(40)
    h.measured(2, 100, 4310)
    h.reset()
    expect(h.of(2, 100)).toBe(4310)
    h.measured(0, 100, 5000)
    expect(h.of(2, 100)).toBe(5000)
  })
  it('ignores a measurement of nothing', () => {
    const h = new ChunkHeights(40)
    h.measured(0, 100, 0)
    h.measured(1, 0, 300)
    expect(h.of(0, 100)).toBe(4000)
  })
})
