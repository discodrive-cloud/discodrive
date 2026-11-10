// Player engine: one shared <video> element (audio plays fine on it, and a single
// element is the anchor for the future EQ and for Document PiP — it never leaves
// the main window). Queue/persistence/error-policy logic lives in ~/lib/player/core
// (pure, unit-tested); this composable wires it to the DOM and the API.
import { computed, ref, watch } from 'vue'
import {
  buildQueue, indexOfTrack, nextIndex, prevIndex, errorAction,
  serializePlayer, restorePlayer, resumePosition, isVideoMime,
  type MediaItem, type ProbeOutcome, type QueueTrack, type RepeatMode,
} from '~/lib/player/core'
import { CoverCache } from '~/lib/player/coverCache'

export type PlayerStatus = 'idle' | 'playing' | 'paused' | 'buffering'

const PERSIST_KEY = 'kf_player'
const PERSIST_INTERVAL_MS = 3000

// Module-level singletons: the SPA has exactly one player. The media element is
// deliberately NOT reactive state — it is DOM owned by PlayerLayer.vue.
let media: HTMLVideoElement | null = null
let coverCache: CoverCache | null = null
let persistTimer: ReturnType<typeof setTimeout> | null = null
let retriedCurrent = false // one URL-refresh retry per track (spec §5.4)
let restored = false

// Detached playback lives in a real popup window (/app/player): unlike Document
// PiP, it survives reloads and even closing the main tab. The two windows
// coordinate over a BroadcastChannel — exactly one of them owns playback.
type PlayerRole = 'main' | 'popup'
let role: PlayerRole = 'main'
let bc: BroadcastChannel | null = null

function useQueueState() {
  return {
    queue: useState<QueueTrack[]>('player.queue', () => []),
    index: useState<number>('player.index', () => -1),
    parentId: useState<string>('player.parent', () => ''),
    status: useState<PlayerStatus>('player.status', () => 'idle'),
    position: useState<number>('player.position', () => 0),
    duration: useState<number>('player.duration', () => 0),
    volume: useState<number>('player.volume', () => 1),
    muted: useState<boolean>('player.muted', () => false),
    repeat: useState<RepeatMode>('player.repeat', () => 'off'),
    shuffle: useState<boolean>('player.shuffle', () => false),
    theater: useState<boolean>('player.theater', () => false),
    coverUrl: useState<string | null>('player.cover', () => null),
    // Transient in-bar notice ("skipped: <name>"), cleared automatically.
    notice: useState<string>('player.notice', () => ''),
  }
}

// Silences playback in this window and empties the visible state. The persisted
// snapshot is left alone — the popup handoff relies on that.
function resetWindowState(st: ReturnType<typeof useQueueState>) {
  if (media) {
    media.pause()
    media.removeAttribute('src')
    media.load()
  }
  st.status.value = 'idle'
  st.theater.value = false
  st.queue.value = []
  st.index.value = -1
  st.coverUrl.value = null
}

// resetPlayerSession wipes the player on logout / account switch: this window's
// playback and state, the persisted snapshot, and (via broadcast) the popup and
// any other tabs. The snapshot carries the previous account's file names and
// tags — the next account must start from a blank player.
export function resetPlayerSession() {
  if (!import.meta.client) return
  if (persistTimer) {
    clearTimeout(persistTimer)
    persistTimer = null
  }
  restored = false
  localStorage.removeItem(PERSIST_KEY)
  resetWindowState(useQueueState())
  bc?.postMessage('session-cleared')
}

// Server-side mime detection (Go mime.TypeByExtension) emits a few legacy names
// that canPlayType does not recognise — normalise before probing.
const mimeAliases: Record<string, string> = {
  'audio/mp4a-latm': 'audio/mp4', // .m4a
  'audio/x-m4a': 'audio/mp4',
  'audio/x-flac': 'audio/flac',
  'audio/x-wav': 'audio/wav',
}

// canPlayType probe on a detached element; '' means "definitely not".
let probeEl: HTMLVideoElement | null = null
export function browserCanPlay(mime: string): boolean {
  if (!mime || !(mime.startsWith('audio/') || mime.startsWith('video/'))) return false
  if (!import.meta.client) return false
  if (!probeEl) probeEl = document.createElement('video')
  return probeEl.canPlayType(mimeAliases[mime] ?? mime) !== ''
}

