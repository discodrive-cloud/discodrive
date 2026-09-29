import { describe, it, expect } from 'vitest'
import MarkdownIt from 'markdown-it'
import { configureMarkdown, renderMarkdown } from './markdown'

const md = configureMarkdown(new MarkdownIt({ html: false, linkify: true }))

describe('markdown preview renderer', () => {
  it('escapes raw HTML instead of rendering it (html:false)', () => {
    const out = md.render('hello <script>alert(1)</script> <img src=x onerror=alert(1)>')
    expect(out).not.toContain('<script>')
    expect(out).not.toContain('<img')
    expect(out).toContain('&lt;script&gt;')
  })

  it('renders [[wikilinks]] as inert styled text', () => {
    const out = md.render('см. [[Моя заметка]] и дальше')
    expect(out).toContain('<span class="md-wikilink">Моя заметка</span>')
    expect(out).not.toContain('<a')
  })

  it('escapes markup smuggled inside a wikilink', () => {
    const out = md.render('[[<b>x</b>]]')
    expect(out).not.toContain('<b>')
  })

  it('renders ![[embeds]] and ![alt](src) images as placeholders', () => {
    const out = md.render('![[attachment.png]]\n\n![диаграмма](https://evil.example/x.png)')
    expect(out).toContain('<span class="md-img-placeholder">attachment.png</span>')
    expect(out).toContain('<span class="md-img-placeholder">диаграмма</span>')
    expect(out).not.toContain('<img')
  })

  it('opens links in a new tab with noopener', () => {
    const out = md.render('[сайт](https://example.com)')
    expect(out).toContain('target="_blank"')
    expect(out).toContain('rel="noopener"')
  })

  it('linkifies bare URLs', () => {
    expect(md.render('см. https://example.com тут')).toContain('<a href="https://example.com"')
  })

  it('renders GFM tables and strikethrough', () => {
    const out = md.render('| a | b |\n|---|---|\n| 1 | 2 |\n\n~~зачёркнуто~~')
    expect(out).toContain('<table>')
    expect(out).toContain('<s>зачёркнуто</s>')
  })

  it('lazy renderMarkdown produces the same configured output', async () => {
    const out = await renderMarkdown('# Заголовок\n\n[[линк]]')
    expect(out).toContain('<h1>Заголовок</h1>')
    expect(out).toContain('md-wikilink')
  })
})

describe('markdown reader mode (allowImages)', () => {
  const mdImg = configureMarkdown(new MarkdownIt({ html: false, linkify: true }), { allowImages: true })

  it('renders inert images: the address in data-ext-src, no src the CSP would block', () => {
    const out = mdImg.render('![схема](https://example.com/pic.jpg)')
    expect(out).toContain('<img data-ext-src="https://example.com/pic.jpg"')
    expect(out).toContain('alt="схема"')
    expect(out).not.toMatch(/<img[^>]*\ssrc=/)
  })

  it('escapes hostile src/alt attributes', () => {
    const out = mdImg.render('![" onerror="alert(1)](https://x/"><script>)')
    expect(out).not.toContain('"><script>')
    expect(out).not.toContain('onerror="alert')
  })

  it('still escapes raw HTML and keeps ![[embeds]] as placeholders', () => {
    const out = mdImg.render('<img src=x onerror=alert(1)>\n\n![[attachment.png]]')
    expect(out).not.toContain('<img src=x')
    expect(out).toContain('&lt;img src=x onerror=alert(1)&gt;')
    expect(out).toContain('<span class="md-img-placeholder">attachment.png</span>')
  })

  it('default renderMarkdown still uses placeholders (cached per mode)', async () => {
    const withImg = await renderMarkdown('![a](https://x/p.png)', { allowImages: true })
    const plain = await renderMarkdown('![a](https://x/p.png)')
    expect(withImg).toContain('<img')
    expect(plain).not.toContain('<img')
    expect(plain).toContain('md-img-placeholder')
  })
})
