<script setup lang="ts">
interface Overview {
  disk: { total: number; used: number; free: number }
  // limit is the STORAGE_TOTAL_GB cap; total is null when discodrive may use the whole
  // disk. used/free are the space taken and left inside that cap — free is what may
  // still be written, which is not assignable (what may still be promised to users).
  limit: { total: number | null; assignable: number | null; used: number | null; free: number | null }
  // Percent-free thresholds that turn a tile amber/red; the server alerts admins by email
  // at exactly the same points.
  thresholds: { warn: number; critical: number }
  users: { id: string; email: string; role: string; quota: number | null; used: number }[]
}

const { t } = useI18n()
const { request } = useApi()
const data = ref<Overview | null>(null)
const error = ref('')

async function load() {
  error.value = ''
  try {
    data.value = await request<Overview>('/admin/overview')
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_load')
  }
}
onMounted(load)

const diskPct = computed(() => {
  const d = data.value?.disk
  return d && d.total ? Math.round((d.used / d.total) * 100) : 0
})
const limitPct = computed(() => {
  const l = data.value?.limit
  return l?.total ? Math.round(((l.used ?? 0) / l.total) * 100) : 0
})

// 'crit' | 'warn' | 'ok' from how much of a limit is still free. Unknown limits (no cap,
// or a disk the server could not stat) are never an alarm.
function level(free: number | null | undefined, total: number | null | undefined) {
  if (!total || free == null) return 'ok'
  const freePct = (free / total) * 100
  const th = data.value?.thresholds
  if (!th) return 'ok'
  if (freePct <= th.critical) return 'crit'
  if (freePct <= th.warn) return 'warn'
  return 'ok'
}
const diskLevel = computed(() => level(data.value?.disk.free, data.value?.disk.total))
const limitLevel = computed(() => level(data.value?.limit.free, data.value?.limit.total))

const textClass = (l: string) => (l === 'crit' ? 'text-danger' : l === 'warn' ? 'text-warn' : '')
const barClass = (l: string) => (l === 'crit' ? 'bg-danger' : l === 'warn' ? 'bg-warn' : 'bg-accent')
const cardClass = (l: string) =>
  l === 'crit' ? 'border-danger/60' : l === 'warn' ? 'border-warn/60' : ''

interface RescanRun {
  id: number
  user_email: string | null
  requested_by: string
  created_at: string
  started_at: string | null
  finished_at: string | null
  imported: number
  missing: number
  changed: number
  errors: number
  error_text: string | null
}
const runs = ref<RescanRun[]>([])
let poll: ReturnType<typeof setInterval> | undefined

async function loadRuns() {
  try {
    runs.value = (await request<{ requests: RescanRun[] }>('/admin/rescan')).requests
  } catch {
    // the section keeps what it showed
  }
}
async function rescanAll() {
  try {
    await request('/admin/rescan', { method: 'POST', body: {} })
    await loadRuns()
  } catch (e: any) {
    error.value = e?.data?.error || t('admin.error_rescan')
  }
}
function runState(r: RescanRun) {
  if (r.finished_at) {
    const res = t('admin.rescan_result', { imported: r.imported, missing: r.missing, changed: r.changed })
    return r.errors > 0 ? `${res} · ${t('admin.rescan_errors', { n: r.errors })}` : res
  }
  return r.started_at ? t('admin.rescan_running') : t('admin.rescan_waiting')
}
function runBy(r: RescanRun) {
  return r.requested_by.startsWith('admin:') ? 'admin' : r.requested_by
}
onMounted(() => {
  loadRuns()
  poll = setInterval(() => {
    if (runs.value.some((r) => !r.finished_at)) loadRuns()
  }, 2000)
})
onUnmounted(() => clearInterval(poll))
</script>

