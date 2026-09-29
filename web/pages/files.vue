<script setup lang="ts">
import { naturalCompare } from '~/lib/naturalSort'
import type { PreviewItem } from '~/lib/preview/types'

interface Node { id: string; name: string; is_dir: boolean; size: number | null; version: number; mime?: string }

const { t } = useI18n()

// --- player ---
const player = usePlayer()
// Playable = the stream endpoint will serve it AND this browser can decode it.
// Non-decodable media stays a plain file row (download still works).
function isPlayable(n: Node): boolean {
  return !n.is_dir && !!n.mime && browserCanPlay(n.mime)
}
async function play(n: Node) {
  try {
    await player.playFolder(parentId.value, n.id)
  } catch (e: any) {
    error.value = e?.data?.error || t('files.error_load')
  }
}

// --- file preview ---
// Gallery = every non-playable file of the folder in listing order; playable
// media opens the player instead. What actually renders (or shows a "no
// preview" placeholder) is PreviewModal's concern.
const preview = reactive({ open: false, index: 0 })
const previewables = computed(() => sorted.value.filter((n) => !n.is_dir && !isPlayable(n)))
const previewItems = computed<PreviewItem[]>(() =>
  previewables.value.map((n) => ({
    name: n.name,
    size: n.size,
    load: () => request<Blob>(`/files/${n.id}/content`, { responseType: 'blob' }),
  })),
)
function openPreview(n: Node) {
  const i = previewables.value.findIndex((p) => p.id === n.id)
  if (i < 0) return
  preview.index = i
  preview.open = true
}

// --- Vault browser ---
const { isVaultListing } = useVault()
const vaultOpen = ref(false)
const currentFolderNode = computed<Node>(() => {
  const top = stack.value[stack.value.length - 1]
  return { id: top.id, name: top.name, is_dir: true, size: null, version: 0 }
})

const { request } = useApi()
const { confirm, prompt } = useDialog()
const { enqueue, enqueueFolder, enqueueEntries } = useUploads()
const dragActive = ref(false)
const dragDepth = ref(0)
function onDragEnter() { dragDepth.value++; dragActive.value = true }
function onDragLeave() {
  dragDepth.value = Math.max(0, dragDepth.value - 1)
  if (dragDepth.value === 0) dragActive.value = false
}

async function onDrop(e: DragEvent) {
  dragDepth.value = 0
  dragActive.value = false
  const items = e.dataTransfer?.items
  if (items?.length) {
    const entries: FileSystemEntry[] = []
    for (const it of Array.from(items)) {
      const ent = it.webkitGetAsEntry?.()
      if (ent) entries.push(ent)
    }
    if (entries.length) {
      // Walking a dropped folder creates server folders on the way; any failure there
      // (name clash with a file, quota, lost session) must reach the user.
      try {
        await enqueueEntries(entries, parentId.value || null)
      } catch (err: any) {
        await uploadFailed(err)
      }
      return
    }
  }
  const files = e.dataTransfer?.files
  if (files?.length) enqueue(Array.from(files).map((f) => ({ file: f, parentId: parentId.value || null })))
}

const folderInput = ref<HTMLInputElement>()
const uploadsTick = useUploadsTick()
watch(uploadsTick, () => load())
const nodes = ref<Node[]>([])
const error = ref('')
const busy = ref(false)
const stack = ref<{ id: string; name: string }[]>([{ id: '', name: t('files.root_name') }])
const parentId = computed(() => stack.value[stack.value.length - 1].id)
const fileInput = ref<HTMLInputElement>()
const newFolder = ref('')

// Natural sort ("2" before "10") — MUST match the player queue's comparator:
// the queue is a snapshot of this listing, and click-to-play order relies on
// the two lists agreeing (see lib/naturalSort.ts).
const sorted = computed(() =>
  [...nodes.value].sort((a, b) => Number(b.is_dir) - Number(a.is_dir) || naturalCompare(a.name, b.name)),
)

