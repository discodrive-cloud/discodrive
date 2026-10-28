import { describe, it, expect } from 'vitest'
import {
  computeLayout, visibleMonths, totalHeight, totalCount, monthIndexAt, indexRange,
  blockOfIndex, defaultColumns, clampColumns, yearMarks, tileSizeFor, pillBlock,
} from './layout'

const buckets = [
  { month: '2024-07', count: 10 },
  { month: '2024-06', count: 2 },
  { month: '2023-12', count: 0 },
  { month: '2019-07', count: 7 },
]

describe('computeLayout', () => {
  // 4 columns, tile 100, gap 2 -> row height 102
  const L = computeLayout(buckets, 4, 100, 2)

  it('skips empty months and keeps a continuous flow of offsets', () => {
    expect(L.map((b) => b.month)).toEqual(['2024-07', '2024-06', '2019-07'])
    expect(L.map((b) => b.offset)).toEqual([0, 10, 12])
    expect(totalCount(L)).toBe(19)
  })

  it('derives rows and heights from the counts alone (shared rows included)', () => {
    // 2024-07: indices 0..9 -> rows 0..2
    expect(L[0]).toMatchObject({ top: 0, rows: 3, height: 3 * 102 - 2 })
    // 2024-06: indices 10..11 -> row 2 only (shares it with 2024-07)
    expect(L[1]).toMatchObject({ top: 204, rows: 1, height: 100 })
    // 2019-07: indices 12..18 -> rows 3..4
    expect(L[2]).toMatchObject({ top: 306, rows: 2, height: 2 * 102 - 2 })
  })

  it('total height equals the bottom of the last month', () => {
    const last = L[L.length - 1]
    expect(totalHeight(L, 4, 100, 2)).toBe(last.top + last.height)
    expect(totalHeight(L, 4, 100, 2)).toBe(Math.ceil(19 / 4) * 102 - 2)
  })

  it('heights sum to the total when no rows are shared', () => {
    const even = computeLayout([{ month: 'a', count: 8 }, { month: 'b', count: 4 }, { month: 'c', count: 3 }], 4, 50, 4)
    const sum = even.reduce((s, b) => s + b.height + 4, 0) - 4
    expect(sum).toBe(totalHeight(even, 4, 50, 4))
  })

  it('the number of tiles in the rows matches the count', () => {
    const big = computeLayout([{ month: 'x', count: 90000 }], 7, 140, 2)
    expect(big[0].rows).toBe(Math.ceil(90000 / 7))
    expect(totalHeight(big, 7, 140, 2)).toBe(big[0].rows * 142 - 2)
  })

  it('empty input', () => {
    expect(computeLayout([], 4, 100, 2)).toEqual([])
    expect(totalHeight([], 4, 100, 2)).toBe(0)
    expect(visibleMonths([], 0, 500, 0)).toEqual([])
  })
})

describe('visibleMonths', () => {
  const L = computeLayout(buckets, 4, 100, 2) // tops 0, 204, 306; bottoms 304, 304, 508

  it('top of the page', () => {
    expect(visibleMonths(L, 0, 100, 0).map((b) => b.month)).toEqual(['2024-07'])
  })

  it('a window touching a block edge includes it', () => {
    // window [204, 205] touches 2024-07 (bottom 304) and 2024-06 (top 204)
    expect(visibleMonths(L, 204, 1, 0).map((b) => b.month)).toEqual(['2024-07', '2024-06'])
    // window ending exactly at 306 includes 2019-07
    expect(visibleMonths(L, 250, 56, 0).map((b) => b.month)).toEqual(['2024-07', '2024-06', '2019-07'])
    // window starting just past 2024-07's bottom drops it
    expect(visibleMonths(L, 305, 10, 0).map((b) => b.month)).toEqual(['2019-07'])
  })

  it('overscan extends both sides', () => {
    expect(visibleMonths(L, 400, 50, 0).map((b) => b.month)).toEqual(['2019-07'])
    expect(visibleMonths(L, 400, 50, 100).map((b) => b.month)).toEqual(['2024-07', '2024-06', '2019-07'])
  })

  it('past the end and before the start', () => {
    expect(visibleMonths(L, 10_000, 500, 0)).toEqual([])
    expect(visibleMonths(L, -1000, 100, 0)).toEqual([])
  })
})

