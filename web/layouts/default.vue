<script setup lang="ts">
const sess = useSession()
const route = useRoute()
const { theme, toggle: toggleTheme } = useTheme()
const { t } = useI18n()
const { request } = useApi()
const { confirm } = useDialog()

const musicEnabled = ref(false)
const booksEnabled = ref(false)
onMounted(async () => {
  try { musicEnabled.value = (await request<{ enabled: boolean }>('/me/music')).enabled } catch { /* ignore */ }
  try { booksEnabled.value = (await request<{ enabled: boolean }>('/me/ebooks')).enabled } catch { /* ignore */ }
})

const nav = computed(() => {
  if (sess.value.role === 'admin') {
    return [
      { to: '/admin', icon: 'lucide:layout-dashboard', label: t('nav.dashboard') },
      { to: '/admin/users', icon: 'lucide:users', label: t('nav.users') },
      { to: '/admin/settings', icon: 'lucide:settings', label: t('nav.settings') },
    ]
  }
  const items = [
    { to: '/files', icon: 'lucide:folder', label: t('nav.files') },
    { to: '/trash', icon: 'lucide:trash-2', label: t('nav.trash') },
    { to: '/shared', icon: 'lucide:share-2', label: t('nav.shared') },
    { to: '/calendar', icon: 'lucide:calendar', label: t('nav.calendar') },
    { to: '/tasks', icon: 'lucide:list-checks', label: t('nav.tasks') },
    { to: '/contacts', icon: 'lucide:contact', label: t('nav.contacts') },
    { to: '/saved', icon: 'lucide:bookmark', label: t('nav.saved') },
  ]
  if (booksEnabled.value) {
    items.push({ to: '/books-edit', icon: 'lucide:book-open', label: t('nav.books') })
  }
  if (musicEnabled.value) {
    items.push({ to: '/music', icon: 'lucide:music', label: t('nav.music') })
  }
  items.push({ to: '/settings', icon: 'lucide:settings', label: t('nav.settings') })
  return items
})

function isActive(to: string) {
  return to === '/admin' || to === '/files' ? route.path === to : route.path.startsWith(to)
}

// On mobile the sidebar is a sliding drawer; close it on route change.
const sidebarOpen = ref(false)
watch(() => route.path, () => { sidebarOpen.value = false })

const logoutBusy = ref(false)
const logoutError = ref('')
async function logout() {
  if (logoutBusy.value) return
  if (!(await confirm(t('common.logout_confirm'), { confirmText: t('common.logout'), danger: true }))) return
  logoutBusy.value = true
  logoutError.value = ''
  try {
    if (await logoutSession()) await navigateTo('/login')
  } catch {
    logoutError.value = t('common.logout_error')
  } finally { logoutBusy.value = false }
}
</script>

<template>
  <div class="flex h-full">
    <!-- Backdrop behind the drawer (mobile only) -->
    <div
      v-if="sidebarOpen"
      class="fixed inset-0 z-30 bg-black/40 md:hidden"
      @click="sidebarOpen = false"
    />

    <aside
      :class="[
        'fixed inset-y-0 left-0 z-40 flex w-64 shrink-0 flex-col border-r border-line bg-panel backdrop-blur transition-transform md:static md:z-0 md:w-60 md:translate-x-0 md:bg-panel/60',
        sidebarOpen ? 'translate-x-0' : '-translate-x-full',
      ]"
    >
      <div class="flex items-center gap-2 px-5 py-5">
        <Icon name="lucide:hard-drive" class="text-accent" size="22" />
        <span class="font-mono text-lg font-semibold tracking-tight">Disco<span class="text-accent">Drive</span></span>
        <button class="btn-ghost ml-auto px-2 py-1 md:hidden" @click="sidebarOpen = false">
          <Icon name="lucide:x" size="20" />
        </button>
      </div>
      <nav class="flex-1 space-y-1 overflow-auto px-3">
        <NuxtLink
          v-for="item in nav"
          :key="item.to"
          :to="item.to"
          :class="[
            'flex items-center gap-3 rounded-lg px-3 py-2 text-sm transition',
            isActive(item.to) ? 'bg-accent/15 text-accent ring-1 ring-accent/20' : 'text-muted hover:bg-ink/5 hover:text-ink',
          ]"
          @click="sidebarOpen = false"
        >
          <Icon :name="item.icon" size="18" />
          <span>{{ item.label }}</span>
        </NuxtLink>
      </nav>
      <div class="border-t border-line p-3">
        <StorageMeter />
        <div class="mb-2 px-2 text-xs text-muted">
          <div class="truncate text-ink">{{ sess.email }}</div>
        </div>
        <button class="btn-ghost w-full justify-start" @click="toggleTheme">
          <Icon :name="theme === 'light' ? 'lucide:moon' : 'lucide:sun'" size="18" />
          {{ theme === 'light' ? t('common.dark_theme') : t('common.light_theme') }}
        </button>
        <button class="btn-ghost w-full justify-start" @click="logout" :disabled="logoutBusy">
          <Icon name="lucide:log-out" size="18" /> {{ t('common.logout') }}
        </button>
        <p v-if="logoutError" class="px-2 text-xs text-danger" role="alert">{{ logoutError }}</p>
      </div>
    </aside>

    <div class="flex min-w-0 flex-1 flex-col">
      <!-- Top bar with hamburger button (mobile only) -->
      <header class="flex items-center gap-2 border-b border-line bg-panel/60 px-3 py-2.5 backdrop-blur md:hidden">
        <button class="btn-ghost px-2 py-2" @click="sidebarOpen = true">
          <Icon name="lucide:menu" size="22" />
        </button>
        <Icon name="lucide:hard-drive" class="text-accent" size="20" />
        <span class="font-mono text-base font-semibold tracking-tight">Disco<span class="text-accent">Drive</span></span>
      </header>
      <main class="flex-1 overflow-auto">
        <div class="mx-auto max-w-6xl p-4 md:p-6">
          <slot />
        </div>
      </main>
      <PlayerLayer />
    </div>
  </div>
</template>
