// isHttpUrl — whether a stored URL (bookmark, Pocket item) may be rendered as a
// clickable link. javascript:, data:, about: and the like are synced as data but never
// opened: CSP is the only other thing between such a link and script execution.
export function isHttpUrl(url: string | null | undefined): boolean {
  return /^https?:\/\//i.test((url || '').trim())
}
