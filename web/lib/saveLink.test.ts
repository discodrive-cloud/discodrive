// @vitest-environment jsdom
import { describe, expect, it, vi } from 'vitest'
import { saveLink } from './saveLink'

describe('saveLink', () => {
  it('clicks a download link for the url and leaves nothing behind', () => {
    const clicked: { href: string; download: string }[] = []
    const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push({ href: this.getAttribute('href') || '', download: this.download })
    })
    saveLink('/files/1/content?dl=abc', 'отчёт 2026.pdf')
    click.mockRestore()
    expect(clicked).toEqual([{ href: '/files/1/content?dl=abc', download: 'отчёт 2026.pdf' }])
    expect(document.querySelectorAll('a').length).toBe(0)
  })
})
