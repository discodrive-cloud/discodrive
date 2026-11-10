// «Дубли»: what the screen of one group works out — what goes along with the copies being
// deleted, the size they free, and «Пропустить» (the group to the end of the queue, for
// this visit only).

export interface DupCopy {
  id: string
  live: boolean // a Live photo: its video goes with the card
  edited: boolean // an edited card: the original before the edit goes with it
  bytes: number
}

export type DupWarning = 'live' | 'edited'

// deleteWarnings: what is lost together with every copy but keepId (each once, live first).
export function deleteWarnings(group: Pick<DupCopy, 'id' | 'live' | 'edited'>[], keepId: string): DupWarning[] {
  const gone = group.filter((c) => c.id !== keepId)
  const out: DupWarning[] = []
  if (gone.some((c) => c.live)) out.push('live')
  if (gone.some((c) => c.edited)) out.push('edited')
  return out
}

// deletedBytes: what deleting every copy but keepId frees once the trash is emptied.
export function deletedBytes(group: Pick<DupCopy, 'id' | 'bytes'>[], keepId: string): number {
  return group.reduce((n, c) => (c.id === keepId ? n : n + (c.bytes || 0)), 0)
}

// groupReclaim: what the group adds to the reclaimable total the server reports — every
// copy but the suggested one (the first).
export function groupReclaim(group: Pick<DupCopy, 'bytes'>[]): number {
  return group.slice(1).reduce((n, c) => n + (c.bytes || 0), 0)
}

// skipGroup: a new queue with item i moved to the end, the rest in their order.
export function skipGroup<T>(queue: T[], i: number): T[] {
  if (i < 0 || i >= queue.length) return queue.slice()
  return [...queue.slice(0, i), ...queue.slice(i + 1), queue[i]!]
}

// groupKey names a group by its cards, whatever their order.
export function groupKey(g: { assets: { id: string }[] }): string {
  return g.assets.map((c) => c.id).sort().join(',')
}

// mergeQueue folds a re-fetch (the scan finds more groups as it goes) into the queue the
// user is working through: the groups still there keep their place (a skipped one stays at
// the end) in their fresh version, gone ones drop out, new ones come last.
//
// pinHead is for a fetch the user did not ask for (the poll while the server scans): the
// group on screen then stays exactly as shown even if the list no longer has it in that
// shape — otherwise another group slides under the cursor on its way to «Удалить
// остальные». A changed version of it is left out too and comes with the next fetch.
export function mergeQueue<G extends { assets: { id: string }[] }>(queue: G[], fresh: G[], pinHead = false): G[] {
  const byKey = new Map(fresh.map((g) => [groupKey(g), g]))
  const out: G[] = []
  const head = queue[0]
  if (pinHead && head && !byKey.has(groupKey(head))) {
    out.push(head)
    const shown = new Set(head.assets.map((c) => c.id))
    for (const [k, g] of byKey) if (g.assets.some((c) => shown.has(c.id))) byKey.delete(k)
    queue = queue.slice(1)
  }
  for (const g of queue) {
    const k = groupKey(g)
    const f = byKey.get(k)
    if (f) { out.push(f); byKey.delete(k) }
  }
  return [...out, ...byKey.values()]
}

// DUP_SETTLE_MS: how long after the last «Удалить остальные» / «Оставить все» the list is
// fetched again. Each fetch makes the server go through the whole library, so working
// through the queue does not fetch after every group — only once the user pauses.
export const DUP_SETTLE_MS = 4_000

// DUP_POLL_MS: how often the list is fetched again while the server is still hashing the
// library, and how long after a failed first load it is tried once more.
export const DUP_POLL_MS = 10_000

// nextLoad: when to fetch the list again after a fetch, or null for not at all. The list
// is fetched again only while the server last said it is still scanning; a failed first
// fetch (nothing shown yet) is retried once.
export function nextLoad(s: { ok: boolean; scanning: boolean; loaded: boolean; retried: boolean }): number | null {
  if (s.ok || s.loaded) return s.scanning ? DUP_POLL_MS : null
  return s.retried ? null : DUP_POLL_MS
}
