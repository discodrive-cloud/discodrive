<script setup lang="ts">
// An album opened by link: no account, only the album — its grid and the viewer, with a
// password first if the owner set one, and downloads if the owner allowed them. Every
// request goes to /public/photos/v1/{token}; the gallery's own API is never called.
import PhotoGrid from '~/components/photos/PhotoGrid.vue'
import PhotoViewer from '~/components/photos/PhotoViewer.vue'
import type { Bucket, PhotoAsset, PhotoRow, PhotoViewSource, PlayInfo } from '~/composables/usePhotos'
import { ThumbLoader, ThumbError, type ThumbLoaderOptions } from '~/lib/photos/thumbLoader'
import { monthsOf, publicAssetURL } from '~/lib/photos/publicAlbum'
import { wallDate } from '~/lib/photos/edit'

definePageMeta({ layout: false })

const { t } = useI18n()
const route = useRoute()
const token = computed(() => String(route.params.token || ''))
const base = computed(() => `/public/photos/v1/${encodeURIComponent(token.value)}`)

interface Meta { name: string; locked: boolean; allowDownload: boolean; count?: number }
const meta = ref<Meta | null>(null)
const state = ref<'loading' | 'missing' | 'error' | 'ready'>('loading')
const password = ref('')
const wrong = ref(false)
const unlocking = ref(false)

// The key a password gave, kept for the tab's life (a reload does not ask again).
const keyName = computed(() => `album-key:${token.value}`)
const key = ref('')
function readKey() {
  try { key.value = sessionStorage.getItem(keyName.value) ?? '' } catch { key.value = '' }
}
function withKey(path: string) {
  return key.value ? `${path}${path.includes('?') ? '&' : '?'}k=${encodeURIComponent(key.value)}` : path
}

async function get<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(withKey(base.value + path), init)
  if (!res.ok) throw Object.assign(new Error(String(res.status)), { status: res.status })
  return res.json() as Promise<T>
}

const rows = ref<PhotoRow[]>([])
const buckets = ref<Bucket[]>([])
let byMonth = new Map<string, PhotoRow[]>()

async function load() {
  state.value = 'loading'
  try {
    meta.value = await get<Meta>('')
    useHead({ title: meta.value.name })
    if (meta.value.locked) { state.value = 'ready'; return }
    const all: PhotoRow[] = []
    let after = ''
    for (;;) {
      const page = await get<{ items: PhotoRow[]; next: string | null }>(`/items${after ? `?after=${encodeURIComponent(after)}` : ''}`)
      all.push(...page.items)
      if (!page.next) break
      after = page.next
    }
    rows.value = all
    const m = monthsOf(all, (r) => wallDate(r.takenAt, r.tzOffsetMin))
    buckets.value = m.buckets
    byMonth = m.rows
    state.value = 'ready'
  } catch (e: any) {
    if (e?.status === 401) {
      // The key ran out (or the link got a password since): ask again.
      key.value = ''
      try { sessionStorage.removeItem(keyName.value) } catch {}
      meta.value = meta.value ? { ...meta.value, locked: true } : null
      state.value = meta.value ? 'ready' : 'error'
      return
    }
    state.value = e?.status === 404 ? 'missing' : 'error'
  }
}

async function unlock() {
  if (!password.value || unlocking.value) return
  unlocking.value = true
  wrong.value = false
  try {
    const res = await fetch(`${base.value}/unlock`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ password: password.value }),
    })
    if (res.status === 401) { wrong.value = true; return }
    if (!res.ok) throw new Error(String(res.status))
    key.value = (await res.json()).key ?? ''
    try { sessionStorage.setItem(keyName.value, key.value) } catch {}
    password.value = ''
    await load()
  } catch {
    state.value = 'error'
  } finally {
    unlocking.value = false
  }
}

// Pictures: the gallery's addresses turned into the link's.
function makeLoader(opts: ThumbLoaderOptions = {}) {
  return new ThumbLoader(async (url, signal) => {
    const to = publicAssetURL(url, token.value, key.value)
    if (!to) throw new ThumbError(404)
    const res = await fetch(to, { signal })
    if (!res.ok) throw new ThumbError(res.status)
    return res.blob()
  }, opts)
}
const loader = makeLoader()

