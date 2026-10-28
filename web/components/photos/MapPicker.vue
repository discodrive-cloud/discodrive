<script setup lang="ts">
// A map over the server's own tiles (Task 10: region.pmtiles + fonts/sprites, all behind
// the Bearer token). Interactive: a click puts the marker and emits the point. With
// interactive=false it is the info panel's mini-map. MapLibre, pmtiles and the protomaps
// style are loaded with import() on first use, so the gallery does not carry them.
import { buildMapStyle, mapLang, mapRequest, type MapFlavor } from '~/lib/photos/mapStyle'
import type { LatLon } from '~/lib/photos/coords'
import { loadMapLib, type MapLib } from '~/lib/photos/mapLib'

const props = withDefaults(defineProps<{
  modelValue: LatLon | null
  interactive?: boolean
  zoom?: number
}>(), { interactive: true, zoom: 12 })
const emit = defineEmits<{ 'update:modelValue': [value: LatLon]; error: [] }>()

const { t, locale } = useI18n()
const { theme } = useTheme()
const sess = useSession()

const el = ref<HTMLElement | null>(null)
const loading = ref(true)
const failed = ref(false)

let map: any = null
let marker: any = null
let lib: MapLib | null = null

function flavor(): MapFlavor {
  return theme.value === 'light' ? 'light' : 'dark'
}

function style() {
  return buildMapStyle({
    origin: location.origin,
    flavor: flavor(),
    lang: mapLang(locale.value),
    layers: (src, fl, lang) => lib!.basemaps.layers(src, lib!.basemaps.namedFlavor(fl), { lang }),
  })
}

function placeMarker(p: LatLon | null, fly: boolean) {
  if (!map || !lib) return
  if (!p) { marker?.remove(); marker = null; return }
  if (!marker) marker = new lib.maplibregl.Marker({ color: '#22d3ee' }).setLngLat([p.lon, p.lat]).addTo(map)
  else marker.setLngLat([p.lon, p.lat])
  if (fly) map.easeTo({ center: [p.lon, p.lat], zoom: Math.max(map.getZoom(), props.interactive ? 10 : props.zoom) })
}

onMounted(async () => {
  try {
    lib = await loadMapLib()
    // The session token renews; the archive's requests use the current one.
    lib.source.setHeaders(new Headers(sess.value.token ? { Authorization: `Bearer ${sess.value.token}` } : {}))
    if (!el.value) return
    const p = props.modelValue
    map = new lib!.maplibregl.Map({
      container: el.value,
      style: style(),
      center: p ? [p.lon, p.lat] : [0, 20],
      zoom: p ? props.zoom : 1,
      interactive: props.interactive,
      attributionControl: props.interactive ? { compact: true } : false,
      transformRequest: (url: string) => mapRequest(location.origin, sess.value.token)(url),
    })
    if (props.interactive) {
      map.addControl(new lib!.maplibregl.NavigationControl({ showCompass: false }), 'top-right')
      map.on('click', (e: any) => {
        const v = { lat: Number(e.lngLat.lat.toFixed(6)), lon: Number(e.lngLat.lng.toFixed(6)) }
        placeMarker(v, false)
        emit('update:modelValue', v)
      })
    }
    map.on('load', () => { loading.value = false })
    map.on('error', (e: any) => {
      // A missing glyph or tile outside the region is not worth a message.
      if (loading.value && e?.error?.status && e.error.status >= 400 && String(e.error.url || '').includes('region.pmtiles')) {
        failed.value = true
        emit('error')
      }
    })
    placeMarker(p, false)
    // No point yet: show the whole downloaded region.
    if (!p) {
      try {
        const h = await lib!.archive.getHeader()
        if (map && Number.isFinite(h.minLon) && h.maxLon > h.minLon) {
          map.fitBounds([[h.minLon, h.minLat], [h.maxLon, h.maxLat]], { padding: 20, duration: 0 })
        }
      } catch { /* the map still works */ }
    }
  } catch {
    failed.value = true
    loading.value = false
    emit('error')
  }
})

watch(() => props.modelValue, (p, old) => {
  if (p?.lat === old?.lat && p?.lon === old?.lon) return
  placeMarker(p, true)
})

watch([theme, locale], () => { if (map && lib) map.setStyle(style()) })
// A renewed session token: tiles fetched from now on carry it (fonts and sprites read it
// live through transformRequest).
watch(() => sess.value.token, (tok) => {
  lib?.source.setHeaders(new Headers(tok ? { Authorization: `Bearer ${tok}` } : {}))
})

onBeforeUnmount(() => {
  marker?.remove()
  map?.remove()
  map = null
  marker = null
})
</script>

<template>
  <div class="relative h-full w-full overflow-hidden rounded-lg bg-ink/5">
    <div ref="el" class="h-full w-full" />
    <div v-if="loading && !failed" class="pointer-events-none absolute inset-0 flex items-center justify-center text-muted">
      <Icon name="lucide:loader-circle" size="18" class="animate-spin" />
    </div>
    <div v-if="failed" class="absolute inset-0 flex items-center justify-center p-3 text-center text-xs text-muted">
      {{ t('photos.map_failed') }}
    </div>
  </div>
</template>
