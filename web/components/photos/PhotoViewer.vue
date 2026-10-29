<script setup lang="ts">
// Full-screen viewer (the approved mockup): date/time, place and camera on top; ♡,
// download, hide, delete and ⓘ; arrows, swipe, Esc and I; a strip of neighbours below;
// the info panel on the right. It walks the same timeline as the grid it was opened from
// (months + a fetcher) and loads the next month when it steps past the end of one.
//
// The preview needs the Bearer token, so it comes through a loader of its own (the grid's
// loader belongs to the grid). Until it arrives the square thumbnail is shown stretched
// over the photo's box. Video plays through POST play (an h264 copy may need preparing);
// a Live photo plays its video part, muted, while hovered or long-pressed.
import type { PhotoAsset, PhotoRow, PlayInfo } from '~/composables/usePhotos'
import { previewURL, thumbURL, thumbKey, takenLabel, formatDuration, cameraName } from '~/composables/usePhotos'
import { step, around, type Pos, type Seq } from '~/lib/photos/viewerSeq'
import { photosHiddenHeaders } from '~/lib/photos/hidden'
import InfoPanel from '~/components/photos/InfoPanel.vue'

const props = withDefaults(defineProps<{
  start: Pos
  months: string[]
  fetchMonth: (month: string, signal: AbortSignal) => Promise<PhotoRow[]>
  initialRows?: Map<string, PhotoRow[]>
  // the Hidden tab: "hide" becomes "show again"
  hiddenMode?: boolean
}>(), { hiddenMode: false })

const emit = defineEmits<{
  close: []
  favorite: [id: string, value: boolean]
  hide: [id: string, hidden: boolean]
  delete: [id: string]
  download: [id: string]
  'edit-date': [id: string]
  'edit-place': [id: string]
}>()

const { t, locale } = useI18n()
const api = usePhotosApi()

// ---- the walk ----
const rows = shallowReactive(new Map<string, PhotoRow[]>(props.initialRows ?? []))
const seq: Seq<PhotoRow> = { months: props.months, rows }
const pos = ref<Pos>({ ...props.start })
const loadingMonth = ref('')
const ctrl = new AbortController()

const current = computed<PhotoRow | undefined>(() => (rows.get(pos.value.month) ?? []).find((r) => r.id === pos.value.id))

async function ensureMonth(month: string): Promise<boolean> {
  if (rows.has(month)) return true
  loadingMonth.value = month
  try {
    rows.set(month, await props.fetchMonth(month, ctrl.signal))
    return true
  } catch {
    return false
  } finally {
    if (loadingMonth.value === month) loadingMonth.value = ''
  }
}

// resolve: the neighbour in a direction, loading months on the way.
async function resolve(p: Pos, dir: 1 | -1): Promise<Pos | null> {
  for (let guard = 0; guard < 400; guard++) {
    const s = step(seq, p, dir)
    if (!s) return null
    if ('to' in s) return s.to
    if (!(await ensureMonth(s.load))) return null
  }
  return null
}

async function go(dir: 1 | -1) {
  const from = pos.value
  const to = await resolve(from, dir)
  if (to && pos.value === from) pos.value = to
}

function goTo(p: Pos) { pos.value = p }

// The months on both sides of the current one come in ahead, so the strip and the
// next step do not wait.
watch(pos, async (p) => {
  const mi = props.months.indexOf(p.month)
  const list = rows.get(p.month) ?? []
  const i = list.findIndex((r) => r.id === p.id)
  if (i >= 0 && i >= list.length - 3 && mi + 1 < props.months.length) ensureMonth(props.months[mi + 1])
  if (i >= 0 && i <= 2 && mi > 0) ensureMonth(props.months[mi - 1])
}, { immediate: true })

onMounted(async () => {
  if (!rows.has(props.start.month)) await ensureMonth(props.start.month)
})

const strip = computed(() => around(seq, pos.value, 7))