async function load() {
  error.value = ''
  busy.value = true
  try {
    const q = parentId.value ? `?parent_id=${parentId.value}` : ''
    nodes.value = await request<Node[]>(`/files${q}`)
  } catch (e: any) {
    error.value = e?.data?.error || t('files.error_load')
  } finally {
    busy.value = false
  }
}
onMounted(load)

function open(n: Node) {
  if (!n.is_dir) return
  stack.value.push({ id: n.id, name: n.name })
  load()
}
function crumbTo(i: number) { stack.value = stack.value.slice(0, i + 1); load() }

async function createFolder() {
  const name = newFolder.value.trim()
  if (!name) return
  error.value = ''
  try {
    await request('/files/folder', { method: 'POST', body: { parent_id: parentId.value || null, name } })
    newFolder.value = ''
    await load()
  } catch (e: any) {
    error.value = e?.data?.error || t('files.error_create_folder')
  }
}

function onUpload(e: Event) {
  const files = (e.target as HTMLInputElement).files
  if (!files?.length) return
  enqueue(Array.from(files).map((f) => ({ file: f, parentId: parentId.value || null })))
  if (fileInput.value) fileInput.value.value = ''
}
async function onUploadFolder(e: Event) {
  const files = (e.target as HTMLInputElement).files
  if (!files?.length) return
  try {
    await enqueueFolder(Array.from(files), parentId.value || null)
  } catch (err: any) {
    await uploadFailed(err)
  } finally {
    if (folderInput.value) folderInput.value.value = ''
  }
}
async function uploadFailed(err: any) {
  await load() // show whatever folders were created before the failure (load resets error)
  error.value = err?.data?.error || err?.message || t('files.error_upload')
}

