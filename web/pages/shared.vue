<script setup lang="ts">
interface Share { share_id: string; resource_id: string; name: string; is_dir: boolean; access: string }
interface Node { id: string; name: string; is_dir: boolean; size: number | null; version: number }

const { t } = useI18n()
const { request } = useApi()
const error = ref('')
const stack = ref<{ id: string; name: string }[]>([{ id: '', name: t('shared.root_name') }])
const atRoot = computed(() => stack.value.length === 1)
const shares = ref<Share[]>([])
const nodes = ref<Node[]>([])

// Only the latest load may fill the list (see files.vue).
let loadSeq = 0
async function load() {
  const seq = ++loadSeq
  error.value = ''
  try {
    if (atRoot.value) {
      const rows = await request<Share[]>('/shared')
      if (seq === loadSeq) shares.value = rows
    } else {
      const pid = stack.value[stack.value.length - 1].id
      const rows = await request<Node[]>(`/files?parent_id=${pid}`)
      if (seq === loadSeq) nodes.value = rows
    }
  } catch (e: any) {
    if (seq === loadSeq) error.value = e?.data?.error || t('shared.error_load')
  }
}
onMounted(load)

function openShare(s: Share) {
  if (!s.is_dir) return
  if (stack.value[stack.value.length - 1]?.id === s.resource_id) return
  stack.value.push({ id: s.resource_id, name: s.name })
  load()
}
function openNode(n: Node) {
  if (!n.is_dir) return
  if (stack.value[stack.value.length - 1]?.id === n.id) return
  stack.value.push({ id: n.id, name: n.name })
  load()
}
function crumbTo(i: number) { stack.value = stack.value.slice(0, i + 1); load() }

async function download(id: string, name: string) {
  try {
    const blob = await request<Blob>(`/files/${id}/content`, { responseType: 'blob' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url; a.download = name; a.click()
    URL.revokeObjectURL(url)
  } catch (e: any) { error.value = e?.data?.error || t('common.error_load') }
}

async function leave(s: Share) {
  error.value = ''
  try {
    await request(`/shared/${s.share_id}`, { method: 'DELETE' })
    await load()
  } catch (e: any) { error.value = e?.data?.error || t('shared.error_leave') }
}
</script>

<template>
  <div>
    <nav class="mb-4 flex items-center gap-1 text-sm">
      <template v-for="(c, i) in stack" :key="i">
        <Icon v-if="i > 0" name="lucide:chevron-right" size="14" class="text-muted" />
        <button class="rounded px-1.5 py-0.5 hover:bg-ink/5"
                :class="i === stack.length - 1 ? 'text-ink' : 'text-muted'"
                @click="crumbTo(i)">{{ c.name }}</button>
      </template>
    </nav>

    <p v-if="error" class="mb-4 flex items-center gap-2 text-sm text-danger">
      <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
    </p>

    <div class="card overflow-hidden">
      <template v-if="atRoot">
        <div v-if="!shares.length" class="p-10 text-center text-sm text-muted">
          <Icon name="lucide:share-2" size="28" class="mx-auto mb-2 block opacity-50" /> {{ t('shared.nothing') }}
        </div>
        <table v-else class="w-full text-sm">
          <tbody>
            <tr v-for="s in shares" :key="s.share_id" class="group border-b border-line/50 last:border-0 hover:bg-ink/5">
              <td class="w-8 py-2.5 pl-4">
                <Icon :name="s.is_dir ? 'lucide:folder' : 'lucide:file'" :class="s.is_dir ? 'text-accent' : 'text-muted'" size="18" />
              </td>
              <td class="py-2.5">
                <button v-if="s.is_dir" class="hover:underline" @click="openShare(s)">{{ s.name }}</button>
                <span v-else>{{ s.name }}</span>
              </td>
              <td class="py-2.5 text-xs text-muted">{{ s.access === 'read_write' ? t('shared.access_read_write') : t('shared.access_read') }}</td>
              <td class="py-2.5 pr-4">
                <div class="flex justify-end gap-1 opacity-0 transition group-hover:opacity-100">
                  <button v-if="!s.is_dir" class="btn-ghost px-2 py-1" :title="t('shared.btn_download')" @click="download(s.resource_id, s.name)">
                    <Icon name="lucide:download" size="16" />
                  </button>
                  <button class="btn-ghost px-2 py-1" :title="t('shared.btn_leave')" @click="leave(s)">
                    <Icon name="lucide:eye-off" size="16" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </template>

      <template v-else>
        <div v-if="!nodes.length" class="p-10 text-center text-sm text-muted">
          <Icon name="lucide:folder-open" size="28" class="mx-auto mb-2 block opacity-50" /> {{ t('shared.folder_empty') }}
        </div>
        <table v-else class="w-full text-sm">
          <tbody>
            <tr v-for="n in nodes" :key="n.id" class="group border-b border-line/50 last:border-0 hover:bg-ink/5">
              <td class="w-8 py-2.5 pl-4">
                <Icon :name="n.is_dir ? 'lucide:folder' : 'lucide:file'" :class="n.is_dir ? 'text-accent' : 'text-muted'" size="18" />
              </td>
              <td class="py-2.5">
                <button v-if="n.is_dir" class="hover:underline" @click="openNode(n)">{{ n.name }}</button>
                <span v-else>{{ n.name }}</span>
              </td>
              <td class="py-2.5 text-right text-xs text-muted">{{ n.is_dir ? '' : formatBytes(n.size) }}</td>
              <td class="py-2.5 pr-4">
                <div class="flex justify-end opacity-0 transition group-hover:opacity-100">
                  <button v-if="!n.is_dir" class="btn-ghost px-2 py-1" :title="t('shared.btn_download')" @click="download(n.id, n.name)">
                    <Icon name="lucide:download" size="16" />
                  </button>
                </div>
              </td>
            </tr>
          </tbody>
        </table>
      </template>
    </div>
  </div>
</template>