// ---- images ----
const previews = makeThumbLoader(photosHiddenHeaders, { concurrency: 3, capacity: 8 })
const thumbs = makeThumbLoader(photosHiddenHeaders, { concurrency: 4, capacity: 80 })
const tick = ref(0)
let raf = 0
function bump() {
  if (raf) return
  raf = requestAnimationFrame(() => { raf = 0; tick.value++ })
}
previews.onChange = bump
thumbs.onChange = bump

const previewKey = (r: PhotoRow) => `p:${r.id}:${r.thumb}`
const origKey = ref('') // set while «show original» is on
const original = ref<{ id: string; thumb: string } | null>(null)

watch([strip, pos, original], () => {
  const list = strip.value
  const at = list.findIndex((x) => x.row.id === pos.value.id)
  const want = []
  for (const [k, x] of list.entries()) {
    const d = Math.abs(k - at)
    if (d <= 1 && x.row.kind === 'photo' || d === 0) want.push({ key: previewKey(x.row), url: previewURL(x.row), priority: d * 2 })
  }
  if (original.value) want.push({ key: origKey.value, url: previewURL(original.value), priority: 0 })
  previews.sync(want)
  thumbs.sync(list.map((x, k) => ({ key: thumbKey(x.row), url: thumbURL(x.row), priority: Math.abs(k - at) })))
}, { immediate: true })

function srcPreview(r: PhotoRow) { void tick.value; return previews.get(previewKey(r)) }
function srcThumb(r: PhotoRow) { void tick.value; return thumbs.get(thumbKey(r)) }
const shownSrc = computed(() => {
  void tick.value
  if (original.value) return previews.get(origKey.value)
  return current.value ? srcPreview(current.value) : undefined
})

// ---- the card ----
const asset = ref<PhotoAsset | null>(null)
const assetFailed = ref(false)
let assetSeq = 0
async function loadAsset(id: string) {
  const n = ++assetSeq
  assetFailed.value = false
  try {
    const a = await api.asset(id)
    if (n === assetSeq) asset.value = a
  } catch {
    if (n === assetSeq) assetFailed.value = true
  }
}

// refresh: the page edited the card (date, place); re-read it and its row.
async function refresh(id = pos.value.id) {
  if (id === pos.value.id) await loadAsset(id)
}
// The page changed a row in place (e.g. ♡).
function patchRow(id: string, patch: Partial<PhotoRow>) {
  for (const [m, list] of rows) {
    if (list.some((r) => r.id === id)) rows.set(m, list.map((r) => (r.id === id ? { ...r, ...patch } : r)))
  }
  if (asset.value?.id === id) asset.value = { ...asset.value, ...patch } as PhotoAsset
}
// drop: the card left this view (hidden, deleted): show the next one, or close.
async function drop(id: string) {
  const p = pos.value
  if (id === p.id) {
    const next = (await resolve(p, 1)) ?? (await resolve(p, -1))
    rows.set(p.month, (rows.get(p.month) ?? []).filter((r) => r.id !== id))
    if (next) pos.value = next
    else emit('close')
    return
  }
  for (const [m, list] of rows) if (list.some((r) => r.id === id)) rows.set(m, list.filter((r) => r.id !== id))
}
defineExpose({ refresh, patchRow, drop, currentId: () => pos.value.id })

// ---- top bar ----
const title = computed(() => (current.value ? takenLabel(current.value, locale.value) : ''))
const subtitle = computed(() => {
  const a = asset.value
  const place = a?.place ?? current.value?.place ?? ''
  const cam = cameraName(a?.camera)
  return [place, cam].filter(Boolean).join(' · ')
})

function toggleOriginal() {
  const a = asset.value
  if (!a?.originalId) return
  // A video's original plays instead of the edit; a photo's shows as a still.
  const isVideo = current.value?.kind === 'video'
  if (original.value) {
    original.value = null
    if (isVideo) startVideo(a.id)
    return
  }
  api.asset(a.originalId).then((o) => {
    if (asset.value?.id !== a.id) return
    origKey.value = `p:${o.id}:${o.thumb}`
    original.value = { id: o.id, thumb: o.thumb }
    if (isVideo) startVideo(o.id)
  }).catch(() => {})
}