describe('helpers', () => {
  const L = computeLayout(buckets, 4, 100, 2)

  it('monthIndexAt prefers the newer month on a shared row', () => {
    expect(monthIndexAt(L, 0)).toBe(0)
    expect(monthIndexAt(L, 250)).toBe(0) // row 2 shared by 2024-07 and 2024-06
    expect(monthIndexAt(L, 306)).toBe(2)
    expect(monthIndexAt(L, 99999)).toBe(2)
    expect(monthIndexAt([], 5)).toBe(-1)
  })

  it('indexRange covers whole rows and clamps to the total', () => {
    expect(indexRange(0, 102, 0, 4, 102, 19)).toEqual([0, 8])
    expect(indexRange(306, 1000, 0, 4, 102, 19)).toEqual([12, 19])
    expect(indexRange(0, 100, 0, 4, 102, 0)).toEqual([0, 0])
  })

  it('blockOfIndex', () => {
    expect(blockOfIndex(L, 0)).toBe(0)
    expect(blockOfIndex(L, 9)).toBe(0)
    expect(blockOfIndex(L, 10)).toBe(1)
    expect(blockOfIndex(L, 12)).toBe(2)
    expect(blockOfIndex(L, 18)).toBe(2)
  })

  it('columns default to ~140px tiles within 3..10', () => {
    expect(defaultColumns(360)).toBe(3)
    expect(defaultColumns(1000)).toBe(7)
    expect(defaultColumns(4000)).toBe(10)
    expect(clampColumns(NaN)).toBe(3)
    expect(tileSizeFor(1000, 7, 2)).toBeCloseTo((1000 - 12) / 7)
  })

  it('yearMarks: one per year at its newest month, crowded labels dropped', () => {
    const total = totalHeight(L, 4, 100, 2)
    expect(yearMarks(L, total)).toEqual([{ year: '2024', frac: 0 }, { year: '2019', frac: 306 / total }])
    expect(yearMarks(L, total, 0.9).map((m) => m.year)).toEqual(['2024'])
  })
})

describe('tile cap', () => {
  it('raises columns so tiles stay within MAX_TILE', async () => {
    const { effectiveColumns, MAX_TILE } = await import('./layout')
    expect(effectiveColumns(2560, 3, 2)).toBe(Math.ceil(2562 / (MAX_TILE + 2))) // 8
    expect(tileSizeFor(2560, effectiveColumns(2560, 3, 2), 2)).toBeLessThanOrEqual(MAX_TILE)
    expect(effectiveColumns(1000, 3, 2)).toBe(3) // 332px tiles are fine
    expect(effectiveColumns(1000, null, 2)).toBe(7)
    expect(effectiveColumns(9000, null, 2)).toBe(10) // never past 10
  })
})

describe('scroll anchor', () => {
  it('keeps the same tile on top when newer months grow', async () => {
    const { anchorAt, scrollForAnchor } = await import('./layout')
    const before = computeLayout(buckets, 4, 100, 2)
    // viewport starts 30px into row 3 (= first tile of 2019-07, index 12)
    const a = anchorAt(before, 306 + 30, 4, 102)!
    expect(a).toMatchObject({ month: '2019-07', index: 0 })
    expect(a.sub).toBeCloseTo(30 / 102)
    // 9 new photos indexed in 2024-07: 2019-07 now starts at global 21 (row 5)
    const after = computeLayout([{ month: '2024-07', count: 19 }, ...buckets.slice(1)], 4, 100, 2)
    expect(scrollForAnchor(after, a, 4, 102)).toBeCloseTo(5 * 102 + 30)
  })

  it('maps through a column change, scaling the sub-row offset', async () => {
    const { anchorAt, scrollForAnchor } = await import('./layout')
    const L4 = computeLayout(buckets, 4, 100, 2)
    const a = anchorAt(L4, 306 + 51, 4, 102)! // half a row into the row of global 12
    const L3 = computeLayout(buckets, 3, 136, 2)
    expect(scrollForAnchor(L3, a, 3, 138)).toBeCloseTo(4 * 138 + 69) // global 12 -> row 4
  })

  it('a vanished month falls to the next older one; an index past the end clamps', async () => {
    const { scrollForAnchor } = await import('./layout')
    const L = computeLayout(buckets, 4, 100, 2)
    expect(scrollForAnchor(L, { month: '2022-01', index: 5, sub: 0 }, 4, 102)).toBe(306) // -> 2019-07
    expect(scrollForAnchor(L, { month: '2024-06', index: 99, sub: 0 }, 4, 102)).toBe(204) // last tile of 2024-06
    expect(scrollForAnchor(L, { month: '1999-01', index: 0, sub: 0 }, 4, 102)).toBe(306) // older than all -> last
  })

  it('no anchor in an empty layout', async () => {
    const { anchorAt } = await import('./layout')
    expect(anchorAt([], 100, 4, 102)).toBeNull()
  })
})

