<script setup lang="ts">
// Floating bar over the grid while photos are selected. Presentational: the page does the work.
const props = defineProps<{
  count: number
  allFavorite: boolean
  busy?: boolean
  // the Hidden tab: "hide" becomes "show again"
  hiddenMode?: boolean
}>()
const emit = defineEmits<{
  favorite: []
  hide: []
  delete: []
  download: []
  'edit-date': []
  'edit-place': []
  clear: []
}>()
const { t } = useI18n()

const actions = computed(() => [
  { id: 'favorite', icon: 'lucide:heart', label: props.allFavorite ? t('photos.unfavorite') : t('photos.favorite'), filled: props.allFavorite },
  { id: 'download', icon: 'lucide:download', label: t('common.download') },
  { id: 'edit-date', icon: 'lucide:calendar-clock', label: t('photos.edit_date') },
  { id: 'edit-place', icon: 'lucide:map-pin', label: t('photos.edit_place') },
  { id: 'hide', icon: props.hiddenMode ? 'lucide:eye' : 'lucide:eye-off', label: props.hiddenMode ? t('photos.unhide') : t('photos.hide') },
  { id: 'delete', icon: 'lucide:trash-2', label: t('common.delete'), danger: true },
] as const)

function run(id: (typeof actions.value)[number]['id']) {
  if (props.busy) return
  switch (id) {
    case 'favorite': emit('favorite'); break
    case 'download': emit('download'); break
    case 'edit-date': emit('edit-date'); break
    case 'edit-place': emit('edit-place'); break
    case 'hide': emit('hide'); break
    case 'delete': emit('delete'); break
  }
}
</script>

<template>
  <div class="pointer-events-none absolute inset-x-0 bottom-3 z-20 flex justify-center px-2">
    <div class="card pointer-events-auto flex max-w-full items-center gap-0.5 px-1.5 py-1 shadow-xl">
      <button type="button" class="btn-ghost px-2 py-1.5" :title="t('photos.clear_selection')" @click="emit('clear')">
        <Icon name="lucide:x" size="16" />
      </button>
      <span class="whitespace-nowrap px-1 text-sm font-medium tabular-nums">{{ t('photos.selected', { n: count }) }}</span>
      <Icon v-if="busy" name="lucide:loader-circle" class="mx-1 animate-spin text-muted" size="16" />
      <span class="mx-1 h-5 w-px bg-line" />
      <button
        v-for="a in actions"
        :key="a.id"
        type="button"
        class="btn-ghost px-2 py-1.5"
        :class="'danger' in a && a.danger ? 'hover:!text-danger' : ''"
        :title="a.label"
        :aria-label="a.label"
        :disabled="busy"
        @click="run(a.id)"
      >
        <Icon :name="a.icon" size="16" :class="'filled' in a && a.filled ? 'text-accent' : ''" />
        <span class="hidden xl:inline">{{ a.label }}</span>
      </button>
    </div>
  </div>
</template>
