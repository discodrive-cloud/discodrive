<script setup lang="ts">
const { t, locale } = useI18n()
const { request } = useApi()
const { SUPPORTED, saveLanguage } = useLocale()
const error = ref('')
const ok = ref('')
const saving = ref(false)
const testing = ref(false)

const smtp = reactive({
  host: '', port: '', username: '', from: '', security: 'starttls',
  password: '', password_set: false,
  notifications_enabled: true,
})

// Language selector — saves immediately on change (independent of the SMTP form).
const langBusy = ref(false)
const langError = ref('')
async function onLanguageChange(e: Event) {
  const code = (e.target as HTMLSelectElement).value
  if (!code) return
  langBusy.value = true
  langError.value = ''
  try {
    await saveLanguage(code as any)
  } catch {
    langError.value = t('settings.error_language')
  } finally {
    langBusy.value = false
  }
}

async function load() {
  error.value = ''
  try {
    const d = await request<any>('/admin/smtp')
    Object.assign(smtp, {
      host: d.host, port: d.port, username: d.username, from: d.from,
      security: d.security || 'starttls', password: '', password_set: d.password_set,
      notifications_enabled: d.notifications_enabled,
    })
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_smtp')
  }
}
onMounted(load)

// External access (WebDAV/CalDAV/CardDAV) is a server-wide switch, so it lives here
// rather than in personal settings. Each change is saved at once, like the language.
type AccessFlags = { webdav: boolean; caldav: boolean; carddav: boolean }
const access = reactive<AccessFlags>({ webdav: false, caldav: false, carddav: false })
const accessKeys = ['webdav', 'caldav', 'carddav'] as const
const savingAccess = ref(false)
const accessError = ref('')

async function loadAccess() {
  try {
    Object.assign(access, await request<AccessFlags>('/me/access'))
  } catch (e: any) {
    accessError.value = e?.data?.error || t('common.error_load')
  }
}
onMounted(loadAccess)

async function saveAccess(key: keyof AccessFlags, on: boolean) {
  savingAccess.value = true
  accessError.value = ''
  try {
    Object.assign(access, await request<AccessFlags>('/me/access', { method: 'PUT', body: { [key]: on } }))
  } catch (e: any) {
    accessError.value = e?.data?.error || t('common.error_save')
    await loadAccess() // revert the optimistic checkbox state
  } finally {
    savingAccess.value = false
  }
}

async function putSetting(key: string, value: string) {
  await request('/admin/settings', { method: 'PUT', body: { key, value } })
}

async function save() {
  error.value = ''; ok.value = ''; saving.value = true
  try {
    await putSetting('smtp.host', smtp.host)
    await putSetting('smtp.port', smtp.port)
    await putSetting('smtp.username', smtp.username)
    await putSetting('smtp.from', smtp.from)
    await putSetting('smtp.security', smtp.security)
    await putSetting('notifications.enabled', smtp.notifications_enabled ? 'true' : 'false')
    if (smtp.password) await putSetting('smtp.password', smtp.password)
    ok.value = t('admin.saved')
    await load()
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_save_smtp')
  } finally {
    saving.value = false
  }
}

async function sendTest() {
  error.value = ''; ok.value = ''; testing.value = true
  try {
    const r = await request<any>('/admin/smtp/test', { method: 'POST' })
    ok.value = r.note || t('admin.test_sent')
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_test')
  } finally {
    testing.value = false
  }
}
</script>

