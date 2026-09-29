// Date helpers for the calendar and tasks pages.
//
// All-day values are dates, not instants: the server sends them as midnight UTC
// ("2026-08-10T00:00:00Z") and expects them back in that form. Reading one with local
// getters (or building one from local midnight) moves it to the neighbouring day in every
// zone east or west of UTC — including a browser that reports UTC (Firefox with
// resistFingerprinting) while the server or the user is elsewhere.

const pad = (n: number) => String(n).padStart(2, '0')

/** YYYY-MM-DD of an all-day value (read in UTC). */
export function utcDateKey(iso: string): string {
  const d = new Date(iso)
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth() + 1)}-${pad(d.getUTCDate())}`
}

/** YYYY-MM-DD of a local calendar day. */
export function localDateKey(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}

/** An <input type="date"> value as the all-day value the server expects. */
export function dateToUtcIso(v: string): string {
  return v ? `${v}T00:00:00.000Z` : ''
}

/** An all-day value as an <input type="date"> value. */
export function isoToUtcDate(iso: string): string {
  return iso ? utcDateKey(iso) : ''
}

/** A localized date for an all-day value. */
export function formatAllDay(iso: string, locale?: string): string {
  return new Date(iso).toLocaleDateString(locale, { timeZone: 'UTC' })
}

export interface Span { start: string; end: string; all_day: boolean }

/**
 * Whether an occurrence falls on a local calendar day. All-day: by calendar date, the end
 * date exclusive (a one-day event ends the next day; an end not after the start counts as
 * one day). Timed: when it overlaps the day, so a multi-day event shows on every day it
 * covers, not only the first.
 */
export function occursOnDay(ev: Span, day: Date): boolean {
  if (ev.all_day) {
    const k = localDateKey(day)
    const s = utcDateKey(ev.start)
    const e = ev.end ? utcDateKey(ev.end) : s
    return e > s ? k >= s && k < e : k === s
  }
  const dayStart = new Date(day.getFullYear(), day.getMonth(), day.getDate()).getTime()
  const dayEnd = new Date(day.getFullYear(), day.getMonth(), day.getDate() + 1).getTime()
  const st = new Date(ev.start).getTime()
  const en = ev.end ? new Date(ev.end).getTime() : st
  if (en <= st) return st >= dayStart && st < dayEnd
  return st < dayEnd && en > dayStart
}

/** Whether a timed occurrence starts on this local day (its time is shown there only). */
export function startsOnDay(ev: Span, day: Date): boolean {
  return !ev.all_day && localDateKey(new Date(ev.start)) === localDateKey(day)
}

/** The browser's IANA zone, "" when unknown. Firefox with resistFingerprinting says "UTC". */
export function browserTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || ''
  } catch {
    return ''
  }
}
