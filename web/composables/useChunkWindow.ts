import type { Ref } from 'vue'
import { chunkRanges, ChunkHeights } from '~/lib/chunkWindow'

const CHUNK = 100 // rows per chunk
const AHEAD = '1000px 0px' // chunks this close to the screen are drawn before they show
const ROW_PX = 44 // a row's height before any was measured

// useChunkWindow draws a long list a part at a time (see lib/chunkWindow). The template
// renders one container per chunk, binds it with bind(i, el) and puts either the chunk's
// rows (shown has i) or one blank block blank(i) px tall into it.
export function useChunkWindow<T>(items: Ref<T[]>) {
  const chunks = computed(() => chunkRanges(items.value.length, CHUNK).map(([a, b]) => items.value.slice(a, b)))
  const shown = shallowRef(new Set<number>([0]))
  // Bumped when a measurement changes what blank() answers, so the blocks re-render.
  const measuredTick = ref(0)
  const heights = new ChunkHeights(ROW_PX)
  const els = new Map<number, Element>()
  const indexOf = new WeakMap<Element, number>()
  let io: IntersectionObserver | null = null

  function rows(i: number) {
    return chunks.value[i]?.length ?? 0
  }

  function onCross(entries: IntersectionObserverEntry[]) {
    const next = new Set(shown.value)
    for (const e of entries) {
      const i = indexOf.get(e.target)
      if (i === undefined) continue
      if (e.isIntersecting) {
        next.add(i)
      } else if (next.has(i) && i !== 0) {
        // Leaving the screen: its rows go, a block of the height they had stays.
        heights.measured(i, rows(i), e.boundingClientRect.height)
        next.delete(i)
      }
    }
    if (next.size !== shown.value.size || [...next].some((i) => !shown.value.has(i))) shown.value = next
  }

  function bind(i: number, el: Element | null) {
    const old = els.get(i)
    if (old === el) return
    if (old) {
      io?.unobserve(old)
      els.delete(i)
    }
    if (!el) return
    els.set(i, el)
    indexOf.set(el, i)
    io?.observe(el)
  }

  function blank(i: number) {
    void measuredTick.value
    return heights.of(i, rows(i))
  }

  // Another list (another folder, or the same one reloaded): old heights describe other
  // rows. What is drawn stays drawn — the observer reports a chunk only when it crosses
  // the screen's edge, and a chunk the reader is looking at does not cross it again.
  watch(items, () => {
    heights.reset()
    const n = chunks.value.length
    shown.value = new Set([0, ...[...shown.value].filter((i) => i < n)])
  })
  // The first chunk tells how tall a row is here (phone and desktop rows differ), which
  // sizes the blocks of chunks nobody has scrolled to yet.
  watchPostEffect(() => {
    const el = els.get(0)
    const n = rows(0)
    if (!shown.value.has(0) || !el || !n) return
    const before = heights.of(1, CHUNK)
    heights.measured(0, n, el.getBoundingClientRect().height)
    if (heights.of(1, CHUNK) !== before) measuredTick.value++
  })

  onMounted(() => {
    if (typeof IntersectionObserver === 'undefined') {
      // No observer (a very old browser): draw everything, as before.
      watchEffect(() => { shown.value = new Set(chunks.value.keys()) })
      return
    }
    io = new IntersectionObserver(onCross, { rootMargin: AHEAD })
    for (const el of els.values()) io.observe(el)
  })
  onBeforeUnmount(() => io?.disconnect())

  return { chunks, shown, bind, blank }
}
