<script setup lang="ts">
// «Карта»: the cards with a position as clusters over the server's own tiles (the same
// region.pmtiles, fonts and sprites as MapPicker). The server groups by a Mercator grid
// (GET map/clusters) for the visible box; clusters that would overlap on screen merge into
// one pin. A pin is the cover's thumbnail with the count; a click emits the pin's cells as
// an area, which the page opens as a grid.
import type { PhotoRow } from '~/composables/usePhotos'
import { thumbKey, thumbURL } from '~/composables/usePhotos'
import { buildMapStyle, mapLang, mapRequest, type MapFlavor } from '~/lib/photos/mapStyle'
import { loadMapLib, openRegion, type MapLib } from '~/lib/photos/mapLib'
import { formatArea, mergePins, padFactor, paddedBbox, type BBox, type Cluster, type Pin } from '~/lib/photos/mapClusters'

const emit = defineEmits<{ open: [area: string, place: string | null, country: string | null] }>()

const { t, locale } = useI18n()
const { theme } = useTheme()
const sess = useSession()
const api = usePhotosApi()

const el = ref<HTMLElement | null>(null)
const loading = ref(true)
const failed = ref(false)
const empty = ref(false)

let map: any = null
let lib: MapLib | null = null
let markers: any[] = []
const loader = makeThumbLoader()
// thumbnail key → the <img> elements showing it (markers are plain DOM, outside Vue)
let imgs = new Map<string, HTMLImageElement[]>()

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

// The grid one level finer than the map's zoom: a cell is 64–128 px on screen, about a
// pin, so places a little apart get pins of their own; mergePins joins those that overlap.
function zoomLevel(): number {
  return Math.max(0, Math.min(20, Math.floor(map.getZoom()) + 1))
}

function formatCount(n: number): string {
  try { return new Intl.NumberFormat(locale.value).format(n) } catch { return String(n) }
}

function pinElement(pin: Pin<PhotoRow>, z: number): HTMLElement {
  const box = document.createElement('button')
  box.type = 'button'
  box.className = 'dd-map-pin'
  box.title = pin.place ?? ''
  const img = document.createElement('img')
  img.alt = ''
  img.draggable = false
  const key = thumbKey(pin.cover)
  const src = loader.get(key)
  if (src) img.src = src
  imgs.set(key, [...(imgs.get(key) ?? []), img])
  box.appendChild(img)
  if (pin.count > 1) {
    const n = document.createElement('span')
    n.textContent = formatCount(pin.count)
    box.appendChild(n)
  }
  box.addEventListener('click', (e) => {
    e.stopPropagation()
    emit('open', formatArea(z, pin.cells), pin.place, pin.country)
  })
  return box
}

function render(clusters: Cluster<PhotoRow>[], z: number) {
  for (const m of markers) m.remove()
  markers = []
  imgs = new Map()
  const pins = mergePins(clusters, (lat, lon) => map.project([lon, lat]))
  for (const pin of pins) {
    markers.push(new lib!.maplibregl.Marker({ element: pinElement(pin, z), anchor: 'bottom' })
      .setLngLat([pin.lon, pin.lat]).addTo(map))
  }
  loader.sync(pins.map((p, i) => ({ key: thumbKey(p.cover), url: thumbURL(p.cover), priority: i })))
}

loader.onChange = () => {
  for (const [key, list] of imgs) {
    const src = loader.get(key)
    if (src) for (const img of list) if (img.src !== src) img.src = src
  }
}

let ctrl: AbortController | null = null
let timer: ReturnType<typeof setTimeout> | null = null
async function refresh() {
  if (!map) return
  ctrl?.abort()
  const mine = (ctrl = new AbortController())
  const z = zoomLevel()
  const b = map.getBounds()
  let cs: Cluster<PhotoRow>[]
  try {
    const box = { w: b.getWest(), s: b.getSouth(), e: b.getEast(), n: b.getNorth() }
    const k = padFactor(el.value?.clientWidth ?? 0, el.value?.clientHeight ?? 0)
    cs = await api.mapClusters(z, paddedBbox(box, k), mine.signal)
  } catch { return /* aborted, or a failed read: the pins on screen stay */ }
  if (mine === ctrl && map) render(cs, z)
}
function scheduleRefresh() {
  if (timer) clearTimeout(timer)
  timer = setTimeout(refresh, 200)
}