const source: PhotoViewSource = {
  async asset(id: string) {
    const r = await get<{ row: PhotoRow; liveVideoId: string | null }>(`/assets/${id}`)
    // The viewer reads only what a link shows; the rest stays empty.
    return {
      ...r.row, liveVideoId: r.liveVideoId, originalId: null, dateSource: '', videoCodec: null, isHdr: false,
      country: null, lat: null, lon: null, hidden: false, copies: 0, owner: null,
      camera: { make: null, model: null, lens: null, fNumber: null, focalMm: null, exposure: null, iso: null },
      file: { name: '', size: null },
    } satisfies PhotoAsset
  },
  play: (id: string, hevc: boolean) => get<PlayInfo>(`/assets/${id}/play`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ hevc }),
  }),
  loader: makeLoader,
}

const fetchMonth = (month: string) => Promise.resolve(byMonth.get(month) ?? [])

const viewer = shallowRef<{ id: string; month: string } | null>(null)
function open(id: string, _ids: string[], month: string) {
  viewer.value = { id, month }
}
function download(id: string) {
  const a = document.createElement('a')
  a.href = withKey(`${base.value}/assets/${id}/original`)
  a.click()
}

onMounted(() => { readKey(); load() })
onBeforeUnmount(() => loader.clear())
</script>

<template>
  <div class="flex h-dvh flex-col text-ink">
    <header class="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-line px-4 py-3 md:px-6">
      <h1 class="min-w-0 truncate text-xl font-semibold">{{ meta?.name || t('photos.public_title') }}</h1>
      <span v-if="state === 'ready' && meta && !meta.locked" class="text-sm text-muted">{{ t('photos.map_count', { n: rows.length }) }}</span>
      <span class="ml-auto text-xs text-muted">DiscoDrive</span>
    </header>

    <div v-if="state === 'loading'" class="p-6 text-sm text-muted">{{ t('common.loading') }}</div>
    <div v-else-if="state === 'missing'" class="flex flex-1 flex-col items-center justify-center gap-2 p-6 text-center text-sm text-muted">
      <Icon name="lucide:link-2-off" size="28" />
      <p>{{ t('photos.public_missing') }}</p>
    </div>
    <div v-else-if="state === 'error'" class="p-6 text-sm text-danger">{{ t('common.error_load') }}</div>

    <div v-else-if="meta?.locked" class="flex flex-1 items-center justify-center p-4">
      <form class="card w-full max-w-sm p-5" @submit.prevent="unlock">
        <Icon name="lucide:lock" size="22" class="mb-2 text-muted" />
        <p class="mb-3 text-sm">{{ t('photos.public_password') }}</p>
        <input v-model="password" type="password" autocomplete="current-password" class="input mb-2" :aria-label="t('photos.link_password')" autofocus />
        <p v-if="wrong" class="mb-2 text-sm text-danger">{{ t('photos.public_wrong') }}</p>
        <button type="submit" class="btn-accent w-full justify-center" :disabled="unlocking || !password">{{ t('photos.public_open') }}</button>
      </form>
    </div>

    <div v-else-if="!rows.length" class="flex flex-1 items-center justify-center p-6 text-sm text-muted">{{ t('photos.album_empty') }}</div>
    <div v-else class="relative min-h-0 flex-1 pl-2 md:pl-4">
      <PhotoGrid :buckets="buckets" :fetch-month="fetchMonth" :loader="loader" :selectable="false" columns-key="photos.public.columns" @open="open" />
    </div>

    <PhotoViewer
      v-if="viewer"
      :start="viewer"
      :months="buckets.map((b) => b.month)"
      :fetch-month="fetchMonth"
      :initial-rows="byMonth"
      :source="source"
      :can-download="!!meta?.allowDownload"
      public-mode
      @close="viewer = null"
      @download="download"
    />
  </div>
</template>
