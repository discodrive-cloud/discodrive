<script setup lang="ts">
import { dateToUtcIso, formatAllDay, isoToUtcDate, localDateKey } from '~/lib/calendarDates'

interface Task {
  uid: string
  summary: string
  notes?: string
  due?: string
  due_all_day: boolean
  priority: number
  completed: boolean
  list_id?: string
}
interface TaskList { id: string; name: string; color: string; is_default: boolean; is_owner: boolean; owner_email?: string; share_id?: string }

const PALETTE = ['#22d3ee', '#34d399', '#f59e0b', '#f43f5e', '#a78bfa', '#3b82f6', '#fb923c', '#e879f9']
const ALL = 'all'
const VIEW_KEY = 'dd.tasks.view'

const { t } = useI18n()
const { request } = useApi()
const { confirm, prompt } = useDialog()

const PRIORITY_OPTIONS = computed(() => [
  { value: 0, label: t('calendar.freq_none') },
  { value: 1, label: t('tasks.priority_high') },
  { value: 5, label: t('tasks.priority_medium') },
  { value: 9, label: t('tasks.priority_low') },
])

const list = ref<Task[]>([])
const error = ref('')
const busy = ref(false)
const showCompleted = ref(false)

const lists = ref<TaskList[]>([])
const view = ref<string>(ALL)
const listMenuFor = ref('')
try { view.value = localStorage.getItem(VIEW_KEY) || ALL } catch {}
watch(view, (v) => { try { localStorage.setItem(VIEW_KEY, v) } catch {} })

const ownListCount = computed(() => lists.value.filter((l) => l.is_owner).length)
const defaultList = computed(() => lists.value.find((l) => l.is_default) || lists.value[0])
const currentList = computed(() => lists.value.find((l) => l.id === view.value))
function listColor(id?: string): string {
  const idx = lists.value.findIndex((l) => l.id === id)
  if (idx < 0) return 'transparent'
  return lists.value[idx].color || PALETTE[idx % PALETTE.length]
}
function listName(id?: string): string { return lists.value.find((l) => l.id === id)?.name || '' }
const openCount = (id: string) => list.value.filter((x) => !x.completed && x.list_id === id).length

// tasks of the current view; all lists are loaded once and filtered here
const viewTasks = computed(() => (view.value === ALL ? list.value : list.value.filter((x) => x.list_id === view.value)))
const completedCount = computed(() => viewTasks.value.filter((t) => t.completed).length)
const visibleTasks = computed(() =>
  showCompleted.value ? viewTasks.value : viewTasks.value.filter((t) => !t.completed),
)
const taskPath = (task: { uid: string; list_id?: string }, suffix = '') =>
  `/me/tasks/${task.uid}${suffix}${task.list_id ? `?list=${task.list_id}` : ''}`

async function loadLists() {
  lists.value = await request<TaskList[]>('/me/task-lists')
  if (view.value !== ALL && !lists.value.some((l) => l.id === view.value)) view.value = ALL
}

async function loadList() {
  error.value = ''
  busy.value = true
  try {
    list.value = await request<Task[]>('/me/tasks')
  } catch (e: any) {
    error.value = e?.data?.error || t('tasks.error_load')
  } finally {
    busy.value = false
  }
}
onMounted(async () => {
  try { await loadLists() } catch (e: any) { error.value = e?.data?.error || t('tasks.error_load') }
  await loadList()
})

