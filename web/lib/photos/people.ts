// People in the gallery: small pure helpers of the «Люди» tab.

export interface NamedPerson { id: string; name: string }

const norm = (s: string) => s.trim().toLocaleLowerCase()

// findSameName returns another person already called name (case and spaces aside): the
// tab offers to merge the two instead of keeping two people with one name.
export function findSameName<T extends NamedPerson>(people: T[], name: string, selfId: string): T | undefined {
  const n = norm(name)
  if (!n) return undefined
  return people.find((p) => p.id !== selfId && norm(p.name) === n)
}

// personLabel is what a person is called on screen.
export function personLabel(p: { name: string }, unnamed: string): string {
  return p.name.trim() || unnamed
}

// ageLabel words an age in the locale ("6 лет", "1 год", "2 years"): forms are the
// locale's plural categories (Intl.PluralRules) with {n}; a missing category uses other.
export function ageLabel(n: number, locale: string, forms: Partial<Record<Intl.LDMLPluralRule, string>>): string {
  const cat = new Intl.PluralRules(locale).select(n)
  return (forms[cat] ?? forms.other ?? '{n}').replace('{n}', String(n))
}

// pickPeople is the selection after a click on id in the list ids (in screen order): a
// plain click toggles that person, a shift-click adds everyone from the last clicked one
// (anchor) to it, the way file managers do.
export function pickPeople(sel: Set<string>, ids: string[], id: string, anchor: string | null, shift: boolean): Set<string> {
  const out = new Set(sel)
  const from = shift && anchor ? ids.indexOf(anchor) : -1
  const to = ids.indexOf(id)
  if (from < 0 || to < 0) {
    if (out.has(id)) out.delete(id)
    else out.add(id)
    return out
  }
  for (let i = Math.min(from, to); i <= Math.max(from, to); i++) out.add(ids[i])
  return out
}
