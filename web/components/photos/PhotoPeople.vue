<script setup lang="ts">
// «Люди»: the persons found on the user's photos, named ones first, then by how many
// photos they are on. A face opens the person's grid (the page does that through
// ?person=). «Показать скрытых» lists the hidden ones instead, to bring them back.
import type { PhotoPerson } from '~/composables/usePhotos'
import { personLabel } from '~/lib/photos/people'

const emit = defineEmits<{ open: [id: string] }>()
const { t } = useI18n()
const api = usePhotosApi()

const showHidden = ref(false)
const people = ref<PhotoPerson[] | null>(null)
const error = ref('')
const loader = makeThumbLoader()
const tick = ref(0)
loader.onChange = () => { tick.value++ }

async function load() {
  try {
    people.value = await api.people(showHidden.value)
    error.value = ''
    loader.sync((people.value ?? []).filter((p) => p.cover).map((p, i) => ({ key: `face:${p.cover}`, url: faceThumbURL(p.cover!), priority: i })))
  } catch (e: any) {
    error.value = e?.data?.error || t('common.error_load')
    people.value ??= []
  }
}
watch(showHidden, () => { people.value = null; load() })
onMounted(load)
onBeforeUnmount(() => { loader.sync([]); loader.clear() })
defineExpose({ reload: load })

function cover(p: PhotoPerson) {
  void tick.value
  return p.cover ? loader.get(`face:${p.cover}`) : undefined
}
</script>

<template>
  <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3 md:px-6">
    <div class="mb-3 flex items-center gap-3">
      <span v-if="showHidden" class="text-sm font-medium">{{ t('photos.people_hidden_title') }}</span>
      <span v-if="error" class="text-xs text-danger">{{ error }}</span>
    </div>
    <p v-if="!people" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <div v-else-if="!people.length" class="flex flex-col items-center gap-2 p-10 text-center text-sm text-muted">
      <Icon name="lucide:users" size="28" />
      <p>{{ showHidden ? t('photos.people_hidden_empty') : t('photos.people_empty') }}</p>
    </div>
    <div v-else class="grid grid-cols-3 gap-x-3 gap-y-4 sm:grid-cols-4 md:grid-cols-6 xl:grid-cols-8">
      <button v-for="p in people" :key="p.id" type="button" class="group min-w-0 text-center" @click="emit('open', p.id)">
        <div class="mx-auto aspect-square w-full max-w-[9rem] overflow-hidden rounded-full bg-ink/5">
          <img v-if="cover(p)" :src="cover(p)" alt="" class="h-full w-full object-cover transition group-hover:scale-[1.03]" draggable="false" />
          <div v-else class="flex h-full w-full items-center justify-center text-muted">
            <Icon name="lucide:user" size="28" />
          </div>
        </div>
        <div class="mt-1.5 truncate text-sm" :class="p.name ? 'font-medium' : 'text-muted'">{{ personLabel(p, t('photos.person_unnamed')) }}</div>
        <div class="truncate text-xs text-muted">{{ t('photos.map_count', { n: p.count }) }}</div>
      </button>
    </div>
    <div class="mt-6 flex justify-center">
      <button type="button" class="btn-ghost px-3 py-1.5 text-sm" @click="showHidden = !showHidden">
        <Icon :name="showHidden ? 'lucide:arrow-left' : 'lucide:eye-off'" size="16" />
        {{ showHidden ? t('photos.people_back_visible') : t('photos.people_show_hidden') }}
      </button>
    </div>
  </div>
</template>
