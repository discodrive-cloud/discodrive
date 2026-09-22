<script setup lang="ts">
interface Device { id: string; name: string; kind: string; last_seen_at: string | null }

const { t } = useI18n()
const { request } = useApi()
const { confirm } = useDialog()
const { copied, copyText } = useCopy()
const sess = useSession()

const error = ref('')
const devices = ref<Device[]>([])

async function loadDevices() {
  try {
    devices.value = await request<Device[]>('/devices')
  } catch (e: any) {
    error.value = e?.data?.error || t('common.error_load')
  }
}
onMounted(loadDevices)

// External-access toggles (global webdav/caldav/carddav enable flags).
type AccessFlags = { webdav: boolean; caldav: boolean; carddav: boolean }
const access = reactive<AccessFlags>({ webdav: false, caldav: false, carddav: false })
const savingAccess = ref(false)

async function loadAccess() {
  try { Object.assign(access, await request<AccessFlags>('/me/access')) } catch { /* silent */ }
}
onMounted(loadAccess)

async function saveAccess(patch: Partial<AccessFlags>) {
  savingAccess.value = true
  error.value = ''
  try {
    Object.assign(access, await request<AccessFlags>('/me/access', { method: 'PUT', body: patch }))
  } catch (e: any) {
    error.value = e?.data?.error || t('common.error_load')
    await loadAccess() // revert the optimistic checkbox state
  } finally {
    savingAccess.value = false
  }
}

const webdavDevices = computed(() => devices.value.filter((d) => d.kind === 'webdav'))

async function disconnect(d: Device) {
  if (!(await confirm(t('settings.confirm_disconnect'), { message: `«${d.name}»`, confirmText: t('settings.confirm_disconnect_btn'), danger: true }))) return
  error.value = ''
  try {
    await request(`/devices/${d.id}`, { method: 'DELETE' })
    await loadDevices()
  } catch (e: any) {
    error.value = e?.data?.error || t('settings.error_disconnect')
  }
}

const webdavName = ref('')
const newPassword = ref('')
const creatingWd = ref(false)

async function createWebdav() {
  if (!webdavName.value.trim()) return
  creatingWd.value = true
  error.value = ''
  try {
    const res = await request<{ password: string }>('/devices/webdav', {
      method: 'POST', body: { name: webdavName.value.trim() },
    })
    newPassword.value = res.password
    webdavName.value = ''
    await loadDevices()
  } catch (e: any) {
    error.value = e?.data?.error || t('settings.error_create_webdav')
  } finally {
    creatingWd.value = false
  }
}

const origin = computed(() => (import.meta.client ? location.origin : ''))
const davUrl = computed(() => origin.value + '/dav/')
const caldavUrl = computed(() => origin.value + '/caldav/')
const carddavUrl = computed(() => origin.value + '/carddav/')
</script>

