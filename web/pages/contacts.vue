<script setup lang="ts">
import { changedElsewhere } from '~/lib/changedElsewhere'
interface ContactSummary { uid: string; full_name: string; emails: string[]; phones: string[] }
interface EmailEntry { type: string; value: string }
interface PhoneEntry { type: string; value: string }
interface Adr { street: string; city: string; region: string; postal: string; country: string }
interface ContactDetail {
  uid: string
  etag?: string
  full_name: string
  family: string
  given: string
  emails: EmailEntry[]
  phones: PhoneEntry[]
  org: string
  title: string
  adr: Adr
  note: string
  bday: string
  has_photo: boolean
  photo_uri?: string
}

const PHONE_TYPES = ['home', 'work', 'cell', 'other']
const EMAIL_TYPES = ['home', 'work', 'other']

const { t } = useI18n()
const { request } = useApi()
const { confirm } = useDialog()

const share = reactive({ open: false, email: '', list: [] as { id: string; email: string }[], busy: false })

async function loadShares() {
  try { share.list = await request<{ id: string; email: string }[]>('/me/contacts/shares') }
  catch { share.list = [] }
}
function openShare() {
  Object.assign(share, { open: true, email: '', list: [], busy: false })
  loadShares()
}
async function submitShare() {
  if (!share.email.trim()) return
  share.busy = true
  try {
    await request('/me/contacts/share', { method: 'POST', body: { email: share.email.trim() } })
    share.email = ''
    await loadShares()
  } catch (e: any) { error.value = e?.data?.error || t('contacts.error_share') }
  finally { share.busy = false }
}
async function revokeShare(id: string) {
  try {
    await request(`/me/contacts/shares/${id}`, { method: 'DELETE' })
    await loadShares()
  } catch (e: any) { error.value = e?.data?.error || t('contacts.error_revoke') }
}

useModalEscape(computed(() => share.open), () => { share.open = false })

const list = ref<ContactSummary[]>([])
const error = ref('')
const busy = ref(false)
const search = ref('')

const filtered = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return list.value
  return list.value.filter(
    (c) =>
      (c.full_name || '').toLowerCase().includes(q) ||
      (c.emails ?? []).some((e) => e.toLowerCase().includes(q)),
  )
})

async function loadList() {
  error.value = ''
  busy.value = true
  try {
    list.value = await request<ContactSummary[]>('/me/contacts')
  } catch (e: any) {
    error.value = e?.data?.error || t('contacts.error_load')
  } finally {
    busy.value = false
  }
}
onMounted(loadList)

const importInput = ref<HTMLInputElement>()
const notice = ref('')

async function onImportFile(e: Event) {
  const f = (e.target as HTMLInputElement).files?.[0]
  if (!f) return
  notice.value = ''
  error.value = ''
  const fd = new FormData()
  fd.append('file', f)
  try {
    const res = await request<{ imported: number; skipped: number }>('/me/contacts/import', { method: 'POST', body: fd })
    notice.value = t('contacts.imported', { n: res.imported }) + (res.skipped ? t('contacts.skipped', { n: res.skipped }) : '')
    await loadList()
  } catch (e: any) {
    error.value = e?.data?.error || t('contacts.error_import')
  } finally {
    if (importInput.value) importInput.value.value = ''
  }
}