// First view: every photo at once, or the region when there are none.
async function frame(h: any) {
  let b: BBox | null = null
  try { b = await api.mapBounds() } catch { /* the region then */ }
  if (!map) return
  empty.value = !b
  if (b) map.fitBounds([[b.w, b.s], [b.e, b.n]], { padding: 80, maxZoom: 12, duration: 0 })
  else if (Number.isFinite(h.minLon) && h.maxLon > h.minLon) {
    map.fitBounds([[h.minLon, h.minLat], [h.maxLon, h.maxLat]], { padding: 20, duration: 0 })
  }
}

onMounted(async () => {
  try {
    lib = await loadMapLib()
    lib.source.setHeaders(new Headers(sess.value.token ? { Authorization: `Bearer ${sess.value.token}` } : {}))
    const h = await openRegion(lib)
    if (!el.value) return
    map = new lib.maplibregl.Map({
      container: el.value,
      style: style(),
      center: [0, 20],
      zoom: 1,
      attributionControl: { compact: true },
      transformRequest: (url: string) => mapRequest(location.origin, sess.value.token)(url),
    })
    map.addControl(new lib.maplibregl.NavigationControl({ showCompass: false }), 'top-right')
    map.on('load', async () => {
      loading.value = false
      map?.on('moveend', scheduleRefresh)
      // Opened on an area's grid (a reload, a link), the map is hidden and has no size:
      // framing it now would fit the photos into a default box. Frame on first show.
      if (!el.value?.clientWidth) { unframed = h; return }
      await frame(h)
      refresh()
    })
  } catch {
    failed.value = true
    loading.value = false
  }
})

watch([theme, locale], () => { if (map && lib) map.setStyle(style()) })
watch(() => sess.value.token, (tok) => {
  lib?.source.setHeaders(new Headers(tok ? { Authorization: `Bearer ${tok}` } : {}))
})

onBeforeUnmount(() => {
  ctrl?.abort()
  if (timer) clearTimeout(timer)
  for (const m of markers) m.remove()
  map?.remove()
  map = null
  loader.sync([])
  loader.clear()
})

// The page hides the map (v-show) under an area's grid: back on screen it takes its size
// again, and is framed then if it never was.
let unframed: any = null
async function shown() {
  if (!map) return
  map.resize()
  if (!unframed) return
  const h = unframed
  unframed = null
  await frame(h)
  refresh()
}
defineExpose({ resize: shown })
</script>

<template>
  <div class="relative min-h-0 flex-1 overflow-hidden">
    <div ref="el" class="h-full w-full" />
    <div v-if="loading && !failed" class="pointer-events-none absolute inset-0 flex items-center justify-center text-muted">
      <Icon name="lucide:loader-circle" size="20" class="animate-spin" />
    </div>
    <div v-if="failed" class="absolute inset-0 flex items-center justify-center p-6 text-center text-sm text-muted">
      {{ t('photos.map_failed') }}
    </div>
    <div v-else-if="empty && !loading" class="pointer-events-none absolute inset-x-0 top-4 flex justify-center px-4">
      <span class="rounded-full bg-black/75 px-3 py-1.5 text-xs text-white">{{ t('photos.map_empty') }}</span>
    </div>
  </div>
</template>

<style>
/* Markers are plain DOM inside MapLibre's container: no scoped attributes reach them. */
.dd-map-pin {
  position: relative;
  display: block;
  width: 64px;
  height: 64px;
  margin-bottom: 8px;
  padding: 0;
  border: 3px solid #fff;
  border-radius: 10px;
  background: rgb(0 0 0 / 0.25);
  box-shadow: 0 2px 8px rgb(0 0 0 / 0.35);
  cursor: pointer;
}
/* MapLibre positions the marker with transform: hover must not touch it. */
.dd-map-pin:hover { z-index: 2; border-color: rgb(var(--c-accent)); }
.dd-map-pin img { display: block; width: 100%; height: 100%; object-fit: cover; border-radius: 7px; }
.dd-map-pin span {
  position: absolute;
  left: 6px;
  bottom: 5px;
  color: #fff;
  font: 700 15px/1 system-ui, sans-serif;
  text-shadow: 0 1px 3px rgb(0 0 0 / 0.85);
  pointer-events: none;
}
.dd-map-pin::after {
  content: "";
  position: absolute;
  left: 50%;
  bottom: -11px;
  margin-left: -6px;
  border: 6px solid transparent;
  border-top-color: #fff;
}
</style>
