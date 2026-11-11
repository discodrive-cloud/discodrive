// A long list is drawn in chunks: the chunks near the screen as real rows, every other
// one as a single blank block of the same height. The page keeps its full length and its
// scrollbar, and the browser holds a few hundred rows however many the folder has.

// chunkRanges cuts n items into [from, to) runs of size.
export function chunkRanges(n: number, size: number): [number, number][] {
  const out: [number, number][] = []
  for (let from = 0; from < n; from += size) out.push([from, Math.min(from + size, n)])
  return out
}

// ChunkHeights knows how tall a chunk's blank block has to be: what the chunk measured
// when it was last drawn, else its row count times the row height seen most recently
// (rows are not all alike — a long name wraps — so this is a guess until it is drawn).
export class ChunkHeights {
  private seen = new Map<number, number>()
  constructor(private row: number) {}

  measured(chunk: number, rows: number, height: number) {
    if (rows <= 0 || height <= 0) return
    this.seen.set(chunk, height)
    this.row = height / rows
  }

  of(chunk: number, rows: number): number {
    return this.seen.get(chunk) ?? Math.round(rows * this.row)
  }

  // reset: the list was replaced, chunk numbers mean other rows now.
  reset() {
    this.seen.clear()
  }
}
