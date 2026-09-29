import { expect, it } from 'vitest'
import { safeRedirect } from './safeRedirect'

it('keeps in-app paths', () => {
  expect(safeRedirect('/pair?code=ABC-123')).toBe('/pair?code=ABC-123')
  expect(safeRedirect('/files')).toBe('/files')
  expect(safeRedirect(['/music', '/x'])).toBe('/music')
})

it('rejects external, protocol-relative and malformed targets', () => {
  for (const bad of [
    'https://evil.test/', '//evil.test/x', '/\\evil.test', '\\\\evil.test', 'javascript:alert(1)',
    'files', '', '/ok\n//evil.test', '/a\\b', undefined, null, 42,
  ]) {
    expect(safeRedirect(bad)).toBeNull()
  }
})
