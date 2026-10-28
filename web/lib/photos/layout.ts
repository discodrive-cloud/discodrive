// Timeline geometry of the photo grid. The grid is one continuous flow of square tiles
// (no month or day headers, as in Apple Photos): a month starts right after the last tile
// of the newer one, so two months may share a row. Every position follows from the month
// counts alone, so the page has its full height before any month's content is loaded and
// nothing jumps when it arrives.

export interface Bucket {
  month: string // YYYY-MM
  count: number
}

export interface MonthBlock {
  month: string
  count: number
  offset: number // global index of the month's first tile
  top: number // y of the month's first row
  height: number // from the top of its first row to the bottom of its last one
  rows: number // rows the month touches (shared rows included)
}

export const MIN_COLUMNS = 3
export const MAX_COLUMNS = 10
export const TARGET_TILE = 140

export function clampColumns(n: number): number {
  if (!Number.isFinite(n)) return MIN_COLUMNS
  return Math.min(MAX_COLUMNS, Math.max(MIN_COLUMNS, Math.round(n)))
}

// defaultColumns fits tiles of about TARGET_TILE px into the width.
export function defaultColumns(width: number, target = TARGET_TILE): number {
  return clampColumns(width / target)
}

export function tileSizeFor(width: number, columns: number, gap: number): number {
  return Math.max(1, (width - gap * (columns - 1)) / columns)
}

export function computeLayout(buckets: Bucket[], columns: number, tileSize: number, gap: number): MonthBlock[] {
  const rowH = tileSize + gap
  const out: MonthBlock[] = []
  let offset = 0
  for (const b of buckets) {
    const count = Math.max(0, Math.floor(b.count))
    if (count === 0) continue
    const firstRow = Math.floor(offset / columns)
    const lastRow = Math.floor((offset + count - 1) / columns)
    const rows = lastRow - firstRow + 1
    out.push({ month: b.month, count, offset, top: firstRow * rowH, height: rows * rowH - gap, rows })
    offset += count
  }
  return out
}

export function totalCount(layout: MonthBlock[]): number {
  const last = layout[layout.length - 1]
  return last ? last.offset + last.count : 0
}

export function totalHeight(layout: MonthBlock[], columns: number, tileSize: number, gap: number): number {
  const n = totalCount(layout)
  if (n === 0) return 0
  return Math.ceil(n / columns) * (tileSize + gap) - gap
}

// monthIndexAt returns the index of the block that covers y (the newer one on a shared
// row), clamped to the first/last block; -1 for an empty layout.
export function monthIndexAt(layout: MonthBlock[], y: number): number {
  if (layout.length === 0) return -1
  // The first block whose bottom reaches y: on a shared row that is the newer month,
  // whose tiles come first in the row.
  let lo = 0
  let hi = layout.length - 1
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (layout[mid].top + layout[mid].height < y) lo = mid + 1
    else hi = mid
  }
  return lo
}

// visibleMonths lists the blocks intersecting [scrollTop - overscan, scrollTop + viewportH + overscan].
export function visibleMonths(layout: MonthBlock[], scrollTop: number, viewportH: number, overscan: number): MonthBlock[] {
  const from = scrollTop - overscan
  const to = scrollTop + viewportH + overscan
  const out: MonthBlock[] = []
  if (layout.length === 0 || to < 0) return out
  // first block whose bottom >= from
  let lo = 0
  let hi = layout.length
  while (lo < hi) {
    const mid = (lo + hi) >> 1
    if (layout[mid].top + layout[mid].height < from) lo = mid + 1
    else hi = mid
  }
  for (let i = lo; i < layout.length && layout[i].top <= to; i++) out.push(layout[i])
  return out
}

// indexRange is the half-open range of global tile indices whose rows intersect the window.
export function indexRange(scrollTop: number, viewportH: number, overscan: number, columns: number, rowH: number, total: number): [number, number] {
  if (total === 0 || rowH <= 0) return [0, 0]
  const firstRow = Math.max(0, Math.floor((scrollTop - overscan) / rowH))
  const lastRow = Math.max(firstRow, Math.floor((scrollTop + viewportH + overscan) / rowH))
  return [Math.min(total, firstRow * columns), Math.min(total, (lastRow + 1) * columns)]
}

// blockOfIndex finds the block holding a global tile index (binary search on offset).
export function blockOfIndex(layout: MonthBlock[], index: number): number {
  let lo = 0
  let hi = layout.length - 1
  if (hi < 0) return -1
  while (lo < hi) {
    const mid = (lo + hi + 1) >> 1
    if (layout[mid].offset <= index) lo = mid
    else hi = mid - 1
  }
  return lo
}

export interface YearMark {
  year: string
  frac: number // 0..1 position on the scrubber
}

