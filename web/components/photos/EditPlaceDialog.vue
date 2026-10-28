<script setup lang="ts">
// Place of one card or a selection: latitude and longitude. Pasting anything parseCoords
// reads (a pair, degrees-minutes-seconds, a Google/Apple/OSM link) into either field fills
// both. "Pick on the map" needs the server's offline maps. "Clear place" removes it.
import type { MetadataEdit } from '~/composables/usePhotos'
import { parseCoords, formatCoord, type LatLon } from '~/lib/photos/coords'
import type { EditResult } from '~/lib/photos/edit'
import EditSummary from '~/components/photos/EditSummary.vue'

const MapPicker = defineAsyncComponent(() => import('~/components/photos/MapPicker.vue'))

const props = defineProps<{ ids: string[] }>()
const emit = defineEmits<{ close: []; done: [result: EditResult] }>()
const { t } = useI18n()

const edit = usePhotoEdit(() => props.ids)
const single = computed(() => props.ids.length === 1)

const lat = ref('')
const lon = ref('')
const error = ref('')
const mapsReady = ref(false)
const showMap = ref(false)

onMounted(async () => {
  photosMapsReady().then((v) => { mapsReady.value = v })
  await edit.loadCards()
  const a = single.value ? edit.assets.value?.[0] : null
  if (a && a.lat != null && a.lon != null) {
    lat.value = formatCoord(a.lat)
    lon.value = formatCoord(a.lon)
  }
})

// What the fields say: both filled and in range.
const point = computed<LatLon | null>(() => {
  if (!lat.value.trim() || !lon.value.trim()) return null
  return parseCoords(`${lat.value.trim()}, ${lon.value.trim()}`)
})

function spread(text: string): boolean {
  const p = parseCoords(text)
  // A single number is just typing, not a pair.
  if (!p || !/[,\s;°@=/]/.test(text.trim())) return false
  lat.value = formatCoord(p.lat)
  lon.value = formatCoord(p.lon)
  return true
}

function onPaste(e: ClipboardEvent) {
  const text = e.clipboardData?.getData('text') ?? ''
  if (spread(text)) e.preventDefault()
}

// Typing never splits ("42,6" on a Russian keyboard is a decimal comma). Text that
// arrives at once without a paste event — a drop, autofill, a mobile keyboard's paste —
// is split when it reads as a pair.
const prevLen = { lat: 0, lon: 0 }
function onInput(field: 'lat' | 'lon', e: Event) {
  const v = field === 'lat' ? lat.value : lon.value
  const type = (e as InputEvent).inputType || ''
  const bulk = type.startsWith('insertFromPaste') || type === 'insertFromDrop' || type === 'insertReplacementText' || v.length - prevLen[field] > 1
  prevLen[field] = v.length
  if (bulk) spread(v)
}

function onPick(p: LatLon) {
  lat.value = formatCoord(p.lat)
  lon.value = formatCoord(p.lon)
}

async function run(body: MetadataEdit, ids = props.ids) {
  error.value = ''
  const r = await edit.run(ids, body)
  emit('done', r)
  if (single.value && r.updated.length === 1) emit('close')
}

function apply() {
  if (!point.value) { error.value = t('photos.place_invalid'); return }
  run({ place: { lat: point.value.lat, lon: point.value.lon } })
}

function clearPlace() {
  run({ place: null })
}

function retry() {
  edit.retry().then((r) => { if (r) emit('done', r) })
}

function close() {
  if (edit.running.value) return
  emit('close')
}
useModalEscape(computed(() => true), close)
</script>

<template>
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-4" @click.self="close">
    <form class="card w-full max-w-lg p-5" role="dialog" aria-modal="true" aria-labelledby="photo-place-title" @submit.prevent="apply">
      <h2 id="photo-place-title" class="mb-1 font-semibold">{{ t('photos.edit_place') }}</h2>
      <p v-if="!single" class="mb-2 text-sm text-muted">{{ t('photos.selected', { n: ids.length }) }}</p>
      <p class="mb-3 text-xs text-muted">{{ t('photos.place_paste_hint') }}</p>

      <div class="mb-3 grid grid-cols-2 gap-3">
        <label class="block text-sm">{{ t('photos.latitude') }}
          <input v-model="lat" class="input mt-1 tabular-nums" inputmode="decimal" placeholder="42.6475" @paste="onPaste" @input="onInput('lat', $event)" />
        </label>
        <label class="block text-sm">{{ t('photos.longitude') }}
          <input v-model="lon" class="input mt-1 tabular-nums" inputmode="decimal" placeholder="77.0820" @paste="onPaste" @input="onInput('lon', $event)" />
        </label>
      </div>

      <div class="mb-3">
        <button
          type="button"
          class="btn-ghost px-2 py-1 text-sm disabled:cursor-not-allowed disabled:opacity-50"
          :disabled="!mapsReady"
          :title="mapsReady ? '' : t('photos.maps_not_loaded')"
          @click="showMap = !showMap"
        >
          <Icon name="lucide:map" size="16" /> {{ showMap ? t('photos.map_hide') : t('photos.map_pick') }}
        </button>
        <p v-if="!mapsReady" class="mt-1 text-xs text-muted">{{ t('photos.maps_not_loaded') }}</p>
        <div v-if="showMap && mapsReady" class="mt-2 h-72">
          <MapPicker :model-value="point" @update:model-value="onPick" />
        </div>
      </div>

      <p class="mb-3 flex gap-2 rounded-md bg-warn/10 p-2 text-xs text-warn">
        <Icon name="lucide:triangle-alert" size="14" class="mt-0.5 shrink-0" />
        <span v-if="edit.loadingCards.value">{{ t('common.loading') }}</span>
        <span v-else-if="edit.estimate.value !== null">
          {{ t('photos.edit_versions_warning', { size: formatBytes(edit.estimate.value) }) }}
          <template v-if="edit.sidecars.value"> {{ t('photos.edit_sidecar_note', { n: edit.sidecars.value }) }}</template>
        </span>
        <span v-else>{{ t('photos.edit_versions_generic') }}</span>
      </p>

      <EditSummary :progress="edit.progress.value" :result="edit.result.value" :shift="edit.resultShift.value" @retry="retry" @stop="edit.stop()" />
      <p v-if="error" class="mb-3 text-sm text-danger" role="alert">{{ error }}</p>

      <div class="flex flex-wrap items-center justify-between gap-2">
        <button type="button" class="btn-danger px-2 py-1.5 text-sm" :disabled="edit.running.value" @click="clearPlace">
          <Icon name="lucide:map-pin-off" size="16" /> {{ t('photos.place_clear') }}
        </button>
        <div class="flex gap-2">
          <button type="button" class="btn-ghost" :disabled="edit.running.value" @click="close">{{ edit.result.value ? t('common.close') : t('dialog.cancel') }}</button>
          <button type="submit" class="btn-accent disabled:cursor-not-allowed disabled:opacity-50" :disabled="edit.running.value || !point">
            <Icon v-if="edit.running.value" name="lucide:loader-circle" size="16" class="animate-spin" />
            {{ t('photos.apply') }}
          </button>
        </div>
      </div>
    </form>
  </div>
</template>
