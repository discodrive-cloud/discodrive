<script setup lang="ts">
interface BookDTO {
  id: string
  title: string
  authors: string[]
  series?: string
  seriesIndex?: number
  language?: string
  format: string
  tags: string[]
  hasCover: boolean
}

interface BookListResponse {
  books: BookDTO[]
  total: number
}

interface FacetsResponse {
  authors: string[]
  series: string[]
  genres: string[]
}

const { t } = useI18n()
const { request } = useApi()

// Filter state
const searchQuery = ref('')
const filterAuthor = ref('')
const filterSeries = ref('')
const filterGenre = ref('')
const start = ref(0)

// Data
const books = ref<BookDTO[]>([])
const total = ref(0)
const facets = ref<FacetsResponse>({ authors: [], series: [], genres: [] })
const coverUrls = ref<Record<string, string>>({})
const error = ref('')
const loading = ref(false)

// Debounce search input
let searchTimer: ReturnType<typeof setTimeout> | null = null
function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    start.value = 0
    loadBooks()
  }, 350)
}

function buildQuery() {
  const p = new URLSearchParams()
  if (searchQuery.value.trim()) p.set('q', searchQuery.value.trim())
  else {
    if (filterAuthor.value) p.set('author', filterAuthor.value)
    if (filterSeries.value) p.set('series', filterSeries.value)
    if (filterGenre.value) p.set('genre', filterGenre.value)
  }
  if (start.value > 0) p.set('start', String(start.value))
  return p.toString()
}

async function loadBooks() {
  error.value = ''
  loading.value = true
  try {
    const q = buildQuery()
    const resp = await request<BookListResponse>(`/me/ebooks/library${q ? '?' + q : ''}`)
    books.value = resp.books
    total.value = resp.total
    // Fetch covers for books that have one
    await loadCovers(resp.books)
  } catch (e: any) {
    error.value = e?.data?.error || t('books_page.error_load')
  } finally {
    loading.value = false
  }
}

// Load blob covers in the background, keyed by book id.
// Covers are fetched through the authed same-origin endpoint (CSP blocks external img hosts;
// Bearer auth cannot be carried by a plain <img src>, so we use fetch → blob → objectURL).
async function loadCovers(list: BookDTO[]) {
  const next: Record<string, string> = {}
  await Promise.all(
    list
      .filter((b) => b.hasCover)
      .map(async (b) => {
        try {
          const blob = await request<Blob>(`/me/ebooks/library/${b.id}/cover`, { responseType: 'blob' })
          next[b.id] = URL.createObjectURL(blob)
        } catch {
          // no cover available
        }
      }),
  )
  // Revoke old object URLs to avoid memory leaks
  for (const url of Object.values(coverUrls.value)) {
    URL.revokeObjectURL(url)
  }
  coverUrls.value = next
}

async function loadFacets() {
  try {
    facets.value = await request<FacetsResponse>('/me/ebooks/library/facets')
  } catch {
    // non-fatal: filters stay empty
  }
}

function applyFilter() {
  start.value = 0
  searchQuery.value = ''
  loadBooks()
}

function clearFilters() {
  filterAuthor.value = ''
  filterSeries.value = ''
  filterGenre.value = ''
  searchQuery.value = ''
  start.value = 0
  loadBooks()
}

const saveFile = useDownload()
async function downloadBook(b: BookDTO) {
  try {
    await saveFile(`/me/ebooks/library/${b.id}/download`, `${b.title}.${b.format}`)
  } catch (e: any) {
    error.value = e?.data?.error || t('books_page.error_download')
  }
}

const hasFilters = computed(
  () => filterAuthor.value || filterSeries.value || filterGenre.value || searchQuery.value.trim(),
)

onMounted(() => {
  loadFacets()
  loadBooks()
})

// Revoke all object URLs on unmount to avoid leaks
onUnmounted(() => {
  for (const url of Object.values(coverUrls.value)) {
    URL.revokeObjectURL(url)
  }
})
</script>

