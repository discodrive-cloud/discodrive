/**
 * useVault.ts — state and navigation for a Cryptomator vault (read-only).
 * Keys live in memory only (ref) and are never sent to the server.
 */
import { mapConcurrent } from '../lib/mapConcurrent'
import {
  openVault,
  WrongPasswordError,
  decryptName,
  dirIdHash,
  decryptContent,
} from '../lib/cryptomator/index.js'
import type { VaultKeys } from '../lib/cryptomator/index.js'

export interface Node {
  id: string
  name: string
  is_dir: boolean
  size: number | null
  version: number
}

export interface VaultEntry {
  name: string
  isDir: boolean
  /** for directories — plaintext dirId (contents of dir.c9r) */
  dirId?: string
  /** for files — node id (.c9r or contents.c9r) used for downloading */
  nodeId?: string
}

interface RateLimitState {
  fails: number
  lockUntil: number
}

function getRateState(vaultFolderId: string): RateLimitState {
  if (!import.meta.client) return { fails: 0, lockUntil: 0 }
  try {
    const raw = localStorage.getItem(`kf_vault_lock_${vaultFolderId}`)
    if (raw) return JSON.parse(raw) as RateLimitState
  } catch {}
  return { fails: 0, lockUntil: 0 }
}

function setRateState(vaultFolderId: string, s: RateLimitState) {
  if (!import.meta.client) return
  localStorage.setItem(`kf_vault_lock_${vaultFolderId}`, JSON.stringify(s))
}

function clearRateState(vaultFolderId: string) {
  if (!import.meta.client) return
  localStorage.removeItem(`kf_vault_lock_${vaultFolderId}`)
}

