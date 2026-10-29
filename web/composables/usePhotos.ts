// Gallery client: types of the /me/photos/v1 API, the shared thumbnail loader and the
// "gallery enabled" flag the sidebar follows.
import { ThumbLoader, ThumbError, type ThumbLoaderOptions } from '~/lib/photos/thumbLoader'
import type { Bucket } from '~/lib/photos/layout'
import { clustersQuery, type BBox, type Cluster } from '~/lib/photos/mapClusters'
import { photosHiddenHeaders, lockPhotosHidden, setPhotosHiddenToken } from '~/lib/photos/hidden'
import { emptyResult, mergeResult, failChunk, wallDate, type EditResult } from '~/lib/photos/edit'
import type { RequestOptions } from '~/composables/useApi'

export const PHOTOS_API = '/me/photos/v1'

export interface PhotoRow {
  id: string
  takenAt: string
  tzOffsetMin: number | null
  kind: 'photo' | 'video'
  w: number | null
  h: number | null
  durationMs: number | null
  live: boolean
  edited: boolean
  favorite: boolean
  thumb: string
  place: string | null
  // false: a partner's card or an album's — look, download, put in an album; nothing more
  own?: boolean
}

export interface PhotoSource { id: string; name: string; path: string }

// GET assets/{id}: the card with everything the viewer and the info panel show.
export interface PhotoAsset extends PhotoRow {
  dateSource: string
  videoCodec: string | null
  isHdr: boolean
  country: string | null // ISO code: countryName() names it
  lat: number | null
  lon: number | null
  camera: {
    make: string | null
    model: string | null
    lens: string | null
    fNumber: number | null
    focalMm: number | null
    exposure: string | null
    iso: number | null
  }
  // nodeId and the folder only on one's own cards: where a partner keeps a file is theirs
  file: { name: string; size: number | null; nodeId?: string; folderPath?: string; folderId?: string | null }
  liveVideoId: string | null
  originalId: string | null
  hidden: boolean
  copies: number
  owner?: string | null // the owner's email on somebody else's card
}

// Where the viewer gets a card, its video and its pictures: the gallery API by default, an
// album link on the public page.
export interface PhotoViewSource {
  asset(id: string): Promise<PhotoAsset>
  play(id: string, hevc: boolean): Promise<PlayInfo>
  loader(opts?: ThumbLoaderOptions): ThumbLoader
}

export interface PhotoPartner { userId: string; email: string; inFeed?: boolean }
export interface PhotoPartners { sharing: PhotoPartner[]; sharedWithMe: PhotoPartner[] }

export interface PhotoAlbum {
  id: string
  name: string
  own: boolean
  ownerEmail: string
  count: number
  members: number
  cover: PhotoRow | null
  updatedAt: string
}

export interface PhotoAlbumDetail {
  id: string
  name: string
  own: boolean
  ownerEmail: string
  members: PhotoPartner[]
  me: string
}

export interface PhotoAlbumLink {
  id: string
  token: string
  url: string
  hasPassword: boolean
  expiresAt: string | null
  allowDownload: boolean
  createdAt: string
}

export interface TrashRow extends PhotoRow { trashedAt: string }

export interface PlayInfo { url: string | null; ready: boolean; unsupported?: boolean }

// PUT assets/metadata body (ids aside).
export interface MetadataEdit {
  takenAt?: string
  tzOffsetMin?: number
  shiftMinutes?: number
  place?: { lat: number; lon: number } | null
}

export interface PhotoSettings {
  available: boolean
  enabled: boolean
  sharedWithMe?: boolean // a gallery or an album somebody opened to the user
  sources: PhotoSource[]
  indexing: { total: number; ready: number; failed: number; pending: number; scanning: boolean; failedFiles: number }
}

// GET failed: a file of the sources that could not be processed.
export interface PhotoFailedFile {
  id: string
  name: string
  folder: string
  error: string
}

export interface PhotoFilter {
  kind: 'all' | 'photo' | 'video' | 'live' | 'raw'
  favorite: boolean
  source: string // '' = every source
  area?: string // the map's cells, "z/x.y,…" (lib/photos/mapClusters)
  owner?: string // a partner's gallery instead of one's own
  album?: string // an album's cards
}

export interface MetadataResult {
  updated: string[]
  conflicts: string[]
  failed: { id: string; error: string }[]
  partial: string[]
  versionBytes: number
}

export type { Bucket }

// The Hidden part's unlock lives in lib/photos/hidden (module memory); useApi's logout
// calls this auto-imported wrapper.
export function lockHiddenPhotos() {
  lockPhotosHidden()
}

