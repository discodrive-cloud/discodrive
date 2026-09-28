import { describe, expect, it } from 'vitest'
import { normalizePairCode } from './pairCode'

describe('normalizePairCode', () => {
  it('keeps a code typed as shown', () => {
    expect(normalizePairCode('ABCD-EFGH')).toBe('ABCD-EFGH')
  })
  it('accepts lower case, spaces and a missing dash', () => {
    expect(normalizePairCode(' abcd efgh ')).toBe('ABCD-EFGH')
    expect(normalizePairCode('abcdefgh')).toBe('ABCD-EFGH')
  })
  it('rejects anything that cannot be a code', () => {
    expect(normalizePairCode('')).toBe('')
    expect(normalizePairCode('ABC-EFGH')).toBe('')
    expect(normalizePairCode('ABCD-EFGH-J')).toBe('')
    expect(normalizePairCode('ABCD-EF/H')).toBe('')
  })
})
