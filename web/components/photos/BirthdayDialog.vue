<script setup lang="ts">
// «День рождения» of a person: a date, or none. With it every photo of theirs tells their
// age on the day.
import type { PhotoPerson } from '~/composables/usePhotos'
import { personLabel } from '~/lib/photos/people'

const props = defineProps<{ person: PhotoPerson }>()
const emit = defineEmits<{ close: []; done: [person: PhotoPerson] }>()
const { t } = useI18n()
const api = usePhotosApi()

const date = ref(props.person.birthDate ?? '')
const today = new Date().toISOString().slice(0, 10)
const busy = ref(false)
const error = ref('')
const input = ref<HTMLInputElement | null>(null)
onMounted(() => input.value?.focus())

async function save(value: string | null) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    emit('done', await api.updatePerson(props.person.id, { birthDate: value }))
  } catch (e: any) {
    error.value = e?.data?.error || t('photos.action_failed')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-4" @click.self="emit('close')">
    <form class="card w-full max-w-xs p-5" role="dialog" aria-modal="true" aria-labelledby="birthday-title" @submit.prevent="save(date || null)">
      <h2 id="birthday-title" class="mb-1 font-semibold">{{ t('photos.person_birthday') }}</h2>
      <p class="mb-3 text-sm text-muted">{{ personLabel(person, t('photos.person_unnamed')) }} · {{ t('photos.person_birthday_hint') }}</p>
      <input ref="input" v-model="date" type="date" class="input w-full" min="1850-01-01" :max="today" :aria-label="t('photos.person_birthday')" />
      <p v-if="error" class="mt-2 text-sm text-danger">{{ error }}</p>
      <div class="mt-4 flex items-center gap-2">
        <button v-if="person.birthDate" type="button" class="btn-ghost hover:!text-danger" :disabled="busy" @click="save(null)">
          {{ t('photos.person_birthday_clear') }}
        </button>
        <span class="flex-1" />
        <button type="button" class="btn-ghost" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button type="submit" class="btn-accent" :disabled="busy || !date">{{ t('common.save') }}</button>
      </div>
    </form>
  </div>
</template>
