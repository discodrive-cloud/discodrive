<script setup lang="ts">
// «В этот день»: today's date in earlier years, a block per year («3 года назад»).
import type { PhotoRow } from '~/composables/usePhotos'
import type { ThumbLoader } from '~/lib/photos/thumbLoader'
import PhotoTiles from '~/components/photos/PhotoTiles.vue'

const emit = defineEmits<{ open: [id: string, months: string[], rows: Map<string, PhotoRow[]>] }>()
const { t, locale } = useI18n()
const api = usePhotosApi()

const groups = ref<{ year: number; assets: PhotoRow[] }[] | null>(null)
const error = ref('')
const loaders = new Map<number, ThumbLoader>()
function loaderFor(year: number) {
  let l = loaders.get(year)
  if (!l) { l = makeThumbLoader(); loaders.set(year, l) }
  return l
}

const now = new Date()
const today = computed(() => {
  try { return new Intl.DateTimeFormat(locale.value, { day: 'numeric', month: 'long' }).format(now) } catch { return '' }
})

async function load() {
  error.value = ''
  try {
    groups.value = await api.onThisDay(now)
  } catch (e: any) {
    error.value = e?.data?.error || t('common.error_load')
  }
}
onMounted(load)
onBeforeUnmount(() => { for (const l of loaders.values()) l.clear() })

// The viewer walks the years as if they were months.
function open(id: string) {
  const list = groups.value ?? []
  const keys = list.map((g) => `y${g.year}`)
  emit('open', id, keys, new Map(list.map((g) => [`y${g.year}`, g.assets])))
}

// «1 год / 3 года / 5 лет назад»: the plural form by the locale's rules.
function ago(year: number) {
  const n = now.getFullYear() - year
  let form = 'other'
  try { form = new Intl.PluralRules(locale.value).select(n) } catch { /* unknown locale */ }
  if (!['one', 'few', 'many', 'other'].includes(form)) form = 'other'
  return t(`photos.years_ago_${form}`, { n })
}

defineExpose({ reload: load, monthOf: (id: string) => {
  const g = groups.value?.find((x) => x.assets.some((a) => a.id === id))
  return g ? `y${g.year}` : ''
} })
</script>

<template>
  <div class="min-h-0 flex-1 overflow-auto p-4 md:p-6">
    <p v-if="error" class="text-sm text-danger">{{ error }}</p>
    <p v-else-if="!groups" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <div v-else-if="!groups.length" class="flex h-full flex-col items-center justify-center gap-2 p-6 text-center text-sm text-muted">
      <Icon name="lucide:calendar-heart" size="28" />
      <p>{{ t('photos.memories_empty', { date: today }) }}</p>
    </div>
    <template v-else>
      <section v-for="g in groups" :key="g.year" class="mb-6">
        <h2 class="mb-2 flex items-baseline gap-2">
          <span class="text-lg font-semibold">{{ ago(g.year) }}</span>
          <span class="text-sm text-muted">{{ today }} {{ g.year }}</span>
        </h2>
        <PhotoTiles :rows="g.assets" :loader="loaderFor(g.year)" @open="open" />
      </section>
    </template>
  </div>
</template>
