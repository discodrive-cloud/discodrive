<script setup lang="ts">
// Admin: «Карты и места» — the offline maps for the gallery (place names for photos, the
// map in the place dialog and the info panel). The admin picks a region; the server
// downloads GeoNames and the region's tiles in the background (polled here while it runs).
interface MapsState {
  region: string
  state: 'none' | 'downloading' | 'ready' | 'failed' | 'unavailable'
  bytes: number
  error: string
  regions: { id: string; approxBytes: number }[]
}

const { t } = useI18n()
const { request } = useApi()
const { confirm } = useDialog()

const st = ref<MapsState | null>(null)
const missing = ref(false) // the server has no maps manager at all (lite build)
const error = ref('')
const busy = ref(false)
const choice = ref('')
let poll: ReturnType<typeof setTimeout> | null = null

function schedule() {
  if (poll) clearTimeout(poll)
  poll = null
  if (st.value?.state === 'downloading') poll = setTimeout(load, 2000)
}

async function load() {
  try {
    st.value = await request<MapsState>('/admin/photos/v1/maps')
    if (!choice.value) choice.value = st.value.region || st.value.regions[0]?.id || ''
    error.value = ''
  } catch (e: any) {
    if (e?.response?.status === 404) missing.value = true
    else error.value = e?.data?.error || t('common.error_load')
  }
  schedule()
}
onMounted(load)
onBeforeUnmount(() => { if (poll) clearTimeout(poll) })

async function put(region: string | null) {
  busy.value = true
  error.value = ''
  try {
    st.value = await request<MapsState>('/admin/photos/v1/maps', { method: 'PUT', body: { region } })
  } catch (e: any) {
    error.value = e?.response?.status === 503 ? t('admin.maps_unavailable') : (e?.data?.error || t('common.error_save'))
  } finally {
    busy.value = false
  }
  schedule()
}

function start() {
  if (choice.value) put(choice.value)
}

async function removeMaps() {
  const ok = await confirm(t('admin.maps_remove_title'), { message: t('admin.maps_remove_message'), confirmText: t('common.delete'), danger: true })
  if (ok) put(null)
}

const approx = (id: string) => st.value?.regions.find((r) => r.id === id)?.approxBytes ?? 0
const pct = computed(() => {
  const s = st.value
  if (!s || s.state !== 'downloading') return 0
  const total = approx(s.region)
  return total ? Math.min(99, Math.round((100 * s.bytes) / total)) : 0
})
// Starting another region (or again after a failure) is what the button does.
const canStart = computed(() => {
  const s = st.value
  if (!s || busy.value || !choice.value || s.state === 'unavailable') return false
  return !(s.region === choice.value && (s.state === 'ready' || s.state === 'downloading'))
})
</script>

<template>
  <div v-if="!missing" class="mb-6 card p-5">
    <h2 class="mb-1 text-sm font-medium text-muted">{{ t('admin.maps_title') }}</h2>
    <p class="mb-3 text-xs text-muted">{{ t('admin.maps_hint') }}</p>

    <p v-if="!st && !error" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <template v-else-if="st">
      <p v-if="st.state === 'unavailable'" class="flex items-center gap-2 text-sm text-warn">
        <Icon name="lucide:triangle-alert" size="16" /> {{ t('admin.maps_unavailable') }}
      </p>
      <template v-else>
        <div class="mb-3 flex flex-wrap items-end gap-3">
          <label class="block text-sm">
            <span class="mb-1 block text-xs text-muted">{{ t('admin.maps_region') }}</span>
            <select v-model="choice" class="input w-64" :disabled="busy">
              <option v-for="r in st.regions" :key="r.id" :value="r.id">
                {{ t(`admin.maps_region_${r.id}`) }} — ≈ {{ formatBytes(r.approxBytes) }}
              </option>
            </select>
          </label>
          <button type="button" class="btn-accent disabled:cursor-not-allowed disabled:opacity-50" :disabled="!canStart" @click="start">
            <Icon name="lucide:download" size="16" />
            {{ st.state === 'ready' || st.state === 'downloading' ? t('admin.maps_switch') : t('admin.maps_download') }}
          </button>
          <button v-if="st.state !== 'none'" type="button" class="btn-danger" :disabled="busy" @click="removeMaps">
            <Icon name="lucide:trash-2" size="16" /> {{ t('admin.maps_remove') }}
          </button>
        </div>
        <p class="mb-2 text-xs text-muted">{{ t('admin.maps_size_note') }}</p>

        <div class="text-sm" role="status">
          <template v-if="st.state === 'none'">
            <span class="text-muted">{{ t('admin.maps_state_none') }}</span>
          </template>
          <template v-else-if="st.state === 'downloading'">
            <div class="mb-1 flex items-center gap-2">
              <Icon name="lucide:loader-circle" size="16" class="animate-spin text-accent" />
              {{ t('admin.maps_state_downloading', { region: t(`admin.maps_region_${st.region}`), size: formatBytes(st.bytes) }) }}
            </div>
            <div class="h-1.5 max-w-md overflow-hidden rounded bg-ink/10">
              <div class="h-full bg-accent transition-all" :style="{ width: `${pct}%` }" />
            </div>
          </template>
          <template v-else-if="st.state === 'ready'">
            <span class="flex items-center gap-2 text-accent">
              <Icon name="lucide:check" size="16" />
              {{ t('admin.maps_state_ready', { region: t(`admin.maps_region_${st.region}`), size: formatBytes(st.bytes) }) }}
            </span>
          </template>
          <template v-else-if="st.state === 'failed'">
            <span class="flex items-center gap-2 text-danger">
              <Icon name="lucide:triangle-alert" size="16" /> {{ t('admin.maps_state_failed', { error: st.error || '—' }) }}
            </span>
          </template>
        </div>
      </template>
    </template>
    <p v-if="error" class="mt-2 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="14" /> {{ error }}
    </p>
  </div>
</template>
