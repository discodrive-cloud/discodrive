// Helpers for the shared-links list (Saved → Links).

const SOURCE_LABELS: Record<string, string> = {
  instagram: 'Instagram',
  threads: 'Threads',
  reddit: 'Reddit',
  youtube: 'YouTube',
  x: 'X',
  tiktok: 'TikTok',
}

// sourceLabel — the brand name of a known source; '' for "other" (the list then shows the host).
export function sourceLabel(source: string): string {
  return SOURCE_LABELS[source] ?? ''
}

function startOfDay(d: Date): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate()).getTime()
}

// relativeDate — "today", "yesterday", "3 days ago" within a month, then a short date
// (with the year when it is not this year). Calendar days, in the viewer's time zone.
export function relativeDate(iso: string, now: Date, locale: string): string {
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ''
  const days = Math.round((startOfDay(now) - startOfDay(d)) / 86_400_000)
  if (days >= 0 && days < 30) {
    return new Intl.RelativeTimeFormat(locale, { numeric: 'auto' }).format(-days, 'day')
  }
  const opts: Intl.DateTimeFormatOptions = { day: 'numeric', month: 'short' }
  if (d.getFullYear() !== now.getFullYear()) opts.year = 'numeric'
  return d.toLocaleDateString(locale, opts)
}
