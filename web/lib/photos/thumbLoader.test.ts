import { describe, it, expect } from 'vitest'
import { ThumbLoader, ThumbError, type ThumbWant } from './thumbLoader'

interface Pending {
  url: string
  signal: AbortSignal
  resolve: () => void
  reject: (status?: number) => void
}

function setup(opts: { concurrency?: number; capacity?: number } = {}) {
  const pending: Pending[] = []
  const started: string[] = []
  const revoked: string[] = []
  const changes: [string, string | null | undefined][] = []
  let n = 0
  const loader = new ThumbLoader(
    (url, signal) => new Promise<Blob>((resolve, reject) => {
      started.push(url)
      pending.push({ url, signal, resolve: () => resolve({} as Blob), reject: (status = 404) => reject(new ThumbError(status)) })
    }),
    {
      ...opts,
      makeURL: () => `blob:${++n}`,
      revokeURL: (u) => revoked.push(u),
      onChange: (k, u) => changes.push([k, u]),
    },
  )
  const finish = async (url: string, ok = true, status = 404) => {
    const p = [...pending].reverse().find((x) => x.url === url)!
    if (ok) p.resolve(); else p.reject(status)
    await Promise.resolve(); await Promise.resolve()
  }
  return { loader, pending, started, revoked, changes, finish }
}

const want = (key: string, priority = 0): ThumbWant => ({ key, url: `/t/${key}`, priority })

describe('ThumbLoader', () => {
  it('keeps at most 6 requests in flight', async () => {
    const { loader, started, finish } = setup()
    loader.sync(Array.from({ length: 10 }, (_, i) => want(`k${i}`)))
    expect(started).toHaveLength(6)
    expect(loader.active).toBe(6)
    expect(loader.queued).toBe(4)
    await finish('/t/k0')
    expect(started).toHaveLength(7)
    expect(loader.active).toBe(6)
  })

  it('serves the lowest priority value (visible tiles) first', async () => {
    const { loader, started, finish } = setup({ concurrency: 1 })
    loader.sync([want('far', 5), want('vis', 0), want('near', 1)])
    // The first request starts at once; among the rest the visible one goes next.
    expect(started).toEqual(['/t/vis'])
    await finish('/t/vis')
    expect(started).toEqual(['/t/vis', '/t/near'])
  })

  it('aborts requests and drops queued entries the grid no longer wants', async () => {
    const { loader, pending, started, finish, changes } = setup({ concurrency: 2 })
    loader.sync([want('a'), want('b'), want('c')])
    expect(started).toEqual(['/t/a', '/t/b'])
    loader.sync([want('b')]) // a scrolled away (in flight), c scrolled away (queued)
    expect(pending.find((p) => p.url === '/t/a')!.signal.aborted).toBe(true)
    expect(pending.find((p) => p.url === '/t/b')!.signal.aborted).toBe(false)
    expect(loader.queued).toBe(0)
    await finish('/t/a') // a late answer for an aborted request is ignored
    expect(loader.get('a')).toBeUndefined()
    await finish('/t/b')
    expect(loader.get('b')).toBe('blob:1')
    expect(started).toEqual(['/t/a', '/t/b']) // c never fetched
    expect(changes).toEqual([['b', 'blob:1']])
  })

  it('a cached key is not fetched again', async () => {
    const { loader, started, finish } = setup()
    loader.sync([want('a')])
    await finish('/t/a')
    loader.sync([want('a')])
    expect(started).toEqual(['/t/a'])
  })

  it('records failures as null', async () => {
    const { loader, finish, changes } = setup()
    loader.sync([want('bad')])
    await finish('/t/bad', false)
    expect(loader.get('bad')).toBeNull()
    expect(changes).toEqual([['bad', null]])
  })

  it('does not cache transient failures: the next sync asks again', async () => {
    const { loader, finish, started, changes } = setup()
    loader.sync([want('t')])
    await finish('/t/t', false, 503)
    expect(loader.get('t')).toBeUndefined()
    expect(changes).toEqual([])
    loader.sync([want('t')])
    expect(started).toEqual(['/t/t', '/t/t'])
    await finish('/t/t')
    expect(loader.get('t')).toBe('blob:1')
  })

  it('forget drops a key and reports it', async () => {
    const { loader, finish, revoked, changes } = setup()
    loader.sync([want('a')]); await finish('/t/a')
    loader.forget('a')
    expect(revoked).toEqual(['blob:1'])
    expect(changes).toContainEqual(['a', undefined])
    loader.forget('nope') // unknown: no-op
  })

  it('evicts least recently used object URLs beyond the capacity and revokes them', async () => {
    const { loader, finish, revoked, changes } = setup({ capacity: 2 })
    loader.sync([want('a')]); await finish('/t/a') // blob:1
    loader.sync([want('b')]); await finish('/t/b') // blob:2
    loader.sync([want('a')]) // touch a: b is now the oldest
    loader.sync([want('c')]); await finish('/t/c') // blob:3 -> evicts b
    expect(revoked).toEqual(['blob:2'])
    expect(loader.get('b')).toBeUndefined()
    expect(loader.get('a')).toBe('blob:1')
    expect(loader.size).toBe(2)
    expect(changes).toContainEqual(['b', undefined])
  })

  it('never evicts what is on screen, even past the capacity', async () => {
    const { loader, finish, revoked } = setup({ capacity: 1 })
    loader.sync([want('a'), want('b')])
    await finish('/t/a')
    await finish('/t/b')
    expect(revoked).toEqual([])
    expect(loader.size).toBe(2)
    loader.sync([want('b')]) // a scrolls away; evicted on the next load
    loader.sync([want('b'), want('c')]); await finish('/t/c')
    expect(revoked).toEqual(['blob:1'])
  })

  it('clear revokes everything and aborts in-flight requests', async () => {
    const { loader, pending, finish, revoked } = setup()
    loader.sync([want('a'), want('b')])
    await finish('/t/a')
    loader.clear()
    expect(revoked).toEqual(['blob:1'])
    expect(pending.find((p) => p.url === '/t/b')!.signal.aborted).toBe(true)
    expect(loader.size).toBe(0)
  })
})
