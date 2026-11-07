import { describe, expect, it } from 'vitest'
import { relativeDate, sourceLabel } from './linkSource'

describe('sourceLabel', () => {
  it('names known sources', () => {
    expect(sourceLabel('instagram')).toBe('Instagram')
    expect(sourceLabel('threads')).toBe('Threads')
    expect(sourceLabel('reddit')).toBe('Reddit')
    expect(sourceLabel('youtube')).toBe('YouTube')
    expect(sourceLabel('x')).toBe('X')
    expect(sourceLabel('tiktok')).toBe('TikTok')
  })
  it('leaves other and unknown sources unnamed', () => {
    expect(sourceLabel('other')).toBe('')
    expect(sourceLabel('somethingnew')).toBe('')
  })
})

describe('relativeDate', () => {
  const now = new Date('2026-11-08T15:00:00')
  it('says today and yesterday by calendar day', () => {
    expect(relativeDate('2026-11-08T01:00:00', now, 'ru')).toBe('сегодня')
    expect(relativeDate('2026-11-07T23:30:00', now, 'ru')).toBe('вчера')
    expect(relativeDate('2026-11-08T01:00:00', now, 'en')).toBe('today')
  })
  it('counts days within a month', () => {
    expect(relativeDate('2026-11-05T12:00:00', now, 'ru')).toBe('3 дня назад')
  })
  it('shows a date for older links', () => {
    expect(relativeDate('2026-08-01T12:00:00', now, 'ru')).toBe(new Date('2026-08-01T12:00:00').toLocaleDateString('ru', { day: 'numeric', month: 'short' }))
    expect(relativeDate('2025-08-01T12:00:00', now, 'ru')).toContain('2025')
  })
  it('survives garbage', () => {
    expect(relativeDate('not a date', now, 'ru')).toBe('')
  })
})
