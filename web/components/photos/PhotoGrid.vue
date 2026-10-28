<script setup lang="ts">
// The timeline: one continuous grid of square tiles (Apple Photos), a «Июль 2019 · place»
// pill top left and the year scrubber on the right (Immich).
//
// Virtualized twice over: geometry comes from the month counts (lib/photos/layout.ts),
// so the scroll height is right before anything loads and never jumps; a month's rows
// are fetched when it comes near the viewport and dropped when it is far away; and only
// the tiles of the rows around the viewport are in the DOM. The grid is generic — the
// timeline, the Hidden tab and others pass their own buckets and month fetcher.
import type { PhotoRow, Bucket } from '~/composables/usePhotos'
import { formatDuration, monthTitle, thumbKey, thumbURL } from '~/composables/usePhotos'
import {
  computeLayout, totalHeight, totalCount, indexRange, blockOfIndex, visibleMonths,
  clampColumns, tileSizeFor, effectiveColumns, anchorAt, scrollForAnchor, rangeIds, pillBlock,
  type MonthBlock, type PillJump,
} from '~/lib/photos/layout'
import type { ThumbLoader, ThumbWant } from '~/lib/photos/thumbLoader'
import TimeScrubber from '~/components/photos/TimeScrubber.vue'
import { mapConcurrent } from '~/lib/mapConcurrent'

const props = withDefaults(defineProps<{
  buckets: Bucket[]
  fetchMonth: (month: string, signal: AbortSignal) => Promise<PhotoRow[]>
  loader: ThumbLoader
  selectable?: boolean
  // localStorage key of the user's column choice
  columnsKey?: string
}>(), { selectable: true, columnsKey: 'photos.columns' })

const selection = defineModel<Set<string>>('selection', { default: () => new Set<string>() })
const emit = defineEmits<{
  // assetId, the ids of the loaded months around it in timeline order, and its month (for the viewer)
  open: [id: string, ids: string[], month: string]
  // a Shift-range could not be resolved (a month failed to load); nothing was selected
  'range-failed': []
}>()

const { t, locale } = useI18n()

const GAP = 2
const MONTH_FETCHES = 4 // month requests in flight
const KEEP_VIEWPORTS = 6 // months farther than this many screens are dropped

// ---- size & scroll ----
const scroller = ref<HTMLElement | null>(null)
const width = ref(0)
const viewportH = ref(0)
const scrollTop = ref(0)
let ro: ResizeObserver | null = null
let rafScroll = 0

function readSize() {
  const el = scroller.value
  if (!el) return
  const cs = getComputedStyle(el)
  // The right padding is the scrubber's lane.
  width.value = el.clientWidth - (parseFloat(cs.paddingLeft) || 0) - (parseFloat(cs.paddingRight) || 0)
  viewportH.value = el.clientHeight
}
function onScroll() {
  if (rafScroll) return
  rafScroll = requestAnimationFrame(() => {
    rafScroll = 0
    if (scroller.value) scrollTop.value = scroller.value.scrollTop
  })
}

// ---- columns ----
function loadColumns(): number | null {
  try {
    const v = Number(localStorage.getItem(props.columnsKey))
    return v ? clampColumns(v) : null
  } catch { return null }
}
const chosenColumns = ref<number | null>(loadColumns())
// The user's choice (or ~140 px by width), raised so tiles never exceed MAX_TILE.
const columns = computed(() => effectiveColumns(width.value || 900, chosenColumns.value, GAP))
function setColumns(n: number) {
  const c = clampColumns(n)
  if (c === columns.value) return
  chosenColumns.value = c
  try { localStorage.setItem(props.columnsKey, String(c)) } catch { /* private mode */ }
}

const tile = computed(() => tileSizeFor(width.value || 900, columns.value, GAP))
const rowH = computed(() => tile.value + GAP)
const layout = computed<MonthBlock[]>(() => computeLayout(props.buckets, columns.value, tile.value, GAP))
const total = computed(() => totalCount(layout.value))
const contentH = computed(() => totalHeight(layout.value, columns.value, tile.value, GAP))


