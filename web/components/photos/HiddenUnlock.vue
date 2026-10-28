<script setup lang="ts">
// Opens the gallery's Hidden part: the identity confirmation of the security settings
// (passkey if the user has one, otherwise the password and, with 2FA on, the code) gives
// an approval token for action photos:hidden, which the server trades for a short-lived
// unlock token. The token stays in this tab's memory (lib/photos/hidden).
const emit = defineEmits<{ unlocked: [] }>()
const { t } = useI18n()
const { request } = useApi()
const api = usePhotosApi()

const ACTION = 'photos:hidden'

const hasPasskeys = ref(false)
const totpEnabled = ref(false)
const ready = ref(false)
const password = ref('')
const code = ref('')
const busy = ref(false)
const error = ref('')
const showPassword = ref(false)

onMounted(async () => {
  const [pk, totp] = await Promise.allSettled([
    request<unknown[]>('/me/webauthn'),
    request<{ enabled: boolean }>('/me/totp'),
  ])
  hasPasskeys.value = pk.status === 'fulfilled' && Array.isArray(pk.value) && pk.value.length > 0 && !!window.PublicKeyCredential
  totpEnabled.value = totp.status === 'fulfilled' && !!totp.value?.enabled
  showPassword.value = !hasPasskeys.value
  ready.value = true
})

async function finish(approval: string) {
  await api.unlock(approval)
  password.value = ''
  code.value = ''
  emit('unlocked')
}

async function withPasskey() {
  busy.value = true
  error.value = ''
  try {
    const begin = await request<{ options: any; session_token: string }>('/me/identity/approval/begin', { method: 'POST', body: { action: ACTION } })
    const { get } = await import('@github/webauthn-json')
    const assertion = await get(begin.options)
    const res = await request<{ approval_token: string }>('/me/identity/approval/finish', {
      method: 'POST', keepSessionOn401: true, body: { session_token: begin.session_token, assertion },
    })
    await finish(res.approval_token)
  } catch {
    error.value = t('passkey.approval_error')
  } finally {
    busy.value = false
  }
}

async function withPassword() {
  if (!password.value) return
  busy.value = true
  error.value = ''
  try {
    const res = await request<{ approval_token: string }>('/me/identity/approval/password', {
      method: 'POST', keepSessionOn401: true, body: { action: ACTION, password: password.value, code: code.value },
    })
    await finish(res.approval_token)
  } catch {
    error.value = t('passkey.approval_error')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <div class="flex h-full items-start justify-center p-6 md:items-center">
    <div class="card w-full max-w-sm p-5">
      <div class="mb-3 flex items-center gap-2">
        <Icon name="lucide:lock" size="18" class="text-muted" />
        <h2 class="font-semibold">{{ t('photos.hidden_locked_title') }}</h2>
      </div>
      <p class="mb-4 text-sm text-muted">{{ t('photos.hidden_locked_text') }}</p>

      <div v-if="!ready" class="text-sm text-muted">{{ t('common.loading') }}</div>
      <template v-else>
        <button v-if="hasPasskeys" type="button" class="btn-accent mb-3 w-full justify-center" :disabled="busy" @click="withPasskey">
          <Icon :name="busy && !showPassword ? 'lucide:loader-circle' : 'lucide:key-round'" size="16" :class="busy && !showPassword ? 'animate-spin' : ''" />
          {{ t('passkey.approval_existing') }}
        </button>
        <button v-if="hasPasskeys && !showPassword" type="button" class="btn-ghost w-full justify-center text-xs" @click="showPassword = true">
          {{ t('photos.hidden_use_password') }}
        </button>
        <form v-if="showPassword" @submit.prevent="withPassword">
          <label class="mb-3 block text-sm">{{ t('passkey.approval_password') }}
            <input v-model="password" type="password" autocomplete="current-password" class="input mt-1" :disabled="busy" autofocus />
          </label>
          <label v-if="totpEnabled" class="mb-3 block text-sm">{{ t('passkey.approval_code') }}
            <input v-model="code" autocomplete="one-time-code" inputmode="numeric" class="input mt-1" :disabled="busy" />
          </label>
          <button type="submit" class="btn-accent w-full justify-center" :disabled="busy || !password">
            <Icon v-if="busy" name="lucide:loader-circle" size="16" class="animate-spin" />
            {{ t('photos.hidden_unlock') }}
          </button>
        </form>
        <p v-if="error" class="mt-3 text-sm text-danger" role="alert">{{ error }}</p>
      </template>
    </div>
  </div>
</template>
