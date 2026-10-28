// Date and place edits: the arithmetic of wall-clock times and zones, and the bookkeeping
// of batch results (PUT /me/photos/v1/assets/metadata answers per card).

// ---- dates ----
// The API carries the capture moment as takenAt (UTC instant) + tzOffsetMin (the zone the
// photo was taken in). With no known zone, takenAt's UTC fields ARE the wall-clock time.

export interface WallTime {
  date: string // YYYY-MM-DD
  time: string // HH:MM
}

const pad = (n: number) => String(n).padStart(2, '0')

export function wallTime(takenAt: string, tzOffsetMin: number | null): WallTime | null {
  const ms = Date.parse(takenAt)
  if (!Number.isFinite(ms)) return null
  const d = new Date(ms + (tzOffsetMin ?? 0) * 60_000)
  return {
    date: `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`,
    time: `${pad(d.getUTCHours())}:${pad(d.getUTCMinutes())}`,
  }
}

// wallDate: a Date whose UTC fields are the wall-clock time (format it with timeZone UTC).
export function wallDate(takenAt: string, tzOffsetMin: number | null): Date {
  return new Date(Date.parse(takenAt) + (tzOffsetMin ?? 0) * 60_000)
}

// offsetSuffix: 360 -> "+06:00", -210 -> "-03:30".
export function offsetSuffix(min: number): string {
  const sign = min < 0 ? '-' : '+'
  const a = Math.abs(min)
  return `${sign}${pad(Math.floor(a / 60))}:${pad(a % 60)}`
}

// offsetLabel: 360 -> "UTC+6", 330 -> "UTC+5:30", -180 -> "UTC−3", 0 -> "UTC".
export function offsetLabel(min: number): string {
  if (min === 0) return 'UTC'
  const a = Math.abs(min)
  const h = Math.floor(a / 60)
  const m = a % 60
  return `UTC${min < 0 ? '−' : '+'}${h}${m ? `:${pad(m)}` : ''}`
}

// The zones that exist (whole hours plus the half and quarter hours in use).
export const TZ_OFFSETS: number[] = (() => {
  const odd = [-570, -210, 210, 270, 330, 345, 390, 525, 570, 630, 765, 825]
  const out = new Set<number>(odd)
  for (let h = -12; h <= 14; h++) out.add(h * 60)
  return [...out].sort((a, b) => a - b)
})()

// takenAtFor builds the request's takenAt from what the user typed. A known zone is
// written as the time's own offset (and sent as tzOffsetMin too); an unknown one as a
// bare wall-clock time in "Z" (the server keeps it without a zone).
export function takenAtFor(date: string, time: string, tzOffsetMin: number | null): string | null {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(date) || !/^\d{2}:\d{2}(:\d{2})?$/.test(time)) return null
  const t = time.length === 5 ? `${time}:00` : time
  const probe = Date.parse(`${date}T${t}Z`)
  if (!Number.isFinite(probe)) return null
  // Date.parse accepts 2019-02-31 (rolls over): insist on the same calendar day.
  const back = new Date(probe)
  if (`${back.getUTCFullYear()}-${pad(back.getUTCMonth() + 1)}-${pad(back.getUTCDate())}` !== date) return null
  return `${date}T${t}${tzOffsetMin == null ? 'Z' : offsetSuffix(tzOffsetMin)}`
}

// shiftMinutes: the signed batch shift.
export function shiftMinutes(sign: 1 | -1, hours: number, minutes: number): number {
  const h = Number.isFinite(hours) ? Math.trunc(Math.abs(hours)) : 0
  const m = Number.isFinite(minutes) ? Math.trunc(Math.abs(minutes)) : 0
  return sign * (h * 60 + m)
}

// ---- version cost ----
// JPEG/HEIC/PNG/WebP are edited in place: each one becomes a new file version, a full copy.
// Video, RAW and the rest get a small XMP sidecar.
const IN_PLACE = /\.(jpe?g|heic|heif|png|webp)$/i

export function inPlaceEdit(name: string): boolean {
  return IN_PLACE.test(name)
}

export function estimateVersionBytes(files: { name: string; size: number | null }[]): number {
  let sum = 0
  for (const f of files) if (inPlaceEdit(f.name) && f.size) sum += f.size
  return sum
}

// ---- batch results ----
export interface EditResult {
  updated: string[]
  conflicts: string[]
  failed: { id: string; error: string }[]
  partial: string[]
  versionBytes: number
}

export function emptyResult(): EditResult {
  return { updated: [], conflicts: [], failed: [], partial: [], versionBytes: 0 }
}

export function mergeResult(into: EditResult, r: Partial<EditResult>): EditResult {
  into.updated.push(...(r.updated ?? []))
  into.conflicts.push(...(r.conflicts ?? []))
  into.failed.push(...(r.failed ?? []))
  into.partial.push(...(r.partial ?? []))
  into.versionBytes += r.versionBytes ?? 0
  return into
}

// failChunk records a whole request that failed (network, 5xx) as failed cards.
export function failChunk(into: EditResult, ids: string[], error: string): EditResult {
  for (const id of ids) into.failed.push({ id, error })
  return into
}

// retryIds: what "retry" may send again — failed cards and conflicts. A partial card was
// written in part: a shift applied again would move its finished members twice, so a shift
// never retries it. Setting a value is idempotent, so a set retries partial cards too.
export function retryIds(r: EditResult, shift: boolean): string[] {
  const partial = new Set(r.partial)
  const out: string[] = []
  const seen = new Set<string>()
  const candidates = [...r.failed.map((f) => f.id), ...r.conflicts, ...(shift ? [] : r.partial)]
  for (const id of candidates) {
    if ((shift && partial.has(id)) || seen.has(id)) continue
    seen.add(id)
    out.push(id)
  }
  return out
}
