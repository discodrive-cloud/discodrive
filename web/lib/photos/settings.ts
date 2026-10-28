// Pure helpers of the «Фото» settings: nav visibility and the indexing progress.

export interface IndexingState {
  total: number // read so far + pending
  ready: number
  failed: number
  pending?: number // found by a running full pass, not read yet
  scanning?: boolean // a full pass is queued or running
  failedFiles?: number
}

/** The nav shows «Фото» only where the gallery works: on and in a build that has it. */
export function photosNavVisible(s: { enabled: boolean; available: boolean } | null | undefined): boolean {
  return !!s && s.enabled && s.available
}

/**
 * Whether the index is still filling, so the panel keeps polling: a pass is running or has
 * files left, thumbnails are still being made, or a change was just saved (graceTicks > 0:
 * the server may not have started the pass yet).
 */
export function indexingRunning(ix: IndexingState | null | undefined, graceTicks: number): boolean {
  if (!ix) return false
  if (ix.scanning || (ix.pending ?? 0) > 0) return true
  if (ix.ready + ix.failed < ix.total) return true
  return graceTicks > 0
}

/** Processed share of the files, 0..100 (floored: 100 only when all are done). */
export function indexingPercent(ix: IndexingState | null | undefined): number {
  if (!ix || !ix.total) return 0
  return Math.min(100, Math.floor(((ix.ready + ix.failed) / ix.total) * 100))
}