async function download(n: Node) {
  try {
    const blob = await request<Blob>(`/files/${n.id}/content`, { responseType: 'blob' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url; a.download = n.name; a.click()
    URL.revokeObjectURL(url)
  } catch (e: any) { error.value = e?.data?.error || t('files.error_download') }
}

async function rename(n: Node) {
  const name = (await prompt(t('files.btn_rename'), n.name, { confirmText: t('files.btn_rename') }))?.trim()
  if (!name || name === n.name) return
  error.value = ''
  try {
    await request(`/files/${n.id}/rename`, { method: 'PATCH', body: { name } })
    await load()
  } catch (e: any) { error.value = e?.data?.error || t('files.error_rename') }
}

async function remove(n: Node) {
  if (!(await confirm(t('common.delete') + '?', { message: `«${n.name}»${n.is_dir ? ` ${t('files.and_contents')}` : ''}`, confirmText: t('common.delete'), danger: true }))) return
  error.value = ''
  try {
    await request(`/files/${n.id}`, { method: 'DELETE' })
    await load()
  } catch (e: any) { error.value = e?.data?.error || t('files.error_delete') }
}

// --- version history ---
interface Version { version: number; size: number | null; content_hash: string; is_conflict_loser: boolean }
const versions = reactive({ open: false, node: null as Node | null, list: [] as Version[], busy: false })

async function openVersions(n: Node) {
  Object.assign(versions, { open: true, node: n, list: [], busy: true })
  try {
    versions.list = await request<Version[]>(`/files/${n.id}/versions`)
  } catch (e: any) {
    error.value = e?.data?.error || t('files.error_versions')
    versions.open = false
  } finally { versions.busy = false }
}

async function rollback(v: Version) {
  if (!versions.node) return
  error.value = ''
  try {
    await request(`/files/${versions.node.id}/restore`, { method: 'POST', body: { version: v.version } })
    versions.open = false
    await load()
  } catch (e: any) { error.value = e?.data?.error || t('files.error_rollback') }
}

// --- sharing ---
const share = reactive({
  open: false, node: null as Node | null, mode: 'user' as 'user' | 'link',
  email: '', access: 'read', expiresDays: '', link: '', busy: false, copied: false, list: [] as any[],
})
async function copyShareLink() {
  try {
    await navigator.clipboard.writeText(share.link)
    share.copied = true
    setTimeout(() => { share.copied = false }, 1500)
  } catch { /* clipboard not available */ }
}
function openShare(n: Node) {
  Object.assign(share, { open: true, node: n, mode: 'user', email: '', access: 'read', expiresDays: '', link: '', list: [] })
  loadNodeShares(n)
}
async function loadNodeShares(n: Node) {
  try { share.list = await request<any[]>(`/files/${n.id}/shares`) }
  catch { share.list = [] }
}
async function revokeShare(shareId: string) {
  error.value = ''
  try {
    await request(`/shares/${shareId}`, { method: 'DELETE' })
    if (share.node) await loadNodeShares(share.node)
  } catch (e: any) { error.value = e?.data?.error || t('files.error_revoke') }
}
async function submitShare() {
  if (!share.node) return
  error.value = ''; share.busy = true
  try {
    const body: any = { access: share.access }
    const days = parseFloat(share.expiresDays)
    if (Number.isFinite(days) && days > 0) body.expires_in_seconds = Math.round(days * 86400)
    if (share.mode === 'link') body.link = true
    else body.email = share.email
    const res = await request<{ token?: string }>(`/files/${share.node.id}/share`, { method: 'POST', body })
    await loadNodeShares(share.node)
    if (share.mode === 'link' && res.token) {
      share.link = `${location.origin}/s/${res.token}`
    } else { share.open = false }
  } catch (e: any) { error.value = e?.data?.error || t('files.error_share') }
  finally { share.busy = false }
}

useModalEscape(computed(() => versions.open), () => { versions.open = false })
useModalEscape(computed(() => share.open), () => { share.open = false })
useModalEscape(computed(() => vaultOpen.value), () => { vaultOpen.value = false })
</script>

<template>
  <div>
    <div class="mb-4 flex flex-wrap items-center justify-between gap-3">
      <nav class="flex items-center gap-1 text-sm">
        <template v-for="(c, i) in stack" :key="i">
          <Icon v-if="i > 0" name="lucide:chevron-right" size="14" class="text-muted" />
          <button
            class="rounded px-1.5 py-0.5 hover:bg-ink/5"
            :class="i === stack.length - 1 ? 'text-ink' : 'text-muted'"
            @click="crumbTo(i)"
          >{{ c.name }}</button>
        </template>
      </nav>
      <div class="flex items-center gap-2">
        <input v-model="newFolder" class="input w-40" :placeholder="t('files.new_folder_placeholder')" @keyup.enter="createFolder" />
        <button class="btn-ghost" :title="t('files.create_folder')" @click="createFolder"><Icon name="lucide:folder-plus" size="18" /></button>
        <button class="btn-ghost" :title="t('files.upload_files')" @click="fileInput?.click()">
          <Icon name="lucide:upload" size="18" />
        </button>
        <button class="btn-ghost" :title="t('files.upload_folder')" @click="folderInput?.click()">
          <Icon name="lucide:folder-up" size="18" />
        </button>
        <input ref="fileInput" type="file" multiple class="hidden" @change="onUpload" />
        <input ref="folderInput" type="file" webkitdirectory class="hidden" @change="onUploadFolder" />
      </div>
    </div>

    <p v-if="error" class="mb-4 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <!-- encrypted vault banner -->
    <div
      v-if="isVaultListing(nodes)"
      class="mb-4 flex items-center gap-3 rounded-xl border border-accent/30 bg-accent/5 px-4 py-3"
    >
      <Icon name="lucide:shield-check" size="20" class="shrink-0 text-accent" />
      <div class="flex-1 text-sm">
        <span class="font-medium text-ink">🔒 {{ t('files.vault_label') }}</span>
        <span class="ml-2 text-muted">{{ t('files.vault_desc') }}</span>
      </div>
      <button class="btn-accent shrink-0" @click="vaultOpen = true">
        <Icon name="lucide:lock-open" size="16" />
        {{ t('files.vault_unlock') }}
      </button>
    </div>

    <div
      class="card relative overflow-hidden"
      @dragenter.prevent="onDragEnter"
      @dragover.prevent
      @dragleave.prevent="onDragLeave"
      @drop.prevent="onDrop"
    >
      <div
        v-if="dragActive"
        class="pointer-events-none absolute inset-0 z-10 flex items-center justify-center rounded-xl border-2 border-dashed border-accent bg-accent/10"
      >
        <div class="flex flex-col items-center gap-2 text-sm font-medium text-accent">
          <Icon name="lucide:upload-cloud" size="32" />
          {{ t('files.drop_hint') }}
        </div>
      </div>

      <div v-if="busy && !nodes.length" class="p-6 text-sm text-muted">{{ t('common.loading') }}</div>
      <div v-else-if="!sorted.length" class="p-10 text-center text-sm text-muted">
        <Icon name="lucide:folder-open" size="28" class="mx-auto mb-2 block opacity-50" /> {{ t('files.folder_empty') }}
      </div>
      <table v-else class="w-full text-sm">
        <tbody>
          <tr v-for="n in sorted" :key="n.id" class="group border-b border-line/50 last:border-0 hover:bg-ink/5">
            <td class="w-8 py-2.5 pl-4">
              <Icon :name="n.is_dir ? 'lucide:folder' : 'lucide:file'" :class="n.is_dir ? 'text-accent' : 'text-muted'" size="18" />
            </td>
            <td class="py-2.5">
              <button v-if="n.is_dir" class="hover:underline" @click="open(n)">{{ n.name }}</button>
              <button
                v-else-if="isPlayable(n)"
                class="group/play inline-flex items-center gap-1.5 hover:text-accent hover:underline"
                :title="t('files.btn_play')"
                @click="play(n)"
              >
                {{ n.name }}
                <Icon name="lucide:play" size="12" class="opacity-0 transition group-hover/play:opacity-100" />
              </button>
              <button
                v-else
                class="text-left hover:underline"
                :class="n.mime && (n.mime.startsWith('audio/') || n.mime.startsWith('video/')) ? 'opacity-50' : ''"
                @click="openPreview(n)"
              >{{ n.name }}</button>
            </td>
            <td class="whitespace-nowrap py-2.5 text-right text-xs text-muted">{{ n.is_dir ? '' : formatBytes(n.size) }}</td>
            <td class="py-2.5 pr-4">
              <div class="ml-auto flex w-20 flex-wrap justify-end gap-1 opacity-100 transition md:w-auto md:flex-nowrap md:opacity-0 md:group-hover:opacity-100">
                <button v-if="!n.is_dir" class="btn-ghost px-2 py-1" :title="t('files.btn_download')" @click="download(n)">
                  <Icon name="lucide:download" size="16" />
                </button>
                <button v-if="!n.is_dir" class="btn-ghost px-2 py-1" :title="t('files.btn_versions')" @click="openVersions(n)">
                  <Icon name="lucide:history" size="16" />
                </button>
                <button class="btn-ghost px-2 py-1" :title="t('files.btn_share')" @click="openShare(n)">
                  <Icon name="lucide:share-2" size="16" />
                </button>
                <button class="btn-ghost px-2 py-1" :title="t('files.btn_rename')" @click="rename(n)">
                  <Icon name="lucide:pencil" size="16" />
                </button>
                <button class="btn-danger px-2 py-1" :title="t('files.btn_delete')" @click="remove(n)">
                  <Icon name="lucide:trash-2" size="16" />
                </button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <!-- versions modal -->
    <div v-if="versions.open" class="fixed inset-0 z-20 flex items-center justify-center bg-black/50 p-4" @click.self="versions.open = false">
      <div class="card w-full max-w-md p-5">
        <div class="mb-4 flex items-center justify-between">
          <h2 class="font-semibold">{{ t('files.versions_title', { name: versions.node?.name }) }}</h2>
          <button class="btn-ghost px-1.5 py-1" @click="versions.open = false"><Icon name="lucide:x" size="18" /></button>
        </div>
        <div v-if="versions.busy" class="text-sm text-muted">{{ t('common.loading') }}</div>
        <table v-else class="w-full text-sm">
          <tbody>
            <tr v-for="v in versions.list" :key="v.version" class="border-b border-line/50 last:border-0">
              <td class="py-2">
                v{{ v.version }}
                <span v-if="v.is_conflict_loser" class="ml-1 rounded bg-ink/5 px-1 py-0.5 text-[10px] text-muted">{{ t('files.version_conflict') }}</span>
              </td>
              <td class="py-2 text-right text-xs text-muted">{{ formatBytes(v.size) }}</td>
              <td class="py-2 pr-1 text-right">
                <button class="btn-ghost px-2 py-1" :title="t('files.btn_rollback')" @click="rollback(v)">
                  <Icon name="lucide:rotate-ccw" size="15" /> {{ t('files.btn_rollback') }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <!-- share modal -->
    <div v-if="share.open" class="fixed inset-0 z-20 flex items-center justify-center bg-black/50 p-4" @click.self="share.open = false">
      <div class="card w-full max-w-md p-5">
        <div class="mb-4 flex items-center justify-between">
          <h2 class="font-semibold">{{ t('files.share_title', { name: share.node?.name }) }}</h2>
          <button class="btn-ghost px-1.5 py-1" @click="share.open = false"><Icon name="lucide:x" size="18" /></button>
        </div>

        <div v-if="share.list.length" class="mb-3 space-y-1">
          <div class="text-xs text-muted">{{ t('files.share_current_access') }}</div>
          <div v-for="sh in share.list" :key="sh.share_id" class="flex items-center justify-between rounded-md border border-line bg-panel2 px-2 py-1.5 text-xs">
            <span>
              <Icon :name="sh.kind === 'link' ? 'lucide:link' : 'lucide:user'" size="13" class="mr-1 inline" />
              {{ sh.kind === 'link' ? t('files.share_link_label') : sh.email }}
              · {{ sh.access === 'read_write' ? t('files.share_access_read_write') : t('files.share_access_read') }}
              <span v-if="sh.expires_at"> · {{ t('common.loading') }} {{ new Date(sh.expires_at).toLocaleDateString() }}</span>
            </span>
            <button class="btn-ghost px-1.5 py-0.5" :title="t('common.revoke')" @click="revokeShare(sh.share_id)">
              <Icon name="lucide:x" size="14" />
            </button>
          </div>
        </div>

        <div class="mb-3 flex gap-2">
          <button class="btn" :class="share.mode === 'user' ? 'btn-accent' : 'btn-ghost'" @click="share.mode = 'user'">{{ t('files.share_to_user') }}</button>
          <button class="btn" :class="share.mode === 'link' ? 'btn-accent' : 'btn-ghost'" @click="share.mode = 'link'">{{ t('files.share_by_link') }}</button>
        </div>

        <div class="space-y-3">
          <input v-if="share.mode === 'user'" v-model="share.email" type="email" class="input" :placeholder="t('files.share_email_placeholder')" />
          <input v-model="share.expiresDays" type="number" min="0" class="input" :placeholder="t('files.share_expires_placeholder')" />

          <div v-if="share.link" class="rounded-md border border-line bg-panel2 p-2 text-xs">
            <div class="mb-1 text-muted">{{ t('files.share_download_link') }}</div>
            <div class="flex items-center gap-2">
              <span class="min-w-0 flex-1 break-all font-mono text-accent">{{ share.link }}</span>
              <button class="btn-ghost shrink-0 px-2 py-1" :title="t('common.copy')" @click="copyShareLink">
                <Icon :name="share.copied ? 'lucide:check' : 'lucide:copy'" size="16" />
              </button>
            </div>
          </div>

          <button class="btn-accent w-full justify-center" :disabled="share.busy || (share.mode === 'user' && !share.email)" @click="submitShare">
            <Icon v-if="share.busy" name="lucide:loader-circle" class="animate-spin" size="18" />
            <Icon v-else name="lucide:check" size="18" />
            {{ share.mode === 'link' ? t('files.share_create_link') : t('files.share_grant_access') }}
          </button>
        </div>
      </div>
    </div>
    <!-- file preview -->
    <PreviewModal
      v-if="preview.open"
      :items="previewItems"
      :start-index="preview.index"
      @close="preview.open = false"
    />

    <!-- VaultBrowser -->
    <VaultBrowser
      v-if="vaultOpen"
      :folder="currentFolderNode"
      @close="vaultOpen = false"
    />
  </div>
</template>
