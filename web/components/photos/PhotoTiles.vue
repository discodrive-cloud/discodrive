<script setup lang="ts">
// A plain grid of square tiles for short lists («В этот день», the trash): no
// virtualization, thumbnails fetched as tiles scroll near (IntersectionObserver).
import type { PhotoRow } from '~/composables/usePhotos'
import { formatDuration, thumbKey, thumbURL } from '~/composables/usePhotos'
import type { ThumbLoader } from '~/lib/photos/thumbLoader'

const props = withDefaults(defineProps<{
  rows: PhotoRow[]
  loader: ThumbLoader
  selectable?: boolean
}>(), { selectable: false })
const selection = defineModel<Set<string>>('selection', { default: () => new Set<string>() })
const emit = defineEmits<{ open: [id: string] }>()
const { t } = useI18n()

const tick = ref(0)
let raf = 0
function bump() {
  if (raf) return
  raf = requestAnimationFrame(() => { raf = 0; tick.value++ })
}
function src(r: PhotoRow) { void tick.value; return props.loader.get(thumbKey(r)) }

// Which tiles are near the viewport: those get fetched, nearest (lowest index) first.
const near = reactive(new Set<string>())
let io: IntersectionObserver | null = null
const root = ref<HTMLElement | null>(null)
function observe() {
  io?.disconnect()
  io = new IntersectionObserver((entries) => {
    for (const e of entries) {
      const id = (e.target as HTMLElement).dataset.id!
      if (e.isIntersecting) near.add(id)
      else near.delete(id)
    }
  }, { rootMargin: '400px' })
  root.value?.querySelectorAll<HTMLElement>('[data-id]').forEach((el) => io!.observe(el))
}
watch(() => props.rows, () => nextTick(observe))
watch([() => props.rows, near], () => {
  props.loader.sync(props.rows.map((r, i) => ({ r, i })).filter(({ r }) => near.has(r.id))
    .map(({ r, i }) => ({ key: thumbKey(r), url: thumbURL(r), priority: i })))
}, { deep: true })

onMounted(() => {
  props.loader.onChange = bump
  observe()
})
onBeforeUnmount(() => {
  io?.disconnect()
  if (raf) cancelAnimationFrame(raf)
  props.loader.sync([])
  props.loader.onChange = () => {}
})

function toggle(id: string) {
  const next = new Set(selection.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selection.value = next
}
function onClick(e: MouseEvent, r: PhotoRow) {
  if (props.selectable && (selection.value.size || e.metaKey || e.ctrlKey)) { toggle(r.id); return }
  emit('open', r.id)
}
</script>

<template>
  <div ref="root" class="tiles grid gap-0.5">
    <div
      v-for="r in rows"
      :key="r.id"
      :data-id="r.id"
      class="group relative aspect-square cursor-pointer overflow-hidden bg-ink/5"
      :class="selection.has(r.id) ? 'is-selected' : ''"
      @click="onClick($event, r)"
    >
      <img v-if="src(r)" :src="src(r)!" alt="" draggable="false" class="thumb h-full w-full object-cover" />
      <div v-else-if="src(r) === null" class="flex h-full w-full items-center justify-center text-muted">
        <Icon name="lucide:image-off" size="18" />
      </div>
      <span v-if="r.kind === 'video'" class="badge absolute bottom-1 right-1 text-[10px] font-semibold tabular-nums">
        {{ formatDuration(r.durationMs) || '▶' }}
      </span>
      <span v-if="r.live" class="badge absolute left-1 top-1"><Icon name="lucide:circle-dot" size="14" /></span>
      <button
        v-if="selectable"
        type="button"
        class="absolute right-1 top-1 flex h-5 w-5 items-center justify-center rounded-full border-2 border-white/90 transition"
        :class="[
          selection.has(r.id) ? 'border-accent bg-accent text-bg opacity-100' : 'bg-black/20 text-transparent',
          selection.size || selection.has(r.id) ? 'opacity-100' : 'opacity-0 group-hover:opacity-100',
        ]"
        :aria-label="t('photos.select')"
        :aria-pressed="selection.has(r.id)"
        @click.stop="toggle(r.id)"
      >
        <Icon name="lucide:check" size="12" />
      </button>
    </div>
  </div>
</template>

<style scoped>
.tiles { grid-template-columns: repeat(auto-fill, minmax(110px, 1fr)); }
@media (min-width: 768px) { .tiles { grid-template-columns: repeat(auto-fill, minmax(150px, 1fr)); } }
.thumb { transition: transform 0.12s ease; }
.is-selected .thumb { transform: scale(0.88); }
.is-selected { background: rgb(var(--c-accent) / 0.15); }
.badge { color: #fff; line-height: 1; filter: drop-shadow(0 1px 1px rgb(0 0 0 / 0.7)); pointer-events: none; }
</style>
