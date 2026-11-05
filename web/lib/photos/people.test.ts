import { describe, expect, it } from 'vitest'
import { ageLabel, findSameName, personLabel, pickPeople } from './people'

const people = [
  { id: 'a', name: 'Аня' },
  { id: 'b', name: '' },
  { id: 'c', name: 'Борис' },
]

describe('findSameName', () => {
  it('finds another person with the name, ignoring case and spaces', () => {
    expect(findSameName(people, '  аня ', 'b')?.id).toBe('a')
  })
  it('does not offer the person itself', () => {
    expect(findSameName(people, 'Аня', 'a')).toBeUndefined()
  })
  it('never matches an empty name', () => {
    expect(findSameName(people, '   ', 'a')).toBeUndefined()
  })
})

describe('personLabel', () => {
  it('names an unnamed person', () => {
    expect(personLabel({ name: '' }, 'Без имени')).toBe('Без имени')
    expect(personLabel({ name: 'Аня' }, 'Без имени')).toBe('Аня')
  })
})

describe('ageLabel', () => {
  const ru = { one: '{n} год', few: '{n} года', many: '{n} лет', other: '{n} года' }
  it('picks the Russian form', () => {
    expect(ageLabel(1, 'ru', ru)).toBe('1 год')
    expect(ageLabel(3, 'ru', ru)).toBe('3 года')
    expect(ageLabel(6, 'ru', ru)).toBe('6 лет')
    expect(ageLabel(11, 'ru', ru)).toBe('11 лет')
    expect(ageLabel(21, 'ru', ru)).toBe('21 год')
    expect(ageLabel(0, 'ru', ru)).toBe('0 лет')
  })
  it('falls back to other', () => {
    expect(ageLabel(2, 'en', { one: '{n} year', other: '{n} years' })).toBe('2 years')
  })
})

describe('pickPeople', () => {
  const ids = ['a', 'b', 'c', 'd', 'e']
  it('toggles one person on a plain click', () => {
    expect([...pickPeople(new Set(['a']), ids, 'c', null, false)]).toEqual(['a', 'c'])
    expect([...pickPeople(new Set(['a', 'c']), ids, 'c', null, false)]).toEqual(['a'])
  })
  it('adds the run from the last clicked one on shift, both ways', () => {
    expect([...pickPeople(new Set(['b']), ids, 'd', 'b', true)].sort()).toEqual(['b', 'c', 'd'])
    expect([...pickPeople(new Set(), ids, 'a', 'c', true)].sort()).toEqual(['a', 'b', 'c'])
  })
  it('falls back to a toggle on shift without a known anchor', () => {
    expect([...pickPeople(new Set(), ids, 'c', 'gone', true)]).toEqual(['c'])
    expect([...pickPeople(new Set(), ids, 'c', null, true)]).toEqual(['c'])
  })
})
