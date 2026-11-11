// changedElsewhere: the server refused a save (412) because the thing was changed after
// the form was loaded — by another tab, another device or another person it is shared
// with. The form's content is stale; the caller reloads and says so.
export function changedElsewhere(e: unknown): boolean {
  const err = e as { status?: number; statusCode?: number; response?: { status?: number } } | null
  return (err?.status ?? err?.statusCode ?? err?.response?.status) === 412
}
