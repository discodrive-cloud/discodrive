<script setup lang="ts">
// Year scrubber on the right edge of the grid (as in Immich): year labels sit where
// their photos start, so busy years get more room. Dragging (mouse or touch) shows a
// «month year» bubble; letting go jumps there. A plain click jumps too.
import { yearMarks, monthIndexAt, type MonthBlock } from '~/lib/photos/layout'
import { monthTitle } from '~/composables/usePhotos'

const props = defineProps<{
  layout: MonthBlock[]
  contentHeight: number
  scrollTop: number
  viewportH: number
}>()
const emit = defineEmits<{ jump: [month: string] }>()

const { locale } = useI18n()
const el = ref<HTMLElement | null>(null)
const h = ref(0)
let ro: ResizeObserver | null = null
onMounted(() => {
  ro = new ResizeObserver(() => { h.value = el.value?.clientHeight ?? 0 })
  if (el.value) ro.observe(el.value)
})
onBeforeUnmount(() => ro?.disconnect())

// Labels need ~14px each; the ones that would overlap are dropped (ticks stay).
const marks = computed(() => yearMarks(props.layout, props.contentHeight, h.value ? 16 / h.value : 0))
const ticks = computed(() => {
  const shown = new Set(marks.value.map((m) => m.year))
  return yearMarks(props.layout, props.contentHeight).filter((m) => !shown.has(m.year))
})

// The viewport's position on the scale.
const posFrac = computed(() => (props.contentHeight > 0 ? Math.min(1, props.scrollTop / props.contentHeight) : 0))

const active = ref(false) // dragging
const hover = ref<number | null>(null) // fraction under the mouse
const dragFrac = ref(0)

function fracOf(e: PointerEvent): number {
  const r = el.value!.getBoundingClientRect()
  return Math.min(1, Math.max(0, (e.clientY - r.top) / Math.max(1, r.height)))
}

function monthAt(frac: number): string | null {
  const i = monthIndexAt(props.layout, frac * props.contentHeight)
  return i < 0 ? null : props.layout[i].month
}

const bubbleFrac = computed(() => (active.value ? dragFrac.value : hover.value))
const bubbleText = computed(() => {
  if (bubbleFrac.value == null) return ''
  const m = monthAt(bubbleFrac.value)
  return m ? monthTitle(m, locale.value) : ''
})

function down(e: PointerEvent) {
  if (e.button !== 0 && e.pointerType === 'mouse') return
  e.preventDefault()
  el.value!.setPointerCapture(e.pointerId)
  active.value = true
  dragFrac.value = fracOf(e)
}
function move(e: PointerEvent) {
  if (active.value) dragFrac.value = fracOf(e)
  else if (e.pointerType === 'mouse') hover.value = fracOf(e)
}
function up(e: PointerEvent) {
  if (!active.value) return
  active.value = false
  dragFrac.value = fracOf(e)
  hover.value = e.pointerType === 'mouse' ? dragFrac.value : null
  const m = monthAt(dragFrac.value)
  if (m) emit('jump', m)
}
function cancel() { active.value = false }
</script>

<template>
  <div
    ref="el"
    class="scrubber group absolute bottom-0 right-0 top-0 z-10 w-8 cursor-ns-resize select-none md:w-10"
    @pointerdown="down"
    @pointermove="move"
    @pointerup="up"
    @pointercancel="cancel"
    @pointerleave="hover = null"
  >
    <!-- year labels: always on wide screens, only while touching on phones -->
    <span
      v-for="m in marks"
      :key="m.year"
      class="absolute right-1.5 -translate-y-px font-mono text-[10px] leading-none text-muted transition-opacity md:opacity-100"
      :class="active ? 'opacity-100' : 'opacity-0'"
      :style="{ top: `${m.frac * 100}%` }"
    >{{ m.year }}</span>
    <span
      v-for="m in ticks"
      :key="`t${m.year}`"
      class="absolute right-2 h-px w-1.5 bg-muted/40"
      :style="{ top: `${m.frac * 100}%` }"
    />

    <!-- where the viewport is -->
    <span
      class="absolute right-0 h-0.5 w-5 rounded bg-accent/70 md:w-7"
      :style="{ top: `${posFrac * 100}%` }"
    />

    <!-- drag / hover bubble -->
    <template v-if="bubbleFrac != null && bubbleText">
      <span
        class="pointer-events-none absolute right-0 h-0.5 w-8 bg-accent md:w-10"
        :style="{ top: `${bubbleFrac * 100}%` }"
      />
      <span
        class="pointer-events-none absolute right-9 -translate-y-1/2 whitespace-nowrap rounded-md bg-accent px-2 py-1 text-xs font-semibold text-bg shadow-lg md:right-11"
        :style="{ top: `${bubbleFrac * 100}%` }"
      >{{ bubbleText }}</span>
    </template>
  </div>
</template>

<style scoped>
.scrubber { touch-action: none; }
</style>