// Ctrl+wheel (also the trackpad pinch on macOS) changes the density.
let wheelAcc = 0
function onWheel(e: WheelEvent) {
  if (!e.ctrlKey) return
  e.preventDefault()
  wheelAcc += e.deltaY
  if (Math.abs(wheelAcc) >= 40) {
    setColumns(columns.value + (wheelAcc > 0 ? 1 : -1))
    wheelAcc = 0
  }
}

// Two-finger pinch on touch screens.
let pinch: { dist: number; cols: number } | null = null
function touchDist(e: TouchEvent) {
  const [a, b] = [e.touches[0], e.touches[1]]
  return Math.hypot(a.clientX - b.clientX, a.clientY - b.clientY)
}
function onTouchStart(e: TouchEvent) {
  if (e.touches.length === 2) {
    pinch = { dist: touchDist(e), cols: columns.value }
    cancelLongPress()
  }
}
function onTouchMove(e: TouchEvent) {
  if (!pinch || e.touches.length !== 2) return
  e.preventDefault()
  const ratio = touchDist(e) / Math.max(1, pinch.dist)
  setColumns(Math.round(pinch.cols / ratio))
}
function onTouchEnd(e: TouchEvent) {
  if (e.touches.length < 2) pinch = null
}

// ---- month content ----
const months = shallowReactive(new Map<string, PhotoRow[]>())
const stale = new Set<string>() // shown until a fresh copy arrives
const fetching = new Map<string, AbortController>()
const failures = new Map<string, { n: number; until: number }>() // per-month retry backoff
let generation = 0

function nearMonths(): MonthBlock[] {
  const vh = viewportH.value || 800
  const st = scrollTop.value
  const center = st + vh / 2
  return visibleMonths(layout.value, st, vh, vh)
    .sort((a, b) => Math.abs(a.top + a.height / 2 - center) - Math.abs(b.top + b.height / 2 - center))
}

function ensureMonths() {
  const vh = viewportH.value || 800
  const near = nearMonths()
  const nearSet = new Set(near.map((b) => b.month))
  // Abort what scrolled away before it arrived.
  for (const [m, ctrl] of fetching) {
    if (!nearSet.has(m)) { ctrl.abort(); fetching.delete(m) }
  }
  for (const b of near) {
    if (fetching.size >= MONTH_FETCHES) break
    if ((months.has(b.month) && !stale.has(b.month)) || fetching.has(b.month)) continue
    if ((failures.get(b.month)?.until ?? 0) > Date.now()) continue
    loadMonth(b.month)
  }
  // Drop what is far away.
  const keep = new Set(visibleMonths(layout.value, scrollTop.value, vh, vh * KEEP_VIEWPORTS).map((b) => b.month))
  for (const m of [...months.keys()]) {
    if (!keep.has(m)) { months.delete(m); stale.delete(m) }
  }
}

function loadMonth(month: string): Promise<PhotoRow[] | null> {
  const ctrl = new AbortController()
  fetching.set(month, ctrl)
  const gen = generation
  return props.fetchMonth(month, ctrl.signal).then(
    (rows) => {
      if (fetching.get(month) === ctrl) fetching.delete(month)
      if (gen !== generation || ctrl.signal.aborted) return null
      months.set(month, rows)
      stale.delete(month)
      failures.delete(month)
      scheduleEnsure()
      return rows
    },
    () => {
      if (fetching.get(month) === ctrl) fetching.delete(month)
      if (!ctrl.signal.aborted) {
        // 1 s, 2 s, 4 s … up to a minute
        const n = (failures.get(month)?.n ?? 0) + 1
        const wait = Math.min(60_000, 1000 * 2 ** (n - 1))
        failures.set(month, { n, until: Date.now() + wait })
        scheduleEnsure(wait)
      }
      return null
    },
  )
}