<template>
  <div>
    <!-- External access toggles: when off, the protocol is reachable only via the web UI -->
    <div class="mb-6 card p-5">
      <h2 class="mb-1 text-sm font-medium text-muted">{{ t('settings.access_title') }}</h2>
      <p class="mb-3 text-xs text-muted">{{ t('settings.access_hint') }}</p>
      <div class="space-y-2">
        <label class="flex items-center gap-3 text-sm">
          <input
            type="checkbox" class="h-4 w-4" :checked="access.webdav" :disabled="savingAccess || sess.role !== 'admin'"
            @change="saveAccess({ webdav: ($event.target as HTMLInputElement).checked })"
          />
          <span>{{ t('settings.access_webdav') }}</span>
        </label>
        <label class="flex items-center gap-3 text-sm">
          <input
            type="checkbox" class="h-4 w-4" :checked="access.caldav" :disabled="savingAccess || sess.role !== 'admin'"
            @change="saveAccess({ caldav: ($event.target as HTMLInputElement).checked })"
          />
          <span>{{ t('settings.access_caldav') }}</span>
        </label>
        <label class="flex items-center gap-3 text-sm">
          <input
            type="checkbox" class="h-4 w-4" :checked="access.carddav" :disabled="savingAccess || sess.role !== 'admin'"
            @change="saveAccess({ carddav: ($event.target as HTMLInputElement).checked })"
          />
          <span>{{ t('settings.access_carddav') }}</span>
        </label>
      </div>
    </div>

  <div class="card p-5">
    <h2 class="mb-3 text-sm font-medium text-muted">{{ t('settings.dav_section') }}</h2>
    <p class="mb-4 text-xs text-muted">{{ t('settings.dav_note') }}</p>

    <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <!-- Login (email) -->
    <div class="mb-4 flex items-center gap-2">
      <span class="w-40 shrink-0 text-xs text-muted">{{ t('settings.dav_login') }}</span>
      <code class="min-w-0 flex-1 truncate rounded-md border border-line bg-panel2 px-2 py-1.5 font-mono text-xs">{{ sess.email }}</code>
      <button class="btn-ghost shrink-0 px-2 py-1.5" :title="t('settings.webdav_copy')" @click="copyText(sess.email || '', 'login')">
        <Icon :name="copied === 'login' ? 'lucide:check' : 'lucide:copy'" size="16" />
      </button>
    </div>

    <!-- Addresses by application type -->
    <div class="mb-1 text-xs font-medium text-muted">{{ t('settings.dav_addresses') }}</div>
    <div class="mb-2 flex items-center gap-2">
      <span class="w-40 shrink-0 text-xs text-muted">{{ t('settings.dav_files') }}</span>
      <code class="min-w-0 flex-1 truncate rounded-md border border-line bg-panel2 px-2 py-1.5 font-mono text-xs">{{ davUrl }}</code>
      <button class="btn-ghost shrink-0 px-2 py-1.5" :title="t('settings.webdav_copy')" @click="copyText(davUrl, 'url-files')">
        <Icon :name="copied === 'url-files' ? 'lucide:check' : 'lucide:copy'" size="16" />
      </button>
    </div>
    <div class="mb-2 flex items-center gap-2">
      <span class="w-40 shrink-0 text-xs text-muted">{{ t('settings.dav_calendars') }}</span>
      <code class="min-w-0 flex-1 truncate rounded-md border border-line bg-panel2 px-2 py-1.5 font-mono text-xs">{{ caldavUrl }}</code>
      <button class="btn-ghost shrink-0 px-2 py-1.5" :title="t('settings.webdav_copy')" @click="copyText(caldavUrl, 'url-cal')">
        <Icon :name="copied === 'url-cal' ? 'lucide:check' : 'lucide:copy'" size="16" />
      </button>
    </div>
    <div class="mb-4 flex items-center gap-2">
      <span class="w-40 shrink-0 text-xs text-muted">{{ t('settings.dav_contacts') }}</span>
      <code class="min-w-0 flex-1 truncate rounded-md border border-line bg-panel2 px-2 py-1.5 font-mono text-xs">{{ carddavUrl }}</code>
      <button class="btn-ghost shrink-0 px-2 py-1.5" :title="t('settings.webdav_copy')" @click="copyText(carddavUrl, 'url-card')">
        <Icon :name="copied === 'url-card' ? 'lucide:check' : 'lucide:copy'" size="16" />
      </button>
    </div>

    <!-- Create a per-device / per-app password -->
    <div class="mb-1 text-xs font-medium text-muted">{{ t('settings.dav_password_title') }}</div>
    <p class="mb-2 text-xs text-muted">{{ t('settings.dav_password_hint') }}</p>
    <div class="flex gap-2">
      <input v-model="webdavName" class="input" :placeholder="t('settings.webdav_device_ph')" />
      <button class="btn-accent shrink-0" :disabled="creatingWd" @click="createWebdav">
        <Icon v-if="creatingWd" name="lucide:loader-circle" class="animate-spin" size="16" />
        <Icon v-else name="lucide:key-round" size="16" /> {{ t('settings.webdav_create') }}
      </button>
    </div>
    <div v-if="newPassword" class="mt-3 rounded-md border border-line bg-panel2 p-2 text-xs">
      <div class="mb-1 text-muted">{{ t('settings.webdav_password_label') }}</div>
      <div class="flex items-center gap-2">
        <span class="flex-1 break-all font-mono text-accent">{{ newPassword }}</span>
        <button class="btn-ghost shrink-0 px-2 py-1" :title="t('settings.webdav_copy')" @click="copyText(newPassword, 'pw')">
          <Icon :name="copied === 'pw' ? 'lucide:check' : 'lucide:copy'" size="16" />
        </button>
      </div>
    </div>
    <ul v-if="webdavDevices.length" class="mt-3 space-y-1 text-sm">
      <li v-for="d in webdavDevices" :key="d.id" class="flex items-center justify-between">
        <span>{{ d.name }}</span>
        <button class="btn-danger px-2 py-1" @click="disconnect(d)"><Icon name="lucide:trash-2" size="14" /></button>
      </li>
    </ul>
  </div>
  </div>
</template>
