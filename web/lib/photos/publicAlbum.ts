// An album opened by link (/a/{token}): the same grid and viewer as the gallery, with the
// gallery's picture addresses turned into the link's.

const ASSET = /^\/me\/photos\/v1\/assets\/([^/?]+)\/([a-z]+)(\?.*)?$/

// '/me/photos/v1/assets/ID/thumb?v=x' → '/public/photos/v1/TOKEN/assets/ID/thumb?v=x&k=KEY'.
// Anything else is not the link's to fetch: null.
export function publicAssetURL(url: string, token: string, key: string): string | null {
  const m = ASSET.exec(url)
  if (!m) return null
  const q = new URLSearchParams(m[3]?.slice(1) ?? '')
  if (key) q.set('k', key)
  const qs = q.toString()
  return `/public/photos/v1/${encodeURIComponent(token)}/assets/${m[1]}/${m[2]}${qs ? `?${qs}` : ''}`
}

// Groups the album's cards (newest first) into the months the grid walks.
export function monthsOf<T extends { takenAt: string; tzOffsetMin: number | null }>(rows: T[], wall: (r: T) => Date) {
  const by = new Map<string, T[]>()
  for (const r of rows) {
    const d = wall(r)
    const m = Number.isFinite(d.getTime()) ? d.toISOString().slice(0, 7) : '0000-00'
    const list = by.get(m)
    if (list) list.push(r)
    else by.set(m, [r])
  }
  const months = [...by.keys()].sort().reverse()
  return { buckets: months.map((month) => ({ month, count: by.get(month)!.length })), rows: by }
}
