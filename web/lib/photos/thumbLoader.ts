// Thumbnail loader of the photo grid. A thumbnail needs the Bearer token, so a plain
// <img src> cannot fetch it: the loader fetches blobs and hands out object URLs.
//
// The grid describes what it wants on every scroll step with sync(): the loader keeps
// at most `concurrency` requests in flight, lowest priority value first (the grid gives
// visible tiles 0 and farther ones growing numbers), drops queued entries the grid no
// longer wants and aborts their in-flight requests. Object URLs pin their blobs, so at
// most `capacity` are kept (LRU, revoked on eviction) — except the ones currently wanted,
// which are on screen and must not break. The thumbnail URLs carry ?v=<etag> and the
// server marks them immutable, so a re-fetch after eviction comes from the HTTP cache.

export interface ThumbWant {
  key: string
  url: string
  priority: number
}

// FetchBlob rejects with an error whose `permanent` is true when the thumbnail cannot be
// had at all (404 render_failed); anything else counts as transient.
export type FetchBlob = (url: string, signal: AbortSignal) => Promise<Blob>

export class ThumbError extends Error {
  constructor(public status: number) {
    super(`thumb ${status}`)
  }

  get permanent(): boolean {
    return this.status === 404 || this.status === 415
  }
}

function isPermanent(err: unknown): boolean {
  return !!(err as { permanent?: boolean } | null)?.permanent
}

export interface ThumbLoaderOptions {
  concurrency?: number
  capacity?: number
  makeURL?: (b: Blob) => string
  revokeURL?: (u: string) => void
  // Called when a key gets its URL, fails (null) or is evicted (undefined).
  onChange?: (key: string, url: string | null | undefined) => void
}

export class ThumbLoader {
  private readonly concurrency: number
  private readonly capacity: number
  private readonly makeURL: (b: Blob) => string
  private readonly revokeURL: (u: string) => void
  onChange: (key: string, url: string | null | undefined) => void

  private urls = new Map<string, string | null>() // LRU order: oldest first; null = failed
  private queue = new Map<string, ThumbWant>()
  private inflight = new Map<string, AbortController>()
  private wanted = new Set<string>()

  constructor(private fetchBlob: FetchBlob, opts: ThumbLoaderOptions = {}) {
    this.concurrency = opts.concurrency ?? 6
    this.capacity = opts.capacity ?? 600
    this.makeURL = opts.makeURL ?? ((b) => URL.createObjectURL(b))
    this.revokeURL = opts.revokeURL ?? ((u) => URL.revokeObjectURL(u))
    this.onChange = opts.onChange ?? (() => {})
  }

  // get: the object URL, null when the thumbnail failed, undefined when not loaded.
  get(key: string): string | null | undefined {
    return this.urls.get(key)
  }

  get active(): number {
    return this.inflight.size
  }

  get queued(): number {
    return this.queue.size
  }

  get size(): number {
    return this.urls.size
  }

  sync(wants: ThumbWant[]) {
    const next = new Set<string>()
    for (const w of wants) next.add(w.key)
    this.wanted = next
    for (const key of this.queue.keys()) {
      if (!next.has(key)) this.queue.delete(key)
    }
    for (const [key, ctrl] of this.inflight) {
      if (!next.has(key)) {
        ctrl.abort()
        this.inflight.delete(key)
      }
    }
    for (const w of wants) {
      if (this.urls.has(w.key)) {
        // refresh the LRU position
        const u = this.urls.get(w.key)!
        this.urls.delete(w.key)
        this.urls.set(w.key, u)
      } else if (!this.inflight.has(w.key)) {
        this.queue.set(w.key, w)
      }
    }
    this.pump()
  }

  private pump() {
    while (this.inflight.size < this.concurrency && this.queue.size > 0) {
      let best: ThumbWant | null = null
      for (const w of this.queue.values()) {
        if (!best || w.priority < best.priority) best = w
      }
      this.queue.delete(best!.key)
      this.start(best!)
    }
  }

  private start(w: ThumbWant) {
    const ctrl = new AbortController()
    this.inflight.set(w.key, ctrl)
    this.fetchBlob(w.url, ctrl.signal).then(
      (blob) => {
        if (this.inflight.get(w.key) !== ctrl) return // aborted or superseded
        this.inflight.delete(w.key)
        const url = this.makeURL(blob)
        this.urls.set(w.key, url)
        this.onChange(w.key, url)
        this.evict()
        this.pump()
      },
      (err) => {
        if (this.inflight.get(w.key) !== ctrl) return
        this.inflight.delete(w.key)
        if (isPermanent(err)) {
          // The server could not render it: show the broken-image tile, do not ask again.
          this.urls.set(w.key, null)
          this.onChange(w.key, null)
          this.evict()
        }
        // A transient failure (network, 401 before a token renewal, 5xx) is not cached:
        // the next sync that still wants the key asks again.
        this.pump()
      },
    )
  }

  private evict() {
    if (this.urls.size <= this.capacity) return
    for (const [key, url] of this.urls) {
      if (this.urls.size <= this.capacity) break
      if (this.wanted.has(key)) continue // on screen
      this.urls.delete(key)
      if (url) this.revokeURL(url)
      this.onChange(key, undefined)
    }
  }

  // forget drops one key (e.g. a failed thumbnail to retry).
  forget(key: string) {
    if (!this.urls.has(key)) return
    const url = this.urls.get(key)
    if (url) this.revokeURL(url)
    this.urls.delete(key)
    this.onChange(key, undefined)
  }

  clear() {
    for (const ctrl of this.inflight.values()) ctrl.abort()
    this.inflight.clear()
    this.queue.clear()
    this.wanted.clear()
    for (const url of this.urls.values()) if (url) this.revokeURL(url)
    this.urls.clear()
  }
}
