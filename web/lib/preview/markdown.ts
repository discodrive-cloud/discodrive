// Lazy markdown renderer for note previews and the saved-articles reader.
// Safety model: html:false — raw HTML in the source is escaped and shown as
// text, so the rendered output can go through v-html without a sanitizer.
// Obsidian-style [[wikilinks]] render as inert styled text. Images: notes
// render placeholders (the modal can't resolve attachments in v1), while the
// articles reader opts into <img> tags via {allowImages: true}. Article images
// are external, and the CSP (img-src 'self' data: blob:) rightly blocks them, so
// the tag carries the address in data-ext-src and no src: the browser requests
// nothing until hydrateExternalImages loads it through the server as a blob.
// ![[embeds]] stay placeholders always.
import type MarkdownIt from 'markdown-it'

export interface MarkdownOptions {
  allowImages?: boolean
}

export function configureMarkdown(md: MarkdownIt, opts: MarkdownOptions = {}): MarkdownIt {
  // [[wikilink]] and ![[embed]] — must run before the standard link rule.
  md.inline.ruler.before('link', 'wikilink', (state, silent) => {
    const src = state.src
    let pos = state.pos
    const isEmbed = src.charCodeAt(pos) === 0x21 /* ! */
    if (isEmbed) pos++
    if (src.charCodeAt(pos) !== 0x5b /* [ */ || src.charCodeAt(pos + 1) !== 0x5b) return false
    const end = src.indexOf(']]', pos + 2)
    if (end < 0) return false
    const inner = src.slice(pos + 2, end)
    if (!inner || inner.includes('\n') || inner.includes('[')) return false
    if (!silent) {
      const token = state.push(isEmbed ? 'wikiembed' : 'wikilink', '', 0)
      token.content = inner
    }
    state.pos = end + 2
    return true
  })
  md.renderer.rules.wikilink = (tokens, idx) =>
    `<span class="md-wikilink">${md.utils.escapeHtml(tokens[idx].content)}</span>`
  md.renderer.rules.wikiembed = (tokens, idx) => imgPlaceholder(md, tokens[idx].content)
  md.renderer.rules.image = (tokens, idx) => {
    const t = tokens[idx]
    if (opts.allowImages) {
      const src = t.attrGet('src') || ''
      const alt = t.content || ''
      return `<img data-ext-src="${md.utils.escapeHtml(src)}" alt="${md.utils.escapeHtml(alt)}" class="md-ext-img">`
    }
    return imgPlaceholder(md, t.content || t.attrGet('src') || '')
  }

  const defaultLink =
    md.renderer.rules.link_open ??
    ((tokens, idx, options, _env, self) => self.renderToken(tokens, idx, options))
  md.renderer.rules.link_open = (tokens, idx, options, env, self) => {
    tokens[idx].attrSet('target', '_blank')
    tokens[idx].attrSet('rel', 'noopener')
    return defaultLink(tokens, idx, options, env, self)
  }
  return md
}

// Loads the external images of rendered article HTML (img[data-ext-src]) once they
// scroll near the viewport. `load` fetches an absolute http(s) address through the
// server and returns an object URL; relative addresses resolve against `base` (the
// article's own URL). data:image/… sources are set as they are (the CSP allows them);
// anything else stays unloaded, its alt text showing. The returned function stops
// pending loads and revokes the object URLs it created.
export function hydrateExternalImages(
  root: ParentNode,
  base: string,
  load: (url: string) => Promise<string>,
): () => void {
  const created: string[] = []
  let disposed = false
  const fetchInto = async (img: HTMLImageElement, url: string) => {
    try {
      const obj = await load(url)
      if (disposed) {
        URL.revokeObjectURL(obj)
        return
      }
      created.push(obj)
      img.src = obj
    } catch {
      img.classList.add('md-ext-img-failed')
    }
  }
  const pending = new Map<Element, string>()
  const observer =
    typeof IntersectionObserver === 'undefined'
      ? null
      : new IntersectionObserver(
          (entries) => {
            for (const e of entries) {
              const url = pending.get(e.target)
              if (!e.isIntersecting || url === undefined) continue
              pending.delete(e.target)
              observer?.unobserve(e.target)
              void fetchInto(e.target as HTMLImageElement, url)
            }
          },
          { rootMargin: '400px' },
        )
  for (const img of Array.from(root.querySelectorAll<HTMLImageElement>('img[data-ext-src]'))) {
    const raw = img.getAttribute('data-ext-src') || ''
    img.removeAttribute('data-ext-src')
    if (/^data:image\//i.test(raw)) {
      img.src = raw
      continue
    }
    let url: URL
    try {
      url = new URL(raw, base)
    } catch {
      continue
    }
    if (url.protocol !== 'http:' && url.protocol !== 'https:') continue
    if (observer) {
      pending.set(img, url.href)
      observer.observe(img)
    } else {
      void fetchInto(img, url.href)
    }
  }
  return () => {
    disposed = true
    observer?.disconnect()
    for (const u of created) URL.revokeObjectURL(u)
    created.length = 0
  }
}

function imgPlaceholder(md: MarkdownIt, name: string): string {
  return `<span class="md-img-placeholder">${md.utils.escapeHtml(name)}</span>`
}

// One cached instance per image mode (the renderer rules differ).
const mdPromises: Record<string, Promise<MarkdownIt> | undefined> = {}

export async function renderMarkdown(src: string, opts: MarkdownOptions = {}): Promise<string> {
  const key = opts.allowImages ? 'img' : 'plain'
  // default preset: tables and strikethrough are already on; html stays OFF.
  mdPromises[key] ??= import('markdown-it').then(
    (m) => configureMarkdown(new m.default({ html: false, linkify: true }), opts),
  )
  return (await mdPromises[key]!).render(src)
}
