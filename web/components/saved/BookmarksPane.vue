<script setup lang="ts">
import { isHttpUrl } from '~/lib/safeUrl'
// Browser bookmark tree: the server copy that extensions two-way sync with.
// Edits here (rename/delete/add) propagate to every synced browser.
import BookmarkTree, { type BookmarkNode } from '~/components/saved/BookmarkTree.vue'

const { t } = useI18n()
const { request } = useApi()
const { confirm, prompt } = useDialog()

const nodes = ref<BookmarkNode[]>([])
const error = ref('')
const busy = ref(false)
const searchQuery = ref('')
const expanded = ref<Set<string>>(new Set())

const childrenMap = computed(() => {
  const m = new Map<string | null, BookmarkNode[]>()
  for (const n of nodes.value) {
    const key = n.parent_id ?? null
    if (!m.has(key)) m.set(key, [])
    m.get(key)!.push(n)
  }
  return m
})

// Flat list of folders, indented by depth, for the parent picker.
const folderOptions = computed(() => {
  const out: { id: string; label: string }[] = []
  const walk = (parentId: string | null, depth: number) => {
    for (const n of childrenMap.value.get(parentId) || []) {
      if (!n.is_folder) continue
      out.push({ id: n.id, label: `${'  '.repeat(depth)}${n.title || '…'}` })
      walk(n.id, depth + 1)
    }
  }
  walk(null, 0)
  return out
})

const searchResults = computed(() => {
  const q = searchQuery.value.trim().toLowerCase()
  if (!q) return null
  return nodes.value.filter(
    (n) => !n.is_folder && (n.title.toLowerCase().includes(q) || n.url.toLowerCase().includes(q)),
  )
})

async function load() {
  error.value = ''
  busy.value = true
  try {
    nodes.value = await request<BookmarkNode[]>('/me/bookmarks')
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.bm_error_load')
  } finally {
    busy.value = false
  }
}
onMounted(load)

