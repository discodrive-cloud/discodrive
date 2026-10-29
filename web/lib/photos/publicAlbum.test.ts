import { describe, expect, it } from 'vitest'
import { monthsOf, publicAssetURL } from './publicAlbum'

describe('publicAssetURL', () => {
  it('turns the gallery address into the link one', () => {
    expect(publicAssetURL('/me/photos/v1/assets/abc/thumb?v=1%3A2', 'T0k', 'K.ey'))
      .toBe('/public/photos/v1/T0k/assets/abc/thumb?v=1%3A2&k=K.ey')
    expect(publicAssetURL('/me/photos/v1/assets/abc/preview', 'T', ''))
      .toBe('/public/photos/v1/T/assets/abc/preview')
  })
  it('refuses anything else', () => {
    expect(publicAssetURL('/files/abc/content', 'T', 'K')).toBeNull()
    expect(publicAssetURL('https://evil.test/me/photos/v1/assets/a/thumb', 'T', 'K')).toBeNull()
    expect(publicAssetURL('/me/photos/v1/assets/a/b/thumb', 'T', 'K')).toBeNull()
  })
})

describe('monthsOf', () => {
  it('groups newest first by the wall-clock month', () => {
    const rows = [
      { id: 'a', takenAt: '2021-03-31T22:00:00Z', tzOffsetMin: 180 }, // April where taken
      { id: 'b', takenAt: '2021-03-10T10:00:00Z', tzOffsetMin: null },
      { id: 'c', takenAt: '2019-01-01T00:00:00Z', tzOffsetMin: 0 },
    ]
    const wall = (r: (typeof rows)[number]) => new Date(Date.parse(r.takenAt) + (r.tzOffsetMin ?? 0) * 60000)
    const { buckets, rows: by } = monthsOf(rows, wall)
    expect(buckets).toEqual([{ month: '2021-04', count: 1 }, { month: '2021-03', count: 1 }, { month: '2019-01', count: 1 }])
    expect(by.get('2021-04')!.map((r) => r.id)).toEqual(['a'])
  })
})
