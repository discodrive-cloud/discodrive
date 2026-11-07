<script setup lang="ts">
import { isHttpUrl } from '~/lib/safeUrl'
import { relativeDate, sourceLabel } from '~/lib/linkSource'
import LinkThumb from '~/components/saved/LinkThumb.vue'

// Links: the inbox of links shared from phones ("Share → DiscoDrive"). The user sorts
// through it here: open, move to the archive (or back), delete. No search by design.
interface Link {
  id: string
  url: string
  host: string
  source: string
  title: string
  author: string
  has_thumb: boolean
  saved_at: string
  archived: boolean
}
interface Page {
  items: Link[]
  inbox_count: number
  sources: string[]
}

const { t, locale } = useI18n()
const { request } = useApi()

const archived = ref(false)
const source = ref('')
const page = ref<Page>({ items: [], inbox_count: 0, sources: [] })
const busy = ref(false)
const error = ref('')

async function load() {
  busy.value = true
  error.value = ''
  try {
    const p = new URLSearchParams({ archived: archived.value ? '1' : '0' })
    if (source.value) p.set('source', source.value)
    page.value = await request<Page>(`/me/links?${p.toString()}`)
    // A source filter that no longer has links falls back to all.
    if (source.value && !page.value.sources.includes(source.value)) {
      source.value = ''
      await load()
    }
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.links_error_load')
  } finally {
    busy.value = false
  }
}
onMounted(load)

function showArchive(v: boolean) {
  if (archived.value === v) return
  archived.value = v
  source.value = ''
  load()
}
function pickSource(s: string) {
  source.value = s
  load()
}

function chipLabel(s: string): string {
  return sourceLabel(s) || t('saved.links_other')
}

// The row's subtitle: who posted it (when the page said), then when it was saved. For an
// unknown site the real host comes first: it is read from the link, while the author is
// whatever the page claims, and a page must not pass itself off as another site.
function subtitle(l: Link): string {
  const when = relativeDate(l.saved_at, new Date(), locale.value)
  const who = l.source === 'other' ? [l.host, l.author] : [l.author || l.host]
  return [...who, when].filter(Boolean).join(' · ')
}

const badges: Record<string, { text: string; cls: string }> = {
  instagram: { text: 'IG', cls: 'bg-gradient-to-tr from-[#f58529] via-[#dd2a7b] to-[#8134af] text-white' },
  threads: { text: '@', cls: 'bg-black text-white dark:bg-white dark:text-black' },
  reddit: { text: 'r/', cls: 'bg-[#ff4500] text-white' },
  youtube: { text: '▶', cls: 'bg-[#e00] text-white' },
  x: { text: 'X', cls: 'bg-black text-white dark:bg-white dark:text-black' },
  tiktok: { text: '♪', cls: 'bg-black text-white dark:bg-white dark:text-black' },
}

async function setArchived(l: Link, v: boolean) {
  error.value = ''
  try {
    await request(`/me/links/${l.id}`, { method: 'PATCH', body: { archived: v } })
    drop(l)
    page.value.inbox_count += v ? -1 : 1
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.links_error_action')
  }
}

async function remove(l: Link) {
  error.value = ''
  try {
    await request(`/me/links/${l.id}`, { method: 'DELETE' })
    drop(l)
    if (!l.archived) page.value.inbox_count -= 1
  } catch (e: any) {
    error.value = e?.data?.error || t('saved.links_error_action')
  }
}

function drop(l: Link) {
  page.value.items = page.value.items.filter((x) => x.id !== l.id)
  if (source.value && !page.value.items.some((x) => x.source === source.value)) {
    source.value = ''
    load()
  }
}
</script>

<template>
  <div>
    <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <div class="mb-3 flex flex-wrap items-center gap-2">
      <div class="flex overflow-hidden rounded-lg border border-line text-sm">
        <button
          :class="['px-3 py-1.5 transition', !archived ? 'bg-accent/15 text-accent' : 'text-muted hover:text-ink']"
          @click="showArchive(false)"
        >{{ t('saved.links_inbox') }} · {{ page.inbox_count }}</button>
        <button
          :class="['border-l border-line px-3 py-1.5 transition', archived ? 'bg-accent/15 text-accent' : 'text-muted hover:text-ink']"
          @click="showArchive(true)"
        >{{ t('saved.links_archive') }}</button>
      </div>
      <template v-if="page.sources.length > 1 || source">
        <button
          :class="['rounded-full border px-3 py-1 text-xs transition', !source ? 'border-accent bg-accent/15 text-accent' : 'border-line text-muted hover:text-ink']"
          @click="pickSource('')"
        >{{ t('saved.links_all') }}</button>
        <button
          v-for="s in page.sources"
          :key="s"
          :class="['rounded-full border px-3 py-1 text-xs transition', source === s ? 'border-accent bg-accent/15 text-accent' : 'border-line text-muted hover:text-ink']"
          @click="pickSource(s)"
        >{{ chipLabel(s) }}</button>
      </template>
    </div>

    <div class="card overflow-hidden">
      <div v-if="busy && !page.items.length" class="p-5 text-sm text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="!page.items.length" class="p-10 text-center text-sm text-muted">
        <Icon name="lucide:link" size="28" class="mx-auto mb-2 block opacity-50" />
        {{ archived ? t('saved.links_empty_archive') : t('saved.links_empty_inbox') }}
      </div>
      <ul v-else>
        <li
          v-for="l in page.items"
          :key="l.id"
          class="flex items-center gap-3 border-b border-line/50 px-4 py-2.5 last:border-0 hover:bg-ink/5"
        >
          <span
            v-if="badges[l.source]"
            class="flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-[11px] font-semibold"
            :class="badges[l.source].cls"
            :title="sourceLabel(l.source)"
          >{{ badges[l.source].text }}</span>
          <Icon v-else name="lucide:globe" size="20" class="w-6 shrink-0 text-muted" />
          <LinkThumb :id="l.id" :has-thumb="l.has_thumb" />
          <component
            :is="isHttpUrl(l.url) ? 'a' : 'div'"
            class="min-w-0 flex-1"
            v-bind="isHttpUrl(l.url) ? { href: l.url, target: '_blank', rel: 'noopener noreferrer' } : {}"
          >
            <div class="truncate text-sm">{{ l.title || l.host || l.url }}</div>
            <div class="truncate text-xs text-muted">{{ subtitle(l) }}</div>
          </component>
          <div class="flex shrink-0 items-center gap-1">
            <a
              v-if="isHttpUrl(l.url)"
              class="btn-ghost px-1.5 py-1"
              :href="l.url"
              target="_blank"
              rel="noopener noreferrer"
              :title="t('saved.links_open')"
            ><Icon name="lucide:external-link" size="15" /></a>
            <button
              v-if="!l.archived"
              class="btn-ghost px-1.5 py-1"
              :title="t('saved.links_to_archive')"
              @click="setArchived(l, true)"
            ><Icon name="lucide:archive" size="15" /></button>
            <button
              v-else
              class="btn-ghost px-1.5 py-1"
              :title="t('saved.links_to_inbox')"
              @click="setArchived(l, false)"
            ><Icon name="lucide:inbox" size="15" /></button>
            <button
              class="btn-ghost px-1.5 py-1"
              :title="t('common.delete')"
              @click="remove(l)"
            ><Icon name="lucide:x" size="15" /></button>
          </div>
        </li>
      </ul>
    </div>
  </div>
</template>
