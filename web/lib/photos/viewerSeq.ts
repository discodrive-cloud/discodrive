// The viewer's walk through a timeline: months in timeline order (newest first, as the
// buckets come) and the rows of the months loaded so far. Stepping past the end of a
// month moves into the next one, asking for it to be loaded first.

export interface SeqRow {
  id: string
}

export interface Seq<R extends SeqRow = SeqRow> {
  months: string[]
  rows: Map<string, R[]>
}

export interface Pos {
  month: string
  id: string
}

export type Step = { to: Pos } | { load: string } | null

export function locate<R extends SeqRow>(seq: Seq<R>, pos: Pos): { mi: number; i: number } | null {
  const mi = seq.months.indexOf(pos.month)
  if (mi < 0) return null
  const i = (seq.rows.get(pos.month) ?? []).findIndex((r) => r.id === pos.id)
  return i < 0 ? null : { mi, i }
}

// step: dir +1 = older (down the timeline), -1 = newer.
export function step<R extends SeqRow>(seq: Seq<R>, pos: Pos, dir: 1 | -1): Step {
  const at = locate(seq, pos)
  if (!at) return null
  const rows = seq.rows.get(pos.month)!
  const j = at.i + dir
  if (j >= 0 && j < rows.length) return { to: { month: pos.month, id: rows[j].id } }
  for (let mi = at.mi + dir; mi >= 0 && mi < seq.months.length; mi += dir) {
    const m = seq.months[mi]
    const r = seq.rows.get(m)
    if (!r) return { load: m }
    if (r.length) return { to: { month: m, id: (dir > 0 ? r[0] : r[r.length - 1]).id } }
  }
  return null
}

// around: up to n loaded rows on each side of pos (the strip under the photo), in order.
export function around<R extends SeqRow>(seq: Seq<R>, pos: Pos, n: number): { month: string; row: R }[] {
  const at = locate(seq, pos)
  if (!at) return []
  const before: { month: string; row: R }[] = []
  const after: { month: string; row: R }[] = []
  // newer side
  let mi = at.mi
  let i = at.i - 1
  while (before.length < n && mi >= 0) {
    const rows = seq.rows.get(seq.months[mi])
    if (!rows) break
    if (i < 0) { mi--; i = mi >= 0 ? (seq.rows.get(seq.months[mi])?.length ?? 0) - 1 : -1; continue }
    before.unshift({ month: seq.months[mi], row: rows[i] })
    i--
  }
  // older side
  mi = at.mi
  i = at.i + 1
  while (after.length < n && mi < seq.months.length) {
    const rows = seq.rows.get(seq.months[mi])
    if (!rows) break
    if (i >= rows.length) { mi++; i = 0; continue }
    after.push({ month: seq.months[mi], row: rows[i] })
    i++
  }
  const self = { month: pos.month, row: seq.rows.get(pos.month)![at.i] }
  return [...before, self, ...after]
}