export function useVault() {
  const { request } = useApi()

  const keys = useState<VaultKeys | null>('vault_keys', () => null)
  const vaultFolderId = useState<string>('vault_folder_id', () => '')
  // breadcrumbs: [{dirId, name}], dirId='' for the vault root
  const dirStack = useState<{ dirId: string; name: string }[]>('vault_dir_stack', () => [])
  const entries = useState<VaultEntry[]>('vault_entries', () => [])

  /** Returns true if nodes contains vault.cryptomator — the marker for a vault folder */
  function isVaultListing(nodes: Node[]): boolean {
    return nodes.some((n) => !n.is_dir && n.name === 'vault.cryptomator')
  }

  /** Download node content as text */
  async function fetchText(nodeId: string): Promise<string> {
    const blob = await request<Blob>(`/files/${nodeId}/content`, { responseType: 'blob' })
    return blob.text()
  }

  /** Download node content as Uint8Array */
  async function fetchBytes(nodeId: string): Promise<Uint8Array> {
    const blob = await request<Blob>(`/files/${nodeId}/content`, { responseType: 'blob' })
    const ab = await blob.arrayBuffer()
    return new Uint8Array(ab)
  }

  /** List child nodes by parent_id */
  async function listNodes(parentId: string): Promise<Node[]> {
    const q = parentId ? `?parent_id=${parentId}` : ''
    return request<Node[]>(`/files${q}`)
  }

  /**
   * resolveStoragePath — traverse path segments "d/XX/YYYY…" starting from vaultFolderId.
   * Returns the id of the last folder in the chain.
   */
  async function resolveStoragePath(startFolderId: string, path: string, rootChildren?: Node[]): Promise<string> {
    const segments = path.split('/')
    let currentId = startFolderId
    for (const seg of segments) {
      const children = currentId === startFolderId && rootChildren ? rootChildren : await listNodes(currentId)
      const found = children.find((n) => n.name === seg && n.is_dir)
      if (!found) throw new Error(`Path segment "${seg}" not found`)
      currentId = found.id
    }
    return currentId
  }

  /**
   * listDir — populate entries for a vault dirId.
   * dirId='' means the vault root.
   */
  async function listDir(dirId: string, displayName: string, rootChildren?: Node[]): Promise<void> {
    if (!keys.value) throw new Error('Vault is locked')
    const k = keys.value
    const vfId = vaultFolderId.value

    const path = await dirIdHash(k, dirId)
    const leafFolderId = await resolveStoragePath(vfId, path, rootChildren)
    const rawEntries = await listNodes(leafFolderId)

    const result = await mapConcurrent(rawEntries, 6, async (child): Promise<VaultEntry | undefined> => {
      // skip internal metadata files
      if (child.name === 'dirid.c9r') return undefined

      if (child.name.endsWith('.c9r')) {
        if (!child.is_dir) {
          // file
          const plain = await decryptName(k, child.name, dirId)
          return { name: plain, isDir: false, nodeId: child.id }
        } else {
          // subdirectory: fetch dir.c9r to get subDirId
          const subChildren = await listNodes(child.id)
          const dirC9r = subChildren.find((c) => c.name === 'dir.c9r')
          if (!dirC9r) return undefined
          const plain = await decryptName(k, child.name, dirId)
          const subDirId = (await fetchText(dirC9r.id)).trim()
          return { name: plain, isDir: true, dirId: subDirId }
        }
      } else if (child.name.endsWith('.c9s') && child.is_dir) {
        // long name: fetch name.c9s to get the full encrypted name
        const subChildren = await listNodes(child.id)
        const nameC9s = subChildren.find((c) => c.name === 'name.c9s')
        if (!nameC9s) return undefined
        const fullEnc = (await fetchText(nameC9s.id)).trim()
        const plain = await decryptName(k, fullEnc, dirId)

        const dirC9r = subChildren.find((c) => c.name === 'dir.c9r')
        const contentsC9r = subChildren.find((c) => c.name === 'contents.c9r')

        if (dirC9r) {
          const subDirId = (await fetchText(dirC9r.id)).trim()
          return { name: plain, isDir: true, dirId: subDirId }
        } else if (contentsC9r) {
          return { name: plain, isDir: false, nodeId: contentsC9r.id }
        }
      }
    })

    entries.value = result.filter((entry): entry is VaultEntry => entry !== undefined)
  }

  /** Unlock the vault */
  async function unlock(vaultFolder: Node, password: string): Promise<void> {
    const folderId = vaultFolder.id

    // Rate limiting
    if (import.meta.client) {
      const rs = getRateState(folderId)
      if (rs.lockUntil > Date.now()) {
        const mins = Math.ceil((rs.lockUntil - Date.now()) / 60_000)
        throw new Error(`Too many attempts. Please wait ${mins} min.`)
      }
    }

    // Locate vault.cryptomator and masterkey.cryptomator
    const children = await listNodes(folderId)
    const vaultNode = children.find((n) => !n.is_dir && n.name === 'vault.cryptomator')
    const masterkeyNode = children.find((n) => !n.is_dir && n.name === 'masterkey.cryptomator')
    if (!vaultNode || !masterkeyNode) throw new Error('Vault configuration files not found')

    const [vaultJwt, masterkeyJson] = await Promise.all([
      fetchText(vaultNode.id), fetchText(masterkeyNode.id),
    ])

    try {
      const k = await openVault(masterkeyJson, vaultJwt, password)
      // Success: reset rate-limit state
      clearRateState(folderId)
      keys.value = k
      vaultFolderId.value = folderId
      dirStack.value = [{ dirId: '', name: vaultFolder.name }]
      await listDir('', vaultFolder.name, children)
    } catch (e) {
      if (e instanceof WrongPasswordError) {
        if (import.meta.client) {
          const rs = getRateState(folderId)
          rs.fails++
          if (rs.fails >= 5) {
            rs.lockUntil = Date.now() + 15 * 60_000
            rs.fails = 0
          }
          setRateState(folderId, rs)
        }
        throw e
      }
      throw e
    }
  }

  /** Navigate into a subdirectory or open a file */
  async function enter(entry: VaultEntry): Promise<{ name: string; bytes: Uint8Array } | null> {
    if (entry.isDir && entry.dirId !== undefined) {
      dirStack.value.push({ dirId: entry.dirId, name: entry.name })
      await listDir(entry.dirId, entry.name)
      return null
    } else if (!entry.isDir) {
      return openFile(entry)
    }
    return null
  }

  /** Navigate to a breadcrumb entry by index */
  async function breadcrumbTo(index: number): Promise<void> {
    dirStack.value = dirStack.value.slice(0, index + 1)
    const crumb = dirStack.value[index]
    await listDir(crumb.dirId, crumb.name)
  }

  /** Download and decrypt a file */
  async function openFile(entry: VaultEntry): Promise<{ name: string; bytes: Uint8Array }> {
    if (!keys.value) throw new Error('Vault is locked')
    if (!entry.nodeId) throw new Error('Entry has no nodeId')
    const raw = await fetchBytes(entry.nodeId)
    const plain = await decryptContent(keys.value, raw)
    return { name: entry.name, bytes: plain }
  }

  /** Lock the vault — clear keys and state */
  function lock(): void {
    keys.value = null
    vaultFolderId.value = ''
    dirStack.value = []
    entries.value = []
  }

  return {
    keys,
    vaultFolderId,
    dirStack,
    entries,
    isVaultListing,
    unlock,
    listDir,
    enter,
    breadcrumbTo,
    openFile,
    lock,
  }
}
