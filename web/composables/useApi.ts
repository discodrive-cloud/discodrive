// Session + API wrapper: JWT stored in localStorage, Authorization header on every request.
export interface Session {
  token: string
  role: string
  email: string
  // Admin-provisioned account with a temporary password: the user is locked onto
  // /change-password until they set a new one (A.2).
  mustChangePassword?: boolean
}

// The API lives at the root (/auth, /files, /setup…) while the UI is served under /app/.
// The global $fetch prefixes paths with app.baseURL='/app/', so for API calls we use
// a separate instance with baseURL='/' — otherwise requests go to /app/* and hit the SPA.
export const apiFetch = $fetch.create({ baseURL: '/' })

export const useSession = () => useState<Session>('session', () => ({ token: '', role: '', email: '' }))

// Whether the initial admin onboarding is needed (no admin exists yet).
// null = not yet determined; true/false = cached response from /setup/status.
export const useSetupNeeded = () => useState<boolean | null>('setupNeeded', () => null)

// Whether WebAuthn is configured server-side (BASE_DOMAIN set). null = not yet determined.
// Drives whether the login screen offers the passkey button.
export const useWebauthnEnabled = () => useState<boolean | null>('webauthnEnabled', () => null)

// tokenIssuedAt returns the JWT's iat (unix seconds), or 0 if missing/unparseable.
// Used to prevent a stale X-Token (from a cached/late response) from regressing the
// session. It reads iat rather than exp because the lifetime is the user's own setting:
// shortening it makes the next renewed token expire EARLIER than the current one, and
// comparing exp would reject exactly the token that carries the new preference.
function tokenIssuedAt(t: string): number {
  try {
    const payload = t.split('.')[1] || ''
    return JSON.parse(atob(payload.replace(/-/g, '+').replace(/_/g, '/'))).iat || 0
  } catch {
    return 0
  }
}

// persist=false applies a session another tab already wrote to localStorage.
export function setSession(s: Session, persist = true) {
  const sess = useSession()
  // Switching accounts without an explicit logout (login page over a live session)
  // must not carry the previous account's player queue over.
  // The same goes for an unlocked Cryptomator vault.
  if (sess.value.email && s.email && s.email !== sess.value.email) {
    resetPlayerSession()
    lockVault()
  }
  sess.value = s
  if (persist && import.meta.client) localStorage.setItem('kf_session', JSON.stringify(s))
}

export function clearSession(persist = true) {
  resetPlayerSession() // stop playback everywhere and drop the persisted queue
  lockVault() // vault keys and decrypted names must not survive a logout
  setSession({ token: '', role: '', email: '' }, persist)
}

function parseStoredSession(raw: string | null): Session {
  try {
    const s = raw ? JSON.parse(raw) : null
    if (s && typeof s.token === 'string' && s.token) return s as Session
  } catch { /* corrupt → treat as signed out */ }
  return { token: '', role: '', email: '' }
}

// followStoredSession — another tab changed kf_session (the storage event). All tabs
// share one localStorage, so the last sign-in wins everywhere; this tab follows it
// instead of silently sending requests with its old in-memory token for an account
// the stored session no longer names.
//   'none'     — nothing relevant changed
//   'renewed'  — same account (token renewal, flag change): adopted quietly
//   'cleared'  — signed out elsewhere: local state cleared, caller goes to /login
//   'switched' — a different account (or a sign-in while this tab had none): adopted,
//                caller reloads so no page keeps the previous account's data
export function followStoredSession(raw: string | null): 'none' | 'renewed' | 'cleared' | 'switched' {
  const sess = useSession()
  const cur = sess.value
  const next = parseStoredSession(raw)
  if (!next.token) {
    if (!cur.token) return 'none'
    clearSession(false)
    return 'cleared'
  }
  if (next.token === cur.token && next.mustChangePassword === cur.mustChangePassword) return 'none'
  if (cur.token && next.email === cur.email && sameSubject(cur.token, next.token)) {
    // Never step back to an older token of the same account (a slower tab's renewal).
    if (tokenIssuedAt(next.token) < tokenIssuedAt(cur.token) && next.mustChangePassword === cur.mustChangePassword) return 'none'
    setSession(next, false)
    return 'renewed'
  }
  if (cur.token) clearSession(false) // drop player queue, vault, per-account state first
  setSession(next, false)
  return 'switched'
}

function sameSubject(a: string, b: string): boolean {
  try {
    const sub = (t: string) => JSON.parse(atob(t.split('.')[1].replace(/-/g, '+').replace(/_/g, '/'))).sub
    return sub(a) === sub(b)
  } catch { return false }
}

// Only compare identities locally; server-side JWT validation remains authoritative.
function sameSession(a: string, b: string): boolean {
  if (!a || !b) return false
  if (a === b) return true
  try {
    const claims = (token: string) => JSON.parse(atob(token.split('.')[1].replace(/-/g, '+').replace(/_/g, '/')))
    const x = claims(a), y = claims(b)
    return !!x.sid && x.sid === y.sid && x.sub === y.sub
  } catch { return false }
}

