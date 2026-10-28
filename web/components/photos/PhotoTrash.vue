<script setup lang="ts">
// «Корзина» of the gallery: the deleted cards, newest deletion first, with «Восстановить».
// Deleting for good happens in the common trash of Files (the link below), where the
// files themselves are.
import type { TrashRow } from '~/composables/usePhotos'
import PhotoTiles from '~/components/photos/PhotoTiles.vue'
import { photosHiddenHeaders } from '~/lib/photos/hidden'

const emit = defineEmits<{ restored: [n: number] }>()
const { t } = useI18n()
const api = usePhotosApi()

const rows = ref<TrashRow[] | null>(null)
const error = ref('')
const busy = ref(false)
const selection = ref<Set<string>>(new Set())
const loader = makeThumbLoader(photosHiddenHeaders)

async function load() {
  error.value = ''
  try {
    rows.value = await api.trash()
  } catch (e: any) {
    error.value = e?.data?.error || t('common.error_load')
  }
}
onMounted(load)
onBeforeUnmount(() => loader.clear())

function toggle(id: string) {
  const next = new Set(selection.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selection.value = next
}

async function restore(ids: string[]) {
  if (busy.value || !ids.length) return
  busy.value = true
  error.value = ''
  try {
    await api.restore(ids)
    const gone = new Set(ids)
    rows.value = (rows.value ?? []).filter((r) => !gone.has(r.id))
    selection.value = new Set()
    emit('restored', ids.length)
  } catch (e: any) {
    error.value = e?.data?.error || t('photos.action_failed')
    await load()
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="relative flex min-h-0 flex-1 flex-col">
    <div class="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2 text-sm md:px-6">
      <span class="text-muted">{{ t('photos.trash_hint') }}</span>
      <NuxtLink to="/trash" class="text-accent hover:underline">{{ t('photos.trash_open_files') }} →</NuxtLink>
    </div>
    <div class="relative min-h-0 flex-1 overflow-auto px-4 pb-20 md:px-6">
      <p v-if="error" class="mb-2 text-sm text-danger">{{ error }}</p>
      <p v-if="!rows && !error" class="text-sm text-muted">{{ t('common.loading') }}</p>
      <div v-else-if="rows && !rows.length" class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm text-muted">
        <Icon name="lucide:trash" size="28" />
        <p>{{ t('photos.trash_empty') }}</p>
      </div>
      <PhotoTiles v-else-if="rows" v-model:selection="selection" :rows="rows" :loader="loader" selectable @open="toggle" />
    </div>
    <div v-if="selection.size" class="pointer-events-none absolute inset-x-0 bottom-3 z-20 flex justify-center px-2">
      <div class="card pointer-events-auto flex items-center gap-1 px-1.5 py-1 shadow-xl">
        <button type="button" class="btn-ghost px-2 py-1.5" :title="t('photos.clear_selection')" @click="selection = new Set()">
          <Icon name="lucide:x" size="16" />
        </button>
        <span class="whitespace-nowrap px-1 text-sm font-medium tabular-nums">{{ t('photos.selected', { n: selection.size }) }}</span>
        <button type="button" class="btn-accent px-2 py-1.5" :disabled="busy" @click="restore([...selection])">
          <Icon :name="busy ? 'lucide:loader-circle' : 'lucide:undo-2'" size="16" :class="busy ? 'animate-spin' : ''" />
          {{ t('photos.restore') }}
        </button>
      </div>
    </div>
  </div>
</template>
