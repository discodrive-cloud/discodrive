<script setup lang="ts">
const { t } = useI18n()
const state = useDialogState()

function ok() {
  const s = state.value
  if (!s) return
  const resolve = s.resolve
  const v = s.kind === 'prompt' ? s.value : true
  state.value = null
  resolve(v)
}

function cancel() {
  const s = state.value
  if (!s) return
  const resolve = s.resolve
  state.value = null
  resolve(s.kind === 'prompt' ? null : false)
}

useModalEscape(computed(() => !!state.value), cancel)
</script>

<template>
  <div
    v-if="state"
    class="fixed inset-0 z-[70] flex items-center justify-center bg-black/50 p-4"
    role="dialog"
    aria-modal="true"
    @click.self="cancel"
  >
    <div class="card w-full max-w-sm p-5">
      <h2 class="mb-2 font-semibold">{{ state.title }}</h2>
      <p v-if="state.message" class="mb-4 whitespace-pre-line text-sm text-muted">{{ state.message }}</p>
      <input
        v-if="state.kind === 'prompt'"
        v-model="state.value"
        class="input mb-4"
        autofocus
        @keyup.enter="ok"
      />
      <div class="flex justify-end gap-2">
        <button class="btn-ghost" @click="cancel">{{ t('dialog.cancel') }}</button>
        <button :class="state.danger ? 'btn-danger' : 'btn-accent'" @click="ok">{{ state.confirmText }}</button>
      </div>
    </div>
  </div>
</template>
