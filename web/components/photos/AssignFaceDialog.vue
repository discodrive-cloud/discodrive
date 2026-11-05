<script setup lang="ts">
// «Кто это?»: a face recognition did not match goes to one of the user's people, or to a
// new person of the typed name. With from it is «Это не …»: a face (or, with assetIds, the
// person's faces on those cards) recognition put on the wrong person goes to somebody
// else, or nowhere.
import type { FaceTarget, PhotoPerson } from '~/composables/usePhotos'
import { findSameName, personLabel } from '~/lib/photos/people'

const props = defineProps<{
  faceId: string | null // the face shown at the top
  from?: { id: string; name: string }
  assetIds?: string[]
}>()
const emit = defineEmits<{ close: []; done: [person: PhotoPerson | null, moved: number] }>()
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

function syncThumbs() {
  const own = props.faceId ? [{ key: `face:${props.faceId}`, url: faceThumbURL(props.faceId), priority: 0 }] : []
  loader.sync([
    ...own,
    ...(people.value ?? []).filter((p) => p.cover).map((p, i) => ({ key: `face:${p.cover}`, url: faceThumbURL(p.cover!), priority: i + 1 })),
  ])
}
onMounted(async () => {
  input.value?.focus()
  syncThumbs()
  try {
    people.value = (await api.people()).filter((p) => p.id !== props.from?.id)
    syncThumbs()
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

const title = computed(() => {
  if (!props.from) return t('photos.face_who')
  return props.from.name ? t('photos.face_not_title', { name: props.from.name }) : t('photos.face_not_other')
})
const hint = computed(() => {
  if (!props.from) return t('photos.face_assign_hint')
  return props.assetIds ? t('photos.face_move_hint_many', { n: props.assetIds.length }) : t('photos.face_move_hint')
})

// Named people matching what is typed first; unnamed ones only while nothing is typed.
const shown = computed(() => {
  const q = query.value.trim().toLocaleLowerCase()
  const list = people.value ?? []
  if (!q) return list
  return list.filter((p) => p.name.toLocaleLowerCase().includes(q))
})
const newName = computed(() => {
  const n = query.value.trim()
  if (!n || findSameName(people.value ?? [], n, '')) return ''
  // Typing the name of the person they are taken from is not a new person.
  if (props.from && findSameName([props.from], n, '')) return ''
  return n
})

async function assign(body: FaceTarget) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    if (props.from && props.assetIds) {
      const r = await api.movePersonFaces(props.from.id, props.assetIds, body)
      emit('done', r.person, r.moved)
    } else {
      const r = await api.assignFace(props.faceId!, body)
      emit('done', 'id' in r ? (r as PhotoPerson) : null, 1)
    }
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
        <div class="flex h-14 w-14 shrink-0 items-center justify-center overflow-hidden rounded-full bg-ink/5 text-muted">
          <img v-if="img(faceId)" :src="img(faceId)" alt="" class="h-full w-full object-cover" draggable="false" />
          <Icon v-else name="lucide:user" size="24" />
        </div>
        <div class="min-w-0">
          <h2 id="assign-face-title" class="truncate font-semibold">{{ title }}</h2>
          <p class="text-sm text-muted">{{ hint }}</p>
        </div>
      </div>
      <form class="mb-3" @submit.prevent="submit">
        <input ref="input" v-model="query" class="input w-full" maxlength="100" :placeholder="t('photos.face_name_placeholder')" :aria-label="t('photos.face_name_placeholder')" />
      </form>
      <div class="min-h-0 flex-1 overflow-y-auto">
        <button
          v-if="from && !query.trim()"
          type="button"
          class="mb-1 flex w-full items-center gap-3 rounded-md px-2 py-2 text-left text-sm hover:bg-ink/5"
          :disabled="busy"
          @click="assign({ remove: true })"
        >
          <span class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-ink/10 text-muted"><Icon name="lucide:user-x" size="16" /></span>
          <span class="min-w-0 truncate">{{ t('photos.face_remove') }}</span>
        </button>
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
