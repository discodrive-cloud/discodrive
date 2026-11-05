<script setup lang="ts">
// «Объединить…»: other people the recognition split off the same person. The picked
// ones join the open person (their photos and faces move over, they are gone afterwards).
import type { PhotoPerson } from '~/composables/usePhotos'
import { personLabel } from '~/lib/photos/people'

const props = defineProps<{ person: PhotoPerson }>()
const emit = defineEmits<{ close: []; done: [merged: PhotoPerson] }>()
const { t } = useI18n()
const api = usePhotosApi()

const people = ref<PhotoPerson[] | null>(null)
const picked = ref<Set<string>>(new Set())
const error = ref('')
const busy = ref(false)
const loader = makeThumbLoader()
const tick = ref(0)
loader.onChange = () => { tick.value++ }

onMounted(async () => {
  try {
    people.value = (await api.people()).filter((p) => p.id !== props.person.id)
    loader.sync(people.value.filter((p) => p.cover).map((p, i) => ({ key: `face:${p.cover}`, url: faceThumbURL(p.cover!), priority: i })))
  } catch (e: any) {
    people.value = []
    error.value = e?.data?.error || t('common.error_load')
  }
})
onBeforeUnmount(() => { loader.sync([]); loader.clear() })

function cover(p: PhotoPerson) {
  void tick.value
  return p.cover ? loader.get(`face:${p.cover}`) : undefined
}
function toggle(id: string) {
  const s = new Set(picked.value)
  if (s.has(id)) s.delete(id)
  else s.add(id)
  picked.value = s
}

async function merge() {
  if (busy.value || !picked.value.size) return
  busy.value = true
  error.value = ''
  try {
    // In the order shown: a person without a name takes the first name among the picked.
    const ids = (people.value ?? []).filter((p) => picked.value.has(p.id)).map((p) => p.id)
    emit('done', await api.mergePeople(props.person.id, ids))
  } catch (e: any) {
    error.value = e?.data?.error || t('photos.action_failed')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-4" @click.self="emit('close')">
    <div class="card flex max-h-[80vh] w-full max-w-lg flex-col p-5" role="dialog" aria-modal="true" aria-labelledby="merge-people-title">
      <h2 id="merge-people-title" class="mb-1 font-semibold">{{ t('photos.people_merge_title', { name: personLabel(person, t('photos.person_unnamed')) }) }}</h2>
      <p class="mb-3 text-sm text-muted">{{ t('photos.people_merge_hint') }}</p>
      <div class="min-h-0 flex-1 overflow-y-auto">
        <p v-if="!people" class="text-sm text-muted">{{ t('common.loading') }}</p>
        <p v-else-if="!people.length && !error" class="text-sm text-muted">{{ t('photos.people_merge_none') }}</p>
        <div v-else class="grid grid-cols-4 gap-3 sm:grid-cols-5">
          <button
            v-for="p in people"
            :key="p.id"
            type="button"
            class="min-w-0 text-center"
            :aria-pressed="picked.has(p.id)"
            @click="toggle(p.id)"
          >
            <div class="relative mx-auto aspect-square w-full overflow-hidden rounded-full bg-ink/5 ring-2 ring-offset-2 ring-offset-panel transition" :class="picked.has(p.id) ? 'ring-accent' : 'ring-transparent'">
              <img v-if="cover(p)" :src="cover(p)" alt="" class="h-full w-full object-cover" draggable="false" />
              <div v-else class="flex h-full w-full items-center justify-center text-muted"><Icon name="lucide:user" size="20" /></div>
              <span v-if="picked.has(p.id)" class="absolute bottom-0.5 right-0.5 rounded-full bg-accent p-0.5 text-white">
                <Icon name="lucide:check" size="12" />
              </span>
            </div>
            <div class="mt-1 truncate text-xs" :class="p.name ? '' : 'text-muted'">{{ personLabel(p, t('photos.person_unnamed')) }}</div>
          </button>
        </div>
      </div>
      <p v-if="error" class="mt-2 text-sm text-danger">{{ error }}</p>
      <div class="mt-4 flex justify-end gap-2">
        <button type="button" class="btn-ghost" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button type="button" class="btn-accent" :disabled="busy || !picked.size" @click="merge">
          {{ t('photos.people_merge_do', { n: picked.size }) }}
        </button>
      </div>
    </div>
  </div>
</template>
