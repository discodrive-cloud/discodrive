<script setup lang="ts">
import { normalizePairCode } from '~/lib/pairCode'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const { request } = useApi()
const code = computed(() => (route.query.code as string) || '')
const typed = ref('')
const info = ref<{ proposed_name: string; kind: string } | null>(null)
const name = ref('')
const state = ref<'enter' | 'loading' | 'ready' | 'done' | 'error'>('loading')
const error = ref('')

// Opened from a device link, the code is in ?code=; opened from Settings, it is typed in.
async function load() {
  if (!code.value) { state.value = 'enter'; return }
  state.value = 'loading'
  try {
    info.value = await request(`/pair/${encodeURIComponent(code.value)}`)
    name.value = info.value!.proposed_name
    state.value = 'ready'
  } catch (e: any) {
    state.value = 'error'
    error.value = e?.response?.status === 404 ? t('pair.error_not_found') : t('pair.error_load')
  }
}

onMounted(load)
watch(code, load)

function enterAnother() {
  typed.value = ''
  error.value = ''
  const { code: _, ...rest } = route.query
  router.replace({ query: rest })
}

function submitCode() {
  const c = normalizePairCode(typed.value)
  if (!c) { error.value = t('pair.error_code_format'); return }
  error.value = ''
  router.replace({ query: { ...route.query, code: c } })
}

async function approve() {
  try {
    await request(`/pair/${encodeURIComponent(code.value)}/approve`, { method: 'POST', body: { name: name.value } })
    state.value = 'done'
  } catch (e: any) {
    state.value = 'error'
    error.value = e?.response?.status === 409 ? t('pair.error_already')
      : e?.response?.status === 410 ? t('pair.error_expired')
      : t('pair.error_pair')
  }
}
</script>

<template>
  <div>
    <h1 class="mb-4 text-xl font-semibold">{{ t('pair.title') }}</h1>

    <form v-if="state === 'enter'" class="card max-w-sm p-5" @submit.prevent="submitCode">
      <p class="mb-4 text-sm">{{ t('pair.enter_hint') }}</p>
      <div class="mb-3">
        <label class="mb-1 block text-xs text-muted">{{ t('pair.code') }}</label>
        <input
          v-model="typed" type="text" class="input font-mono uppercase tracking-wider" placeholder="ABCD-EFGH"
          autocomplete="off" autocapitalize="characters" spellcheck="false" autofocus
        />
      </div>
      <p v-if="error" class="mb-3 flex items-center gap-2 text-xs text-danger">
        <Icon name="lucide:triangle-alert" size="14" /> {{ error }}
      </p>
      <button type="submit" class="btn-accent">
        <Icon name="lucide:arrow-right" size="16" /> {{ t('pair.btn_next') }}
      </button>
    </form>

    <p v-else-if="state === 'loading'" class="text-sm text-muted">{{ t('pair.loading') }}</p>

    <div v-else-if="state === 'ready'" class="card max-w-sm p-5">
      <p class="mb-4 text-sm">{{ t('pair.question') }}</p>
      <div class="mb-3">
        <label class="mb-1 block text-xs text-muted">{{ t('pair.device_name') }}</label>
        <input v-model="name" type="text" class="input" :placeholder="t('pair.device_name_ph')" />
      </div>
      <p class="mb-4 text-xs text-muted">{{ t('pair.type', { kind: info?.kind }) }}</p>
      <div class="flex gap-2">
        <button class="btn-accent" @click="approve">
          <Icon name="lucide:link" size="16" /> {{ t('pair.btn_pair') }}
        </button>
        <NuxtLink to="/files">
          <button class="btn-ghost">{{ t('pair.btn_cancel') }}</button>
        </NuxtLink>
      </div>
    </div>

    <div v-else-if="state === 'done'" class="card max-w-sm p-5">
      <p class="flex items-center gap-2 text-sm text-accent">
        <Icon name="lucide:check-circle" size="18" /> {{ t('pair.done') }}
      </p>
    </div>

    <div v-else>
      <p class="mb-3 flex items-center gap-2 text-sm text-danger">
        <Icon name="lucide:triangle-alert" size="16" /> {{ error }}
      </p>
      <button class="btn-ghost" @click="enterAnother">{{ t('pair.btn_other_code') }}</button>
    </div>
  </div>
</template>
