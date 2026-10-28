<script setup lang="ts">
// «Фото»: the gallery. Sections are tabs driven by ?tab= (as on the settings page):
// Фото (the timeline) · В этот день · Скрытое · Корзина. Albums, shared and the map come
// in later stages and are not shown.
import PhotoGrid from '~/components/photos/PhotoGrid.vue'
import PhotoFilters from '~/components/photos/PhotoFilters.vue'
import SelectionBar from '~/components/photos/SelectionBar.vue'
import PhotoViewer from '~/components/photos/PhotoViewer.vue'
import PhotoMemories from '~/components/photos/PhotoMemories.vue'
import PhotoTrash from '~/components/photos/PhotoTrash.vue'
import HiddenUnlock from '~/components/photos/HiddenUnlock.vue'
import EditDateDialog from '~/components/photos/EditDateDialog.vue'
import EditPlaceDialog from '~/components/photos/EditPlaceDialog.vue'
import type { Bucket, PhotoFilter, PhotoRow, PhotoSettings } from '~/composables/usePhotos'
import { mapConcurrent } from '~/lib/mapConcurrent'
import { photosHiddenHeaders, photosHiddenUnlocked, lockPhotosHidden } from '~/lib/photos/hidden'
import { photosNavVisible } from '~/lib/photos/settings'
import type { EditResult } from '~/lib/photos/edit'

definePageMeta({ fullBleed: true })

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const api = usePhotosApi()
const { request } = useApi()
const { confirm } = useDialog()
const photosEnabled = usePhotosEnabled()

// ---- sections ----
const tabs = ['photos', 'memories', 'hidden', 'trash'] as const
type Tab = (typeof tabs)[number]
const tab = computed<Tab>(() => {
  const q = route.query.tab
  return typeof q === 'string' && (tabs as readonly string[]).includes(q) ? (q as Tab) : 'photos'
})
function selectTab(id: Tab) {
  selection.value = new Set()
  viewer.value = null
  router.replace({ query: { ...route.query, tab: id === 'photos' ? undefined : id } })
}

// ---- settings & buckets ----
const settings = ref<PhotoSettings | null>(null)
const loadError = ref('')
const filter = ref<PhotoFilter>({ kind: 'all', favorite: false, source: '' })
const buckets = ref<Bucket[]>([])
const bucketsLoaded = ref(false)

async function loadSettings() {
  try {
    settings.value = await api.settings()
    photosEnabled.value = photosNavVisible(settings.value)
  } catch (e: any) {
    loadError.value = e?.data?.error || t('common.error_load')
  }
}

let bucketsSeq = 0
async function loadBuckets() {
  if (tab.value === 'hidden') return loadHiddenBuckets()
  if (!settings.value?.enabled || !settings.value.available) return
  const seq = ++bucketsSeq
  try {
    const b = await api.buckets(filter.value)
    if (seq !== bucketsSeq) return
    // Same counts: keep the array, so the grid does not refetch for nothing.
    if (JSON.stringify(b) !== JSON.stringify(buckets.value)) buckets.value = b
    loadError.value = ''
  } catch (e: any) {
    if (seq === bucketsSeq) loadError.value = e?.data?.error || t('common.error_load')
  } finally {
    if (seq === bucketsSeq) bucketsLoaded.value = true
  }
}

// One fetcher per filter: a new function tells the grid to start over.
const fetchMonth = computed(() => {
  const f = { ...filter.value }
  return (month: string, signal: AbortSignal) => api.month(month, f, signal)
})
watch(filter, () => {
  selection.value = new Set()
  bucketsLoaded.value = false
  buckets.value = []
  loadBuckets()
}, { deep: true })

const indexing = computed(() => {
  const s = settings.value
  if (!s?.enabled || !s.sources.length) return null
  const done = s.indexing.ready + s.indexing.failed
  return done < s.indexing.total ? s.indexing : null
})

// While the index fills, the timeline grows: poll the progress and the counts.
// Sources with no media never get a total: give up waiting after a minute.
let poll: ReturnType<typeof setInterval> | null = null
let emptyPolls = 0
function startPolling() {
  if (poll) return
  emptyPolls = 0
  poll = setInterval(async () => {
    if (document.hidden || tab.value !== 'photos') return
    await loadSettings()
    await loadBuckets()
    if (!settings.value?.indexing.total && ++emptyPolls >= 12) stopPolling()
    if (!indexing.value && settings.value?.indexing.total) stopPolling()
  }, 5000)
}
function stopPolling() {
  if (poll) clearInterval(poll)
  poll = null
}

