<script setup lang="ts">
// Sharing an album (its owner): people of this server who see it and may add to it, and
// links that open it to anyone — with a password, an expiry and downloads if wanted.
import type { PhotoAlbumDetail, PhotoAlbumLink } from '~/composables/usePhotos'

const props = defineProps<{ album: PhotoAlbumDetail }>()
const emit = defineEmits<{ close: []; changed: [] }>()
const { t, locale } = useI18n()
const api = usePhotosApi()
const { confirm } = useDialog()

const members = ref([...props.album.members])
const links = ref<PhotoAlbumLink[] | null>(null)
const email = ref('')
const error = ref('')
const busy = ref(false)
const copied = ref('')

// A new link.
const withPassword = ref(false)
const password = ref('')
const expires = ref('') // yyyy-mm-dd, empty = never
const allowDownload = ref(false)

onMounted(async () => {
  try { links.value = await api.albumLinks(props.album.id) } catch (e: any) {
    links.value = []
    error.value = e?.data?.error || t('common.error_load')
  }
})

async function run(fn: () => Promise<void>) {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try { await fn() } catch (e: any) {
    const status = e?.response?.status
    error.value = status === 404 && email.value ? t('photos.user_not_found') : (e?.data?.error || t('photos.action_failed'))
  } finally { busy.value = false }
}

function addMember() {
  const v = email.value.trim()
  if (!v) return
  return run(async () => {
    const m = await api.addAlbumMember(props.album.id, v)
    if (!members.value.some((x) => x.userId === m.userId)) members.value.push(m)
    email.value = ''
    emit('changed')
  })
}

function removeMember(userId: string) {
  return run(async () => {
    await api.removeAlbumMember(props.album.id, userId)
    members.value = members.value.filter((m) => m.userId !== userId)
    emit('changed')
  })
}

function linkURL(l: PhotoAlbumLink) {
  return `${location.origin}${l.url}`
}

function createLink() {
  if (withPassword.value && !password.value) return
  return run(async () => {
    const body: { password?: string; expiresAt?: string; allowDownload: boolean } = { allowDownload: allowDownload.value }
    if (withPassword.value) body.password = password.value
    // The end of the chosen day, where the owner is.
    if (expires.value) body.expiresAt = new Date(`${expires.value}T23:59:59`).toISOString()
    const l = await api.createAlbumLink(props.album.id, body)
    links.value = [...(links.value ?? []), l]
    password.value = ''
    withPassword.value = false
    expires.value = ''
    allowDownload.value = false
    await copy(l)
  })
}

async function copy(l: PhotoAlbumLink) {
  try {
    await navigator.clipboard.writeText(linkURL(l))
    copied.value = l.id
    setTimeout(() => { if (copied.value === l.id) copied.value = '' }, 2000)
  } catch { /* the address stays on screen to copy by hand */ }
}

async function deleteLink(l: PhotoAlbumLink) {
  const ok = await confirm(t('photos.link_delete_title'), { message: t('photos.link_delete_message'), confirmText: t('common.delete'), danger: true })
  if (!ok) return
  return run(async () => {
    await api.deleteAlbumLink(props.album.id, l.id)
    links.value = (links.value ?? []).filter((x) => x.id !== l.id)
  })
}

function dateLabel(iso: string) {
  try { return new Intl.DateTimeFormat(locale.value, { day: 'numeric', month: 'long', year: 'numeric' }).format(new Date(iso)) } catch { return iso.slice(0, 10) }
}
const today = new Date(Date.now() + 86400000).toISOString().slice(0, 10)
</script>

