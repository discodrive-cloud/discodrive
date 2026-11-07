<script setup lang="ts">
// The server-made preview of a shared link, fetched as a blob through the authed
// endpoint (the CSP blocks third-party images). Falls back to a neutral tile.
const props = defineProps<{ id: string; hasThumb: boolean }>()

const { request } = useApi()
const url = ref('')

onMounted(async () => {
  if (!props.hasThumb) return
  try {
    const blob = await request<Blob>(`/me/links/${props.id}/thumb`, { responseType: 'blob' })
    url.value = URL.createObjectURL(blob)
  } catch { /* keep the tile */ }
})
onBeforeUnmount(() => { if (url.value) URL.revokeObjectURL(url.value) })
</script>

<template>
  <img v-if="url" :src="url" class="h-11 w-11 shrink-0 rounded-md object-cover" alt="" />
  <div v-else class="h-11 w-11 shrink-0 rounded-md bg-ink/5" />
</template>
