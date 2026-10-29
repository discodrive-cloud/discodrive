<script setup lang="ts">
// «Общие»: the galleries other people opened to the user (open one, mix it into the feed,
// walk away) and the people the user opened theirs to.
import type { PhotoPartners } from '~/composables/usePhotos'

const emit = defineEmits<{ open: [userId: string, email: string]; feed: [] }>()
const { t } = useI18n()
const api = usePhotosApi()
const { confirm } = useDialog()

const partners = ref<PhotoPartners | null>(null)
const email = ref('')
const error = ref('')
const busy = ref(false)

async function load() {
  try {
    partners.value = await api.partners()
    error.value = ''
  } catch (e: any) {
    error.value = e?.data?.error || t('common.error_load')
    partners.value ??= { sharing: [], sharedWithMe: [] }
  }
}
onMounted(load)

async function run(fn: () => Promise<void>) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try { await fn() } catch (e: any) {
    error.value = e?.response?.status === 404 && email.value ? t('photos.user_not_found') : (e?.data?.error || t('photos.action_failed'))
  } finally { busy.value = false }
}

function share() {
  const v = email.value.trim()
  if (!v) return
  return run(async () => {
    await api.addPartner(v)
    email.value = ''
    await load()
  })
}

async function revoke(userId: string, who: string) {
  const ok = await confirm(t('photos.partner_revoke_title', { email: who }), { message: t('photos.partner_revoke_message'), confirmText: t('photos.partner_revoke'), danger: true })
  if (!ok) return
  return run(async () => { await api.revokePartner(userId); await load() })
}

async function leave(userId: string, who: string) {
  const ok = await confirm(t('photos.partner_leave_title', { email: who }), { confirmText: t('photos.partner_leave'), danger: true })
  if (!ok) return
  return run(async () => { await api.leavePartner(userId); await load(); emit('feed') })
}

function setFeed(userId: string, inFeed: boolean) {
  return run(async () => {
    await api.setPartnerFeed(userId, inFeed)
    const p = partners.value?.sharedWithMe.find((x) => x.userId === userId)
    if (p) p.inFeed = inFeed
    emit('feed')
  })
}
</script>

<template>
  <div class="min-h-0 flex-1 overflow-y-auto px-4 py-3 md:px-6">
    <p v-if="!partners" class="text-sm text-muted">{{ t('common.loading') }}</p>
    <div v-else class="grid max-w-3xl gap-6">
      <section>
        <h2 class="mb-1 font-medium">{{ t('photos.shared_with_me') }}</h2>
        <p v-if="!partners.sharedWithMe.length" class="text-sm text-muted">{{ t('photos.shared_with_me_none') }}</p>
        <ul v-else class="space-y-2">
          <li v-for="p in partners.sharedWithMe" :key="p.userId" class="card flex flex-wrap items-center gap-x-4 gap-y-2 p-3 text-sm">
            <button type="button" class="flex min-w-0 flex-1 items-center gap-2 text-left font-medium hover:text-accent" @click="emit('open', p.userId, p.email)">
              <Icon name="lucide:images" size="18" class="shrink-0 text-muted" />
              <span class="truncate">{{ p.email }}</span>
            </button>
            <label class="flex items-center gap-2 text-xs">
              <input type="checkbox" :checked="p.inFeed" :disabled="busy" @change="setFeed(p.userId, ($event.target as HTMLInputElement).checked)" />
              {{ t('photos.partner_in_feed') }}
            </label>
            <button type="button" class="btn-ghost px-2 py-1 text-xs" :disabled="busy" @click="leave(p.userId, p.email)">{{ t('photos.partner_leave') }}</button>
          </li>
        </ul>
      </section>

      <section>
        <h2 class="mb-1 font-medium">{{ t('photos.sharing_mine') }}</h2>
        <p class="mb-2 text-xs text-muted">{{ t('photos.sharing_mine_hint') }}</p>
        <ul v-if="partners.sharing.length" class="mb-2 space-y-1">
          <li v-for="p in partners.sharing" :key="p.userId" class="flex items-center justify-between gap-2 text-sm">
            <span class="min-w-0 truncate">{{ p.email }}</span>
            <button type="button" class="btn-ghost px-2 py-1 text-xs" :disabled="busy" @click="revoke(p.userId, p.email)">{{ t('photos.partner_revoke') }}</button>
          </li>
        </ul>
        <form class="flex max-w-md gap-2" @submit.prevent="share">
          <input v-model="email" type="email" class="input flex-1" :placeholder="t('photos.share_email')" :aria-label="t('photos.share_email')" />
          <button type="submit" class="btn-accent" :disabled="busy || !email.trim()">{{ t('photos.partner_share') }}</button>
        </form>
      </section>
      <p v-if="error" class="text-sm text-danger">{{ error }}</p>
    </div>
  </div>
</template>