export function previewURL(row: Pick<PhotoRow, 'id' | 'thumb'>): string {
  return `${PHOTOS_API}/assets/${row.id}/preview?v=${encodeURIComponent(row.thumb)}`
}

// Server batch limits (internal/api/photos_manage.go).
export const PHOTO_BATCH = 1000
export const PHOTO_EDIT_BATCH = 200

export function chunk<T>(items: T[], size: number): T[][] {
  const out: T[][] = []
  for (let i = 0; i < items.length; i += size) out.push(items.slice(i, i + size))
  return out
}

export function filterQuery(f: PhotoFilter): string {
  const p = new URLSearchParams()
  if (f.kind === 'photo' || f.kind === 'video') p.set('kind', f.kind)
  if (f.kind === 'live') p.set('live', 'true')
  if (f.kind === 'raw') p.set('raw', 'true')
  if (f.favorite) p.set('favorite', 'true')
  if (f.source) p.set('source', f.source)
  if (f.area) p.set('area', f.area)
  if (f.owner) p.set('owner', f.owner)
  if (f.album) p.set('album', f.album)
  const s = p.toString()
  return s ? `?${s}` : ''
}

export function thumbURL(row: Pick<PhotoRow, 'id' | 'thumb'>): string {
  return `${PHOTOS_API}/assets/${row.id}/thumb?v=${encodeURIComponent(row.thumb)}`
}

export function thumbKey(row: Pick<PhotoRow, 'id' | 'thumb'>): string {
  return `${row.id}:${row.thumb}`
}