function toggle(id: string) {
  const next = new Set(expanded.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  expanded.value = next
}

// Inline add form: a bookmark or a folder, with a parent picker.
const addMode = ref<'' | 'bookmark' | 'folder'>('')
const addURL = ref('')
const addTitle = ref('')
const addParent = ref<string>('')
const addBusy = ref(false)

function openAdd(mode: 'bookmark' | 'folder', parentId?: string) {
  addMode.value = mode
  addURL.value = ''
  addTitle.value = ''
  // Default to the folder this was opened from, otherwise the first top-level
  // folder: a node sitting in the very root gets filed wherever each browser
  // sees fit.
  addParent.value = parentId ?? folderOptions.value[0]?.id ?? ''
}

function closeAdd() {
  addMode.value = ''
  addURL.value = ''
  addTitle.value = ''
}

async function submitAdd() {
  if (addMode.value === 'bookmark' && !addURL.value.trim()) return
  if (addMode.value === 'folder' && !addTitle.value.trim()) return
  addBusy.value = true
  error.value = ''
  try {
    await request('/me/bookmarks', {
      method: 'POST',
      body: {
        is_folder: addMode.value === 'folder',
        title: addTitle.value.trim(),
        url: addMode.value === 'bookmark' ? addURL.value.trim() : '',
        ...(addParent.value ? { parent_id: addParent.value } : {}),
      },
    })
    // Expand the parent so the new node is visible right away.
    if (addParent.value) expanded.value = new Set(expanded.value).add(addParent.value)
    closeAdd()
    await load()
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.bm_error_save')
  } finally {
    addBusy.value = false
  }
}

async function rename(node: BookmarkNode) {
  const title = await prompt(t('saved.bm_rename'), node.title)
  if (title === null || title.trim() === node.title) return
  error.value = ''
  try {
    await request(`/me/bookmarks/${node.id}`, { method: 'PATCH', body: { title: title.trim() } })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.bm_error_save')
  }
}

async function remove(node: BookmarkNode) {
  const title = node.is_folder ? t('saved.bm_confirm_delete_folder') : t('saved.bm_confirm_delete')
  if (!(await confirm(title, { message: node.title || node.url, confirmText: t('common.delete'), danger: true }))) return
  error.value = ''
  try {
    await request(`/me/bookmarks/${node.id}`, { method: 'DELETE' })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.bm_error_delete')
  }
}

useModalEscape(computed(() => addMode.value !== ''), closeAdd)
</script>

<template>
  <div>
    <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <div class="mb-3 flex flex-wrap items-center gap-2">
      <input
        v-model="searchQuery"
        type="search"
        class="input min-w-0 flex-1"
        :placeholder="t('saved.bm_search_ph')"
      />
      <button class="btn-accent" @click="openAdd('bookmark')">
        <Icon name="lucide:plus" size="16" /> {{ t('saved.bm_add_bookmark') }}
      </button>
      <button class="btn-ghost" @click="openAdd('folder')">
        <Icon name="lucide:folder-plus" size="16" /> {{ t('saved.bm_add_folder') }}
      </button>
    </div>

    <div v-if="addMode" class="card mb-3 p-3">
      <div class="mb-2 text-xs font-medium text-muted">
        {{ addMode === 'folder' ? t('saved.bm_new_folder') : t('saved.bm_new_bookmark') }}
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <input
          v-if="addMode === 'bookmark'"
          v-model="addURL"
          type="url"
          class="input min-w-0 flex-1"
          :placeholder="t('saved.bm_url_ph')"
          @keyup.enter="submitAdd"
        />
        <input
          v-model="addTitle"
          class="input w-56"
          :placeholder="addMode === 'folder' ? t('saved.bm_folder_title_ph') : t('saved.bm_title_ph')"
          @keyup.enter="submitAdd"
        />
        <label class="flex items-center gap-2 text-sm text-muted">
          {{ t('saved.bm_parent') }}
          <select v-model="addParent" class="input w-auto">
            <option value="">{{ t('saved.bm_root') }}</option>
            <option v-for="f in folderOptions" :key="f.id" :value="f.id">{{ f.label }}</option>
          </select>
        </label>
        <button
          class="btn-accent"
          :disabled="addBusy || (addMode === 'bookmark' ? !addURL.trim() : !addTitle.trim())"
          @click="submitAdd"
        >
          <Icon v-if="addBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
          <Icon v-else name="lucide:check" size="16" />
          {{ t('common.create') }}
        </button>
        <button class="btn-ghost" :disabled="addBusy" @click="closeAdd">
          <Icon name="lucide:x" size="16" /> {{ t('common.cancel') }}
        </button>
      </div>
    </div>

    <div class="card overflow-hidden p-2">
      <div v-if="busy && !nodes.length" class="p-5 text-sm text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="!nodes.length" class="p-10 text-center text-sm text-muted">
        <Icon name="lucide:bookmark" size="28" class="mx-auto mb-2 block opacity-50" />
        {{ t('saved.bm_empty') }}
      </div>

      <!-- flat search results -->
      <ul v-else-if="searchResults">
        <li v-if="!searchResults.length" class="p-5 text-center text-sm text-muted">
          {{ t('saved.bm_no_results') }}
        </li>
        <li
          v-for="node in searchResults"
          :key="node.id"
          class="group flex items-center gap-2 rounded px-2 py-1.5 hover:bg-ink/5"
        >
          <SavedFavicon :src="node.has_favicon ? `/me/bookmarks/${node.id}/favicon` : ''" icon="lucide:globe" />
          <div class="min-w-0 flex-1">
            <a v-if="isHttpUrl(node.url)" :href="node.url" target="_blank" rel="noopener" class="block truncate text-sm hover:text-accent">
              {{ node.title || node.url }}
            </a>
            <span v-else class="block truncate text-sm text-muted">{{ node.title || node.url }}</span>
            <div class="truncate text-xs text-muted">{{ node.url }}</div>
          </div>
          <div class="flex shrink-0 items-center gap-0.5 opacity-0 transition group-hover:opacity-100">
            <button class="btn-ghost px-1 py-0.5" :title="t('saved.bm_rename')" @click="rename(node)">
              <Icon name="lucide:pencil" size="14" />
            </button>
            <button class="btn-ghost px-1 py-0.5" :title="t('common.delete')" @click="remove(node)">
              <Icon name="lucide:trash-2" size="14" />
            </button>
          </div>
        </li>
      </ul>

      <!-- tree -->
      <BookmarkTree
        v-else
        :parent-id="null"
        :children-map="childrenMap"
        :expanded="expanded"
        @toggle="toggle"
        @rename="rename"
        @remove="remove"
        @add-inside="(id) => openAdd('bookmark', id)"
      />
    </div>
  </div>
</template>
