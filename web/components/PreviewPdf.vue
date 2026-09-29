<script setup lang="ts">
// PDF renderer: pdf.js pages on <canvas> with a selectable text layer on top.
// pdf.js is a lazy chunk; its worker is a same-origin build asset — the CSP
// (worker-src falls back to default-src 'self') stays untouched. Pages render
// on demand via IntersectionObserver so a 100+ page document stays cheap.
const props = defineProps<{ blob: Blob }>()

const { t } = useI18n()

const scroller = ref<HTMLElement>()
const busy = ref(true)
const error = ref(false)
const numPages = ref(0)
const currentPage = ref(1)
const zoom = ref(1) // multiplier on top of fit-width

let pdfjs: typeof import('pdfjs-dist') | null = null
let doc: import('pdfjs-dist').PDFDocumentProxy | null = null
let observer: IntersectionObserver | null = null
let baseScale = 1 // fit-width scale at zoom=1
let renderEpoch = 0 // bump on zoom change: pages with an older epoch re-render
// Unmounting mid-load (fast paging through a gallery) must still free the document
// and its worker: the loading task owns both until the document promise resolves.
let destroyed = false
let loadingTask: import('pdfjs-dist').PDFDocumentLoadingTask | null = null

onMounted(load)
onBeforeUnmount(() => {
  destroyed = true
  observer?.disconnect()
  if (loadingTask) void loadingTask.destroy() // also destroys doc once it exists
  else void doc?.destroy()
  loadingTask = null
  doc = null
})

async function load() {
  try {
    pdfjs = await import('pdfjs-dist')
    const workerUrl = (await import('pdfjs-dist/build/pdf.worker.min.mjs?url')).default
    pdfjs.GlobalWorkerOptions.workerSrc = workerUrl
    const data = await props.blob.arrayBuffer()
    if (destroyed) return
    const task = pdfjs.getDocument({ data })
    loadingTask = task
    const loaded = await task.promise
    if (destroyed) { void loaded.destroy(); return }
    doc = loaded
    numPages.value = doc.numPages

    // fit-width from page 1; pages of other sizes still get per-page viewports
    const first = await doc.getPage(1)
    if (destroyed) return
    const w = first.getViewport({ scale: 1 }).width
    const avail = (scroller.value?.clientWidth ?? 800) - 32
    baseScale = Math.min(2, Math.max(0.3, avail / w))

    busy.value = false
    await nextTick()
    observePages()
  } catch {
    if (destroyed) return // the task was destroyed on unmount; nothing to report
    busy.value = false
    error.value = true
  }
}

function observePages() {
  if (!scroller.value) return
  observer = new IntersectionObserver(
    (entries) => {
      for (const e of entries) {
        if (e.isIntersecting) renderPage(e.target as HTMLElement)
      }
    },
    { root: scroller.value, rootMargin: '200% 0px' },
  )
  scroller.value.querySelectorAll<HTMLElement>('[data-page]').forEach((el) => observer!.observe(el))
}

async function renderPage(el: HTMLElement) {
  if (!doc || !pdfjs) return
  const epoch = renderEpoch
  if (el.dataset.epoch === String(epoch)) return
  el.dataset.epoch = String(epoch)

  try {
    const page = await doc.getPage(Number(el.dataset.page))
    const viewport = page.getViewport({ scale: baseScale * zoom.value })
    if (epoch !== renderEpoch) return

    const canvas = el.querySelector('canvas')!
    const textDiv = el.querySelector<HTMLElement>('.textLayer')!
    const dpr = window.devicePixelRatio || 1
    canvas.width = Math.floor(viewport.width * dpr)
    canvas.height = Math.floor(viewport.height * dpr)
    el.style.width = `${Math.floor(viewport.width)}px`
    el.style.height = `${Math.floor(viewport.height)}px`
    el.style.setProperty('--scale-factor', String(viewport.scale))

    await page.render({
      canvasContext: canvas.getContext('2d')!,
      viewport,
      transform: dpr !== 1 ? [dpr, 0, 0, dpr, 0, 0] : undefined,
    }).promise

    textDiv.replaceChildren()
    const textLayer = new pdfjs.TextLayer({
      textContentSource: page.streamTextContent(),
      container: textDiv,
      viewport,
    })
    await textLayer.render()
  } catch {
    // a single broken page shouldn't kill the whole document view
  }
}

