<script setup lang="ts">
// «Люди»: the persons found on the user's photos, named ones first, then by how many
// photos they are on. A face opens the person's grid (the page does that through
// ?person=). «Показать скрытых» lists the hidden ones instead, to bring them back.
// «Выбрать» picks many at once (shift-click picks a run) to hide or show them in one go.
import type { PhotoPerson } from '~/composables/usePhotos'
import { personLabel, pickPeople } from '~/lib/photos/people'

const emit = defineEmits<{ open: [id: string] }>()
const { t } = useI18n()
const api = usePhotosApi()

const showHidden = ref(false)
const people = ref<PhotoPerson[] | null>(null)
const error = ref('')
const loader = makeThumbLoader()
const tick = ref(0)
loader.onChange = () => { tick.value++ }

const selecting = ref(false)
const picked = ref<Set<string>>(new Set())
const anchor = ref<string | null>(null)
const busy = ref(false)

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
watch(showHidden, () => { people.value = null; stopSelecting(); load() })
onMounted(load)
onBeforeUnmount(() => { loader.sync([]); loader.clear() })
defineExpose({ reload: load })

function cover(p: PhotoPerson) {
  void tick.value
  return p.cover ? loader.get(`face:${p.cover}`) : undefined
}

function stopSelecting() {
  selecting.value = false
  picked.value = new Set()
  anchor.value = null
}
function click(p: PhotoPerson, e: MouseEvent) {
  if (!selecting.value) return emit('open', p.id)
  const ids = (people.value ?? []).map((x) => x.id)
  picked.value = pickPeople(picked.value, ids, p.id, anchor.value, e.shiftKey)
  anchor.value = p.id
}
// The visible list offers all the unnamed ones (most of the noise); the hidden list, everyone.
function pickMany() {
  const list = people.value ?? []
  picked.value = new Set((showHidden.value ? list : list.filter((p) => !p.name)).map((p) => p.id))
}

async function apply() {
  if (busy.value || !picked.value.size) return
  busy.value = true
  error.value = ''
  const ids = [...picked.value]
  try {
    await api.hidePeople(ids, !showHidden.value)
    // They leave this list either way.
    people.value = (people.value ?? []).filter((p) => !picked.value.has(p.id))
    stopSelecting()
  } catch (e: any) {
    error.value = e?.data?.error || t('photos.action_failed')
  } finally {
    busy.value = false
  }
}

function onKey(e: KeyboardEvent) {
  if (e.key === 'Escape' && selecting.value && !document.querySelector('[role="dialog"]')) stopSelecting()
}
onMounted(() => window.addEventListener('keydown', onKey))
onBeforeUnmount(() => window.removeEventListener('keydown', onKey))
</script>

<template>
  <div class="relative flex min-h-0 flex-1 flex-col">
    <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3 md:px-6" :class="selecting ? 'pb-20' : ''">
      <div class="mb-3 flex min-h-8 items-center gap-3">
        <span v-if="showHidden" class="text-sm font-medium">{{ t('photos.people_hidden_title') }}</span>
        <span v-if="error" class="text-xs text-danger">{{ error }}</span>
        <span class="flex-1" />
        <template v-if="people?.length">
          <button v-if="selecting" type="button" class="btn-ghost px-3 py-1.5 text-sm" @click="pickMany">
            {{ showHidden ? t('photos.people_pick_all') : t('photos.people_pick_unnamed') }}
          </button>
          <button type="button" class="btn-ghost px-3 py-1.5 text-sm" @click="selecting ? stopSelecting() : (selecting = true)">
            <Icon :name="selecting ? 'lucide:x' : 'lucide:square-check'" size="16" />
            {{ selecting ? t('common.cancel') : t('photos.select') }}
          </button>
        </template>
      </div>
      <p v-if="!people" class="text-sm text-muted">{{ t('common.loading') }}</p>
      <div v-else-if="!people.length" class="flex flex-col items-center gap-2 p-10 text-center text-sm text-muted">
        <Icon name="lucide:users" size="28" />
        <p>{{ showHidden ? t('photos.people_hidden_empty') : t('photos.people_empty') }}</p>
      </div>
      <div v-else class="grid select-none grid-cols-3 gap-x-3 gap-y-4 sm:grid-cols-4 md:grid-cols-6 xl:grid-cols-8">
        <button
          v-for="p in people"
          :key="p.id"
          type="button"
          class="group min-w-0 text-center"
          :aria-pressed="selecting ? picked.has(p.id) : undefined"
          @click="click(p, $event)"
        >
          <div
            class="relative mx-auto aspect-square w-full max-w-[9rem] overflow-hidden rounded-full bg-ink/5 ring-offset-2 ring-offset-panel transition"
            :class="selecting && picked.has(p.id) ? 'ring-4 ring-accent' : ''"
          >
            <img
              v-if="cover(p)"
              :src="cover(p)"
              alt=""
              class="h-full w-full object-cover transition"
              :class="selecting ? (picked.has(p.id) ? '' : 'opacity-70') : 'group-hover:scale-[1.03]'"
              draggable="false"
            />
            <div v-else class="flex h-full w-full items-center justify-center text-muted">
              <Icon name="lucide:user" size="28" />
            </div>
            <span
              v-if="selecting"
              class="absolute bottom-[8%] right-[8%] flex h-6 w-6 items-center justify-center rounded-full border-2 border-white shadow"
              :class="picked.has(p.id) ? 'bg-accent text-white' : 'bg-black/30'"
            >
              <Icon v-if="picked.has(p.id)" name="lucide:check" size="14" />
            </span>
          </div>
          <div class="mt-1.5 truncate text-sm" :class="p.name ? 'font-medium' : 'text-muted'">{{ personLabel(p, t('photos.person_unnamed')) }}</div>
          <div class="truncate text-xs text-muted">{{ t('photos.map_count', { n: p.count }) }}</div>
        </button>
      </div>
      <div v-if="!selecting" class="mt-6 flex justify-center">
        <button type="button" class="btn-ghost px-3 py-1.5 text-sm" @click="showHidden = !showHidden">
          <Icon :name="showHidden ? 'lucide:arrow-left' : 'lucide:eye-off'" size="16" />
          {{ showHidden ? t('photos.people_back_visible') : t('photos.people_show_hidden') }}
        </button>
      </div>
    </div>

    <div v-if="selecting" class="pointer-events-none absolute inset-x-0 bottom-3 z-20 flex justify-center px-2">
      <div class="card pointer-events-auto flex max-w-full items-center gap-1 px-1.5 py-1 shadow-xl">
        <button type="button" class="btn-ghost px-2 py-1.5" :title="t('photos.clear_selection')" :aria-label="t('photos.clear_selection')" @click="stopSelecting">
          <Icon name="lucide:x" size="16" />
        </button>
        <span class="whitespace-nowrap px-1 text-sm font-medium tabular-nums">{{ t('photos.selected', { n: picked.size }) }}</span>
        <Icon v-if="busy" name="lucide:loader-circle" class="mx-1 animate-spin text-muted" size="16" />
        <span class="mx-1 h-5 w-px bg-line" />
        <button type="button" class="btn-ghost px-3 py-1.5 text-sm" :disabled="busy || !picked.size" @click="apply">
          <Icon :name="showHidden ? 'lucide:eye' : 'lucide:eye-off'" size="16" />
          {{ showHidden ? t('photos.people_unhide') : t('photos.hide') }}
        </button>
      </div>
    </div>
  </div>
</template>
