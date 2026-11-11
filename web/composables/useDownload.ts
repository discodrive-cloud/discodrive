import { saveLink } from '~/lib/saveLink'

// useDownload saves a server file without reading it into the page. The API takes the
// session in a header a link cannot carry, so the server first answers with a link good
// for that one path (POST /me/download-url); the browser downloads from it directly.
// Reading the file as a blob instead would hold all of it in the tab's memory — a few
// gigabytes of video are enough to kill the tab on a small device.
export function useDownload() {
  const { request } = useApi()
  return async function download(path: string, name: string) {
    const { url } = await request<{ url: string }>('/me/download-url', { method: 'POST', body: { path } })
    saveLink(url, name)
  }
}
