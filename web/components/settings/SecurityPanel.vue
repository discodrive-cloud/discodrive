<script setup lang="ts">
import QRCode from 'qrcode'

const { t } = useI18n()
const { request } = useApi()
const { confirm, prompt } = useDialog()
const { copied, copyText } = useCopy()

const error = ref('')

// Password change
const pwCurrent = ref('')
const pwNew = ref('')
const pwConfirm = ref('')
const pwBusy = ref(false)
const pwOk = ref('')

async function changePassword() {
  pwOk.value = ''
  error.value = ''
  if (pwNew.value.length < 8) {
    error.value = t('settings.error_min_length')
    return
  }
  if (pwNew.value !== pwConfirm.value) {
    error.value = t('settings.error_mismatch')
    return
  }
  pwBusy.value = true
  try {
    await request('/me/password', {
      method: 'PUT',
      body: { current_password: pwCurrent.value, new_password: pwNew.value },
    })
    pwCurrent.value = ''
    pwNew.value = ''
    pwConfirm.value = ''
    pwOk.value = t('settings.password_changed')
  } catch (e: any) {
    error.value = e?.data?.error || t('settings.error_change_password')
  } finally {
    pwBusy.value = false
  }
}

// Session lifetime. The session slides — every request renews the token — so this is
// how long the browser may sit idle before the next visit needs a fresh sign-in.
// 0 = never expires; the server then issues a token without an expiry.
const SESSION_OPTIONS: { minutes: number; key: string }[] = [
  { minutes: 60, key: 'session.opt_1h' },
  { minutes: 240, key: 'session.opt_4h' },
  { minutes: 480, key: 'session.opt_8h' },
  { minutes: 1440, key: 'session.opt_1d' },
  { minutes: 10080, key: 'session.opt_7d' },
  { minutes: 43200, key: 'session.opt_30d' },
  { minutes: 0, key: 'session.opt_never' },
]
const sessionTtl = ref<number | null>(null)
const sessionBusy = ref(false)
const sessionError = ref('')
const sessionOk = ref('')

async function loadSessionTtl() {
  try {
    const res = await request<{ ttl_minutes: number }>('/me/session')
    sessionTtl.value = res.ttl_minutes
  } catch { /* silent */ }
}
onMounted(loadSessionTtl)

async function saveSessionTtl(e: Event) {
  const minutes = Number((e.target as HTMLSelectElement).value)
  sessionError.value = ''
  sessionOk.value = ''
  sessionBusy.value = true
  try {
    await request('/me/session', { method: 'PUT', body: { ttl_minutes: minutes } })
    sessionTtl.value = minutes
    sessionOk.value = t('session.saved')
  } catch (err: any) {
    sessionError.value = err?.data?.error || t('session.error_save')
  } finally {
    sessionBusy.value = false
  }
}

// Two-factor authentication (TOTP)
const totpEnabled = ref<boolean | null>(null)
const totpBusy = ref(false)
const totpError = ref('')
// Setup flow
const totpStep = ref<'idle' | 'setup' | 'backups'>('idle')
const totpQr = ref('')
const totpSecret = ref('')
const totpCode = ref('')
const totpBackupCodes = ref<string[]>([])
// Disable flow
const totpDisabling = ref(false)
const totpDisablePassword = ref('')
const totpDisableCode = ref('')

async function loadTotp() {
  try {
    const res = await request<{ enabled: boolean }>('/me/totp')
    totpEnabled.value = res.enabled
  } catch { /* silent */ }
}
onMounted(loadTotp)

async function startTotpSetup() {
  totpError.value = ''
  totpBusy.value = true
  try {
    const res = await request<{ otpauth_url: string; secret: string }>('/me/totp/setup', { method: 'POST' })
    totpSecret.value = res.secret
    totpQr.value = await QRCode.toDataURL(res.otpauth_url, { width: 200, margin: 1 })
    totpStep.value = 'setup'
    totpCode.value = ''
  } catch (e: any) {
    totpError.value = e?.data?.error || t('twofa.error_setup')
  } finally {
    totpBusy.value = false
  }
}