const loader = makeThumbLoader()

// ---- Скрытое: the same grid over the hidden cards, behind the unlock ----
const hiddenBuckets = ref<Bucket[]>([])
const hiddenLoaded = ref(false)
const hiddenLoader = makeThumbLoader(photosHiddenHeaders)
// A 403 hidden_locked means the unlock expired on the server: ask again.
function lockedOut(e: any): boolean {
  if (e?.response?.status === 403 && e?.data?.error === 'hidden_locked') {
    lockPhotosHidden()
    return true
  }
  return false
}
async function loadHiddenBuckets() {
  if (!photosHiddenUnlocked.value) return
  try {
    const b = await api.hiddenBuckets()
    if (JSON.stringify(b) !== JSON.stringify(hiddenBuckets.value)) hiddenBuckets.value = b
  } catch (e: any) {
    if (!lockedOut(e)) say(e?.data?.error || t('common.error_load'))
  } finally {
    hiddenLoaded.value = true
  }
}
const fetchHiddenMonth = (month: string, signal: AbortSignal) =>
  api.hiddenMonth(month, signal).catch((e) => { lockedOut(e); throw e })
watch(photosHiddenUnlocked, (on) => {
  if (on) { hiddenLoaded.value = false; loadHiddenBuckets() }
  else {
    hiddenBuckets.value = []
    hiddenLoader.clear()
    if (tab.value === 'hidden') { viewer.value = null; selection.value = new Set() }
  }
})
// Back on «Фото» the timeline re-reads its counts: an unhide (Hidden), a restore (Trash)
// or a hide/delete from the viewer elsewhere changed them. The grid mounts afresh and
// fetches its months; a count change on a mounted grid goes through invalidate().
watch(tab, (v) => {
  if (v === 'hidden') loadHiddenBuckets()
  else if (v === 'photos') loadBuckets()
})

onMounted(async () => {
  await loadSettings()
  await loadBuckets()
  if (tab.value === 'hidden') loadHiddenBuckets()
  if (indexing.value || (settings.value?.enabled && settings.value.sources.length && !settings.value.indexing.total)) startPolling()
  window.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  stopPolling()
  window.removeEventListener('keydown', onKey)
  if (noticeTimer) clearTimeout(noticeTimer)
  loader.clear()
  hiddenLoader.clear()
})

// ---- grid, selection, actions ----
const grid = ref<InstanceType<typeof PhotoGrid> | null>(null)
const selection = ref<Set<string>>(new Set())
const busy = ref(false)
const notice = ref('')
let noticeTimer: ReturnType<typeof setTimeout> | null = null
function say(msg: string) {
  notice.value = msg
  if (noticeTimer) clearTimeout(noticeTimer)
  noticeTimer = setTimeout(() => { notice.value = '' }, 4000)
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && selection.value.size && !viewer.value && !document.querySelector('[role="dialog"]')) selection.value = new Set()
}

const selectedIds = computed(() => [...selection.value])
const allFavorite = computed(() => {
  if (!selection.value.size || !grid.value) return false
  for (const id of selection.value) if (!grid.value.getItem(id)?.favorite) return false
  return true
})

async function act(fn: () => Promise<void>) {
  if (busy.value) return
  busy.value = true
  try { await fn() } catch (e: any) {
    if (!lockedOut(e)) say(e?.data?.error || t('photos.action_failed'))
  } finally { busy.value = false }
}

// The selection's actions; the viewer calls the same ones with a single id.
function setFavorite(ids: string[], favorite: boolean) {
  return act(async () => {
    await api.patch(ids, { favorite })
    grid.value?.patchItems(ids, { favorite })
    for (const id of ids) viewerRef.value?.patchRow(id, { favorite })
    if (tab.value === 'photos' && filter.value.favorite && !favorite) {
      grid.value?.removeItems(ids)
      if (!viewer.value) selection.value = new Set()
      await loadBuckets()
    }
  })
}
function toggleFavorite() {
  return setFavorite(selectedIds.value, !allFavorite.value)
}

