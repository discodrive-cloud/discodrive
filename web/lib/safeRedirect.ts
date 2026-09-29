// safeRedirect — the ?redirect= target after sign-in, or null when it is not a path
// inside this app. Only same-origin paths starting with a single "/" are accepted:
// "//host", "/\host" (browsers read "\" as "/"), absolute URLs and anything with
// control characters would leave the app or make navigateTo throw after the session
// is already set.
export function safeRedirect(value: unknown): string | null {
  const v = Array.isArray(value) ? value[0] : value
  if (typeof v !== 'string') return null
  if (!v.startsWith('/') || v.startsWith('//')) return null
  if (/[\\\u0000-\u001f\u007f]/.test(v)) return null
  return v
}
