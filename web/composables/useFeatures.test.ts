import { describe, expect, it } from 'vitest'
import { sectionOfRoute } from './useFeatures'

describe('sectionOfRoute', () => {
  it('maps section pages, and nothing else', () => {
    expect(sectionOfRoute('/music')).toBe('music')
    expect(sectionOfRoute('/books')).toBe('books')
    expect(sectionOfRoute('/books-edit')).toBe('books')
    expect(sectionOfRoute('/photos')).toBe('photos')
    expect(sectionOfRoute('/files')).toBeNull()
    expect(sectionOfRoute('/musical')).toBeNull()
    expect(sectionOfRoute('/player')).toBeNull() // the Files player stays
  })
})
