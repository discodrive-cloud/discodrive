import { describe, expect, test } from 'vitest'
import { DUP_POLL_MS, deleteWarnings, deletedBytes, groupKey, mergeQueue, nextLoad, skipGroup } from './duplicates'

const a = (id: string, live = false, edited = false, bytes = 0) => ({ id, live, edited, bytes })

describe('deleteWarnings', () => {
  test('deleteWarnings: удаляемая Live → [live]', () => {
    expect(deleteWarnings([a('k'), a('d', true)], 'k')).toEqual(['live'])
  })
  test('deleteWarnings: Live оставляем → []', () => {
    expect(deleteWarnings([a('k', true), a('d')], 'k')).toEqual([])
  })
  test('deleteWarnings: удаляемая с правкой → [edited]', () => {
    expect(deleteWarnings([a('k'), a('d', false, true)], 'k')).toEqual(['edited'])
  })
  test('deleteWarnings: both, each once, live first', () => {
    expect(deleteWarnings([a('d1', false, true), a('k', true, true), a('d2', true), a('d3', true, true)], 'k')).toEqual(['live', 'edited'])
  })
})

describe('skipGroup', () => {
  test('skipGroup переносит группу в конец, порядок остальных сохранён', () => {
    const q = ['a', 'b', 'c', 'd']
    expect(skipGroup(q, 1)).toEqual(['a', 'c', 'd', 'b'])
    expect(q).toEqual(['a', 'b', 'c', 'd'])
  })
  test('skipGroup: the last one or an index out of range leaves the order', () => {
    expect(skipGroup(['a', 'b'], 1)).toEqual(['a', 'b'])
    expect(skipGroup(['a', 'b'], 5)).toEqual(['a', 'b'])
    expect(skipGroup([], 0)).toEqual([])
  })
})

describe('deletedBytes', () => {
  test('sums the bytes of every copy but the kept one', () => {
    expect(deletedBytes([a('k', false, false, 10), a('d1', false, false, 5), a('d2', false, false, 7)], 'k')).toBe(12)
    expect(deletedBytes([a('k', false, false, 10)], 'k')).toBe(0)
  })
})

describe('mergeQueue', () => {
  const g = (...ids: string[]) => ({ assets: ids.map((id) => ({ id })) })
  test('groupKey does not depend on the order of the copies', () => {
    expect(groupKey(g('b', 'a'))).toBe(groupKey(g('a', 'b')))
    expect(groupKey(g('a', 'b'))).not.toBe(groupKey(g('a', 'b', 'c')))
  })
  test('keeps the queue order (skipped ones stay at the end), drops gone groups, appends new ones', () => {
    const queue = [g('c', 'd'), g('e', 'f'), g('a', 'b')] // a/b was skipped
    const fresh = [g('a', 'b'), g('c', 'd'), g('x', 'y')] // e/f resolved elsewhere, x/y new
    expect(mergeQueue(queue, fresh).map(groupKey)).toEqual([g('c', 'd'), g('a', 'b'), g('x', 'y')].map(groupKey))
  })
  test('takes the fresh version of a group that stayed', () => {
    const fresh = [{ assets: [{ id: 'a', v: 2 }, { id: 'b', v: 2 }] }]
    expect(mergeQueue([{ assets: [{ id: 'a', v: 1 }, { id: 'b', v: 1 }] }], fresh)[0]).toBe(fresh[0])
  })
})

describe('nextLoad', () => {
  test('polls only while the server is scanning', () => {
    expect(nextLoad({ ok: true, scanning: true, loaded: true, retried: false })).toBe(DUP_POLL_MS)
    expect(nextLoad({ ok: true, scanning: false, loaded: true, retried: false })).toBeNull()
  })
  test('a failed first load is retried once', () => {
    expect(nextLoad({ ok: false, scanning: false, loaded: false, retried: false })).toBe(DUP_POLL_MS)
    expect(nextLoad({ ok: false, scanning: false, loaded: false, retried: true })).toBeNull()
  })
  test('a failed later load keeps polling only while the scan goes on', () => {
    expect(nextLoad({ ok: false, scanning: true, loaded: true, retried: false })).toBe(DUP_POLL_MS)
    expect(nextLoad({ ok: false, scanning: false, loaded: true, retried: false })).toBeNull()
  })
})
