<script setup lang="ts">
// «Фото» settings: the switch, the source folders and the indexing progress.
import type { PhotoFailedFile, PhotoSettings } from '~/composables/usePhotos'
import { indexingPercent, indexingRunning, photosNavVisible } from '~/lib/photos/settings'

const { t } = useI18n()
const api = usePhotosApi()
const photosEnabled = usePhotosEnabled()

const settings = ref<PhotoSettings | null>(null)
const busy = ref(false)
const error = ref('')
const pickerOpen = ref(false)
const showFailed = ref(false)

async function load() {
  try {
    settings.value = await api.settings()
    photosEnabled.value = photosNavVisible(settings.value)
  } catch (e: any) {
    error.value = e?.data?.error || t('common.error_load')
  }
}

async function save(enabled: boolean, sourceIds: string[]) {
  error.value = ''
  busy.value = true
  try {
    settings.value = await api.saveSettings(enabled, sourceIds)
    photosEnabled.value = photosNavVisible(settings.value)
    // The pass starts in the background: keep looking for a while even when the rows
    // already there are all done (a second source added to a finished first one).
    waitingForRows.value = 8 // ~30 s
    if (showFailed.value) loadFailed(true)
  } catch (e: any) {
    error.value = e?.data?.error === 'photos_unavailable' ? t('settings.photos_unavailable') : (e?.data?.error || t('common.error_save'))
  } finally {
    busy.value = false
  }
}

const sourceIds = computed(() => settings.value?.sources.map((s) => s.id) ?? [])

async function toggle(e: Event) {
  const box = e.target as HTMLInputElement
  await save(box.checked, sourceIds.value)
  // A failed save leaves settings.enabled unchanged, so :checked would not re-render.
  box.checked = settings.value?.enabled ?? false
}
function addSource(folder: { id: string; name: string } | null) {
  if (!folder || !settings.value || sourceIds.value.includes(folder.id)) return
  save(settings.value.enabled, [...sourceIds.value, folder.id])
}
function removeSource(id: string) {
  if (!settings.value) return
  save(settings.value.enabled, sourceIds.value.filter((x) => x !== id))
}

// Right after a change the server may not have started its pass yet: keep looking for a while.
const waitingForRows = ref(0)
const running = computed(() => {
  if (!settings.value?.enabled || !settings.value.sources.length) return false
  return indexingRunning(settings.value.indexing, waitingForRows.value)
})
const percent = computed(() => indexingPercent(settings.value?.indexing))

// The files that could not be processed, loaded when the list is opened.
const failedFiles = ref<PhotoFailedFile[]>([])
const failedMore = ref(false)
const failedLoading = ref(false)
async function loadFailed(reset = false) {
  if (failedLoading.value) return
  failedLoading.value = true
  try {
    const r = await api.failed(reset ? 0 : failedFiles.value.length)
    failedFiles.value = reset ? r.items : [...failedFiles.value, ...r.items]
    failedMore.value = r.more
  } catch (e: any) {
    error.value = e?.data?.error || t('common.error_load')
  } finally {
    failedLoading.value = false
  }
}
function toggleFailed() {
  showFailed.value = !showFailed.value
  if (showFailed.value) loadFailed(true)
}

// Follow the progress while the index fills.
let timer: ReturnType<typeof setInterval> | null = null
watch(running, (on) => {
  if (on && !timer) {
    timer = setInterval(() => {
      if (waitingForRows.value > 0) waitingForRows.value--
      if (!document.hidden) load()
    }, 4000)
  }
  if (!on && timer) { clearInterval(timer); timer = null }
})
onMounted(load)
onBeforeUnmount(() => { if (timer) clearInterval(timer) })
</script>

