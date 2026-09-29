import { describe, expect, it } from 'vitest'
import { isHttpUrl, withScheme } from './safeUrl'

it('accepts only http(s) URLs', () => {
  expect(isHttpUrl('https://example.com/a')).toBe(true)
  expect(isHttpUrl('HTTP://example.com')).toBe(true)
  for (const bad of ['javascript:alert(1)', ' javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,<script>1</script>', 'about:blank', '//evil.test', '', null, undefined]) {
    expect(isHttpUrl(bad as any)).toBe(false)
  }
})

describe('withScheme', () => {
  it('adds https to a bare address', () => {
    expect(withScheme('example.com/page')).toBe('https://example.com/page')
    expect(withScheme('  //example.com ')).toBe('https://example.com')
  })
  it('keeps an explicit scheme', () => {
    expect(withScheme('http://example.com')).toBe('http://example.com')
    expect(withScheme('javascript:alert(1)')).toBe('javascript:alert(1)')
  })
  it('leaves an empty value empty', () => {
    expect(withScheme('   ')).toBe('')
  })
})
