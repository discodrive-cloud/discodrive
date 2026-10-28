<script setup lang="ts">
// «Инфо» beside the viewer: date and time with the zone, the place (with a mini-map when
// the server has maps), the camera, the file, and where it lies in Files.
import type { PhotoAsset } from '~/composables/usePhotos'
import { takenLabel, formatDuration, cameraName } from '~/composables/usePhotos'
import { offsetLabel } from '~/lib/photos/edit'
import { formatCoord } from '~/lib/photos/coords'
import MapPicker from '~/components/photos/MapPicker.vue'

const props = defineProps<{
  asset: PhotoAsset | null
  mapsReady: boolean
  readonly?: boolean
}>()
const emit = defineEmits<{ 'edit-date': []; 'edit-place': []; close: [] }>()
const { t, locale } = useI18n()

const a = computed(() => props.asset)

const zone = computed(() => {
  const v = a.value?.tzOffsetMin
  return v == null ? t('photos.tz_unknown') : offsetLabel(v)
})

const place = computed(() => {
  const x = a.value
  if (!x) return ''
  const name = [x.place, countryName(x.country, locale.value)].filter(Boolean).join(', ')
  if (name) return name
  if (x.lat != null && x.lon != null) return `${formatCoord(x.lat)}, ${formatCoord(x.lon)}`
  return ''
})

const point = computed(() => {
  const x = a.value
  return x && x.lat != null && x.lon != null ? { lat: x.lat, lon: x.lon } : null
})

const camera = computed(() => {
  const c = a.value?.camera
  if (!c) return { body: '', shot: '' }
  const body = cameraName(c)
  const shot = [
    c.focalMm ? `${Math.round(c.focalMm)} ${t('photos.mm')}` : '',
    c.fNumber ? `ƒ${Number(c.fNumber.toFixed(1))}` : '',
    c.exposure || '',
    c.iso ? `ISO ${c.iso}` : '',
  ].filter(Boolean).join(' · ')
  return { body, shot, lens: c.lens || '' }
})

const fileLine = computed(() => {
  const x = a.value
  if (!x) return ''
  return [
    x.w && x.h ? `${x.w}×${x.h}` : '',
    formatBytes(x.file.size),
    x.kind === 'video' && x.durationMs ? formatDuration(x.durationMs) : '',
    x.videoCodec ? x.videoCodec.toUpperCase() : '',
    x.isHdr ? 'HDR' : '',
  ].filter(Boolean).join(' · ')
})

const whereLink = computed(() => {
  const f = a.value?.file
  if (!f) return null
  const path = f.folderPath ? `${f.folderPath}/${f.name}` : f.name
  const query: Record<string, string> = {}
  if (f.folderId) { query.folder = f.folderId; query.name = f.folderPath }
  return { path, to: { path: '/files', query } }
})
</script>

<template>
  <aside class="info flex h-full w-full flex-col overflow-y-auto p-4 text-sm">
    <div class="mb-4 flex items-center justify-between">
      <h3 class="font-semibold">{{ t('photos.info') }}</h3>
      <button type="button" class="rounded p-1 opacity-70 hover:opacity-100" :aria-label="t('common.close')" @click="emit('close')">
        <Icon name="lucide:x" size="16" />
      </button>
    </div>

    <div v-if="!a" class="opacity-60">{{ t('common.loading') }}</div>
    <template v-else>
      <section class="row">
        <div class="k">{{ t('photos.info_date') }}</div>
        <div class="flex items-start justify-between gap-2">
          <span>{{ takenLabel(a, locale) }} <span class="opacity-60">({{ zone }})</span></span>
          <button v-if="!readonly" type="button" class="edit" @click="emit('edit-date')">{{ t('photos.change') }}</button>
        </div>
        <div v-if="a.dateSource === 'edit'" class="mt-0.5 text-xs opacity-50">{{ t('photos.date_edited') }}</div>
      </section>

      <section class="row">
        <div class="k">{{ t('photos.info_place') }}</div>
        <div class="flex items-start justify-between gap-2">
          <span :class="place ? '' : 'opacity-50'">{{ place || t('photos.place_none') }}</span>
          <button v-if="!readonly" type="button" class="edit" @click="emit('edit-place')">{{ t('photos.change') }}</button>
        </div>
      </section>
      <div v-if="mapsReady && point" class="mb-4 h-32">
        <MapPicker :model-value="point" :interactive="false" :zoom="11" />
      </div>

      <section v-if="camera.body || camera.shot" class="row">
        <div class="k">{{ t('photos.info_camera') }}</div>
        <div v-if="camera.body">{{ camera.body }}</div>
        <div v-if="camera.lens" class="opacity-70">{{ camera.lens }}</div>
        <div v-if="camera.shot" class="opacity-70">{{ camera.shot }}</div>
      </section>

      <section class="row">
        <div class="k">{{ t('photos.info_file') }}</div>
        <div class="break-all">{{ a.file.name }}</div>
        <div class="opacity-70">{{ fileLine }}</div>
      </section>

      <section v-if="whereLink" class="row">
        <div class="k">{{ t('photos.info_where') }}</div>
        <NuxtLink :to="whereLink.to" class="edit break-all text-left">{{ whereLink.path }} →</NuxtLink>
      </section>
    </template>
  </aside>
</template>

<style scoped>
.info { background: #1b1b1b; color: #eee; }
.row { margin-bottom: 1rem; }
.k { opacity: 0.55; font-size: 0.75rem; margin-bottom: 0.125rem; }
.edit { color: #7ab8ff; font-size: 0.75rem; white-space: nowrap; }
.edit:hover { text-decoration: underline; }
</style>
