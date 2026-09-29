import { afterEach, beforeEach, expect, it, vi } from 'vitest'

beforeEach(() => {
  vi.useFakeTimers()
  const states = new Map<string, { value: any }>()
  vi.stubGlobal('useState', (key: string, init: () => any) => {
    if (!states.has(key)) states.set(key, { value: init() })
    return states.get(key)
  })
  vi.stubGlobal('useStorageTick', () => ({ value: 0 }))
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

it('waits before retrying a chunk after a network error', async () => {
  const chunkPuts: number[] = []
  let failures = 2
  vi.stubGlobal('useApi', () => ({ request: async (url: string, opts: any = {}) => {
    if (url === '/upload/init') return { upload_id: 'u1' }
    if (url === '/upload/u1/chunk/0') {
      chunkPuts.push(Date.now())
      if (failures-- > 0) throw new Error('network')
      return { next_chunk: 1 }
    }
    if (url === '/upload/u1' && !opts.method) return { next_chunk: 0 }
    if (url === '/upload/u1/complete') return {}
    throw new Error(url)
  }}))
  const { useUploads } = await import('./useUploads')
  const up = useUploads()
  up.enqueue([{ file: new File(['hello'], 'a.txt'), parentId: null }])
  await vi.advanceTimersByTimeAsync(0)
  expect(chunkPuts).toHaveLength(1)
  await vi.advanceTimersByTimeAsync(200) // below the minimum first backoff (250 ms)
  expect(chunkPuts).toHaveLength(1)
  await vi.advanceTimersByTimeAsync(20_000)
  expect(chunkPuts).toHaveLength(3)
  expect(chunkPuts[2] - chunkPuts[1]).toBeGreaterThanOrEqual(500) // second backoff is longer
  expect(up.tasks.value[0].status).toBe('done')
})