// ---- video ----
const video = ref<{ state: 'idle' | 'loading' | 'preparing' | 'ready' | 'unsupported' | 'error'; url: string }>({ state: 'idle', url: '' })
let videoTimer: ReturnType<typeof setTimeout> | null = null
let videoFor = ''
const hevc = import.meta.client && typeof document !== 'undefined'
  ? document.createElement('video').canPlayType('video/mp4; codecs="hvc1"') !== ''
  : false

function resetVideo() {
  if (videoTimer) clearTimeout(videoTimer)
  videoTimer = null
  videoFor = ''
  video.value = { state: 'idle', url: '' }
}

async function startVideo(id: string) {
  resetVideo()
  videoFor = id
  video.value = { state: 'loading', url: '' }
  const poll = async () => {
    let r: PlayInfo
    try { r = await api.play(id, hevc) } catch {
      if (videoFor === id) video.value = { state: 'error', url: '' }
      return
    }
    if (videoFor !== id) return
    if (r.ready && r.url) video.value = { state: 'ready', url: r.url }
    else if (r.unsupported) video.value = { state: 'unsupported', url: '' }
    else {
      video.value = { state: 'preparing', url: '' }
      videoTimer = setTimeout(poll, 3000)
    }
  }
  await poll()
}

// ---- Live ----
const liveURL = ref('')
const livePlaying = ref(false)
const liveCache = new Map<string, string>()
let liveWant = ''
async function startLive() {
  const a = asset.value
  if (!a?.live || !a.liveVideoId || original.value) return
  liveWant = a.liveVideoId
  let url = liveCache.get(a.liveVideoId)
  if (!url) {
    try {
      const r = await api.play(a.liveVideoId, hevc)
      if (!r.ready || !r.url) return
      url = r.url
      liveCache.set(a.liveVideoId, url)
    } catch { return }
  }
  if (liveWant !== a.liveVideoId) return
  liveURL.value = url
  livePlaying.value = true
}
function stopLive() {
  liveWant = ''
  livePlaying.value = false
}
// The pointer may rest on the photo before its card has loaded, or already be there when
// the viewer opens (no mouseenter fires then): check :hover once the card is in, and let
// any move over the photo start it too.
let hovering = false
const photoBox = ref<HTMLElement | null>(null)
function onEnter() { hovering = true; startLive() }
function onLeave() { hovering = false; stopLive() }
function onMove() { if (!hovering) onEnter() }
function pointerIsOver(): boolean {
  try { return !!photoBox.value?.matches(':hover') } catch { return false }
}
watch(asset, async (a) => {
  if (!a?.live) return
  await nextTick()
  if (hovering || pointerIsOver()) { hovering = true; startLive() }
})
let livePress: ReturnType<typeof setTimeout> | null = null

// Keyed on the row being there: on a cold start the month arrives after the position.
watch(() => current.value?.id, (id) => {
  asset.value = null
  original.value = null
  stopLive()
  resetVideo()
  if (!id) return
  loadAsset(id)
  if (current.value?.kind === 'video') startVideo(id)
}, { immediate: true })

const mapsReady = ref(false)
onMounted(() => { photosMapsReady().then((v) => { mapsReady.value = v }) })

// ---- gestures & keys ----
let swipe: { x: number; y: number; id: number } | null = null
function onPointerDown(e: PointerEvent) {
  if (e.pointerType === 'mouse') return
  swipe = { x: e.clientX, y: e.clientY, id: e.pointerId }
  if (asset.value?.live) livePress = setTimeout(() => { livePress = null; startLive() }, 350)
}
function onPointerMove(e: PointerEvent) {
  if (swipe && livePress && Math.hypot(e.clientX - swipe.x, e.clientY - swipe.y) > 10) { clearTimeout(livePress); livePress = null }
}
function onPointerUp(e: PointerEvent) {
  if (livePress) { clearTimeout(livePress); livePress = null }
  if (e.pointerType !== 'mouse') stopLive()
  if (!swipe || swipe.id !== e.pointerId) return
  const dx = e.clientX - swipe.x
  const dy = e.clientY - swipe.y
  swipe = null
  if (Math.abs(dx) > 50 && Math.abs(dx) > Math.abs(dy) * 1.5) go(dx < 0 ? 1 : -1)
  else if (dy > 90 && Math.abs(dy) > Math.abs(dx) * 1.5) emit('close')
}

