<script setup lang="ts">
// Forced password change (A.2): admin-provisioned accounts start with a temporary
// password and must set a new one before reaching the rest of the app. The route guard
// locks the user here while session.mustChangePassword is true.
definePageMeta({ layout: 'auth' })

const { t } = useI18n()
const { request, session } = useApi()

const current = ref('')
const next = ref('')
const confirm = ref('')
const error = ref('')
const busy = ref(false)

async function submit() {
  error.value = ''
  if (next.value.length < 8) {
    error.value = t('force_password.error_min_length')
    return
  }
  if (next.value !== confirm.value) {
    error.value = t('force_password.error_mismatch')
    return
  }
  busy.value = true
  try {
    await request('/me/password', {
      keepSessionOn401: true, // 401 = wrong current password, not a dead session
      method: 'PUT',
      body: { current_password: current.value, new_password: next.value },
    })
    // Flag cleared server-side; mirror it locally so the guard releases the user.
    setSession({ ...session.value, mustChangePassword: false })
    await navigateTo(session.value.role === 'admin' ? '/admin' : '/files')
  } catch (e: any) {
    error.value = e?.data?.error || t('force_password.error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="card w-full max-w-sm p-7">
    <div class="mb-6 flex items-center gap-2">
      <Icon name="lucide:key-round" class="text-accent" size="24" />
      <span class="font-mono text-xl font-semibold tracking-tight">{{ t('force_password.title') }}</span>
    </div>
    <p class="mb-5 text-sm text-muted">{{ t('force_password.intro') }}</p>
    <form class="space-y-4" @submit.prevent="submit">
      <div>
        <label class="mb-1 block text-xs text-muted">{{ t('force_password.current_label') }}</label>
        <input v-model="current" type="password" autocomplete="current-password" class="input" placeholder="••••••••" />
      </div>
      <div>
        <label class="mb-1 block text-xs text-muted">{{ t('force_password.new_label') }}</label>
        <input v-model="next" type="password" autocomplete="new-password" class="input" placeholder="••••••••" />
      </div>
      <div>
        <label class="mb-1 block text-xs text-muted">{{ t('force_password.confirm_label') }}</label>
        <input v-model="confirm" type="password" autocomplete="new-password" class="input" placeholder="••••••••" />
      </div>
      <p v-if="error" class="flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
      </p>
      <button type="submit" class="btn-accent w-full justify-center" :disabled="busy">
        <Icon v-if="busy" name="lucide:loader-circle" class="animate-spin" size="18" />
        <Icon v-else name="lucide:check" size="18" />
        {{ t('force_password.submit') }}
      </button>
    </form>
  </div>
</template>
