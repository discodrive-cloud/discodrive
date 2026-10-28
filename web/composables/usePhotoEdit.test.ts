import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import type { EditResult } from '../lib/photos/edit'

let calls: { ids: string[]; edit: Record<string, unknown> }[]
let answers: Partial<EditResult>[]

beforeEach(() => {
  calls = []
  answers = []
  vi.stubGlobal('usePhotosApi', () => ({
    asset: vi.fn(),
    metadata: vi.fn(async (ids: string[], edit: Record<string, unknown>) => {
      calls.push({ ids, edit })
      return { updated: [], conflicts: [], failed: [], partial: [], versionBytes: 0, ...answers.shift() }
    }),
  }))
})
afterEach(() => vi.unstubAllGlobals())

async function composable() {
  vi.resetModules()
  return (await import('./usePhotoEdit')).usePhotoEdit(() => ['a', 'b', 'p', 'f'])
}

it('retries a shift with the shift, never the partial cards', async () => {
  const e = await composable()
  answers.push({ updated: ['a'], conflicts: ['b'], failed: [{ id: 'f', error: 'x' }], partial: ['p'] })
  await e.run(['a', 'b', 'p', 'f'], { shiftMinutes: 120 })
  expect(e.resultShift.value).toBe(true)
  expect(e.retryList()).toEqual(['f', 'b'])
  await e.retry()
  expect(calls[1]).toEqual({ ids: ['f', 'b'], edit: { shiftMinutes: 120 } })
})

it('retries a set with the same values, partial cards included', async () => {
  const e = await composable()
  answers.push({ conflicts: ['b'], partial: ['p'] })
  const set = { takenAt: '2019-07-14T19:42:00+06:00', tzOffsetMin: 360 }
  await e.run(['b', 'p'], set)
  expect(e.resultShift.value).toBe(false)
  await e.retry()
  expect(calls[1]).toEqual({ ids: ['b', 'p'], edit: set })
})

it('keeps the mode of the run that produced the summary', async () => {
  const e = await composable()
  answers.push({ failed: [{ id: 'f', error: 'x' }], partial: ['p'] })
  await e.run(['f', 'p'], { shiftMinutes: -60 })
  // whatever the dialog's toggle says afterwards, the retry is that run's shift
  expect(e.retryList()).toEqual(['f'])
  await e.retry()
  expect(calls[1].edit).toEqual({ shiftMinutes: -60 })
  expect(calls[1].ids).not.toContain('p')
})

it('does nothing when there is nothing to retry', async () => {
  const e = await composable()
  answers.push({ updated: ['a'], partial: ['p'] })
  await e.run(['a', 'p'], { shiftMinutes: 5 })
  expect(await e.retry()).toBeNull()
  expect(calls).toHaveLength(1)
})
