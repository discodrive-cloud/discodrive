import { afterEach, expect, it, vi } from 'vitest'
import { useVault } from './useVault'

vi.mock('../lib/cryptomator/index.js', () => ({
  openVault: vi.fn(async () => ({})),
  WrongPasswordError: class extends Error {},
  dirIdHash: vi.fn(async () => 'd/AA/BB'),
  decryptName: vi.fn(async (_keys, name) => name.replace('.c9r', '')),
  decryptContent: vi.fn(),
}))
afterEach(() => vi.unstubAllGlobals())

it('opens with bounded parallel metadata reads and reuses the root listing', async () => {
  let active = 0, peak = 0, rootReads = 0
  const states = new Map<string, { value: any }>()
  vi.stubGlobal('useState', (key: string, init: () => any) => {
    if (!states.has(key)) states.set(key, { value: init() })
    return states.get(key)
  })
  const node = (id: string, name = id, is_dir = true) => ({ id, name, is_dir, size: 0, version: 1 })
  vi.stubGlobal('useApi', () => ({ request: async (url: string) => {
    peak = Math.max(peak, ++active)
    await new Promise(resolve => setTimeout(resolve, 2))
    active--
    if (url === '/files?parent_id=vault') {
      rootReads++
      return [node('mk', 'masterkey.cryptomator', false), node('jwt', 'vault.cryptomator', false), node('d')]
    }
    if (url === '/files?parent_id=d') return [node('AA')]
    if (url === '/files?parent_id=AA') return [node('BB')]
    if (url === '/files?parent_id=BB') return Array.from({length: 17}, (_, i) => node(`folder-${i}`, `folder-${i}.c9r`))
    if (url.startsWith('/files?parent_id=folder-')) return [node(url.split('=')[1] + '-dir', 'dir.c9r', false)]
    if (url.endsWith('/content')) return new Blob(['metadata'])
    throw new Error(url)
  }}))
  await useVault().unlock(node('vault'), 'password')
  expect(rootReads).toBe(1)
  expect(peak).toBeGreaterThan(1)
  expect(peak).toBeLessThanOrEqual(6)
  expect(states.get('vault_entries')!.value.map((entry: any) => entry.name))
    .toEqual(Array.from({length: 17}, (_, i) => `folder-${i}`))
})
