// The gallery map's clusters (GET map/clusters: the server groups cards by a Mercator
// grid) and what the page does with them: merging clusters that would overlap on screen,
// the ?area= value naming the cells of a cluster, and the box the clusters are asked for.

export interface Cluster<C = unknown> {
  lat: number
  lon: number
  count: number
  cell: string // "x.y" at the request's zoom
  place: string | null
  country: string | null
  cover: C
}

export interface Pin<C = unknown> {
  lat: number
  lon: number
  count: number
  cells: string[]
  place: string | null
  country: string | null
  cover: C
}

// The server's grid puts two clusters side by side when photos sit on both sides of a cell
// edge; their thumbnails would overlap. mergePins joins clusters whose points are closer
// than minPx on screen, biggest first, so the biggest one's cover stays on top.
export function mergePins<C>(clusters: Cluster<C>[], project: (lat: number, lon: number) => { x: number; y: number }, minPx = 56): Pin<C>[] {
  const pins: (Pin<C> & { x: number; y: number })[] = []
  for (const cl of [...clusters].sort((a, b) => b.count - a.count)) {
    const p = project(cl.lat, cl.lon)
    const into = pins.find((q) => Math.hypot(q.x - p.x, q.y - p.y) < minPx)
    if (!into) {
      pins.push({ ...p, lat: cl.lat, lon: cl.lon, count: cl.count, cells: [cl.cell], place: cl.place, country: cl.country, cover: cl.cover })
      continue
    }
    const n = into.count + cl.count
    into.lat = (into.lat * into.count + cl.lat * cl.count) / n
    into.lon = (into.lon * into.count + cl.lon * cl.count) / n
    into.x = (into.x * into.count + p.x * cl.count) / n
    into.y = (into.y * into.count + p.y * cl.count) / n
    into.count = n
    into.cells.push(cl.cell)
    if (into.place !== cl.place) into.place = null
    if (into.country !== cl.country) into.country = null
  }
  return pins.map(({ x: _x, y: _y, ...pin }) => pin)
}

// The ?area= value: "z/x.y,x.y" (the server's limit: zoom 0…20, at most 16 cells).
export function formatArea(z: number, cells: string[]): string {
  return `${z}/${cells.join(',')}`
}

export function parseArea(s: string): { z: number; cells: string[] } | null {
  const m = /^(\d{1,2})\/(\d+\.\d+(?:,\d+\.\d+)*)$/.exec(s)
  if (!m) return null
  const z = Number(m[1])
  const cells = m[2].split(',')
  if (z > 20 || cells.length > 16) return null
  return { z, cells }
}

export interface BBox { w: number; s: number; e: number; n: number }

const MAX_LAT = 85

// paddedBbox widens the visible box by k around its centre (clusters just off screen are
// there when the map moves a little) and puts it in the server's terms: latitudes within
// ±85 (or to the pole past that), longitudes within ±180, w > e when the box crosses the
// 180th meridian. The input
// may run past ±180 (MapLibre's bounds of a wrapped view), with e ≥ w.
// padFactor: the k for paddedBbox that adds at least minPx on each side of a w×h px view.
// The server counts the cards inside the box, so a cell cut by its edge has a pin with a
// smaller count than its grid; two cells (≤128 px each) of margin keep such pins off screen.
export function padFactor(widthPx: number, heightPx: number, minPx = 256): number {
  const side = Math.min(widthPx, heightPx)
  return side > 0 ? Math.max(1.5, 1 + (2 * minPx) / side) : 1.5
}

export function paddedBbox(b: BBox, k = 1.5): BBox {
  const width = (b.e - b.w) * k
  const height = (b.n - b.s) * k
  const cy = (b.s + b.n) / 2
  // At the projection's edge run to the pole: photos above 85° sit in the edge row.
  const s = cy - height / 2 <= -MAX_LAT ? -90 : cy - height / 2
  const n = cy + height / 2 >= MAX_LAT ? 90 : cy + height / 2
  if (width >= 360) return { w: -180, s, e: 180, n }
  const cx = (b.w + b.e) / 2
  return { w: wrap(cx - width / 2), s, e: wrap(cx + width / 2), n }
}

function wrap(lon: number): number {
  while (lon > 180) lon -= 360
  while (lon < -180) lon += 360
  return lon
}

// clustersQuery: the query string of GET map/clusters (no leading "?").
export function clustersQuery(z: number, b: BBox): string {
  const r = (v: number) => String(Math.round(v * 1e5) / 1e5)
  return new URLSearchParams({ z: String(z), bbox: [b.w, b.s, b.e, b.n].map(r).join(',') }).toString()
}