<template>
  <div>
    <div class="mb-6">
      <h1 class="text-xl font-semibold">{{ t('books_page.title') }}</h1>
    </div>

    <!-- Search and filter bar -->
    <div class="mb-5 flex flex-wrap gap-3">
      <input
        v-model="searchQuery"
        type="search"
        class="input min-w-0 flex-1"
        :placeholder="t('books_page.search_placeholder')"
        @input="onSearchInput"
      />

      <select
        v-if="facets.authors.length > 0"
        v-model="filterAuthor"
        class="input w-auto"
        @change="applyFilter"
      >
        <option value="">{{ t('books_page.filter_author') }}: {{ t('books_page.filter_all') }}</option>
        <option v-for="a in facets.authors" :key="a" :value="a">{{ a }}</option>
      </select>

      <select
        v-if="facets.series.length > 0"
        v-model="filterSeries"
        class="input w-auto"
        @change="applyFilter"
      >
        <option value="">{{ t('books_page.filter_series') }}: {{ t('books_page.filter_all') }}</option>
        <option v-for="s in facets.series" :key="s" :value="s">{{ s }}</option>
      </select>

      <select
        v-if="facets.genres.length > 0"
        v-model="filterGenre"
        class="input w-auto"
        @change="applyFilter"
      >
        <option value="">{{ t('books_page.filter_genre') }}: {{ t('books_page.filter_all') }}</option>
        <option v-for="g in facets.genres" :key="g" :value="g">{{ g }}</option>
      </select>

      <button v-if="hasFilters" class="btn-ghost" @click="clearFilters">
        <Icon name="lucide:x" size="16" class="mr-1" />
        {{ t('books_page.btn_clear') }}
      </button>
    </div>

    <!-- Error banner -->
    <div v-if="error" class="mb-4 rounded-lg bg-red-500/10 px-4 py-3 text-sm text-red-500">
      {{ error }}
    </div>

    <!-- Loading -->
    <div v-if="loading && books.length === 0" class="py-12 text-center text-muted">
      {{ t('books_page.loading') }}
    </div>

    <!-- Empty state -->
    <div v-else-if="!loading && books.length === 0" class="py-12 text-center">
      <div class="mb-2 text-muted">{{ t('books_page.empty') }}</div>
      <div v-if="!hasFilters" class="text-sm text-muted">{{ t('books_page.empty_hint') }}</div>
    </div>

    <!-- Book grid -->
    <div
      v-else
      class="grid grid-cols-2 gap-4 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6"
    >
      <div
        v-for="book in books"
        :key="book.id"
        class="flex flex-col overflow-hidden rounded-lg border border-line bg-panel"
      >
        <!-- Cover image -->
        <div class="relative aspect-[2/3] w-full bg-ink/5">
          <img
            v-if="coverUrls[book.id]"
            :src="coverUrls[book.id]"
            :alt="t('books_page.cover_alt')"
            class="h-full w-full object-cover"
          />
          <!-- Placeholder when no cover -->
          <div
            v-else
            class="flex h-full w-full items-center justify-center"
          >
            <Icon name="lucide:book-open" size="40" class="text-muted/40" />
          </div>
        </div>

        <!-- Book info -->
        <div class="flex flex-1 flex-col p-2">
          <div class="mb-1 line-clamp-2 text-xs font-medium leading-tight">
            {{ book.title }}
          </div>
          <div v-if="book.authors.length > 0" class="mb-1 line-clamp-1 text-xs text-muted">
            {{ book.authors.join(', ') }}
          </div>
          <div v-if="book.series" class="mb-1 line-clamp-1 text-xs text-muted italic">
            {{ book.series }}<span v-if="book.seriesIndex"> #{{ book.seriesIndex }}</span>
          </div>
          <!-- Tags -->
          <div v-if="book.tags.length > 0" class="mb-2 flex flex-wrap gap-1">
            <span
              v-for="tag in book.tags.slice(0, 3)"
              :key="tag"
              class="rounded bg-accent/10 px-1 py-0.5 text-xs text-accent"
            >
              {{ tag }}
            </span>
          </div>
          <div class="mt-auto">
            <button
              class="btn-ghost w-full justify-center py-1 text-xs"
              :title="t('books_page.btn_download')"
              @click="downloadBook(book)"
            >
              <Icon name="lucide:download" size="14" class="mr-1" />
              {{ t('books_page.btn_download') }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
