<script setup lang="ts">
// «Дубли»: near-identical cards, one group at a time. The copies stand side by side
// (stacked on a phone) with what tells them apart; the suggested one (assets[0]) carries
// «Оставить», a tap moves it. «Удалить остальные» moves the others to the trash,
// «Оставить все» tells the server they are not duplicates, «Пропустить» puts the group at
// the end of the queue for this visit. While the server is still hashing the library
// (progress.scanning) the list is re-fetched every 10 s and new groups join the end of the queue;
// the group on screen is not touched by that.
import type { DupAsset, DupGroups } from '~/composables/usePhotos'
import { previewURL, thumbURL, thumbKey, takenLabel, formatDuration } from '~/composables/usePhotos'
import { deleteWarnings, deletedBytes, groupKey, mergeQueue, nextLoad, skipGroup } from '~/lib/photos/duplicates'

type Group = DupGroups['groups'][number]

const emit = defineEmits<{ resolved: [bytes: number] }>()
const { t, locale } = useI18n()
const api = usePhotosApi()

const loaded = ref(false)
const queue = ref<Group[]>([])
const handled = ref(0) // groups resolved or dismissed in this visit: «Группа N из M»
const reclaim = ref(0)
const progress = ref({ hashed: 0, total: 0, scanning: false })
const error = ref('')
const busy = ref(false)
const keepId = ref('')

const scanning = computed(() => progress.value.scanning)
const current = computed<Group | null>(() => queue.value[0] ?? null)
const warnings = computed(() => (current.value ? deleteWarnings(current.value.assets, keepId.value) : []))

// The mark goes back to the suggested copy when another group comes up (or the marked
// copy left the group on a re-fetch).
watch(current, (g) => {
  if (!g) { keepId.value = ''; return }
  if (!g.assets.some((c) => c.id === keepId.value)) keepId.value = g.assets[0]?.id ?? ''
}, { immediate: true })

let timer: ReturnType<typeof setTimeout> | null = null
let alive = true
// A fetch that started before a group was handled must not bring it back: done() and every
// load() bump seq, only the latest fetch applies.
let seq = 0
let retried = false // a failed first load is tried once more
// polled: the fetch is the background poll, not the user's doing — the group on screen
// then stays as it is (mergeQueue's pinHead).
async function load(polled = false) {
  if (timer) { clearTimeout(timer); timer = null }
  const n = ++seq
  let ok = false
  try {
    const r = await api.duplicates()
    if (!alive || n !== seq) return
    queue.value = mergeQueue(queue.value, r.groups ?? [], polled)
    reclaim.value = r.reclaimBytes
    progress.value = { hashed: r.progress?.hashed ?? 0, total: r.progress?.total ?? 0, scanning: !!r.progress?.scanning }
    loaded.value = true
    error.value = ''
    ok = true
  } catch (e: any) {
    if (!alive || n !== seq) return
    error.value = e?.data?.error || t('common.error_load')
  }
  const wait = nextLoad({ ok, scanning: scanning.value, loaded: loaded.value, retried })
  if (!ok && !loaded.value && wait != null) retried = true
  if (alive && wait != null) timer = setTimeout(() => load(true), wait)
}
onMounted(() => load())

// ---- pictures: the preview of each copy, the thumbnail until it comes (or if it fails) ----
const previews = makeThumbLoader(() => ({}), { concurrency: 3, capacity: 24 })
const thumbs = makeThumbLoader(() => ({}), { concurrency: 4, capacity: 48 })
const tick = ref(0)
previews.onChange = () => { tick.value++ }
thumbs.onChange = () => { tick.value++ }
const previewKey = (c: DupAsset) => `p:${c.id}:${c.thumb}`
watch(queue, (q) => {
  const now = q[0]?.assets ?? []
  const next = q[1]?.assets ?? []
  previews.sync([
    ...now.map((c, i) => ({ key: previewKey(c), url: previewURL(c), priority: i })),
    ...next.map((c, i) => ({ key: previewKey(c), url: previewURL(c), priority: 10 + i })),
  ])
  thumbs.sync(now.map((c, i) => ({ key: thumbKey(c), url: thumbURL(c), priority: i })))
}, { immediate: true })
onBeforeUnmount(() => {
  alive = false
  if (timer) clearTimeout(timer)
  previews.sync([]); previews.clear()
  thumbs.sync([]); thumbs.clear()
})
function picture(c: DupAsset): string | undefined {
  void tick.value
  return previews.get(previewKey(c)) || thumbs.get(thumbKey(c)) || undefined
}

function details(c: DupAsset): string {
  const parts = [c.format, formatBytes(c.bytes)]
  if (c.w && c.h) parts.push(`${c.w}×${c.h}`)
  if (c.kind === 'video' && c.durationMs != null) parts.push(formatDuration(c.durationMs))
  return parts.filter(Boolean).join(' · ')
}
const folder = (c: DupAsset) => `/${c.path}`

// ---- actions ----
function done(g: Group) {
  seq++
  const k = groupKey(g)
  queue.value = queue.value.filter((x) => groupKey(x) !== k)
  handled.value++
}

