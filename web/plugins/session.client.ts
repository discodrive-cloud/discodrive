// Hydrate the session from localStorage on SPA startup. Initial setup status
// (/setup/status) is resolved by the route guard (auth.global.ts) — so we don't
// depend on this plugin running before the initial navigation.
export default defineNuxtPlugin(() => {
  const sess = useSession()
  const raw = localStorage.getItem('kf_session')
  if (raw) {
    try {
      sess.value = JSON.parse(raw)
    } catch {
      localStorage.removeItem('kf_session')
    }
  }

  // Tabs share kf_session: a sign-in, account switch or logout in one tab must reach
  // the others, so no tab keeps acting as an account the browser no longer holds.
  window.addEventListener('storage', (e) => {
    if (e.storageArea !== localStorage) return
    if (e.key !== null && e.key !== 'kf_session') return // key null = storage cleared
    const action = followStoredSession(localStorage.getItem('kf_session'))
    if (action === 'cleared') void navigateTo('/login')
    else if (action === 'switched') window.location.reload()
  })
})