async function confirmTotp() {
  totpError.value = ''
  totpBusy.value = true
  try {
    const res = await request<{ backup_codes: string[] }>('/me/totp/confirm', { method: 'POST', body: { code: totpCode.value } })
    totpBackupCodes.value = res.backup_codes
    totpStep.value = 'backups'
  } catch (e: any) {
    totpError.value = e?.data?.error || t('twofa.error_invalid_code')
  } finally {
    totpBusy.value = false
  }
}

function finishTotpSetup() {
  totpEnabled.value = true
  totpStep.value = 'idle'
  totpBackupCodes.value = []
  totpSecret.value = ''
  totpQr.value = ''
}

async function disableTotp() {
  totpError.value = ''
  totpBusy.value = true
  try {
    await request('/me/totp', {
      method: 'DELETE',
      body: { password: totpDisablePassword.value, code: totpDisableCode.value },
    })
    totpEnabled.value = false
    totpDisabling.value = false
    totpDisablePassword.value = ''
    totpDisableCode.value = ''
  } catch (e: any) {
    totpError.value = e?.data?.error || t('twofa.error_disable')
  } finally {
    totpBusy.value = false
  }
}

// Regenerate backup codes
const regenBusy = ref(false)
const regenError = ref('')
const regenCodes = ref<string[]>([])

async function regenerateBackupCodes() {
  regenError.value = ''
  const code = await prompt(t('twofa.regenerate_prompt'), '')
  if (code === null) return
  regenBusy.value = true
  try {
    const res = await request<{ backup_codes: string[] }>('/me/totp/backup-codes', { method: 'POST', body: { code: code.trim() } })
    regenCodes.value = res.backup_codes
    totpBackupCodes.value = res.backup_codes
  } catch (e: any) {
    regenError.value = e?.data?.error || t('twofa.error_invalid_code')
  } finally {
    regenBusy.value = false
  }
}

// Security activity (audit log) — shown in a modal, loaded on open.
interface AuditEntry { event: string; ip: string; user_agent: string; created_at: string }
const auditLog = ref<AuditEntry[]>([])
const auditOpen = ref(false)
useModalEscape(auditOpen, () => (auditOpen.value = false))

async function openAudit() {
  auditOpen.value = true
  try { auditLog.value = await request<AuditEntry[]>('/me/audit') } catch { /* silent */ }
}

// Security keys / Passkeys
interface Passkey { id: string; name: string; created_at: string; last_used_at: string | null }
const passkeys = ref<Passkey[]>([])
const passkeyError = ref('')
const passkeyBusy = ref(false)
const passkeyStatus = ref('')
// Rename flow
const renamingId = ref('')
const renameName = ref('')

async function loadPasskeys() {
  try { passkeys.value = await request<Passkey[]>('/me/webauthn') } catch { /* silent */ }
}
onMounted(loadPasskeys)

const approvalAction = ref('')
const approvalPassword = ref('')
const approvalCode = ref('')
const approvalError = ref('')
const approvalBusy = ref(false)
let resolveApproval: ((token: string | null) => void) | undefined
function authorizePasskeyAction(action: string): Promise<string | null> {
  approvalPassword.value = ''; approvalCode.value = ''; approvalError.value = ''
  approvalAction.value = action
  return new Promise(resolve => { resolveApproval = resolve })
}
function closeApproval(token: string | null = null) {
  approvalAction.value = ''; approvalPassword.value = ''; approvalCode.value = ''
  resolveApproval?.(token); resolveApproval = undefined
}
useModalEscape(computed(() => !!approvalAction.value), () => { if (!approvalBusy.value) closeApproval() })
onBeforeUnmount(() => closeApproval())
async function confirmIdentity(withPasskey: boolean) {
  approvalBusy.value = true; approvalError.value = ''
  try {
    let result: { approval_token: string }
    if (withPasskey) {
      const begin = await request<{ options: any; session_token: string }>('/me/webauthn/approval/begin', { method: 'POST', body: { action: approvalAction.value } })
      const { get } = await import('@github/webauthn-json')
      const assertion = await get(begin.options)
      result = await request('/me/webauthn/approval/finish', { method: 'POST', body: { session_token: begin.session_token, assertion } })
    } else {
      result = await request('/me/webauthn/approval/password', { method: 'POST', body: { action: approvalAction.value, password: approvalPassword.value, code: approvalCode.value } })
    }
    closeApproval(result.approval_token)
  } catch { approvalError.value = t('passkey.approval_error') }
  finally { approvalBusy.value = false }
}

