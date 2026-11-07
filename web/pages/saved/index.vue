<script setup lang="ts">
import BookmarksPane from '~/components/saved/BookmarksPane.vue'
import PocketPane from '~/components/saved/PocketPane.vue'
import LinksPane from '~/components/saved/LinksPane.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()

const tabs = [
  { id: 'bookmarks', component: BookmarksPane },
  { id: 'pocket', component: PocketPane },
  { id: 'links', component: LinksPane },
] as const

const ids = tabs.map((x) => x.id)
// Active tab is driven by ?tab=; fall back to 'bookmarks' for missing/unknown values.
const active = computed(() => {
  const q = route.query.tab
  return typeof q === 'string' && (ids as readonly string[]).includes(q) ? q : 'bookmarks'
})
const activeComponent = computed(() => tabs.find((x) => x.id === active.value)!.component)

function select(id: string) {
  router.replace({ query: { ...route.query, tab: id } })
}
</script>

<template>
  <div>
    <h1 class="mb-4 text-xl font-semibold">{{ t('nav.saved') }}</h1>

    <div class="mb-6 flex gap-1 overflow-x-auto border-b border-line">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        :class="[
          'shrink-0 border-b-2 px-4 py-2 text-sm transition',
          active === tab.id
            ? 'border-accent text-accent'
            : 'border-transparent text-muted hover:text-ink',
        ]"
        @click="select(tab.id)"
      >
        {{ t(`saved.tab_${tab.id}`) }}
      </button>
    </div>

    <component :is="activeComponent" />
  </div>
</template>