export function usePlayer() {
  const st = useQueueState()
  const sess = useSession()
  const { t } = useI18n()

  const current = computed<QueueTrack | null>(() => st.queue.value[st.index.value] ?? null)
  const currentIsVideo = computed(() => !!current.value && isVideoMime(current.value.mime))
  const active = computed(() => st.status.value !== 'idle' && !!current.value)

  function authHeaders(): Record<string, string> {
    return sess.value.token ? { Authorization: `Bearer ${sess.value.token}` } : {}
  }

  function mediaEndpoint(parentId: string): string {
    return parentId ? `/files/${parentId}/media` : '/files/media'
  }

  if (!coverCache) {
    coverCache = new CoverCache(async (nodeId) => {
      try {
        return await apiFetch<Blob>(`/files/${nodeId}/media-cover`, {
          responseType: 'blob', headers: authHeaders(),
        })
      } catch {
        return null
      }
    })
  }

  // ---- persistence ----
  function writePersist() {
    // No session → no owner to stamp the snapshot with; don't write at all.
    if (!st.queue.value.length || !sess.value.email) return
    localStorage.setItem(PERSIST_KEY, serializePlayer({
      owner: sess.value.email,
      parentId: st.parentId.value,
      items: st.queue.value.map(({ playable, ...it }) => it),
      index: st.index.value,
      position: st.position.value,
      volume: st.volume.value,
      muted: st.muted.value,
      repeat: st.repeat.value,
      shuffle: st.shuffle.value,
    }))
  }

  function persistSoon() {
    if (!import.meta.client || persistTimer) return
    persistTimer = setTimeout(() => {
      persistTimer = null
      writePersist()
    }, PERSIST_INTERVAL_MS)
  }

  // persistNow flushes immediately — used for the popup handoff and on pagehide,
  // where the debounce would lose the last seconds of position.
  function persistNow() {
    if (!import.meta.client) return
    if (persistTimer) {
      clearTimeout(persistTimer)
      persistTimer = null
    }
    writePersist()
  }

  function restoreFromStorage(force = false) {
    if (!import.meta.client || (restored && !force)) return
    restored = true
    const raw = localStorage.getItem(PERSIST_KEY)
    const saved = restorePlayer(raw, sess.value.email)
    if (!saved) {
      // Another account's snapshot (or malformed/pre-v2): discard it for good.
      if (raw) localStorage.removeItem(PERSIST_KEY)
      return
    }
    st.queue.value = buildQueue(saved.items, browserCanPlay)
    // buildQueue re-sorts; find the saved track again by id.
    const savedId = saved.items[saved.index]?.node_id
    st.index.value = savedId ? Math.max(0, indexOfTrack(st.queue.value, savedId)) : 0
    st.parentId.value = saved.parentId
    st.position.value = saved.position
    st.volume.value = saved.volume
    st.muted.value = saved.muted
    st.repeat.value = saved.repeat
    st.shuffle.value = saved.shuffle
    st.status.value = 'paused' // browsers block autoplay after reload anyway
    void updateSessionMetadata()
  }

  // ---- media element wiring (called once per window: PlayerLayer or /player) ----
  function attachMedia(el: HTMLVideoElement, asRole: PlayerRole = 'main') {
    role = asRole
    media = el
    el.volume = st.volume.value
    el.muted = st.muted.value

    el.addEventListener('timeupdate', () => {
      st.position.value = el.currentTime
      persistSoon()
    })
    el.addEventListener('durationchange', () => { st.duration.value = el.duration || 0 })
    el.addEventListener('playing', () => {
      st.status.value = 'playing'
      retriedCurrent = false
    })
    el.addEventListener('pause', () => {
      if (st.status.value === 'playing') st.status.value = 'paused'
    })
    el.addEventListener('waiting', () => {
      if (st.status.value === 'playing') st.status.value = 'buffering'
    })
    el.addEventListener('stalled', () => {
      if (st.status.value === 'playing') st.status.value = 'buffering'
    })
    el.addEventListener('ended', () => { advance(false) })
    el.addEventListener('error', () => { void handleMediaError() })

    // Cross-window coordination: exactly one window owns playback.
    if (import.meta.client && 'BroadcastChannel' in window && !bc) {
      bc = new BroadcastChannel('dd-player')
      bc.onmessage = (ev) => {
        const msg = ev.data
        // Logout in any window kills playback everywhere. The popup closes itself;
        // its queue is already empty, so the pagehide flush writes nothing back.
        if (msg === 'session-cleared') {
          restored = false
          stopLocal()
          if (role === 'popup') window.close()
          return
        }
        if (role === 'main') {
          // A popup announced itself (on open, or replying to our hello after a
          // main-window reload) → the main window yields.
          if (msg === 'popup-open') stopLocal()
          // Popup closed → pick the state back up, paused, unless we are already
          // playing something new (the user took over in the main window).
          else if (msg === 'popup-closed' && st.status.value === 'idle') restoreFromStorage(true)
        } else {
          // Starting playback in the main window supersedes the popup.
          if (msg === 'main-took-over') window.close()
          else if (msg === 'main-hello') bc?.postMessage('popup-open')
        }
      }
    }
    if (role === 'popup' && import.meta.client) {
      window.addEventListener('pagehide', () => {
        persistNow()
        bc?.postMessage('popup-closed')
      })
    }

    setupMediaSession()
    restoreFromStorage()
    if (import.meta.client) {
      if (role === 'popup') bc?.postMessage('popup-open')
      else bc?.postMessage('main-hello') // discover an already-running popup after reload
    }
  }

  function showNotice(msg: string) {
    st.notice.value = msg
    setTimeout(() => { if (st.notice.value === msg) st.notice.value = '' }, 4000)
  }

  // probeNode asks the single-node media endpoint about a track with the CURRENT
  // session — used both to recover from mid-playback errors and to mint stream
  // URLs for restored queues. Deliberately bypasses useApi: its 401 handler
  // force-logouts, which is wrong for a background stream hiccup.
  async function probeNode(nodeId: string): Promise<ProbeOutcome> {
    try {
      const item = await apiFetch<MediaItem>(mediaEndpoint(st.parentId.value), {
        query: { node_id: nodeId }, headers: authHeaders(),
      })
      return { ok: true, item }
    } catch (e: any) {
      return { ok: false, status: e?.response?.status ?? 0 }
    }
  }

  // ---- core playback ----
  async function loadAndPlay(fromPosition = 0) {
    let track = current.value
    if (!media || !track) return
    if (!track.playable) {
      showNotice(t('player.skipped_unplayable', { name: track.name }))
      advance(false)
      return
    }
    // Restored queues carry no stream URLs (bearer tokens are never persisted) —
    // mint one under the current session, so the server re-checks access as the
    // user who is actually logged in now.
    if (!track.stream_url) {
      const probe = await probeNode(track.node_id)
      // "Next" pressed again while the probe ran: the queue has moved, and the call
      // for the track it moved to does its own work. Writing this result at the new
      // index would replace that track with this one.
      if (current.value?.node_id !== track.node_id) return
      if (!probe.ok) {
        showNotice(t('player.skipped_missing', { name: track.name }))
        advance(false)
        return
      }
      fromPosition = resumePosition(fromPosition, track.version, probe.item.version)
      track = { ...track, ...probe.item, playable: track.playable }
      st.queue.value.splice(st.index.value, 1, track)
    }
    st.duration.value = track.duration ?? 0
    st.position.value = fromPosition
    // A video track opens the theater by itself — sound without picture is a bug,
    // not a feature. The bar button (or Escape) collapses it back to audio-only.
    if (isVideoMime(track.mime)) st.theater.value = true
    media.src = track.stream_url
    if (fromPosition > 0) media.currentTime = fromPosition
    try {
      await media.play()
    } catch {
      // Autoplay refused (e.g. right after restore) — stay paused at position.
      st.status.value = 'paused'
    }
    void updateSessionMetadata()
    persistSoon()
  }

  function advance(manual: boolean) {
    const ni = nextIndex(st.queue.value, st.index.value, st.repeat.value, st.shuffle.value, { manual })
    if (ni === null) {
      st.status.value = 'paused'
      if (media) media.pause()
      return
    }
    st.index.value = ni
    void loadAndPlay(0)
  }

  // Media element errors carry no HTTP status → probe the single-node endpoint
  // with the session. Alive → fresh stream_url (+version check for resume), retry
  // once. Gone/forbidden → skip.
  async function handleMediaError() {
    const track = current.value
    if (!media || !track || !media.error) return
    const savedPos = st.position.value
    const probe = await probeNode(track.node_id)
    if (current.value?.node_id !== track.node_id) return // moved on meanwhile, as above
    const action = errorAction(probe, retriedCurrent)
    if (action.kind === 'resume') {
      retriedCurrent = true
      const fresh = action.item
      st.queue.value.splice(st.index.value, 1, { ...track, ...fresh, playable: track.playable })
      await loadAndPlay(resumePosition(savedPos, track.version, fresh.version))
      return
    }
    showNotice(t('player.skipped_missing', { name: track.name }))
    advance(false)
  }

  // ---- public controls ----
  async function playFolder(parentId: string, clickedNodeId: string) {
    bc?.postMessage('main-took-over') // an open popup closes: latest intent wins
    const { request } = useApi()
    const resp = await request<{ items: MediaItem[] }>(mediaEndpoint(parentId))
    const queue = buildQueue(resp.items, browserCanPlay)
    if (!queue.length) return
    st.parentId.value = parentId
    st.queue.value = queue
    st.index.value = Math.max(0, indexOfTrack(queue, clickedNodeId))
    retriedCurrent = false
    await loadAndPlay(0)
  }

  async function playAt(i: number) {
    if (i < 0 || i >= st.queue.value.length) return
    st.index.value = i
    retriedCurrent = false
    await loadAndPlay(0)
  }

  async function toggle() {
    if (!media || !current.value) return
    if (st.status.value === 'playing' || st.status.value === 'buffering') {
      media.pause()
      return
    }
    // After a reload the element has no src yet — (re)load at the saved position.
    if (!media.src) {
      await loadAndPlay(st.position.value)
      return
    }
    try { await media.play() } catch { /* autoplay policy */ }
  }

  function next() { advance(true) }
  function prev() {
    // Convention: >3s into the track "prev" restarts it, otherwise goes back.
    if (media && media.currentTime > 3) {
      media.currentTime = 0
      return
    }
    const pi = prevIndex(st.queue.value, st.index.value)
    if (pi !== null) void playAt(pi)
    else if (media) media.currentTime = 0
  }

  function seek(sec: number) {
    if (!media) return
    media.currentTime = Math.max(0, Math.min(sec, st.duration.value || sec))
    st.position.value = media.currentTime
  }

  function setVolume(v: number) {
    st.volume.value = Math.min(1, Math.max(0, v))
    if (media) media.volume = st.volume.value
    if (st.volume.value > 0 && st.muted.value) toggleMute()
    persistSoon()
  }

  function toggleMute() {
    st.muted.value = !st.muted.value
    if (media) media.muted = st.muted.value
    persistSoon()
  }

  function cycleRepeat() {
    st.repeat.value = st.repeat.value === 'off' ? 'all' : st.repeat.value === 'all' ? 'one' : 'off'
    persistSoon()
  }

  function toggleShuffle() {
    st.shuffle.value = !st.shuffle.value
    persistSoon()
  }

  // stopLocal silences THIS window without forgetting the saved state — used when
  // the popup takes over. close() is the user's explicit X: forget everything.
  function stopLocal() {
    resetWindowState(st)
  }

  function close() {
    stopLocal()
    if (import.meta.client) localStorage.removeItem(PERSIST_KEY)
  }

  // detachToPopup hands playback to a standalone window: unlike Document PiP it
  // keeps playing when this tab reloads or closes. Handoff = flush state to
  // localStorage, open /player; the popup announces itself and this window yields.
  function detachToPopup() {
    if (!import.meta.client) return
    persistNow()
    const win = window.open('/app/player', 'dd-player', 'popup=yes,width=440,height=240')
    if (!win) showNotice(t('player.popup_blocked'))
  }

  // ---- Media Session (OS media keys, lock screen) ----
  function setupMediaSession() {
    if (!import.meta.client || !('mediaSession' in navigator)) return
    const ms = navigator.mediaSession
    ms.setActionHandler('play', () => { void toggle() })
    ms.setActionHandler('pause', () => { void toggle() })
    ms.setActionHandler('previoustrack', () => prev())
    ms.setActionHandler('nexttrack', () => next())
    try {
      ms.setActionHandler('seekto', (d) => { if (d.seekTime != null) seek(d.seekTime) })
    } catch { /* seekto unsupported */ }
  }

  async function updateSessionMetadata() {
    const track = current.value
    st.coverUrl.value = track ? await coverCache!.get(track.node_id) : null
    if (!import.meta.client || !('mediaSession' in navigator) || !track) return
    navigator.mediaSession.metadata = new MediaMetadata({
      title: track.title || track.name,
      artist: track.artist || '',
      album: track.album || '',
      artwork: st.coverUrl.value ? [{ src: st.coverUrl.value }] : [],
    })
  }

  return {
    // state
    queue: st.queue, index: st.index, status: st.status, position: st.position,
    duration: st.duration, volume: st.volume, muted: st.muted, repeat: st.repeat,
    shuffle: st.shuffle, theater: st.theater, coverUrl: st.coverUrl, notice: st.notice,
    current, currentIsVideo, active,
    // wiring + controls
    attachMedia, playFolder, playAt, toggle, next, prev, seek,
    setVolume, toggleMute, cycleRepeat, toggleShuffle, close,
    detachToPopup, persistNow,
  }
}
