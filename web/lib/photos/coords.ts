// parseCoords reads a point the way people paste one: plain decimal pairs, degrees with
// minutes and seconds, or a link from Google, Apple or OpenStreetMap maps. Anything it
// cannot read, or a point outside the globe, gives null.

export interface LatLon {
  lat: number
  lon: number
}

const NUM = String.raw`[-+]?\d{1,3}(?:\.\d+)?`

function valid(lat: number, lon: number): LatLon | null {
  if (!Number.isFinite(lat) || !Number.isFinite(lon)) return null
  if (lat < -90 || lat > 90 || lon < -180 || lon > 180) return null
  // -0 reads oddly in an input field
  return { lat: lat + 0, lon: lon + 0 }
}

function pair(a: string, b: string): LatLon | null {
  return valid(Number(a), Number(b))
}

// Links: Google "/@lat,lon,zoom", Google "?q=lat,lon", Apple "ll=lat,lon",
// OSM "#map=zoom/lat/lon" and "mlat=…&mlon=…".
function fromURL(s: string): LatLon | null {
  if (!/^(https?:\/\/|www\.|maps\.|geo:)/i.test(s)) return null
  let text = s
  try { text = decodeURIComponent(s) } catch { /* keep the raw string */ }
  // An OSM marker is the point itself; its #map is only the view around it.
  const mlat = text.match(new RegExp(String.raw`[?&]mlat=(${NUM})`))
  const mlon = text.match(new RegExp(String.raw`[?&]mlon=(${NUM})`))
  if (mlat && mlon) return pair(mlat[1], mlon[1])
  const patterns: RegExp[] = [
    new RegExp(String.raw`@(${NUM}),(${NUM})(?:[,/z]|$)`),
    new RegExp(String.raw`[?&](?:ll|sll|q|query|center)=(${NUM}),\s*(${NUM})(?:&|$|[^\d.])`),
    new RegExp(String.raw`#map=\d{1,2}(?:\.\d+)?/(${NUM})/(${NUM})`),
    new RegExp(String.raw`^geo:(${NUM}),(${NUM})`, 'i'),
  ]
  for (const re of patterns) {
    const m = text.match(re)
    if (m) return pair(m[1], m[2])
  }
  return null
}

// One DMS component without its hemisphere: 42°38'51", 42° 38′ 51.5″, 42°38.85', 42.6475°.
const CORE = String.raw`(\d{1,3}(?:\.\d+)?)\s*°\s*(?:(\d{1,2}(?:\.\d+)?)\s*['′’]\s*)?(?:(\d{1,2}(?:\.\d+)?)\s*(?:"|″|”|''|′′)\s*)?`
const HEMI = String.raw`([NSEWСЮВЗ])`
const SEP = String.raw`\s*[,;]?\s*`
// The hemisphere letter goes after each value (42°…N 77°…E) or before it (N42°… E77°…).
const DMS_SUFFIX = new RegExp(String.raw`^\s*${CORE}${HEMI}?${SEP}${CORE}${HEMI}?\s*$`, 'i')
const DMS_PREFIX = new RegExp(String.raw`^\s*${HEMI}\s*${CORE}${SEP}${HEMI}\s*${CORE}\s*$`, 'i')

function dmsValue(deg: string, min?: string, sec?: string): number | null {
  if ((min && Number(min) >= 60) || (sec && Number(sec) >= 60)) return null
  return Number(deg) + (min ? Number(min) / 60 : 0) + (sec ? Number(sec) / 3600 : 0)
}

function hemisphere(h: string): 'lat' | 'lon' | null {
  const c = h.toUpperCase()
  if (c === 'N' || c === 'S' || c === 'С' || c === 'Ю') return 'lat'
  if (c === 'E' || c === 'W' || c === 'В' || c === 'З') return 'lon'
  return null
}

function negative(h: string): boolean {
  const c = h.toUpperCase()
  return c === 'S' || c === 'W' || c === 'Ю' || c === 'З'
}

function fromDMS(s: string): LatLon | null {
  if (!s.includes('°')) return null
  let parts: { h?: string; deg: string; min?: string; sec?: string }[]
  let m = s.match(DMS_SUFFIX)
  if (m) {
    parts = [{ deg: m[1], min: m[2], sec: m[3], h: m[4] }, { deg: m[5], min: m[6], sec: m[7], h: m[8] }]
  } else {
    m = s.match(DMS_PREFIX)
    if (!m) return null
    parts = [{ h: m[1], deg: m[2], min: m[3], sec: m[4] }, { h: m[5], deg: m[6], min: m[7], sec: m[8] }]
  }
  let lat: number | null = null
  let lon: number | null = null
  for (const [i, p] of parts.entries()) {
    let v = dmsValue(p.deg, p.min, p.sec)
    if (v === null) return null
    if (p.h && negative(p.h)) v = -v
    const axis = p.h ? hemisphere(p.h) : i === 0 ? 'lat' : 'lon'
    if (axis === 'lat') {
      if (lat !== null) return null
      lat = v
    } else {
      if (lon !== null) return null
      lon = v
    }
  }
  if (lat === null || lon === null) return null
  return valid(lat, lon)
}

// A number as typed: a dot or (Russian keyboards) a comma before the decimals.
const NUMC = String.raw`[-+]?\d{1,3}(?:[.,]\d+)?`
const DOTTED = String.raw`[-+]?\d{1,3}\.\d+`

// fromDecimal reads two numbers. A comma right between two digits is a decimal comma
// ("42,6475" is one number), so the pair separator is a comma or semicolon followed by
// a space, a semicolon, whitespace — or a bare comma between two dot-decimals
// ("42.6475,77.0820", as map sites copy it).
function fromDecimal(s: string): LatLon | null {
  const num = (x: string) => Number(x.replace(',', '.'))
  let m = s.match(new RegExp(String.raw`^\s*(${NUMC})\s*(?:,\s+|;\s*|\s+)(${NUMC})\s*$`))
  if (!m) m = s.match(new RegExp(String.raw`^\s*(${DOTTED})\s*,(${DOTTED})\s*$`))
  return m ? valid(num(m[1]), num(m[2])) : null
}

export function parseCoords(input: string): LatLon | null {
  const s = (input || '').trim()
  if (!s) return null
  return fromURL(s) ?? fromDMS(s) ?? fromDecimal(s)
}

// formatCoord prints a coordinate for an input field: 6 decimals (~10 cm), no trailing zeros.
export function formatCoord(v: number): string {
  return String(Number(v.toFixed(6)))
}
