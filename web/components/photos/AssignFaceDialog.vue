<script setup lang="ts">
// «Кто это?»: a face recognition did not match (or matched wrong) goes to one of the
// user's people, or to a new person of the typed name.
import type { PhotoPerson } from '~/composables/usePhotos'
import { findSameName, personLabel } from '~/lib/photos/people'

const props = defineProps<{ faceId: string }>()
const emit = defineEmits<{ close: []; done: [person: PhotoPerson] }>()
const { t } = useI18n()
const api = usePhotosApi()

const people = ref<PhotoPerson[] | null>(null)
const query = ref('')
const busy = ref(false)
const error = ref('')
const input = ref<HTMLInputElement | null>(null)
const loader = makeThumbLoader()
const tick = ref(0)
loader.onChange = () => { tick.value++ }

onMounted(async () => {
  input.value?.focus()
  loader.sync([{ key: `face:${props.faceId}`, url: faceThumbURL(props.faceId), priority: 0 }])
  try {
    people.value = await api.people()
    loader.sync([
      { key: `face:${props.faceId}`, url: faceThumbURL(props.faceId), priority: 0 },
      ...people.value.filter((p) => p.cover).map((p, i) => ({ key: `face:${p.cover}`, url: faceThumbURL(p.cover!), priority: i + 1 })),
    ])
  } catch (e: any) {
    people.value = []
    error.value = e?.data?.error || t('common.error_load')
  }
})
onBeforeUnmount(() => { loader.sync([]); loader.clear() })

function img(faceId: string | null) {
  void tick.value
  return faceId ? loader.get(`face:${faceId}`) : undefined
}

// Named people matching what is typed first; unnamed ones only while nothing is typed.
const shown = computed(() => {
  const q = query.value.trim().toLocaleLowerCase()
  const list = people.value ?? []
  if (!q) return list
  return list.filter((p) => p.name.toLocaleLowerCase().includes(q))
})
const newName = computed(() => {
  const n = query.value.trim()
  return n && !findSameName(people.value ?? [], n, '') ? n : ''
})

async function assign(body: { personId?: string; name?: string }) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    emit('done', await api.assignFace(props.faceId, body))
  } catch (e: any) {
    error.value = e?.data?.error || t('photos.action_failed')
  } finally {
    busy.value = false
  }
}
function submit() {
  const same = findSameName(people.value ?? [], query.value, '')
  if (same) return assign({ personId: same.id })
  if (newName.value) return assign({ name: newName.value })
}
</script>

<template>
  <div class="fixed inset-0 z-[70] flex items-center justify-center bg-black/50 p-4" @click.self="emit('close')">
    <div class="card flex max-h-[80vh] w-full max-w-md flex-col p-5" role="dialog" aria-modal="true" aria-labelledby="assign-face-title">
      <div class="mb-3 flex items-center gap-3">
        <div class="h-14 w-14 shrink-0 overflow-hidden rounded-full bg-ink/5">
          <img v-if="img(faceId)" :src="img(faceId)" alt="" class="h-full w-full object-cover" draggable="false" />
        </div>
        <div>
          <h2 id="assign-face-title" class="font-semibold">{{ t('photos.face_who') }}</h2>
          <p class="text-sm text-muted">{{ t('photos.face_assign_hint') }}</p>
        </div>
      </div>
      <form class="mb-3" @submit.prevent="submit">
        <input ref="input" v-model="query" class="input w-full" maxlength="100" :placeholder="t('photos.face_name_placeholder')" :aria-label="t('photos.face_name_placeholder')" />
      </form>
      <div class="min-h-0 flex-1 overflow-y-auto">
        <button
          v-if="newName"
          type="button"
          class="mb-1 flex w-full items-center gap-3 rounded-md px-2 py-2 text-left text-sm hover:bg-ink/5"
          :disabled="busy"
          @click="assign({ name: newName })"
        >
          <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-accent/15 text-accent"><Icon name="lucide:user-plus" size="16" /></span>
          <span class="min-w-0 truncate">{{ t('photos.face_new_person', { name: newName }) }}</span>
        </button>
        <p v-if="!people" class="text-sm text-muted">{{ t('common.loading') }}</p>
        <button
          v-for="p in shown"
          :key="p.id"
          type="button"
          class="flex w-full items-center gap-3 rounded-md px-2 py-1.5 text-left text-sm hover:bg-ink/5"
          :disabled="busy"
          @click="assign({ personId: p.id })"
        >
          <span class="h-9 w-9 shrink-0 overflow-hidden rounded-full bg-ink/5">
            <img v-if="img(p.cover)" :src="img(p.cover)" alt="" class="h-full w-full object-cover" draggable="false" />
          </span>
          <span class="min-w-0 flex-1 truncate" :class="p.name ? '' : 'text-muted'">{{ personLabel(p, t('photos.person_unnamed')) }}</span>
          <span class="shrink-0 text-xs text-muted">{{ p.count }}</span>
        </button>
      </div>
      <p v-if="error" class="mt-2 text-sm text-danger">{{ error }}</p>
      <div class="mt-4 flex justify-end">
        <button type="button" class="btn-ghost" @click="emit('close')">{{ t('common.cancel') }}</button>
      </div>
    </div>
  </div>
</template>
