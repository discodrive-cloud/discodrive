import { expect, it } from 'vitest'
import { backoffDelay } from './backoff'

it('grows exponentially and stays within the jitter window', () => {
  expect(backoffDelay(1, 500, 8000, () => 0)).toBe(250)
  expect(backoffDelay(1, 500, 8000, () => 1)).toBe(500)
  expect(backoffDelay(2, 500, 8000, () => 0)).toBe(500)
  expect(backoffDelay(3, 500, 8000, () => 1)).toBe(2000)
})

it('is capped', () => {
  expect(backoffDelay(20, 500, 8000, () => 1)).toBe(8000)
  expect(backoffDelay(20, 500, 8000, () => 0)).toBe(4000)
})
