import { describe, it, expect } from 'vitest'
import {
  wallTime, offsetSuffix, offsetLabel, takenAtFor, shiftMinutes, TZ_OFFSETS,
  estimateVersionBytes, emptyResult, mergeResult, failChunk, retryIds,
} from './edit'

describe('wall time', () => {
  it('applies the zone to the instant', () => {
    expect(wallTime('2019-07-14T13:42:00Z', 360)).toEqual({ date: '2019-07-14', time: '19:42' })
    expect(wallTime('2019-07-14T23:30:00Z', 360)).toEqual({ date: '2019-07-15', time: '05:30' })
    expect(wallTime('2019-07-14T02:00:00Z', -300)).toEqual({ date: '2019-07-13', time: '21:00' })
  })
  it('reads UTC fields as wall time without a zone', () => {
    expect(wallTime('2019-07-14T19:42:00Z', null)).toEqual({ date: '2019-07-14', time: '19:42' })
    expect(wallTime('nonsense', null)).toBeNull()
  })
})

describe('offsets', () => {
  it('formats suffixes and labels', () => {
    expect(offsetSuffix(360)).toBe('+06:00')
    expect(offsetSuffix(-210)).toBe('-03:30')
    expect(offsetSuffix(0)).toBe('+00:00')
    expect(offsetLabel(360)).toBe('UTC+6')
    expect(offsetLabel(330)).toBe('UTC+5:30')
    expect(offsetLabel(-180)).toBe('UTC−3')
    expect(offsetLabel(0)).toBe('UTC')
  })
  it('lists real zones in order', () => {
    expect(TZ_OFFSETS[0]).toBe(-720)
    expect(TZ_OFFSETS[TZ_OFFSETS.length - 1]).toBe(840)
    expect(TZ_OFFSETS).toContain(345)
    expect([...TZ_OFFSETS].sort((a, b) => a - b)).toEqual(TZ_OFFSETS)
  })
})

describe('takenAtFor', () => {
  it('writes the zone as the offset', () => {
    expect(takenAtFor('2019-07-14', '19:42', 360)).toBe('2019-07-14T19:42:00+06:00')
    expect(takenAtFor('2019-07-14', '19:42:05', -210)).toBe('2019-07-14T19:42:05-03:30')
  })
  it('writes an unknown zone as Z', () => {
    expect(takenAtFor('2019-07-14', '19:42', null)).toBe('2019-07-14T19:42:00Z')
  })
  it('refuses bad input', () => {
    expect(takenAtFor('2019-02-31', '10:00', null)).toBeNull()
    expect(takenAtFor('2019-7-1', '10:00', null)).toBeNull()
    expect(takenAtFor('2019-07-01', '25:00', null)).toBeNull()
    expect(takenAtFor('', '', 0)).toBeNull()
  })
})

describe('shiftMinutes', () => {
  it('signs and sums', () => {
    expect(shiftMinutes(1, 2, 30)).toBe(150)
    expect(shiftMinutes(-1, 5, 0)).toBe(-300)
    expect(shiftMinutes(-1, -5, NaN)).toBe(-300)
  })
})

describe('estimateVersionBytes', () => {
  it('counts files edited in place only', () => {
    expect(estimateVersionBytes([
      { name: 'IMG_1.HEIC', size: 2_000_000 },
      { name: 'a.jpg', size: 1_000 },
      { name: 'clip.MOV', size: 50_000_000 },
      { name: 'raw.DNG', size: 30_000_000 },
      { name: 'b.jpeg', size: null },
    ])).toBe(2_001_000)
  })
})

describe('batch results', () => {
  it('merges chunks and failed requests', () => {
    const r = emptyResult()
    mergeResult(r, { updated: ['a'], conflicts: ['b'], failed: [], partial: [], versionBytes: 10 })
    mergeResult(r, { updated: ['c'], conflicts: [], failed: [{ id: 'd', error: 'x' }], partial: ['e'], versionBytes: 5 })
    failChunk(r, ['f', 'g'], 'network')
    expect(r.updated).toEqual(['a', 'c'])
    expect(r.versionBytes).toBe(15)
    expect(r.failed.map((f) => f.id)).toEqual(['d', 'f', 'g'])
  })
  it('never retries a partial card in a shift', () => {
    const r = emptyResult()
    mergeResult(r, { updated: [], conflicts: ['b', 'p'], failed: [{ id: 'd', error: 'x' }, { id: 'p', error: 'y' }], partial: ['p', 'q'], versionBytes: 0 })
    expect(retryIds(r, true)).toEqual(['d', 'b'])
    expect(retryIds(r, false)).toEqual(['d', 'p', 'b', 'q'])
  })
})
