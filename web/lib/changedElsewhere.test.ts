import { describe, expect, it } from 'vitest'
import { changedElsewhere } from './changedElsewhere'

describe('changedElsewhere', () => {
  it('is a 412 answer, however the fetch error carries the status', () => {
    expect(changedElsewhere({ status: 412 })).toBe(true)
    expect(changedElsewhere({ statusCode: 412 })).toBe(true)
    expect(changedElsewhere({ response: { status: 412 } })).toBe(true)
  })
  it('is not any other failure', () => {
    expect(changedElsewhere({ status: 409 })).toBe(false)
    expect(changedElsewhere({ status: 500 })).toBe(false)
    expect(changedElsewhere(new Error('network'))).toBe(false)
    expect(changedElsewhere(null)).toBe(false)
  })
})