<template>
  <div class="card p-5">
    <h2 class="mb-3 text-sm font-medium text-muted">{{ t('settings.photos_section') }}</h2>
    <p class="mb-4 text-xs text-muted">{{ t('settings.photos_note') }}</p>

    <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <!-- Enable toggle -->
    <label class="mb-1 flex items-center gap-3 text-sm" :class="settings && !settings.available ? 'opacity-60' : ''">
      <input
        type="checkbox"
        class="h-5 w-5"
        :checked="settings?.enabled ?? false"
        :disabled="busy || !settings || (!settings.available && !settings.enabled)"
        @change="toggle"
      />
      <span>{{ t('settings.photos_enabled') }}</span>
      <Icon v-if="busy" name="lucide:loader-circle" class="animate-spin text-muted" size="16" />
    </label>
    <p v-if="settings && !settings.available" class="mb-4 ml-8 text-xs text-warn">{{ t('settings.photos_unavailable') }}</p>
    <div v-else class="mb-4" />

    <!-- Source folders -->
    <div class="mb-1 text-xs font-medium text-muted">{{ t('settings.photos_sources') }}</div>
    <p class="mb-2 text-xs text-muted">{{ t('settings.photos_sources_hint') }}</p>
    <ul v-if="settings?.sources.length" class="mb-3 space-y-2">
      <li
        v-for="s in settings.sources"
        :key="s.id"
        class="flex items-center gap-2 rounded-md border border-line bg-panel2 px-3 py-2 text-sm"
      >
        <Icon name="lucide:folder" class="shrink-0 text-accent" size="16" />
        <span class="min-w-0 flex-1 truncate font-mono text-xs" :title="s.path">{{ s.path || s.name }}</span>
        <button
          class="btn-ghost shrink-0 px-2 py-1"
          :title="t('settings.photos_remove_source')"
          :aria-label="t('settings.photos_remove_source')"
          :disabled="busy"
          @click="removeSource(s.id)"
        >
          <Icon name="lucide:x" size="14" />
        </button>
      </li>
    </ul>
    <p v-else-if="settings" class="mb-3 text-sm text-muted">{{ t('settings.photos_no_sources') }}</p>
    <button class="btn-accent mb-5" :disabled="busy || !settings || !settings.available" @click="pickerOpen = true">
      <Icon name="lucide:folder-plus" size="16" /> {{ t('settings.photos_add_source') }}
    </button>

    <!-- Indexing -->
    <template v-if="settings?.enabled && settings.sources.length">
      <div class="mb-1 text-xs font-medium text-muted">{{ t('settings.photos_indexing') }}</div>
      <div class="mb-1 h-1.5 w-full overflow-hidden rounded bg-ink/10">
        <div class="h-full rounded bg-accent transition-all" :style="{ width: `${percent}%` }" />
      </div>
      <p class="text-xs text-muted">
        {{ t('settings.photos_progress', { ready: settings.indexing.ready, total: settings.indexing.total }) }}
        <span v-if="settings.indexing.pending"> · {{ t('settings.photos_pending', { n: settings.indexing.pending }) }}</span>
        <span v-if="running"> · {{ t('settings.photos_in_progress') }}</span>
      </p>
      <div v-if="settings.indexing.failedFiles" class="mt-2 text-xs">
        <button class="flex items-center gap-1 text-warn hover:underline" :aria-expanded="showFailed" @click="toggleFailed">
          <Icon :name="showFailed ? 'lucide:chevron-down' : 'lucide:chevron-right'" size="14" />
          {{ t('settings.photos_failed', { n: settings.indexing.failedFiles }) }}
        </button>
        <div v-if="showFailed" class="ml-5 mt-1">
          <p class="mb-2 text-muted">{{ t('settings.photos_failed_hint') }}</p>
          <ul class="max-h-64 space-y-1 overflow-y-auto">
            <li v-for="f in failedFiles" :key="f.id" class="min-w-0 rounded-md border border-line bg-panel2 px-2 py-1">
              <div class="truncate font-mono" :title="`${f.folder}/${f.name}`">
                <span class="text-muted">{{ f.folder === '/' ? '/' : `${f.folder}/` }}</span>{{ f.name }}
              </div>
              <div class="truncate text-muted" :title="f.error">{{ f.error }}</div>
            </li>
          </ul>
          <button v-if="failedMore" class="btn-ghost mt-2 px-2 py-1" :disabled="failedLoading" @click="loadFailed()">
            {{ t('settings.photos_failed_more') }}
          </button>
          <Icon v-if="failedLoading" name="lucide:loader-circle" class="ml-1 animate-spin text-muted" size="14" />
        </div>
      </div>
    </template>

    <MusicFolderPicker
      v-if="pickerOpen"
      :model-value="null"
      @update:model-value="addSource"
      @close="pickerOpen = false"
    />
  </div>
</template>