async function deleteRest() {
  const g = current.value
  if (!g || busy.value) return
  const keep = keepId.value
  const del = g.assets.filter((c) => c.id !== keep).map((c) => c.id)
  busy.value = true
  error.value = ''
  try {
    await api.resolveDuplicates(keep, del)
    done(g)
    emit('resolved', deletedBytes(g.assets, keep))
    await load() // the reclaimable size changed
  } catch (e: any) {
    const msg = e?.data?.error || t('photos.action_failed')
    await load() // a good load clears the error; this one must stay
    error.value = msg
  } finally {
    busy.value = false
  }
}

async function keepAll() {
  const g = current.value
  if (!g || busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api.dismissDuplicates(g.assets.map((c) => c.id))
    done(g)
    await load()
  } catch (e: any) {
    const msg = e?.data?.error || t('photos.action_failed')
    await load()
    error.value = msg
  } finally {
    busy.value = false
  }
}

function skip() {
  if (busy.value) return
  error.value = ''
  queue.value = skipGroup(queue.value, 0)
}
</script>

<template>
  <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3 md:px-6">
    <div class="mb-3 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
      <span v-if="reclaim > 0" class="text-muted">{{ t('photos.dups.reclaim', { size: formatBytes(reclaim) }) }}</span>
      <span v-if="scanning" class="flex items-center gap-1.5 text-muted">
        <Icon name="lucide:loader-circle" size="14" class="animate-spin" />
        {{ t('photos.dups.scanning', { done: progress.hashed, total: progress.total }) }}
      </span>
    </div>
    <p v-if="error" class="mb-2 text-sm text-danger">{{ error }}</p>

    <p v-if="!loaded && !error" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <div v-else-if="loaded && !current && !scanning" class="flex flex-col items-center gap-2 p-10 text-center text-sm text-muted">
      <Icon name="lucide:copy-check" size="28" />
      <p>{{ t('photos.dups.none') }}</p>
    </div>

    <section v-else-if="current" class="mx-auto max-w-5xl">
      <h2 class="mb-3 font-medium">
        {{ t('photos.dups.group_of', { n: handled + 1, m: handled + queue.length }) }}
        <span v-if="current.assets[0]" class="font-normal text-muted"> · {{ takenLabel(current.assets[0], locale) }}</span>
      </h2>

      <div class="flex flex-col gap-3 sm:flex-row sm:flex-wrap">
        <button
          v-for="c in current.assets"
          :key="c.id"
          type="button"
          class="card group flex min-w-0 flex-col overflow-hidden text-left transition sm:basis-64 sm:grow"
          :class="c.id === keepId ? 'ring-2 ring-accent' : 'hover:ring-1 hover:ring-line'"
          :aria-pressed="c.id === keepId"
          :disabled="busy"
          @click="keepId = c.id"
        >
          <div class="relative flex h-72 items-center justify-center bg-panel2 sm:h-80">
            <img v-if="picture(c)" :src="picture(c)" alt="" class="h-full w-full object-contain" draggable="false" />
            <Icon v-else name="lucide:image" size="28" class="text-muted" />
            <span
              v-if="c.id === keepId"
              class="absolute left-2 top-2 flex items-center gap-1 rounded-full bg-accent px-2 py-0.5 text-xs font-medium text-white shadow"
            >
              <Icon name="lucide:check" size="12" /> {{ t('photos.dups.keep') }}
            </span>
            <span class="absolute right-2 top-2 flex gap-1">
              <span v-if="c.live" class="rounded-full bg-black/60 px-2 py-0.5 text-xs text-white">{{ t('photos.kind_live') }}</span>
              <span v-if="c.edited" class="rounded-full bg-black/60 px-2 py-0.5 text-xs text-white">{{ t('photos.dups.edited') }}</span>
            </span>
          </div>
          <div class="space-y-0.5 p-3 text-sm">
            <p class="tabular-nums">{{ details(c) }}</p>
            <p class="truncate font-medium" :title="c.name">{{ c.name }}</p>
            <p class="truncate text-xs text-muted" :title="folder(c)">{{ folder(c) }}</p>
          </div>
        </button>
      </div>

      <div class="mt-4 flex flex-wrap items-center gap-2">
        <button type="button" class="btn-accent" :disabled="busy" @click="deleteRest">
          <Icon :name="busy ? 'lucide:loader-circle' : 'lucide:trash-2'" size="16" :class="busy ? 'animate-spin' : ''" />
          {{ t('photos.dups.delete_rest') }}
        </button>
        <button type="button" class="btn-ghost" :disabled="busy" @click="keepAll">{{ t('photos.dups.keep_all') }}</button>
        <button type="button" class="btn-ghost" :disabled="busy || queue.length < 2" @click="skip">{{ t('photos.dups.skip') }}</button>
      </div>
      <p v-for="w in warnings" :key="w" class="mt-2 flex items-center gap-1.5 text-xs text-warn">
        <Icon name="lucide:triangle-alert" size="14" />
        {{ t(w === 'live' ? 'photos.dups.warn_live' : 'photos.dups.warn_edited') }}
      </p>
    </section>
  </div>
</template>