// yearMarks places each year at the top of its newest month, as a fraction of the height.
// Labels closer than minGap (fraction) to the previous shown one are dropped.
export function yearMarks(layout: MonthBlock[], total: number, minGap = 0): YearMark[] {
  const out: YearMark[] = []
  if (total <= 0) return out
  let prevYear = ''
  for (const b of layout) {
    const y = b.month.slice(0, 4)
    if (y === prevYear) continue
    prevYear = y
    const frac = Math.min(1, b.top / total)
    if (out.length && frac - out[out.length - 1].frac < minGap) continue
    out.push({ year: y, frac })
  }
  return out
}

// ---- tile cap ----

// MAX_TILE bounds the tile size: very wide screens get more columns instead of huge
// tiles, which also keeps the scroll height under the browsers' element-height caps
// (Firefox ~17.9M px): 90k items stay under ~11M px at any width.
export const MAX_TILE = 360

// effectiveColumns: the user's choice (or the width default), raised so no tile is
// larger than MAX_TILE.
export function effectiveColumns(width: number, chosen: number | null, gap: number): number {
  const base = chosen ?? defaultColumns(width)
  const min = Math.ceil((width + gap) / (MAX_TILE + gap))
  return clampColumns(Math.max(base, min))
}

// ---- scroll anchor ----

// The first tile of the first visible row, as (month, index within the month), plus how
// far into that row the viewport starts (as a fraction of the row, so a new row height
// scales it). Survives relayouts that move offsets: new photos indexed in newer months,
// another column count, cards removed.
export interface ScrollAnchor {
  month: string
  index: number
  sub: number // 0..1 of a row
}

export function anchorAt(layout: MonthBlock[], scrollTop: number, columns: number, rowH: number): ScrollAnchor | null {
  const total = totalCount(layout)
  if (!total || rowH <= 0) return null
  const row = Math.max(0, Math.floor(scrollTop / rowH))
  const first = Math.min(total - 1, row * columns)
  const b = layout[blockOfIndex(layout, first)]
  return { month: b.month, index: first - b.offset, sub: Math.min(1, Math.max(0, (scrollTop - row * rowH) / rowH)) }
}

// scrollForAnchor puts the anchor tile's row back at the top. A month that is gone gives
// the next older month still there (months are YYYY-MM, newest first); an index past the
// month's new count clamps to its last tile.
export function scrollForAnchor(layout: MonthBlock[], a: ScrollAnchor, columns: number, rowH: number): number {
  if (!layout.length) return 0
  let b = layout.find((x) => x.month === a.month)
  let index = a.index
  if (!b) {
    b = layout.find((x) => x.month < a.month) ?? layout[layout.length - 1]
    index = 0
  }
  const global = b.offset + Math.min(index, b.count - 1)
  return Math.floor(global / columns) * rowH + a.sub * rowH
}

// ---- range selection ----

// rangeIds resolves a Shift-range of global indices [a, b] to asset ids. Every month the
// range touches must be in `rows`; the months that are not come back in `missing` (and
// then ids is empty), so the caller loads them and asks again rather than selecting part
// of the range.
export function rangeIds(layout: MonthBlock[], rows: (month: string) => { id: string }[] | undefined, a: number, b: number): { ids: string[]; missing: string[] } {
  const [lo, hi] = a <= b ? [a, b] : [b, a]
  const ids: string[] = []
  const missing: string[] = []
  if (!layout.length) return { ids, missing }
  for (let bi = blockOfIndex(layout, lo); bi < layout.length && layout[bi].offset <= hi; bi++) {
    const blk = layout[bi]
    const list = rows(blk.month)
    if (!list) { missing.push(blk.month); continue }
    const from = Math.max(lo, blk.offset) - blk.offset
    const to = Math.min(hi, blk.offset + blk.count - 1) - blk.offset
    for (let i = from; i <= to && i < list.length; i++) ids.push(list[i].id)
  }
  return missing.length ? { ids: [], missing } : { ids, missing }
}

// A scrubber jump: the month it went to and where it left the view.
export interface PillJump {
  month: string
  scrollTop: number
}

// pillBlock is the block the date pill names: the month of the top-left tile on screen.
// Right after a scrubber jump (the view still where the jump left it) the jumped-to month
// wins while it is on screen: it may start mid-row behind the tail of a newer month, or
// sit lower on the screen when the jump hit the end of the scroll range, and the pill
// must then agree with the scrubber's bubble.
export function pillBlock(layout: MonthBlock[], scrollTop: number, viewportH: number, columns: number, rowH: number, total: number, jump: PillJump | null): number {
  if (!layout.length || total <= 0 || rowH <= 0) return -1
  const first = Math.min(total - 1, Math.floor(scrollTop / rowH) * columns)
  if (jump && Math.abs(scrollTop - jump.scrollTop) < rowH / 2) {
    const j = layout.findIndex((b) => b.month === jump.month)
    const lastOnScreen = Math.min(total, first + Math.max(1, Math.ceil(viewportH / rowH)) * columns)
    if (j >= 0 && layout[j].offset < lastOnScreen && layout[j].offset + layout[j].count > first) return j
  }
  return blockOfIndex(layout, first)
}