async function createList() {
  const name = (await prompt(t('tasks.new_list'), '', { confirmText: t('common.create') }))?.trim()
  if (!name) return
  try {
    const color = PALETTE[lists.value.length % PALETTE.length]
    const l = await request<TaskList>('/me/task-lists', { method: 'POST', body: { name, color } })
    await loadLists()
    view.value = l.id
  } catch (e: any) { error.value = e?.data?.error || t('tasks.error_list_save') }
}
async function renameList(l: TaskList) {
  const name = (await prompt(t('tasks.rename_list'), l.name, { confirmText: t('common.save') }))?.trim()
  if (!name || name === l.name) return
  try { await request(`/me/task-lists/${l.id}`, { method: 'PATCH', body: { name } }); await loadLists() }
  catch (e: any) { error.value = e?.data?.error || t('tasks.error_list_save') }
}
async function setListColor(l: TaskList, color: string) {
  try { await request(`/me/task-lists/${l.id}`, { method: 'PATCH', body: { color } }); await loadLists() }
  catch (e: any) { error.value = e?.data?.error || t('tasks.error_list_save') }
}
async function leaveList(l: TaskList) {
  if (!l.share_id) return
  if (!(await confirm(t('tasks.confirm_leave_list', { name: l.name }), {
    message: t('tasks.confirm_leave_list_msg', { email: l.owner_email || '' }), confirmText: t('tasks.leave_list'), danger: true,
  }))) return
  try {
    await request(`/me/task-lists/${l.id}/shares/${l.share_id}`, { method: 'DELETE' })
    if (view.value === l.id) view.value = ALL
    await loadLists(); await loadList()
  } catch (e: any) { error.value = e?.data?.error || t('tasks.error_leave') }
}

// sharing: one list, any number of recipients
const share = reactive({ open: false, listId: '', name: '', email: '', busy: false, list: [] as { id: string; email: string }[] })
async function loadShares() {
  try { share.list = await request<{ id: string; email: string }[]>(`/me/task-lists/${share.listId}/shares`) }
  catch { share.list = [] }
}
function openShare(l: TaskList) {
  Object.assign(share, { open: true, listId: l.id, name: l.name, email: '', busy: false, list: [] })
  loadShares()
}
async function submitShare() {
  const email = share.email.trim()
  if (!email) return
  share.busy = true
  try {
    await request(`/me/task-lists/${share.listId}/share`, { method: 'POST', body: { email } })
    share.email = ''
    await loadShares()
  } catch (e: any) { error.value = e?.data?.error || t('tasks.error_share') }
  finally { share.busy = false }
}
async function revokeShare(id: string) {
  try { await request(`/me/task-lists/${share.listId}/shares/${id}`, { method: 'DELETE' }); await loadShares() }
  catch (e: any) { error.value = e?.data?.error || t('tasks.error_share') }
}
useModalEscape(computed(() => share.open), () => { share.open = false })

async function deleteList(l: TaskList) {
  const n = list.value.filter((x) => x.list_id === l.id).length
  if (!(await confirm(t('tasks.confirm_delete_list', { name: l.name }), {
    message: n ? t('tasks.confirm_delete_list_tasks', { n }) : undefined, confirmText: t('common.delete'), danger: true,
  }))) return
  try {
    await request(`/me/task-lists/${l.id}`, { method: 'DELETE' })
    if (view.value === l.id) view.value = ALL
    await loadLists(); await loadList()
  } catch (e: any) { error.value = e?.data?.error || t('tasks.error_list_delete') }
}

function dueLabel(task: Task): string {
  if (!task.due) return ''
  // an all-day due date is a date (sent as UTC midnight): read it in UTC, not locally
  return task.due_all_day
    ? formatAllDay(task.due)
    : new Date(task.due).toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}

async function toggleDone(task: Task) {
  const next = !task.completed
  task.completed = next
  try {
    await request(taskPath(task, '/done'), { method: 'PUT', body: { completed: next } })
    await loadList()
  } catch (e: any) {
    task.completed = !next
    error.value = e?.data?.error || t('tasks.error_update')
  }
}

const modalOpen = ref(false)
const modalBusy = ref(false)
const form = reactive<Task>({
  uid: '', summary: '', notes: '', due: '', due_all_day: false, priority: 0, completed: false, list_id: '',
})
const formOrigList = ref('') // the list the edited task lives in (form.list_id may move it)
const dueDateTime = ref('')
const dueDate = ref('')

