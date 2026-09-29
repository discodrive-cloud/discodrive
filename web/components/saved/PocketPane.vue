<script setup lang="ts">
import { isHttpUrl } from '~/lib/safeUrl'
// Pocket: the read-later article list. Articles are fetched server-side into
// markdown and read at /saved/{id}; processing state polls until idle.
interface SavedItem {
  id: string
  url: string
  kind: string
  title: string
  status: string
  error?: string
  has_content: boolean
  created_at: string
}

const { t } = useI18n()
const { request } = useApi()
const { confirm } = useDialog()

const list = ref<SavedItem[]>([])
const error = ref('')
const busy = ref(false)
const searchQuery = ref('')

function buildQuery() {
  const p = new URLSearchParams({ kind: 'article' })
  if (searchQuery.value.trim()) p.set('q', searchQuery.value.trim())
  return `?${p.toString()}`
}

async function load(background = false) {
  if (!background) {
    error.value = ''
    busy.value = true
  }
  try {
    list.value = await request<SavedItem[]>(`/me/saved${buildQuery()}`)
  } catch (e: any) {
    if (!background) error.value = e?.data?.error || t('saved.error_load')
  } finally {
    busy.value = false
  }
}
onMounted(() => load())

// Debounce search input (same pattern as the books library).
let searchTimer: ReturnType<typeof setTimeout> | null = null
function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => load(), 350)
}

// Poll while anything is pending/processing; stop as soon as the queue is idle.
const hasActive = computed(() => list.value.some((i) => i.status === 'pending' || i.status === 'processing'))
let pollTimer: ReturnType<typeof setInterval> | null = null
watch(hasActive, (active) => {
  if (active && !pollTimer) {
    pollTimer = setInterval(() => load(true), 2500)
  } else if (!active && pollTimer) {
    clearInterval(pollTimer)
    pollTimer = null
  }
}, { immediate: true })
onBeforeUnmount(() => { if (pollTimer) clearInterval(pollTimer) })

function subtitle(item: SavedItem): string {
  if (item.status === 'error') return item.error || t('saved.status_error')
  try { return new URL(item.url).hostname } catch { return item.url }
}

function statusBadge(item: SavedItem): { label: string; cls: string } | null {
  switch (item.status) {
    case 'pending': return { label: t('saved.status_pending'), cls: 'bg-ink/10 text-muted' }
    case 'processing': return { label: t('saved.status_processing'), cls: 'bg-accent/15 text-accent' }
    case 'error': return { label: t('saved.status_error'), cls: 'bg-danger/15 text-danger' }
    default: return null
  }
}

function open(item: SavedItem) {
  if (item.has_content) {
    navigateTo(`/saved/${item.id}`)
  } else if (isHttpUrl(item.url)) {
    window.open(item.url, '_blank', 'noopener')
  }
}

async function retry(item: SavedItem) {
  error.value = ''
  try {
    await request(`/me/saved/${item.id}/retry`, { method: 'POST' })
    await load(true)
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.error_retry')
  }
}

async function remove(item: SavedItem) {
  if (!(await confirm(t('saved.confirm_delete'), { message: item.title || item.url, confirmText: t('common.delete'), danger: true }))) return
  error.value = ''
  try {
    await request(`/me/saved/${item.id}`, { method: 'DELETE' })
    list.value = list.value.filter((i) => i.id !== item.id)
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.error_delete')
  }
}
</script>

<template>
  <div>
    <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <div class="mb-3">
      <input
        v-model="searchQuery"
        type="search"
        class="input"
        :placeholder="t('saved.search_ph')"
        @input="onSearchInput"
      />
    </div>

    <div class="card overflow-hidden">
      <div v-if="busy && !list.length" class="p-5 text-sm text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="!list.length" class="p-10 text-center text-sm text-muted">
        <Icon name="lucide:newspaper" size="28" class="mx-auto mb-2 block opacity-50" />
        {{ t('saved.empty') }}
      </div>
      <ul v-else>
        <li
          v-for="item in list"
          :key="item.id"
          class="flex items-center gap-3 border-b border-line/50 px-4 py-3 last:border-0 hover:bg-ink/5"
        >
          <Icon name="lucide:newspaper" size="18" class="shrink-0 text-muted" />
          <div class="min-w-0 flex-1 cursor-pointer" @click="open(item)">
            <div class="flex items-center gap-2">
              <span class="truncate text-sm">{{ item.title || item.url }}</span>
              <span
                v-if="statusBadge(item)"
                class="shrink-0 rounded-full px-2 py-0.5 text-xs"
                :class="statusBadge(item)!.cls"
              >{{ statusBadge(item)!.label }}</span>
            </div>
            <div class="truncate text-xs" :class="item.status === 'error' ? 'text-danger' : 'text-muted'">
              {{ subtitle(item) }}
            </div>
          </div>
          <div class="flex shrink-0 items-center gap-1">
            <a
              v-if="isHttpUrl(item.url)"
              class="btn-ghost px-1.5 py-1"
              :href="item.url"
              target="_blank"
              rel="noopener"
              :title="t('saved.open_original')"
            ><Icon name="lucide:external-link" size="15" /></a>
            <button
              v-if="item.status === 'error' || item.status === 'done'"
              class="btn-ghost px-1.5 py-1"
              :title="t('saved.retry')"
              @click="retry(item)"
            ><Icon name="lucide:rotate-cw" size="15" /></button>
            <button
              class="btn-ghost px-1.5 py-1"
              :title="t('common.delete')"
              @click="remove(item)"
            ><Icon name="lucide:trash-2" size="15" /></button>
          </div>
        </li>
      </ul>
    </div>
  </div>
</template>