// Throttled, not debounced: a long momentum scroll keeps loading months on its way.
let ensureTimer: ReturnType<typeof setTimeout> | null = null
let ensureDue = 0
function scheduleEnsure(delay = 80) {
  const due = Date.now() + delay
  if (ensureTimer && ensureDue <= due) return // an earlier run is already scheduled
  if (ensureTimer) clearTimeout(ensureTimer)
  ensureDue = due
  ensureTimer = setTimeout(() => { ensureTimer = null; ensureMonths() }, delay)
}

// reset: new data set (another filter) — forget everything and go to the top.
function reset() {
  generation++
  for (const c of fetching.values()) c.abort()
  fetching.clear()
  rangeCtrl?.abort()
  months.clear()
  stale.clear()
  failures.clear()
  lastIndex = null // pointed into the old index space
  if (scroller.value) scroller.value.scrollTop = 0
  scrollTop.value = 0
  scheduleEnsure(0)
}

// invalidate: same data set changed (counts moved) — refetch, keep showing the old rows meanwhile.
function invalidate() {
  generation++
  for (const c of fetching.values()) c.abort()
  fetching.clear()
  for (const m of months.keys()) stale.add(m)
  failures.clear()
  scheduleEnsure(0)
}

watch(() => props.fetchMonth, reset)
watch(() => props.buckets, invalidate)
watch([scrollTop, viewportH, layout], () => scheduleEnsure())

// Keep the first visible tile in place across relayouts: new photos indexed into newer
// months (the buckets poll), another column count or width, removed cards. The anchor is
// (month, index in month), read before the DOM changes (pre-flush, so the browser has not
// clamped scrollTop yet) and applied after it. At the very top we stay at the top.
watch([layout, columns, rowH], (_n, [oldLayout, oldCols, oldRowH]) => {
  const el = scroller.value
  if (!el || el.scrollTop < 1 || !oldLayout?.length) return
  const a = anchorAt(oldLayout, el.scrollTop, oldCols, oldRowH)
  if (!a) return
  nextTick(() => {
    if (!layout.value.length) return
    el.scrollTop = scrollForAnchor(layout.value, a, columns.value, rowH.value)
    scrollTop.value = el.scrollTop
  })
})

// ---- tiles in the DOM ----
interface Slot {
  index: number
  x: number
  y: number
  month: string
  item: PhotoRow | undefined
}

const slots = computed<Slot[]>(() => {
  const L = layout.value
  if (!L.length) return []
  const vh = viewportH.value || 800
  const [from, to] = indexRange(scrollTop.value, vh, vh / 2, columns.value, rowH.value, total.value)
  const out: Slot[] = []
  let bi = blockOfIndex(L, from)
  const c = columns.value
  const rh = rowH.value
  for (let i = from; i < to; i++) {
    while (bi < L.length - 1 && i >= L[bi + 1].offset) bi++
    const b = L[bi]
    out.push({
      index: i,
      x: (i % c) * rh,
      y: Math.floor(i / c) * rh,
      month: b.month,
      item: months.get(b.month)?.[i - b.offset],
    })
  }
  return out
})

// ---- thumbnails ----
// The loader reports each finished thumbnail; re-render once per frame, not per image.
const thumbTick = ref(0)
let rafThumb = 0
function onThumb() {
  if (rafThumb) return
  rafThumb = requestAnimationFrame(() => { rafThumb = 0; thumbTick.value++ })
}
function srcOf(item: PhotoRow): string | null | undefined {
  void thumbTick.value
  return props.loader.get(thumbKey(item))
}

watch(slots, (list) => {
  const st = scrollTop.value
  const vh = viewportH.value || 800
  const wants: ThumbWant[] = []
  for (const s of list) {
    if (!s.item) continue
    // 0 on screen, then the distance in rows
    const dist = s.y + tile.value < st ? (st - s.y) : s.y > st + vh ? (s.y - st - vh) : 0
    wants.push({ key: thumbKey(s.item), url: thumbURL(s.item), priority: Math.ceil(dist / rowH.value) })
  }
  props.loader.sync(wants)
})

