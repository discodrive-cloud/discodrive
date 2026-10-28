<script setup lang="ts">
// Timeline filters: what (all / photos / videos / Live / RAW), ♡ only, which source.
import type { PhotoFilter, PhotoSource } from '~/composables/usePhotos'

defineProps<{ sources: PhotoSource[] }>()
const filter = defineModel<PhotoFilter>({ required: true })
const { t } = useI18n()

const kinds = ['all', 'photo', 'video', 'live', 'raw'] as const

function set(patch: Partial<PhotoFilter>) {
  filter.value = { ...filter.value, ...patch }
}
</script>

<template>
  <div class="flex flex-wrap items-center gap-2">
    <div class="flex overflow-hidden rounded-lg border border-line text-xs" role="group" :aria-label="t('photos.filter_kind')">
      <button
        v-for="k in kinds"
        :key="k"
        type="button"
        class="px-2.5 py-1.5 transition"
        :class="filter.kind === k ? 'bg-accent/15 font-semibold text-accent' : 'text-muted hover:bg-ink/5 hover:text-ink'"
        :aria-pressed="filter.kind === k"
        @click="set({ kind: k })"
      >{{ t(`photos.kind_${k}`) }}</button>
    </div>
    <button
      type="button"
      class="flex items-center gap-1 rounded-lg border px-2.5 py-1.5 text-xs transition"
      :class="filter.favorite ? 'border-accent/40 bg-accent/15 text-accent' : 'border-line text-muted hover:bg-ink/5 hover:text-ink'"
      :aria-pressed="filter.favorite"
      :title="t('photos.favorites_only')"
      @click="set({ favorite: !filter.favorite })"
    >
      <Icon name="lucide:heart" size="14" />
      <span class="hidden sm:inline">{{ t('photos.favorites') }}</span>
    </button>
    <select
      v-if="sources.length > 1"
      class="input w-auto max-w-[12rem] py-1.5 text-xs"
      :value="filter.source"
      :aria-label="t('photos.source')"
      @change="set({ source: ($event.target as HTMLSelectElement).value })"
    >
      <option value="">{{ t('photos.all_sources') }}</option>
      <option v-for="s in sources" :key="s.id" :value="s.id">{{ s.name }}</option>
    </select>
  </div>
</template>
