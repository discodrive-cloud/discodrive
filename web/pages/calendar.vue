<script setup lang="ts">
interface EventOccurrence {
  uid: string; recurrence_id?: string; summary: string; location?: string
  start: string; end: string; all_day: boolean; recurring: boolean; calendar_id: string
}
interface EventDetail {
  uid: string; summary: string; location: string; description: string
  start: string; end: string; all_day: boolean; freq: string; until: string; alarm: string; calendar_id: string
}
interface CalendarMeta { id: string; name: string; color: string; is_default: boolean; is_owner: boolean; owner_email?: string }

const PALETTE = ['#22d3ee', '#34d399', '#f59e0b', '#f43f5e', '#a78bfa', '#3b82f6', '#fb923c', '#e879f9']

const { t, tm, rt } = useI18n()
const { request } = useApi()
const { confirm, prompt } = useDialog()
const { today } = useToday()

const FREQ_OPTIONS = computed(() => [
  { value: '', label: t('calendar.freq_none') },
  { value: 'DAILY', label: t('calendar.freq_daily') },
  { value: 'WEEKLY', label: t('calendar.freq_weekly') },
  { value: 'MONTHLY', label: t('calendar.freq_monthly') },
  { value: 'YEARLY', label: t('calendar.freq_yearly') },
])

const ALARM_OPTIONS = computed(() => [
  { value: '', label: t('calendar.alarm_none') },
  { value: '0', label: t('calendar.alarm_at_time') },
  { value: '5', label: t('calendar.alarm_5min') },
  { value: '15', label: t('calendar.alarm_15min') },
  { value: '30', label: t('calendar.alarm_30min') },
  { value: '60', label: t('calendar.alarm_1h') },
  { value: '1440', label: t('calendar.alarm_1d') },
])

// Arrays in vue-i18n v9 are fetched via tm()+rt(), not t() (t() would return the key for arrays).
const WEEKDAYS = computed(() => (tm('calendar.weekdays') as unknown[]).map((s) => rt(s as string)))
const MONTHS = computed(() => (tm('calendar.months') as unknown[]).map((s) => rt(s as string)))

// --- state ---
type View = 'month' | 'week' | 'day'
const view = ref<View>('month')
const cursor = ref<Date>(startOfDay(new Date()))
const events = ref<EventOccurrence[]>([])
const error = ref('')
const loading = ref(false)

// --- calendars state ---
const calendars = ref<CalendarMeta[]>([])
const hidden = ref<Set<string>>(new Set())
const HIDDEN_KEY = 'calendar_hidden'
const colorMenuFor = ref<string>('')

function loadHidden() {
  try {
    const raw = localStorage.getItem(HIDDEN_KEY)
    hidden.value = new Set(raw ? JSON.parse(raw) : [])
  } catch { hidden.value = new Set() }
}
function persistHidden() {
  try { localStorage.setItem(HIDDEN_KEY, JSON.stringify([...hidden.value])) } catch { /* ignore */ }
}
function toggleCalendar(id: string) {
  if (hidden.value.has(id)) hidden.value.delete(id)
  else hidden.value.add(id)
  hidden.value = new Set(hidden.value)
  persistHidden()
}
function calColor(id: string): string {
  const idx = calendars.value.findIndex((c) => c.id === id)
  if (idx < 0) return PALETTE[0]
  return calendars.value[idx].color || PALETTE[idx % PALETTE.length]
}
async function loadCalendars() {
  try {
    calendars.value = await request<CalendarMeta[]>('/me/calendars')
    if (!Array.isArray(calendars.value)) calendars.value = []
  } catch { calendars.value = [] }
}