describe('rangeIds', () => {
  const L = computeLayout(buckets, 4, 100, 2) // offsets 0 (10), 10 (2), 12 (7)
  const rowsOf = (m: string, n: number) => Array.from({ length: n }, (_, i) => ({ id: `${m}#${i}` }))
  const all: Record<string, { id: string }[]> = { '2024-07': rowsOf('2024-07', 10), '2024-06': rowsOf('2024-06', 2), '2019-07': rowsOf('2019-07', 7) }

  it('spans months, in either direction', async () => {
    const { rangeIds } = await import('./layout')
    const r = rangeIds(L, (m) => all[m], 8, 13)
    expect(r.missing).toEqual([])
    expect(r.ids).toEqual(['2024-07#8', '2024-07#9', '2024-06#0', '2024-06#1', '2019-07#0', '2019-07#1'])
    expect(rangeIds(L, (m) => all[m], 13, 8).ids).toEqual(r.ids)
  })

  it('reports missing months instead of selecting part of the range', async () => {
    const { rangeIds } = await import('./layout')
    const r = rangeIds(L, (m) => (m === '2024-06' ? undefined : all[m]), 0, 18)
    expect(r).toEqual({ ids: [], missing: ['2024-06'] })
  })

  it('a single tile', async () => {
    const { rangeIds } = await import('./layout')
    expect(rangeIds(L, (m) => all[m], 18, 18).ids).toEqual(['2019-07#6'])
  })
})

describe('pillBlock', () => {
  // 4 columns, row 102: 2024-07 = tiles 0..9 (rows 0-2), 2024-06 = 10..11 (row 2), 2019-07 = 12..18 (rows 3-4)
  const L = computeLayout(buckets, 4, 100, 2)
  const total = totalCount(L)

  it('names the month of the top-left tile', () => {
    expect(L[pillBlock(L, 0, 300, 4, 102, total, null)].month).toBe('2024-07')
    // row 2 starts with 2024-07 tiles 8-9, then 2024-06: the top-left tile is still July
    expect(L[pillBlock(L, 204, 300, 4, 102, total, null)].month).toBe('2024-07')
    expect(L[pillBlock(L, 306, 300, 4, 102, total, null)].month).toBe('2019-07')
  })

  it('prefers the jumped-to month while the view stays where the jump left it', () => {
    const jump = { month: '2024-06', scrollTop: 204 }
    expect(L[pillBlock(L, 204, 300, 4, 102, total, jump)].month).toBe('2024-06')
    // scrolled away: back to the top-left tile
    expect(L[pillBlock(L, 0, 300, 4, 102, total, jump)].month).toBe('2024-07')
    // clamped at the end: the jumped-to month is lower on screen and still wins
    const end = { month: '2019-07', scrollTop: 102 }
    expect(L[pillBlock(L, 102, 400, 4, 102, total, end)].month).toBe('2019-07')
  })

  it('is -1 for an empty layout', () => {
    expect(pillBlock([], 0, 300, 4, 102, 0, null)).toBe(-1)
  })
})
