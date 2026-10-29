<script setup lang="ts">
// «В альбом»: the selection (or the viewer's card) into one of the user's albums — their
// own or one they were added to — or into a new one.
import type { PhotoAlbum } from '~/composables/usePhotos'

const props = defineProps<{ ids: string[] }>()
const emit = defineEmits<{ close: []; done: [albumId: string, name: string, added: number] }>()
const { t } = useI18n()
const api = usePhotosApi()

const albums = ref<PhotoAlbum[] | null>(null)
const error = ref('')
const busy = ref(false)
const name = ref('')

onMounted(async () => {
  try { albums.value = await api.albums() } catch (e: any) {
    albums.value = []
    error.value = e?.data?.error || t('common.error_load')
  }
})

async function run(fn: () => Promise<void>) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try { await fn() } catch (e: any) {
    error.value = e?.data?.error || t('photos.action_failed')
  } finally { busy.value = false }
}

function pick(a: PhotoAlbum) {
  return run(async () => {
    const added = await api.addToAlbum(a.id, props.ids)
    emit('done', a.id, a.name, added)
  })
}

function create() {
  const n = name.value.trim()
  if (!n) return
  return run(async () => {
    const r = await api.createAlbum(n, props.ids)
    emit('done', r.id, r.name, r.added)
  })
}
</script>

<template>
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-4" @click.self="emit('close')">
    <div class="card flex max-h-[80vh] w-full max-w-sm flex-col p-5" role="dialog" aria-modal="true" aria-labelledby="photo-album-title">
      <h2 id="photo-album-title" class="mb-1 font-semibold">{{ t('photos.add_to_album') }}</h2>
      <p class="mb-3 text-sm text-muted">{{ t('photos.selected', { n: ids.length }) }}</p>

      <form class="mb-3 flex gap-2" @submit.prevent="create">
        <input v-model="name" class="input flex-1" maxlength="200" :placeholder="t('photos.album_new_name')" :aria-label="t('photos.album_new_name')" />
        <button type="submit" class="btn-accent" :disabled="busy || !name.trim()">{{ t('photos.album_create') }}</button>
      </form>

      <div class="min-h-0 flex-1 overflow-y-auto">
        <p v-if="!albums" class="text-sm text-muted">{{ t('common.loading') }}</p>
        <p v-else-if="!albums.length && !error" class="text-sm text-muted">{{ t('photos.albums_none') }}</p>
        <button
          v-for="a in albums ?? []"
          :key="a.id"
          type="button"
          class="flex w-full items-center justify-between gap-2 rounded-md px-2 py-2 text-left text-sm hover:bg-ink/5"
          :disabled="busy"
          @click="pick(a)"
        >
          <span class="min-w-0 truncate">{{ a.name }}</span>
          <span class="shrink-0 text-xs text-muted">
            <Icon v-if="!a.own || a.members" name="lucide:users" size="12" class="mr-1 align-[-2px]" />{{ a.count }}
          </span>
        </button>
      </div>

      <p v-if="error" class="mt-2 text-sm text-danger">{{ error }}</p>
      <div class="mt-4 flex justify-end">
        <button type="button" class="btn-ghost" @click="emit('close')">{{ t('common.cancel') }}</button>
      </div>
    </div>
  </div>
</template>