// --- date helpers ---
function startOfDay(d: Date): Date { const r = new Date(d); r.setHours(0,0,0,0); return r }
function mondayOf(d: Date): Date {
  const r = startOfDay(d); const dow = r.getDay()
  const diff = (dow === 0 ? -6 : 1 - dow); r.setDate(r.getDate() + diff); return r
}
function addDays(d: Date, n: number): Date { const r = new Date(d); r.setDate(r.getDate() + n); return r }
function isSameDay(a: Date, b: Date): boolean {
  return a.getFullYear() === b.getFullYear() && a.getMonth() === b.getMonth() && a.getDate() === b.getDate()
}
function isToday(d: Date): boolean { return isSameDay(d, today.value) }
function isoToLocal(iso: string): string {
  if (!iso) return ''; const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth()+1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function localToIso(v: string): string { return v ? new Date(v).toISOString() : '' }
function isoToDate(iso: string): string {
  if (!iso) return ''; const d = new Date(iso)
  const pad = (n: number) => String(n).padStart(2, '0')
  return `${d.getUTCFullYear()}-${pad(d.getUTCMonth()+1)}-${pad(d.getUTCDate())}`
}
function dateToIso(v: string): string { return v ? `${v}T00:00:00.000Z` : '' }
function formatEventTime(iso: string): string {
  const d = new Date(iso); const pad = (n: number) => String(n).padStart(2, '0')
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`
}

// --- range ---
const range = computed<{ start: Date; end: Date }>(() => {
  const c = cursor.value
  if (view.value === 'month') {
    const first = new Date(c.getFullYear(), c.getMonth(), 1)
    const last = new Date(c.getFullYear(), c.getMonth() + 1, 0)
    const start = mondayOf(first)
    const lastMon = mondayOf(last)
    const end = addDays(lastMon, 7)
    return { start, end }
  } else if (view.value === 'week') {
    const start = mondayOf(c); const end = addDays(start, 7); return { start, end }
  } else {
    const start = startOfDay(c); const end = addDays(start, 1); return { start, end }
  }
})

// --- period title ---
const periodTitle = computed<string>(() => {
  const c = cursor.value
  const months = MONTHS.value
  if (view.value === 'month') {
    return `${months[c.getMonth()]} ${c.getFullYear()}`
  } else if (view.value === 'week') {
    const { start, end } = range.value; const endDay = addDays(end, -1)
    const pad = (n: number) => String(n).padStart(2, '0')
    const s = `${pad(start.getDate())}.${pad(start.getMonth()+1)}`
    const e = `${pad(endDay.getDate())}.${pad(endDay.getMonth()+1)}.${endDay.getFullYear()}`
    return `${s} – ${e}`
  } else {
    const pad = (n: number) => String(n).padStart(2, '0')
    return `${pad(c.getDate())}.${pad(c.getMonth()+1)}.${c.getFullYear()}`
  }
})

async function loadEvents() {
  error.value = ''; loading.value = true
  try {
    const { start, end } = range.value
    events.value = await request<EventOccurrence[]>(`/me/calendar/events?start=${start.toISOString()}&end=${end.toISOString()}`)
    if (!Array.isArray(events.value)) events.value = []
  } catch (e: any) { error.value = e?.data?.error || t('calendar.error_load'); events.value = [] }
  finally { loading.value = false }
}

loadHidden()
onMounted(loadCalendars)
watch([view, cursor], loadEvents, { immediate: true })

function goToday() { cursor.value = startOfDay(new Date()) }
function goPrev() {
  const c = cursor.value
  if (view.value === 'month') cursor.value = new Date(c.getFullYear(), c.getMonth()-1, 1)
  else if (view.value === 'week') cursor.value = addDays(c, -7)
  else cursor.value = addDays(c, -1)
}
function goNext() {
  const c = cursor.value
  if (view.value === 'month') cursor.value = new Date(c.getFullYear(), c.getMonth()+1, 1)
  else if (view.value === 'week') cursor.value = addDays(c, 7)
  else cursor.value = addDays(c, 1)
}

const monthCells = computed<Date[]>(() => {
  if (view.value !== 'month') return []
  const { start, end } = range.value; const cells: Date[] = []
  let d = new Date(start)
  while (d < end) { cells.push(new Date(d)); d = addDays(d, 1) }
  return cells
})
function eventsForDay(day: Date): EventOccurrence[] {
  return (events.value ?? []).filter((ev) => {
    if (hidden.value.has(ev.calendar_id)) return false
    return isSameDay(startOfDay(new Date(ev.start)), day)
  })
}
function isCurrentMonth(day: Date): boolean { return day.getMonth() === cursor.value.getMonth() }
const weekDays = computed<Date[]>(() => {
  if (view.value !== 'week') return []
  const { start } = range.value
  return Array.from({ length: 7 }, (_, i) => addDays(start, i))
})

// --- event modal ---
const modalOpen = ref(false)
const modalBusy = ref(false)
const form = reactive<EventDetail & { recurring: boolean; calendar_id: string }>({
  uid: '', summary: '', location: '', description: '', start: '', end: '',
  all_day: false, freq: '', until: '', alarm: '', recurring: false, calendar_id: '',
})
const formStartLocal = ref(''); const formEndLocal = ref('')
const formStartDate = ref(''); const formEndDate = ref(''); const formUntilDate = ref('')

function resetForm() {
  form.uid = ''; form.summary = ''; form.location = ''; form.description = ''
  form.start = ''; form.end = ''; form.all_day = false; form.freq = ''
  form.until = ''; form.alarm = ''; form.recurring = false; form.calendar_id = ''
  formStartLocal.value = ''; formEndLocal.value = ''
  formStartDate.value = ''; formEndDate.value = ''; formUntilDate.value = ''
}
function openNewEvent(day?: Date) {
  resetForm()
  const def = calendars.value.find((c) => c.is_default) || calendars.value[0]
  form.calendar_id = def ? def.id : ''
  if (day) {
    const base = new Date(day); base.setHours(10, 0, 0, 0)
    const baseEnd = new Date(day); baseEnd.setHours(11, 0, 0, 0)
    formStartLocal.value = isoToLocal(base.toISOString()); formEndLocal.value = isoToLocal(baseEnd.toISOString())
    formStartDate.value = isoToDate(base.toISOString()); formEndDate.value = isoToDate(baseEnd.toISOString())
  }
  modalOpen.value = true
}
async function openEvent(uid: string) {
  error.value = ''
  const occ = (events.value ?? []).find((e) => e.uid === uid)
  const calId = occ?.calendar_id || ''
  try {
    const d = await request<EventDetail>(`/me/calendar/events/${uid}?calendar_id=${encodeURIComponent(calId)}`)
    form.uid = d.uid; form.summary = d.summary; form.location = d.location || ''
    form.description = d.description || ''; form.start = d.start; form.end = d.end
    form.all_day = d.all_day; form.freq = d.freq || ''; form.until = d.until || ''
    form.alarm = d.alarm || ''; form.calendar_id = d.calendar_id || calId
    form.recurring = occ?.recurring ?? false
    formStartLocal.value = isoToLocal(d.start); formEndLocal.value = isoToLocal(d.end)
    formStartDate.value = isoToDate(d.start); formEndDate.value = isoToDate(d.end)
    formUntilDate.value = d.until ? isoToDate(d.until) : ''
    modalOpen.value = true
  } catch (e: any) { error.value = e?.data?.error || t('calendar.error_load') }
}
function buildBody() {
  const startIso = form.all_day ? dateToIso(formStartDate.value) : localToIso(formStartLocal.value)
  const endIso = form.all_day ? dateToIso(formEndDate.value) : localToIso(formEndLocal.value)
  const untilIso = form.freq && formUntilDate.value ? dateToIso(formUntilDate.value) : ''
  return { summary: form.summary, location: form.location, description: form.description,
    start: startIso, end: endIso, all_day: form.all_day, freq: form.freq, until: untilIso,
    alarm: form.alarm, calendar_id: form.calendar_id }
}
async function saveEvent() {
  if (!form.summary.trim()) { error.value = t('calendar.error_name_required'); return }
  error.value = ''; modalBusy.value = true
  try {
    if (form.uid) await request(`/me/calendar/events/${form.uid}`, { method: 'PUT', body: buildBody() })
    else await request('/me/calendar/events', { method: 'POST', body: buildBody() })
    modalOpen.value = false; await loadEvents()
  } catch (e: any) { error.value = e?.data?.error || t('calendar.error_save') }
  finally { modalBusy.value = false }
}
async function deleteEvent() {
  if (!form.uid) return
  if (!(await confirm(t('calendar.delete_event'), { message: form.summary || t('calendar.confirm_delete_event_msg'), confirmText: t('calendar.delete_event_btn'), danger: true }))) return
  error.value = ''
  try {
    await request(`/me/calendar/events/${form.uid}?calendar_id=${encodeURIComponent(form.calendar_id)}`, { method: 'DELETE' })
    modalOpen.value = false; await loadEvents()
  } catch (e: any) { error.value = e?.data?.error || t('calendar.error_delete') }
}

// --- calendar management ---
async function createCalendar() {
  const name = (await prompt(t('calendar.new_calendar'), '', { confirmText: t('common.create') }))?.trim()
  if (!name) return
  const color = PALETTE[calendars.value.length % PALETTE.length]
  try {
    await request('/me/calendars', { method: 'POST', body: { name, color } })
    await loadCalendars(); await loadEvents()
  } catch (e: any) { error.value = e?.data?.error || t('calendar.error_create_calendar') }
}
async function renameCalendar(c: CalendarMeta) {
  const name = (await prompt(t('calendar.rename_calendar'), c.name, { confirmText: t('calendar.rename_calendar_btn') }))?.trim()
  if (!name || name === c.name) return
  try { await request(`/me/calendars/${c.id}`, { method: 'PATCH', body: { name } }); await loadCalendars() }
  catch (e: any) { error.value = e?.data?.error || t('calendar.error_rename_calendar') }
}
async function setCalendarColor(c: CalendarMeta, color: string) {
  try { await request(`/me/calendars/${c.id}`, { method: 'PATCH', body: { color } }); await loadCalendars(); await loadEvents() }
  catch (e: any) { error.value = e?.data?.error || t('calendar.error_color_calendar') }
}
async function deleteCalendar(c: CalendarMeta) {
  if (!(await confirm(t('calendar.delete_calendar'), { message: t('calendar.delete_calendar_msg', { name: c.name }), confirmText: t('calendar.delete_calendar_btn'), danger: true }))) return
  try { await request(`/me/calendars/${c.id}`, { method: 'DELETE' }); await loadCalendars(); await loadEvents() }
  catch (e: any) { error.value = e?.data?.error || t('calendar.error_delete_calendar') }
}

useModalEscape(modalOpen, () => { modalOpen.value = false })

// --- calendar sharing ---
const share = reactive({ open: false, calId: '', name: '', email: '', list: [] as { id: string; email: string }[], busy: false })
const feed = reactive({ password: '', list: [] as { id: string; token: string; has_password: boolean }[], busy: false })

function feedUrl(token: string): string { return (import.meta.client ? location.origin : '') + '/cal/' + token + '.ics' }
function feedWebcal(token: string): string { const host = import.meta.client ? location.host : ''; return 'webcal://' + host + '/cal/' + token + '.ics' }
async function loadFeeds() {
  try { feed.list = await request<typeof feed.list>(`/me/calendars/${share.calId}/feed`) }
  catch { feed.list = [] }
}
async function createFeed() {
  feed.busy = true
  try { await request(`/me/calendars/${share.calId}/feed`, { method: 'POST', body: { password: feed.password } }); feed.password = ''; await loadFeeds() }
  catch (e: any) { error.value = e?.data?.error || t('calendar.error_feed_create') }
  finally { feed.busy = false }
}
async function revokeFeed(id: string) {
  try { await request(`/me/calendars/${share.calId}/feed/${id}`, { method: 'DELETE' }); await loadFeeds() }
  catch (e: any) { error.value = e?.data?.error || t('calendar.error_feed_revoke') }
}
async function copyText(text: string) { try { await navigator.clipboard.writeText(text) } catch { /* no access */ } }
async function loadShares() {
  try { share.list = await request<{ id: string; email: string }[]>(`/me/calendars/${share.calId}/shares`) }
  catch { share.list = [] }
}
function openShare(c: CalendarMeta) {
  Object.assign(share, { open: true, calId: c.id, name: c.name, email: '', list: [], busy: false })
  loadShares(); feed.password = ''; loadFeeds()
}
async function submitShare() {
  if (!share.email.trim()) return; share.busy = true
  try { await request(`/me/calendars/${share.calId}/share`, { method: 'POST', body: { email: share.email.trim() } }); share.email = ''; await loadShares() }
  catch (e: any) { error.value = e?.data?.error || t('calendar.error_share') }
  finally { share.busy = false }
}
async function revokeShare(id: string) {
  try { await request(`/me/calendars/${share.calId}/shares/${id}`, { method: 'DELETE' }); await loadShares() }
  catch (e: any) { error.value = e?.data?.error || t('calendar.error_revoke') }
}
useModalEscape(computed(() => share.open), () => { share.open = false })
</script>

<template>
  <div class="flex gap-4 h-[calc(100vh-4rem)]">
    <!-- sidebar -->
    <aside class="hidden w-56 shrink-0 overflow-auto md:block">
      <div class="mb-2 flex items-center justify-between">
        <span class="text-xs font-medium text-muted">{{ t('calendar.calendars') }}</span>
        <button class="btn-ghost px-1.5 py-1" :title="t('calendar.new_calendar')" @click="createCalendar">
          <Icon name="lucide:plus" size="16" />
        </button>
      </div>
      <ul class="space-y-1">
        <li v-for="c in calendars" :key="c.id" class="group flex items-center gap-2 rounded px-2 py-1.5 hover:bg-ink/5" @mouseleave="colorMenuFor = ''">
          <button class="flex min-w-0 flex-1 items-center gap-2" @click="toggleCalendar(c.id)">
            <span class="h-3 w-3 shrink-0 rounded-sm ring-1 ring-line" :style="{ backgroundColor: hidden.has(c.id) ? 'transparent' : calColor(c.id), borderColor: calColor(c.id) }" />
            <span class="truncate text-sm" :class="hidden.has(c.id) ? 'text-muted line-through' : 'text-ink'">{{ c.name }}</span>
            <Icon v-if="!c.is_owner" name="lucide:users" size="13" class="ml-0.5 shrink-0 text-accent"
              :title="c.owner_email ? `${t('calendar.shared_calendar')} · ${c.owner_email}` : t('calendar.shared_calendar')" />
          </button>
          <div class="relative shrink-0 opacity-0 group-hover:opacity-100">
            <button v-if="c.is_owner" class="btn-ghost px-1 py-0.5" @click="colorMenuFor = colorMenuFor === c.id ? '' : c.id">
              <Icon name="lucide:more-horizontal" size="14" />
            </button>
            <div v-if="colorMenuFor === c.id" class="absolute right-0 z-10 mt-1 w-40 rounded-lg border border-line bg-panel p-2 shadow-xl">
              <div v-if="c.is_owner" class="mb-2 flex flex-wrap gap-1">
                <button v-for="p in PALETTE" :key="p" class="h-5 w-5 rounded-sm ring-1 ring-line" :style="{ backgroundColor: p }" @click="setCalendarColor(c, p); colorMenuFor = ''" />
              </div>
              <button v-if="c.is_owner" class="btn-ghost w-full justify-start px-2 py-1 text-xs" @click="renameCalendar(c); colorMenuFor = ''">
                <Icon name="lucide:pencil" size="13" /> {{ t('calendar.color_rename') }}
              </button>
              <button v-if="c.is_owner" class="btn-ghost w-full justify-start px-2 py-1 text-xs text-danger" @click="deleteCalendar(c); colorMenuFor = ''">
                <Icon name="lucide:trash-2" size="13" /> {{ t('calendar.color_delete') }}
              </button>
              <button v-if="c.is_owner" class="btn-ghost w-full justify-start px-2 py-1 text-xs" @click="openShare(c); colorMenuFor = ''">
                <Icon name="lucide:share-2" size="13" /> {{ t('calendar.color_share') }}
              </button>
            </div>
          </div>
        </li>
      </ul>
    </aside>

    <!-- main column -->
    <div class="flex flex-1 flex-col min-h-0">
      <!-- header -->
      <div class="mb-4 flex flex-wrap items-center gap-3">
        <div class="flex rounded-lg border border-line overflow-hidden">
          <button v-for="v in (['month', 'week', 'day'] as const)" :key="v" class="px-3 py-1.5 text-sm transition"
            :class="view === v ? 'bg-accent/20 text-accent' : 'text-muted hover:bg-ink/5 hover:text-ink'"
            @click="view = v">
            {{ v === 'month' ? t('calendar.month') : v === 'week' ? t('calendar.week') : t('calendar.day') }}
          </button>
        </div>
        <div class="flex items-center gap-1">
          <button class="btn-ghost px-2 py-1.5" @click="goPrev"><Icon name="lucide:chevron-left" size="18" /></button>
          <button class="btn-ghost px-3 py-1.5 text-sm" @click="goToday">{{ t('calendar.today') }}</button>
          <button class="btn-ghost px-2 py-1.5" @click="goNext"><Icon name="lucide:chevron-right" size="18" /></button>
        </div>
        <span class="text-base font-semibold">{{ periodTitle }}</span>
        <div class="ml-auto">
          <button class="btn-accent" @click="openNewEvent()">
            <Icon name="lucide:plus" size="18" /> {{ t('calendar.new_event') }}
          </button>
        </div>
      </div>

      <p v-if="error" class="mb-3 flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
      </p>

      <!-- MONTH VIEW -->
      <div v-if="view === 'month'" class="flex-1 flex flex-col min-h-0">
        <div class="grid grid-cols-7 border-b border-line">
          <div v-for="wd in WEEKDAYS" :key="wd" class="py-1.5 text-center text-xs font-medium text-muted">{{ wd }}</div>
        </div>
        <div class="flex-1 grid grid-cols-7 auto-rows-fr min-h-0">
          <div v-for="day in monthCells" :key="day.toISOString()"
            class="border-b border-r border-line/50 p-1 min-h-0 cursor-pointer hover:bg-ink/[0.03] transition"
            @click="openNewEvent(day)">
            <div class="mb-0.5 flex h-6 w-6 items-center justify-center rounded-full text-xs font-medium"
              :class="[isToday(day) ? 'bg-accent text-white' : '', !isCurrentMonth(day) ? 'text-muted/40' : isToday(day) ? '' : 'text-ink']">
              {{ day.getDate() }}
            </div>
            <template v-for="(ev, idx) in eventsForDay(day).slice(0, 3)" :key="ev.uid + (ev.recurrence_id || '')">
              <div class="mb-0.5 truncate rounded px-1 text-[11px] leading-4 cursor-pointer transition"
                :style="{ backgroundColor: calColor(ev.calendar_id) + '33', color: calColor(ev.calendar_id) }"
                @click.stop="openEvent(ev.uid)">
                <span v-if="!ev.all_day" class="opacity-70">{{ formatEventTime(ev.start) }} </span>{{ ev.summary }}
              </div>
            </template>
            <div v-if="eventsForDay(day).length > 3" class="text-[10px] text-muted pl-1">+{{ eventsForDay(day).length - 3 }}</div>
          </div>
        </div>
      </div>

      <!-- WEEK VIEW -->
      <div v-else-if="view === 'week'" class="flex-1 overflow-auto">
        <div class="grid grid-cols-7 gap-2">
          <div v-for="day in weekDays" :key="day.toISOString()" class="card p-2 min-h-32">
            <div class="mb-1 flex items-center gap-1">
              <span class="text-xs text-muted">{{ WEEKDAYS[day.getDay() === 0 ? 6 : day.getDay() - 1] }}</span>
              <span class="flex h-5 w-5 items-center justify-center rounded-full text-xs font-semibold"
                :class="isToday(day) ? 'bg-accent text-white' : 'text-ink'">{{ day.getDate() }}</span>
            </div>
            <div v-for="ev in eventsForDay(day)" :key="ev.uid + (ev.recurrence_id || '')"
              class="mb-1 truncate rounded px-1.5 py-0.5 text-xs cursor-pointer transition"
              :style="{ backgroundColor: calColor(ev.calendar_id) + '33', color: calColor(ev.calendar_id) }"
              @click="openEvent(ev.uid)">
              <span v-if="!ev.all_day" class="opacity-70">{{ formatEventTime(ev.start) }} </span>{{ ev.summary }}
            </div>
            <div v-if="!eventsForDay(day).length" class="text-[10px] text-muted/40 mt-1 text-center cursor-pointer" @click="openNewEvent(day)">+</div>
          </div>
        </div>
      </div>

      <!-- DAY VIEW -->
      <div v-else class="flex-1 overflow-auto">
        <div class="card p-4">
          <div v-if="loading" class="text-sm text-muted">{{ t('common.loading') }}</div>
          <div v-else-if="!eventsForDay(cursor).length" class="py-10 text-center text-sm text-muted">
            <Icon name="lucide:calendar" size="28" class="mx-auto mb-2 opacity-40" />
            <p>{{ t('calendar.no_events') }}</p>
            <button class="btn-ghost mt-3 text-sm" @click="openNewEvent(cursor)">
              <Icon name="lucide:plus" size="16" /> {{ t('calendar.add_event') }}
            </button>
          </div>
          <div v-else class="space-y-2">
            <div v-for="ev in [...eventsForDay(cursor)].sort((a, b) => a.start.localeCompare(b.start))"
              :key="ev.uid + (ev.recurrence_id || '')"
              class="flex items-start gap-3 rounded-lg border border-line/50 px-4 py-3 cursor-pointer hover:bg-ink/5 transition"
              @click="openEvent(ev.uid)">
              <span class="mt-1 h-2.5 w-2.5 shrink-0 rounded-full" :style="{ backgroundColor: calColor(ev.calendar_id) }" />
              <div class="mt-0.5 shrink-0 text-sm text-muted font-mono">
                <span v-if="ev.all_day">{{ t('calendar.all_day') }}</span>
                <span v-else>{{ formatEventTime(ev.start) }}</span>
              </div>
              <div class="min-w-0">
                <div class="font-medium text-sm">{{ ev.summary }}</div>
                <div v-if="ev.location" class="text-xs text-muted mt-0.5">
                  <Icon name="lucide:map-pin" size="12" class="inline" /> {{ ev.location }}
                </div>
              </div>
              <Icon v-if="ev.recurring" name="lucide:repeat" size="14" class="ml-auto shrink-0 text-muted mt-1" />
            </div>
          </div>
        </div>
      </div>

      <!-- SHARE MODAL -->
      <div v-if="share.open" class="fixed inset-0 z-20 flex items-center justify-center bg-black/50 p-4" @click.self="share.open = false">
        <div class="card w-full max-w-md p-5">
          <div class="mb-4 flex items-center justify-between">
            <h2 class="font-semibold">{{ t('calendar.share_calendar', { name: share.name }) }}</h2>
            <button class="btn-ghost px-1.5 py-1" @click="share.open = false"><Icon name="lucide:x" size="18" /></button>
          </div>
          <div class="mb-3 flex gap-2">
            <input v-model="share.email" type="email" class="input flex-1" :placeholder="t('calendar.share_email_ph')" @keyup.enter="submitShare" />
            <button class="btn-accent shrink-0" :disabled="share.busy" @click="submitShare">
              <Icon name="lucide:user-plus" size="16" /> {{ t('calendar.share_btn_access') }}
            </button>
          </div>
          <p class="mb-2 text-xs text-muted">{{ t('calendar.share_full_access') }}</p>
          <ul v-if="share.list.length" class="space-y-1">
            <li v-for="sh in share.list" :key="sh.id" class="flex items-center justify-between rounded px-2 py-1.5 hover:bg-ink/5">
              <span class="truncate text-sm">{{ sh.email }}</span>
              <button class="btn-ghost px-2 py-1 text-danger" @click="revokeShare(sh.id)"><Icon name="lucide:x" size="14" /></button>
            </li>
          </ul>
          <p v-else class="text-sm text-muted">{{ t('calendar.share_nobody') }}</p>
          <div class="mt-4 border-t border-line/50 pt-3">
            <h3 class="mb-2 text-sm font-medium text-muted">{{ t('calendar.feed_title') }}</h3>
            <div class="mb-2 flex gap-2">
              <input v-model="feed.password" type="text" class="input flex-1" :placeholder="t('calendar.feed_password_ph')" />
              <button class="btn-accent shrink-0" :disabled="feed.busy" @click="createFeed">
                <Icon name="lucide:link" size="16" /> {{ t('calendar.feed_create') }}
              </button>
            </div>
            <ul v-if="feed.list.length" class="space-y-2">
              <li v-for="f in feed.list" :key="f.id" class="rounded border border-line/50 p-2">
                <div class="mb-1 flex items-center gap-2">
                  <Icon v-if="f.has_password" name="lucide:lock" size="12" class="shrink-0 text-muted" />
                  <code class="flex-1 truncate text-xs">{{ feedUrl(f.token) }}</code>
                  <button class="btn-ghost px-1.5 py-0.5" :title="t('calendar.feed_copy_https')" @click="copyText(feedUrl(f.token))"><Icon name="lucide:copy" size="13" /></button>
                  <button class="btn-ghost px-1.5 py-0.5" :title="t('calendar.feed_copy_webcal')" @click="copyText(feedWebcal(f.token))"><Icon name="lucide:calendar-plus" size="13" /></button>
                  <button class="btn-ghost px-1.5 py-0.5 text-danger" :title="t('calendar.feed_revoke')" @click="revokeFeed(f.id)"><Icon name="lucide:x" size="13" /></button>
                </div>
              </li>
            </ul>
            <p v-else class="text-xs text-muted">{{ t('calendar.feed_empty') }}</p>
          </div>
        </div>
      </div>

      <!-- EVENT MODAL -->
      <div v-if="modalOpen" class="fixed inset-0 z-20 flex items-center justify-center bg-black/50 p-4" @click.self="modalOpen = false">
        <div class="card w-full max-w-lg p-5 max-h-[90vh] overflow-auto">
          <div class="mb-4 flex items-center justify-between">
            <h2 class="font-semibold">{{ form.uid ? t('calendar.edit_event') : t('calendar.new_event_form') }}</h2>
            <button class="btn-ghost px-1.5 py-1" @click="modalOpen = false"><Icon name="lucide:x" size="18" /></button>
          </div>

          <div v-if="form.uid && form.recurring" class="mb-3 flex items-center gap-2 rounded-md bg-accent/10 px-3 py-2 text-xs text-accent">
            <Icon name="lucide:repeat" size="14" />
            {{ t('calendar.recurring_notice') }}
          </div>

          <div class="mb-3">
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('calendar.field_name_req') }}</label>
            <input v-model="form.summary" class="input" :placeholder="t('calendar.field_name_ph')" />
          </div>
          <div class="mb-3">
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('calendar.field_calendar') }}</label>
            <select v-model="form.calendar_id" class="input">
              <option v-for="c in calendars" :key="c.id" :value="c.id">{{ c.name }}</option>
            </select>
          </div>
          <div class="mb-3">
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('calendar.field_location') }}</label>
            <input v-model="form.location" class="input" :placeholder="t('calendar.field_location_ph')" />
          </div>
          <div class="mb-3">
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('calendar.field_description') }}</label>
            <textarea v-model="form.description" class="input min-h-32 resize-y" :placeholder="t('calendar.field_description_ph')" />
          </div>
          <div class="mb-3 flex items-center gap-2">
            <input id="allDay" v-model="form.all_day" type="checkbox" class="h-4 w-4 accent-accent" />
            <label for="allDay" class="text-sm cursor-pointer">{{ t('calendar.all_day') }}</label>
          </div>
          <div class="mb-3 flex gap-3">
            <div class="flex-1">
              <label class="mb-1 block text-xs font-medium text-muted">{{ t('calendar.field_start') }}</label>
              <input v-if="form.all_day" v-model="formStartDate" type="date" class="input" />
              <input v-else v-model="formStartLocal" type="datetime-local" class="input" />
            </div>
            <div class="flex-1">
              <label class="mb-1 block text-xs font-medium text-muted">{{ t('calendar.field_end') }}</label>
              <input v-if="form.all_day" v-model="formEndDate" type="date" class="input" />
              <input v-else v-model="formEndLocal" type="datetime-local" class="input" />
            </div>
          </div>
          <div class="mb-3">
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('calendar.field_recurrence') }}</label>
            <select v-model="form.freq" class="input">
              <option v-for="opt in FREQ_OPTIONS" :key="opt.value" :value="opt.value">{{ opt.label }}</option>
            </select>
          </div>
          <div v-if="form.freq" class="mb-3">
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('calendar.field_until') }}</label>
            <input v-model="formUntilDate" type="date" class="input" />
          </div>
          <div class="mb-3">
            <label class="mb-1 block text-xs font-medium text-muted">{{ t('calendar.field_alarm') }}</label>
            <select v-model="form.alarm" class="input">
              <option v-if="form.alarm === 'keep'" value="keep">{{ t('calendar.alarm_keep') }}</option>
              <option v-for="a in ALARM_OPTIONS" :key="a.value" :value="a.value">{{ a.label }}</option>
            </select>
          </div>
          <div class="mt-5 flex items-center justify-between gap-2">
            <button v-if="form.uid" class="btn-danger" :disabled="modalBusy" @click="deleteEvent">
              <Icon name="lucide:trash-2" size="16" /> {{ t('calendar.btn_delete') }}
            </button>
            <span v-else />
            <button class="btn-accent" :disabled="modalBusy" @click="saveEvent">
              <Icon v-if="modalBusy" name="lucide:loader-circle" class="animate-spin" size="16" />
              <Icon v-else name="lucide:check" size="16" />
              {{ form.uid ? t('calendar.btn_save') : t('calendar.btn_create') }}
            </button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
