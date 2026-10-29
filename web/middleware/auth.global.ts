// Route guard: first run → /setup; unauthenticated → /login; roles route
// admin and user sections. Initial setup status is resolved here
// (once, cached in useState) — independent of plugin initialization order.
export default defineNuxtRouteMiddleware(async (to) => {
  // An album opened by link is for anyone, signed in or not.
  if (to.path.startsWith('/a/')) return
  const sess = useSession()
  const authed = !!sess.value.token
  const setupState = useSetupNeeded()

  // for unauthenticated users, check once whether onboarding is needed
  if (!authed && setupState.value === null) {
    try {
      const { needed, webauthn } = await apiFetch<{ needed: boolean; webauthn: boolean }>('/setup/status')
      setupState.value = needed
      useWebauthnEnabled().value = webauthn
    } catch {
      setupState.value = false
    }
  }
  const setupNeeded = setupState.value === true

  // Forced password change takes precedence once authenticated: lock the user onto
  // /change-password until they set a new password (A.2).
  if (authed && sess.value.mustChangePassword) {
    return to.path === '/change-password' ? undefined : navigateTo('/change-password')
  }

  if (to.path === '/setup') {
    if (authed) return navigateTo(sess.value.role === 'admin' ? '/admin' : '/files')
    if (!setupNeeded) return navigateTo('/login')
    return
  }
  if (to.path === '/login') {
    if (authed) return navigateTo(sess.value.role === 'admin' ? '/admin' : '/files')
    if (setupNeeded) return navigateTo('/setup')
    return
  }
  if (!authed) {
    if (setupNeeded) return navigateTo('/setup')
    // preserve the original destination (e.g. /pair?code=…) to redirect back after login.
    // Object form — query string is encoded/parsed more reliably than a hand-built string.
    if (to.fullPath !== '/') return navigateTo({ path: '/login', query: { redirect: to.fullPath } })
    return navigateTo('/login')
  }

  const isAdminRoute = to.path.startsWith('/admin')
  if (isAdminRoute && sess.value.role !== 'admin') return navigateTo('/files')
  if (!isAdminRoute && sess.value.role === 'admin' && to.path === '/') return navigateTo('/admin')
  // A section the server has switched off has no pages either.
  const section = sectionOfRoute(to.path)
  if (section && !(await loadFeatures())[section]) return navigateTo('/files')
})
