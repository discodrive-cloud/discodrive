// normalizePairCode turns what a person typed into the server's pairing code form
// (ABCD-EFGH): case, spaces and the dash are forgiven. Returns '' for anything else.
export function normalizePairCode(input: string): string {
  const s = input.toUpperCase().replace(/[\s-]/g, '')
  if (!/^[A-Z0-9]{8}$/.test(s)) return ''
  return `${s.slice(0, 4)}-${s.slice(4)}`
}
