<script setup lang="ts">
import { safeRedirect } from '~/lib/safeRedirect'

definePageMeta({ layout: 'auth' })

const { t } = useI18n()
const route = useRoute()
const email = ref('')
const password = ref('')
const error = ref('')
const busy = ref(false)

// MFA second step
const mfaRequired = ref(false)
const mfaToken = ref('')
const mfaCode = ref('')

// Only offer the passkey button when WebAuthn is configured server-side.
const webauthnEnabled = useWebauthnEnabled()
onMounted(async () => {
  if (webauthnEnabled.value === null) {
    try {
      const r = await apiFetch<{ webauthn: boolean }>('/setup/status')
      webauthnEnabled.value = r.webauthn
    } catch { /* leave null → button stays hidden */ }
  }
})

async function submit() {
  error.value = ''
  busy.value = true
  try {
    const res = await login(email.value, password.value)
    if ('mfa_required' in res && res.mfa_required) {
      mfaToken.value = res.mfa_token
      mfaRequired.value = true
      return
    }
    await navigateAfterLogin(res.user.role)
  } catch (e: any) {
    error.value = e?.data?.error || t('login.error_login')
  } finally {
    busy.value = false
  }
}

async function submitMfa() {
  error.value = ''
  busy.value = true
  try {
    const res = await completeMfaTotp(mfaToken.value, mfaCode.value)
    await navigateAfterLogin(res.user.role)
  } catch (e: any) {
    error.value = e?.data?.error || t('twofa.error_invalid_code')
  } finally {
    busy.value = false
  }
}

async function passkeyLogin() {
  error.value = ''
  busy.value = true
  try {
    const res = await loginWithPasskey()
    await navigateAfterLogin(res.user.role)
  } catch (e: any) {
    // A dismissed/cancelled browser prompt is not an error worth shouting about.
    if (e?.name !== 'NotAllowedError') error.value = e?.data?.error || e?.message || t('login.passkey_error')
  } finally {
    busy.value = false
  }
}

async function navigateAfterLogin(role: string) {
  // Only in-app paths: an external or protocol-relative target is ignored (navigateTo
  // would throw after the session is already set and show "login failed").
  const redirect = safeRedirect(route.query.redirect)
  if (redirect) { await navigateTo(redirect); return }
  await navigateTo(role === 'admin' ? '/admin' : '/files')
}
</script>

<template>
  <div class="card w-full max-w-sm p-7">
    <div class="mb-6 flex items-center gap-2">
      <Icon name="lucide:hard-drive" class="text-accent" size="26" />
      <span class="font-mono text-2xl font-semibold tracking-tight">Disco<span class="text-accent">Drive</span></span>
    </div>

    <!-- Step 1: email + password -->
    <form v-if="!mfaRequired" class="space-y-4" @submit.prevent="submit">
      <div>
        <label class="mb-1 block text-xs text-muted">Email</label>
        <input v-model="email" type="email" autocomplete="username" class="input" placeholder="you@host" />
      </div>
      <div>
        <label class="mb-1 block text-xs text-muted">{{ t('login.password_label') }}</label>
        <input v-model="password" type="password" autocomplete="current-password" class="input" placeholder="••••••••" />
      </div>
      <p v-if="error" class="flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
      </p>
      <button type="submit" class="btn-accent w-full justify-center" :disabled="busy">
        <Icon v-if="busy" name="lucide:loader-circle" class="animate-spin" size="18" />
        <Icon v-else name="lucide:log-in" size="18" />
        {{ t('login.submit') }}
      </button>

      <template v-if="webauthnEnabled">
        <div class="flex items-center gap-3 text-xs text-muted">
          <span class="h-px flex-1 bg-line" />{{ t('login.or') }}<span class="h-px flex-1 bg-line" />
        </div>
        <button type="button" class="btn-ghost w-full justify-center" :disabled="busy" @click="passkeyLogin">
          <Icon name="lucide:fingerprint" size="18" /> {{ t('login.passkey_btn') }}
        </button>
      </template>
    </form>

    <!-- Step 2: TOTP / backup code -->
    <form v-else class="space-y-4" @submit.prevent="submitMfa">
      <div class="flex items-center gap-2 text-sm text-muted">
        <Icon name="lucide:shield-check" size="18" class="text-accent" />
        <span>{{ t('twofa.mfa_login_prompt') }}</span>
      </div>
      <div>
        <label class="mb-1 block text-xs text-muted">{{ t('twofa.mfa_code_label') }}</label>
        <input
          v-model="mfaCode"
          type="text"
          inputmode="numeric"
          autocomplete="one-time-code"
          class="input font-mono tracking-widest"
          :placeholder="t('twofa.mfa_code_ph')"
          autofocus
        />
      </div>
      <p v-if="error" class="flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
      </p>
      <button type="submit" class="btn-accent w-full justify-center" :disabled="busy || !mfaCode">
        <Icon v-if="busy" name="lucide:loader-circle" class="animate-spin" size="18" />
        <Icon v-else name="lucide:log-in" size="18" />
        {{ t('twofa.mfa_submit') }}
      </button>
      <button type="button" class="btn-ghost w-full justify-center text-xs" @click="mfaRequired = false; error = ''">
        {{ t('twofa.mfa_back') }}
      </button>
    </form>
  </div>
</template>