export function formatDuration(ms: number | null): string {
  if (ms == null) return ''
  const s = Math.round(ms / 1000)
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = String(s % 60).padStart(2, '0')
  return h ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`
}

// monthTitle: '2019-07' -> «Июль 2019» / "July 2019" (standalone month name).
// countryName names an ISO country code in the interface language (the code itself where
// the browser does not know it).
export function countryName(code: string | null | undefined, locale: string): string {
  if (!code) return ''
  try {
    return new Intl.DisplayNames([locale], { type: 'region' }).of(code) || code
  } catch {
    return code
  }
}

export function monthTitle(month: string, locale: string): string {
  const [y, m] = month.split('-').map(Number)
  if (!y || !m) return month
  let name = month
  try {
    name = new Intl.DateTimeFormat(locale, { month: 'long', timeZone: 'UTC' }).format(new Date(Date.UTC(y, m - 1, 15)))
  } catch { /* unknown locale */ }
  return `${name.charAt(0).toUpperCase()}${name.slice(1)} ${y}`
}

// takenLabel: «14 июля 2019, 19:42» in the photo's own wall-clock time.
export function takenLabel(row: Pick<PhotoRow, 'takenAt' | 'tzOffsetMin'>, locale: string, withTime = true): string {
  const d = wallDate(row.takenAt, row.tzOffsetMin)
  if (!Number.isFinite(d.getTime())) return ''
  try {
    return new Intl.DateTimeFormat(locale, {
      day: 'numeric', month: 'long', year: 'numeric', timeZone: 'UTC',
      ...(withTime ? { hour: '2-digit', minute: '2-digit' } : {}),
    }).format(d)
  } catch {
    return d.toISOString().slice(0, withTime ? 16 : 10).replace('T', ' ')
  }
}

// cameraName: "Apple iPhone XS" — the model alone when it already starts with the make.
export function cameraName(c: { make: string | null; model: string | null } | null | undefined): string {
  if (!c) return ''
  const make = c.make || ''
  const model = c.model || ''
  return make && model.toLowerCase().startsWith(make.toLowerCase()) ? model : [make, model].filter(Boolean).join(' ')
}

// Whether the server has the offline maps (cached for the page's lifetime; the info panel
// and the place dialog ask).
let mapsReadyOnce: Promise<boolean> | null = null
export function photosMapsReady(): Promise<boolean> {
  if (!mapsReadyOnce) {
    const { request } = useApi()
    mapsReadyOnce = request<{ ready: boolean }>(`${PHOTOS_API}/maps/status`).then((r) => !!r.ready, () => {
      mapsReadyOnce = null
      return false
    })
  }
  return mapsReadyOnce
}

// The sidebar shows «Фото» when this is true; the settings panel updates it on toggle.
export const usePhotosEnabled = () => useState<boolean>('photosEnabled', () => false)

// makeThumbLoader builds a loader that fetches with the session token (plus extra
// headers, e.g. the hidden-gallery token for the Hidden tab).
export function makeThumbLoader(extraHeaders: () => Record<string, string> = () => ({}), opts: ThumbLoaderOptions = {}): ThumbLoader {
  const sess = useSession()
  return new ThumbLoader(async (url, signal) => {
    const headers: Record<string, string> = { ...extraHeaders() }
    if (sess.value.token) headers.Authorization = `Bearer ${sess.value.token}`
    const res = await fetch(url, { headers, signal })
    if (!res.ok) throw new ThumbError(res.status)
    return res.blob()
  }, opts)
}

export function usePhotosApi() {
  const { request: send } = useApi()
  // Place names come in the interface language (the server names them per request).
  const i18n = useNuxtApp().$i18n
  const request = <T = any>(path: string, opts: RequestOptions = {}) =>
    send<T>(path, { ...opts, headers: { 'Accept-Language': unref(i18n.locale), ...(opts.headers || {}) } })
  // Every card request carries the Hidden unlock while there is one: hidden cards answer
  // 404 without it, everything else ignores it.
  const h = () => photosHiddenHeaders()

  return {
    settings: () => request<PhotoSettings>(`${PHOTOS_API}/settings`),
    saveSettings: (enabled: boolean, sourceIds: string[]) =>
      request<PhotoSettings>(`${PHOTOS_API}/settings`, { method: 'PUT', body: { enabled, sourceIds } }),
    failed: (offset = 0, limit = 200) =>
      request<{ items: PhotoFailedFile[]; more: boolean }>(`${PHOTOS_API}/failed?offset=${offset}&limit=${limit}`),
    buckets: (f: PhotoFilter) => request<Bucket[]>(`${PHOTOS_API}/buckets${filterQuery(f)}`),
    month: (month: string, f: PhotoFilter, signal?: AbortSignal) =>
      request<PhotoRow[]>(`${PHOTOS_API}/months/${month}${filterQuery(f)}`, { signal }),
    mapClusters: (z: number, box: BBox, signal?: AbortSignal) =>
      request<Cluster<PhotoRow>[]>(`${PHOTOS_API}/map/clusters?${clustersQuery(z, box)}`, { signal }),
    mapBounds: () => request<BBox | null>(`${PHOTOS_API}/map/bounds`),
    asset: (id: string) => request<PhotoAsset>(`${PHOTOS_API}/assets/${id}`, { headers: h() }),
    original: (id: string) => request<Blob>(`${PHOTOS_API}/assets/${id}/original`, { headers: h(), responseType: 'blob' }),
    partners: () => request<PhotoPartners>(`${PHOTOS_API}/partners`),
    addPartner: (email: string) => request<PhotoPartner>(`${PHOTOS_API}/partners`, { method: 'POST', body: { email } }),
    revokePartner: (userId: string) => request(`${PHOTOS_API}/partners/${userId}`, { method: 'DELETE' }),
    leavePartner: (userId: string) => request(`${PHOTOS_API}/partners/${userId}/received`, { method: 'DELETE' }),
    setPartnerFeed: (userId: string, inFeed: boolean) =>
      request(`${PHOTOS_API}/partners/${userId}/feed`, { method: 'PUT', body: { inFeed } }),
    albums: () => request<PhotoAlbum[]>(`${PHOTOS_API}/albums`),
    album: (id: string) => request<PhotoAlbumDetail>(`${PHOTOS_API}/albums/${id}`),
    createAlbum: (name: string, assetIds: string[] = []) =>
      request<{ id: string; name: string; added: number }>(`${PHOTOS_API}/albums`, { method: 'POST', body: { name, assetIds } }),
    renameAlbum: (id: string, name: string) => request(`${PHOTOS_API}/albums/${id}`, { method: 'PATCH', body: { name } }),
    // The owner deletes the album; a member leaves it.
    deleteAlbum: (id: string) => request(`${PHOTOS_API}/albums/${id}`, { method: 'DELETE' }),
    async addToAlbum(id: string, assetIds: string[]) {
      let added = 0
      for (const part of chunk(assetIds, PHOTO_BATCH)) {
        added += (await request<{ added: number }>(`${PHOTOS_API}/albums/${id}/items`, { method: 'POST', body: { assetIds: part } })).added
      }
      return added
    },
    async removeFromAlbum(id: string, assetIds: string[]) {
      const out = { removed: 0, kept: 0 }
      for (const part of chunk(assetIds, PHOTO_BATCH)) {
        const r = await request<{ removed: number; kept: number }>(`${PHOTOS_API}/albums/${id}/items/remove`, { method: 'POST', body: { assetIds: part } })
        out.removed += r.removed
        out.kept += r.kept
      }
      return out
    },
    addAlbumMember: (id: string, email: string) =>
      request<PhotoPartner>(`${PHOTOS_API}/albums/${id}/members`, { method: 'POST', body: { email } }),
    removeAlbumMember: (id: string, userId: string) => request(`${PHOTOS_API}/albums/${id}/members/${userId}`, { method: 'DELETE' }),
    albumLinks: (id: string) => request<PhotoAlbumLink[]>(`${PHOTOS_API}/albums/${id}/links`),
    createAlbumLink: (id: string, body: { password?: string; expiresAt?: string; allowDownload: boolean }) =>
      request<PhotoAlbumLink>(`${PHOTOS_API}/albums/${id}/links`, { method: 'POST', body }),
    deleteAlbumLink: (id: string, linkId: string) => request(`${PHOTOS_API}/albums/${id}/links/${linkId}`, { method: 'DELETE' }),
    async patch(ids: string[], patch: { favorite?: boolean; hidden?: boolean }) {
      for (const part of chunk(ids, PHOTO_BATCH)) {
        await request(`${PHOTOS_API}/assets`, { method: 'PATCH', body: { ids: part, ...patch }, headers: h() })
      }
    },
    async remove(ids: string[]) {
      for (const part of chunk(ids, PHOTO_BATCH)) {
        await request(`${PHOTOS_API}/assets/delete`, { method: 'POST', body: { ids: part }, headers: h() })
      }
    },
    play: (id: string, hevc: boolean) =>
      request<PlayInfo>(`${PHOTOS_API}/assets/${id}/play`, { method: 'POST', body: { hevc }, headers: h() }),
    // onThisDay: the same day of earlier years, by the browser's calendar.
    onThisDay(now = new Date()) {
      const md = `${String(now.getMonth() + 1).padStart(2, '0')}-${String(now.getDate()).padStart(2, '0')}`
      return request<{ year: number; assets: PhotoRow[] }[]>(`${PHOTOS_API}/on-this-day?date=${md}`)
    },
    hiddenBuckets: () => request<Bucket[]>(`${PHOTOS_API}/hidden/buckets`, { headers: h() }),
    hiddenMonth: (month: string, signal?: AbortSignal) =>
      request<PhotoRow[]>(`${PHOTOS_API}/hidden?month=${month}`, { headers: h(), signal }),
    // unlock trades an approval token (action photos:hidden) for the Hidden part's token.
    async unlock(approvalToken: string) {
      const res = await request<{ token: string; expiresAt: string }>(`${PHOTOS_API}/hidden/unlock`, {
        method: 'POST', body: { approvalToken }, keepSessionOn401: true,
      })
      setPhotosHiddenToken(res.token, res.expiresAt)
      return res
    },
    trash: () => request<TrashRow[]>(`${PHOTOS_API}/trash`, { headers: h() }),
    async restore(ids: string[]) {
      for (const part of chunk(ids, PHOTO_BATCH)) {
        await request(`${PHOTOS_API}/trash/restore`, { method: 'POST', body: { ids: part }, headers: h() })
      }
    },
    // metadata applies an edit in requests of PHOTO_EDIT_BATCH cards. A request that fails
    // as a whole (network, 5xx) marks its cards failed; the next chunks still go.
    async metadata(ids: string[], edit: MetadataEdit, onProgress?: (done: number, total: number) => void, signal?: AbortSignal): Promise<EditResult> {
      const result = emptyResult()
      let done = 0
      onProgress?.(0, ids.length)
      for (const part of chunk(ids, PHOTO_EDIT_BATCH)) {
        if (signal?.aborted) { failChunk(result, part, 'canceled'); continue }
        try {
          const r = await request<EditResult>(`${PHOTOS_API}/assets/metadata`, { method: 'PUT', body: { ids: part, ...edit }, headers: h() })
          mergeResult(result, r)
        } catch (e: any) {
          failChunk(result, part, e?.data?.error || e?.message || 'error')
        }
        done += part.length
        onProgress?.(done, ids.length)
      }
      return result
    },
  }
}

// The gallery's own source for the viewer: the user's cards and the ones shared with them.
export function galleryViewSource(): PhotoViewSource {
  const api = usePhotosApi()
  return {
    asset: api.asset,
    play: api.play,
    loader: (opts) => makeThumbLoader(photosHiddenHeaders, opts),
  }
}