// ---- pill ----
// The last scrubber jump (see pillBlock).
const lastJump = ref<PillJump | null>(null)
const pill = computed(() => {
  const L = layout.value
  if (!L.length) return null
  const first = Math.min(total.value - 1, Math.floor(scrollTop.value / rowH.value) * columns.value)
  // The month of the top-left tile on screen, or the one a scrubber jump just went to.
  const bi = pillBlock(L, scrollTop.value, viewportH.value || 800, columns.value, rowH.value, total.value, lastJump.value)
  if (bi < 0) return null
  const b = L[bi]
  let place: string | null = null
  // The first photo on screen with a known place, within its month.
  const rows = months.get(b.month)
  if (rows) {
    const visibleTiles = Math.ceil((viewportH.value || 800) / rowH.value) * columns.value
    for (let i = Math.max(0, first - b.offset); i < rows.length && i < Math.max(0, first - b.offset) + visibleTiles; i++) {
      if (rows[i]?.place) { place = rows[i].place; break }
    }
  }
  return { title: monthTitle(b.month, locale.value), place }
})

// ---- selection & opening ----
let lastIndex: number | null = null
const selecting = computed(() => selection.value.size > 0)

function toggle(item: PhotoRow, index: number) {
  const next = new Set(selection.value)
  if (next.has(item.id)) next.delete(item.id)
  else next.add(item.id)
  selection.value = next
  lastIndex = index
}

// Shift-range: every month the range touches must be loaded before anything is
// selected — far months come through their own requests, outside the viewport loading
// (which aborts what scrolls away), so nothing in the range is silently left out.
let rangeCtrl: AbortController | null = null
async function selectRange(to: number) {
  const from = lastIndex ?? to
  const L = layout.value
  const gen = generation
  const extra = new Map<string, PhotoRow[]>()
  const rowsOf = (m: string) => extra.get(m) ?? months.get(m)
  let r = rangeIds(L, rowsOf, from, to)
  if (r.missing.length) {
    rangeCtrl?.abort()
    const ctrl = new AbortController()
    rangeCtrl = ctrl
    try {
      await mapConcurrent(r.missing, MONTH_FETCHES, async (m) => {
        extra.set(m, await props.fetchMonth(m, ctrl.signal))
      })
    } catch {
      if (!ctrl.signal.aborted) emit('range-failed')
      return
    } finally {
      if (rangeCtrl === ctrl) rangeCtrl = null
    }
    // The counts changed meanwhile (a buckets refresh): the indices no longer hold.
    if (gen !== generation) { emit('range-failed'); return }
    r = rangeIds(L, rowsOf, from, to)
    if (r.missing.length) { emit('range-failed'); return }
  }
  const next = new Set(selection.value)
  for (const id of r.ids) next.add(id)
  selection.value = next
  lastIndex = to
}

// orderedAround: the loaded, contiguous months around the clicked one, in order.
function orderedAround(month: string): string[] {
  const L = layout.value
  const at = L.findIndex((b) => b.month === month)
  if (at < 0) return []
  let lo = at
  let hi = at
  while (lo > 0 && months.has(L[lo - 1].month)) lo--
  while (hi < L.length - 1 && months.has(L[hi + 1].month)) hi++
  const ids: string[] = []
  for (let i = lo; i <= hi; i++) for (const r of months.get(L[i].month) ?? []) ids.push(r.id)
  return ids
}

let suppressClick = false
function onTileClick(e: MouseEvent, s: Slot) {
  if (suppressClick) { suppressClick = false; return }
  if (!s.item) return
  if (props.selectable && e.shiftKey && (selecting.value || lastIndex !== null)) { selectRange(s.index); return }
  if (props.selectable && (selecting.value || e.metaKey || e.ctrlKey)) { toggle(s.item, s.index); return }
  emit('open', s.item.id, orderedAround(s.month), s.month)
}
function onCheckClick(e: MouseEvent, s: Slot) {
  if (!s.item) return
  if (e.shiftKey && lastIndex !== null) selectRange(s.index)
  else toggle(s.item, s.index)
}

