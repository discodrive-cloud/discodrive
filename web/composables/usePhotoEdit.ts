// Shared by the date and place dialogs: what an edit will cost in file versions, and the
// batch run with its progress and per-card summary.
import { ref } from 'vue'
import type { MetadataEdit, PhotoAsset } from '~/composables/usePhotos'
import { estimateVersionBytes, retryIds, type EditResult } from '../lib/photos/edit'
import { mapConcurrent } from '../lib/mapConcurrent'

// Above this many cards the sizes are not looked up one by one: a general warning instead.
export const ESTIMATE_LOOKUP_MAX = 50

export function usePhotoEdit(ids: () => string[]) {
  const api = usePhotosApi()

  // The cards, looked up for a moderate selection (sizes, names, the single card's values).
  const assets = ref<PhotoAsset[] | null>(null)
  const estimate = ref<number | null>(null) // bytes; null = unknown (general warning)
  const sidecars = ref(0) // cards that get an .xmp next to them instead
  const loadingCards = ref(false)

  async function loadCards() {
    const list = ids()
    if (list.length > ESTIMATE_LOOKUP_MAX) return
    loadingCards.value = true
    try {
      const found = await mapConcurrent(list, 6, async (id) => {
        try { return await api.asset(id) } catch { return null }
      })
      const ok = found.filter((a): a is PhotoAsset => !!a)
      assets.value = ok
      estimate.value = estimateVersionBytes(ok.map((a) => a.file))
      sidecars.value = ok.filter((a) => estimateVersionBytes([a.file]) === 0).length
    } finally {
      loadingCards.value = false
    }
  }

  const progress = ref<{ done: number; total: number } | null>(null)
  const result = ref<EditResult | null>(null)
  const running = ref(false)
  // The edit that produced `result`, fixed at submit: the summary and "retry" go by it,
  // never by what the dialog's controls say now (a shift must not be retried as a set).
  const lastEdit = ref<MetadataEdit | null>(null)
  const resultShift = ref(false)
  let ctrl: AbortController | null = null

  async function run(list: string[], edit: MetadataEdit): Promise<EditResult> {
    running.value = true
    result.value = null
    lastEdit.value = edit
    resultShift.value = edit.shiftMinutes !== undefined
    ctrl = new AbortController()
    try {
      const r = await api.metadata(list, edit, (done, total) => { progress.value = { done, total } }, ctrl.signal)
      result.value = r
      return r
    } finally {
      running.value = false
      progress.value = null
      ctrl = null
    }
  }

  // The cards "retry" sends: failed and conflicting ones, never partial ones of a shift.
  function retryList(): string[] {
    return result.value ? retryIds(result.value, resultShift.value) : []
  }

  // retry re-sends the same edit to retryList() (the ids are those, whatever the caller passes in).
  async function retry(): Promise<EditResult | null> {
    const ids = retryList()
    if (!lastEdit.value || !ids.length) return null
    return run(ids, lastEdit.value)
  }

  // stop: the chunks not sent yet are reported as failed ("canceled").
  function stop() {
    ctrl?.abort()
  }

  return { assets, estimate, sidecars, loadingCards, loadCards, progress, result, running, run, stop, lastEdit, resultShift, retryList, retry }
}
