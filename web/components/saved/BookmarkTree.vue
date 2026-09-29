<script setup lang="ts">
import { isHttpUrl } from '~/lib/safeUrl'
// One level of the bookmark tree; recurses into expanded folders (Vue SFCs can
// reference themselves by file name). All state lives in BookmarksPane.
export interface BookmarkNode {
  id: string
  parent_id: string | null
  is_folder: boolean
  title: string
  url: string
  position: number
  has_favicon: boolean
}

defineProps<{
  parentId: string | null
  childrenMap: Map<string | null, BookmarkNode[]>
  expanded: Set<string>
}>()

const { t } = useI18n()

const emit = defineEmits<{
  toggle: [id: string]
  rename: [node: BookmarkNode]
  remove: [node: BookmarkNode]
  addInside: [id: string]
}>()

// javascript:/about:/data: bookmarks are synced as data but never rendered as
// clickable links (isHttpUrl, shared with the search results and Pocket).
</script>

<template>
  <ul>
    <li v-for="node in childrenMap.get(parentId) || []" :key="node.id">
      <div class="group flex items-center gap-2 rounded px-2 py-1.5 hover:bg-ink/5">
        <!-- folder: toggle; bookmark: favicon -->
        <button v-if="node.is_folder" class="shrink-0" @click="emit('toggle', node.id)">
          <Icon
            :name="expanded.has(node.id) ? 'lucide:folder-open' : 'lucide:folder'"
            size="18"
            class="text-accent/80"
          />
        </button>
        <SavedFavicon
          v-else
          :src="node.has_favicon ? `/me/bookmarks/${node.id}/favicon` : ''"
          icon="lucide:globe"
        />

        <div class="min-w-0 flex-1">
          <button
            v-if="node.is_folder"
            class="block max-w-full truncate text-sm font-medium"
            @click="emit('toggle', node.id)"
          >{{ node.title || '…' }}</button>
          <a
            v-else-if="isHttpUrl(node.url)"
            :href="node.url"
            target="_blank"
            rel="noopener"
            class="block truncate text-sm hover:text-accent"
            :title="node.url"
          >{{ node.title || node.url }}</a>
          <span v-else class="block truncate text-sm text-muted" :title="node.url">
            {{ node.title || node.url }}
          </span>
        </div>

        <div class="flex shrink-0 items-center gap-0.5 opacity-0 transition group-hover:opacity-100">
          <button
            v-if="node.is_folder"
            class="btn-ghost px-1 py-0.5"
            :title="t('saved.bm_add_here')"
            @click="emit('addInside', node.id)"
          >
            <Icon name="lucide:plus" size="14" />
          </button>
          <button class="btn-ghost px-1 py-0.5" :title="t('saved.bm_rename')" @click="emit('rename', node)">
            <Icon name="lucide:pencil" size="14" />
          </button>
          <button class="btn-ghost px-1 py-0.5" :title="t('common.delete')" @click="emit('remove', node)">
            <Icon name="lucide:trash-2" size="14" />
          </button>
        </div>
      </div>

      <div v-if="node.is_folder && expanded.has(node.id)" class="ml-5 border-l border-line/40 pl-1">
        <BookmarkTree
          :parent-id="node.id"
          :children-map="childrenMap"
          :expanded="expanded"
          @toggle="(id) => emit('toggle', id)"
          @rename="(n) => emit('rename', n)"
          @remove="(n) => emit('remove', n)"
          @add-inside="(id) => emit('addInside', id)"
        />
      </div>
    </li>
  </ul>
</template>

