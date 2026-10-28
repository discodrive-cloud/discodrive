<script setup lang="ts">
// Capture date of one card or a selection: set a date, time and zone (by default the
// photo's own), or — the usual fix for a camera with a wrong clock — shift every card by
// ±N hours and minutes from its own date. Edits go into the files as new versions, which
// the warning sizes up front.
import type { MetadataEdit } from '~/composables/usePhotos'
import { TZ_OFFSETS, offsetLabel, takenAtFor, wallTime, shiftMinutes, type EditResult } from '~/lib/photos/edit'
import EditSummary from '~/components/photos/EditSummary.vue'

const props = defineProps<{ ids: string[] }>()
const emit = defineEmits<{ close: []; done: [result: EditResult] }>()
const { t } = useI18n()

const edit = usePhotoEdit(() => props.ids)
const single = computed(() => props.ids.length === 1)

const mode = ref<'set' | 'shift'>(props.ids.length > 1 ? 'shift' : 'set')
const date = ref('')
const time = ref('')
const tz = ref<string>('unknown') // minutes as a string, or 'unknown'
const sign = ref<1 | -1>(1)
const hours = ref(0)
const minutes = ref(0)
const error = ref('')

onMounted(async () => {
  await edit.loadCards()
  // Defaults: the (first) card's own date and zone.
  const a = edit.assets.value?.[0]
  if (a) {
    const w = wallTime(a.takenAt, a.tzOffsetMin)
    if (w) { date.value = w.date; time.value = w.time }
    tz.value = a.tzOffsetMin == null ? 'unknown' : String(a.tzOffsetMin)
  }
})

const tzOptions = computed(() => {
  const list = TZ_OFFSETS.map((m) => ({ value: String(m), label: offsetLabel(m) }))
  // An odd offset from a file stays selectable.
  if (tz.value !== 'unknown' && !TZ_OFFSETS.includes(Number(tz.value))) list.push({ value: tz.value, label: offsetLabel(Number(tz.value)) })
  return list
})

const shift = computed(() => shiftMinutes(sign.value, Number(hours.value), Number(minutes.value)))

const canApply = computed(() => {
  if (edit.running.value) return false
  if (mode.value === 'shift') return shift.value !== 0
  return !!takenAtFor(date.value, time.value, tz.value === 'unknown' ? null : Number(tz.value))
})

async function apply(ids = props.ids) {
  error.value = ''
  let body: MetadataEdit
  if (mode.value === 'shift') {
    body = { shiftMinutes: shift.value }
  } else {
    const off = tz.value === 'unknown' ? null : Number(tz.value)
    const takenAt = takenAtFor(date.value, time.value, off)
    if (!takenAt) { error.value = t('photos.date_invalid'); return }
    body = { takenAt, ...(off == null ? {} : { tzOffsetMin: off }) }
  }
  const r = await edit.run(ids, body)
  emit('done', r)
  if (single.value && r.updated.length === 1 && !r.partial.length) emit('close')
}

function retry() {
  edit.retry().then((r) => { if (r) emit('done', r) })
}

function close() {
  if (edit.running.value) return
  emit('close')
}
useModalEscape(computed(() => true), close)
</script>

<template>
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-4" @click.self="close">
    <form class="card w-full max-w-md p-5" role="dialog" aria-modal="true" aria-labelledby="photo-date-title" @submit.prevent="apply()">
      <h2 id="photo-date-title" class="mb-1 font-semibold">{{ t('photos.edit_date') }}</h2>
      <p v-if="!single" class="mb-3 text-sm text-muted">{{ t('photos.selected', { n: ids.length }) }}</p>

      <div v-if="!single && !edit.result.value && !edit.running.value" class="mb-4 flex gap-1 rounded-md bg-ink/5 p-1 text-sm">
        <button type="button" class="flex-1 rounded px-2 py-1" :class="mode === 'shift' ? 'bg-panel text-ink shadow' : 'text-muted'" @click="mode = 'shift'">{{ t('photos.date_mode_shift') }}</button>
        <button type="button" class="flex-1 rounded px-2 py-1" :class="mode === 'set' ? 'bg-panel text-ink shadow' : 'text-muted'" @click="mode = 'set'">{{ t('photos.date_mode_set') }}</button>
      </div>

      <div v-if="mode === 'set'" class="mb-3 grid grid-cols-2 gap-3">
        <label class="block text-sm">{{ t('photos.date') }}
          <input v-model="date" type="date" class="input mt-1" required />
        </label>
        <label class="block text-sm">{{ t('photos.time') }}
          <input v-model="time" type="time" class="input mt-1" required />
        </label>
        <label class="col-span-2 block text-sm">{{ t('photos.timezone') }}
          <select v-model="tz" class="input mt-1">
            <option value="unknown">{{ t('photos.tz_unknown') }}</option>
            <option v-for="o in tzOptions" :key="o.value" :value="o.value">{{ o.label }}</option>
          </select>
        </label>
      </div>

      <div v-else class="mb-3">
        <p class="mb-2 text-xs text-muted">{{ t('photos.date_shift_hint') }}</p>
        <div class="flex items-end gap-2">
          <select v-model.number="sign" class="input w-20" :aria-label="t('photos.date_shift_direction')">
            <option :value="1">+</option>
            <option :value="-1">−</option>
          </select>
          <label class="block flex-1 text-sm">{{ t('photos.hours') }}
            <input v-model.number="hours" type="number" min="0" max="876000" class="input mt-1" />
          </label>
          <label class="block flex-1 text-sm">{{ t('photos.minutes') }}
            <input v-model.number="minutes" type="number" min="0" max="59" class="input mt-1" />
          </label>
        </div>
      </div>

      <!-- the cost: every edited JPEG/HEIC becomes a new version of its file -->
      <p class="mb-3 flex gap-2 rounded-md bg-warn/10 p-2 text-xs text-warn">
        <Icon name="lucide:triangle-alert" size="14" class="mt-0.5 shrink-0" />
        <span v-if="edit.loadingCards.value">{{ t('common.loading') }}</span>
        <span v-else-if="edit.estimate.value !== null">
          {{ t('photos.edit_versions_warning', { size: formatBytes(edit.estimate.value) }) }}
          <template v-if="edit.sidecars.value"> {{ t('photos.edit_sidecar_note', { n: edit.sidecars.value }) }}</template>
        </span>
        <span v-else>{{ t('photos.edit_versions_generic') }}</span>
      </p>

      <EditSummary :progress="edit.progress.value" :result="edit.result.value" :shift="edit.resultShift.value" @retry="retry" @stop="edit.stop()" />
      <p v-if="error" class="mb-3 text-sm text-danger" role="alert">{{ error }}</p>

      <div class="flex justify-end gap-2">
        <button type="button" class="btn-ghost" :disabled="edit.running.value" @click="close">{{ edit.result.value ? t('common.close') : t('dialog.cancel') }}</button>
        <button v-if="!edit.result.value" type="submit" class="btn-accent disabled:cursor-not-allowed disabled:opacity-50" :disabled="!canApply">
          <Icon v-if="edit.running.value" name="lucide:loader-circle" size="16" class="animate-spin" />
          {{ t('photos.apply') }}
        </button>
      </div>
    </form>
  </div>
</template>