function rerenderAll() {
  renderEpoch++
  scroller.value
    ?.querySelectorAll<HTMLElement>('[data-page]')
    .forEach((el) => observer?.unobserve(el))
  observer?.disconnect()
  observePages()
}

// The render observer pre-renders 200% ahead, so "intersecting" ≠ "on screen";
// the page counter is derived from scroll position instead.
function onScroll() {
  const sc = scroller.value
  if (!sc) return
  const top = sc.scrollTop + sc.clientHeight / 3
  let page = 1
  sc.querySelectorAll<HTMLElement>('[data-page]').forEach((el) => {
    if (el.offsetTop <= top) page = Number(el.dataset.page) || page
  })
  currentPage.value = page
}

function zoomBy(f: number) {
  zoom.value = Math.min(4, Math.max(0.4, zoom.value * f))
  rerenderAll()
}
function zoomFit() {
  zoom.value = 1
  rerenderAll()
}
</script>

<template>
  <div class="flex min-h-0 flex-col">
    <div v-if="busy" class="py-10 text-center text-sm text-muted">
      <Icon name="lucide:loader-circle" class="mx-auto mb-2 block animate-spin" size="28" />
      {{ t('common.loading') }}
    </div>

    <p v-else-if="error" class="py-10 text-center text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="15" class="mr-1 inline" />
      {{ t('preview.pdf_error') }}
    </p>

    <template v-else>
      <div class="flex items-center justify-center gap-1 border-b border-line px-3 py-1.5 text-xs text-muted">
        <span class="mr-2 tabular-nums">{{ t('preview.page_of', { page: currentPage, pages: numPages }) }}</span>
        <button class="btn-ghost px-1.5 py-1" :title="t('preview.zoom_out')" @click="zoomBy(1 / 1.25)">
          <Icon name="lucide:zoom-out" size="15" />
        </button>
        <button class="btn-ghost px-1.5 py-1" :title="t('preview.zoom_fit')" @click="zoomFit">
          <Icon name="lucide:maximize" size="15" />
        </button>
        <button class="btn-ghost px-1.5 py-1" :title="t('preview.zoom_in')" @click="zoomBy(1.25)">
          <Icon name="lucide:zoom-in" size="15" />
        </button>
      </div>

      <div ref="scroller" class="relative min-h-0 flex-1 overflow-auto bg-black/20 p-4" @scroll.passive="onScroll">
        <div
          v-for="p in numPages"
          :key="p"
          :data-page="p"
          class="pdf-page relative mx-auto mb-4 bg-white shadow-md"
        >
          <canvas class="absolute inset-0 h-full w-full" />
          <div class="textLayer" />
        </div>
      </div>
    </template>
  </div>
</template>

<style>
/* Minimal pdf.js text-layer CSS: invisible selectable text positioned by the
   TextLayer API (spans are absolutely placed, sized via --scale-factor). */
.pdf-page .textLayer {
  position: absolute;
  inset: 0;
  overflow: hidden;
  line-height: 1;
  text-size-adjust: none;
  forced-color-adjust: none;
  transform-origin: 0 0;
  caret-color: CanvasText;
  z-index: 1;
}
.pdf-page .textLayer :is(span, br) {
  color: transparent;
  position: absolute;
  white-space: pre;
  cursor: text;
  transform-origin: 0% 0%;
}
.pdf-page .textLayer ::selection {
  background: rgb(var(--c-accent) / 0.35);
}
</style>
