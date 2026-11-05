<script setup lang="ts">
// «Инфо» beside the viewer: date and time with the zone, the place (with a mini-map when
// the server has maps), who is on it, the camera, the file, and where it lies in Files.
import type { PhotoAsset } from '~/composables/usePhotos'
import { takenLabel, formatDuration, cameraName } from '~/composables/usePhotos'
import { offsetLabel } from '~/lib/photos/edit'
import { formatCoord } from '~/lib/photos/coords'
import MapPicker from '~/components/photos/MapPicker.vue'
import { ageLabel } from '~/lib/photos/people'
import AssignFaceDialog from '~/components/photos/AssignFaceDialog.vue'

const props = defineProps<{
  asset: PhotoAsset | null
  mapsReady: boolean
  readonly?: boolean
}>()
const emit = defineEmits<{ 'edit-date': []; 'edit-place': []; close: []; 'people-changed': [] }>()
const { t, locale } = useI18n()

const a = computed(() => props.asset)

// Who is on it: their faces cut from this very photo, the name and the age that day.
const faceLoader = makeThumbLoader()
const faceTick = ref(0)
faceLoader.onChange = () => { faceTick.value++ }
watch(() => [a.value?.people, a.value?.unknownFaces], () => {
  const ids = [...(a.value?.people ?? []).map((p) => p.faceId), ...(a.value?.unknownFaces ?? []).map((f) => f.faceId)]
  faceLoader.sync(ids.map((id, i) => ({ key: `face:${id}`, url: faceThumbURL(id), priority: i })))
}, { immediate: true })
// «Кто это?» on a face nobody was matched to.
const assigning = ref<string | null>(null)
function assigned() {
  assigning.value = null
  emit('people-changed')
}
onBeforeUnmount(() => { faceLoader.sync([]); faceLoader.clear() })
function faceSrc(faceId: string) {
  void faceTick.value
  return faceLoader.get(`face:${faceId}`)
}
function age(n: number) {
  const forms: Partial<Record<Intl.LDMLPluralRule, string>> = {}
  for (const c of ['one', 'few', 'many', 'other'] as const) forms[c] = t(`photos.age.${c}`, { n })
  return ageLabel(n, locale.value, forms)
}

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
  if (!f?.nodeId) return null // somebody else's card: where it lies is theirs
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

      <section v-if="a.people?.length || a.unknownFaces?.length" class="row">
        <div class="k">{{ t('photos.info_people') }}</div>
        <div class="flex flex-wrap gap-x-2 gap-y-3 pt-1">
          <NuxtLink
            v-for="p in a.people"
            :key="p.id"
            :to="{ path: '/photos', query: { tab: 'people', person: p.id } }"
            class="person"
          >
            <span class="face">
              <img v-if="faceSrc(p.faceId)" :src="faceSrc(p.faceId)" alt="" draggable="false" />
              <Icon v-else name="lucide:user" size="18" class="opacity-50" />
            </span>
            <span class="name" :class="p.name ? '' : 'opacity-60'">{{ p.name || t('photos.person_unnamed') }}</span>
            <span v-if="p.age != null" class="age">{{ age(p.age) }}</span>
          </NuxtLink>
          <button
            v-for="f in a.unknownFaces ?? []"
            :key="f.faceId"
            type="button"
            class="person"
            :title="t('photos.face_who')"
            @click="assigning = f.faceId"
          >
            <span class="face unknown">
              <img v-if="faceSrc(f.faceId)" :src="faceSrc(f.faceId)" alt="" draggable="false" />
              <Icon v-else name="lucide:user" size="18" class="opacity-50" />
            </span>
            <span class="name opacity-60">{{ t('photos.face_unknown') }}</span>
          </button>
        </div>
      </section>

      <section v-if="camera.body || camera.shot" class="row">
        <div class="k">{{ t('photos.info_camera') }}</div>
        <div v-if="camera.body">{{ camera.body }}</div>
        <div v-if="camera.lens" class="opacity-70">{{ camera.lens }}</div>
        <div v-if="camera.shot" class="opacity-70">{{ camera.shot }}</div>
      </section>

      <section v-if="a.owner" class="row">
        <div class="k">{{ t('photos.info_owner') }}</div>
        <div class="break-all">{{ a.owner }}</div>
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
    <AssignFaceDialog v-if="assigning" :face-id="assigning" @close="assigning = null" @done="assigned" />
  </aside>
</template>

<style scoped>
.info { background: #1b1b1b; color: #eee; }
.row { margin-bottom: 1rem; }
.k { opacity: 0.55; font-size: 0.75rem; margin-bottom: 0.125rem; }
.edit { color: #7ab8ff; font-size: 0.75rem; white-space: nowrap; }
.edit:hover { text-decoration: underline; }
.person { display: flex; width: 4.25rem; flex-direction: column; align-items: center; text-align: center; font-size: 0.75rem; line-height: 1.2; }
.face { display: flex; height: 3.25rem; width: 3.25rem; align-items: center; justify-content: center; overflow: hidden; border-radius: 9999px; background: rgba(255, 255, 255, 0.08); margin-bottom: 0.25rem; transition: box-shadow 0.15s; }
.face img { height: 100%; width: 100%; object-fit: cover; }
.person:hover .face { box-shadow: 0 0 0 2px #7ab8ff; }
.face.unknown { box-shadow: 0 0 0 1px rgba(255, 255, 255, 0.35); }
.face.unknown img { opacity: 0.8; }
.name { max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.age { opacity: 0.55; }
</style>