async function exportContacts() {
  error.value = ''
  try {
    const blob = await request<Blob>('/me/contacts/export', { responseType: 'blob' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = 'contacts.vcf'
    a.click()
    URL.revokeObjectURL(url)
  } catch (e: any) {
    error.value = e?.data?.error || t('contacts.error_export')
  }
}

// --- form ---
const formOpen = ref(false)
const formBusy = ref(false)
const formError = ref<HTMLElement>()
const form = reactive<ContactDetail>({
  uid: '',
  full_name: '',
  family: '',
  given: '',
  emails: [],
  phones: [],
  org: '',
  title: '',
  adr: { street: '', city: '', region: '', postal: '', country: '' },
  note: '',
  bday: '',
  has_photo: false,
})

function resetForm() {
  Object.assign(form, {
    uid: '',
    full_name: '',
    family: '',
    given: '',
    emails: [],
    phones: [],
    org: '',
    title: '',
    adr: { street: '', city: '', region: '', postal: '', country: '' },
    note: '',
    bday: '',
    has_photo: false,
    photo_uri: '',
    etag: '',
  })
}

function newContact() {
  resetForm()
  formOpen.value = true
}

async function selectContact(uid: string) {
  error.value = ''
  try {
    const d = await request<ContactDetail>(`/me/contacts/${uid}`)
    Object.assign(form, {
      ...d,
      emails: (d.emails ?? []).map((e) => ({ ...e })),
      phones: (d.phones ?? []).map((p) => ({ ...p })),
      adr: { ...(d.adr ?? { street: '', city: '', region: '', postal: '', country: '' }) },
      photo_uri: d.photo_uri || '',
      has_photo: d.has_photo ?? false,
    })
    formOpen.value = true
  } catch (e: any) {
    error.value = e?.data?.error || t('contacts.error_contact')
  }
}

// email rows
function addEmail() { form.emails.push({ type: 'home', value: '' }) }
function removeEmail(i: number) { form.emails.splice(i, 1) }

// phone rows
function addPhone() { form.phones.push({ type: 'cell', value: '' }) }
function removePhone(i: number) { form.phones.splice(i, 1) }

function bodyFromForm() {
  return {
    full_name: form.full_name,
    family: form.family,
    given: form.given,
    emails: form.emails.filter((e) => e.value.trim()),
    phones: form.phones.filter((p) => p.value.trim()),
    org: form.org,
    title: form.title,
    adr: form.adr,
    note: form.note,
    bday: form.bday,
  }
}

async function save() {
  error.value = ''
  formBusy.value = true
  try {
    if (form.uid) {
      // etag: the version the form was loaded from; the server refuses a save over a newer one
      const res = await request<{ etag?: string }>(`/me/contacts/${form.uid}`, { method: 'PUT', body: { ...bodyFromForm(), etag: form.etag || undefined } })
      form.etag = res.etag || ''
    } else {
      const res = await request<{ uid: string }>('/me/contacts', { method: 'POST', body: bodyFromForm() })
      form.uid = res.uid
    }
    await loadList()
  } catch (e: any) {
    // A stale form is replaced with what is saved now (selectContact clears the error).
    if (changedElsewhere(e) && form.uid) await selectContact(form.uid)
    error.value = changedElsewhere(e) ? t('common.changed_elsewhere') : e?.data?.error || t('contacts.error_save')
    // The message appears above the button just pressed and pushes it down: keep it on screen.
    await nextTick()
    formError.value?.scrollIntoView({ block: 'nearest' })
  } finally {
    formBusy.value = false
  }
}

async function remove() {
  if (!form.uid) return
  if (!(await confirm(t('contacts.confirm_delete'), { message: form.full_name || t('contacts.confirm_delete_msg'), confirmText: t('contacts.confirm_delete_btn'), danger: true }))) return
  error.value = ''
  try {
    await request(`/me/contacts/${form.uid}`, { method: 'DELETE' })
    formOpen.value = false
    resetForm()
    await loadList()
  } catch (e: any) {
    error.value = e?.data?.error || t('contacts.error_delete')
  }
}
</script>

<template>
  <div class="flex gap-4 h-[calc(100vh-4rem)]">
    <!-- left panel -->
    <div class="w-full shrink-0 flex-col md:w-72" :class="formOpen ? 'hidden md:flex' : 'flex'">
      <div class="mb-3 flex items-center gap-2">
        <input v-model="search" class="input flex-1" :placeholder="t('contacts.search_ph')" />
        <button class="btn-accent shrink-0" @click="newContact">
          <Icon name="lucide:user-plus" size="18" />
        </button>
        <button class="btn-ghost shrink-0" :title="t('contacts.share_book')" @click="openShare">
          <Icon name="lucide:share-2" size="18" />
        </button>
        <button class="btn-ghost shrink-0" :title="t('contacts.import_vcf')" @click="importInput?.click()">
          <Icon name="lucide:upload" size="18" />
        </button>
        <button class="btn-ghost shrink-0" :title="t('contacts.export_vcf')" @click="exportContacts">
          <Icon name="lucide:download" size="18" />
        </button>
        <input ref="importInput" type="file" accept=".vcf,text/vcard" class="hidden" @change="onImportFile" />
      </div>

      <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
      </p>
      <p v-if="notice" class="mb-3 flex items-center gap-2 text-sm text-accent">
        <Icon name="lucide:check" size="16" /> {{ notice }}
      </p>

      <div class="card flex-1 overflow-auto">
        <div v-if="busy && !list.length" class="p-5 text-sm text-muted">{{ t('contacts.loading') }}</div>
        <div v-else-if="!filtered.length" class="p-8 text-center text-sm text-muted">
          <Icon name="lucide:users" size="28" class="mx-auto mb-2 block opacity-50" />
          {{ search ? t('contacts.not_found') : t('contacts.no_contacts') }}
        </div>
        <ul v-else>
          <li
            v-for="c in filtered"
            :key="c.uid"
            class="flex cursor-pointer items-center gap-3 border-b border-line/50 px-4 py-3 last:border-0 hover:bg-ink/5"
            :class="formOpen && form.uid === c.uid ? 'bg-accent/10' : ''"
            @click="selectContact(c.uid)"
          >
            <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-full bg-accent/20 text-sm font-semibold text-accent">
              {{ (c.full_name || '?')[0].toUpperCase() }}
            </div>
            <div class="min-w-0">
              <div class="truncate text-sm font-medium">{{ c.full_name || t('contacts.no_name') }}</div>
              <div class="truncate text-xs text-muted">{{ c.emails?.[0] || c.phones?.[0] || '' }}</div>
            </div>
          </li>
        </ul>
      </div>
    </div>

    <!-- right panel — form -->
    <div v-if="formOpen" class="w-full overflow-auto md:flex-1">
      <div class="card p-5">
        <div class="mb-4 flex items-center justify-between">
          <h2 class="font-semibold">{{ form.uid ? t('contacts.edit_contact') : t('contacts.new_contact') }}</h2>
          <button class="btn-ghost px-1.5 py-1" @click="formOpen = false">
            <Icon name="lucide:x" size="18" />
          </button>
        </div>

        <!-- photo (display only) -->
        <div v-if="form.photo_uri || form.has_photo" class="mb-4 flex justify-center">
          <img
            v-if="form.photo_uri"
            :src="form.photo_uri"
            :alt="t('contacts.photo')"
            class="h-28 w-28 rounded-full object-cover ring-1 ring-line"
          />
          <div v-else class="flex h-28 w-28 items-center justify-center rounded-full bg-ink/5 text-xs text-muted ring-1 ring-line">
            {{ t('contacts.photo') }}
          </div>
        </div>

        <!-- name -->
        <fieldset class="mb-4 space-y-2">
          <legend class="mb-2 text-xs font-medium text-muted">{{ t('contacts.field_name') }}</legend>
          <input v-model="form.full_name" class="input" :placeholder="t('contacts.field_full_name_ph')" />
          <div class="flex gap-2">
            <input v-model="form.given" class="input flex-1" :placeholder="t('contacts.field_given_ph')" />
            <input v-model="form.family" class="input flex-1" :placeholder="t('contacts.field_family_ph')" />
          </div>
        </fieldset>

        <!-- emails -->
        <fieldset class="mb-4">
          <div class="mb-2 flex items-center justify-between">
            <legend class="text-xs font-medium text-muted">{{ t('contacts.field_email') }}</legend>
            <button class="btn-ghost px-2 py-0.5 text-xs" @click="addEmail">
              <Icon name="lucide:plus" size="14" /> {{ t('contacts.btn_add') }}
            </button>
          </div>
          <div v-for="(e, i) in form.emails" :key="i" class="mb-2 flex gap-2">
            <select v-model="e.type" class="input w-28 shrink-0">
              <option v-for="tp in EMAIL_TYPES" :key="tp" :value="tp">{{ tp }}</option>
            </select>
            <input v-model="e.value" type="email" class="input flex-1" placeholder="email@example.com" />
            <button class="btn-ghost px-2 py-1" @click="removeEmail(i)">
              <Icon name="lucide:x" size="16" class="text-muted" />
            </button>
          </div>
        </fieldset>

        <!-- phones -->
        <fieldset class="mb-4">
          <div class="mb-2 flex items-center justify-between">
            <legend class="text-xs font-medium text-muted">{{ t('contacts.field_phone') }}</legend>
            <button class="btn-ghost px-2 py-0.5 text-xs" @click="addPhone">
              <Icon name="lucide:plus" size="14" /> {{ t('contacts.btn_add') }}
            </button>
          </div>
          <div v-for="(p, i) in form.phones" :key="i" class="mb-2 flex gap-2">
            <select v-model="p.type" class="input w-28 shrink-0">
              <option v-for="tp in PHONE_TYPES" :key="tp" :value="tp">{{ tp }}</option>
            </select>
            <input v-model="p.value" type="tel" class="input flex-1" placeholder="+1 999 …" />
            <button class="btn-ghost px-2 py-1" @click="removePhone(i)">
              <Icon name="lucide:x" size="16" class="text-muted" />
            </button>
          </div>
        </fieldset>

        <!-- org/title -->
        <div class="mb-4 flex gap-2">
          <input v-model="form.org" class="input flex-1" :placeholder="t('contacts.field_org_ph')" />
          <input v-model="form.title" class="input flex-1" :placeholder="t('contacts.field_title_ph')" />
        </div>

        <!-- address -->
        <fieldset class="mb-4 space-y-2">
          <legend class="mb-2 text-xs font-medium text-muted">{{ t('contacts.field_address') }}</legend>
          <input v-model="form.adr.street" class="input" :placeholder="t('contacts.field_street_ph')" />
          <div class="flex gap-2">
            <input v-model="form.adr.city" class="input flex-1" :placeholder="t('contacts.field_city_ph')" />
            <input v-model="form.adr.region" class="input flex-1" :placeholder="t('contacts.field_region_ph')" />
          </div>
          <div class="flex gap-2">
            <input v-model="form.adr.postal" class="input w-32" :placeholder="t('contacts.field_postal_ph')" />
            <input v-model="form.adr.country" class="input flex-1" :placeholder="t('contacts.field_country_ph')" />
          </div>
        </fieldset>

        <!-- birthday -->
        <div class="mb-4">
          <label class="mb-1 block text-xs font-medium text-muted">{{ t('contacts.field_bday') }}</label>
          <input v-model="form.bday" type="date" class="input w-48" />
        </div>

        <!-- note -->
        <div class="mb-4">
          <label class="mb-1 block text-xs font-medium text-muted">{{ t('contacts.field_note') }}</label>
          <textarea v-model="form.note" class="input min-h-20 resize-y" :placeholder="t('contacts.field_note_ph')" />
        </div>

        <!-- On a phone the list column, and the message above it, is hidden while the form is open. -->
        <p v-if="error" ref="formError" class="mb-3 flex items-center gap-2 text-sm text-danger md:hidden">
          <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
        </p>

        <!-- buttons -->
        <div class="flex items-center justify-between gap-2">
          <button
            v-if="form.uid"
            class="btn-danger"
            :disabled="formBusy"
            @click="remove"
          >
            <Icon name="lucide:trash-2" size="16" /> {{ t('contacts.btn_delete') }}
          </button>
          <span v-else />
          <button class="btn-accent" :disabled="formBusy" @click="save">
            <Icon v-if="formBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
            <Icon v-else name="lucide:check" size="16" />
            {{ form.uid ? t('contacts.btn_save') : t('contacts.btn_create') }}
          </button>
        </div>
      </div>
    </div>

    <div v-else class="flex flex-1 items-center justify-center text-muted">
      <div class="text-center">
        <Icon name="lucide:contact" size="40" class="mx-auto mb-3 opacity-30" />
        <p class="text-sm">{{ t('contacts.select_or_create') }}</p>
      </div>
    </div>

    <!-- share modal -->
    <div
      v-if="share.open"
      class="fixed inset-0 z-20 flex items-center justify-center bg-black/50 p-4"
      @click.self="share.open = false"
    >
      <div class="card w-full max-w-md p-5">
        <div class="mb-4 flex items-center justify-between">
          <h2 class="font-semibold">{{ t('contacts.share_title') }}</h2>
          <button class="btn-ghost px-1.5 py-1" @click="share.open = false"><Icon name="lucide:x" size="18" /></button>
        </div>
        <div class="mb-3 flex gap-2">
          <input v-model="share.email" type="email" class="input flex-1" :placeholder="t('contacts.share_email_ph')" @keyup.enter="submitShare" />
          <button class="btn-accent shrink-0" :disabled="share.busy" @click="submitShare">
            <Icon name="lucide:user-plus" size="16" /> {{ t('contacts.share_btn_access') }}
          </button>
        </div>
        <p class="mb-2 text-xs text-muted">{{ t('contacts.share_full_access') }}</p>
        <ul v-if="share.list.length" class="space-y-1">
          <li v-for="sh in share.list" :key="sh.id" class="flex items-center justify-between rounded px-2 py-1.5 hover:bg-ink/5">
            <span class="truncate text-sm">{{ sh.email }}</span>
            <button class="btn-ghost px-2 py-1 text-danger" @click="revokeShare(sh.id)"><Icon name="lucide:x" size="14" /></button>
          </li>
        </ul>
        <p v-else class="text-sm text-muted">{{ t('contacts.share_nobody') }}</p>
      </div>
    </div>
  </div>
</template>
