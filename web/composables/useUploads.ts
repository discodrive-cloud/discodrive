// Chunked upload manager on top of /upload/* (init→chunk→complete + abort).
// The reactive task list lives in useState (for the panel); File objects and
// pause/cancel flags are in a module-level Map (outside reactivity).
import { backoffDelay, sleep } from '../lib/backoff'

export interface UploadTask {
  id: string
  name: string
  parentId: string | null
  total: number
  sent: number
  status: 'queued' | 'uploading' | 'paused' | 'done' | 'error' | 'canceled'
  uploadId?: string
  error?: string
}

const CHUNK = 8 * 1024 * 1024
const MAX_CONCURRENT = 3
const MAX_RETRIES = 3 // transient network errors per chunk before giving up

interface Ctl { file: File; paused: boolean; canceled: boolean }
const controls = new Map<string, Ctl>()

export const useUploadTasks = () => useState<UploadTask[]>('uploadTasks', () => [])
// completion tick — files.vue reloads the listing whenever this changes
export const useUploadsTick = () => useState<number>('uploadsTick', () => 0)

let localSeq = 0

export function useUploads() {
  const tasks = useUploadTasks()
  const tick = useUploadsTick()
  const storageTick = useStorageTick()
  const { request } = useApi()

  function schedule() {
    const active = tasks.value.filter((t) => t.status === 'uploading').length
    let free = MAX_CONCURRENT - active
    for (const t of tasks.value) {
      if (free <= 0) break
      if (t.status === 'queued') {
        free--
        void runTask(t)
      }
    }
  }

  async function runTask(t: UploadTask) {
    const ctl = controls.get(t.id)
    if (!ctl) return
    t.status = 'uploading'
    try {
      if (!t.uploadId) {
        const init = await request<{ upload_id: string }>('/upload/init', {
          method: 'POST',
          // size lets the server reject a Complete whose chunks don't add up to the
          // whole file, instead of publishing a short upload as if it were finished.
          // modified_at keeps the file's own date instead of dating it "just now".
          body: {
            parent_id: t.parentId,
            name: t.name,
            size: t.total,
            modified_at: ctl.file.lastModified ? new Date(ctl.file.lastModified).toISOString() : undefined,
          },
        })
        t.uploadId = init.upload_id
      }
      let n = Math.floor(t.sent / CHUNK)
      let retries = 0
      do {
        if (ctl.canceled) return
        if (ctl.paused) {
          t.status = 'paused'
          return
        }
        const start = n * CHUNK
        const blob = ctl.file.slice(start, Math.min(start + CHUNK, t.total))
        try {
          const res = await request<{ next_chunk: number }>(`/upload/${t.uploadId}/chunk/${n}`, {
            method: 'PUT',
            body: blob,
          })
          n = res.next_chunk
          retries = 0
        } catch (err: any) {
          const status = err?.response?.status
          if (status === 409 && typeof err?.data?.next_chunk === 'number') {
            // server already accepted this chunk — sync to next_chunk and continue
            n = err.data.next_chunk
            retries = 0
          } else if (status === 404) {
            // upload session is gone (server restarted) — resume is not possible
            throw new Error('upload interrupted (session lost), please start over')
          } else if (status === 507) {
            // out of storage: retrying cannot help, and the server's message says
            // how much room is left — surface it instead of hammering the chunk.
            throw err
          } else if (retries < MAX_RETRIES) {
            // transient network error: back off, check status and retry the same chunk
            retries++
            await sleep(backoffDelay(retries))
            if (ctl.canceled) return
            try {
              const st = await request<{ next_chunk: number }>(`/upload/${t.uploadId}`)
              n = st.next_chunk
            } catch {
              /* status unavailable — retry with current n */
            }
          } else {
            throw err
          }
        }
        t.sent = Math.min(n * CHUNK, t.total)
      } while (t.sent < t.total)

      await request(`/upload/${t.uploadId}/complete`, { method: 'POST' })
      t.status = 'done'
      controls.delete(t.id)
      tick.value++
      storageTick.value++ // useApi skips the chunk PUTs; the finished file is what counts
    } catch (e: any) {
      if (ctl.canceled) return
      t.status = 'error'
      t.error = e?.data?.error || e?.message || 'upload error'
    } finally {
      schedule()
    }
  }

  // enqueue adds files (with a pre-known parentId) to the upload queue.
  function enqueue(items: { file: File; parentId: string | null; name?: string }[]) {
    for (const it of items) {
      const id = `u${++localSeq}`
      controls.set(id, { file: it.file, paused: false, canceled: false })
      tasks.value.push({
        id,
        name: it.name ?? it.file.name,
        parentId: it.parentId,
        total: it.file.size,
        sent: 0,
        status: 'queued',
      })
    }
    schedule()
  }

  function pause(id: string) {
    const c = controls.get(id)
    if (c) c.paused = true
  }
  function resume(id: string) {
    const c = controls.get(id)
    const t = tasks.value.find((x) => x.id === id)
    if (c && t && (t.status === 'paused' || t.status === 'error')) {
      c.paused = false
      t.status = 'queued'
      t.error = undefined
      schedule()
    }
  }
  async function cancel(id: string) {
    const c = controls.get(id)
    const t = tasks.value.find((x) => x.id === id)
    if (c) c.canceled = true
    if (t) {
      t.status = 'canceled'
      if (t.uploadId) {
        try {
          await request(`/upload/${t.uploadId}`, { method: 'DELETE' })
        } catch {
          /* abort is idempotent */
        }
      }
    }
    controls.delete(id)
    schedule()
  }
  function clearFinished() {
    tasks.value = tasks.value.filter((t) => t.status !== 'done' && t.status !== 'canceled')
  }

  // ensureFolder: create a folder or return the id of an existing one (for folder uploads).
  async function ensureFolder(parentId: string | null, name: string): Promise<string> {
    try {
      const n = await request<{ id: string }>('/files/folder', {
        method: 'POST',
        body: { parent_id: parentId, name },
      })
      return n.id
    } catch (e: any) {
      if (e?.response?.status === 409) {
        const q = parentId ? `?parent_id=${parentId}` : ''
        const kids = await request<{ id: string; name: string; is_dir: boolean }[]>(`/files${q}`)
        const found = kids.find((k) => k.is_dir && k.name === name)
        if (found) return found.id
      }
      throw e
    }
  }

  // enqueueEntries recursively walks FileSystemEntry[] (from drag-drop webkitGetAsEntry):
  // files → enqueue with parentId; directories → ensureFolder + recurse. Preserves nesting.
  async function enqueueEntries(entries: FileSystemEntry[], rootParentId: string | null) {
    const files: { file: File; parentId: string | null }[] = []
    async function walk(entry: FileSystemEntry, parentId: string | null) {
      if (entry.isFile) {
        const f = await new Promise<File>((res, rej) => (entry as FileSystemFileEntry).file(res, rej))
        files.push({ file: f, parentId })
      } else if (entry.isDirectory) {
        const id = await ensureFolder(parentId, entry.name)
        const reader = (entry as FileSystemDirectoryEntry).createReader()
        // readEntries returns results in batches — loop until the batch is empty
        let batch: FileSystemEntry[]
        do {
          batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej))
          for (const e of batch) await walk(e, id)
        } while (batch.length > 0)
      }
    }
    for (const e of entries) await walk(e, rootParentId)
    if (files.length) enqueue(files)
  }

  // enqueueFolder: builds the folder tree from webkitRelativePath, then enqueues files.
  async function enqueueFolder(files: File[], rootParentId: string | null) {
    const dirCache = new Map<string, string | null>()
    dirCache.set('', rootParentId)
    const ensurePath = async (dir: string): Promise<string | null> => {
      if (dirCache.has(dir)) return dirCache.get(dir)!
      const parts = dir.split('/')
      const name = parts.pop() as string
      const parent = await ensurePath(parts.join('/'))
      const id = await ensureFolder(parent, name)
      dirCache.set(dir, id)
      return id
    }
    const items: { file: File; parentId: string | null; name?: string }[] = []
    for (const f of files) {
      const rel = (f as any).webkitRelativePath || f.name
      const segs = rel.split('/')
      const fname = segs.pop() as string
      const parentId = await ensurePath(segs.join('/'))
      items.push({ file: f, parentId, name: fname })
    }
    enqueue(items)
  }

  return { tasks, enqueue, enqueueFolder, enqueueEntries, pause, resume, cancel, clearFinished }
}
