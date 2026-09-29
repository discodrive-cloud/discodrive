import { afterEach, describe, expect, it } from 'vitest'
import { dateToUtcIso, formatAllDay, isoToUtcDate, localDateKey, occursOnDay, startsOnDay } from './calendarDates'

// Node reads process.env.TZ on every Date operation, so a test can move the browser.
const origTZ = process.env.TZ
function inZone(tz: string) { process.env.TZ = tz }
afterEach(() => { process.env.TZ = origTZ })

// the old tasks.vue: local midnight turned into an instant
const oldDateToIso = (v: string) => new Date(v + 'T00:00:00').toISOString()

describe('all-day values are dates', () => {
  it('a picked date is sent as that date in every zone', () => {
    for (const tz of ['Asia/Bishkek', 'America/Los_Angeles', 'UTC', 'Pacific/Auckland']) {
      inZone(tz)
      expect(dateToUtcIso('2026-09-28')).toBe('2026-09-28T00:00:00.000Z')
      expect(isoToUtcDate(dateToUtcIso('2026-09-28'))).toBe('2026-09-28')
    }
  })
  it('the old local-midnight form moved a Bishkek due date to the day before', () => {
    inZone('Asia/Bishkek')
    expect(isoToUtcDate(oldDateToIso('2026-09-28'))).toBe('2026-09-27')
  })
  it('is shown as its own date west of UTC', () => {
    inZone('America/New_York')
    expect(formatAllDay('2026-09-28T00:00:00Z', 'en-US')).toBe('9/28/2026')
  })
})

describe('occursOnDay', () => {
  const day = (y: number, m: number, d: number) => new Date(y, m - 1, d)

  it('places an all-day event by its dates, whatever the zone', () => {
    const ev = { start: '2026-08-10T00:00:00Z', end: '2026-08-12T00:00:00Z', all_day: true }
    for (const tz of ['America/New_York', 'Asia/Bishkek', 'UTC']) {
      inZone(tz)
      expect(occursOnDay(ev, day(2026, 8, 9))).toBe(false)
      expect(occursOnDay(ev, day(2026, 8, 10))).toBe(true)
      expect(occursOnDay(ev, day(2026, 8, 11))).toBe(true)
      expect(occursOnDay(ev, day(2026, 8, 12))).toBe(false) // DTEND is exclusive
    }
  })
  it('an all-day event without a later end is one day', () => {
    inZone('America/New_York')
    const ev = { start: '2026-08-10T00:00:00Z', end: '2026-08-10T00:00:00Z', all_day: true }
    expect(occursOnDay(ev, day(2026, 8, 10))).toBe(true)
    expect(occursOnDay(ev, day(2026, 8, 9))).toBe(false)
  })
  it('shows a timed multi-day event on every day it covers', () => {
    inZone('Asia/Bishkek')
    const ev = { start: '2026-08-10T16:00:00Z', end: '2026-08-12T04:00:00Z', all_day: false } // 22:00 10th – 10:00 12th local
    expect(occursOnDay(ev, day(2026, 8, 10))).toBe(true)
    expect(occursOnDay(ev, day(2026, 8, 11))).toBe(true)
    expect(occursOnDay(ev, day(2026, 8, 12))).toBe(true)
    expect(occursOnDay(ev, day(2026, 8, 13))).toBe(false)
    expect(startsOnDay(ev, day(2026, 8, 10))).toBe(true)
    expect(startsOnDay(ev, day(2026, 8, 11))).toBe(false)
  })
  it('an event ending at midnight does not spill into the next day', () => {
    inZone('Asia/Bishkek')
    const ev = { start: '2026-08-10T12:00:00Z', end: '2026-08-10T18:00:00Z', all_day: false } // 18:00–24:00 local
    expect(occursOnDay(ev, day(2026, 8, 10))).toBe(true)
    expect(occursOnDay(ev, day(2026, 8, 11))).toBe(false)
  })
  it('localDateKey uses the local date', () => {
    inZone('Asia/Bishkek')
    expect(localDateKey(new Date('2026-08-10T20:00:00Z'))).toBe('2026-08-11')
  })
})