// setHidden: hide from the timeline, or (Hidden tab) show again.
function setHidden(ids: string[], hidden: boolean) {
  return act(async () => {
    await api.patch(ids, { hidden })
    grid.value?.removeItems(ids)
    for (const id of ids) await viewerRef.value?.drop(id)
    selection.value = new Set()
    say(hidden ? t('photos.hidden_done', { n: ids.length }) : t('photos.unhidden_done', { n: ids.length }))
    await loadBuckets()
    if (tab.value === 'memories') memories.value?.reload()
  })
}
function hide() {
  return setHidden(selectedIds.value, tab.value !== 'hidden')
}

// How many other copies (same content in another source folder) go to the trash with
// the selection. Looked up per card for a moderate selection; a large one gets the
// general warning.
const COPIES_LOOKUP_MAX = 50
async function countCopies(ids: string[]): Promise<number | null> {
  if (ids.length > COPIES_LOOKUP_MAX) return null
  const found = await mapConcurrent(ids, 6, async (id) => {
    try { return Number((await api.asset(id)).copies) || 0 } catch { return 0 }
  })
  return found.reduce((a, b) => a + b, 0)
}

function removeIds(ids: string[]) {
  return act(async () => {
    const copies = await countCopies(ids)
    let message = t('photos.delete_message')
    if (copies === null) message += ' ' + t('photos.delete_copies_maybe')
    else if (copies > 0) message += ' ' + t('photos.delete_copies', { n: copies })
    busy.value = false // the dialog is up; the bar must not look stuck
    const ok = await confirm(t('photos.delete_title', { n: ids.length }), { message, confirmText: t('common.delete'), danger: true })
    busy.value = true
    if (!ok) return
    await api.remove(ids)
    grid.value?.removeItems(ids)
    for (const id of ids) await viewerRef.value?.drop(id)
    selection.value = new Set()
    say(t('photos.deleted_done', { n: ids.length }))
    await loadBuckets()
    if (tab.value === 'memories') memories.value?.reload()
  })
}
function remove() {
  return removeIds(selectedIds.value)
}

// Downloads the originals one by one (no zip endpoint): the browser saves each file.
const DOWNLOAD_CONFIRM = 20
function downloadIds(ids: string[]) {
  return act(async () => {
    if (ids.length > DOWNLOAD_CONFIRM) {
      busy.value = false
      const ok = await confirm(t('photos.download_many', { n: ids.length }), { confirmText: t('common.download') })
      busy.value = true
      if (!ok) return
    }
    let failed = 0
    for (const id of ids) {
      try {
        const a = await api.asset(id)
        const blob = await request<Blob>(`/files/${a.file.nodeId}/content`, { responseType: 'blob' })
        const url = URL.createObjectURL(blob)
        const link = document.createElement('a')
        link.href = url
        link.download = a.file.name
        link.click()
        setTimeout(() => URL.revokeObjectURL(url), 1000)
      } catch { failed++ }
    }
    if (failed) say(t('photos.download_failed', { n: failed }))
  })
}
function download() {
  return downloadIds(selectedIds.value)
}

// ---- the viewer ----
interface ViewerState {
  start: { id: string; month: string }
  months: string[]
  fetchMonth: (month: string, signal: AbortSignal) => Promise<PhotoRow[]>
  rows: Map<string, PhotoRow[]>
  hiddenMode: boolean
}
const viewer = shallowRef<ViewerState | null>(null)
const viewerRef = ref<InstanceType<typeof PhotoViewer> | null>(null)
const memories = ref<InstanceType<typeof PhotoMemories> | null>(null)

// From a grid: the timeline (or the hidden one) with the months already loaded.
function openViewer(id: string, _ids: string[], month: string) {
  const hidden = tab.value === 'hidden'
  viewer.value = {
    start: { id, month },
    months: (hidden ? hiddenBuckets.value : buckets.value).map((b) => b.month),
    fetchMonth: hidden ? fetchHiddenMonth : fetchMonth.value,
    rows: grid.value?.loadedMonths() ?? new Map(),
    hiddenMode: hidden,
  }
}
// From «В этот день»: the years stand in for months, all of them loaded.
function openMemory(id: string, months: string[], rows: Map<string, PhotoRow[]>) {
  const month = months.find((m) => rows.get(m)?.some((r) => r.id === id)) ?? ''
  viewer.value = {
    start: { id, month },
    months,
    fetchMonth: async (m: string) => rows.get(m) ?? [],
    rows,
    hiddenMode: false,
  }
}
function closeViewer() {
  viewer.value = null
}

