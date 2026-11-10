<script setup lang="ts">
interface FsNode { id: string; name: string; is_dir: boolean; size: number | null; version: number }

interface BookListItem {
  id: string
  nodeId: string
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
  books: BookListItem[]
  total: number
}

interface FacetsResponse {
  authors: string[]
  series: string[]
  genres: string[]
}

const { t } = useI18n()
const { request } = useApi()

const ebookRoot = ref<string | null>(null)
const selected = ref<{ id: string; name: string; isDir: boolean } | null>(null)
const scanBusy = ref(false)
const scanDone = ref(false)

onMounted(async () => {
  const es = await request<{ enabled: boolean; folder: { id: string; name: string } | null }>('/me/ebooks')
  if (!es.enabled || !es.folder) { await navigateTo('/books'); return }
  ebookRoot.value = es.folder.id
  loadFacets()
})

async function rescan() {
  scanBusy.value = true
  scanDone.value = false
  try {
    await request('/me/ebooks/scan', { method: 'POST' })
    scanDone.value = true
    setTimeout(() => { scanDone.value = false }, 3000)
  } finally {
    scanBusy.value = false
  }
}

// --- Inline file tree ---
const BOOK_RE = /\.(epub|fb2|pdf|mobi|azw3|cbz|cbr)$/i

interface TreeNode extends FsNode { children?: TreeNode[] | null; expanded?: boolean; loading?: boolean }

const treeNodes = ref<TreeNode[]>([])
const treeBusy = ref(false)

async function loadChildren(parentId: string): Promise<TreeNode[]> {
  const nodes = await request<FsNode[]>(`/files?parent_id=${parentId}`)
  return nodes
    .filter((n) => n.is_dir || BOOK_RE.test(n.name))
    .sort((a, b) => Number(b.is_dir) - Number(a.is_dir) || a.name.localeCompare(b.name))
    .map((n) => ({ ...n, children: n.is_dir ? null : undefined, expanded: false, loading: false }))
}

watch(ebookRoot, async (id) => {
  if (!id) return
  treeBusy.value = true
  try { treeNodes.value = await loadChildren(id) } finally { treeBusy.value = false }
}, { immediate: true })

async function toggleExpand(node: TreeNode) {
  if (!node.is_dir) return
  if (node.expanded) { node.expanded = false; return }
  if (node.children == null) {
    node.loading = true
    try { node.children = await loadChildren(node.id) } finally { node.loading = false }
  }
  node.expanded = true
}

function selectNode(node: TreeNode) {
  selected.value = { id: node.id, name: node.name, isDir: node.is_dir }
}

// --- Filter bar ---
const searchQuery = ref('')
const filterAuthor = ref('')
const filterGenre = ref('')
const facets = ref<FacetsResponse>({ authors: [], series: [], genres: [] })

const filterActive = computed(() => !!(searchQuery.value.trim() || filterAuthor.value || filterGenre.value))

async function loadFacets() {
  try {
    facets.value = await request<FacetsResponse>('/me/ebooks/library/facets')
  } catch {
    // non-fatal
  }
}

// --- Filtered flat list ---
const flatBooks = ref<BookListItem[]>([])
const flatTotal = ref(0)
const flatStart = ref(0)
const flatLoading = ref(false)

let searchTimer: ReturnType<typeof setTimeout> | null = null

function onSearchInput() {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    flatStart.value = 0
    flatBooks.value = []
    loadFlatBooks()
  }, 350)
}

function buildFilterQuery(start: number) {
  const p = new URLSearchParams()
  if (searchQuery.value.trim()) p.set('q', searchQuery.value.trim())
  if (filterAuthor.value) p.set('author', filterAuthor.value)
  if (filterGenre.value) p.set('genre', filterGenre.value)
  if (start > 0) p.set('start', String(start))
  return p.toString()
}

async function loadFlatBooks(append = false) {
  flatLoading.value = true
  try {
    const q = buildFilterQuery(flatStart.value)
    const resp = await request<BookListResponse>(`/me/ebooks/library${q ? '?' + q : ''}`)
    if (append) {
      flatBooks.value = [...flatBooks.value, ...resp.books]
    } else {
      flatBooks.value = resp.books
    }
    flatTotal.value = resp.total
  } finally {
    flatLoading.value = false
  }
}

async function loadMore() {
  // start is an offset: the next page begins after everything already shown. Adding the
  // length to the previous offset skipped a page from the second click on.
  flatStart.value = flatBooks.value.length
  await loadFlatBooks(true)
}

// When filter becomes active, load; when inactive, reset list
watch(filterActive, (active) => {
  if (active) {
    flatStart.value = 0
    flatBooks.value = []
    loadFlatBooks()
  } else {
    flatBooks.value = []
    flatTotal.value = 0
    flatStart.value = 0
  }
})