const showInfo = ref(false)
try { showInfo.value = localStorage.getItem('photos.viewer.info') === '1' } catch { /* private mode */ }
watch(showInfo, (v) => { try { localStorage.setItem('photos.viewer.info', v ? '1' : '0') } catch { /* private mode */ } })

function otherDialogOpen() {
  return !!document.querySelector('[role="dialog"]:not(.photo-viewer)')
}
function onKey(e: KeyboardEvent) {
  if (otherDialogOpen()) return
  const tag = (e.target as HTMLElement | null)?.tagName
  if (tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT') return
  if (e.key === 'Escape') { e.preventDefault(); emit('close') }
  else if (e.key === 'ArrowRight') { e.preventDefault(); go(1) }
  else if (e.key === 'ArrowLeft') { e.preventDefault(); go(-1) }
  else if (e.key === 'i' || e.key === 'I' || e.key === 'ш' || e.key === 'Ш') { if (!e.metaKey && !e.ctrlKey) showInfo.value = !showInfo.value }
}

// ---- the photo's box ----
const stage = ref<HTMLElement | null>(null)
const stageSize = ref({ w: 0, h: 0 })
let ro: ResizeObserver | null = null
const box = computed(() => {
  const r = current.value
  const { w: sw, h: sh } = stageSize.value
  if (!r || !sw || !sh) return { w: 0, h: 0 }
  const ratio = r.w && r.h ? r.w / r.h : 1
  let w = sw
  let h = w / ratio
  if (h > sh) { h = sh; w = h * ratio }
  // a small picture is not blown up past its own pixels (the preview never is either)
  if (r.w && r.h && r.w < w) { w = r.w; h = r.h }
  return { w: Math.round(w), h: Math.round(h) }
})

onMounted(() => {
  window.addEventListener('keydown', onKey)
  ro = new ResizeObserver(() => {
    if (stage.value) stageSize.value = { w: stage.value.clientWidth, h: stage.value.clientHeight }
  })
  if (stage.value) ro.observe(stage.value)
  document.documentElement.style.overflow = 'hidden'
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey)
  ro?.disconnect()
  ctrl.abort()
  resetVideo()
  if (livePress) clearTimeout(livePress)
  if (raf) cancelAnimationFrame(raf)
  previews.clear()
  thumbs.clear()
  document.documentElement.style.overflow = ''
})

const fav = computed(() => current.value?.favorite ?? false)
</script>

