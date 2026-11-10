<script setup lang="ts">
interface FsNode { id: string; name: string; is_dir: boolean; size: number | null; version: number; mime?: string }

const { t } = useI18n()
const { request } = useApi()

const musicRoot = ref<string | null>(null)
const selected = ref<{ id: string; name: string; isDir: boolean } | null>(null)
const scanBusy = ref(false)
const scanDone = ref(false)

onMounted(async () => {
  const ms = await request<{ enabled: boolean; folder: { id: string; name: string } | null }>('/me/music')
  if (!ms.enabled || !ms.folder) { await navigateTo('/files'); return }
  musicRoot.value = ms.folder.id
})

async function rescan() {
  scanBusy.value = true
  scanDone.value = false
  try {
    await request('/me/music/scan', { method: 'POST' })
    scanDone.value = true
    setTimeout(() => { scanDone.value = false }, 3000)
  } finally {
    scanBusy.value = false
  }
}

// --- Inline file tree ---
// Audio detection comes from the server-side mime in nodeDTO — the single
// source of truth shared with the player (no client-side extension regexes).
const isAudioNode = (n: FsNode) => !!n.mime && n.mime.startsWith('audio/')

interface TreeNode extends FsNode { children?: TreeNode[] | null; expanded?: boolean; loading?: boolean }

const treeNodes = ref<TreeNode[]>([])
const treeBusy = ref(false)

async function loadChildren(parentId: string): Promise<TreeNode[]> {
  const nodes = await request<FsNode[]>(`/files?parent_id=${parentId}`)
  return nodes
    .filter((n) => n.is_dir || isAudioNode(n))
    .sort((a, b) => Number(b.is_dir) - Number(a.is_dir) || a.name.localeCompare(b.name))
    .map((n) => ({ ...n, children: n.is_dir ? null : undefined, expanded: false, loading: false }))
}

watch(musicRoot, async (id) => {
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
</script>

<template>
  <div class="flex h-full min-h-0 gap-0">
    <!-- Left pane: file tree -->
    <aside class="flex w-72 shrink-0 flex-col overflow-hidden border-r border-line">
      <!-- Toolbar -->
      <div class="flex items-center justify-between border-b border-line px-3 py-2">
        <span class="text-sm font-medium text-muted">{{ t('settings.music_section') }}</span>
        <button
          class="btn-ghost px-2 py-1"
          :title="t('music.rescan')"
          :disabled="scanBusy"
          @click="rescan"
        >
          <Icon v-if="scanBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
          <Icon v-else-if="scanDone" name="lucide:check" size="16" class="text-accent" />
          <Icon v-else name="lucide:refresh-cw" size="16" />
        </button>
      </div>
      <!-- Tree -->
      <div class="flex-1 overflow-auto p-1">
        <div v-if="treeBusy" class="px-3 py-4 text-sm text-muted">{{ t('common.loading') }}</div>
        <FileTreeList
          v-else-if="treeNodes.length"
          :nodes="treeNodes"
          :selected-id="selected?.id ?? null"
          @toggle="toggleExpand"
          @select="selectNode"
        />
        <div v-else class="px-3 py-4 text-sm text-muted">{{ t('files.folder_empty') }}</div>
      </div>
    </aside>

    <!-- Right pane -->
    <section class="min-w-0 flex-1 overflow-auto p-4">
      <!-- key: a fresh form per track, so a slow answer for the previous track cannot fill this one -->
      <MusicTagForm v-if="selected" :key="selected.id" :node="selected" />
      <div v-else class="flex h-full items-center justify-center">
        <p class="text-sm text-muted">{{ t('music.pick_hint') }}</p>
      </div>
    </section>
  </div>
</template>
