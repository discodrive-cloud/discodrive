// isHttpUrl — whether a stored URL (bookmark, Pocket item) may be rendered as a
// clickable link. javascript:, data:, about: and the like are synced as data but never
// opened: CSP is the only other thing between such a link and script execution.
export function isHttpUrl(url: string | null | undefined): boolean {
  return /^https?:\/\//i.test((url || '').trim())
}

// withScheme — what a typed-in bookmark address means: "example.com/page" is
// https://example.com/page. An explicit scheme is left alone (the server accepts only
// http(s) and answers anything else with a clear error).
export function withScheme(url: string): string {
  const u = url.trim()
  if (!u || /^[a-z][a-z0-9+.-]*:/i.test(u)) return u
  return 'https://' + u.replace(/^\/+/, '')
}
