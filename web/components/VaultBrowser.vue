<script setup lang="ts">
import type { Node, VaultEntry } from '~/composables/useVault'
import type { PreviewItem } from '~/lib/preview/types'
import { WrongPasswordError } from '~/lib/cryptomator/index.js'

const props = defineProps<{ folder: Node }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const vault = useVault()
const { keys, dirStack, entries, unlock, enter, breadcrumbTo, lock } = vault

// --- unlock ---
const password = ref('')
const unlockError = ref('')
const unlocking = ref(false)
const countdown = ref(0)
let countdownTimer: ReturnType<typeof setInterval> | null = null

function startCountdown(until: number) {
  countdown.value = Math.ceil((until - Date.now()) / 1000)
  if (countdownTimer) clearInterval(countdownTimer)
  countdownTimer = setInterval(() => {
    countdown.value = Math.max(0, Math.ceil((until - Date.now()) / 1000))
    if (countdown.value === 0 && countdownTimer) {
      clearInterval(countdownTimer)
      countdownTimer = null
    }
  }, 1000)
}

// Keys and decrypted names live only as long as this dialog: closing it locks the
// vault, and a different vault folder never shows the previous one's listing.
if (vault.vaultFolderId.value !== props.folder.id) lock()
watch(() => props.folder.id, () => lock())

onBeforeUnmount(() => {
  if (countdownTimer) clearInterval(countdownTimer)
  lock()
})

async function doUnlock() {
  if (unlocking.value) return
  unlocking.value = true
  unlockError.value = ''
  try {
    await unlock(props.folder, password.value)
    password.value = ''
  } catch (e: any) {
    if (e instanceof WrongPasswordError) {
      unlockError.value = t('vault.error_wrong_password')
      if (import.meta.client) {
        const raw = localStorage.getItem(`kf_vault_lock_${props.folder.id}`)
        if (raw) {
          const rs = JSON.parse(raw)
          if (rs.lockUntil > Date.now()) { unlockError.value = ''; startCountdown(rs.lockUntil) }
        }
      }
    } else if (e?.message?.includes('Too many attempts')) {
      const raw = import.meta.client ? localStorage.getItem(`kf_vault_lock_${props.folder.id}`) : null
      if (raw) {
        const rs = JSON.parse(raw)
        startCountdown(rs.lockUntil)
      } else {
        unlockError.value = e.message
      }
    } else {
      unlockError.value = e?.message || t('vault.error_unlock')
    }
  } finally {
    unlocking.value = false
  }
}

// --- file preview ---
// The shared PreviewModal works on bytes; here the source is decrypted vault
// content, so files stay a fully client-side path (no plaintext leaves memory).
// Vault entries carry no plaintext size, so the text cap can't apply (size: null).
const preview = reactive({ open: false, index: 0 })
const previewFiles = computed(() => entries.value.filter((e) => !e.isDir))
const previewItems = computed<PreviewItem[]>(() =>
  previewFiles.value.map((e) => ({
    name: e.name,
    size: null,
    load: async () => new Blob([(await vault.openFile(e)).bytes as BlobPart]),
  })),
)

async function openEntry(entry: VaultEntry) {
  if (entry.isDir) { await enter(entry); return }
  const i = previewFiles.value.indexOf(entry)
  if (i < 0) return
  preview.index = i
  preview.open = true
}

function doLock() { lock() }

const isUnlockOpen = computed(() => !keys.value)
const isPreviewOpen = computed(() => preview.open)

// The outer vault modal must NOT close on Escape while the preview is open —
// PreviewModal handles that Escape itself (its listener is registered later,
// so these guards still see the preview as open for the same key press).
useModalEscape(computed(() => isUnlockOpen.value && !isPreviewOpen.value), () => emit('close'))
useModalEscape(computed(() => !!keys.value && !isPreviewOpen.value), () => emit('close'))
</script>

