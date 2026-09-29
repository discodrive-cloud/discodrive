<script setup lang="ts">
// Reader for saved articles: markdown from /me/saved/{id}/content rendered
// in a narrow reader column. Article images are external: the CSP blocks them
// (img-src 'self' data: blob:), so they are fetched through the server's
// /me/saved/{id}/image proxy with the session token and shown as blobs.
import { renderMarkdown, hydrateExternalImages } from '~/lib/preview/markdown'

interface SavedItem {
  id: string
  url: string
  title: string
  kind: string
  status: string
  has_content: boolean
  created_at: string
}

const route = useRoute()
const { t } = useI18n()
const { request } = useApi()

const item = ref<SavedItem | null>(null)
const html = ref('')
const error = ref('')
const busy = ref(true)
const body = ref<HTMLElement | null>(null)
let disposeImages: (() => void) | null = null

onMounted(async () => {
  const id = String(route.params.id)
  try {
    const [meta, md] = await Promise.all([
      request<SavedItem>(`/me/saved/${id}`),
      request<string>(`/me/saved/${id}/content`, { responseType: 'text' }),
    ])
    item.value = meta
    html.value = await renderMarkdown(stripFrontmatter(md), { allowImages: true })
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.error_content')
  } finally {
    busy.value = false
  }
  await nextTick()
  if (body.value && item.value) {
    disposeImages = hydrateExternalImages(body.value, item.value.url, async (url) => {
      const blob = await request<Blob>(`/me/saved/${id}/image`, { query: { url }, responseType: 'blob' })
      return URL.createObjectURL(blob)
    })
  }
})

onBeforeUnmount(() => disposeImages?.())

// The stored file carries a YAML frontmatter (url/title/saved) for sync & MCP
// consumers; the reader shows its own header instead.
function stripFrontmatter(md: string): string {
  if (!md.startsWith('---\n')) return md
  const end = md.indexOf('\n---\n', 4)
  return end < 0 ? md : md.slice(end + 5)
}

function savedDate(): string {
  if (!item.value?.created_at) return ''
  return new Date(item.value.created_at).toLocaleDateString()
}
</script>

<template>
  <div>
    <div class="mb-4 flex items-center gap-2">
      <NuxtLink to="/saved?tab=pocket" class="btn-ghost px-2 py-1">
        <Icon name="lucide:arrow-left" size="16" /> {{ t('saved.reader_back') }}
      </NuxtLink>
    </div>

    <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>
    <div v-else-if="busy" class="p-5 text-sm text-muted">{{ t('common.loading') }}</div>

    <div v-else class="card p-4 sm:p-6">
      <header class="docx-body mb-6 border-b border-line/50 pb-4">
        <h1 class="text-2xl font-semibold leading-tight">{{ item?.title }}</h1>
        <div class="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-sm text-muted">
          <span v-if="savedDate()">{{ savedDate() }}</span>
          <a
            v-if="item?.url"
            :href="item.url"
            target="_blank"
            rel="noopener"
            class="inline-flex items-center gap-1 text-accent hover:underline"
          >
            <Icon name="lucide:external-link" size="14" /> {{ t('saved.open_original') }}
          </a>
        </div>
      </header>
      <!-- eslint-disable-next-line vue/no-v-html — renderMarkdown escapes raw HTML (html:false) -->
      <article ref="body" class="md-body docx-body" v-html="html" />
    </div>
  </div>
</template>