// Long press selects on phones.
let pressTimer: ReturnType<typeof setTimeout> | null = null
let pressStart: { x: number; y: number } | null = null
function onTilePointerDown(e: PointerEvent, s: Slot) {
  // A long-press is not always followed by a click (iOS): never let it eat the next tap.
  suppressClick = false
  if (e.pointerType !== 'touch' || !props.selectable || !s.item) return
  pressStart = { x: e.clientX, y: e.clientY }
  const item = s.item
  pressTimer = setTimeout(() => {
    pressTimer = null
    suppressClick = true
    toggle(item, s.index)
    navigator.vibrate?.(10)
  }, 450)
}
function onTilePointerMove(e: PointerEvent) {
  if (pressStart && Math.hypot(e.clientX - pressStart.x, e.clientY - pressStart.y) > 10) cancelLongPress()
}
function cancelLongPress() {
  if (pressTimer) clearTimeout(pressTimer)
  pressTimer = null
  pressStart = null
}

function jumpTo(month: string) {
  const b = layout.value.find((x) => x.month === month)
  if (!b || !scroller.value) return
  scroller.value.scrollTop = b.top
  scrollTop.value = scroller.value.scrollTop
  lastJump.value = { month, scrollTop: scrollTop.value }
}

// ---- lifecycle ----
onMounted(() => {
  const el = scroller.value!
  readSize()
  ro = new ResizeObserver(readSize)
  ro.observe(el)
  el.addEventListener('wheel', onWheel, { passive: false })
  el.addEventListener('touchmove', onTouchMove, { passive: false })
  props.loader.onChange = onThumb
  scheduleEnsure(0)
})
onBeforeUnmount(() => {
  ro?.disconnect()
  scroller.value?.removeEventListener('wheel', onWheel)
  scroller.value?.removeEventListener('touchmove', onTouchMove)
  if (rafScroll) cancelAnimationFrame(rafScroll)
  if (rafThumb) cancelAnimationFrame(rafThumb)
  if (ensureTimer) clearTimeout(ensureTimer)
  cancelLongPress()
  for (const c of fetching.values()) c.abort()
  rangeCtrl?.abort()
  props.loader.sync([])
  props.loader.onChange = () => {}
})

// ---- API for the page ----
// id -> row over the loaded months, rebuilt when they change (getItem is called per
// selected id, e.g. to tell whether a whole selection is favorite).
const byId = computed(() => {
  const m = new Map<string, PhotoRow>()
  for (const rows of months.values()) for (const r of rows) m.set(r.id, r)
  return m
})
function getItem(id: string): PhotoRow | undefined {
  return byId.value.get(id)
}
function patchItems(ids: Iterable<string>, patch: Partial<PhotoRow>) {
  const set = new Set(ids)
  for (const [m, rows] of months) {
    if (rows.some((r) => set.has(r.id))) months.set(m, rows.map((r) => (set.has(r.id) ? { ...r, ...patch } : r)))
  }
}
function removeItems(ids: Iterable<string>) {
  const set = new Set(ids)
  for (const [m, rows] of months) {
    if (rows.some((r) => set.has(r.id))) months.set(m, rows.filter((r) => !set.has(r.id)))
  }
}
// loadedMonths: a snapshot of the months in memory (the viewer starts from it).
function loadedMonths(): Map<string, PhotoRow[]> {
  return new Map(months)
}
function loadedIds(): string[] {
  const ids: string[] = []
  for (const b of layout.value) for (const r of months.get(b.month) ?? []) ids.push(r.id)
  return ids
}

defineExpose({ reset, invalidate, getItem, patchItems, removeItems, loadedIds, loadedMonths, jumpTo, columns, setColumns })
</script>

