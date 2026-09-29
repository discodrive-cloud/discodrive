// @vitest-environment jsdom
import { describe, it, expect, vi } from 'vitest'
import MarkdownIt from 'markdown-it'
import { configureMarkdown, hydrateExternalImages } from './markdown'

const mdImg = configureMarkdown(new MarkdownIt({ html: false, linkify: true }), { allowImages: true })
const flush = () => new Promise((r) => setTimeout(r, 0))

function render(src: string): HTMLElement {
  const root = document.createElement('article')
  root.innerHTML = mdImg.render(src)
  return root
}

describe('hydrateExternalImages (Pocket reader)', () => {
  it('loads article images through the given loader as object URLs, never directly', async () => {
    const root = render('![a](https://cdn.example/a.png)\n\n![b](/img/b.jpg)')
    const load = vi.fn(async (url: string) => `blob:test/${encodeURIComponent(url)}`)
    const revoke = vi.fn()
    URL.revokeObjectURL = revoke // jsdom has no object URLs
    const dispose = hydrateExternalImages(root, 'https://news.example/post/1', load)
    await flush()
    expect(load.mock.calls.map((c) => c[0])).toEqual([
      'https://cdn.example/a.png',
      'https://news.example/img/b.jpg', // relative → against the article URL
    ])
    const srcs = Array.from(root.querySelectorAll('img')).map((i) => i.getAttribute('src'))
    expect(srcs.every((s) => s?.startsWith('blob:test/'))).toBe(true)
    dispose()
    expect(revoke).toHaveBeenCalledTimes(2)
  })

  it('never loads non-web addresses; data:image is set as is', async () => {
    const root = document.createElement('article')
    root.innerHTML =
      '<img data-ext-src="javascript:alert(1)" alt="x">' +
      '<img data-ext-src="file:///etc/passwd" alt="y">' +
      '<img data-ext-src="data:image/png;base64,iVBORw0KGgo=" alt="z">'
    const load = vi.fn(async () => 'blob:x')
    hydrateExternalImages(root, 'https://news.example/', load)
    await flush()
    expect(load).not.toHaveBeenCalled()
    const imgs = root.querySelectorAll('img')
    expect(imgs[0].getAttribute('src')).toBeNull()
    expect(imgs[1].getAttribute('src')).toBeNull()
    expect(imgs[2].getAttribute('src')).toBe('data:image/png;base64,iVBORw0KGgo=')
  })

  it('marks a failed image and keeps its alt text', async () => {
    const root = render('![подпись](https://cdn.example/gone.png)')
    hydrateExternalImages(root, 'https://news.example/', async () => {
      throw new Error('502')
    })
    await flush()
    const img = root.querySelector('img')!
    expect(img.getAttribute('src')).toBeNull()
    expect(img.classList.contains('md-ext-img-failed')).toBe(true)
    expect(img.getAttribute('alt')).toBe('подпись')
  })
})
