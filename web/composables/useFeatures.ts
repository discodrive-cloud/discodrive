// Optional sections the server offers at all (MUSIC_ENABLED, BOOKS_ENABLED,
// PHOTOS_ENABLED). A section switched off there is neither shown nor can be turned on.
export interface Features { music: boolean; books: boolean; photos: boolean }

export const useFeatures = () => useState<Features>('features', () => ({ music: true, books: true, photos: true }))

let loading: Promise<Features> | null = null

// loadFeatures asks the server once per page load; a failed request is retried next
// time and leaves everything on (the server still refuses a section that is off).
export function loadFeatures(): Promise<Features> {
  const features = useFeatures()
  const sess = useSession()
  if (!sess.value.token) return Promise.resolve(features.value)
  if (!loading) {
    loading = apiFetch<Features>('/me/features', { headers: { Authorization: `Bearer ${sess.value.token}` } })
      .then((f) => (features.value = f))
      .catch(() => {
        loading = null
        return features.value
      })
  }
  return loading
}

// sectionOfRoute names the section a page belongs to.
export function sectionOfRoute(path: string): keyof Features | null {
  if (path === '/music' || path.startsWith('/music/')) return 'music'
  if (path === '/books' || path.startsWith('/books/') || path === '/books-edit') return 'books'
  if (path === '/photos' || path.startsWith('/photos/')) return 'photos'
  return null
}