<template>
  <div class="relative h-full min-h-0">
    <div
      ref="scroller"
      class="grid-scroller h-full overflow-y-auto overflow-x-hidden pr-8 md:pr-10"
      @scroll.passive="onScroll"
      @touchstart.passive="onTouchStart"
      @touchend.passive="onTouchEnd"
      @touchcancel.passive="onTouchEnd"
    >
      <div class="relative" :style="{ height: `${contentH}px` }">
        <div
          v-for="s in slots"
          :key="s.index"
          class="tile group absolute overflow-hidden bg-ink/5"
          :class="s.item && selection.has(s.item.id) ? 'is-selected' : ''"
          :style="{ width: `${tile}px`, height: `${tile}px`, transform: `translate(${s.x}px, ${s.y}px)` }"
          @click="onTileClick($event, s)"
          @pointerdown="onTilePointerDown($event, s)"
          @pointermove="onTilePointerMove"
          @pointerup="cancelLongPress"
          @pointercancel="cancelLongPress"
          @contextmenu="(e) => { if (pressStart || suppressClick) e.preventDefault() }"
        >
          <template v-if="s.item">
            <img
              v-if="srcOf(s.item)"
              :src="srcOf(s.item)!"
              alt=""
              draggable="false"
              class="thumb h-full w-full cursor-pointer object-cover"
            />
            <div v-else-if="srcOf(s.item) === null" class="flex h-full w-full items-center justify-center text-muted">
              <Icon name="lucide:image-off" size="18" />
            </div>

            <!-- badges -->
            <span v-if="s.item.live && tile >= 60" class="badge absolute left-1 top-1">
              <Icon name="lucide:circle-dot" size="14" />
            </span>
            <span v-if="s.item.kind === 'video'" class="badge absolute bottom-1 right-1 text-[10px] font-semibold tabular-nums">
              {{ formatDuration(s.item.durationMs) || '▶' }}
            </span>
            <span v-if="s.item.favorite" class="badge absolute bottom-1 left-1">
              <!-- filled heart (lucide icons render as masks and cannot be filled) -->
              <svg width="12" height="12" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                <path d="M12 21s-7.5-4.6-9.6-9.2C.9 8.4 2.9 4.5 6.6 4.1c2.1-.2 3.9.9 5.4 2.7 1.5-1.8 3.3-2.9 5.4-2.7 3.7.4 5.7 4.3 4.2 7.7C19.5 16.4 12 21 12 21z" />
              </svg>
            </span>

            <!-- selection circle -->
            <button
              v-if="selectable"
              type="button"
              class="check absolute right-1 top-1 flex h-5 w-5 items-center justify-center rounded-full border-2 border-white/90 transition"
              :class="[
                selection.has(s.item.id) ? 'border-accent bg-accent text-bg opacity-100' : 'bg-black/20 text-transparent',
                selecting || selection.has(s.item.id) ? 'opacity-100' : 'opacity-0 group-hover:opacity-100',
              ]"
              :aria-label="t('photos.select')"
              :aria-pressed="selection.has(s.item.id)"
              @click.stop="onCheckClick($event, s)"
            >
              <Icon name="lucide:check" size="12" />
            </button>
          </template>
        </div>
      </div>
    </div>

    <!-- where we are -->
    <div
      v-if="pill && !selecting"
      class="pointer-events-none absolute left-2 top-2 z-10 max-w-[70%] truncate rounded-full bg-black/60 px-3 py-1 text-xs font-semibold text-white backdrop-blur"
    >
      {{ pill.title }}<span v-if="pill.place" class="font-normal opacity-80"> · {{ pill.place }}</span>
    </div>

    <TimeScrubber
      v-if="layout.length"
      :layout="layout"
      :content-height="contentH"
      :scroll-top="scrollTop"
      :viewport-h="viewportH"
      @jump="jumpTo"
    />
  </div>
</template>

<style scoped>
.grid-scroller {
  touch-action: pan-y;
  scrollbar-width: none;
  overscroll-behavior: contain;
}
.grid-scroller::-webkit-scrollbar { display: none; }
.tile { contain: strict; }
.thumb {
  -webkit-touch-callout: none;
  user-select: none;
  transition: transform 0.12s ease;
}
.is-selected .thumb { transform: scale(0.88); }
.is-selected { background: rgb(var(--c-accent) / 0.15); }
.badge {
  color: #fff;
  line-height: 1;
  filter: drop-shadow(0 1px 1px rgb(0 0 0 / 0.7));
  pointer-events: none;
}
</style>