async function addPasskey() {
  passkeyError.value = ''
  passkeyStatus.value = ''
  passkeyBusy.value = true
  const approval = await authorizePasskeyAction('register')
  if (!approval) { passkeyBusy.value = false; return }
  let res: { options: { publicKey: Record<string, unknown> }; session_token: string }
  try {
    res = await request<{ options: { publicKey: Record<string, unknown> }; session_token: string }>('/me/webauthn/register/begin', { method: 'POST', body: { approval_token: approval } })
  } catch (e: any) {
    passkeyError.value = e?.data?.error || t('passkey.error_begin')
    passkeyBusy.value = false
    return
  }

  let credential: unknown
  try {
    passkeyStatus.value = t('passkey.registering')
    // Basic JSON API: converts base64url ⇄ ArrayBuffer for us (challenge, user.id, …) and
    // returns a JSON-serializable credential. (The /browser-ponyfill `create` does NOT convert
    // — it expects real ArrayBuffers — which is why a raw JSON challenge was rejected.)
    const { create } = await import('@github/webauthn-json')
    credential = await create(res.options as Parameters<typeof create>[0])
  } catch (e: any) {
    passkeyStatus.value = ''
    passkeyBusy.value = false
    if (e?.name === 'NotAllowedError') {
      passkeyError.value = t('passkey.cancelled') // user dismissed the prompt or it timed out
    } else if (!window.isSecureContext) {
      passkeyError.value = t('passkey.error_insecure')
    } else {
      // Surface the real browser error (e.g. RP ID / origin mismatch) instead of hiding it.
      passkeyError.value = e?.message || e?.name || t('passkey.error_finish')
    }
    return
  }

  const name = await prompt(t('passkey.name_prompt_label'), t('passkey.name_prompt_default'))
  if (!name) {
    passkeyStatus.value = ''
    passkeyBusy.value = false
    return
  }

  try {
    await request('/me/webauthn/register/finish', {
      method: 'POST',
      body: { session_token: res.session_token, name: name.trim() || t('passkey.name_prompt_default'), credential },
    })
    passkeyStatus.value = t('passkey.success')
    await loadPasskeys()
  } catch (e: any) {
    passkeyError.value = e?.data?.error || t('passkey.error_finish')
    passkeyStatus.value = ''
  } finally {
    passkeyBusy.value = false
  }
}

function startRename(pk: Passkey) {
  renamingId.value = pk.id
  renameName.value = pk.name
  passkeyError.value = ''
}

async function saveRename(pk: Passkey) {
  if (!renameName.value.trim()) return
  try {
    await request(`/me/webauthn/${pk.id}`, { method: 'PATCH', body: { name: renameName.value.trim() } })
    pk.name = renameName.value.trim()
    renamingId.value = ''
  } catch (e: any) {
    passkeyError.value = e?.data?.error || t('passkey.error_rename')
  }
}

async function deletePasskey(pk: Passkey) {
  if (!(await confirm(t('passkey.confirm_delete'), { message: t('passkey.confirm_delete_msg', { name: pk.name }), confirmText: t('passkey.confirm_delete_btn'), danger: true }))) return
  passkeyError.value = ''
  try {
    const approval = await authorizePasskeyAction(`delete:${pk.id}`)
    if (!approval) return
    await request(`/me/webauthn/${pk.id}`, { method: 'DELETE', body: { approval_token: approval } })
    await loadPasskeys()
  } catch (e: any) {
    passkeyError.value = e?.data?.error || t('passkey.error_delete')
  }
}
</script>

