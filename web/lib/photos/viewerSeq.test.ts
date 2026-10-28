import { describe, it, expect } from 'vitest'
import { step, around, type Seq } from './viewerSeq'

const r = (...ids: string[]) => ids.map((id) => ({ id }))

function seq(): Seq {
  return {
    months: ['2024-03', '2024-02', '2024-01', '2023-12'],
    rows: new Map([
      ['2024-03', r('a', 'b')],
      ['2024-02', r()],
      ['2024-01', r('c', 'd')],
    ]),
  }
}

describe('step', () => {
  it('moves inside a month', () => {
    expect(step(seq(), { month: '2024-03', id: 'a' }, 1)).toEqual({ to: { month: '2024-03', id: 'b' } })
    expect(step(seq(), { month: '2024-01', id: 'd' }, -1)).toEqual({ to: { month: '2024-01', id: 'c' } })
  })
  it('crosses months, skipping empty ones', () => {
    expect(step(seq(), { month: '2024-03', id: 'b' }, 1)).toEqual({ to: { month: '2024-01', id: 'c' } })
    expect(step(seq(), { month: '2024-01', id: 'c' }, -1)).toEqual({ to: { month: '2024-03', id: 'b' } })
  })
  it('asks for a month that is not loaded', () => {
    expect(step(seq(), { month: '2024-01', id: 'd' }, 1)).toEqual({ load: '2023-12' })
  })
  it('stops at the ends', () => {
    expect(step(seq(), { month: '2024-03', id: 'a' }, -1)).toBeNull()
    const s = seq()
    s.rows.set('2023-12', r('e'))
    expect(step(s, { month: '2023-12', id: 'e' }, 1)).toBeNull()
    expect(step(s, { month: '2023-12', id: 'zzz' }, 1)).toBeNull()
  })
})

describe('around', () => {
  it('collects neighbours across loaded months', () => {
    expect(around(seq(), { month: '2024-01', id: 'c' }, 2).map((x) => x.row.id)).toEqual(['a', 'b', 'c', 'd'])
    expect(around(seq(), { month: '2024-03', id: 'a' }, 1).map((x) => x.row.id)).toEqual(['a', 'b'])
  })
})
