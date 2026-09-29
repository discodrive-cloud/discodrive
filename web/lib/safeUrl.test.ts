import { expect, it } from 'vitest'
import { isHttpUrl } from './safeUrl'

it('accepts only http(s) URLs', () => {
  expect(isHttpUrl('https://example.com/a')).toBe(true)
  expect(isHttpUrl('HTTP://example.com')).toBe(true)
  for (const bad of ['javascript:alert(1)', ' javascript:alert(1)', 'JaVaScRiPt:alert(1)', 'data:text/html,<script>1</script>', 'about:blank', '//evil.test', '', null, undefined]) {
    expect(isHttpUrl(bad as any)).toBe(false)
  }
})