<template>
  <div>
    <div v-if="approvalAction" class="fixed inset-0 z-40 flex items-center justify-center bg-black/50 p-4" @click.self="!approvalBusy && closeApproval()">
      <form class="card w-full max-w-sm p-5" role="dialog" aria-modal="true" aria-labelledby="passkey-approval-title" @submit.prevent="confirmIdentity(false)">
        <h2 id="passkey-approval-title" class="mb-2 font-semibold">{{ t('passkey.approval_title') }}</h2>
        <p class="mb-4 text-sm text-muted">{{ t('passkey.approval_description') }}</p>
        <label class="mb-3 block text-sm">{{ t('passkey.approval_password') }}
          <input v-model="approvalPassword" type="password" autocomplete="current-password" class="input mt-1 w-full" :disabled="approvalBusy" autofocus />
        </label>
        <label v-if="totpEnabled" class="mb-3 block text-sm">{{ t('passkey.approval_code') }}
          <input v-model="approvalCode" autocomplete="one-time-code" inputmode="numeric" class="input mt-1 w-full" :disabled="approvalBusy" />
        </label>
        <p v-if="approvalError" class="mb-3 text-sm text-danger" role="alert">{{ approvalError }}</p>
        <button v-if="passkeys.length" type="button" class="btn-ghost mb-3 w-full" :disabled="approvalBusy" @click="confirmIdentity(true)">{{ t('passkey.approval_existing') }}</button>
        <div class="flex justify-end gap-2">
          <button type="button" class="btn-ghost" :disabled="approvalBusy" @click="closeApproval()">{{ t('dialog.cancel') }}</button>
          <button type="submit" class="btn-accent" :disabled="approvalBusy || !approvalPassword">{{ t('passkey.approval_confirm') }}</button>
        </div>
      </form>
    </div>
    <p v-if="error" class="mb-4 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <div class="mb-6 card p-5">
      <h2 class="mb-3 text-sm font-medium text-muted">{{ t('settings.password_section') }}</h2>
      <p class="mb-3 text-xs text-muted">{{ t('settings.password_note') }}</p>
      <div class="grid max-w-sm gap-2">
        <input v-model="pwCurrent" type="password" autocomplete="current-password" class="input" :placeholder="t('settings.current_password')" />
        <input v-model="pwNew" type="password" autocomplete="new-password" class="input" :placeholder="t('settings.new_password')" />
        <input v-model="pwConfirm" type="password" autocomplete="new-password" class="input" :placeholder="t('settings.confirm_password')" @keyup.enter="changePassword" />
        <button class="btn-accent" :disabled="pwBusy || !pwCurrent || !pwNew" @click="changePassword">
          <Icon v-if="pwBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
          <Icon v-else name="lucide:lock" size="16" /> {{ t('settings.change_password') }}
        </button>
        <p v-if="pwOk" class="flex items-center gap-2 text-sm text-accent">
          <Icon name="lucide:check" size="16" /> {{ pwOk }}
        </p>
      </div>
    </div>

    <!-- Session lifetime -->
    <div class="mb-6 card p-5">
      <h2 class="mb-3 text-sm font-medium text-muted">{{ t('session.title') }}</h2>
      <p class="mb-3 text-xs text-muted">{{ t('session.description') }}</p>
      <label class="flex items-center gap-3 text-sm">
        <span class="text-muted">{{ t('session.label') }}</span>
        <select
          :value="sessionTtl ?? 60"
          class="input w-44"
          :disabled="sessionTtl === null || sessionBusy"
          @change="saveSessionTtl"
        >
          <option v-for="opt in SESSION_OPTIONS" :key="opt.minutes" :value="opt.minutes">
            {{ t(opt.key) }}
          </option>
        </select>
        <Icon v-if="sessionBusy" name="lucide:loader-circle" class="animate-spin text-muted" size="16" />
      </label>
      <p v-if="sessionTtl === 0" class="mt-2 flex items-center gap-2 text-xs text-warn">
        <Icon name="lucide:triangle-alert" size="14" /> {{ t('session.never_warning') }}
      </p>
      <p v-if="sessionOk" class="mt-2 flex items-center gap-2 text-sm text-accent">
        <Icon name="lucide:check" size="14" /> {{ sessionOk }}
      </p>
      <p v-if="sessionError" class="mt-2 flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="14" /> {{ sessionError }}
      </p>
    </div>

    <!-- Two-factor authentication -->
    <div class="mb-6 card p-5">
      <h2 class="mb-3 text-sm font-medium text-muted">{{ t('twofa.title') }}</h2>
      <p class="mb-4 text-xs text-muted">{{ t('twofa.description') }}</p>

      <p v-if="totpError" class="mb-3 flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="16" /> {{ totpError }}
      </p>

      <!-- Loading -->
      <div v-if="totpEnabled === null" class="text-sm text-muted">{{ t('common.loading') }}</div>

      <!-- Enabled state -->
      <template v-else-if="totpEnabled">
        <div class="mb-3 flex items-center gap-2 text-sm">
          <Icon name="lucide:shield-check" size="18" class="text-accent" />
          <span class="font-medium">{{ t('twofa.status_enabled') }}</span>
        </div>
        <template v-if="!totpDisabling">
          <div class="flex flex-wrap gap-2">
            <button class="btn-ghost" @click="regenerateBackupCodes" :disabled="regenBusy">
              <Icon v-if="regenBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
              <Icon v-else name="lucide:refresh-cw" size="16" /> {{ t('twofa.regenerate_btn') }}
            </button>
            <button class="btn-ghost" @click="totpDisabling = true; totpError = ''">
              <Icon name="lucide:shield-off" size="16" /> {{ t('twofa.btn_disable') }}
            </button>
          </div>
          <p v-if="regenError" class="mt-2 flex items-center gap-2 text-sm text-danger">
            <Icon name="lucide:triangle-alert" size="16" /> {{ regenError }}
          </p>
          <!-- Regenerated backup codes (shown once) -->
          <template v-if="regenCodes.length">
            <p class="mt-3 mb-2 text-xs font-medium text-danger">{{ t('twofa.backup_save_note') }}</p>
            <div class="mb-2 rounded-md border border-line bg-panel2 p-3">
              <div class="mb-2 flex justify-end">
                <button class="btn-ghost px-2 py-1" :title="t('settings.webdav_copy')" @click="copyText(regenCodes.join('\n'), 'regen-backups')">
                  <Icon :name="copied === 'regen-backups' ? 'lucide:check' : 'lucide:copy'" size="16" />
                </button>
              </div>
              <ul class="grid grid-cols-2 gap-x-4 gap-y-1">
                <li v-for="code in regenCodes" :key="code" class="select-all font-mono text-sm">{{ code }}</li>
              </ul>
            </div>
            <p class="text-xs text-accent">{{ t('twofa.regenerated') }}</p>
          </template>
        </template>
        <template v-else>
          <p class="mb-2 text-xs text-muted">{{ t('twofa.disable_hint') }}</p>
          <div class="grid max-w-sm gap-2">
            <input v-model="totpDisablePassword" type="password" autocomplete="current-password" class="input" :placeholder="t('twofa.disable_password_ph')" />
            <input v-model="totpDisableCode" type="text" inputmode="numeric" class="input font-mono" :placeholder="t('twofa.disable_code_ph')" />
            <div class="flex gap-2">
              <button class="btn-danger" :disabled="totpBusy || !totpDisablePassword || !totpDisableCode" @click="disableTotp">
                <Icon v-if="totpBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
                <Icon v-else name="lucide:shield-off" size="16" /> {{ t('twofa.btn_disable_confirm') }}
              </button>
              <button class="btn-ghost" :disabled="totpBusy" @click="totpDisabling = false; totpError = ''">
                {{ t('common.cancel') }}
              </button>
            </div>
          </div>
        </template>
      </template>

      <!-- Disabled state / setup flow -->
      <template v-else>
        <!-- idle: show Enable button -->
        <template v-if="totpStep === 'idle'">
          <div class="mb-3 flex items-center gap-2 text-sm text-muted">
            <Icon name="lucide:shield" size="18" />
            <span>{{ t('twofa.status_disabled') }}</span>
          </div>
          <button class="btn-accent" :disabled="totpBusy" @click="startTotpSetup">
            <Icon v-if="totpBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
            <Icon v-else name="lucide:shield-plus" size="16" /> {{ t('twofa.btn_enable') }}
          </button>
        </template>

        <!-- setup: show QR + secret + first code input -->
        <template v-else-if="totpStep === 'setup'">
          <p class="mb-3 text-xs text-muted">{{ t('twofa.setup_hint') }}</p>
          <div class="mb-3">
            <img v-if="totpQr" :src="totpQr" alt="TOTP QR code" class="rounded border border-line" width="200" height="200" />
          </div>
          <div class="grid max-w-sm gap-2">
            <div>
              <p class="mb-1 text-xs text-muted">{{ t('twofa.secret_label') }}</p>
              <div class="flex items-center gap-2 rounded-md border border-line bg-panel2 px-3 py-2">
                <span class="min-w-0 flex-1 select-all break-all font-mono text-xs">{{ totpSecret }}</span>
                <button class="btn-ghost shrink-0 px-2 py-1" :title="t('settings.webdav_copy')" @click="copyText(totpSecret, 'totp-secret')">
                  <Icon :name="copied === 'totp-secret' ? 'lucide:check' : 'lucide:copy'" size="16" />
                </button>
              </div>
            </div>
            <input v-model="totpCode" type="text" inputmode="numeric" class="input font-mono tracking-widest" :placeholder="t('twofa.code_ph')" autofocus />
            <button class="btn-accent" :disabled="totpBusy || !totpCode" @click="confirmTotp">
              <Icon v-if="totpBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
              <Icon v-else name="lucide:check" size="16" /> {{ t('twofa.btn_confirm') }}
            </button>
            <button class="btn-ghost" :disabled="totpBusy" @click="totpStep = 'idle'; totpError = ''">
              {{ t('common.cancel') }}
            </button>
          </div>
        </template>

        <!-- backups: show one-time backup codes -->
        <template v-else-if="totpStep === 'backups'">
          <div class="mb-3 flex items-center gap-2 text-sm text-accent">
            <Icon name="lucide:check-circle" size="18" />
            <span class="font-medium">{{ t('twofa.enabled_success') }}</span>
          </div>
          <p class="mb-2 text-xs font-medium text-danger">{{ t('twofa.backup_save_note') }}</p>
          <div class="mb-4 rounded-md border border-line bg-panel2 p-3">
            <div class="mb-2 flex justify-end">
              <button class="btn-ghost px-2 py-1" :title="t('settings.webdav_copy')" @click="copyText(totpBackupCodes.join('\n'), 'totp-backups')">
                <Icon :name="copied === 'totp-backups' ? 'lucide:check' : 'lucide:copy'" size="16" />
              </button>
            </div>
            <ul class="grid grid-cols-2 gap-x-4 gap-y-1">
              <li v-for="code in totpBackupCodes" :key="code" class="select-all font-mono text-sm">{{ code }}</li>
            </ul>
          </div>
          <button class="btn-accent" @click="finishTotpSetup">
            <Icon name="lucide:check" size="16" /> {{ t('twofa.btn_done') }}
          </button>
        </template>
      </template>
    </div>

    <!-- Security keys / Passkeys -->
    <div class="mb-6 card p-5">
      <h2 class="mb-3 text-sm font-medium text-muted">{{ t('passkey.title') }}</h2>
      <p class="mb-4 text-xs text-muted">{{ t('passkey.description') }}</p>

      <p v-if="passkeyError" class="mb-3 flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="16" /> {{ passkeyError }}
      </p>
      <p v-if="passkeyStatus" class="mb-3 flex items-center gap-2 text-sm text-accent">
        <Icon v-if="passkeyBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
        <Icon v-else name="lucide:check-circle" size="16" /> {{ passkeyStatus }}
      </p>

      <!-- Key list -->
      <ul v-if="passkeys.length" class="mb-4 space-y-2">
        <li v-for="pk in passkeys" :key="pk.id" class="rounded-md border border-line bg-panel2 p-3 text-sm">
          <!-- View row -->
          <template v-if="renamingId !== pk.id">
            <div class="flex items-start justify-between gap-2">
              <div class="min-w-0">
                <div class="font-medium">{{ pk.name }}</div>
                <div class="mt-0.5 text-xs text-muted">
                  {{ t('passkey.created_label') }}: {{ new Date(pk.created_at).toLocaleString() }}
                  · {{ t('passkey.last_used_label') }}: {{ pk.last_used_at ? new Date(pk.last_used_at).toLocaleString() : t('passkey.last_used_never') }}
                </div>
              </div>
              <div class="flex shrink-0 gap-1">
                <button class="btn-ghost px-2 py-1" @click="startRename(pk)">
                  <Icon name="lucide:pencil" size="14" /> {{ t('passkey.rename_btn') }}
                </button>
                <button class="btn-danger px-2 py-1" @click="deletePasskey(pk)">
                  <Icon name="lucide:trash-2" size="14" /> {{ t('passkey.delete_btn') }}
                </button>
              </div>
            </div>
          </template>
          <!-- Rename row -->
          <template v-else>
            <div class="flex items-center gap-2">
              <input v-model="renameName" class="input flex-1" :placeholder="t('passkey.rename_new_name_ph')" @keyup.enter="saveRename(pk)" @keyup.esc="renamingId = ''" />
              <button class="btn-accent px-3 py-1.5" :disabled="!renameName.trim()" @click="saveRename(pk)">
                <Icon name="lucide:check" size="14" />
              </button>
              <button class="btn-ghost px-2 py-1.5" @click="renamingId = ''">
                {{ t('common.cancel') }}
              </button>
            </div>
          </template>
        </li>
      </ul>
      <p v-else class="mb-4 text-sm text-muted">{{ t('passkey.empty') }}</p>

      <button class="btn-accent" :disabled="passkeyBusy" @click="addPasskey">
        <Icon v-if="passkeyBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
        <Icon v-else name="lucide:fingerprint" size="16" /> {{ t('passkey.add_btn') }}
      </button>
    </div>

    <!-- Security activity (audit log) — opens in a modal -->
    <div class="card p-5">
      <h2 class="mb-3 text-sm font-medium text-muted">{{ t('audit.title') }}</h2>
      <p class="mb-4 text-xs text-muted">{{ t('audit.description') }}</p>
      <button class="btn-ghost" @click="openAudit">
        <Icon name="lucide:scroll-text" size="16" /> {{ t('audit.open_btn') }}
      </button>
    </div>

    <!-- Audit log modal -->
    <div v-if="auditOpen" class="fixed inset-0 z-20 flex items-center justify-center bg-black/50 p-4" @click.self="auditOpen = false">
      <div class="card flex max-h-[80vh] w-full max-w-lg flex-col p-5">
        <div class="mb-3 flex items-center justify-between">
          <h2 class="text-sm font-medium text-muted">{{ t('audit.title') }}</h2>
          <button class="btn-ghost px-2 py-1" :title="t('common.close')" @click="auditOpen = false">
            <Icon name="lucide:x" size="16" />
          </button>
        </div>
        <div v-if="!auditLog.length" class="text-sm text-muted">{{ t('audit.empty') }}</div>
        <ul v-else class="space-y-2 overflow-y-auto">
          <li
            v-for="(entry, i) in auditLog"
            :key="i"
            class="rounded-md border border-line bg-panel2 p-3 text-sm"
          >
            <div class="flex items-start justify-between gap-2">
              <span class="font-medium">{{ t(`audit.events.${entry.event}`) }}</span>
              <span class="shrink-0 text-xs text-muted">{{ new Date(entry.created_at).toLocaleString() }}</span>
            </div>
            <div class="mt-0.5 text-xs text-muted">
              {{ entry.ip }} · {{ entry.user_agent }}
            </div>
          </li>
        </ul>
      </div>
    </div>
  </div>
</template>
