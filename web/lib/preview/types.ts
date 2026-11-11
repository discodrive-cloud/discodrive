// A preview gallery item. The byte source is the caller's concern: the file
// browser fetches /files/{id}/content as a blob, the vault hands over already
// decrypted in-memory bytes — the modal itself never talks to the API.
export interface PreviewItem {
  name: string
  size: number | null
  load: () => Promise<Blob>
  // The server path the file downloads from, when it is a plain server file (not a
  // vault entry decrypted in the page): "Download" then saves it without loading it.
  path?: string
}
