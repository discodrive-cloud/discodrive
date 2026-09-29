import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { useDialog, useDialogState } from './useDialog'

beforeEach(() => {
  const states = new Map<string, { value: any }>()
  vi.stubGlobal('useState', (key: string, init: () => any) => {
    if (!states.has(key)) states.set(key, { value: init() })
    return states.get(key)
  })
})
afterEach(() => vi.unstubAllGlobals())

it('a second dialog resolves the first as cancelled', async () => {
  const { confirm, prompt } = useDialog()
  const first = confirm('Delete?')
  const second = prompt('Name', 'x')
  await expect(first).resolves.toBe(false)
  expect(useDialogState().value?.title).toBe('Name')
  const third = confirm('Again?')
  await expect(second).resolves.toBeNull()
  useDialogState().value!.resolve(true)
  await expect(third).resolves.toBe(true)
})