<template>
  <div class="max-w-2xl">
    <h1 class="mb-4 text-xl font-semibold">{{ t('admin.app_settings_title') }}</h1>
    <p v-if="error" class="mb-4 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>
    <p v-if="ok" class="mb-4 flex items-center gap-2 text-sm text-accent">
      <Icon name="lucide:check" size="16" /> {{ ok }}
    </p>

    <div class="mb-6 card p-5">
      <h2 class="mb-1 text-sm font-medium text-muted">{{ t('settings.access_title') }}</h2>
      <p class="mb-3 text-xs text-muted">{{ t('settings.access_hint') }}</p>
      <div class="space-y-2">
        <label v-for="key in accessKeys" :key="key" class="flex items-center gap-3 text-sm">
          <input
            type="checkbox" class="h-4 w-4" :checked="access[key]" :disabled="savingAccess"
            @change="saveAccess(key, ($event.target as HTMLInputElement).checked)"
          />
          <span>{{ t(`settings.access_${key}`) }}</span>
        </label>
      </div>
      <p v-if="accessError" class="mt-2 flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="14" /> {{ accessError }}
      </p>
    </div>

    <div class="mb-6 card p-5">
      <h2 class="mb-3 text-sm font-medium text-muted">{{ t('admin.smtp_section') }}</h2>
      <div class="grid gap-3">
        <label class="block"><span class="mb-1 block text-xs text-muted">{{ t('admin.smtp_host') }}</span><input v-model="smtp.host" class="input" placeholder="smtp.example.com" /></label>
        <div class="grid grid-cols-2 gap-3">
          <label class="block"><span class="mb-1 block text-xs text-muted">{{ t('admin.smtp_port') }}</span><input v-model="smtp.port" class="input" placeholder="587" /></label>
          <label class="block"><span class="mb-1 block text-xs text-muted">{{ t('admin.smtp_encryption') }}</span>
            <select v-model="smtp.security" class="input">
              <option value="starttls">STARTTLS (587)</option>
              <option value="tls">TLS (465)</option>
              <option value="none">{{ t('admin.smtp_no_encryption') }}</option>
            </select>
          </label>
        </div>
        <label class="block"><span class="mb-1 block text-xs text-muted">{{ t('admin.smtp_login') }}</span><input v-model="smtp.username" class="input" /></label>
        <label class="block"><span class="mb-1 block text-xs text-muted">{{ t('admin.smtp_from') }}</span><input v-model="smtp.from" class="input" placeholder="noreply@example.com" /></label>
        <label class="block">
          <span class="mb-1 block text-xs text-muted">{{ t('admin.smtp_password') }} <span v-if="smtp.password_set" class="text-accent">{{ t('admin.smtp_password_set') }}</span></span>
          <input v-model="smtp.password" type="password" class="input" :placeholder="smtp.password_set ? t('admin.smtp_password_keep') : t('admin.smtp_password_ph')" />
        </label>
      </div>
    </div>

    <div class="mb-6 card flex items-center justify-between p-5">
      <div>
        <div class="text-sm font-medium">{{ t('admin.notif_enabled') }}</div>
        <div class="text-xs text-muted">{{ t('admin.notif_enabled_desc') }}</div>
      </div>
      <input v-model="smtp.notifications_enabled" type="checkbox" class="h-5 w-5" />
    </div>

    <div class="flex gap-3">
      <button class="btn-accent" :disabled="saving" @click="save">
        <Icon v-if="saving" name="lucide:loader-circle" class="animate-spin" size="16" /><Icon v-else name="lucide:save" size="16" /> {{ t('admin.btn_save') }}
      </button>
      <button class="btn-ghost" :disabled="testing" @click="sendTest">
        <Icon v-if="testing" name="lucide:loader-circle" class="animate-spin" size="16" /><Icon v-else name="lucide:send" size="16" /> {{ t('admin.btn_send_test') }}
      </button>
    </div>

    <div class="mt-6 card p-5">
      <h2 class="mb-3 text-sm font-medium text-muted">{{ t('settings.language_section') }}</h2>
      <label class="flex items-center gap-3 text-sm">
        <span class="text-muted">{{ t('settings.language_label') }}</span>
        <select
          :value="locale"
          class="input w-44"
          :disabled="langBusy"
          @change="onLanguageChange"
        >
          <option v-for="code in SUPPORTED" :key="code" :value="code">
            {{ t(`languages.${code}`) }}
          </option>
        </select>
        <Icon v-if="langBusy" name="lucide:loader-circle" class="animate-spin text-muted" size="16" />
      </label>
      <p v-if="langError" class="mt-2 flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="14" /> {{ langError }}
      </p>
    </div>
  </div>
</template>