export async function logoutSession(): Promise<boolean> {
  const sess = useSession()
  const token = sess.value.token
  if (!token) return true
  try {
    await apiFetch('/auth/logout', { method: 'POST', headers: { Authorization: `Bearer ${token}` } })
  } catch (e: any) {
    // Already expired/revoked also means that this token has no server access.
    if (e?.response?.status !== 401) throw e
  }
  if (!sameSession(sess.value.token, token)) return !sess.value.token
  clearSession()
  return true
}

export type RequestOptions = Record<string, any> & { keepSessionOn401?: boolean }

export function useApi() {
  const sess = useSession()
  const storageTick = useStorageTick()

  // keepSessionOn401: the endpoint answers 401 for a wrong credential the user typed
  // (current password, TOTP code, identity confirmation), not for a dead session. The
  // caller shows the error inline instead of being signed out. The server keeps its
  // status codes because other clients depend on them.
  async function request<T = any>(path: string, opts: RequestOptions = {}): Promise<T> {
    const { keepSessionOn401, ...fetchOpts } = opts
    const requestToken = sess.value.token
    const headers: Record<string, string> = { ...(fetchOpts.headers || {}) }
    if (sess.value.token) headers.Authorization = `Bearer ${sess.value.token}`
    try {
      // .raw — needed to access response headers: the server renews the session via X-Token.
      const res = await apiFetch.raw<T>(path, { ...fetchOpts, headers })
      const fresh = res.headers.get('X-Token')
      // Only adopt a renewed token if it isn't older than the current one. A cached
      // or slow-in-flight response can carry a stale X-Token; saving it would regress
      // the session to an earlier (possibly expired) token and force a spurious logout.
      if (sess.value.token === requestToken && fresh && fresh !== sess.value.token && tokenIssuedAt(fresh) >= tokenIssuedAt(sess.value.token)) {
        setSession({ ...sess.value, token: fresh })
      }
      // Anything that is not a read can change how much space the user occupies —
      // delete, purge, restore, rollback, a saved item, a tag edit. Signalling here
      // means the sidebar meter follows every operation, including ones added later.
      // Chunk uploads are the exception: they fire per 8 MiB, so useUploads signals
      // once when the file is complete instead.
      if ((fetchOpts.method || 'GET').toUpperCase() !== 'GET' && !path.startsWith('/upload/')) {
        storageTick.value++
      }
      return res._data as T
    } catch (e: any) {
      if (e?.response?.status === 401 && !keepSessionOn401 && sess.value.token === requestToken) {
        clearSession()
        await navigateTo('/login')
      }
      throw e
    }
  }

  return { request, session: sess }
}

type LoginUser = { role: string; email: string; must_change_password?: boolean }
type LoginSuccess = { token: string; user: LoginUser }
type LoginMfaRequired = { mfa_required: true; mfa_token: string; methods: string[] }
type LoginResult = LoginSuccess | LoginMfaRequired

// Login: fetches a token and stores the session, OR returns an MFA challenge without setting a session.
export async function login(email: string, password: string): Promise<LoginResult> {
  const res = await apiFetch<LoginResult>('/auth/login', {
    method: 'POST',
    body: { email, password },
  })
  if ('mfa_required' in res && res.mfa_required) {
    // MFA step required — do not set session yet; return challenge to caller.
    return res
  }
  const r = res as LoginSuccess
  setSession({
    token: r.token,
    role: r.user.role,
    email: r.user.email,
    mustChangePassword: r.user.must_change_password ?? false,
  })
  return r
}

// Passwordless sign-in with a passkey (discoverable WebAuthn): the authenticator picks the
// credential and identifies the user. Uses @github/webauthn-json's JSON get() for base64url.
export async function loginWithPasskey() {
  const begin = await apiFetch<{ options: any; session_token: string }>('/auth/webauthn/login/begin', { method: 'POST' })
  const { get } = await import('@github/webauthn-json')
  const assertion = await get(begin.options)
  const res = await apiFetch<LoginSuccess>('/auth/webauthn/login/finish', {
    method: 'POST',
    body: { session_token: begin.session_token, assertion },
  })
  setSession({
    token: res.token,
    role: res.user.role,
    email: res.user.email,
    mustChangePassword: res.user.must_change_password ?? false,
  })
  return res
}

// Complete TOTP MFA: exchange mfa_token + TOTP/backup code for a full session token.
export async function completeMfaTotp(mfaToken: string, code: string) {
  const res = await apiFetch<LoginSuccess>('/auth/mfa/totp', {
    method: 'POST',
    body: { mfa_token: mfaToken, code },
  })
  setSession({
    token: res.token,
    role: res.user.role,
    email: res.user.email,
    mustChangePassword: res.user.must_change_password ?? false,
  })
  return res
}
