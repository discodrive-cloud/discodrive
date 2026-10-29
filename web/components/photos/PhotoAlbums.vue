<script setup lang="ts">
// «Альбомы»: the user's albums and the ones they were added to, newest change first. A
// card opens the album's grid (the page does that through ?album=).
import type { PhotoAlbum } from '~/composables/usePhotos'

const emit = defineEmits<{ open: [id: string] }>()
const { t } = useI18n()
const api = usePhotosApi()
const { prompt } = useDialog()

const albums = ref<PhotoAlbum[] | null>(null)
const error = ref('')
const loader = makeThumbLoader()
const tick = ref(0)
loader.onChange = () => { tick.value++ }

async function load() {
  try {
    albums.value = await api.albums()
    error.value = ''
    loader.sync((albums.value ?? []).filter((a) => a.cover).map((a, i) => ({ key: thumbKey(a.cover!), url: thumbURL(a.cover!), priority: i })))
  } catch (e: any) {
    error.value = e?.data?.error || t('common.error_load')
    albums.value ??= []
  }
}
onMounted(load)
onBeforeUnmount(() => { loader.sync([]); loader.clear() })
defineExpose({ reload: load })

function cover(a: PhotoAlbum) {
  void tick.value
  return a.cover ? loader.get(thumbKey(a.cover)) : undefined
}

async function create() {
  const name = (await prompt(t('photos.album_new_name'), '', { confirmText: t('photos.album_create') }))?.trim()
  if (!name) return
  try {
    const r = await api.createAlbum(name)
    emit('open', r.id)
  } catch (e: any) {
    error.value = e?.data?.error || t('photos.action_failed')
  }
}
</script>

<template>
  <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3 md:px-6">
    <div class="mb-3 flex items-center gap-3">
      <button type="button" class="btn-accent" @click="create">
        <Icon name="lucide:plus" size="16" /> {{ t('photos.album_new') }}
      </button>
      <span v-if="error" class="text-xs text-danger">{{ error }}</span>
    </div>
    <p v-if="!albums" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <div v-else-if="!albums.length" class="flex flex-col items-center gap-2 p-10 text-center text-sm text-muted">
      <Icon name="lucide:book-image" size="28" />
      <p>{{ t('photos.albums_empty') }}</p>
    </div>
    <div v-else class="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-6">
      <button v-for="a in albums" :key="a.id" type="button" class="group text-left" @click="emit('open', a.id)">
        <div class="relative aspect-square overflow-hidden rounded-lg bg-ink/5">
          <img v-if="cover(a)" :src="cover(a)" alt="" class="h-full w-full object-cover transition group-hover:scale-[1.02]" draggable="false" />
          <div v-else class="flex h-full w-full items-center justify-center text-muted">
            <Icon name="lucide:image" size="28" />
          </div>
          <span v-if="!a.own || a.members" class="absolute right-1.5 top-1.5 rounded-full bg-black/60 p-1 text-white" :title="t('photos.album_shared')">
            <Icon name="lucide:users" size="12" />
          </span>
        </div>
        <div class="mt-1.5 truncate text-sm font-medium">{{ a.name }}</div>
        <div class="truncate text-xs text-muted">
          {{ t('photos.map_count', { n: a.count }) }}<template v-if="!a.own"> · {{ a.ownerEmail }}</template>
        </div>
      </button>
    </div>
  </div>
</template>