<template>
  <div class="fixed inset-0 z-20 flex items-center justify-center bg-black/50 p-4" @click.self="emit('close')">

    <!-- UNLOCK MODAL -->
    <div v-if="!keys" class="card w-full max-w-sm p-6">
      <div class="mb-4 flex items-center justify-between">
        <h2 class="flex items-center gap-2 font-semibold">
          <Icon name="lucide:lock" size="18" class="text-accent" />
          {{ t('vault.title') }}
        </h2>
        <button class="btn-ghost px-1.5 py-1" @click="emit('close')">
          <Icon name="lucide:x" size="18" />
        </button>
      </div>

      <p class="mb-4 text-sm text-muted">{{ folder.name }}</p>

      <!-- rate limit -->
      <div v-if="countdown > 0" class="mb-4 rounded-md bg-danger/10 px-3 py-2.5 text-sm text-danger">
        <Icon name="lucide:clock" size="15" class="mr-1 inline" />
        {{ t('vault.rate_limit') }}
        <span class="font-mono font-semibold">{{ Math.floor(countdown / 60) }}:{{ String(countdown % 60).padStart(2, '0') }}</span>
      </div>

      <div v-else class="space-y-3">
        <input
          v-model="password"
          type="password"
          class="input"
          :placeholder="t('vault.password_ph')"
          autofocus
          :disabled="unlocking"
          @keyup.enter="doUnlock"
        />

        <p v-if="unlockError" class="flex items-center gap-1.5 text-sm text-danger">
          <Icon name="lucide:triangle-alert" size="15" />
          {{ unlockError }}
        </p>

        <button class="btn-accent w-full justify-center" :disabled="unlocking || !password" @click="doUnlock">
          <Icon v-if="unlocking" name="lucide:loader-circle" class="animate-spin" size="18" />
          <Icon v-else name="lucide:unlock" size="18" />
          {{ t('vault.btn_unlock') }}
        </button>
      </div>

      <p class="mt-5 text-[11px] leading-relaxed text-muted/70">
        {{ t('vault.security_note') }}
      </p>
    </div>

    <!-- VAULT BROWSER -->
    <div v-else class="card flex w-full max-w-2xl flex-col" style="max-height: 85vh">
      <div class="flex items-center justify-between border-b border-line px-5 py-3.5">
        <nav class="flex flex-wrap items-center gap-1 text-sm min-w-0">
          <template v-for="(crumb, i) in dirStack" :key="i">
            <Icon v-if="i > 0" name="lucide:chevron-right" size="14" class="shrink-0 text-muted" />
            <button
              class="rounded px-1.5 py-0.5 hover:bg-ink/5 truncate max-w-[160px]"
              :class="i === dirStack.length - 1 ? 'text-ink' : 'text-muted'"
              :title="crumb.name"
              @click="breadcrumbTo(i)"
            >{{ crumb.name }}</button>
          </template>
        </nav>

        <div class="flex shrink-0 items-center gap-1 pl-3">
          <button class="btn-ghost px-2 py-1 text-xs" @click="doLock">
            <Icon name="lucide:lock" size="15" />
            {{ t('vault.btn_lock') }}
          </button>
          <button class="btn-ghost px-1.5 py-1" @click="emit('close')">
            <Icon name="lucide:x" size="18" />
          </button>
        </div>
      </div>

      <div class="overflow-y-auto">
        <div v-if="!entries.length" class="p-10 text-center text-sm text-muted">
          <Icon name="lucide:folder-open" size="28" class="mx-auto mb-2 block opacity-50" />
          {{ t('vault.folder_empty') }}
        </div>
        <table v-else class="w-full text-sm">
          <tbody>
            <tr
              v-for="entry in entries"
              :key="entry.name + (entry.dirId ?? entry.nodeId ?? '')"
              class="group border-b border-line/50 last:border-0 hover:bg-ink/5"
            >
              <td class="w-8 py-2.5 pl-4">
                <Icon
                  :name="entry.isDir ? 'lucide:folder' : 'lucide:file'"
                  :class="entry.isDir ? 'text-accent' : 'text-muted'"
                  size="18"
                />
              </td>
              <td class="py-2.5 pr-4">
                <button class="hover:underline text-left w-full" @click="openEntry(entry)">
                  {{ entry.name }}
                </button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  </div>

  <!-- FILE PREVIEW (shared modal, fed with decrypted bytes) -->
  <PreviewModal
    v-if="preview.open"
    :items="previewItems"
    :start-index="preview.index"
    @close="preview.open = false"
  />
</template>
