<script setup lang="ts">
// Progress and outcome of a batch date/place edit: updated, conflicts (the file changed
// meanwhile and was left alone), failed and partial (a card of several files written in
// part). "Retry" sends failed and conflicting cards again — never partial ones in a shift,
// which would move their finished files twice.
import { retryIds, type EditResult } from '~/lib/photos/edit'

const props = defineProps<{
  progress: { done: number; total: number } | null
  result: EditResult | null
  shift: boolean
}>()
const emit = defineEmits<{ retry: [ids: string[]]; stop: [] }>()
const { t } = useI18n()

const retry = computed(() => (props.result ? retryIds(props.result, props.shift) : []))
const pct = computed(() => (props.progress && props.progress.total ? Math.round((100 * props.progress.done) / props.progress.total) : 0))
</script>

<template>
  <div v-if="progress" class="mb-3">
    <div class="mb-1 flex items-center justify-between text-xs text-muted">
      <span>{{ t('photos.edit_progress', { done: progress.done, total: progress.total }) }}</span>
      <button v-if="progress.total > 200" type="button" class="hover:text-ink" @click="emit('stop')">{{ t('photos.edit_stop') }}</button>
    </div>
    <div class="h-1.5 overflow-hidden rounded bg-ink/10">
      <div class="h-full bg-accent transition-all" :style="{ width: `${pct}%` }" />
    </div>
  </div>
  <div v-else-if="result" class="mb-3 rounded-md border border-line bg-panel2 p-3 text-sm" role="status">
    <div class="flex items-center gap-2">
      <Icon :name="result.updated.length && !retry.length && !result.partial.length ? 'lucide:check' : 'lucide:info'" size="16" class="text-accent" />
      {{ t('photos.edit_updated', { n: result.updated.length }) }}
    </div>
    <div v-if="result.conflicts.length" class="mt-1 text-warn">{{ t('photos.edit_conflicts', { n: result.conflicts.length }) }}</div>
    <div v-if="result.failed.length" class="mt-1 text-danger">{{ t('photos.edit_failed', { n: result.failed.length }) }}</div>
    <div v-if="result.partial.length" class="mt-1 text-warn">
      {{ t('photos.edit_partial', { n: result.partial.length }) }}
      <span v-if="shift" class="block text-xs text-muted">{{ t('photos.edit_partial_shift') }}</span>
    </div>
    <div v-if="result.versionBytes" class="mt-1 text-xs text-muted">{{ t('photos.edit_version_bytes', { size: formatBytes(result.versionBytes) }) }}</div>
    <button v-if="retry.length" type="button" class="btn-ghost mt-2 px-2 py-1 text-xs" @click="emit('retry', retry)">
      <Icon name="lucide:rotate-ccw" size="14" /> {{ t('photos.edit_retry', { n: retry.length }) }}
    </button>
  </div>
</template>