function pad(n: number) { return String(n).padStart(2, '0') }
function isoToLocal(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function localToIso(v: string): string { return v ? new Date(v).toISOString() : '' }

function resetForm() {
  form.uid = ''; form.summary = ''; form.notes = ''; form.due = ''
  form.due_all_day = false; form.priority = 0; form.completed = false
  form.list_id = currentList.value?.id || defaultList.value?.id || ''; formOrigList.value = ''
  dueDateTime.value = ''; dueDate.value = ''
}

function newTask() { resetForm(); modalOpen.value = true }

async function openTask(task: Task) {
  error.value = ''
  try {
    const d = await request<Task>(taskPath(task))
    form.list_id = d.list_id || ''; formOrigList.value = d.list_id || ''
    form.uid = d.uid; form.summary = d.summary; form.notes = d.notes || ''
    form.due = d.due || ''; form.due_all_day = d.due_all_day
    form.priority = d.priority || 0; form.completed = d.completed
    if (!d.due) { dueDateTime.value = ''; dueDate.value = '' }
    else if (d.due_all_day) { dueDate.value = isoToUtcDate(d.due); dueDateTime.value = `${dueDate.value}T09:00` }
    else { dueDateTime.value = isoToLocal(d.due); dueDate.value = localDateKey(new Date(d.due)) }
    modalOpen.value = true
  } catch (e: any) { error.value = e?.data?.error || t('tasks.error_load_task') }
}

function buildBody() {
  // all-day: the picked date at UTC midnight — local midnight east of UTC is the previous
  // day in UTC, and the task was saved a day early
  const due = form.due_all_day ? dateToUtcIso(dueDate.value) : localToIso(dueDateTime.value)
  return { summary: form.summary, notes: form.notes, due, due_all_day: form.due_all_day, priority: form.priority, completed: form.completed, list_id: form.list_id || undefined }
}

async function save() {
  if (!form.summary.trim()) { error.value = t('tasks.error_name_required'); return }
  error.value = ''
  modalBusy.value = true
  try {
    if (form.uid) {
      await request(taskPath({ uid: form.uid, list_id: formOrigList.value }), { method: 'PUT', body: buildBody() })
    } else {
      await request('/me/tasks', { method: 'POST', body: buildBody() })
    }
    modalOpen.value = false
    await loadList()
  } catch (e: any) { error.value = e?.data?.error || t('tasks.error_save') }
  finally { modalBusy.value = false }
}

async function remove() {
  if (!form.uid) return
  if (!(await confirm(t('tasks.confirm_delete'), { message: form.summary || t('tasks.no_name'), confirmText: t('common.delete'), danger: true }))) return
  error.value = ''
  try {
    await request(taskPath({ uid: form.uid, list_id: formOrigList.value }), { method: 'DELETE' })
    modalOpen.value = false
    await loadList()
  } catch (e: any) { error.value = e?.data?.error || t('tasks.error_delete') }
}

useModalEscape(modalOpen, () => { modalOpen.value = false })
watch(view, () => { showCompleted.value = false; listMenuFor.value = '' })
</script>

<template>
  <div class="flex gap-4 h-[calc(100vh-4rem)]">
    <!-- lists -->
    <aside class="hidden w-56 shrink-0 overflow-auto md:block">
      <div class="mb-2 flex items-center justify-between">
        <span class="text-xs font-medium text-muted">{{ t('tasks.lists') }}</span>
        <button class="btn-ghost px-1.5 py-1" :title="t('tasks.new_list')" @click="createList">
          <Icon name="lucide:plus" size="16" />
        </button>
      </div>
      <ul class="space-y-1">
        <li>
          <button class="flex w-full items-center gap-2 rounded px-2 py-1.5 text-sm hover:bg-ink/5"
            :class="view === ALL ? 'bg-accent/15 text-accent' : 'text-ink'" @click="view = ALL">
            <Icon name="lucide:layers" size="14" class="shrink-0" />
            <span class="flex-1 truncate text-left">{{ t('tasks.all_lists') }}</span>
            <span class="text-xs text-muted">{{ list.filter((x) => !x.completed).length || '' }}</span>
          </button>
        </li>
        <li v-for="l in lists" :key="l.id" class="group relative flex items-center rounded hover:bg-ink/5"
          :class="view === l.id ? 'bg-accent/15' : ''" @mouseleave="listMenuFor = ''">
          <button class="flex min-w-0 flex-1 items-center gap-2 px-2 py-1.5 text-sm" @click="view = l.id">
            <span class="h-3 w-3 shrink-0 rounded-full" :style="{ backgroundColor: listColor(l.id) }" />
            <span class="flex-1 truncate text-left" :class="view === l.id ? 'text-accent' : 'text-ink'">{{ l.name }}</span>
            <Icon v-if="!l.is_owner" name="lucide:users" size="13" class="shrink-0 text-accent"
              :title="t('tasks.shared_by', { email: l.owner_email || '' })" />
            <span class="text-xs text-muted group-hover:hidden">{{ openCount(l.id) || '' }}</span>
          </button>
          <div class="relative hidden shrink-0 pr-1 group-hover:block">
            <button class="btn-ghost px-1 py-0.5" @click="listMenuFor = listMenuFor === l.id ? '' : l.id">
              <Icon name="lucide:more-horizontal" size="14" />
            </button>
            <div v-if="listMenuFor === l.id" class="absolute right-0 z-10 mt-1 w-40 rounded-lg border border-line bg-panel p-2 shadow-xl">
              <template v-if="l.is_owner">
                <div class="mb-2 flex flex-wrap gap-1">
                  <button v-for="p in PALETTE" :key="p" class="h-5 w-5 rounded-sm ring-1 ring-line" :style="{ backgroundColor: p }" @click="setListColor(l, p); listMenuFor = ''" />
                </div>
                <button class="btn-ghost w-full justify-start px-2 py-1 text-xs" @click="renameList(l); listMenuFor = ''">
                  <Icon name="lucide:pencil" size="13" /> {{ t('tasks.rename_list') }}
                </button>
                <button class="btn-ghost w-full justify-start px-2 py-1 text-xs" @click="openShare(l); listMenuFor = ''">
                  <Icon name="lucide:share-2" size="13" /> {{ t('tasks.share_list') }}
                </button>
                <button v-if="ownListCount > 1" class="btn-ghost w-full justify-start px-2 py-1 text-xs text-danger" @click="deleteList(l); listMenuFor = ''">
                  <Icon name="lucide:trash-2" size="13" /> {{ t('tasks.delete_list') }}
                </button>
              </template>
              <template v-else>
                <p class="mb-1 truncate px-2 text-xs text-muted">{{ t('tasks.shared_by', { email: l.owner_email || '' }) }}</p>
                <button class="btn-ghost w-full justify-start px-2 py-1 text-xs text-danger" @click="leaveList(l); listMenuFor = ''">
                  <Icon name="lucide:log-out" size="13" /> {{ t('tasks.leave_list') }}
                </button>
              </template>
            </div>
          </div>
        </li>
      </ul>
    </aside>

    <div class="flex min-h-0 min-w-0 flex-1 flex-col">
    <div class="mb-4 flex items-center justify-between gap-3">
      <h1 class="truncate text-xl font-semibold">{{ currentList?.name || t('nav.tasks') }}</h1>
      <div class="flex shrink-0 items-center gap-2">
      <!-- lists on narrow screens -->
      <select v-model="view" class="input w-auto py-1.5 text-sm md:hidden" :aria-label="t('tasks.lists')">
        <option :value="ALL">{{ t('tasks.all_lists') }}</option>
        <option v-for="l in lists" :key="l.id" :value="l.id">{{ l.name }}</option>
      </select>
      <button v-if="currentList?.is_owner" class="btn-ghost px-1.5 py-1.5 md:hidden" :title="t('tasks.share_list')" @click="openShare(currentList)">
        <Icon name="lucide:share-2" size="18" />
      </button>
      <button v-if="currentList && !currentList.is_owner" class="btn-ghost px-1.5 py-1.5 text-danger md:hidden" :title="t('tasks.leave_list')" @click="leaveList(currentList)">
        <Icon name="lucide:log-out" size="18" />
      </button>
      <button class="btn-ghost px-1.5 py-1.5 md:hidden" :title="t('tasks.new_list')" @click="createList">
        <Icon name="lucide:list-plus" size="18" />
      </button>
      <button class="btn-accent" @click="newTask">
        <Icon name="lucide:plus" size="16" /> {{ t('tasks.new_task') }}
      </button>
      </div>
    </div>

    <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <label v-if="completedCount" class="mb-3 flex items-center gap-2 text-sm text-muted">
      <input v-model="showCompleted" type="checkbox" class="h-4 w-4" />
      {{ t('tasks.show_all', { n: completedCount }) }}
    </label>

    <div class="card overflow-hidden">
      <div v-if="busy && !viewTasks.length" class="p-5 text-sm text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="!viewTasks.length" class="p-10 text-center text-sm text-muted">
        <Icon name="lucide:list-checks" size="28" class="mx-auto mb-2 block opacity-50" />
        {{ t('tasks.empty') }}
      </div>
      <div v-else-if="!visibleTasks.length" class="p-10 text-center text-sm text-muted">
        <Icon name="lucide:check-check" size="28" class="mx-auto mb-2 block opacity-50" />
        {{ t('tasks.all_done') }}
      </div>
      <ul v-else>
        <li
          v-for="task in visibleTasks"
          :key="task.uid"
          class="flex items-center gap-3 border-b border-line/50 px-4 py-3 last:border-0 hover:bg-ink/5"
        >
          <button class="shrink-0" :title="task.completed ? t('tasks.unmark') : t('tasks.mark_done')" @click="toggleDone(task)">
            <Icon
              :name="task.completed ? 'lucide:check-circle-2' : 'lucide:circle'"
              size="20"
              :class="task.completed ? 'text-accent' : 'text-muted'"
            />
          </button>
          <div class="min-w-0 flex-1 cursor-pointer" @click="openTask(task)">
            <div class="truncate text-sm" :class="task.completed ? 'text-muted line-through' : ''">
              {{ task.summary || `(${t('tasks.no_name')})` }}
            </div>
            <div v-if="task.due || (view === ALL && lists.length > 1)" class="flex items-center gap-2 text-xs text-muted">
              <span v-if="view === ALL && lists.length > 1" class="flex min-w-0 items-center gap-1">
                <span class="h-2 w-2 shrink-0 rounded-full" :style="{ backgroundColor: listColor(task.list_id) }" />
                <span class="truncate">{{ listName(task.list_id) }}</span>
              </span>
              <span v-if="task.due">{{ dueLabel(task) }}</span>
            </div>
          </div>
          <Icon v-if="task.priority >= 1 && task.priority <= 4" name="lucide:flag" size="14" class="shrink-0 text-danger" />
        </li>
      </ul>
    </div>

    </div>

    <!-- share a list -->
    <div v-if="share.open" class="fixed inset-0 z-20 flex items-center justify-center bg-black/50 p-4" @click.self="share.open = false">
      <div class="card w-full max-w-md p-5">
        <div class="mb-4 flex items-center justify-between gap-2">
          <h2 class="truncate font-semibold">{{ t('tasks.share_title', { name: share.name }) }}</h2>
          <button class="btn-ghost px-1.5 py-1" @click="share.open = false"><Icon name="lucide:x" size="18" /></button>
        </div>
        <div class="mb-3 flex gap-2">
          <input v-model="share.email" type="email" class="input flex-1" :placeholder="t('tasks.share_email_ph')" @keyup.enter="submitShare" />
          <button class="btn-accent shrink-0" :disabled="share.busy" @click="submitShare">
            <Icon name="lucide:user-plus" size="16" /> {{ t('tasks.share_add') }}
          </button>
        </div>
        <p class="mb-2 text-xs text-muted">{{ t('tasks.share_full_access') }}</p>
        <ul v-if="share.list.length" class="space-y-1">
          <li v-for="sh in share.list" :key="sh.id" class="flex items-center justify-between rounded px-2 py-1.5 hover:bg-ink/5">
            <span class="truncate text-sm">{{ sh.email }}</span>
            <button class="btn-ghost px-2 py-1 text-danger" :title="t('tasks.share_revoke')" @click="revokeShare(sh.id)"><Icon name="lucide:x" size="14" /></button>
          </li>
        </ul>
        <p v-else class="text-sm text-muted">{{ t('tasks.share_nobody') }}</p>
      </div>
    </div>

    <!-- modal -->
    <div
      v-if="modalOpen"
      class="fixed inset-0 z-20 flex items-center justify-center bg-black/50 p-4"
      @click.self="modalOpen = false"
    >
      <div class="card w-full max-w-md p-5">
        <div class="mb-4 flex items-center justify-between">
          <h2 class="font-semibold">{{ form.uid ? t('tasks.edit_task') : t('tasks.new_task_form') }}</h2>
          <button class="btn-ghost px-1.5 py-1" @click="modalOpen = false">
            <Icon name="lucide:x" size="18" />
          </button>
        </div>

        <div class="space-y-3">
          <input v-model="form.summary" class="input" :placeholder="t('tasks.task_name_ph')" />

          <div v-if="lists.length > 1">
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('tasks.list') }}</label>
            <select v-model="form.list_id" class="input">
              <option v-for="l in lists" :key="l.id" :value="l.id">{{ l.name }}</option>
            </select>
          </div>

          <label class="flex items-center gap-2 text-sm">
            <input v-model="form.completed" type="checkbox" class="h-4 w-4" /> {{ t('tasks.completed') }}
          </label>

          <div>
            <label class="mb-1 flex items-center justify-between text-xs font-medium text-muted">
              <span>{{ t('tasks.due') }}</span>
              <label class="flex items-center gap-1.5 normal-case">
                <input v-model="form.due_all_day" type="checkbox" class="h-3.5 w-3.5" /> {{ t('calendar.all_day') }}
              </label>
            </label>
            <input v-if="form.due_all_day" v-model="dueDate" type="date" class="input" />
            <input v-else v-model="dueDateTime" type="datetime-local" class="input" />
          </div>

          <div>
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('tasks.priority') }}</label>
            <select v-model.number="form.priority" class="input">
              <option v-for="p in PRIORITY_OPTIONS" :key="p.value" :value="p.value">{{ p.label }}</option>
            </select>
          </div>

          <div>
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('tasks.notes') }}</label>
            <textarea v-model="form.notes" class="input min-h-32 resize-y" :placeholder="t('tasks.notes_ph')" />
          </div>
        </div>

        <div class="mt-5 flex items-center justify-between gap-2">
          <button v-if="form.uid" class="btn-danger" :disabled="modalBusy" @click="remove">
            <Icon name="lucide:trash-2" size="16" /> {{ t('common.delete') }}
          </button>
          <span v-else />
          <button class="btn-accent" :disabled="modalBusy" @click="save">
            <Icon v-if="modalBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
            <Icon v-else name="lucide:check" size="16" />
            {{ form.uid ? t('common.save') : t('common.create') }}
          </button>
        </div>
      </div>
    </div>
  </div>
</template>
