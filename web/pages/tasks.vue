<script setup lang="ts">
interface Task {
  uid: string
  summary: string
  notes?: string
  due?: string
  due_all_day: boolean
  priority: number
  completed: boolean
}

const { t } = useI18n()
const { request } = useApi()
const { confirm } = useDialog()

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

const completedCount = computed(() => list.value.filter((t) => t.completed).length)
const visibleTasks = computed(() =>
  showCompleted.value ? list.value : list.value.filter((t) => !t.completed),
)

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
onMounted(loadList)

function dueLabel(task: Task): string {
  if (!task.due) return ''
  const d = new Date(task.due)
  return task.due_all_day
    ? d.toLocaleDateString()
    : d.toLocaleString(undefined, { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}

async function toggleDone(task: Task) {
  const next = !task.completed
  task.completed = next
  try {
    await request(`/me/tasks/${task.uid}/done`, { method: 'PUT', body: { completed: next } })
    await loadList()
  } catch (e: any) {
    task.completed = !next
    error.value = e?.data?.error || t('tasks.error_update')
  }
}

const modalOpen = ref(false)
const modalBusy = ref(false)
const form = reactive<Task>({
  uid: '', summary: '', notes: '', due: '', due_all_day: false, priority: 0, completed: false,
})
const dueDateTime = ref('')
const dueDate = ref('')

function pad(n: number) { return String(n).padStart(2, '0') }
function isoToLocal(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function isoToDate(iso: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`
}
function localToIso(v: string): string { return v ? new Date(v).toISOString() : '' }
function dateToIso(v: string): string { return v ? new Date(v + 'T00:00:00').toISOString() : '' }

function resetForm() {
  form.uid = ''; form.summary = ''; form.notes = ''; form.due = ''
  form.due_all_day = false; form.priority = 0; form.completed = false
  dueDateTime.value = ''; dueDate.value = ''
}

function newTask() { resetForm(); modalOpen.value = true }

async function openTask(uid: string) {
  error.value = ''
  try {
    const d = await request<Task>(`/me/tasks/${uid}`)
    form.uid = d.uid; form.summary = d.summary; form.notes = d.notes || ''
    form.due = d.due || ''; form.due_all_day = d.due_all_day
    form.priority = d.priority || 0; form.completed = d.completed
    dueDateTime.value = d.due ? isoToLocal(d.due) : ''
    dueDate.value = d.due ? isoToDate(d.due) : ''
    modalOpen.value = true
  } catch (e: any) { error.value = e?.data?.error || t('tasks.error_load_task') }
}

function buildBody() {
  const due = form.due_all_day ? dateToIso(dueDate.value) : localToIso(dueDateTime.value)
  return { summary: form.summary, notes: form.notes, due, due_all_day: form.due_all_day, priority: form.priority, completed: form.completed }
}

async function save() {
  if (!form.summary.trim()) { error.value = t('tasks.error_name_required'); return }
  error.value = ''
  modalBusy.value = true
  try {
    if (form.uid) {
      await request(`/me/tasks/${form.uid}`, { method: 'PUT', body: buildBody() })
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
    await request(`/me/tasks/${form.uid}`, { method: 'DELETE' })
    modalOpen.value = false
    await loadList()
  } catch (e: any) { error.value = e?.data?.error || t('tasks.error_delete') }
}

useModalEscape(modalOpen, () => { modalOpen.value = false })
</script>

<template>
  <div class="h-[calc(100vh-4rem)]">
    <div class="mb-4 flex items-center justify-between">
      <h1 class="text-xl font-semibold">{{ t('nav.tasks') }}</h1>
      <button class="btn-accent" @click="newTask">
        <Icon name="lucide:plus" size="16" /> {{ t('tasks.new_task') }}
      </button>
    </div>

    <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <label v-if="completedCount" class="mb-3 flex items-center gap-2 text-sm text-muted">
      <input v-model="showCompleted" type="checkbox" class="h-4 w-4" />
      {{ t('tasks.show_all', { n: completedCount }) }}
    </label>

    <div class="card overflow-hidden">
      <div v-if="busy && !list.length" class="p-5 text-sm text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="!list.length" class="p-10 text-center text-sm text-muted">
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
          <div class="min-w-0 flex-1 cursor-pointer" @click="openTask(task.uid)">
            <div class="truncate text-sm" :class="task.completed ? 'text-muted line-through' : ''">
              {{ task.summary || `(${t('tasks.no_name')})` }}
            </div>
            <div v-if="task.due" class="text-xs text-muted">{{ dueLabel(task) }}</div>
          </div>
          <Icon v-if="task.priority >= 1 && task.priority <= 4" name="lucide:flag" size="14" class="shrink-0 text-danger" />
        </li>
      </ul>
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
