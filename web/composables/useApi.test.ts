import { afterEach, beforeEach, expect, it, vi } from 'vitest'
let api: typeof import('./useApi')
let fetchMock: ReturnType<typeof vi.fn> & { raw: ReturnType<typeof vi.fn> }
const token = (sid: string, iat = 1) => `e30.${Buffer.from(JSON.stringify({ sid, sub: 'user', iat })).toString('base64url')}.signature`
const session = (sid: string, iat = 1) => ({ token: token(sid, iat), role: 'user', email: 'a@example.test' })
function deferred<T>() {
  let resolve!: (v: T) => void, reject!: (err: unknown) => void
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej })
  return { promise, resolve, reject }
}
beforeEach(async () => {
  vi.resetModules()
  fetchMock = Object.assign(vi.fn().mockResolvedValue(undefined), { raw: vi.fn() })
  vi.stubGlobal('$fetch', { create: () => fetchMock })
  const states = new Map<string, { value: any }>()
  vi.stubGlobal('useState', (key: string, init: () => any) => {
    if (!states.has(key)) states.set(key, { value: init() })
    return states.get(key)
  })
  vi.stubGlobal('useStorageTick', () => ({ value: 0 }))
  vi.stubGlobal('resetPlayerSession', vi.fn())
  vi.stubGlobal('lockVault', vi.fn())
  vi.stubGlobal('navigateTo', vi.fn())
  api = await import('./useApi')
  api.setSession(session('first'))
})
afterEach(() => vi.unstubAllGlobals())
it('revokes on the server before clearing local state', async () => {
  const response = deferred<void>(); fetchMock.mockReturnValueOnce(response.promise)
  const logout = api.logoutSession()
  expect(api.useSession().value.token).toBe(token('first'))
  expect(fetchMock).toHaveBeenCalledWith('/auth/logout', { method: 'POST', headers: { Authorization: `Bearer ${token('first')}` } })
  response.resolve(); expect(await logout).toBe(true)
  expect(api.useSession().value.token).toBe('')
})
it('preserves state on network error', async () => {
  fetchMock.mockRejectedValueOnce(new Error('offline'))
  await expect(api.logoutSession()).rejects.toThrow('offline')
  expect(api.useSession().value.token).toBe(token('first'))
})
it('accepts 401 as already revoked', async () => {
  fetchMock.mockRejectedValueOnce({ response: { status: 401 } })
  expect(await api.logoutSession()).toBe(true); expect(api.useSession().value.token).toBe('')
})
it('late X-Token cannot restore a logged out session', async () => {
  const response = deferred<any>(); fetchMock.raw.mockReturnValueOnce(response.promise)
  const request = api.useApi().request('/me'); await api.logoutSession()
  response.resolve({ headers: new Headers({ 'X-Token': token('first', 2) }), _data: {} })
  await request; expect(api.useSession().value.token).toBe('')
})
it('pending logout cannot clear another login', async () => {
  const response = deferred<void>(); fetchMock.mockReturnValueOnce(response.promise)
  const logout = api.logoutSession(); api.setSession(session('second')); response.resolve()
  expect(await logout).toBe(false); expect(api.useSession().value.token).toBe(token('second'))
})
it('logout clears renewals from the same session', async () => {
  const response = deferred<void>(); fetchMock.mockReturnValueOnce(response.promise)
  const logout = api.logoutSession(); api.setSession(session('first', 2)); response.resolve()
  expect(await logout).toBe(true); expect(api.useSession().value.token).toBe('')
})
it('late 401 cannot sign out a new login', async () => {
  const response = deferred<any>(); fetchMock.raw.mockReturnValueOnce(response.promise)
  const request = api.useApi().request('/me'); api.setSession(session('second'))
  response.reject({ response: { status: 401 } })
  await expect(request).rejects.toEqual({ response: { status: 401 } })
  expect(api.useSession().value.token).toBe(token('second'))
})
it('401 signs out by default', async () => {
  fetchMock.raw.mockRejectedValueOnce({ response: { status: 401 } })
  await expect(api.useApi().request('/me')).rejects.toEqual({ response: { status: 401 } })
  expect(api.useSession().value.token).toBe('')
})
it('keepSessionOn401 leaves the session for a wrong credential', async () => {
  const err = { response: { status: 401 }, data: { error: 'invalid current password' } }
  fetchMock.raw.mockRejectedValueOnce(err)
  await expect(api.useApi().request('/me/password', { method: 'PUT', keepSessionOn401: true })).rejects.toBe(err)
  expect(api.useSession().value.token).toBe(token('first'))
  expect(fetchMock.raw.mock.calls[0][1]).not.toHaveProperty('keepSessionOn401')
})
it('follows a logout in another tab', () => {
  expect(api.followStoredSession(JSON.stringify({ token: '', role: '', email: '' }))).toBe('cleared')
  expect(api.useSession().value.token).toBe('')
  expect(api.followStoredSession(null)).toBe('none')
})
it('adopts a renewal of the same account quietly and never steps back', () => {
  expect(api.followStoredSession(JSON.stringify(session('first', 5)))).toBe('renewed')
  expect(api.useSession().value.token).toBe(token('first', 5))
  expect(api.followStoredSession(JSON.stringify(session('first', 3)))).toBe('none')
  expect(api.useSession().value.token).toBe(token('first', 5))
})
it('switches to another account signed in elsewhere', () => {
  const other = { token: `e30.${Buffer.from(JSON.stringify({ sid: 'x', sub: 'user-b', iat: 1 })).toString('base64url')}.s`, role: 'admin', email: 'b@example.test' }
  const lock = (globalThis as any).lockVault as ReturnType<typeof vi.fn>
  lock.mockClear()
  expect(api.followStoredSession(JSON.stringify(other))).toBe('switched')
  expect(api.useSession().value).toEqual(other)
  expect(lock).toHaveBeenCalled()
})
it('treats a corrupt stored session as signed out', () => {
  expect(api.followStoredSession('{not json')).toBe('cleared')
})
it('logout and an account switch lock the vault', () => {
  const lock = (globalThis as any).lockVault as ReturnType<typeof vi.fn>
  lock.mockClear()
  api.setSession(session('first', 2)) // renewal of the same account
  expect(lock).not.toHaveBeenCalled()
  api.setSession({ ...session('other'), email: 'b@example.test' })
  expect(lock).toHaveBeenCalledTimes(1)
  api.clearSession()
  expect(lock).toHaveBeenCalledTimes(2)
})