// ---- the date and place dialogs (from the selection or the viewer) ----
const editDateIds = ref<string[] | null>(null)
const editPlaceIds = ref<string[] | null>(null)
function editDate() { editDateIds.value = selectedIds.value }
function editPlace() { editPlaceIds.value = selectedIds.value }

// After an edit: dates move cards between months, so the counts and the months reload;
// the viewer re-reads its card.
async function afterEdit(r: EditResult) {
  if (!r.updated.length && !r.partial.length) return
  await loadBuckets()
  grid.value?.invalidate()
  const v = viewerRef.value
  if (v) {
    const id = v.currentId()
    if (r.updated.includes(id) || r.partial.includes(id)) {
      try {
        const a = await api.asset(id)
        v.patchRow(id, { takenAt: a.takenAt, tzOffsetMin: a.tzOffsetMin, place: a.place })
      } catch { /* the viewer keeps its copy */ }
      await v.refresh(id)
    }
  }
  if (tab.value === 'memories' && !viewer.value) memories.value?.reload()
}
</script>

<template>
  <div class="flex h-full min-h-0 flex-col">
    <!-- header: title, sections, filters -->
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-line px-4 pt-3 md:px-6 md:pt-4">
      <h1 class="text-xl font-semibold">{{ t('photos.title') }}</h1>
      <nav class="-mb-px flex gap-1 overflow-x-auto">
        <button
          v-for="id in tabs"
          :key="id"
          type="button"
          class="shrink-0 border-b-2 px-3 py-2 text-sm transition"
          :class="tab === id ? 'border-accent text-accent' : 'border-transparent text-muted hover:text-ink'"
          @click="selectTab(id)"
        >{{ t(`photos.tabs.${id}`) }}</button>
      </nav>
    </div>

    <!-- states before the timeline -->
    <div v-if="!settings && !loadError" class="p-6 text-sm text-muted">{{ t('common.loading') }}</div>
    <div v-else-if="!settings" class="p-6 text-sm text-danger">{{ loadError }}</div>
    <div v-else-if="!settings.available || !settings.enabled" class="p-6">
      <div class="card max-w-lg p-5 text-sm">
        <Icon name="lucide:image" size="22" class="mb-2 text-muted" />
        <p v-if="!settings.available">{{ t('settings.photos_unavailable') }}</p>
        <p v-else>{{ t('photos.disabled') }}</p>
        <NuxtLink v-if="settings.available" to="/settings?tab=photos" class="btn-accent mt-3">
          <Icon name="lucide:settings" size="16" /> {{ t('photos.open_settings') }}
        </NuxtLink>
      </div>
    </div>

    <template v-else>
      <!-- Фото: the timeline -->
      <template v-if="tab === 'photos'">
        <div class="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2 md:px-6">
          <PhotoFilters v-model="filter" :sources="settings.sources" />
          <span v-if="indexing" class="flex items-center gap-1.5 text-xs text-muted">
            <Icon name="lucide:loader-circle" size="14" class="animate-spin" />
            {{ t('photos.indexing', { ready: indexing.ready, total: indexing.total }) }}
          </span>
          <span v-if="loadError" class="text-xs text-danger">{{ loadError }}</span>
        </div>

        <div class="relative min-h-0 flex-1 pl-2 md:pl-4">
          <div v-if="!settings.sources.length" class="flex h-full items-center justify-center p-6 text-center text-sm text-muted">
            <div>
              <p class="mb-3">{{ t('photos.no_sources') }}</p>
              <NuxtLink to="/settings?tab=photos" class="btn-accent">
                <Icon name="lucide:folder-plus" size="16" /> {{ t('photos.add_sources') }}
              </NuxtLink>
            </div>
          </div>
          <div v-else-if="bucketsLoaded && !buckets.length" class="flex h-full items-center justify-center p-6 text-center text-sm text-muted">
            {{ indexing || !settings.indexing.total ? t('photos.empty_indexing') : (filter.kind !== 'all' || filter.favorite || filter.source ? t('photos.empty_filtered') : t('photos.empty')) }}
          </div>
          <PhotoGrid
            v-else
            ref="grid"
            v-model:selection="selection"
            :buckets="buckets"
            :fetch-month="fetchMonth"
            :loader="loader"
            @open="openViewer"
            @range-failed="say(t('photos.range_failed'))"
          />

          <SelectionBar
            v-if="selection.size"
            :count="selection.size"
            :all-favorite="allFavorite"
            :busy="busy"
            @clear="selection = new Set()"
            @favorite="toggleFavorite"
            @hide="hide"
            @delete="remove"
            @download="download"
            @edit-date="editDate"
            @edit-place="editPlace"
          />
          <div
            v-if="notice"
            class="pointer-events-none absolute inset-x-0 z-30 flex justify-center px-4"
            :class="selection.size ? 'bottom-20' : 'bottom-4'"
            role="status"
          >
            <span class="rounded-full bg-black/75 px-3 py-1.5 text-xs text-white">{{ notice }}</span>
          </div>
        </div>
      </template>

      <!-- В этот день -->
      <PhotoMemories v-else-if="tab === 'memories'" ref="memories" @open="openMemory" />

      <!-- Скрытое: unlock first, then the grid of hidden cards -->
      <template v-else-if="tab === 'hidden'">
        <HiddenUnlock v-if="!photosHiddenUnlocked" @unlocked="loadHiddenBuckets" />
        <div v-else class="relative min-h-0 flex-1 pl-2 pt-2 md:pl-4">
          <div v-if="hiddenLoaded && !hiddenBuckets.length" class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm text-muted">
            <Icon name="lucide:eye-off" size="28" />
            <p>{{ t('photos.hidden_empty') }}</p>
          </div>
          <PhotoGrid
            v-else-if="hiddenBuckets.length"
            ref="grid"
            v-model:selection="selection"
            :buckets="hiddenBuckets"
            :fetch-month="fetchHiddenMonth"
            :loader="hiddenLoader"
            columns-key="photos.columns"
            @open="openViewer"
            @range-failed="say(t('photos.range_failed'))"
          />
          <SelectionBar
            v-if="selection.size"
            :count="selection.size"
            :all-favorite="allFavorite"
            :busy="busy"
            hidden-mode
            @clear="selection = new Set()"
            @favorite="toggleFavorite"
            @hide="hide"
            @delete="remove"
            @download="download"
            @edit-date="editDate"
            @edit-place="editPlace"
          />
          <div v-if="notice" class="pointer-events-none absolute inset-x-0 z-30 flex justify-center px-4" :class="selection.size ? 'bottom-20' : 'bottom-4'" role="status">
            <span class="rounded-full bg-black/75 px-3 py-1.5 text-xs text-white">{{ notice }}</span>
          </div>
        </div>
      </template>

      <!-- Корзина -->
      <PhotoTrash v-else-if="tab === 'trash'" @restored="(n) => say(t('photos.restored_done', { n }))" />
    </template>

    <!-- outside the tabs: the viewer and the dialogs over it -->
    <PhotoViewer
      v-if="viewer"
      ref="viewerRef"
      :key="viewer.start.id + viewer.months.length"
      :start="viewer.start"
      :months="viewer.months"
      :fetch-month="viewer.fetchMonth"
      :initial-rows="viewer.rows"
      :hidden-mode="viewer.hiddenMode"
      @close="closeViewer"
      @favorite="(id, v) => setFavorite([id], v)"
      @hide="(id, h) => setHidden([id], h)"
      @delete="(id) => removeIds([id])"
      @download="(id) => downloadIds([id])"
      @edit-date="(id) => (editDateIds = [id])"
      @edit-place="(id) => (editPlaceIds = [id])"
    />
    <div v-if="notice && (viewer || tab === 'memories' || tab === 'trash')" class="pointer-events-none fixed inset-x-0 bottom-20 z-[55] flex justify-center px-4" role="status">
      <span class="rounded-full bg-black/75 px-3 py-1.5 text-xs text-white">{{ notice }}</span>
    </div>
    <EditDateDialog v-if="editDateIds" :ids="editDateIds" @close="editDateIds = null" @done="afterEdit" />
    <EditPlaceDialog v-if="editPlaceIds" :ids="editPlaceIds" @close="editPlaceIds = null" @done="afterEdit" />
  </div>
</template>