<template>
  <div class="fixed inset-0 z-[60] flex items-center justify-center bg-black/50 p-4" @click.self="emit('close')">
    <div class="card flex max-h-[90vh] w-full max-w-lg flex-col overflow-y-auto p-5" role="dialog" aria-modal="true" aria-labelledby="album-share-title">
      <h2 id="album-share-title" class="mb-4 font-semibold">{{ t('photos.album_share_title', { name: album.name }) }}</h2>

      <!-- people -->
      <section class="mb-5">
        <h3 class="mb-1 text-sm font-medium">{{ t('photos.album_members') }}</h3>
        <p class="mb-2 text-xs text-muted">{{ t('photos.album_members_hint') }}</p>
        <ul v-if="members.length" class="mb-2 space-y-1">
          <li v-for="m in members" :key="m.userId" class="flex items-center justify-between gap-2 text-sm">
            <span class="min-w-0 truncate">{{ m.email }}</span>
            <button type="button" class="btn-ghost px-2 py-1 text-xs" :disabled="busy" @click="removeMember(m.userId)">{{ t('photos.remove') }}</button>
          </li>
        </ul>
        <form class="flex gap-2" @submit.prevent="addMember">
          <input v-model="email" type="email" class="input flex-1" :placeholder="t('photos.share_email')" :aria-label="t('photos.share_email')" />
          <button type="submit" class="btn-accent" :disabled="busy || !email.trim()">{{ t('photos.add') }}</button>
        </form>
      </section>

      <!-- links -->
      <section>
        <h3 class="mb-1 text-sm font-medium">{{ t('photos.album_links') }}</h3>
        <p class="mb-2 text-xs text-muted">{{ t('photos.album_links_hint') }}</p>
        <p v-if="!links" class="text-sm text-muted">{{ t('common.loading') }}</p>
        <ul v-else-if="links.length" class="mb-3 space-y-2">
          <li v-for="l in links" :key="l.id" class="rounded-md border border-line p-2 text-sm">
            <div class="flex items-center gap-2">
              <input :value="linkURL(l)" readonly class="input min-w-0 flex-1 py-1 text-xs" :aria-label="t('photos.album_link')" @focus="($event.target as HTMLInputElement).select()" />
              <button type="button" class="btn-ghost px-2 py-1 text-xs" @click="copy(l)">{{ copied === l.id ? t('photos.copied') : t('photos.copy') }}</button>
              <button type="button" class="btn-ghost px-2 py-1 text-xs hover:!text-danger" :disabled="busy" @click="deleteLink(l)">{{ t('common.delete') }}</button>
            </div>
            <div class="mt-1 flex flex-wrap gap-x-3 text-xs text-muted">
              <span v-if="l.hasPassword"><Icon name="lucide:lock" size="11" /> {{ t('photos.link_password') }}</span>
              <span>{{ l.expiresAt ? t('photos.link_until', { date: dateLabel(l.expiresAt) }) : t('photos.link_forever') }}</span>
              <span>{{ l.allowDownload ? t('photos.link_download_on') : t('photos.link_download_off') }}</span>
            </div>
          </li>
        </ul>

        <form class="space-y-2 rounded-md bg-ink/5 p-3 text-sm" @submit.prevent="createLink">
          <label class="flex items-center gap-2">
            <input v-model="withPassword" type="checkbox" /> {{ t('photos.link_with_password') }}
          </label>
          <input v-if="withPassword" v-model="password" type="password" autocomplete="new-password" class="input" :placeholder="t('photos.link_password')" :aria-label="t('photos.link_password')" />
          <label class="flex flex-wrap items-center gap-2">
            {{ t('photos.link_expires') }}
            <input v-model="expires" type="date" :min="today" class="input w-auto py-1" />
          </label>
          <label class="flex items-center gap-2">
            <input v-model="allowDownload" type="checkbox" /> {{ t('photos.link_allow_download') }}
          </label>
          <button type="submit" class="btn-accent" :disabled="busy || (withPassword && !password)">
            <Icon name="lucide:link" size="16" /> {{ t('photos.link_create') }}
          </button>
        </form>
      </section>

      <p v-if="error" class="mt-3 text-sm text-danger">{{ error }}</p>
      <div class="mt-4 flex justify-end">
        <button type="button" class="btn-ghost" @click="emit('close')">{{ t('common.close') }}</button>
      </div>
    </div>
  </div>
</template>