<template>
  <div class="photo-viewer fixed inset-0 z-50 flex bg-[#111] text-[#eee]" role="dialog" aria-modal="true" :aria-label="t('photos.viewer')">
    <div class="relative flex min-w-0 flex-1 flex-col">
      <!-- top bar -->
      <div class="top absolute inset-x-0 top-0 z-20 flex items-start justify-between gap-2 px-2 py-2 md:px-3">
        <div class="flex min-w-0 items-start gap-1">
          <button type="button" class="act" :title="t('photos.back')" :aria-label="t('photos.back')" @click="emit('close')">
            <Icon name="lucide:chevron-left" size="20" />
          </button>
          <div class="min-w-0 pt-1">
            <div class="truncate text-sm font-semibold">{{ title }}</div>
            <div v-if="subtitle" class="truncate text-xs opacity-70">{{ subtitle }}</div>
          </div>
        </div>
        <div v-if="current" class="flex shrink-0 items-center">
          <button v-if="asset?.edited && asset.originalId" type="button" class="act text-xs" :class="original ? 'text-[#7ab8ff]' : ''" @click="toggleOriginal">
            {{ original ? t('photos.show_edited') : t('photos.show_original') }}
          </button>
          <button type="button" class="act" :title="fav ? t('photos.unfavorite') : t('photos.favorite')" :aria-pressed="fav" @click="emit('favorite', current.id, !fav)">
            <svg v-if="fav" width="20" height="20" viewBox="0 0 24 24" fill="currentColor" class="text-[#ff6b81]" aria-hidden="true">
              <path d="M12 21s-7.5-4.6-9.6-9.2C.9 8.4 2.9 4.5 6.6 4.1c2.1-.2 3.9.9 5.4 2.7 1.5-1.8 3.3-2.9 5.4-2.7 3.7.4 5.7 4.3 4.2 7.7C19.5 16.4 12 21 12 21z" />
            </svg>
            <Icon v-else name="lucide:heart" size="20" />
          </button>
          <button type="button" class="act" :title="t('photos.download_original')" @click="emit('download', current.id)">
            <Icon name="lucide:download" size="20" />
          </button>
          <button type="button" class="act" :title="hiddenMode ? t('photos.unhide') : t('photos.hide')" @click="emit('hide', current.id, !hiddenMode)">
            <Icon :name="hiddenMode ? 'lucide:eye' : 'lucide:eye-off'" size="20" />
          </button>
          <button type="button" class="act" :title="t('common.delete')" @click="emit('delete', current.id)">
            <Icon name="lucide:trash-2" size="20" />
          </button>
          <button type="button" class="act" :class="showInfo ? 'text-[#7ab8ff]' : ''" :title="t('photos.info')" :aria-pressed="showInfo" @click="showInfo = !showInfo">
            <Icon name="lucide:info" size="20" />
          </button>
        </div>
      </div>

      <!-- the photo -->
      <div
        ref="stage"
        class="stage relative flex-1 select-none"
        @pointerdown="onPointerDown"
        @pointermove="onPointerMove"
        @pointerup="onPointerUp"
        @pointercancel="onPointerUp"
        @contextmenu="(e) => { if (asset?.live) e.preventDefault() }"
      >
        <div v-if="current" class="absolute inset-0 flex items-center justify-center">
          <div
            class="relative overflow-hidden"
            ref="photoBox"
            :style="{ width: `${box.w}px`, height: `${box.h}px` }"
            @mouseenter="onEnter"
            @mouseleave="onLeave"
            @mousemove="onMove"
          >
            <!-- video -->
            <template v-if="current.kind === 'video'">
              <video
                v-if="video.state === 'ready'"
                :key="video.url"
                :src="video.url"
                class="h-full w-full bg-black"
                controls
                autoplay
                playsinline
                @error="video = { state: 'error', url: '' }"
              />
              <template v-else>
                <img v-if="srcPreview(current) || srcThumb(current)" :src="(srcPreview(current) || srcThumb(current))!" alt="" class="h-full w-full object-cover" :class="srcPreview(current) ? '' : 'blur-sm'" draggable="false" />
                <div class="absolute inset-0 flex flex-col items-center justify-center gap-2 bg-black/40 p-4 text-center text-sm">
                  <template v-if="video.state === 'preparing'">
                    <Icon name="lucide:loader-circle" size="28" class="animate-spin" />
                    <span>{{ t('photos.video_preparing') }}</span>
                  </template>
                  <template v-else-if="video.state === 'unsupported' || video.state === 'error'">
                    <Icon name="lucide:film" size="28" class="opacity-70" />
                    <span>{{ video.state === 'unsupported' ? t('photos.video_unsupported') : t('photos.video_error') }}</span>
                    <button type="button" class="mt-1 rounded-md bg-white/15 px-3 py-1.5 hover:bg-white/25" @click="emit('download', current.id)">
                      <Icon name="lucide:download" size="16" /> {{ t('photos.download_original') }}
                    </button>
                  </template>
                  <Icon v-else name="lucide:loader-circle" size="28" class="animate-spin opacity-70" />
                </div>
              </template>
            </template>

            <!-- photo: the preview, the stretched thumbnail until it comes -->
            <template v-else>
              <img v-if="shownSrc" :src="shownSrc" alt="" class="h-full w-full object-contain" draggable="false" />
              <img v-else-if="srcThumb(current)" :src="srcThumb(current)!" alt="" class="h-full w-full scale-105 object-cover blur-[2px]" draggable="false" />
              <div v-else-if="shownSrc === null" class="flex h-full w-full items-center justify-center opacity-60">
                <Icon name="lucide:image-off" size="28" />
              </div>
              <video
                v-if="livePlaying && liveURL"
                :src="liveURL"
                class="absolute inset-0 h-full w-full object-contain"
                autoplay
                muted
                playsinline
                @ended="stopLive"
              />
            </template>
          </div>
          <span v-if="current.live" class="pointer-events-none absolute left-3 top-16 rounded bg-black/50 px-1.5 py-0.5 text-[10px] font-semibold tracking-wide">
            ◎ LIVE
          </span>
        </div>
        <div v-else class="absolute inset-0 flex items-center justify-center opacity-60">
          <Icon name="lucide:loader-circle" size="24" class="animate-spin" />
        </div>

        <!-- arrows -->
        <button type="button" class="nav left-1 md:left-3" :aria-label="t('photos.prev')" @click.stop="go(-1)">
          <Icon name="lucide:chevron-left" size="28" />
        </button>
        <button type="button" class="nav right-1 md:right-3" :aria-label="t('photos.next')" @click.stop="go(1)">
          <Icon name="lucide:chevron-right" size="28" />
        </button>
        <div v-if="loadingMonth" class="pointer-events-none absolute right-3 top-16 opacity-60">
          <Icon name="lucide:loader-circle" size="16" class="animate-spin" />
        </div>
      </div>

      <!-- neighbours -->
      <div class="flex h-14 shrink-0 items-center justify-center gap-0.5 overflow-hidden px-2 pb-2 pt-1">
        <button
          v-for="x in strip"
          :key="x.row.id"
          type="button"
          class="strip-tile relative h-10 w-10 shrink-0 overflow-hidden rounded-sm bg-white/10"
          :class="x.row.id === pos.id ? 'ring-2 ring-white' : 'opacity-70 hover:opacity-100'"
          @click="goTo({ month: x.month, id: x.row.id })"
        >
          <img v-if="srcThumb(x.row)" :src="srcThumb(x.row)!" alt="" class="h-full w-full object-cover" draggable="false" />
          <span v-if="x.row.kind === 'video'" class="absolute bottom-0 right-0.5 text-[8px] font-semibold">{{ formatDuration(x.row.durationMs) }}</span>
        </button>
      </div>
    </div>

    <!-- info -->
    <div v-if="showInfo" class="info-pane absolute inset-x-0 bottom-0 z-30 h-[60%] border-t border-[#333] md:static md:h-auto md:w-72 md:border-l md:border-t-0">
      <InfoPanel
        :asset="asset"
        :maps-ready="mapsReady"
        @close="showInfo = false"
        @edit-date="current && emit('edit-date', current.id)"
        @edit-place="current && emit('edit-place', current.id)"
      />
    </div>
  </div>
</template>

<style scoped>
.top { background: linear-gradient(rgb(0 0 0 / 0.65), rgb(0 0 0 / 0)); }
.act {
  display: inline-flex;
  align-items: center;
  gap: 0.25rem;
  border-radius: 0.375rem;
  padding: 0.5rem;
  opacity: 0.85;
}
.act:hover { opacity: 1; background: rgb(255 255 255 / 0.08); }
.stage { touch-action: none; }
.nav {
  position: absolute;
  top: 50%;
  transform: translateY(-50%);
  z-index: 10;
  border-radius: 9999px;
  padding: 0.25rem;
  opacity: 0.55;
}
.nav:hover { opacity: 1; background: rgb(255 255 255 / 0.08); }
@media (hover: none) { .nav { display: none; } }
</style>