<template>
  <div>
    <h1 class="mb-4 text-xl font-semibold">{{ t('admin.dashboard') }}</h1>
    <p v-if="error" class="mb-4 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <div class="mb-6 grid gap-4 sm:grid-cols-3">
      <div class="card p-5" :class="cardClass(diskLevel)">
        <div class="mb-2 text-xs text-muted">{{ t('admin.disk_used') }}</div>
        <div class="text-2xl font-semibold">{{ formatBytes(data?.disk.used) }}</div>
        <div class="mt-1 text-xs text-muted">{{ t('admin.of') }} {{ formatBytes(data?.disk.total) }}</div>
        <div class="mt-3 h-1.5 w-full overflow-hidden rounded-full bg-ink/5">
          <div class="h-full rounded-full" :class="barClass(diskLevel)" :style="{ width: diskPct + '%' }" />
        </div>
      </div>
      <div class="card p-5" :class="cardClass(diskLevel)">
        <div class="mb-2 text-xs text-muted">{{ t('admin.disk_free') }}</div>
        <div class="text-2xl font-semibold" :class="textClass(diskLevel)">{{ formatBytes(data?.disk.free) }}</div>
        <div v-if="diskLevel !== 'ok'" class="mt-1 flex items-center gap-1 text-xs" :class="textClass(diskLevel)">
          <Icon name="lucide:triangle-alert" size="14" /> {{ t('admin.low_space') }}
        </div>
      </div>
      <div class="card p-5">
        <div class="mb-2 text-xs text-muted">{{ t('admin.users_count') }}</div>
        <div class="text-2xl font-semibold">{{ data?.users.length ?? '—' }}</div>
      </div>
      <template v-if="data?.limit.total != null">
        <div class="card p-5">
          <div class="mb-2 text-xs text-muted">{{ t('admin.storage_limit') }}</div>
          <div class="text-2xl font-semibold">{{ formatBytes(data.limit.total) }}</div>
          <div class="mt-1 text-xs text-muted">
            {{ t('admin.quota_assignable', { size: formatBytes(data.limit.assignable ?? 0) }) }}
          </div>
        </div>
        <!-- Space left inside the cap: what may still be written, as opposed to the
             assignable figure above, which is what may still be promised to users. -->
        <div class="card p-5" :class="cardClass(limitLevel)">
          <div class="mb-2 text-xs text-muted">{{ t('admin.limit_free') }}</div>
          <div class="text-2xl font-semibold" :class="textClass(limitLevel)">{{ formatBytes(data.limit.free) }}</div>
          <div class="mt-1 text-xs text-muted">
            {{ t('admin.limit_used', { used: formatBytes(data.limit.used), total: formatBytes(data.limit.total) }) }}
          </div>
          <div class="mt-3 h-1.5 w-full overflow-hidden rounded-full bg-ink/5">
            <div class="h-full rounded-full" :class="barClass(limitLevel)" :style="{ width: limitPct + '%' }" />
          </div>
          <div v-if="limitLevel !== 'ok'" class="mt-2 flex items-center gap-1 text-xs" :class="textClass(limitLevel)">
            <Icon name="lucide:triangle-alert" size="14" /> {{ t('admin.low_space') }}
          </div>
        </div>
      </template>
    </div>

    <div class="card overflow-hidden">
      <table class="w-full text-sm">
        <thead class="border-b border-line text-left text-xs text-muted">
          <tr>
            <th class="px-4 py-3 font-medium">{{ t('admin.col_email') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.col_role') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.col_used') }}</th>
            <th class="px-4 py-3 font-medium">{{ t('admin.col_quota') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="u in data?.users" :key="u.id" class="border-b border-line/50 last:border-0">
            <td class="px-4 py-3">{{ u.email }}</td>
            <td class="px-4 py-3">
              <span class="rounded bg-ink/5 px-1.5 py-0.5 font-mono text-[10px] uppercase">{{ u.role }}</span>
            </td>
            <td class="px-4 py-3 text-muted">{{ formatBytes(u.used) }}</td>
            <td class="px-4 py-3 text-muted">{{ u.quota == null ? t('admin.no_limit') : formatBytes(u.quota) }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="mt-6 card p-5">
      <div class="mb-3 flex items-center justify-between gap-4">
        <div>
          <h2 class="text-sm font-medium">{{ t('admin.rescan_section') }}</h2>
          <p class="mt-1 text-xs text-muted">{{ t('admin.rescan_hint') }}</p>
        </div>
        <button class="btn-ghost shrink-0" @click="rescanAll">
          <Icon name="lucide:refresh-cw" size="16" /> {{ t('admin.rescan_all') }}
        </button>
      </div>
      <table v-if="runs.length" class="w-full text-sm">
        <thead class="border-b border-line text-left text-xs text-muted">
          <tr>
            <th class="py-2 font-medium">{{ t('admin.rescan_col_target') }}</th>
            <th class="py-2 font-medium">{{ t('admin.rescan_col_by') }}</th>
            <th class="py-2 font-medium">{{ t('admin.rescan_col_when') }}</th>
            <th class="py-2 font-medium">{{ t('admin.rescan_col_result') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="r in runs" :key="r.id" class="border-b border-line/50 last:border-0">
            <td class="py-2">{{ r.user_email ?? t('admin.rescan_everyone') }}</td>
            <td class="py-2 text-muted">{{ runBy(r) }}</td>
            <td class="py-2 text-muted">{{ new Date(r.created_at).toLocaleString() }}</td>
            <td class="py-2" :class="r.errors > 0 ? 'text-danger' : 'text-muted'" :title="r.error_text ?? ''">
              {{ runState(r) }}
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>
