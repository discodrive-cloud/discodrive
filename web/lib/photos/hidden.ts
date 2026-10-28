// The unlock token of the gallery's Hidden part. It lives in this tab's memory only (never
// localStorage): a reload, a logout or its expiry locks the part again.
import { ref, computed } from 'vue'

interface HiddenToken {
  token: string
  expiresAt: number // ms since epoch
}

const state = ref<HiddenToken | null>(null)
let timer: ReturnType<typeof setTimeout> | null = null

// Re-unlock a little before the server would refuse the token.
const MARGIN_MS = 5_000

export const photosHiddenUnlocked = computed(() => state.value !== null)

export function photosHiddenToken(now = Date.now()): string | null {
  const s = state.value
  if (!s) return null
  if (s.expiresAt - MARGIN_MS <= now) {
    lockPhotosHidden()
    return null
  }
  return s.token
}

// photosHiddenHeaders: the header that opens hidden cards, or nothing while locked.
export function photosHiddenHeaders(): Record<string, string> {
  const tok = photosHiddenToken()
  return tok ? { 'X-Photos-Hidden': tok } : {}
}

export function setPhotosHiddenToken(token: string, expiresAt: string | number, now = Date.now()) {
  const exp = typeof expiresAt === 'number' ? expiresAt : Date.parse(expiresAt)
  if (!token || !Number.isFinite(exp) || exp - MARGIN_MS <= now) {
    lockPhotosHidden()
    return
  }
  state.value = { token, expiresAt: exp }
  if (timer) clearTimeout(timer)
  timer = setTimeout(lockPhotosHidden, exp - MARGIN_MS - now)
}

export function lockPhotosHidden() {
  if (timer) clearTimeout(timer)
  timer = null
  state.value = null
}