// Re-fetch when author/genre selects change
function onFilterSelectChange() {
  flatStart.value = 0
  flatBooks.value = []
  if (filterActive.value) loadFlatBooks()
}

function selectFlatBook(book: BookListItem) {
  selected.value = { id: book.nodeId, name: book.title, isDir: false }
}
</script>

<template>
  <div class="flex h-full min-h-0 gap-0">
    <!-- Left pane: filter bar + tree or flat list -->
    <aside class="flex w-72 shrink-0 flex-col overflow-hidden border-r border-line">
      <!-- Toolbar -->
      <div class="flex items-center justify-between border-b border-line px-3 py-2">
        <span class="text-sm font-medium text-muted">{{ t('settings.books_section') }}</span>
        <button
          class="btn-ghost px-2 py-1"
          :title="t('bookmeta.rescan')"
          :disabled="scanBusy"
          @click="rescan"
        >
          <Icon v-if="scanBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
          <Icon v-else-if="scanDone" name="lucide:check" size="16" class="text-accent" />
          <Icon v-else name="lucide:refresh-cw" size="16" />
        </button>
      </div>

      <!-- Filter bar -->
      <div class="flex flex-col gap-1.5 border-b border-line px-2 py-2">
        <input
          v-model="searchQuery"
          type="search"
          class="input w-full text-sm"
          :placeholder="t('books_page.search_placeholder')"
          @input="onSearchInput"
        />
        <select
          v-if="facets.authors.length > 0"
          v-model="filterAuthor"
          class="input w-full text-sm"
          @change="onFilterSelectChange"
        >
          <option value="">{{ t('bookmeta.all_authors') }}</option>
          <option v-for="a in facets.authors" :key="a" :value="a">{{ a }}</option>
        </select>
        <select
          v-if="facets.genres.length > 0"
          v-model="filterGenre"
          class="input w-full text-sm"
          @change="onFilterSelectChange"
        >
          <option value="">{{ t('bookmeta.all_genres') }}</option>
          <option v-for="g in facets.genres" :key="g" :value="g">{{ g }}</option>
        </select>
      </div>

      <!-- Tree (when filter not active) -->
      <div v-if="!filterActive" class="flex-1 overflow-auto p-1">
        <div v-if="treeBusy" class="px-3 py-4 text-sm text-muted">{{ t('common.loading') }}</div>
        <FileTreeList
          v-else-if="treeNodes.length"
          :nodes="treeNodes"
          :selected-id="selected?.id ?? null"
          file-icon="lucide:book-open"
          @toggle="toggleExpand"
          @select="selectNode"
        />
        <div v-else class="px-3 py-4 text-sm text-muted">{{ t('files.folder_empty') }}</div>
      </div>

      <!-- Flat list (when filter active) -->
      <div v-else class="flex flex-1 flex-col overflow-auto">
        <div v-if="flatLoading && flatBooks.length === 0" class="px-3 py-4 text-sm text-muted">
          {{ t('common.loading') }}
        </div>
        <div v-else-if="!flatLoading && flatBooks.length === 0" class="px-3 py-4 text-sm text-muted">
          {{ t('bookmeta.list_empty') }}
        </div>
        <ul v-else class="flex-1 select-none p-1">
          <li v-for="book in flatBooks" :key="book.nodeId">
            <button
              class="flex w-full items-start gap-1.5 rounded px-2 py-1.5 text-left hover:bg-ink/5"
              :class="selected?.id === book.nodeId ? 'bg-accent/10 text-accent' : 'text-ink'"
              @click="selectFlatBook(book)"
            >
              <Icon name="lucide:book-open" size="14" class="mt-0.5 shrink-0 text-muted" />
              <div class="min-w-0 flex-1">
                <div class="truncate text-xs font-medium">{{ book.title }}</div>
                <div v-if="book.authors.length" class="truncate text-xs text-muted">
                  {{ book.authors.join(', ') }}
                </div>
              </div>
            </button>
          </li>
        </ul>
        <div v-if="flatBooks.length < flatTotal" class="border-t border-line p-2">
          <button
            class="btn-ghost w-full justify-center text-xs"
            :disabled="flatLoading"
            @click="loadMore"
          >
            <Icon v-if="flatLoading" name="lucide:loader-circle" class="animate-spin" size="14" />
            <span v-else>{{ t('bookmeta.load_more') }}</span>
          </button>
        </div>
      </div>
    </aside>

    <!-- Right pane -->
    <section class="min-w-0 flex-1 overflow-auto p-4">
      <!-- key: a fresh form per book, so a slow answer for the previous book cannot fill (and then save into) this one -->
      <BookMetaForm v-if="selected" :key="selected.id" :node="selected" />
      <div v-else class="flex h-full items-center justify-center">
        <p class="text-sm text-muted">{{ t('bookmeta.pick_hint') }}</p>
      </div>
    </section>
  </div>
</template>
