<script setup lang="ts">
interface UserRow {
  id: string
  email: string
  role: string
  quota: number | null
  used: number
}

const { t } = useI18n()
const { request, session } = useApi()
const { confirm } = useDialog()
const users = ref<UserRow[]>([])
const error = ref('')

const showCreate = ref(false)
const draft = reactive({ email: '', password: '', role: 'user', quotaGb: '' })
const creating = ref(false)

const editId = ref('')
const editRole = ref('user')
const editQuotaGb = ref('')

async function load() {
  error.value = ''
  try {
    const data = await request<{ users: UserRow[] }>('/admin/overview')
    users.value = data.users
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_load')
  }
}
onMounted(load)

async function create() {
  error.value = ''
  creating.value = true
  try {
    await request('/admin/users', {
      method: 'POST',
      body: { email: draft.email, password: draft.password, role: draft.role, quota: gbToBytes(draft.quotaGb) },
    })
    Object.assign(draft, { email: '', password: '', role: 'user', quotaGb: '' })
    showCreate.value = false
    await load()
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_create')
  } finally {
    creating.value = false
  }
}

function startEdit(u: UserRow) {
  editId.value = u.id
  editRole.value = u.role
  editQuotaGb.value = bytesToGb(u.quota)
}

async function saveEdit(id: string) {
  error.value = ''
  try {
    await request(`/admin/users/${id}`, {
      method: 'PATCH',
      body: { role: editRole.value, quota: gbToBytes(editQuotaGb.value) },
    })
    editId.value = ''
    await load()
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_save')
  }
}

async function remove(u: UserRow) {
  if (!(await confirm(t('admin.confirm_delete_user'), { message: t('admin.confirm_delete_user_msg', { email: u.email }), confirmText: t('admin.confirm_delete_user_btn'), danger: true }))) return
  error.value = ''
  try {
    await request(`/admin/users/${u.id}`, { method: 'DELETE' })
    await load()
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_delete')
  }
}

const notice = ref('')

async function rescan(id: string) {
  error.value = ''
  notice.value = ''
  try {
    await request('/admin/rescan', { method: 'POST', body: { user_id: id } })
    notice.value = t('admin.rescan_queued')
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_rescan')
  }
}
</script>

<template>
  <div>
    <div class="mb-4 flex items-center justify-between">
      <h1 class="text-xl font-semibold">{{ t('admin.users_title') }}</h1>
      <button class="btn-accent" @click="showCreate = !showCreate">
        <Icon name="lucide:user-plus" size="18" /> {{ t('admin.btn_create') }}
      </button>
    </div>

    <p v-if="error" class="mb-4 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>
    <p v-if="notice" class="mb-4 text-sm text-muted">{{ notice }}</p>

    <form v-if="showCreate" class="card mb-4 grid gap-3 p-4 sm:grid-cols-5" @submit.prevent="create">
      <input v-model="draft.email" type="email" class="input sm:col-span-2" :placeholder="t('admin.placeholder_email')" autocomplete="off" />
      <input v-model="draft.password" type="password" class="input" :placeholder="t('admin.placeholder_password')" autocomplete="new-password" />
      <select v-model="draft.role" class="input">
        <option value="user">user</option>
        <option value="admin">admin</option>
      </select>
      <input v-model="draft.quotaGb" type="number" min="0" step="0.5" class="input" :placeholder="t('admin.placeholder_quota')" />
      <div class="sm:col-span-5">
        <button type="submit" class="btn-accent" :disabled="creating">
          <Icon v-if="creating" name="lucide:loader-circle" class="animate-spin" size="18" />
          <Icon v-else name="lucide:check" size="18" /> {{ t('admin.btn_add') }}
        </button>
      </div>
    </form>

    <div class="card overflow-hidden">
      <table class="w-full text-sm">
        <thead class="border-b border-line text-left text-xs text-muted">
          <tr>
            <th class="px-4 py-3 font-medium">{{ t('admin.col_email') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.col_role') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.col_used') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.col_quota') }}</th>
            <th class="px-4 py-3" />
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in users" :key="u.id" class="border-b border-line/50 last:border-0">
            <td class="px-4 py-3">{{ u.email }}</td>
            <td class="px-4 py-3">
              <select v-if="editId === u.id" v-model="editRole" class="input py-1">
                <option value="user">user</option>
                <option value="admin">admin</option>
              </select>
              <span v-else class="rounded bg-ink/5 px-1.5 py-0.5 font-mono text-[10px] uppercase">{{ u.role }}</span>
            </td>
            <td class="px-4 py-3 text-muted">{{ formatBytes(u.used) }}</td>
            <td class="px-4 py-3 text-muted">
              <input v-if="editId === u.id" v-model="editQuotaGb" type="number" min="0" step="0.5"
                     class="input w-24 py-1" placeholder="GB" />
              <span v-else>{{ u.quota == null ? t('admin.no_limit') : formatBytes(u.quota) }}</span>
            </td>
            <td class="px-4 py-3">
              <div class="flex justify-end gap-1">
                <template v-if="editId === u.id">
                  <button class="btn-accent px-2 py-1" @click="saveEdit(u.id)"><Icon name="lucide:check" size="16" /></button>
                  <button class="btn-ghost px-2 py-1" @click="editId = ''"><Icon name="lucide:x" size="16" /></button>
                </template>
                <template v-else>
                  <button class="btn-ghost px-2 py-1" :title="t('admin.rescan_user')" @click="rescan(u.id)">
                    <Icon name="lucide:refresh-cw" size="16" />
                  </button>
                  <button class="btn-ghost px-2 py-1" @click="startEdit(u)"><Icon name="lucide:pencil" size="16" /></button>
                  <button v-if="u.email !== session.email"
                          class="btn-danger px-2 py-1" @click="remove(u)"><Icon name="lucide:trash-2" size="16" /></button>
                </template>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
